package e2e

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"connectrpc.com/connect"
	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
)

// ErrRunnerLogin reports an e2e-runner authentication failure.
var ErrRunnerLogin = errors.New("e2e-runner login failed")

// RunnerSession owns the e2e-runner development account login (REQ-065) and the
// resulting access token. Every formal-API E2E seam authenticates through one
// session, so the login handshake is implemented once and the token has a
// single owner instead of being copied into each adapter.
//
// The access token is never serialized and never surfaced in a run artifact.
type RunnerSession struct {
	cfg     *Config
	clients *ClientBundle

	mu          sync.Mutex
	accessToken string
	userID      string
}

// NewRunnerSession constructs the session. The config must already be validated
// (LoadConfig resolves the password from its named environment variable).
func NewRunnerSession(cfg *Config, clients *ClientBundle) (*RunnerSession, error) {
	if cfg == nil {
		return nil, configInvalid("config", "nil config")
	}
	if clients == nil {
		return nil, configInvalid("clients", "nil client bundle")
	}
	return &RunnerSession{cfg: cfg, clients: clients}, nil
}

// Login authenticates as e2e-runner and resolves the authoritative user id.
// The user id is read from ValidateToken because LoginResponse.user may be
// unset on the current auth version (see smoke.sh D-029 D4).
func (s *RunnerSession) Login(ctx context.Context) (string, error) {
	if s == nil || s.cfg == nil || s.clients == nil {
		return "", ErrRunnerLogin
	}
	password := s.cfg.Password()
	if password == "" {
		return "", fmt.Errorf("%w: password environment variable is not set", ErrRunnerLogin)
	}
	login, err := s.clients.auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{
		Username: s.cfg.Credentials.E2ERunner.Username,
		Password: password,
	}))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrRunnerLogin, err)
	}
	if login == nil || login.Msg == nil {
		return "", fmt.Errorf("%w: empty login response", ErrRunnerLogin)
	}
	accessToken := login.Msg.AccessToken
	if accessToken == "" {
		return "", fmt.Errorf("%w: empty access token", ErrRunnerLogin)
	}

	userID := ""
	if login.Msg.User != nil && login.Msg.User.Id != "" {
		userID = login.Msg.User.Id
	} else {
		valid, err := s.clients.auth.ValidateToken(ctx, connect.NewRequest(&authv1.ValidateTokenRequest{Token: accessToken}))
		if err != nil {
			return "", fmt.Errorf("%w: validate token: %v", ErrRunnerLogin, err)
		}
		if valid.Msg == nil || !valid.Msg.Valid {
			return "", fmt.Errorf("%w: token validation rejected", ErrRunnerLogin)
		}
		userID = valid.Msg.UserId
	}

	s.mu.Lock()
	s.accessToken = accessToken
	s.userID = userID
	s.mu.Unlock()
	return userID, nil
}

// EnsureLogin authenticates once. Later calls are no-ops while a token is held,
// so an adapter can call it before every write without re-authenticating.
func (s *RunnerSession) EnsureLogin(ctx context.Context) error {
	if s == nil {
		return ErrRunnerLogin
	}
	s.mu.Lock()
	authenticated := s.accessToken != ""
	s.mu.Unlock()
	if authenticated {
		return nil
	}
	_, err := s.Login(ctx)
	return err
}

// Token returns the current bearer token (empty before Login).
func (s *RunnerSession) Token() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accessToken
}

// UserID returns the authenticated runner user id after a successful Login.
func (s *RunnerSession) UserID() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.userID
}

// WriteIdempotencyKey returns a stable key for an E2E write stage, optionally
// scoped to the state the write starts from.
//
// Stability (rather than a random suffix) is what makes a replayed stage dedupe
// to the same server-side operation instead of submitting a second write
// (ADR-009 scoped idempotency).
//
// The state qualifier is what keeps a later run able to submit at all. The
// server rejects a reused key whose request hash differs ("idempotency_conflict:
// key already used with different request"), and these stages read their starting
// revision fresh on every run, so a key that ignored it could only ever be used
// once per target: the first successful upgrade changed the revision, and every
// later run sent the same key with a different expected revision (real smoke
// 2026-09-11). A write from a different state is a different write; a replay from
// the same state still dedupes.
func WriteIdempotencyKey(kind, target string, state ...string) string {
	key := "e2e-" + kind + "-" + target
	for _, part := range state {
		if part != "" {
			key += "-" + part
		}
	}
	return key
}
