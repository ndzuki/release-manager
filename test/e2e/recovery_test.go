package e2e_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ndzuki/release-manager/test/e2e"
)

func TestRecoveryLedgerPersistsStateTransitions(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "recovery.json")
	ledger, err := e2e.NewRecoveryLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := e2e.LedgerEntry{RunID: "run-a", OperationID: "op-a", Seq: 1}
	if err := ledger.Prepare(entry); err != nil {
		t.Fatal(err)
	}
	for _, step := range []func(string) error{
		ledger.MarkAttempted,
		ledger.MarkUnknown,
	} {
		if err := step("op-a"); err != nil {
			t.Fatal(err)
		}
	}
	if err := ledger.Reconcile("op-a", e2e.Applied); err != nil {
		t.Fatal(err)
	}
	if err := ledger.BeginCompensation("op-a"); err != nil {
		t.Fatal(err)
	}
	if err := ledger.MarkCompensated("op-a"); err != nil {
		t.Fatal(err)
	}

	reopened, err := e2e.OpenRecoveryLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Entry("op-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != e2e.Compensated {
		t.Fatalf("status = %s, want compensated", got.Status)
	}
	if got.RunID != "run-a" || got.Seq != 1 {
		t.Fatalf("entry identity changed: %+v", got)
	}
}

func TestRecoveryLedgerRejectsInvalidTransitionAndCorruption(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "recovery.json")
	ledger, err := e2e.NewRecoveryLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Prepare(e2e.LedgerEntry{RunID: "run-a", OperationID: "op-a"}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.MarkApplied("op-a"); !errors.Is(err, e2e.ErrLedgerTransition) {
		t.Fatalf("invalid transition = %v, want ErrLedgerTransition", err)
	}
	if err := ledger.MarkAttempted("missing"); !errors.Is(err, e2e.ErrLedgerNotFound) {
		t.Fatalf("missing entry = %v, want ErrLedgerNotFound", err)
	}

	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e2e.NewRecoveryLedger(path); !errors.Is(err, e2e.ErrLedgerCorrupt) {
		t.Fatalf("corrupt ledger = %v, want ErrLedgerCorrupt", err)
	}
}
