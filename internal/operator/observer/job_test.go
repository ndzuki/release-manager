package observer

import (
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestObserver_JobReady(t *testing.T) {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "migrate",
			Namespace:       "default",
			Generation:      3,
			ResourceVersion: "31",
		},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{{
				Type:   batchv1.JobComplete,
				Status: corev1.ConditionTrue,
			}},
		},
	}

	result, err := New(fake.NewSimpleClientset(job)).Observe(t.Context(), jobRef(), 3, time.Second)

	require.NoError(t, err)
	assert.True(t, result.Ready)
	assert.Equal(t, int64(3), result.ObservedGeneration)
	assert.Equal(t, "31", result.ResourceVersion)
	require.Len(t, result.Conditions, 1)
	assert.Equal(t, "Complete", result.Conditions[0].Type)
}

func TestObserver_JobWaitsForExpectedGeneration(t *testing.T) {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "migrate",
			Namespace:       "default",
			Generation:      2,
			ResourceVersion: "32",
		},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{{
				Type:   batchv1.JobComplete,
				Status: corev1.ConditionTrue,
			}},
		},
	}

	result, err := New(fake.NewSimpleClientset(job)).Observe(t.Context(), jobRef(), 3, 20*time.Millisecond)

	assert.ErrorIs(t, err, ErrRolloutTimeout)
	assert.False(t, result.Ready)
	assert.Equal(t, int64(2), result.ObservedGeneration)
}

func TestObserver_JobFailed(t *testing.T) {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "migrate",
			Namespace:       "default",
			Generation:      4,
			ResourceVersion: "33",
		},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{{
				Type:    batchv1.JobFailed,
				Status:  corev1.ConditionTrue,
				Reason:  "BackoffLimitExceeded",
				Message: "job has reached the specified backoff limit",
			}},
		},
	}

	result, err := New(fake.NewSimpleClientset(job)).Observe(t.Context(), jobRef(), 4, time.Second)

	assert.ErrorIs(t, err, ErrWorkloadUnavailable)
	assert.False(t, result.Ready)
	assert.True(t, result.Failed)
	require.Len(t, result.Conditions, 1)
	assert.Equal(t, "BackoffLimitExceeded", result.Conditions[0].Reason)
}

func jobRef() ResourceRef {
	return ResourceRef{GVR: JobGVR, Namespace: "default", Name: "migrate"}
}
