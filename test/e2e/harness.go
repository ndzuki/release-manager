//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	defaultClusterName = "release-e2e-test"
	defaultCustomerID  = "localhost001"
)

var testHarness *Harness

// fatalf panics or calls t.Fatalf. Use when t may be nil (TestMain context).
func fatalf(t *testing.T, format string, args ...interface{}) {
	if t != nil {
		t.Fatalf(format, args...)
	} else {
		panic(fmt.Sprintf(format, args...))
	}
}

// logf logs via t.Logf or stdout when t is nil (TestMain context).
func logf(t *testing.T, format string, args ...interface{}) {
	if t != nil {
		t.Logf(format, args...)
	} else {
		fmt.Printf(format+"\n", args...)
	}
}

// Harness provides the full E2E test environment.
type Harness struct {
	T *testing.T

	ClusterName string
	Kubeconfig  string
	K8sClient   kubernetes.Interface

	// Endpoints
	RegistryAddr         string // host-accessible address (localhost:30500)
	RegistryInternalAddr string // cluster-internal address (registry.registry:5000)
	ManagerHTTP          string
	ManagerGRPC          string

	// mTLS
	CustomerID  string
	CABundle    []byte
	ClientCert  tls.Certificate
	Fingerprint string

	// Cert paths
	caFile   string
	certFile string
	keyFile  string

	// Config
	hmacKey string

	// Cleanup stack (LIFO)
	cleanupFns []func()
	mu         sync.Mutex
	closeOnce  sync.Once
}

// newHarnessInternal creates the full E2E environment. Use via SetupTest or TestMain.
func newHarnessInternal() *Harness {
	h := &Harness{
		T:           nil,
		ClusterName: defaultClusterName,
		CustomerID:  defaultCustomerID,
		hmacKey:     "e2e-test-hmac-secret",
	}
	testHarness = h // set immediately so TestMain's defer/recover can access h.Close()

	ctx := context.Background()

	// Step 0: Check for cluster reuse
	reuseCluster := os.Getenv("KIND_CLUSTER_REUSE")
	if reuseCluster != "" {
		h.ClusterName = reuseCluster
		logf(h.T, "Reusing existing kind cluster: %s", h.ClusterName)
	} else {
		// Step 1: Create kind cluster
		logf(h.T, "Creating kind cluster...")
		if err := createKindCluster(h.ClusterName); err != nil {
			fatalf(h.T, "create kind cluster: %v", err)
		}
		h.addCleanup(func() {
			if os.Getenv("KEEP_CLUSTER") == "1" {
				logf(h.T, "Keeping cluster: %s", h.ClusterName)
				return
			}
			logf(h.T, "Deleting kind cluster...")
			_ = deleteKindCluster(h.ClusterName)
		})
	}

	// Step 2: Get kubeconfig
	var err error
	h.Kubeconfig, err = kindKubeconfig(h.ClusterName)
	if err != nil {
		fatalf(h.T, "get kubeconfig: %v", err)
	}
	os.Setenv("KUBECONFIG", h.Kubeconfig)
	h.addCleanup(func() { os.Remove(h.Kubeconfig) })

	restCfg, err := clientcmd.BuildConfigFromFlags("", h.Kubeconfig)
	if err != nil {
		fatalf(h.T, "build rest config: %v", err)
	}

	h.K8sClient, err = kubernetes.NewForConfig(restCfg)
	if err != nil {
		fatalf(h.T, "create k8s client: %v", err)
	}

	// Step 3: Determine Harbor mode
	harborURL := os.Getenv("HARBOR_URL")
	harborUser := os.Getenv("HARBOR_ROBOT_NAME")
	harborPass := os.Getenv("HARBOR_ROBOT_TOKEN")

	registryMode := "standalone"
	if harborURL != "" && harborPass != "" {
		registryMode = "proxy"
		logf(h.T, "Harbor mode: proxy to %s", harborURL)
	} else {
		logf(h.T, "Harbor mode: standalone registry")
	}

	// Step 4: Deploy registry
	logf(h.T, "Deploying registry...")
	h.RegistryAddr, _, err = deployRegistry(ctx, h.K8sClient, h.ClusterName, registryMode, harborURL, harborUser, harborPass)
	h.RegistryInternalAddr = "registry.registry:5000" // cluster-internal (HTTPS with self-signed cert)
	if err != nil {
		fatalf(h.T, "deploy registry: %v", err)
	}
	h.addCleanup(func() {
		_ = h.K8sClient.CoreV1().Namespaces().Delete(ctx, "registry", metav1.DeleteOptions{})
	})

	// Step 5: Generate certs
	logf(h.T, "Generating mTLS certificates...")
	h.caFile, h.certFile, h.keyFile, h.Fingerprint, err = generateCerts(ctx, h.CustomerID)
	if err != nil {
		fatalf(h.T, "generate certs: %v", err)
	}
	certDir := filepath.Dir(h.caFile)
	h.addCleanup(func() { os.RemoveAll(certDir) })

	h.CABundle, err = os.ReadFile(h.caFile)
	if err != nil {
		fatalf(h.T, "read CA file %s: %v", h.caFile, err)
	}
	h.ClientCert, err = tls.LoadX509KeyPair(h.certFile, h.keyFile)
	if err != nil {
		fatalf(h.T, "load client cert pair (%s, %s): %v", h.certFile, h.keyFile, err)
	}

	// Step 6: Deploy manager
	logf(h.T, "Deploying manager...")
	dingtalkURL := os.Getenv("E2E_DINGTALK_URL")
	var managerCleanup func()
	h.ManagerHTTP, h.ManagerGRPC, managerCleanup, err = deployManager(ctx, h.K8sClient, h.ClusterName, h.caFile, h.certFile, h.keyFile, []string{h.Fingerprint}, h.hmacKey, dingtalkURL)
	if err != nil {
		fatalf(h.T, "deploy manager: %v", err)
	}
	h.addCleanup(managerCleanup)

	// Step 7: Deploy operator first (need its ClusterIP for customer registration)
	logf(h.T, "Deploying operator...")
	operatorCleanup, err := deployOperator(ctx, h.K8sClient, h.ClusterName, h.CustomerID,
		"release-manager.release-manager:8443", h.caFile, h.certFile, h.keyFile)
	if err != nil {
		fatalf(h.T, "deploy operator: %v", err)
	}
	h.addCleanup(operatorCleanup)

	// Step 8: Register customer using operator ClusterIP (avoids TLS SNI issues)
	logf(h.T, "Registering customer...")
	opNS := fmt.Sprintf("release-operator-%s", h.CustomerID)
	opSvc := fmt.Sprintf("release-operator-%s", h.CustomerID)
	opIP, err := getServiceClusterIP(ctx, h.K8sClient, opNS, opSvc)
	if err != nil {
		fatalf(h.T, "get operator ClusterIP: %v", err)
	}
	if err := h.registerCustomer(ctx, opIP); err != nil {
		fatalf(h.T, "register customer: %v", err)
	}

	return h
}

