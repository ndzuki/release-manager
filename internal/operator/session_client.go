package operator

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	operatorv1connect "github.com/ndzuki/release-manager/api/gen/operator/v1/operatorv1connect"
)

type SessionClientConfig struct {
	BaseURL          string
	OperatorID       string
	InstanceID       string
	Version          string
	Capabilities     map[string]string
	Certificate      tls.Certificate
	RootCAs          *x509.CertPool
	InitialBackoff   time.Duration
	MaxBackoff       time.Duration
	HeartbeatTimeout time.Duration
}

type SessionClient struct {
	config SessionClientConfig
	client operatorv1connect.OperatorServiceClient
	logger *slog.Logger
}

func NewSessionClient(config SessionClientConfig, logger *slog.Logger) (*SessionClient, error) {
	if config.BaseURL == "" {
		return nil, fmt.Errorf("base url is required")
	}
	if config.OperatorID == "" {
		return nil, fmt.Errorf("operator id is required")
	}
	if config.InstanceID == "" {
		return nil, fmt.Errorf("instance id is required")
	}
	if config.Version == "" {
		return nil, fmt.Errorf("version is required")
	}
	if config.InitialBackoff <= 0 {
		config.InitialBackoff = time.Second
	}
	if config.MaxBackoff < config.InitialBackoff {
		config.MaxBackoff = 30 * time.Second
	}
	if config.HeartbeatTimeout <= 0 {
		config.HeartbeatTimeout = 30 * time.Second
	}
	if config.Capabilities == nil {
		config.Capabilities = map[string]string{}
	}

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion:   tls.VersionTLS13,
			Certificates: []tls.Certificate{config.Certificate},
			RootCAs:      config.RootCAs,
		},
	}
	httpClient := &http.Client{Transport: transport}
	return &SessionClient{
		config: config,
		client: operatorv1connect.NewOperatorServiceClient(httpClient, config.BaseURL),
		logger: logger,
	}, nil
}

func (c *SessionClient) Run(ctx context.Context) error {
	backoff := c.config.InitialBackoff
	for {
		err := c.runSession(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.logger.Warn("operator session disconnected", "error", err, "retry_after", backoff)

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		backoff = min(backoff*2, c.config.MaxBackoff)
	}
}

func (c *SessionClient) runSession(ctx context.Context) error {
	stream := c.client.CommandStream(ctx)
	if err := stream.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_Hello{
			Hello: &operatorv1.Hello{
				OperatorId:   c.config.OperatorID,
				InstanceId:   c.config.InstanceID,
				Version:      c.config.Version,
				Capabilities: c.config.Capabilities,
			},
		},
	}); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}

	response, err := stream.Receive()
	if err != nil {
		return fmt.Errorf("receive session established: %w", err)
	}
	established := response.GetSessionEstablished()
	if established == nil {
		return fmt.Errorf("session established response required")
	}

	interval := time.Duration(established.GetHeartbeatIntervalSeconds()) * time.Second
	if interval <= 0 {
		return fmt.Errorf("invalid heartbeat interval")
	}
	c.logger.Info(
		"operator session established",
		"session_id", established.GetSessionId(),
		"active_config_version", established.GetActiveConfigVersion(),
	)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := stream.Send(&operatorv1.CommandStreamRequest{
				Payload: &operatorv1.CommandStreamRequest_Heartbeat{
					Heartbeat: &operatorv1.Heartbeat{SessionId: established.GetSessionId()},
				},
			}); err != nil {
				if errors.Is(err, context.Canceled) {
					return err
				}
				return fmt.Errorf("send heartbeat: %w", err)
			}
		}
	}
}
