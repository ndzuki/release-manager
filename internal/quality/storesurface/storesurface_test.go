package storesurface

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

func exception(iface, name, reason, reviewBy string) Exception {
	return Exception{Interface: iface, Method: name, Reason: reason, ReviewBy: reviewBy}
}

// A dead method with no entry is the gate's core signal.
func TestValidateReportsUnregisteredDeadMethod(t *testing.T) {
	findings := Validate([]Method{Method{Interface: "AStore", Name: "Dead"}}, nil, fixedNow, nil)
	if len(findings) != 1 || !strings.Contains(findings[0], "no exception entry") {
		t.Fatalf("want one unregistered finding, got %#v", findings)
	}
}

// A registered method passes, and its review date is surfaced.
func TestValidateAcceptsRegisteredDeadMethod(t *testing.T) {
	dates := map[string]string{}
	findings := Validate(
		[]Method{Method{Interface: "AStore", Name: "Dead"}},
		[]Exception{exception("AStore", "Dead", "[unwired] pending batch review", "2026-12-31")},
		fixedNow, dates,
	)
	if len(findings) != 0 {
		t.Fatalf("registered dead method must pass, got %#v", findings)
	}
	if dates["AStore.Dead"] != "2026-12-31" {
		t.Fatalf("review date not recorded: %#v", dates)
	}
}

// The interface name is part of the key: an entry cannot register a different interface's
// same-named method and silently cover this one.
func TestValidateKeysOnInterfaceAndMethod(t *testing.T) {
	findings := Validate(
		[]Method{Method{Interface: "AStore", Name: "Dead"}},
		[]Exception{exception("BStore", "Dead", "[unwired] pending batch review", "2026-12-31")},
		fixedNow, nil,
	)
	if len(findings) != 2 {
		t.Fatalf("want a stale entry AND a missing entry, got %#v", findings)
	}
	if !strings.Contains(strings.Join(findings, "\n"), "stale") {
		t.Fatalf("entries that do not match a dead method must be stale: %#v", findings)
	}
}

func TestValidateRejectsExpiredEntry(t *testing.T) {
	findings := Validate(
		[]Method{Method{Interface: "AStore", Name: "Dead"}},
		[]Exception{exception("AStore", "Dead", "[unwired] pending batch review", "2026-01-01")},
		fixedNow, nil,
	)
	if len(findings) != 1 || !strings.Contains(findings[0], "has passed") {
		t.Fatalf("expired entry must fail, got %#v", findings)
	}
}

func TestValidateRejectsMissingReason(t *testing.T) {
	findings := Validate(
		[]Method{Method{Interface: "AStore", Name: "Dead"}},
		[]Exception{exception("AStore", "Dead", "  ", "2026-12-31")},
		fixedNow, nil,
	)
	if len(findings) != 1 || !strings.Contains(findings[0], "needs a reason") {
		t.Fatalf("missing reason must fail, got %#v", findings)
	}
}

func TestValidateRejectsStaleEntryForAliveMethod(t *testing.T) {
	findings := Validate(nil, []Exception{exception("AStore", "Get", "[unwired] pending", "2026-12-31")}, fixedNow, nil)
	if len(findings) != 1 || !strings.Contains(findings[0], "stale") {
		t.Fatalf("an entry for a called method must fail, got %#v", findings)
	}
}

// The synthetic module below is what a text search cannot get right, and what structural
// types.Implements gets wrong as well: CStore's method set is a subset of AStore's, so the
// concrete implementer of AStore implements CStore too, and a call on it must not keep
// CStore.Ping alive (review's counter-example, kept as a regression test).
func writeModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/fake\n\ngo 1.27\n",
		"internal/store/store.go": `package store

import "context"

type AStore interface {
	Ping(ctx context.Context) error
	ConcreteOnly(ctx context.Context) error
}

type BStore interface {
	Ping(ctx context.Context) error
	Dead(ctx context.Context) error
}

// CStore's method set is a strict subset of AStore's: the same concrete type implements both.
type CStore interface {
	Ping(ctx context.Context) error
}

type AStoreImpl struct{}

// NewAStore binds *AStoreImpl to AStore, which is what makes its concrete calls count.
func NewAStore() AStore { return &AStoreImpl{} }

func (s *AStoreImpl) Ping(ctx context.Context) error         { return nil }
func (s *AStoreImpl) ConcreteOnly(ctx context.Context) error { return nil }
`,
		// AStore.Ping is called through the interface.
		"internal/app/app.go": `package app

import (
	"context"

	"example.com/fake/internal/store"
)

func use(a store.AStore) error { return a.Ping(context.Background()) }
`,
		// A call on the concrete type bound to AStore keeps AStore.ConcreteOnly alive, and the
		// same call shape must NOT keep the subset interface's CStore.Ping alive.
		"internal/app/concrete.go": `package app

import (
	"context"

	"example.com/fake/internal/store"
)

func useConcrete() error { return (&store.AStoreImpl{}).ConcreteOnly(context.Background()) }

func useConcretePing() error { return (&store.AStoreImpl{}).Ping(context.Background()) }
`,
		// Comments and string literals must not keep BStore.Dead alive.
		"internal/app/comment.go": `package app

// x.Dead( is how you would call it.
const deadMention = ".Dead("
`,
		// A call that exists only in a test file is not a shipping call site.
		"internal/app/app_test.go": `package app

import (
	"context"

	"example.com/fake/internal/store"
)

func helper(b store.BStore) error { return b.Dead(context.Background()) }
`,
	}
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return root
}

func TestAnalyzeIsTypeBased(t *testing.T) {
	root := writeModule(t)
	analysis, err := Analyze(root, "example.com/fake/internal/store")
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if analysis.Interfaces != 3 || analysis.Declared != 5 {
		t.Fatalf("want 3 interfaces / 5 declarations, got %d / %d", analysis.Interfaces, analysis.Declared)
	}
	dead := map[string]bool{}
	for _, m := range analysis.Dead {
		dead[m.Interface+"."+m.Name] = true
	}
	for _, alive := range []string{"AStore.Ping", "AStore.ConcreteOnly"} {
		if dead[alive] {
			t.Errorf("%s has a call site and must not be dead", alive)
		}
	}
	for _, wantDead := range []string{"BStore.Ping", "BStore.Dead", "CStore.Ping"} {
		if !dead[wantDead] {
			t.Errorf("%s has no attributable call site and must stay dead", wantDead)
		}
	}
}