// getServiceClusterIP returns the ClusterIP of a Kubernetes service.
func getServiceClusterIP(ctx context.Context, clientset kubernetes.Interface, namespace, name string) (string, error) {
	svc, err := clientset.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get service %s/%s: %w", namespace, name, err)
	}
	return svc.Spec.ClusterIP, nil
}

// Close runs all cleanup functions in reverse order.
// Resets h.T to nil first so cleanup logging via logf uses fmt.Printf
// instead of t.Logf (which panics after test completion).
func (h *Harness) Close() {
	h.closeOnce.Do(func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.T = nil // prevent t.Logf panic in cleanup after test completion
		for i := len(h.cleanupFns) - 1; i >= 0; i-- {
			h.cleanupFns[i]()
		}
	})
}

// SetupTest returns the global Harness for a single test. TestMain must call newHarnessInternal() first.
// It wires the test's *testing.T into the harness so that logging and DumpState work correctly.
func SetupTest(t *testing.T) *Harness {
	t.Helper()
	if testHarness == nil {
		t.Fatal("testHarness is nil — TestMain must call newHarnessInternal()")
	}
	testHarness.T = t
	return testHarness
}

func (h *Harness) addCleanup(fn func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cleanupFns = append(h.cleanupFns, fn)
}

// registerCustomer registers the test customer using the given endpoint.
func (h *Harness) registerCustomer(ctx context.Context, endpoint string) error {
	return h.RegisterCustomer(ctx, h.CustomerID, "E2E Test Customer",
		fmt.Sprintf("%s:8443", endpoint),
		h.Fingerprint, true)
}

