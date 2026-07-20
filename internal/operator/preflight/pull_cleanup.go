package preflight

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (p *PullProber) cleanupPod(ctx context.Context, namespace, name string) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.cleanupTimeout)
	defer cancel()

	propagation := metav1.DeletePropagationBackground
	graceSeconds := int64(0)
	err := p.client.CoreV1().Pods(namespace).Delete(cleanupCtx, name, metav1.DeleteOptions{
		GracePeriodSeconds: &graceSeconds,
		PropagationPolicy:  &propagation,
	})
	if err == nil || apierrors.IsNotFound(err) {
		return nil
	}
	return fmt.Errorf("delete runtime pull probe %s/%s: %w", namespace, name, err)
}
