// Package storesurface gates the store interface surface: every method declared by an
// interface in internal/store must have an attributable call site in shipping code, unless it
// is registered in storesurface.exceptions.yaml with a reason and a future review date.
//
// A call site is attributable in two shapes: the method is selected through its interface (a
// method value or call, including interface method expressions such as store.BundleStore.Get),
// or a concrete type EXPLICITLY BOUND to that interface (see collectBindings) calls the same
// name with an identical signature. The binding requirement is what keeps two interfaces with
// the same method set from masking each other -- a structural types.Implements check would let
// a call on the shared implementer keep a dead method of the other interface alive, which is a
// counter-example review found and the fixture now pins.
//
// Known limits, all of which report a method that is in fact called (the safe direction for a
// gate that hunts dead surface) and are listed here so nobody mistakes them for coverage:
// a binding written inside a function body (including init), a binding inherited through an
// embedded interface, a conversion to the interface, a struct-literal field, a multi-value
// return, a package-level func literal, assignments and named-result writes, and concrete
// method expressions.
//
// One more, found in review and worth naming because Go recommends the pattern: a call through a
// CONSUMER-LOCAL NARROW INTERFACE. `internal/audit/archiver.go` declares its own
// `archiveEventStore{ ListOlderThan; DeleteByIDs }` and calls those methods, but the selected
// object belongs to that local interface, not to store.AuditEventStore, so the identity match
// misses and the methods land in the report (they are registered as `[local-narrow-iface]`).
// Do not fix this by matching names or signatures: that is exactly the same-name masking the
// binding rule exists to prevent (see the counter-example above).
//
// A method caught by one of these lands in the report, where an operator registers it or extends
// the collector.
//
// Why this gate exists: TASK-221's audit found methods with no call sites at all, and a later
// fix orphaned another. A method nobody calls is either dead weight or a missing wire-up, and
// neither shows up in a green test suite -- TASK-222's withdrawn claim ("nothing creates
// convergence tasks") happened because the writer was reached through a unit-of-work helper
// instead of the interface method, and TASK-218 happened because the reader's contract had no
// writer at all.
//
// The analysis is type-based on purpose. A text search for ".Method(" cannot answer the
// question: it counts comments and string literals, it cannot tell two interfaces' identically
// named methods apart, and it cannot see whether a call goes through the interface or through
// a concrete type. This gate loads the packages with go/packages and matches method calls
// against the actual *types.Func objects the interfaces declare, so a same-named method on
// another interface cannot mask a dead one.
package storesurface

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"
	"time"

	"golang.org/x/tools/go/packages"
	"gopkg.in/yaml.v3"
)

// Method identifies one declared interface method.
type Method struct {
	Interface string `yaml:"interface"`
	Name      string `yaml:"name"`
}

// Exception is one registry entry: a dead method the project knowingly keeps for now.
type Exception struct {
	Interface string `yaml:"interface"`
	Method    string `yaml:"method"`
	Reason    string `yaml:"reason"`
	ReviewBy  string `yaml:"review_by"`
}

// Analysis is what the loader found.
type Analysis struct {
	Interfaces int
	Declared   int
	Dead       []Method
}

// Options configures Run.
type Options struct {
	Root           string
	StorePackage   string
	ExceptionsFile string
	Now            time.Time
}

// Report is the outcome of one run.
type Report struct {
	Interfaces  int
	Declared    int
	Dead        []Method
	Exceptions  int
	Findings    []string
	ReviewDates map[string]string
}

// Run loads the packages under Root, finds the store interfaces' methods and their call
// sites, and validates the registry against them. Findings make the gate fail.
func Run(opts Options) (*Report, error) {
	opts = withDefaults(opts)

	analysis, err := Analyze(opts.Root, opts.StorePackage)
	if err != nil {
		return nil, err
	}
	exceptions, err := loadExceptions(opts.ExceptionsFile)
	if err != nil {
		return nil, err
	}

	report := &Report{
		Interfaces:  analysis.Interfaces,
		Declared:    analysis.Declared,
		Dead:        analysis.Dead,
		Exceptions:  len(exceptions),
		ReviewDates: map[string]string{},
	}
	report.Findings = append(report.Findings, Validate(analysis.Dead, exceptions, opts.Now, report.ReviewDates)...)
	sort.Strings(report.Findings)
	return report, nil
}

