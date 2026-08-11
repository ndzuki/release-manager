package observer

import (
	"errors"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

var (
	ErrInvalidArgument     = errors.New("invalid_argument")
	ErrWatchDisconnected   = errors.New("watch_disconnected")
	ErrRolloutTimeout      = errors.New("rollout_timeout")
	ErrCancelled           = errors.New("cancelled")
	ErrWorkloadUnavailable = errors.New("workload_unavailable")
	ErrUnsupportedResource = errors.New("unsupported_resource")
)

var (
	DeploymentGVR  = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	StatefulSetGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}
	DaemonSetGVR   = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}
	JobGVR         = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
)

type ErrorCode string

const (
	ErrorCodeInvalidArgument     ErrorCode = "invalid_argument"
	ErrorCodeUnsupportedResource ErrorCode = "unsupported_resource"
	ErrorCodeWorkloadUnavailable ErrorCode = "workload_unavailable"
	ErrorCodeRolloutTimeout      ErrorCode = "rollout_timeout"
	ErrorCodeCancelled           ErrorCode = "cancelled"
	ErrorCodeWatchDisconnected   ErrorCode = "watch_disconnected"
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
	ResourceUID        types.UID
	Generation         int64
	ObservedGeneration int64
	ResourceVersion    string
	Ready              bool
	Failed             bool
	Conditions         []Condition
}

type RolloutError struct {
	Kind  error
	Last  WatchResult
	Err   error
	cause error
}

func (e *RolloutError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return e.Kind.Error()
	}
	return e.Err.Error()
}

func (e *RolloutError) Unwrap() error {
	return e.Kind
}

func (e *RolloutError) Cause() error {
	if e.cause != nil {
		return e.cause
	}
	return e.Err
}

func (e *RolloutError) Code() ErrorCode {
	if e == nil {
		return ""
	}
	switch {
	case errors.Is(e.Kind, ErrInvalidArgument):
		return ErrorCodeInvalidArgument
	case errors.Is(e.Kind, ErrUnsupportedResource):
		return ErrorCodeUnsupportedResource
	case errors.Is(e.Kind, ErrWorkloadUnavailable):
		return ErrorCodeWorkloadUnavailable
	case errors.Is(e.Kind, ErrRolloutTimeout):
		return ErrorCodeRolloutTimeout
	case errors.Is(e.Kind, ErrCancelled):
		return ErrorCodeCancelled
	default:
		return ErrorCodeWatchDisconnected
	}
}
