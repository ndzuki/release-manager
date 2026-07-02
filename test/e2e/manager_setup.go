//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	managerNamespace = "release-manager"
	managerHTTPPort  = 30080
	managerGRPCPort  = 30443
)

// deployManager builds the manager image, loads it into kind, and deploys via Kustomize.
//
// Returns HTTP address (localhost:30080), gRPC address (localhost:30443),
// and a cleanup function.
func deployManager(ctx context.Context, clientset kubernetes.Interface, clusterName, caFile string, allowedFingerprints []string, hmacKey string) (string, string, func(), error) {
	ns := managerNamespace

	// Create namespace
	_, _ = clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})

	// Helper for kubectl commands
	kubectl := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "kubectl", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("kubectl %v: %w\n%s", args, err, string(out))
		}
		return nil
	}

	// Build and load image
	if os.Getenv("SKIP_BUILD") != "1" {
		buildCmd := exec.CommandContext(ctx, "go", "build", "-ldflags=-s -w",
			"-o", "bin/release-manager", "./cmd/release-manager/")
		if out, err := buildCmd.CombinedOutput(); err != nil {
			return "", "", nil, fmt.Errorf("build manager: %w\n%s", err, string(out))
		}

		dockerCmd := exec.CommandContext(ctx, "docker", "build",
			"-f", "Dockerfile.manager", "-t", "release-manager:dev", ".")
		if out, err := dockerCmd.CombinedOutput(); err != nil {
			return "", "", nil, fmt.Errorf("docker build manager: %w\n%s", err, string(out))
		}

		loadCmd := exec.CommandContext(ctx, "kind", "load", "docker-image",
			"release-manager:dev", "--name", clusterName)
		if out, err := loadCmd.CombinedOutput(); err != nil {
			return "", "", nil, fmt.Errorf("kind load manager: %w\n%s", err, string(out))
		}
	}

	// Generate a self-signed server cert for the manager (for mTLS serving)
	certDir, err := os.MkdirTemp("", "manager-server-certs-*")
	if err != nil {
		return "", "", nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(certDir)

	serverKey := filepath.Join(certDir, "tls.key")
	serverCrt := filepath.Join(certDir, "tls.crt")

	genCert := exec.CommandContext(ctx, "openssl", "req", "-x509", "-newkey", "rsa:2048",
		"-keyout", serverKey, "-out", serverCrt, "-days", "365", "-nodes",
		"-subj", "/O=Release Manager E2E/CN=release-manager")
	if out, err := genCert.CombinedOutput(); err != nil {
		return "", "", nil, fmt.Errorf("generate server cert: %w\n%s", err, string(out))
	}

	// Create TLS secret with CA cert (for client verification), server cert, and server key
	_ = kubectl("-n", ns, "delete", "secret", "release-manager-tls", "--ignore-not-found")
	if err := kubectl("-n", ns, "create", "secret", "generic", "release-manager-tls",
		fmt.Sprintf("--from-file=ca.crt=%s", caFile),
		fmt.Sprintf("--from-file=tls.crt=%s", serverCrt),
		fmt.Sprintf("--from-file=tls.key=%s", serverKey)); err != nil {
		return "", "", nil, fmt.Errorf("create TLS secret: %w", err)
	}

	// Render manager config via templated YAML
	configYaml := fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: release-manager-config
  namespace: %s
data:
  config.yaml: |
    server:
      grpc_addr: ":8443"
      http_addr: ":8080"
    log:
      level: debug
      format: json
    tls:
      ca_file: /etc/release-manager/certs/ca.crt
      cert_file: /etc/release-manager/certs/tls.crt
      key_file: /etc/release-manager/certs/tls.key
      require_client_cert: true
      allowed_fingerprints:
        - %s
    harbor:
      url: http://registry.registry:5000
      insecure_skip_verify: true
      webhook_hmac_secret: "%s"
    helm:
      upgrade_timeout: 10m
      default_namespace: default
      max_history: 10
      atomic: true
      wait: true
      create_namespace: true
    store:
      type: sqlite
      dsn: /data/release-manager.db
    dev_mode: true
    api_key: e2e-test-key
`, ns, strings.Join(allowedFingerprints, "\n        - "), hmacKey)

	// Apply ConfigMap
	cmCmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmCmd.Stdin = strings.NewReader(configYaml)
	if out, err := cmCmd.CombinedOutput(); err != nil {
		return "", "", nil, fmt.Errorf("kubectl apply configmap: %w\n%s", err, string(out))
	}

	// Deploy manager as a simple Pod + Service (avoids Kustomize complexity in CI)
	manifest := fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: release-manager
  namespace: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: release-manager
  template:
    metadata:
      labels:
        app: release-manager
    spec:
      containers:
      - name: manager
        image: release-manager:dev
        imagePullPolicy: IfNotPresent
        ports:
        - containerPort: 8080
          name: http
        - containerPort: 8443
          name: grpc
        volumeMounts:
        - name: config
          mountPath: /etc/release-manager
        - name: certs
          mountPath: /etc/release-manager/certs
      volumes:
      - name: config
        configMap:
          name: release-manager-config
      - name: certs
        secret:
          secretName: release-manager-tls
---
apiVersion: v1
kind: Service
metadata:
  name: release-manager
  namespace: %s
spec:
  type: NodePort
  selector:
    app: release-manager
  ports:
  - port: 8080
    targetPort: 8080
    nodePort: %d
    name: http
  - port: 8443
    targetPort: 8443
    nodePort: %d
    name: grpc
`, ns, ns, managerHTTPPort, managerGRPCPort)

	depCmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	depCmd.Stdin = strings.NewReader(manifest)
	if out, err := depCmd.CombinedOutput(); err != nil {
		return "", "", nil, fmt.Errorf("kubectl apply manager: %w\n%s", err, string(out))
	}

	// Wait for manager pod ready
	if err := waitForPodReady(ctx, clientset, ns, "app=release-manager", 2*time.Minute); err != nil {
		return "", "", nil, fmt.Errorf("wait for manager pod: %w", err)
	}

	httpAddr := fmt.Sprintf("localhost:%d", managerHTTPPort)
	grpcAddr := fmt.Sprintf("localhost:%d", managerGRPCPort)

	// Verify endpoints
	if err := waitForHTTPReady(ctx, fmt.Sprintf("http://%s/health", httpAddr), 30*time.Second); err != nil {
		return "", "", nil, fmt.Errorf("wait for manager HTTP: %w", err)
	}
	if err := waitForGRPCReady(ctx, grpcAddr, 30*time.Second); err != nil {
		return "", "", nil, fmt.Errorf("wait for manager gRPC: %w", err)
	}

	cleanup := func() {
		_ = clientset.CoreV1().Namespaces().Delete(ctx, ns, metav1.DeleteOptions{})
	}

	return httpAddr, grpcAddr, cleanup, nil
}