func withDefaults(opts Options) Options {
	if opts.Root == "" {
		opts.Root = "."
	}
	if opts.StorePackage == "" {
		opts.StorePackage = "github.com/ndzuki/release-manager/internal/store"
	}
	if opts.ExceptionsFile == "" {
		opts.ExceptionsFile = "storesurface.exceptions.yaml"
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	return opts
}

// Analyze loads every shipping package in the module and reports which interface methods the
// store package declares and which of them nobody calls. Test files are not loaded, so a call
// that only exists in a test does not keep a method alive.
func Analyze(root, storePackage string) (*Analysis, error) {
	cfg := &packages.Config{
		Dir: root,
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo |
			packages.NeedSyntax | packages.NeedImports | packages.NeedDeps,
	}
	// The whole module: a caller can live in cmd/, test/ or an internal package, and loading
	// everything keeps the gate honest when the layout changes.
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, fmt.Errorf("load packages: %w", err)
	}
	if failed := loadFailures(pkgs); len(failed) > 0 {
		return nil, fmt.Errorf("packages failed to load: %s", strings.Join(failed, "; "))
	}

	storePkg := findPackage(pkgs, storePackage)
	if storePkg == nil || storePkg.Types == nil {
		return nil, fmt.Errorf("store package %s not found under %s", storePackage, root)
	}
	targets, interfaceCount := interfaceMethods(storePkg.Types)
	if len(targets) == 0 {
		return nil, fmt.Errorf("no interface methods found in %s", storePackage)
	}

	byName := map[string][]int{}
	for index, t := range targets {
		byName[t.method.Name] = append(byName[t.method.Name], index)
	}
	bound := collectBindings(pkgs)
	called := map[int]bool{}
	for _, pkg := range pkgs {
		markCalls(pkg, targets, byName, bound, called)
	}

	var dead []Method
	for index, t := range targets {
		if !called[index] {
			dead = append(dead, t.method)
		}
	}
	sort.Slice(dead, func(i, j int) bool {
		if dead[i].Interface != dead[j].Interface {
			return dead[i].Interface < dead[j].Interface
		}
		return dead[i].Name < dead[j].Name
	})
	return &Analysis{Interfaces: interfaceCount, Declared: len(targets), Dead: dead}, nil
}

// target is one declared interface method plus the interface it belongs to.
type target struct {
	method Method
	fn     *types.Func
	iface  *types.Interface
	sig    *types.Signature
}

// interfaceMethods collects every explicitly declared method of every interface in the
// package. Embedded methods belong to the interface that declares them, which is what makes a
// same-named method on an unrelated interface harmless.
func interfaceMethods(pkg *types.Package) (targets []target, interfaceCount int) {
	for _, name := range pkg.Scope().Names() {
		typeName, ok := pkg.Scope().Lookup(name).(*types.TypeName)
		if !ok {
			continue
		}
		// An alias of an interface is not a second interface: counting it would inflate the
		// reported surface and duplicate its methods (review found this with a synthetic alias).
		if typeName.IsAlias() {
			continue
		}
		iface, ok := typeName.Type().Underlying().(*types.Interface)
		if !ok {
			continue
		}
		interfaceCount++
		complete := iface.Complete()
		for i := 0; i < complete.NumExplicitMethods(); i++ {
			method := complete.ExplicitMethod(i)
			signature, isSignature := method.Type().(*types.Signature)
			if !isSignature {
				continue
			}
			targets = append(targets, target{
				method: Method{Interface: typeName.Name(), Name: method.Name()},
				fn:     method,
				iface:  complete,
				sig:    signature,
			})
		}
	}
	return targets, interfaceCount
}

