// Package main starts the release-notification-sink service.
//
// It is a dev-only microservice that terminates the notifier's webhook
// channel: the notifier POSTs the same JSON payload it would send to any
// webhook recipient to the sink, which buffers the most recent notifications
// in a bounded ring and serves them back over GET /notifications. Email and
// Slack channels are disabled in the dev environment (REQ-065), so the sink
// is the single observability endpoint for notification delivery.
package main

import (
	"encoding/json"
	"flag"
	"log/slog"
	"net/http"
	"sync"

	"github.com/ndzuki/release-manager/internal/app"
	"github.com/ndzuki/release-manager/internal/config"
)

// webhookNotification mirrors the payload the notifier webhook sender POSTs
// (internal/notifier/webhook.go). Fields are optional on the wire but kept
// consistent so the buffer round-trips the exact delivery intent.
type webhookNotification struct {
	OperationID string            `json:"operation_id"`
	Channel     string            `json:"channel"`
	Recipient   string            `json:"recipient"`
	Metadata    map[string]string `json:"metadata"`
	JobID       string            `json:"job_id"`
}

// ring is a fixed-capacity circular buffer of notifications. Once full, every
// new write evicts the oldest item; the eviction is reported as a dropped
// notification by the sink.
type ring struct {
	items []webhookNotification
	next  int
	count int
}

func newRing(capacity int) *ring {
	return &ring{items: make([]webhookNotification, capacity)}
}

func (r *ring) push(n webhookNotification) {
	r.items[r.next] = n
	r.next = (r.next + 1) % len(r.items)
	if r.count < len(r.items) {
		r.count++
	}
}

// snapshot returns the buffered notifications in insertion order.
func (r *ring) snapshot() []webhookNotification {
	out := make([]webhookNotification, 0, r.count)
	start := 0
	if r.count == len(r.items) {
		// The ring is full: the oldest live item sits at the next write slot.
		start = r.next
	}
	for i := range r.count {
		out = append(out, r.items[(start+i)%len(r.items)])
	}
	return out
}

// notificationSink is the app.Run service behind the sink.
type notificationSink struct {
	cfg      config.ServiceConfig
	capacity int

	mu           sync.Mutex
	ring         *ring
	received     int
	droppedCount int
}

func (s *notificationSink) Name() string { return "release-notification-sink" }

func (s *notificationSink) Configure(cfg *config.ServiceConfig) { s.cfg = *cfg }

// Register mounts the two sink endpoints: the webhook receiver the notifier
// POSTs to, and the read-back list endpoint. app.Run already registers
// /health and /readyz.
func (s *notificationSink) Register(mux *http.ServeMux, logger *slog.Logger) error {
	s.ring = newRing(s.capacity)
	mux.HandleFunc("POST /webhook", s.handleWebhook)
	mux.HandleFunc("GET /notifications", s.handleList)
	return nil
}

// handleWebhook accepts a notification from the notifier webhook channel and
// buffers it. A malformed body is rejected with 400; a well-formed write is
// acknowledged with 202 Accepted (the sink never confirms delivery, it only
// records the observation).
func (s *notificationSink) handleWebhook(w http.ResponseWriter, r *http.Request) {
	var n webhookNotification
	if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	s.push(n)
	w.WriteHeader(http.StatusAccepted)
}

func (s *notificationSink) push(n webhookNotification) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.received++
	if s.received > s.capacity {
		s.droppedCount++
	}
	s.ring.push(n)
}

// handleList returns the buffered notifications plus the overflow counter so
// callers can detect notification loss during bursts.
func (s *notificationSink) handleList(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	items := s.ring.snapshot()
	dropped := s.droppedCount
	s.mu.Unlock()

	resp := struct {
		Notifications []webhookNotification `json:"notifications"`
		DroppedCount  int                   `json:"dropped_count"`
	}{
		Notifications: items,
		DroppedCount:  dropped,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func main() {
	configPath := flag.String("config", "configs/notification-sink.dev.yaml", "path to config file")
	flag.Parse()
	app.Run(*configPath, &notificationSink{capacity: 100})
}
