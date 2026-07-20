package operator

import (
	"context"
	"crypto/tls"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSessionClientValidation(t *testing.T) {
	client, err := NewSessionClient(SessionClientConfig{}, slog.New(slog.DiscardHandler))
	require.Error(t, err)
	assert.Nil(t, client)
}

func TestNewSessionClientDefaults(t *testing.T) {
	client, err := NewSessionClient(SessionClientConfig{
		BaseURL:     "https://localhost:8443",
		OperatorID:  "operator-1",
		InstanceID:  "instance-1",
		Version:     "1.0.0",
		Certificate: tls.Certificate{},
	}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	assert.Equal(t, time.Second, client.config.InitialBackoff)
	assert.Equal(t, 30*time.Second, client.config.MaxBackoff)
	assert.Equal(t, 30*time.Second, client.config.HeartbeatTimeout)
}

func TestSessionClientRunStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client, err := NewSessionClient(SessionClientConfig{
		BaseURL:    "https://localhost:8443",
		OperatorID: "operator-1",
		InstanceID: "instance-1",
		Version:    "1.0.0",
	}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	assert.ErrorIs(t, client.Run(ctx), context.Canceled)
}