// RegisterCustomer registers a customer with the manager API.
func (h *Harness) RegisterCustomer(ctx context.Context, id, name, endpoint, fingerprint string, enabled bool) error {
	body := map[string]interface{}{
		"id":                id,
		"name":              name,
		"operator_endpoint": endpoint,
		"cert_fingerprint":  fingerprint,
		"enabled":           enabled,
	}
	payload, _ := json.Marshal(body)

	req, _ := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("http://%s/api/v1/customers", h.ManagerHTTP),
		bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "e2e-test-key")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("register customer %s: %w", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("register customer %s: status %d: %s", id, resp.StatusCode, string(body))
	}
	return nil
}

// TriggerWebhook sends a Harbor webhook payload to the manager.
// In Harbor mode, it includes HMAC signature. In standalone mode, no signature.
func (h *Harness) TriggerWebhook(ctx context.Context, chartName, version string) error {
	payload := map[string]interface{}{
		"type":      "PUSH_HELMCHART",
		"occur_at":  time.Now().Unix(),
		"operator":  "e2e-test",
		"event_data": map[string]interface{}{
			"resources": []map[string]interface{}{
				{
					"tag":          version,
					"resource_url": fmt.Sprintf("oci://%s/helm/%s", h.RegistryInternalAddr, chartName),
				},
			},
			"repository": map[string]interface{}{
				"name":      fmt.Sprintf("helm/%s", chartName),
				"namespace": "helm",
				"repo_type": "CHART",
			},
		},
	}

	body, _ := json.Marshal(payload)

	req, _ := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("http://%s/api/v1/webhook/harbor", h.ManagerHTTP),
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	// Add HMAC if key is set
	if h.hmacKey != "" {
		mac := hmac.New(sha256.New, []byte(h.hmacKey))
		mac.Write(body)
		sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
		req.Header.Set("Authorization", "Harbor-Signature "+sig)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("trigger webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("webhook returned %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// ReleaseRecord represents a release from the manager API.
type ReleaseRecord struct {
	ID         string `json:"id"`
	CustomerID string `json:"customer_id"`
	ChartName  string `json:"chart_name"`
	Version    string `json:"version"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

// GetReleases fetches releases from the manager API for a customer.
func (h *Harness) GetReleases(ctx context.Context, customerID string) ([]ReleaseRecord, error) {
	url := fmt.Sprintf("http://%s/api/v1/releases", h.ManagerHTTP)
	if customerID != "" {
		url += "?customer_id=" + customerID
	}

	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("X-API-Key", "e2e-test-key")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get releases: %w", err)
	}
	defer resp.Body.Close()

	var releases []ReleaseRecord
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("decode releases: %w", err)
	}

	return releases, nil
}

// WaitForReleaseStatus polls until a release for the given chart reaches expectedStatus.
func (h *Harness) WaitForReleaseStatus(ctx context.Context, customerID, chartName, expectedStatus string, timeout time.Duration) error {
	return retryUntil(ctx, 5*time.Second, timeout, func() (bool, error) {
		releases, err := h.GetReleases(ctx, customerID)
		if err != nil {
			fmt.Printf("[WaitForReleaseStatus] transient error from GetReleases: %v\n", err)
			return false, nil
		}
		for _, r := range releases {
			if r.ChartName == "helm/"+chartName && r.Status == expectedStatus {
				return true, nil
			}
		}
		return false, nil
	})
}

// DumpState collects diagnostic info on test failure.
func (h *Harness) DumpState() {
	if h.T == nil || !h.T.Failed() {
		return
	}

	h.T.Log("=== BEGIN STATE DUMP ===")
	ctx := context.Background()

	// Pods
	pods, _ := h.K8sClient.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	for _, p := range pods.Items {
		h.T.Logf("  Pod/%s/%s: %s", p.Namespace, p.Name, p.Status.Phase)
	}

	// Releases
	releases, _ := h.GetReleases(ctx, "")
	for _, r := range releases {
		h.T.Logf("  Release/%s: chart=%s version=%s status=%s error=%s",
			r.CustomerID, r.ChartName, r.Version, r.Status, r.Error)
	}

	h.T.Log("=== END STATE DUMP ===")
}
