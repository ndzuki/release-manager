package observer

import (
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	ErrWatchDisconnected      = errors.New("watch_disconnected")
	ErrResourceVersionExpired = errors.New("resource_version_expired")
	ErrRolloutTimeout         = errors.New("rollout_timeout")
	ErrCancelled              = errors.New("cancelled")
	ErrWorkloadUnavailable    = errors.New("workload_unavailable")
	ErrUnsupportedResource    = errors.New("unsupported_resource")
)

var (
	DeploymentGVR  = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	StatefulSetGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}
	DaemonSetGVR   = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}
)

type ResourceRef struct {
	GVR       schema.GroupVersionResource
	Namespace string
	Name      string
}

type Condition struct {
	Type    string
	Status  string
	Reason  string
	Message string
}

type WatchResult struct {
	Resource           ResourceRef
	Ready              bool
	Failed             bool
	ObservedGeneration int64
	ResourceVersion    string
	Conditions         []Condition
}

type RolloutError struct {
	Kind error
	Last WatchResult
	Err  error
}

func (e *RolloutError) Error() string {
	if e.Err == nil {
		return e.Kind.Error()
	}
	return fmt.Sprintf("%s: %v", e.Kind, e.Err)
}

func (e *RolloutError) Unwrap() error {
	return e.Kind
}
func (e *RolloutError) Cause() error {
	return e.Err
}
