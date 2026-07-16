package helmengine

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
)

func TestWithRequestContext_PropagatesCancellationWithoutGoroutineLeak(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	config := withRequestContext(ctx, &rest.Config{
		Host:      "https://cluster.example",
		Transport: blockingRoundTripper{},
	})

	transport, err := rest.TransportFor(config)
	require.NoError(t, err)

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, config.Host, http.NoBody)
	require.NoError(t, err)

	var wait sync.WaitGroup
	result := make(chan error, 1)
	wait.Go(func() {
		response, requestErr := transport.RoundTrip(request)
		if response != nil {
			response.Body.Close()
		}
		result <- requestErr
	})

	cancel()
	assert.ErrorIs(t, <-result, context.Canceled)
	wait.Wait()
}

type blockingRoundTripper struct{}

func (blockingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	<-request.Context().Done()
	return nil, request.Context().Err()
}

func TestWithRequestContext_PreservesTransportErrors(t *testing.T) {
	wantErr := errors.New("transport failed")
	config := withRequestContext(t.Context(), &rest.Config{
		Host:      "https://cluster.example",
		Transport: errorRoundTripper{err: wantErr},
	})

	transport, err := rest.TransportFor(config)
	require.NoError(t, err)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, config.Host, http.NoBody)
	require.NoError(t, err)

	response, err := transport.RoundTrip(request)
	if response != nil {
		response.Body.Close()
	}
	assert.ErrorIs(t, err, wantErr)
}

type errorRoundTripper struct {
	err error
}

func (t errorRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, t.err
}
