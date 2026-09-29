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

// The synthetic module below is what a text search cannot get right: two interfaces declare a
// method with the same name, a comment and a string literal mention a third, a concrete
// implementer is called directly, and one call exists only in a test file.
func writeModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/fake\n\ngo 1.27\n",
		"internal/store/store.go": `package store

import "context"

type AStore interface {
	Ping(ctx context.Context) error
	Dead(ctx context.Context) error
}

type BStore interface {
	Ping(ctx context.Context) error
	AlsoDead(ctx context.Context) error
}

type AStoreImpl struct{}

func NewAStore() AStore { return &AStoreImpl{} }

func (s *AStoreImpl) Ping(ctx context.Context) error     { return nil }
func (s *AStoreImpl) Dead(ctx context.Context) error     { return nil }
func (s *AStoreImpl) AlsoDead(ctx context.Context) error { return nil }
`,
		// AStore.Ping is called through the interface; BStore.Ping shares the name and must
		// stay dead.
		"internal/app/app.go": `package app

import (
	"context"

	"example.com/fake/internal/store"
)

func use(a store.AStore) error { return a.Ping(context.Background()) }
`,
		// Comments and string literals must not keep Dead alive.
		"internal/app/comment.go": `package app

// x.Dead( is how you would call it.
const deadMention = ".Dead("
`,
		// A concrete implementer call keeps a method alive: this is how the store package calls
		// its own types inside a unit of work.
		"internal/app/concrete.go": `package app

import (
	"context"

	"example.com/fake/internal/store"
)

func useConcrete() error { return (&store.AStoreImpl{}).AlsoDead(context.Background()) }
`,
		// A call that exists only in a test file is not a shipping call site.
		"internal/app/app_test.go": `package app

import (
	"context"

	"example.com/fake/internal/store"
)

func helper(a store.AStore) error { return a.Dead(context.Background()) }
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
	if analysis.Interfaces != 2 || analysis.Declared != 4 {
		t.Fatalf("want 2 interfaces / 4 declarations, got %d / %d", analysis.Interfaces, analysis.Declared)
	}
	dead := map[string]bool{}
	for _, m := range analysis.Dead {
		dead[m.Interface+"."+m.Name] = true
	}
	if dead["AStore.Ping"] {
		t.Error("AStore.Ping is called through the interface")
	}
	if dead["BStore.AlsoDead"] {
		t.Error("BStore.AlsoDead is called on a concrete implementer")
	}
	if !dead["AStore.Dead"] {
		t.Error("AStore.Dead is only called from a test file and must stay dead")
	}
	if !dead["BStore.Ping"] {
		t.Error("BStore.Ping shares its name with AStore.Ping but is never called")
	}
}