// markCalls records every place an interface method is exercised. Two shapes count:
//
//   - the method is selected on the interface itself (any caller doing s.store.Bundles().Get),
//     and method expressions such as store.BundleStore.Get; and
//   - a concrete receiver BOUND to the interface (see collectBindings) calls the same name with
//     an identical signature. The store package calls its own concrete types inside units of
//     work, and a method those call is not dead just because no caller goes through the
//     interface; a type that merely happens to implement the interface does NOT count.
//
// Both shapes come from go/types, so comments and string literals cannot keep a method alive,
// and a same-named method on an unrelated type cannot either.
func markCalls(pkg *packages.Package, targets []target, byName map[string][]int, bound bindings, called map[int]bool) {
	if pkg == nil || pkg.TypesInfo == nil || isVendored(pkg.PkgPath) {
		return
	}
	for _, file := range pkg.Syntax {
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			markSelection(pkg.TypesInfo, selector, targets, byName, bound, called)
			markUse(pkg.TypesInfo, selector, targets, byName, called)
			return true
		})
	}
}

// isVendored keeps third-party Go that happens to live in the tree (a checked-in
// web/node_modules package) out of the analysis, so the local and CI package sets agree.
func isVendored(path string) bool {
	return strings.Contains(path, "/node_modules/") || strings.Contains(path, "/vendor/")
}

// markSelection covers method calls and method values: either the receiver IS the interface,
// or its type implements the interface and the name and signature match (the store package
// calls its own concrete types inside units of work).
func markSelection(
	info *types.Info, selector *ast.SelectorExpr,
	targets []target, byName map[string][]int, bound bindings, called map[int]bool,
) {
	selection, ok := info.Selections[selector]
	if !ok || selection.Kind() != types.MethodVal {
		return
	}
	fn, isFunc := selection.Obj().(*types.Func)
	if !isFunc {
		return
	}
	signature, isSignature := fn.Type().(*types.Signature)
	for _, index := range byName[fn.Name()] {
		switch {
		case targets[index].fn == fn:
			called[index] = true
		// types.Identical, not string equality: the concrete method lives in another package,
		// so the two signatures render with different qualifiers even when they are the same.
		case isSignature && targets[index].sig != nil && types.Identical(signature, targets[index].sig) &&
			bound.covers(targets[index].iface, selection.Recv()):
			called[index] = true
		}
	}
}

// markUse covers method expressions such as store.BundleStore.Get, where the selected object
// is the interface method itself.
func markUse(info *types.Info, selector *ast.SelectorExpr, targets []target, byName map[string][]int, called map[int]bool) {
	fn, ok := info.Uses[selector.Sel].(*types.Func)
	if !ok {
		return
	}
	for _, index := range byName[fn.Name()] {
		if targets[index].fn == fn {
			called[index] = true
		}
	}
}

// bindings records the concrete types that shipping code explicitly binds to an interface.
// collectBindings fills it from exactly two forms -- a function whose declared result is the
// interface and whose body returns a concrete named type, and a file-level
// `var x Iface = <concrete>` -- because broader collection was tried and falsified: catching
// assignments, named-result writes and conversions produced both false-alive and false-dead
// results, and walking into nested function literals fabricated bindings outright.
//
// Structural types.Implements is not enough on its own. Two interfaces with the same method set
// are implemented by the same type, so a call on that type would keep a dead method of the
// OTHER interface alive. That counter-example was found in review; requiring an explicit
// binding removes it, because a type only counts for an interface some code actually binds it
// to.
type bindings map[*types.Interface]map[string]bool

func (b bindings) add(iface *types.Interface, concrete types.Type) {
	if iface == nil || concrete == nil || isInterfaceType(concrete) {
		return
	}
	key := concreteKey(concrete)
	if key == "" {
		return
	}
	if b[iface] == nil {
		b[iface] = map[string]bool{}
	}
	b[iface][key] = true
}

// covers reports whether the receiver is a concrete type explicitly bound to the interface.
func (b bindings) covers(iface *types.Interface, recv types.Type) bool {
	if iface == nil || recv == nil {
		return false
	}
	return b[iface][concreteKey(recv)]
}

