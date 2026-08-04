package observer

import (
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestObserver_JobReady(t *testing.T) {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "migrate",
			Namespace:       "default",
			UID:             "job-uid",
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

	result, err := New(fake.NewSimpleClientset(job)).Observe(t.Context(), jobRef(), 0, time.Second)

	require.NoError(t, err)
	assert.True(t, result.Ready)
	assert.False(t, result.Failed)
	assert.Equal(t, job.UID, result.ResourceUID)
	assert.Equal(t, int64(3), result.Generation)
	assert.Zero(t, result.ObservedGeneration)
	assert.Equal(t, "31", result.ResourceVersion)
	require.Len(t, result.Conditions, 1)
	assert.Equal(t, string(batchv1.JobComplete), result.Conditions[0].Type)
	assert.Equal(t, string(corev1.ConditionTrue), result.Conditions[0].Status)
}

func TestObserver_JobRejectsExpectedGeneration(t *testing.T) {
	client := fake.NewSimpleClientset()

	result, err := New(client).Observe(t.Context(), jobRef(), 1, time.Second)

	assert.ErrorIs(t, err, ErrInvalidArgument)
	assert.Equal(t, ErrorCodeInvalidArgument, rolloutErrorCode(t, err))
	assert.Equal(t, jobRef(), result.Resource)
	assert.Empty(t, client.Actions())
}

func TestObserver_JobFailed(t *testing.T) {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "migrate",
			Namespace:       "default",
			UID:             "job-uid",
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

	result, err := New(fake.NewSimpleClientset(job)).Observe(t.Context(), jobRef(), 0, time.Second)

	assert.ErrorIs(t, err, ErrWorkloadUnavailable)
	assert.Equal(t, ErrorCodeWorkloadUnavailable, rolloutErrorCode(t, err))
	assert.False(t, result.Ready)
	assert.True(t, result.Failed)
	assert.Equal(t, job.UID, result.ResourceUID)
	assert.Equal(t, int64(4), result.Generation)
	assert.Zero(t, result.ObservedGeneration)
	require.Len(t, result.Conditions, 1)
	assert.Equal(t, string(batchv1.JobFailed), result.Conditions[0].Type)
	assert.Equal(t, string(corev1.ConditionTrue), result.Conditions[0].Status)
	assert.Equal(t, "BackoffLimitExceeded", result.Conditions[0].Reason)
	assert.Equal(t, "job has reached the specified backoff limit", result.Conditions[0].Message)
	assertRolloutLast(t, result, err)
}

func TestObserver_JobFailureOverridesComplete(t *testing.T) {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "migrate",
			Namespace:       "default",
			UID:             "job-uid",
			Generation:      5,
			ResourceVersion: "34",
		},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue},
			},
		},
	}

	result, err := New(fake.NewSimpleClientset(job)).Observe(t.Context(), jobRef(), 0, time.Second)

	assert.ErrorIs(t, err, ErrWorkloadUnavailable)
	assert.False(t, result.Ready)
	assert.True(t, result.Failed)
	assertRolloutLast(t, result, err)
}

func jobRef() ResourceRef {
	return ResourceRef{
		GVR:       schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"},
		Namespace: "default",
		Name:      "migrate",
	}
}
