package e2e_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ndzuki/release-manager/test/e2e"
)

func TestProcessLockConflictCarriesOwner(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "dev.lock")
	first, err := e2e.AcquireProcessLock(path, "run-a")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := first.Release(); err != nil {
			t.Fatal(err)
		}
	}()

	second, err := e2e.AcquireProcessLock(path, "run-b")
	if second != nil {
		t.Fatal("conflicting acquisition returned a lock")
	}
	if !errors.Is(err, e2e.ErrLockUnavailable) {
		t.Fatalf("conflict error = %v, want ErrLockUnavailable", err)
	}
	var conflict *e2e.LockConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("conflict error = %T, want *LockConflictError", err)
	}
	if conflict.Owner.Owner != "run-a" {
		t.Fatalf("owner = %q, want run-a", conflict.Owner.Owner)
	}
	if conflict.Owner.PID == 0 {
		t.Fatal("owner pid was not recorded")
	}
}

func TestProcessLockReleaseIsIdempotentAndClearsMarker(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "dev.lock")
	lock, err := e2e.AcquireLock(path, "run-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("owner marker remains after release: %q", data)
	}

	again, err := e2e.AcquireProcessLock(path, "run-b")
	if err != nil {
		t.Fatal(err)
	}
	if err := again.Release(); err != nil {
		t.Fatal(err)
	}
}
