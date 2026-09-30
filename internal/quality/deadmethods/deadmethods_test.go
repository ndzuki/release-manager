package deadmethods

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

func entry(pkg, method, reason, reviewBy string) Exception {
	return Exception{Package: pkg, Receiver: "helper", Method: method, Reason: reason, ReviewBy: reviewBy}
}

const storelikePackage = "example.com/fake/internal/storelike"

// A method nothing references and no interface claims is the gate's core signal.
func TestValidateReportsUnregisteredMethod(t *testing.T) {
	findings := Validate([]Method{{Package: storelikePackage, Receiver: "helper", Name: "DeadMethod"}}, nil, fixedNow, nil)
	if len(findings) != 1 || !strings.Contains(findings[0], "no exception entry") {
		t.Fatalf("want one unregistered finding, got %#v", findings)
	}
}

func TestValidateAcceptsRegisteredMethod(t *testing.T) {
	dates := map[string]string{}
	findings := Validate(
		[]Method{{Package: storelikePackage, Receiver: "helper", Name: "DeadMethod"}},
		[]Exception{entry(storelikePackage, "DeadMethod", "[test-utility] seeded by fixtures", "2026-12-31")},
		fixedNow, dates,
	)
	if len(findings) != 0 {
		t.Fatalf("registered method must pass, got %#v", findings)
	}
	if dates[storelikePackage+":helper.DeadMethod"] != "2026-12-31" {
		t.Fatalf("review date not recorded: %#v", dates)
	}
}

// Package and receiver are part of the key: an entry for another package's same-named type
// cannot silently cover this method.
func TestValidateKeysOnPackageAndReceiver(t *testing.T) {
	findings := Validate(
		[]Method{{Package: storelikePackage, Receiver: "helper", Name: "DeadMethod"}},
		[]Exception{entry("example.com/fake/internal/other", "DeadMethod", "[unwired] pending", "2026-12-31")},
		fixedNow, nil,
	)
	if len(findings) != 2 {
		t.Fatalf("want a stale entry AND a missing entry, got %#v", findings)
	}
	if !strings.Contains(strings.Join(findings, "\n"), "stale") {
		t.Fatalf("entries that do not match must be stale: %#v", findings)
	}
}

func TestValidateRejectsExpiredEntry(t *testing.T) {
	findings := Validate(
		[]Method{{Package: storelikePackage, Receiver: "helper", Name: "DeadMethod"}},
		[]Exception{entry(storelikePackage, "DeadMethod", "[unwired] pending", "2026-01-01")},
		fixedNow, nil,
	)
	if len(findings) != 1 || !strings.Contains(findings[0], "has passed") {
		t.Fatalf("expired entry must fail, got %#v", findings)
	}
}

func TestValidateRejectsMissingReasonAndReviewDate(t *testing.T) {
	dead := []Method{{Package: storelikePackage, Receiver: "helper", Name: "DeadMethod"}}
	noReason := Validate(dead, []Exception{entry(storelikePackage, "DeadMethod", "   ", "2026-12-31")}, fixedNow, nil)
	if len(noReason) != 1 || !strings.Contains(noReason[0], "needs a reason") {
		t.Fatalf("missing reason must fail, got %#v", noReason)
	}
	noDate := Validate(dead, []Exception{entry(storelikePackage, "DeadMethod", "[unwired] pending", "")}, fixedNow, nil)
	if len(noDate) != 1 || !strings.Contains(noDate[0], "needs review_by") {
		t.Fatalf("missing review_by must fail, got %#v", noDate)
	}
}

// An entry for a method that is alive (or gone) is stale, never silently ignored.
func TestValidateRejectsStaleEntryForReferencedMethod(t *testing.T) {
	findings := Validate(nil, []Exception{entry(storelikePackage, "LiveCalled", "[unwired] pending", "2026-12-31")}, fixedNow, nil)
	if len(findings) != 1 || !strings.Contains(findings[0], "stale") {
		t.Fatalf("an entry for a referenced method must fail, got %#v", findings)
	}
}

func TestValidateRejectsEmptyEntry(t *testing.T) {
	findings := Validate(nil, []Exception{{Reason: "[unwired] pending", ReviewBy: "2026-12-31"}}, fixedNow, nil)
	if len(findings) != 1 || !strings.Contains(findings[0], "empty package") {
		t.Fatalf("an entry without package/receiver/method must fail, got %#v", findings)
	}
}