// concreteKey names a concrete type unambiguously: aliases are resolved first (an alias used
// silently to be dropped before, so a binding through one counted for nothing), and the key is
// the fully qualified type string, which keeps instantiated generics apart.
func concreteKey(t types.Type) string {
	t = types.Unalias(t)
	if pointer, ok := t.(*types.Pointer); ok {
		t = types.Unalias(pointer.Elem())
	}
	if _, ok := t.(*types.Named); !ok {
		return ""
	}
	return types.TypeString(t, func(pkg *types.Package) string { return pkg.Path() })
}

func isInterfaceType(t types.Type) bool {
	if pointer, ok := t.(*types.Pointer); ok {
		t = pointer.Elem()
	}
	_, ok := t.Underlying().(*types.Interface)
	return ok
}

// collectBindings walks every shipping package and records the interface bindings shipping
// code states explicitly. Two forms are collected, and only these:
//
//   - a function whose declared result is the interface and whose body returns a concrete named
//     type (the store's accessors: `func (s *Store) Bundles() store.BundleStore { return s.bundles }`); and
//   - a file-level `var x Iface = <concrete>`, which also covers compile-time assertions such
//     as `var _ Iface = (*T)(nil)`.
//
// The collector is deliberately narrow. Review found that a broader walk fabricated bindings --
// attributing a nested closure's `return` to the enclosing function's result list made a
// genuinely dead method look alive -- and that trying to catch assignments, named-result writes
// and conversions produced both false alive and false dead results. The failure mode of the
// narrow rule is visible, not silent: a binding written some other way leaves the method in the
// report, where an operator registers it (or extends this function) instead of the gate quietly
// accepting a dead method.
func collectBindings(pkgs []*packages.Package) bindings {
	bound := bindings{}
	for _, pkg := range pkgs {
		if pkg == nil || pkg.TypesInfo == nil || isVendored(pkg.PkgPath) {
			continue
		}
		for _, file := range pkg.Syntax {
			collectFileLevelBindings(pkg.TypesInfo, file, bound)
			for _, decl := range file.Decls {
				funcDecl, ok := decl.(*ast.FuncDecl)
				if !ok || funcDecl.Body == nil {
					continue
				}
				bindFunctionReturns(pkg.TypesInfo, funcDecl.Type.Results, funcDecl.Body, bound)
			}
		}
	}
	return bound
}

// collectFileLevelBindings handles `var x Iface = <concrete>` at file scope, including
// compile-time assertions such as `var _ Iface = (*T)(nil)`.
func collectFileLevelBindings(info *types.Info, file *ast.File, bound bindings) {
	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.VAR {
			continue
		}
		for _, spec := range genDecl.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			bindValueSpec(info, valueSpec, bound)
		}
	}
}

// bindFunctionReturns records what one function returns as an interface. Nested function
// literals are skipped: their `return`s belong to their own signature, and attributing them to
// the enclosing function produced bindings that no code ever made (found in review).
func bindFunctionReturns(info *types.Info, results *ast.FieldList, body *ast.BlockStmt, bound bindings) {
	if results == nil {
		return
	}
	ast.Inspect(body, func(node ast.Node) bool {
		if _, isLiteral := node.(*ast.FuncLit); isLiteral {
			return false
		}
		stmt, ok := node.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		for position, result := range stmt.Results {
			expected := resultTypeAt(info, results, position)
			iface := interfaceOf(expected)
			if iface == nil {
				continue
			}
			bound.add(iface, staticType(info, result))
		}
		return true
	})
}

// resultTypeAt resolves the declared result type at position i. Named results make the
// positions explicit; otherwise the statement order matches. It is called with the result list
// of the function whose body is being walked, never an enclosing one.
func resultTypeAt(info *types.Info, results *ast.FieldList, position int) types.Type {
	if results == nil || len(results.List) == 0 {
		return nil
	}
	fields := 0
	for _, field := range results.List {
		count := len(field.Names)
		if count == 0 {
			count = 1
		}
		if position >= fields && position < fields+count {
			return info.TypeOf(field.Type)
		}
		fields += count
	}
	return nil
}

