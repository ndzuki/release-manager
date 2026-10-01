package authorization

import "errors"

// StaleCause is the bounded, non-sensitive reason an authorization snapshot was
// rejected as stale. It exists because fail-closed rejections used to collapse every
// pull failure into one constant ("authorization snapshot stale"), so a CI run could
// not tell a warm-up apart from a 200ms snapshot RPC timeout (TASK-160/TASK-239).
//
// The values are an enum on purpose: they become the `cause` label of
// auth_snapshot_stale_cause_total and the X-Stale-Cause metadata header, so free text
// (and anything derived from a raw pull error, which may carry credentials) must never
// reach them.
type StaleCause string

const (
	// StaleCauseWarmup is the healthy-path first pull: the module has no persisted
	// checkpoint yet, so `changed` is necessarily true once.
	StaleCauseWarmup StaleCause = "warmup"
	// StaleCauseGap is a checkpoint that skips forward by more than one version.
	StaleCauseGap StaleCause = "gap"
	// StaleCauseRegression is a snapshot older than the persisted checkpoint.
	StaleCauseRegression StaleCause = "regression"
	// StaleCauseNotFresh is a snapshot that is present but not fresh, or a cache entry
	// that has not been initialized yet.
	StaleCauseNotFresh StaleCause = "not-fresh"
	// StaleCauseSnapshotRPCTimeout is the snapshot RPC exceeding snapshotDeadline.
	StaleCauseSnapshotRPCTimeout StaleCause = "snapshot-rpc-timeout"
	// StaleCauseSnapshotRPCError is any other snapshot RPC failure.
	StaleCauseSnapshotRPCError StaleCause = "snapshot-rpc-error"
	// StaleCauseScopeMismatch is a snapshot answering for another scope/actor.
	StaleCauseScopeMismatch StaleCause = "snapshot-scope-mismatch"
	// StaleCauseCheckpointRead is a failed checkpoint read.
	StaleCauseCheckpointRead StaleCause = "checkpoint-read-error"
	// StaleCauseCheckpointWrite is a failed checkpoint write.
	StaleCauseCheckpointWrite StaleCause = "checkpoint-write-error"
	// StaleCauseUnknown is the bounded fallback for a rejection with no attributed
	// cause; it keeps the label cardinality capped.
	StaleCauseUnknown StaleCause = "unknown"
)

// StaleCauses returns every accepted label value. Tests assert that the metric never
// carries anything outside this set (AC-239-02).
func StaleCauses() []StaleCause {
	return []StaleCause{
		StaleCauseWarmup,
		StaleCauseGap,
		StaleCauseRegression,
		StaleCauseNotFresh,
		StaleCauseSnapshotRPCTimeout,
		StaleCauseSnapshotRPCError,
		StaleCauseScopeMismatch,
		StaleCauseCheckpointRead,
		StaleCauseCheckpointWrite,
		StaleCauseUnknown,
	}
}

// Valid reports whether the cause is part of the enum.
func (c StaleCause) Valid() bool {
	for _, known := range StaleCauses() {
		if c == known {
			return true
		}
	}
	return false
}

// staleCauseError attaches a cause to the stable stale error without changing what the
// caller returns or how reasonCode reads it: Unwrap keeps errors.As(*connect.Error)
// working, so the wire contract (X-Reason-Code, codes, meta) is untouched.
type staleCauseError struct {
	cause StaleCause
	err   error
}

func (e *staleCauseError) Error() string { return e.err.Error() }

func (e *staleCauseError) Unwrap() error { return e.err }

// withStaleCause annotates a stale error with its cause.
func withStaleCause(err error, cause StaleCause) error {
	if err == nil {
		return nil
	}
	if !cause.Valid() {
		cause = StaleCauseUnknown
	}
	return &staleCauseError{cause: cause, err: err}
}

// staleCauseOf extracts the attributed cause; ok is false when the error carries none.
func staleCauseOf(err error) (StaleCause, bool) {
	var withCause *staleCauseError
	if errors.As(err, &withCause) {
		return withCause.cause, true
	}
	return StaleCauseUnknown, false
}
