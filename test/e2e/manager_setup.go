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
// If dingtalkURL is non-empty, the manager config will include DingTalk bot integration.
func deployManager(ctx context.Context, clientset kubernetes.Interface, clusterName, caFile string, allowedFingerprints []string, hmacKey, dingtalkURL string) (string, string, func(), error) {
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
		root := projectRoot()

		buildCmd := exec.CommandContext(ctx, "go", "build", "-ldflags=-s -w",
			"-o", "bin/release-manager", "./cmd/release-manager/")
		buildCmd.Dir = root
		if out, err := buildCmd.CombinedOutput(); err != nil {
			return "", "", nil, fmt.Errorf("build manager: %w\n%s", err, string(out))
		}

		dockerCmd := exec.CommandContext(ctx, "docker", "build",
			"-f", "Dockerfile.manager", "-t", "release-manager:dev", ".")
		dockerCmd.Dir = root
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

	// Build dingtalk config section if URL is provided
	dingtalkSection := ""
	if dingtalkURL != "" {
		dingtalkSection = fmt.Sprintf(`    dingtalk:
      webhook_url: "%s"
      enabled: true
`, dingtalkURL)
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
%s    store:
      type: sqlite
      dsn: /data/release-manager.db
    dev_mode: true
    api_key: e2e-test-key
`, ns, strings.Join(allowedFingerprints, "\n        - "), hmacKey, dingtalkSection)

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

// patchManagerDingTalk updates the manager ConfigMap with a DingTalk webhook URL
// and restarts the manager deployment so the new config takes effect.
// It waits for the new pod to be ready and the HTTP endpoint to become available.
func patchManagerDingTalk(ctx context.Context, clientset kubernetes.Interface, dingtalkURL, httpAddr string) error {
	ns := managerNamespace

	kubectl := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "kubectl", args...)
		return cmd.CombinedOutput()
	}

	// Get the current ConfigMap as text
	cmData, err := kubectl("-n", ns, "get", "configmap", "release-manager-config",
		"-o", `jsonpath={.data['config\.yaml']}`)
	if err != nil {
		return fmt.Errorf("get configmap: %w\n%s", err, string(cmData))
	}

	configContent := string(cmData)

	// Check if dingtalk section already exists
	if strings.Contains(configContent, "dingtalk:") {
		// Update the webhook_url line
		lines := strings.Split(configContent, "\n")
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "webhook_url:") {
				lines[i] = "      webhook_url: \"" + dingtalkURL + "\""
				break
			}
		}
		configContent = strings.Join(lines, "\n")
	} else {
		// Insert dingtalk section before "store:"
		dingtalkSection := fmt.Sprintf(`    dingtalk:
      webhook_url: "%s"
      enabled: true
`, dingtalkURL)
		configContent = strings.Replace(configContent, "    store:", dingtalkSection+"    store:", 1)
	}

	// Re-apply the ConfigMap with updated content using stdin
	patchConfig := fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: release-manager-config
  namespace: %s
data:
  config.yaml: |
%s`, ns, indentConfigYAML(configContent))

	applyCmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	applyCmd.Stdin = strings.NewReader(patchConfig)
	applyOut, err := applyCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("apply configmap: %w\n%s", err, string(applyOut))
	}

	// Rollout restart and wait for it to complete
	restartOut, err := kubectl("-n", ns, "rollout", "restart", "deployment", "release-manager")
	if err != nil {
		return fmt.Errorf("rollout restart: %w\n%s", err, string(restartOut))
	}

	statusOut, err := kubectl("-n", ns, "rollout", "status", "deployment", "release-manager", "--timeout=120s")
	if err != nil {
		return fmt.Errorf("rollout status: %w\n%s", err, string(statusOut))
	}

	// Verify endpoints are back
	if err := waitForHTTPReady(ctx, fmt.Sprintf("http://%s/health", httpAddr), 60*time.Second); err != nil {
		return fmt.Errorf("wait for manager HTTP after restart: %w", err)
	}

	return nil
}

// indentConfigYAML adds 4 spaces of indentation to each line for embedding
// in a ConfigMap data section.
func indentConfigYAML(content string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = "    " + line
		}
	}
	return strings.Join(lines, "\n")
}
