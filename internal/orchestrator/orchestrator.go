// Package orchestrator coordinates release notification flows between
// the webhook receiver, customer forwarder, and notification channels.
package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"

	"github.com/ndzuki/release-manager/internal/notifier"
	"github.com/ndzuki/release-manager/internal/webhook"
)

// Notifier sends release status notifications (DingTalk, email).
type Notifier interface {
	SendReleaseNotification(chartName, chartVersion string, results []notifier.ForwardResult) error
	SendStatusUpdate(customerName, chartName, chartVersion, status, errMsg string) error
}

// Forwarder sends notifications to customer operator clusters.
type Forwarder interface {
	ForwardToAll(ctx context.Context, notification webhook.ReleaseNotification) ([]notifier.ForwardResult, error)
}

// RecordStore persists release records.
type RecordStore interface {
	CreateReleaseRecord(r ReleaseRecord) error
}

// ReleaseRecord represents a release operation record.
type ReleaseRecord struct {
	ID           string
	RequestID    string
	CustomerID   string
	ChartName    string
	ChartVersion string
	Status       string
	ErrorMessage string
	StartedAt    time.Time
	CompletedAt  time.Time
}

// Orchestrator coordinates the release notification pipeline.
type Orchestrator struct {
	forwarder Forwarder
	notifier  Notifier
	store     RecordStore
	log       logr.Logger
	timeout   time.Duration
}

// New creates a new Orchestrator.
func New(forwarder Forwarder, notifier Notifier, store RecordStore, log logr.Logger) *Orchestrator {
	return &Orchestrator{
		forwarder: forwarder,
		notifier:  notifier,
		store:     store,
		log:       log.WithName("orchestrator"),
		timeout:   5 * time.Minute,
	}
}

// OnReleaseNotification processes a release notification from the webhook handler.
// It forwards to all enabled customers and notifies via DingTalk.
func (o *Orchestrator) OnReleaseNotification(notification webhook.ReleaseNotification) error {
	o.log.Info("processing release notification",
		"chart", notification.ChartName,
		"version", notification.ChartVersion,
	)

	ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
	defer cancel()

	results, err := o.forwarder.ForwardToAll(ctx, notification)
	if err != nil {
		return fmt.Errorf("forward to customers: %w", err)
	}

	// Record failed releases.
	for _, r := range results {
		if !r.Success {
			record := ReleaseRecord{
				RequestID:    webhook.GenerateRequestID(),
				CustomerID:   r.CustomerID,
				ChartName:    notification.ChartName,
				ChartVersion: notification.ChartVersion,
				Status:       "failed",
				ErrorMessage: r.ErrorMessage,
				StartedAt:    notification.OccurredAt,
				CompletedAt:  time.Now(),
			}
			if err := o.store.CreateReleaseRecord(record); err != nil {
				o.log.Error(err, "failed to create failed release record", "customer", r.CustomerID)
			}
		}
	}

	// DingTalk notification is best-effort.
	if err := o.notifier.SendReleaseNotification(
		notification.ChartName,
		notification.ChartVersion,
		results,
	); err != nil {
		o.log.Error(err, "failed to send DingTalk notification")
	}

	return nil
}
