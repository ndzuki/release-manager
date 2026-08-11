// Package bootstrap implements the operator agent identity bootstrap: it
// consumes a single-use enrollment token, generates an Ed25519 key and CSR,
// enrolls through the agent gateway, and durably persists the resulting
// identity so later restarts reconnect with the enrolled certificate instead
// of re-enrolling (REQ-015/REQ-044, TASK-075 plan v1 Step 6).
package bootstrap

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	operatorv1connect "github.com/ndzuki/release-manager/api/gen/operator/v1/operatorv1connect"
	"github.com/ndzuki/release-manager/internal/operator/ca"
	"github.com/ndzuki/release-manager/internal/operator/localstore"
)

// ErrTokenInvalid marks enrollment credentials that will never succeed:
// retrying the same token is pointless and must fail fast (REQ-015 error
// model: invalid_token / enroll_token_expired / token_reused).
var ErrTokenInvalid = errors.New("enrollment token rejected")

// Enroller is the minimal enrollment seam: the real implementation dials the
// agent gateway over TLS. Tests inject a stub to exercise retry and
// persistence behavior without a live server.
type Enroller interface {
	Enroll(ctx context.Context, req *connect.Request[operatorv1.EnrollRequest]) (*connect.Response[operatorv1.EnrollResponse], error)
}

// Config carries the bootstrap inputs. All fields except TokenFile/TokenEnv
// are required.
type Config struct {
	GatewayURL    string // agent gateway base URL (https://…)
	CAFilePath    string // gateway CA certificate (trust anchor)
	TokenFile     string // optional: file containing the enrollment token
	TokenEnv      string // optional: environment variable name holding the token
	CustomerID    string
	ClusterID     string
	OperatorName  string // defaults to ClusterID when empty
	IdentityStore localstore.IdentityStore
	// Enroller overrides the gateway client; nil builds the real one from
	// GatewayURL/CAFilePath. Tests inject a stub through this seam.
	Enroller Enroller
	Logger   *slog.Logger
}

// Result is the completed enrollment identity.
type Result struct {
	Identity  *localstore.Identity
	SessionID string
}