func bindValueSpec(info *types.Info, spec *ast.ValueSpec, bound bindings) {
	if spec.Type == nil {
		return
	}
	iface := interfaceOf(info.TypeOf(spec.Type))
	if iface == nil {
		return
	}
	for _, value := range spec.Values {
		bound.add(iface, staticType(info, value))
	}
}

func interfaceOf(t types.Type) *types.Interface {
	if t == nil {
		return nil
	}
	iface, ok := t.Underlying().(*types.Interface)
	if !ok {
		return nil
	}
	return iface.Complete()
}

func staticType(info *types.Info, expr ast.Expr) types.Type {
	if tv, ok := info.Types[expr]; ok {
		return tv.Type
	}
	return nil
}

func findPackage(pkgs []*packages.Package, path string) *packages.Package {
	for _, pkg := range pkgs {
		if pkg.PkgPath == path {
			return pkg
		}
		if dep, ok := pkg.Imports[path]; ok && dep.Types != nil {
			return dep
		}
	}
	return nil
}

func loadFailures(pkgs []*packages.Package) []string {
	var failures []string
	packages.Visit(pkgs, nil, func(pkg *packages.Package) {
		if isVendored(pkg.PkgPath) {
			return
		}
		for _, err := range pkg.Errors {
			failures = append(failures, fmt.Sprintf("%s: %s", pkg.PkgPath, err.Msg))
		}
	})
	return failures
}

// Validate checks every registry entry on its own terms and records the review date of the
// entries that describe a real, still-dead method. It is pure so the rules can be tested
// without loading packages.
func Validate(dead []Method, exceptions []Exception, now time.Time, reviewDates map[string]string) []string {
	deadSet := map[string]bool{}
	for _, m := range dead {
		deadSet[m.Interface+"."+m.Name] = true
	}
	registered := map[string]bool{}
	var findings []string
	for _, e := range exceptions {
		key := e.Interface + "." + e.Method
		switch {
		case e.Method == "" || e.Interface == "":
			findings = append(findings, "exception entry with an empty interface or method")
		case registered[key]:
			findings = append(findings, fmt.Sprintf("duplicate exception entry for %s", key))
		default:
			registered[key] = true
			findings = append(findings, validateException(e, key, deadSet, now, reviewDates)...)
		}
	}
	for _, m := range dead {
		if !registered[m.Interface+"."+m.Name] {
			findings = append(findings, fmt.Sprintf("%s.%s: no call site through the interface and no exception entry", m.Interface, m.Name))
		}
	}
	return findings
}

func validateException(e Exception, key string, deadSet map[string]bool, now time.Time, reviewDates map[string]string) []string {
	var findings []string
	if strings.TrimSpace(e.Reason) == "" {
		findings = append(findings, fmt.Sprintf("%s: exception needs a reason", key))
	}
	if !deadSet[key] {
		return append(findings, fmt.Sprintf("%s: exception is stale -- the method is called through the interface or gone, delete the entry", key))
	}
	if e.ReviewBy == "" {
		return append(findings, fmt.Sprintf("%s: exception needs review_by", key))
	}
	reviewBy, parseErr := time.Parse("2006-01-02", e.ReviewBy)
	if parseErr != nil {
		return append(findings, fmt.Sprintf("%s: review_by %q is not a YYYY-MM-DD date", key, e.ReviewBy))
	}
	if reviewDates != nil {
		reviewDates[key] = e.ReviewBy
	}
	if reviewBy.Before(now) {
		findings = append(findings, fmt.Sprintf("%s: review_by %s has passed -- delete the method or re-justify the entry", key, e.ReviewBy))
	}
	return findings
}

func loadExceptions(path string) ([]Exception, error) {
	content, err := readFile(path)
	if err != nil {
		return nil, err
	}
	var file struct {
		Exceptions []Exception `yaml:"exceptions"`
	}
	if err := yaml.Unmarshal(content, &file); err != nil {
		return nil, fmt.Errorf("parse exceptions: %w", err)
	}
	return file.Exceptions, nil
}