// writeModule builds a synthetic module holding one positive and one negative case per shape the
// analyzer has to get right. It deliberately splits the shapes the way a text search cannot:
// every "negative" method below is unreferenced in shipping code and survives only because of a
// type-level rule.
func writeModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/fake\n\ngo 1.27\n",
		// A third-party-style interface, in its own package: the shape Casbin/client-go present
		// in this repository.
		"internal/persist/persist.go": `package persist

import "context"

type Adapter interface {
	SavePolicy(ctx context.Context) error
}
`,
		"internal/storelike/store.go": `package storelike

import (
	"context"
	"database/sql"

	"example.com/fake/internal/persist"
)

// helper holds one genuinely dead method and one that shipping code calls.
type helper struct{}

func (h *helper) DeadMethod(ctx context.Context) error { return nil }
func (h *helper) LiveCalled(ctx context.Context) error { return nil }

// TestOnly is referenced only from a test file: with the default (test files not loaded) it is
// reported, which pins the "test-utility needs a registered reason" boundary.
func (h *helper) TestOnly(ctx context.Context) error { return nil }

// resultImpl implements the stdlib interface database/sql.Result. The compile-time assertion is
// a TYPE use, so nothing references these methods; only the interface-membership rule keeps them
// out of the report. This is the case TASK-234 must never regress on.
type resultImpl struct{}

func (resultImpl) LastInsertId() (int64, error) { return 0, nil }
func (resultImpl) RowsAffected() (int64, error) { return 0, nil }

var _ sql.Result = resultImpl{}

// adapterImpl implements a third-party-style interface the same way.
type adapterImpl struct{}

func (adapterImpl) SavePolicy(ctx context.Context) error { return nil }

var _ persist.Adapter = adapterImpl{}

// anonImpl is only reachable through an anonymous interface assertion.
type anonImpl struct{}

func (anonImpl) AnonMethod() {}

var _ = any(anonImpl{}).(interface{ AnonMethod() })

// reflectImpl is only reachable through reflect-by-name.
type reflectImpl struct{}

func (reflectImpl) ReflectMethod() {}

// embedded's method is promoted by wrapper, so it can be part of an exported API.
type embedded struct{}

func (embedded) Promoted() {}

type wrapper struct{ embedded }

// valueMethodImpl has one method used as a method value and one as a method expression.
type valueMethodImpl struct{}

func (v *valueMethodImpl) ViaMethodValue(ctx context.Context) error      { return nil }
func (v *valueMethodImpl) ViaMethodExpression(ctx context.Context) error { return nil }

// sentinel implements the universe error interface: it is reached through the interface, never
// by name. Missing this one is a false positive on every unexported error type.
type sentinel string

func (s sentinel) Error() string { return string(s) }

var ErrSentinel = sentinel("boom")

// speakerImpl is selected through an exported interface from another package.
type speakerImpl struct{}

func (speakerImpl) Speak(ctx context.Context) error { return nil }

type Speaker interface {
	Speak(ctx context.Context) error
}

func NewSpeaker() Speaker { return speakerImpl{} }

// ExportedReceiver's methods are out of scope: another package may call them.
type ExportedReceiver struct{}

func (e *ExportedReceiver) PublicMethod() {}
`,
		"internal/storelike/callers.go": `package storelike

import (
	"context"
	"reflect"
)

// LiveCalled is referenced by a call, ViaMethodValue by a method value, and
// ViaMethodExpression by a method expression.
func Use(h *helper) error { return h.LiveCalled(context.Background()) }

// dynamicName reaches ReflectMethod only through reflect-by-name, which no static analysis can
// resolve: the name is treated as alive and surfaced as a boundary note.
func dynamicName() any { return reflect.ValueOf(reflectImpl{}).MethodByName("ReflectMethod") }

func methodValue(v *valueMethodImpl) func(context.Context) error { return v.ViaMethodValue }

func methodExpression() func(*valueMethodImpl, context.Context) error {
	return (*valueMethodImpl).ViaMethodExpression
}
`,
		"internal/app/app.go": `package app

import (
	"context"

	"example.com/fake/internal/storelike"
)

// Speak is called through the interface, which is what keeps the concrete implementation alive.
func Use(s storelike.Speaker) error { return s.Speak(context.Background()) }
`,
		"internal/storelike/store_test.go": `package storelike

import (
	"context"
	"testing"
)

func TestHelpers(t *testing.T) {
	// Only a test calls TestOnly; the default analysis must still report it.
	_ = (&helper{}).TestOnly(context.Background())
}
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

func deadKeys(analysis *Analysis) map[string]bool {
	dead := map[string]bool{}
	for _, method := range analysis.Dead {
		dead[method.Key()] = true
	}
	return dead
}

// The positive signal, and every shape that must not produce one.
func TestAnalyzeSeparatesDeadFromReachableShapes(t *testing.T) {
	analysis, err := Analyze(writeModule(t), false)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	dead := deadKeys(analysis)

	for _, alive := range []string{
		// Referenced in shipping code: a call, a method value, a method expression.
		storelikePackage + ":helper.LiveCalled",
		storelikePackage + ":valueMethodImpl.ViaMethodValue",
		storelikePackage + ":valueMethodImpl.ViaMethodExpression",
		// Interface members: stdlib, third-party-style, local-interface selection, anonymous
		// interface assertion, and the universe error interface.
		storelikePackage + ":resultImpl.LastInsertId",
		storelikePackage + ":resultImpl.RowsAffected",
		storelikePackage + ":adapterImpl.SavePolicy",
		storelikePackage + ":speakerImpl.Speak",
		storelikePackage + ":anonImpl.AnonMethod",
		storelikePackage + ":sentinel.Error",
		// Promotion through embedding.
		storelikePackage + ":embedded.Promoted",
		// Dynamic name-based dispatch: treated as alive, and reported as a boundary note.
		storelikePackage + ":reflectImpl.ReflectMethod",
	} {
		if dead[alive] {
			t.Errorf("%s is reachable and must not be reported", alive)
		}
	}

	for _, wantDead := range []string{
		// Nothing references it and no interface claims it.
		storelikePackage + ":helper.DeadMethod",
		// Referenced only by a test file, which the default analysis does not load.
		storelikePackage + ":helper.TestOnly",
	} {
		if !dead[wantDead] {
			t.Errorf("%s is unreachable and must be reported", wantDead)
		}
	}

	// An exported receiver type is out of scope on purpose, so its method is not even a
	// candidate.
	if dead[storelikePackage+":ExportedReceiver.PublicMethod"] {
		t.Errorf("methods on exported types are out of scope")
	}
	if len(analysis.Dynamic) == 0 || analysis.Dynamic[0] != "ReflectMethod" {
		t.Errorf("the reflect boundary must be surfaced, got %#v", analysis.Dynamic)
	}
}

// TestInterfaceCheckIsLoadBearing is the reproducible mutation: with the interface-membership
// rule disabled, the stdlib and third-party shapes are reported as dead. That is exactly the
// false positive the rule exists to prevent, so the check cannot be dropped silently.
func TestInterfaceCheckIsLoadBearing(t *testing.T) {
	analysis, err := analyze(analyzeOptions{root: writeModule(t), interfaceCheck: false})
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	dead := deadKeys(analysis)
	for _, mustBeReportedWithoutTheRule := range []string{
		storelikePackage + ":resultImpl.LastInsertId",
		storelikePackage + ":resultImpl.RowsAffected",
		storelikePackage + ":adapterImpl.SavePolicy",
		storelikePackage + ":sentinel.Error",
	} {
		if !dead[mustBeReportedWithoutTheRule] {
			t.Errorf("without the interface check, %s must be a false positive; the check is not load-bearing", mustBeReportedWithoutTheRule)
		}
	}
}

// TestIncludeTestsCountsTestReferences pins the other side of the boundary: with -include-tests
// the test-only method is no longer reported.
func TestIncludeTestsCountsTestReferences(t *testing.T) {
	root := writeModule(t)
	analysis, err := Analyze(root, true)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if deadKeys(analysis)[storelikePackage+":helper.TestOnly"] {
		t.Fatalf("with test files loaded, a test reference keeps the method alive")
	}
}

// Registration is what makes the gate pass, and the entry count is asserted (AC-234-03).
func TestRunRegistersUnreachableMethod(t *testing.T) {
	root := writeModule(t)
	registry := filepath.Join(root, "deadmethods.exceptions.yaml")
	content := "exceptions:\n" +
		"  - package: " + storelikePackage + "\n    receiver: helper\n    method: DeadMethod\n    reason: \"[test-utility] seeded by fixtures\"\n    review_by: \"2026-12-31\"\n" +
		"  - package: " + storelikePackage + "\n    receiver: helper\n    method: TestOnly\n    reason: \"[test-utility] reached from tests only\"\n    review_by: \"2026-12-31\"\n"
	if err := os.WriteFile(registry, []byte(content), 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}

	report, err := Run(Options{Root: root, ExceptionsFile: registry, Now: fixedNow})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("registered methods must pass, got %#v", report.Findings)
	}
	if report.Exceptions != 2 {
		t.Fatalf("want 2 registered exceptions, got %d", report.Exceptions)
	}
	if len(report.Dead) != 2 {
		t.Fatalf("want the 2 unreferenced methods, got %#v", report.Dead)
	}
	for _, method := range report.Dead {
		if report.ReviewDates[method.Key()] != "2026-12-31" {
			t.Errorf("review date missing for %s: %#v", method.Key(), report.ReviewDates)
		}
	}
}
