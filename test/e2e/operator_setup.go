//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// deployOperator builds the operator image, loads it into kind, and deploys via Helm.
//
// customerID identifies the customer for this operator instance.
// notifEndpoint is the manager's gRPC address that the operator reports back to.
// caFile, certFile, keyFile are mTLS certificate paths.
//
// Returns a cleanup function that removes the operator namespace.
func deployOperator(ctx context.Context, clientset kubernetes.Interface, clusterName, customerID, notifEndpoint, caFile, certFile, keyFile string) (func(), error) {
	ns := fmt.Sprintf("release-operator-%s", customerID)

	// Create namespace
	_, _ = clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})

	// Helper to run kubectl commands
	kubectl := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "kubectl", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("kubectl %v: %w\n%s", args, err, string(out))
		}
		return nil
	}

	// Create TLS secret (kubernetes.io/tls type) via kubectl.
	// Delete first for idempotency.
	_ = kubectl("-n", ns, "delete", "secret", "release-operator-tls", "--ignore-not-found")
	if err := kubectl("-n", ns, "create", "secret", "tls", "release-operator-tls",
		fmt.Sprintf("--cert=%s", certFile), fmt.Sprintf("--key=%s", keyFile)); err != nil {
		return nil, fmt.Errorf("create TLS secret: %w", err)
	}

	// Create CA secret (Opaque type with ca.crt key) via kubectl.
	_ = kubectl("-n", ns, "delete", "secret", "release-operator-ca", "--ignore-not-found")
	if err := kubectl("-n", ns, "create", "secret", "generic", "release-operator-ca",
		fmt.Sprintf("--from-file=ca.crt=%s", caFile)); err != nil {
		return nil, fmt.Errorf("create CA secret: %w", err)
	}

	// Build and load image (if SKIP_BUILD is not set)
	if os.Getenv("SKIP_BUILD") != "1" {
		root := projectRoot()

		// Build binary
		buildCmd := exec.CommandContext(ctx, "go", "build", "-ldflags=-s -w",
			"-o", "bin/release-operator", "./cmd/release-operator/")
		buildCmd.Dir = root
		buildCmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := buildCmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("build operator: %w\n%s", err, string(out))
		}

		// Build image
		dockerCmd := exec.CommandContext(ctx, "docker", "build",
			"-f", "Dockerfile.operator", "-t", "release-operator:dev", ".")
		dockerCmd.Dir = root
		if out, err := dockerCmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("docker build operator: %w\n%s", err, string(out))
		}

		// Load into kind
		loadCmd := exec.CommandContext(ctx, "kind", "load", "docker-image",
			"release-operator:dev", "--name", clusterName)
		if out, err := loadCmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("kind load operator: %w\n%s", err, string(out))
		}
	}

	// Deploy via Helm
	helmCmd := exec.CommandContext(ctx, "helm", "upgrade", "--install",
		fmt.Sprintf("release-operator-%s", customerID),
		filepath.Join(projectRoot(), "deployments/release-operator"),
		"-n", ns,
		"--set", fmt.Sprintf("customerID=%s", customerID),
		"--set", fmt.Sprintf("notificationEndpoint=%s", notifEndpoint),
		"--set", "image.repository=release-operator",
		"--set", "image.tag=dev",
		"--set", "image.pullPolicy=IfNotPresent",
		"--set", "tls.enabled=true",
		"--set", "tls.existingCertSecret=release-operator-tls",
		"--set", "tls.existingCaSecret=release-operator-ca",
		"--set", "harbor.insecureSkipVerify=true",
		"--set", "rbac.managedNamespaces[0]=default",
		"--set", "networkPolicy.enabled=false",
		"--timeout", "2m",
	)
	if out, err := helmCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("helm install operator: %w\n%s", err, string(out))
	}

	// Wait for operator pod ready
	if err := waitForPodReady(ctx, clientset, ns, "app.kubernetes.io/name=release-operator", 2*time.Minute); err != nil {
		return nil, fmt.Errorf("wait for operator pod: %w", err)
	}

	cleanup := func() {
		_ = clientset.CoreV1().Namespaces().Delete(ctx, ns, metav1.DeleteOptions{})
	}

	return cleanup, nil
}
