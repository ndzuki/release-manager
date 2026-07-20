package preflight

import (
	"errors"
	"log/slog"
	"time"

	"k8s.io/client-go/kubernetes"
)

const (
	PullStateCreated   = "created"
	PullStatePulling   = "pulling"
	PullStateSucceeded = "succeeded"
	PullStateFailed    = "failed"
	PullStateTimeout   = "timeout"
	PullStateCleaned   = "cleaned"

	ErrImagePullBackOff     = "image_pull_backoff"
	ErrRegistryUnauthorized = "registry_unauthorized"
	ErrNetworkUnreachable   = "network_unreachable"
	ErrIAMDenied            = "iam_denied"
	ErrPullTimeout          = "timeout"
	ErrCleanupFailed        = "cleanup_failed"
	ErrPullUnknown          = "pull_unknown"

	ManagedLabel       = "release-manager.io/runtime-pull-preflight"
	OperationLabel     = "release-manager.io/operation-id"
	ImageLabel         = "release-manager.io/image-id"
	ExpireAtAnnotation = "release-manager.io/preflight-expire-at"

	DefaultPullTimeout    = 2 * time.Minute
	DefaultCleanupTimeout = 5 * time.Second
	DefaultProbeTTL       = 15 * time.Minute
	DefaultGCInterval     = time.Minute
	DefaultGCBatchSize    = 100
	MaxPullImages         = 64
	maxPullReasonBytes    = 512
)

var (
	ErrPullDisabled          = errors.New("runtime pull preflight is disabled")
	ErrPullInputInvalid      = errors.New("runtime pull preflight input is invalid")
	ErrUnpinnedImage         = errors.New("runtime pull preflight image must be digest pinned")
	ErrUntrustedProbeCommand = errors.New("runtime pull preflight command must be approved")
)

type CleanupPolicy string

const (
	CleanupAlways     CleanupPolicy = "always"
	CleanupOnSuccess  CleanupPolicy = "on_success"
	CleanupBackground CleanupPolicy = "background"
)

type PullInput struct {
	OperationID    string        `json:"operation_id"`
	Namespace      string        `json:"namespace"`
	ServiceAccount string        `json:"service_account"`
	Images         []string      `json:"images"`
	Timeout        time.Duration `json:"timeout"`
	CleanupPolicy  CleanupPolicy `json:"cleanup_policy"`
	ProbeCommand   []string      `json:"probe_command,omitempty"`
}

type ImagePullResult struct {
	Image         string        `json:"image"`
	ProbeName     string        `json:"probe_name"`
	State         string        `json:"state"`
	Pulled        bool          `json:"pulled"`
	ErrorCode     string        `json:"error_code,omitempty"`
	Reason        string        `json:"reason,omitempty"`
	Node          string        `json:"node,omitempty"`
	CleanupFailed bool          `json:"cleanup_failed,omitempty"`
	Duration      time.Duration `json:"duration"`
}

type PullBatchResult struct {
	OperationID    string            `json:"operation_id"`
	Namespace      string            `json:"namespace"`
	ServiceAccount string            `json:"service_account"`
	Passed         bool              `json:"passed"`
	CleanupFailed  bool              `json:"cleanup_failed,omitempty"`
	Warning        string            `json:"warning,omitempty"`
	Results        []ImagePullResult `json:"results"`
	Duration       time.Duration     `json:"duration"`
}

func PullGate(result *PullBatchResult) bool {
	return result != nil && result.Passed
}

type PullProber struct {
	client         kubernetes.Interface
	logger         *slog.Logger
	now            func() time.Time
	cleanupTimeout time.Duration
	probeTTL       time.Duration
}

func NewPullProber(client kubernetes.Interface, logger *slog.Logger) *PullProber {
	if logger == nil {
		logger = slog.Default()
	}
	return &PullProber{
		client:         client,
		logger:         logger,
		now:            time.Now,
		cleanupTimeout: DefaultCleanupTimeout,
		probeTTL:       DefaultProbeTTL,
	}
}
