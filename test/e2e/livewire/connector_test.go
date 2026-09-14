package livewire

import (
	"strings"
	"testing"
)

// TestWriteIdempotencyKeyIsScopedToTheRun pins the property whose absence made a
// repeated run apply nothing. The emergency stage always asks for the same
// replica count, so a key built only from the parameters is byte-identical on the
// second run; the server then answers with the first run's terminal operation as
// an ADR-009 replay and the cluster is never changed, while the stage still sees
// a succeeded operation (real smoke 2026-09-11).
func TestWriteIdempotencyKeyIsScopedToTheRun(t *testing.T) {
	t.Parallel()

	first := (&Connector{}).WithRunID("local-run-1")
	second := (&Connector{}).WithRunID("local-run-2")

	a := first.writeKey("emergency-set-replicas", "def-1", "2")
	b := second.writeKey("emergency-set-replicas", "def-1", "2")
	if a == b {
		t.Fatalf("two runs produced the same key %q; the second run would replay the first", a)
	}
	// A retry inside one run must still dedupe onto the same key.
	if replay := first.writeKey("emergency-set-replicas", "def-1", "2"); replay != a {
		t.Fatalf("retry key = %q, want the original %q", replay, a)
	}
	// The run scope must not swallow the state qualifier: two writes inside one
	// run that start from different states are different writes.
	if first.writeKey("upgrade", "def-1", "4") == first.writeKey("upgrade", "def-1", "5") {
		t.Fatal("the starting state no longer distinguishes two writes inside one run")
	}
	for _, key := range []string{a, first.writeKey("upgrade", "def-1", "4")} {
		if !strings.Contains(key, "local-run-1") {
			t.Fatalf("key %q carries no run scope", key)
		}
	}
}

// TestWriteIdempotencyKeyWithoutARunKeepsTheParameterOnlyShape keeps the unscoped
// shape for callers that never declare a run, so the in-process harness keys stay
// exactly as its assertions spell them.
func TestWriteIdempotencyKeyWithoutARunKeepsTheParameterOnlyShape(t *testing.T) {
	t.Parallel()

	if got := (&Connector{}).writeKey("upgrade", "def-1", "4"); got != "e2e-upgrade-def-1-4" {
		t.Fatalf("writeKey() = %q, want the unscoped parameter-only shape", got)
	}
}
