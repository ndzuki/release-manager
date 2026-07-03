//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"log"
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
	registryNamespace = "registry"
	registryImage     = "registry:3.0.0"
	registryNodePort  = 30500
	registryAddr      = "localhost:30500"
)

// deployRegistry deploys a Docker registry to the kind cluster with HTTPS
// using a self-signed certificate (operator's Helm client defaults to HTTPS).
//
// Two modes:
//   - "proxy": registry acts as pull-through cache to Harbor (requires harbor creds)
//   - "standalone": registry runs as an independent OCI registry (no Harbor)
//
// clusterName is the kind cluster name, used to pre-load the registry image.
// Returns the registry address (localhost:30500) and a cleanup function.
func deployRegistry(ctx context.Context, clientset kubernetes.Interface, clusterName, mode string, harborURL, harborUser, harborPass string) (string, func(), error) {
	// Pre-load registry image into kind (kind nodes may not have internet access)
	pullCmd := exec.CommandContext(ctx, "docker", "pull", registryImage)
	if out, err := pullCmd.CombinedOutput(); err != nil {
		_ = out // image might already exist locally
	}
	loadCmd := exec.CommandContext(ctx, "kind", "load", "docker-image", registryImage, "--name", clusterName)
	if out, err := loadCmd.CombinedOutput(); err != nil {
		return "", nil, fmt.Errorf("kind load docker-image %s: %w\n%s", registryImage, err, string(out))
	}

	// Create namespace
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: registryNamespace}}
	if _, err := clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil {
		log.Printf("warning: create namespace %s: %v", registryNamespace, err)
	}

	// Generate self-signed cert for HTTPS. The operator's Helm client defaults
	// to HTTPS; a plain HTTP registry would cause "http: server gave HTTP
	// response to HTTPS client" errors during helm pull.
	certDir, err := os.MkdirTemp("", "registry-certs-*")
	if err != nil {
		return "", nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(certDir)
	certKey := filepath.Join(certDir, "tls.key")
	certCrt := filepath.Join(certDir, "tls.crt")
	genCert := exec.CommandContext(ctx, "openssl", "req", "-x509", "-newkey", "rsa:2048",
		"-keyout", certKey, "-out", certCrt, "-days", "365", "-nodes",
		"-subj", "/CN=registry.registry",
		"-addext", "subjectAltName=DNS:registry.registry,DNS:localhost")
	if out, err := genCert.CombinedOutput(); err != nil {
		return "", nil, fmt.Errorf("generate registry cert: %w\n%s", err, string(out))
	}

	// Create TLS secret
	createCmd := exec.CommandContext(ctx, "kubectl", "-n", registryNamespace,
		"create", "secret", "tls", "registry-tls",
		"--cert="+certCrt, "--key="+certKey,
		"--dry-run=client", "-o", "yaml")
	secretYAML, err := createCmd.CombinedOutput()
	if err != nil {
		return "", nil, fmt.Errorf("create registry TLS secret YAML: %w\n%s", err, string(secretYAML))
	}
	applyCmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	applyCmd.Stdin = strings.NewReader(string(secretYAML))
	if out, err := applyCmd.CombinedOutput(); err != nil {
		return "", nil, fmt.Errorf("kubectl apply registry TLS secret: %w\n%s", err, string(out))
	}

	// Build registry config YAML
	configYaml := ""
	if mode == "proxy" && harborURL != "" {
		configYaml = fmt.Sprintf(`version: 0.1
proxy:
  remoteurl: %s
  username: %s
  password: %s
  ttl: 168h
`, harborURL, harborUser, harborPass)
	} else {
		configYaml = `version: 0.1
storage:
  filesystem:
    rootdirectory: /var/lib/registry
`
	}

	manifest := fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: registry
  namespace: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: registry
  template:
    metadata:
      labels:
        app: registry
    spec:
      containers:
      - name: registry
        image: %s
        ports:
        - containerPort: 5000
        env:
        - name: REGISTRY_HTTP_ADDR
          value: "0.0.0.0:5000"
        - name: REGISTRY_HTTP_TLS_CERTIFICATE
          value: /certs/tls.crt
        - name: REGISTRY_HTTP_TLS_KEY
          value: /certs/tls.key
        volumeMounts:
        - name: config
          mountPath: /etc/docker/registry
        - name: certs
          mountPath: /certs
          readOnly: true
      volumes:
      - name: config
        configMap:
          name: registry-config
      - name: certs
        secret:
          secretName: registry-tls
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: registry-config
  namespace: %s
data:
  config.yml: |
%s
---
apiVersion: v1
kind: Service
metadata:
  name: registry
  namespace: %s
spec:
  type: NodePort
  selector:
    app: registry
  ports:
  - port: 5000
    targetPort: 5000
    nodePort: %d
`, registryNamespace, registryImage, registryNamespace, indent(configYaml, 4), registryNamespace, registryNodePort)

	// Apply via kubectl
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", nil, fmt.Errorf("kubectl apply registry: %w\n%s", err, string(out))
	}

	// Wait for registry pod ready
	if err := waitForPodReady(ctx, clientset, registryNamespace, "app=registry", 2*time.Minute); err != nil {
		return "", nil, fmt.Errorf("wait for registry pod: %w", err)
	}

	// Verify registry HTTPS endpoint (use --insecure for self-signed cert)
	if err := waitForHTTPReady(ctx, fmt.Sprintf("https://%s/v2/", registryAddr), 30*time.Second); err != nil {
		return "", nil, fmt.Errorf("wait for registry HTTPS: %w", err)
	}

	cleanup := func() {
		_ = clientset.CoreV1().Namespaces().Delete(ctx, registryNamespace, metav1.DeleteOptions{})
	}

	return registryAddr, cleanup, nil
}

// indent prepends n spaces to each line in s.
func indent(s string, n int) string {
	prefix := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	result := ""
	for _, line := range lines {
		result += prefix + line + "\n"
	}
	return result
}
