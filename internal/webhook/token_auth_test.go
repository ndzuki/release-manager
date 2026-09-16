package webhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRequireTokenRejectsEveryUnusableCredential(t *testing.T) {
	accept := func(token string) bool { return token == "harbor-key" }
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("ok")); err != nil {
			panic(err)
		}
	})

	tests := []struct {
		name     string
		verify   TokenVerifier
		header   string
		wantCode int
	}{
		{name: "nil verifier matches nothing", verify: nil, header: "Bearer harbor-key", wantCode: http.StatusUnauthorized},
		{name: "rejecting verifier", verify: func(string) bool { return false }, header: "Bearer harbor-key", wantCode: http.StatusUnauthorized},
		{name: "missing header", verify: accept, wantCode: http.StatusUnauthorized},
		{name: "empty bearer", verify: accept, header: "Bearer ", wantCode: http.StatusUnauthorized},
		{name: "non bearer scheme", verify: accept, header: "Basic harbor-key", wantCode: http.StatusUnauthorized},
		{name: "wrong token", verify: accept, header: "Bearer other-key", wantCode: http.StatusUnauthorized},
		{name: "accepted token reaches the handler", verify: accept, header: "Bearer harbor-key", wantCode: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/webhooks/harbor", http.NoBody)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			RequireToken(tt.verify, next).ServeHTTP(rec, req)
			assert.Equal(t, tt.wantCode, rec.Code)
		})
	}
}
