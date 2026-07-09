//go:build e2e

package e2e

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

// retryUntil polls fn at interval until it returns (true, nil) or ctx expires.
// Returns the last error from fn, or context error.
func retryUntil(ctx context.Context, interval, timeout time.Duration, fn func() (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return wait.PollUntilContextTimeout(ctx, interval, timeout, true, func(ctx context.Context) (bool, error) {
		return fn()
	})
}

// waitForPodReady waits for at least one pod matching labelSelector in namespace
// to be in Ready condition.
func waitForPodReady(ctx context.Context, clientset kubernetes.Interface, namespace, labelSelector string, timeout time.Duration) error {
	return retryUntil(ctx, 2*time.Second, timeout, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: labelSelector,
		})
		if err != nil {
			return false, err
		}
		if len(pods.Items) == 0 {
			return false, nil
		}
		for _, pod := range pods.Items {
			for _, cond := range pod.Status.Conditions {
				if cond.Type == "Ready" && cond.Status == "True" {
					return true, nil
				}
			}
		}
		return false, nil
	})
}

// waitForHTTPReady polls url until it returns 200 OK.
// Uses an insecure HTTP client that skips TLS verification (needed for
// self-signed certs on the local registry).
func waitForHTTPReady(ctx context.Context, url string, timeout time.Duration) error {
	insecureClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	return retryUntil(ctx, 1*time.Second, timeout, func() (bool, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return false, err
		}
		resp, err := insecureClient.Do(req)
		if err != nil {
			return false, nil // not ready yet
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK, nil
	})
}

// waitForGRPCReady polls addr until a TCP connection can be established.
func waitForGRPCReady(ctx context.Context, addr string, timeout time.Duration) error {
	return retryUntil(ctx, 1*time.Second, timeout, func() (bool, error) {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return false, nil
		}
		conn.Close()
		return true, nil
	})
}