// Bootstrap resolves the identity: it returns the persisted identity when one
// exists (AC-075-02) or performs one Enroll round trip and persists the
// response (AC-075-01). The token file is removed only after the identity has
// been durably saved (single-use semantics: losing the token after success is
// safe, losing the identity before saving is not).
func Bootstrap(ctx context.Context, cfg Config) (*Result, error) {
	if cfg.IdentityStore == nil {
		return nil, fmt.Errorf("identity store is required")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// Fast path: an identity from a previous enrollment survives restarts.
	if identity, err := cfg.IdentityStore.LoadIdentity(ctx); err == nil {
		logger.Info("operator identity found; skipping enrollment", "operator_id", identity.OperatorID)
		return &Result{Identity: identity, SessionID: identity.SessionID}, nil
	} else if !errors.Is(err, localstore.ErrNotFound) {
		return nil, fmt.Errorf("load identity: %w", err)
	}

	token, err := resolveToken(cfg)
	if err != nil {
		return nil, err
	}

	identity, err := enrollAndPersist(ctx, cfg, token, logger)
	if err != nil {
		return nil, err
	}
	// The token is single-use: remove the file only after the identity is
	// durable. Deleting it on failure would strand the agent with neither an
	// identity nor a credential to retry with.
	if cfg.TokenFile != "" {
		if err := os.Remove(cfg.TokenFile); err != nil && !os.IsNotExist(err) {
			logger.Warn("failed to remove consumed enrollment token file",
				"path", cfg.TokenFile, "error", err)
		}
	}
	logger.Info("operator enrolled",
		"operator_id", identity.OperatorID,
		"customer_id", identity.CustomerID,
		"cluster_id", identity.ClusterID,
	)
	return &Result{Identity: identity, SessionID: identity.SessionID}, nil
}

// enrollAndPersist runs the Enroll round trip with retry and persists the
// identity. The retry loop retries only transient failures (network, 5xx);
// token rejection fails fast.
func enrollAndPersist(ctx context.Context, cfg Config, token string, logger *slog.Logger) (*localstore.Identity, error) {
	enroller := cfg.Enroller
	if enroller == nil {
		var err error
		enroller, err = newGatewayEnroller(cfg)
		if err != nil {
			return nil, err
		}
	}
	keyPEM, csrPEM, err := newKeyAndCSR(cfg.CustomerID, cfg.ClusterID, cfg.OperatorName)
	if err != nil {
		return nil, fmt.Errorf("generate identity key and CSR: %w", err)
	}

	operatorID := newOperatorID()
	backoff := 1 * time.Second
	const maxBackoff = 30 * time.Second
	for attempt := 0; ; attempt++ {
		resp, err := enroller.Enroll(ctx, connect.NewRequest(&operatorv1.EnrollRequest{
			EnrollmentToken: token,
			CustomerId:      cfg.CustomerID,
			ClusterId:       cfg.ClusterID,
			OperatorId:      operatorID,
			CsrPem:          csrPEM,
		}))
		if err == nil {
			identity := &localstore.Identity{
				OperatorID:     operatorID,
				OperatorName:   operatorName(cfg),
				CustomerID:     cfg.CustomerID,
				ClusterID:      cfg.ClusterID,
				SessionID:      resp.Msg.GetSessionId(),
				PrivateKeyPEM:  string(keyPEM),
				CertificatePEM: string(resp.Msg.GetCertificatePem()),
			}
			// Durably persist before returning; the token is consumed server
			// side, so losing the identity now would require operator
			// re-enrollment with a fresh token.
			if err := cfg.IdentityStore.SaveIdentity(ctx, identity); err != nil {
				return nil, fmt.Errorf("save identity: %w", err)
			}
			return identity, nil
		}
		// Classify the failure: token rejection is permanent; only network
		// and server-side 5xx failures are transient (REQ-015 error model:
		// permanent errors such as scope_mismatch / csr_invalid fail fast so
		// a misconfigured agent surfaces its error instead of retrying
		// forever).
		if isTokenRejection(err) {
			return nil, fmt.Errorf("%w: %s", ErrTokenInvalid, err.Error())
		}
		if !retryableEnrollError(err) {
			return nil, fmt.Errorf("enrollment rejected (permanent error): %w", err)
		}
		logger.Warn("enroll attempt failed; retrying",
			"attempt", attempt+1, "error", err)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// isTokenRejection reports whether the Connect error is one of the permanent
// enrollment credential failures (REQ-015 error model): invalid_token,
// enroll_token_expired, token_reused. All of these surface as
// CodeUnauthenticated with a stable message.
func isTokenRejection(err error) bool {
	if err == nil {
		return false
	}
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		return false
	}
	for _, marker := range []string{
		"invalid enrollment token",
		"enrollment token already used",
		"enrollment token expired",
	} {
		if strings.Contains(err.Error(), marker) {
			return true
		}
	}
	return false
}

// retryableEnrollError reports whether an Enroll failure is transient and
// worth backing off: network failures and server-side 5xx only. Everything
// else — invalid arguments, scope mismatches, permission denials — is a
// permanent configuration or contract error that must fail fast (REQ-015
// error model client handling).
func retryableEnrollError(err error) bool {
	switch connect.CodeOf(err) {
	case connect.CodeUnavailable, // network / 502 / 503 / 504
		connect.CodeUnknown,  // unmapped transport failures
		connect.CodeInternal, // server 5xx
		connect.CodeDeadlineExceeded:
		return true
	default:
		return false
	}
}

// newGatewayEnroller builds a Connect client that dials the gateway with the
// CA certificate as the trust anchor and no client certificate: Enroll
// accepts certificate-less requests on the gateway (mixed mTLS contract).
func newGatewayEnroller(cfg Config) (Enroller, error) {
	if cfg.GatewayURL == "" {
		return nil, fmt.Errorf("gateway url is required")
	}
	pool, err := ca.LoadCertPool(cfg.CAFilePath)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs:    pool,
			MinVersion: tls.VersionTLS13,
		},
		ForceAttemptHTTP2: true,
	}
	client := operatorv1connect.NewOperatorServiceClient(&http.Client{Transport: transport}, cfg.GatewayURL)
	return gatewayEnroller{client: client}, nil
}

type gatewayEnroller struct {
	client operatorv1connect.OperatorServiceClient
}

func (g gatewayEnroller) Enroll(ctx context.Context, req *connect.Request[operatorv1.EnrollRequest]) (*connect.Response[operatorv1.EnrollResponse], error) {
	return g.client.Enroll(ctx, req)
}
