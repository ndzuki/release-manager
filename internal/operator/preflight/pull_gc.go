package preflight

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type PullGC struct {
	client    kubernetes.Interface
	namespace string
	logger    *slog.Logger
	interval  time.Duration
	batchSize int
	now       func() time.Time
}

func NewPullGC(client kubernetes.Interface, namespace string, logger *slog.Logger) *PullGC {
	if logger == nil {
		logger = slog.Default()
	}
	return &PullGC{
		client:    client,
		namespace: namespace,
		logger:    logger,
		interval:  DefaultGCInterval,
		batchSize: DefaultGCBatchSize,
		now:       time.Now,
	}
}

func (g *PullGC) Run(ctx context.Context) {
	if g == nil || g.client == nil {
		return
	}
	g.runOnceAndLog(ctx)
	ticker := time.NewTicker(g.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.runOnceAndLog(ctx)
		}
	}
}

func (g *PullGC) runOnceAndLog(ctx context.Context) {
	deleted, err := g.RunOnce(ctx)
	if err != nil && ctx.Err() == nil {
		g.logger.Warn("runtime pull preflight gc failed", "error", err)
		return
	}
	if deleted > 0 {
		g.logger.Info("runtime pull preflight gc completed", "deleted", deleted)
	}
}

func (g *PullGC) RunOnce(ctx context.Context) (int, error) {
	if g == nil || g.client == nil {
		return 0, fmt.Errorf("runtime pull preflight gc client is required")
	}
	list, err := g.client.CoreV1().Pods(g.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: ManagedLabel + "=true",
		Limit:         int64(g.batchSize),
	})
	if err != nil {
		return 0, fmt.Errorf("list runtime pull probes: %w", err)
	}

	deleted := 0
	for i := range list.Items {
		pod := &list.Items[i]
		expiresAt, err := time.Parse(time.RFC3339, pod.Annotations[ExpireAtAnnotation])
		if err != nil || expiresAt.After(g.now()) {
			continue
		}
		propagation := metav1.DeletePropagationBackground
		graceSeconds := int64(0)
		if err := g.client.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{
			GracePeriodSeconds: &graceSeconds,
			PropagationPolicy:  &propagation,
		}); err != nil {
			return deleted, fmt.Errorf("delete expired runtime pull probe %s/%s: %w", pod.Namespace, pod.Name, err)
		}
		deleted++
	}
	return deleted, nil
}
