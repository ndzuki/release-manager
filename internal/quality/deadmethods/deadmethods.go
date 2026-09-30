// Package deadmethods gates one class of dead code that neither of the project's other two Go
// gates can see: an EXPORTED method declared on an UNEXPORTED type.
//
// Why it is invisible today:
//
//   - check-store-surface (internal/quality/storesurface) only tracks the methods declared by
//     interfaces in internal/store;
//   - golangci-lint's `unused` is enabled (see .golangci.yml) but deliberately does not report an
//     exported method on an unexported type, because the type may implement an interface the
//     linter cannot see. Resolving that question is exactly what go/types is for, and this gate
//     does it.
//
// TASK-226's manual audit found seven such methods across the store engines (five Casbin
// persist.Adapter methods, a client-go RESTClientGetter, a database/sql.Result and one cascading
// private helper); adding them back left both gates green, which is the blind spot this package
// closes.
//
// A candidate is reported only when every question below comes back negative.
//
//  1. TYPE-LEVEL REFERENCE. Does the method's *types.Func object appear in TypesInfo.Uses (or
//     TypesInfo.Selections) anywhere in the module's own shipping code? Calls, method values
//     (x.M) and method expressions (T.M) all record that object, so a mention in a comment or a
//     string literal cannot keep a method alive, and a same-named method on another type cannot
//     either.
//
//  2. INTERFACE MEMBERSHIP. Does any interface reachable from the loaded package graph -- the
//     standard library and third-party modules included -- declare a method with this name and
//     an identical signature that the receiver type implements? This is the gate's core
//     false-positive defense and it is load-bearing: every candidate this repository produced
//     was an implementation of Casbin's persist.Adapter (five methods), client-go's
//     RESTClientGetter or database/sql.Result. Membership is decided structurally
//     (types.Implements over every interface in the graph) rather than by name, which is why a
//     same-named method on an unrelated type cannot mask the answer.
//
//  3. PROMOTION AND NAME-BASED DISPATCH. An unexported type embedded in another struct promotes
//     its methods, which can make them part of an exported API; and reflect.Value.MethodByName
//     ("X") cannot be resolved statically. An embedded receiver type, and a method name that
//     appears in a MethodByName string literal, are treated as alive.
//
// Known limits, all in the SAFE direction (they hide a dead method instead of reporting a live
// one) and listed here so nobody mistakes them for coverage:
//
//   - structural interface membership means a type that happens to satisfy an unrelated
//     interface is alive even when no code ever selects the method through it;
//   - consumers outside this module are invisible; an exported method on an unexported type
//     cannot be reached from another module, but a method on an EXPORTED type can, which is why
//     exported receiver types are out of scope on purpose;
//   - a MethodByName literal naming a method of a different type keeps the name alive everywhere;
//   - test files are not loaded by default (Options.IncludeTests), so a method exercised only by
//     tests is reported and has to be registered in deadmethods.exceptions.yaml with a reason --
//     the same "test-utility" disposition check-store-surface already uses.
//
// What the gate cannot decide lands in the report as an explicit entry, where an operator either
// deletes the method, wires it up, or registers it with a reason and a review date. Silence is
// never used to cover an unknown.
package deadmethods

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/tools/go/packages"
	"gopkg.in/yaml.v3"
)

// Method identifies one candidate: an exported method on an unexported receiver type.
type Method struct {
	Package  string `yaml:"package"`
	Receiver string `yaml:"receiver"`
	Name     string `yaml:"name"`
}

// Key is the stable identity used by the registry and by error messages.
func (m Method) Key() string {
	return m.Package + ":" + m.Receiver + "." + m.Name
}

// Exception is one registry entry: an unreferenced method the project knowingly keeps for now.
type Exception struct {
	Package  string `yaml:"package"`
	Receiver string `yaml:"receiver"`
	Method   string `yaml:"method"`
	Reason   string `yaml:"reason"`
	ReviewBy string `yaml:"review_by"`
}

// key mirrors Method.Key for the entry side.
func (e Exception) key() string {
	return e.Package + ":" + e.Receiver + "." + e.Method
}

// Analysis is what the loader found.
type Analysis struct {
	Packages   int
	Candidates int
	Dead       []Method
	// Dynamic records method names that appeared in a reflect MethodByName string literal and
	// were therefore treated as alive. It is reported so the boundary is visible.
	Dynamic []string
}

// Options configures Run.
type Options struct {
	Root           string
	ExceptionsFile string
	// IncludeTests loads test files too. When false (the default) a call that exists only in a
	// test does not keep a method alive, mirroring check-store-surface.
	IncludeTests bool
	Now          time.Time
}

// Report is the outcome of one run.
type Report struct {
	Packages    int
	Candidates  int
	Dead        []Method
	Dynamic     []string
	Exceptions  int
	Findings    []string
	ReviewDates map[string]string
}

// Run loads the module under Root, finds exported methods on unexported types that nothing
// references and no interface claims, and validates the registry against them. Findings make the
// gate fail.
func Run(opts Options) (*Report, error) {
	opts = withDefaults(opts)

	analysis, err := Analyze(opts.Root, opts.IncludeTests)
	if err != nil {
		return nil, err
	}
	exceptions, err := loadExceptions(opts.ExceptionsFile)
	if err != nil {
		return nil, err
	}

	report := &Report{
		Packages:    analysis.Packages,
		Candidates:  analysis.Candidates,
		Dead:        analysis.Dead,
		Dynamic:     analysis.Dynamic,
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
	if opts.ExceptionsFile == "" {
		opts.ExceptionsFile = "deadmethods.exceptions.yaml"
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	return opts
}

// Analyze loads the module under root and reports the exported methods of unexported receiver
// types that have no reference in shipping code and are not an interface member.
func Analyze(root string, includeTests bool) (*Analysis, error) {
	return analyze(analyzeOptions{root: root, includeTests: includeTests, interfaceCheck: true})
}

// analyzeOptions carries the knobs the tests need. interfaceCheck exists so the load-bearing
// test can prove what the interface-membership rule contributes: with it disabled, an
// implementation of a stdlib interface is reported (false positive), which is the mutation the
// gate's core defense must survive.
type analyzeOptions struct {
	root           string
	includeTests   bool
	interfaceCheck bool
}

func analyze(opts analyzeOptions) (*Analysis, error) {
	cfg := &packages.Config{
		Dir:  opts.root,
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedSyntax | packages.NeedImports | packages.NeedDeps,
		// Test variants are loaded only when asked for: a call that exists only in a test must
		// not keep a method alive, exactly like check-store-surface.
		Tests: opts.includeTests,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, fmt.Errorf("load packages: %w", err)
	}
	if failed := loadFailures(pkgs); len(failed) > 0 {
		return nil, fmt.Errorf("packages failed to load: %s", strings.Join(failed, "; "))
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("no packages found under %s", opts.root)
	}

	interfaces := collectInterfaces(pkgs)
	used := collectUsedMethods(pkgs)
	embedded := collectEmbeddedTypes(pkgs)
	dynamic := collectDynamicNames(pkgs)

	analysis := &Analysis{Packages: len(pkgs)}
	checks := aliveChecks{
		used:           used,
		embedded:       embedded,
		dynamic:        dynamic,
		interfaces:     interfaces,
		interfaceCheck: opts.interfaceCheck,
	}
	// Test variants repeat the package under a different ID, so a method could be enumerated
	// twice when IncludeTests is set. The key is the report identity; count it once.
	seen := map[string]bool{}
	for _, pkg := range pkgs {
		collectCandidates(pkg, checks, seen, analysis)
	}
	sort.Slice(analysis.Dead, func(i, j int) bool { return analysis.Dead[i].Key() < analysis.Dead[j].Key() })
	for name := range dynamic {
		analysis.Dynamic = append(analysis.Dynamic, name)
	}
	sort.Strings(analysis.Dynamic)
	return analysis, nil
}

// aliveChecks answers the three questions a candidate has to fail to be reported: no reference,
// no interface claims it, and it is neither promoted nor named for dynamic dispatch.
type aliveChecks struct {
	used           map[string]bool
	embedded       map[string]bool
	dynamic        map[string]bool
	interfaces     []*types.Interface
	interfaceCheck bool
}

// alive reports whether any shape keeps the candidate reachable. Every branch is a safe-direction
// rule: it can only hide a dead method from the report, never invent one.
func (c aliveChecks) alive(candidate candidate) bool {
	switch {
	case c.used[candidate.method.Key()]:
		return true
	case c.embedded[candidate.receiverKey]:
		return true
	case c.dynamic[candidate.method.Name]:
		return true
	case c.interfaceCheck && isInterfaceMember(candidate.recv, candidate.fn, c.interfaces):
		return true
	}
	return false
}

// collectCandidates adds one package's candidates to the analysis, counting each method once and
// recording the unreachable ones.
func collectCandidates(pkg *packages.Package, checks aliveChecks, seen map[string]bool, analysis *Analysis) {
	if pkg == nil || pkg.Types == nil || pkg.TypesInfo == nil || isVendored(pkg.PkgPath) {
		return
	}
	for _, candidate := range candidatesOf(pkg) {
		if seen[candidate.method.Key()] {
			continue
		}
		seen[candidate.method.Key()] = true
		analysis.Candidates++
		if checks.alive(candidate) {
			continue
		}
		analysis.Dead = append(analysis.Dead, candidate.method)
	}
}

// candidate is one exported method declared on an unexported receiver type.
type candidate struct {
	method      Method
	fn          *types.Func
	recv        types.Type
	receiverKey string
}

// candidatesOf enumerates the methods this package declares on its own unexported named types.
// Only explicitly declared methods count (Named.NumMethods): promoted methods belong to the type
// they were declared on, which is the type that gets reported.
func candidatesOf(pkg *packages.Package) []candidate {
	var found []candidate
	for _, name := range pkg.Types.Scope().Names() {
		typeName, ok := pkg.Types.Scope().Lookup(name).(*types.TypeName)
		if !ok || typeName.IsAlias() {
			continue
		}
		named, ok := typeName.Type().(*types.Named)
		if !ok {
			continue
		}
		// An exported receiver type is out of scope: other packages may call its methods, and
		// this gate only sees this module.
		if ast.IsExported(named.Obj().Name()) {
			continue
		}
		for i := 0; i < named.NumMethods(); i++ {
			fn := named.Method(i)
			if !ast.IsExported(fn.Name()) {
				continue
			}
			signature, isSignature := fn.Type().(*types.Signature)
			if !isSignature || signature.Recv() == nil {
				continue
			}
			found = append(found, candidate{
				method: Method{Package: pkg.PkgPath, Receiver: named.Obj().Name(), Name: fn.Name()},
				fn:     fn,
				recv:   signature.Recv().Type(),
				// The receiver type as declared: *T for pointer receivers, T otherwise. Embedding
				// records the element type, so both forms resolve to the same key.
				receiverKey: typeKey(deref(signature.Recv().Type())),
			})
		}
	}
	return found
}

// collectInterfaces gathers every interface reachable from the loaded graph: the universe scope
// (whose `error` is what keeps an unexported sentinel type's Error method alive -- it is called
// through the interface, never by name), named interfaces from every package's scope (the
// standard library and third-party modules included, which is what makes the adapter/getter/
// sql.Result implementations survive) plus anonymous interfaces written in this module's source
// (a `.(interface{ M() })` assertion is a real interface too).
func collectInterfaces(pkgs []*packages.Package) []*types.Interface {
	seen := map[string]bool{}
	var interfaces []*types.Interface
	add := func(iface *types.Interface) {
		if iface == nil {
			return
		}
		complete := iface.Complete()
		if complete.NumMethods() == 0 {
			return
		}
		key := types.TypeString(complete, func(p *types.Package) string { return p.Path() })
		if seen[key] {
			return
		}
		seen[key] = true
		interfaces = append(interfaces, complete)
	}
	addScopeInterfaces(types.Universe, add)
	packages.Visit(pkgs, nil, func(pkg *packages.Package) {
		if pkg == nil || pkg.Types == nil || isVendored(pkg.PkgPath) {
			return
		}
		addScopeInterfaces(pkg.Types.Scope(), add)
		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(node ast.Node) bool {
				interfaceType, ok := node.(*ast.InterfaceType)
				if !ok {
					return true
				}
				if info := pkg.TypesInfo; info != nil {
					if iface, ok := info.TypeOf(interfaceType).Underlying().(*types.Interface); ok {
						add(iface)
					}
				}
				return true
			})
		}
	})
	return interfaces
}

// addScopeInterfaces feeds every named interface of a scope to add. It is called for the
// universe scope and for each package scope in the graph.
func addScopeInterfaces(scope *types.Scope, add func(*types.Interface)) {
	for _, name := range scope.Names() {
		typeName, ok := scope.Lookup(name).(*types.TypeName)
		if !ok {
			continue
		}
		if iface, ok := typeName.Type().Underlying().(*types.Interface); ok {
			add(iface)
		}
	}
}

// memberInterface reports whether iface declares a method with fn's name and signature.
func memberInterface(iface *types.Interface, fn *types.Func, stripped *types.Signature) bool {
	for i := 0; i < iface.NumMethods(); i++ {
		method := iface.Method(i)
		if method.Name() != fn.Name() {
			continue
		}
		signature, ok := method.Type().(*types.Signature)
		if !ok {
			continue
		}
		if types.Identical(signature, stripped) {
			return true
		}
	}
	return false
}

// isInterfaceMember reports whether any interface in the graph declares this method and the
// receiver type implements that interface. Structural membership (types.Implements) is used on
// purpose: the candidate's type is exactly what a caller would select the method through.
func isInterfaceMember(recv types.Type, fn *types.Func, interfaces []*types.Interface) bool {
	signature, ok := fn.Type().(*types.Signature)
	if !ok {
		return false
	}
	// Interface methods carry no receiver; comparing the raw signatures never matches. Strip it.
	stripped := types.NewSignatureType(nil, nil, nil, signature.Params(), signature.Results(), signature.Variadic())
	for _, iface := range interfaces {
		if !memberInterface(iface, fn, stripped) {
			continue
		}
		if types.Implements(recv, iface) {
			return true
		}
	}
	return false
}

// collectUsedMethods records every method object the module's shipping code references in any
// shape: a call, a method value, a method expression, or a selection. The result is keyed by the
// method's stable identity (package path, receiver type, method name) rather than by pointer:
// when test variants are loaded, a reference in a test variant points at a different *types.Func
// object for the same method, and the key is what uniquely names a method in Go anyway.
func collectUsedMethods(pkgs []*packages.Package) map[string]bool {
	used := map[string]bool{}
	for _, pkg := range pkgs {
		if pkg == nil || pkg.TypesInfo == nil || isVendored(pkg.PkgPath) {
			continue
		}
		info := pkg.TypesInfo
		for _, object := range info.Uses {
			if fn, ok := object.(*types.Func); ok && isMethod(fn) {
				used[methodObjectKey(fn)] = true
			}
		}
		for _, selection := range info.Selections {
			if fn, ok := selection.Obj().(*types.Func); ok && isMethod(fn) {
				used[methodObjectKey(fn)] = true
			}
		}
	}
	return used
}

// methodObjectKey names a method the way Method.Key does, without depending on object identity.
func methodObjectKey(fn *types.Func) string {
	signature, ok := fn.Type().(*types.Signature)
	if !ok || signature.Recv() == nil {
		return ""
	}
	named, ok := types.Unalias(deref(signature.Recv().Type())).(*types.Named)
	if !ok {
		return ""
	}
	obj := named.Obj()
	path := ""
	if obj.Pkg() != nil {
		path = obj.Pkg().Path()
	}
	return path + ":" + obj.Name() + "." + fn.Name()
}

func isMethod(fn *types.Func) bool {
	signature, ok := fn.Type().(*types.Signature)
	return ok && signature.Recv() != nil
}

// collectEmbeddedTypes records every unexported named type that appears as an embedded struct
// field. Promotion means its methods can be reached through the outer type, which may be
// exported, so such receivers are not reported.
func collectEmbeddedTypes(pkgs []*packages.Package) map[string]bool {
	embedded := map[string]bool{}
	for _, pkg := range pkgs {
		if pkg == nil || pkg.TypesInfo == nil || isVendored(pkg.PkgPath) {
			continue
		}
		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(node ast.Node) bool {
				structType, ok := node.(*ast.StructType)
				if !ok || structType.Fields == nil {
					return true
				}
				for _, field := range structType.Fields.List {
					if len(field.Names) != 0 {
						continue
					}
					key := typeKey(deref(pkg.TypesInfo.TypeOf(field.Type)))
					if key != "" {
						embedded[key] = true
					}
				}
				return true
			})
		}
	}
	return embedded
}

// collectDynamicNames records the names passed to reflect (Value|Type).MethodByName. Name-based
// dispatch cannot be resolved statically, so a name mentioned there is treated as alive and the
// boundary is reported instead of hidden.
func collectDynamicNames(pkgs []*packages.Package) map[string]bool {
	names := map[string]bool{}
	for _, pkg := range pkgs {
		if pkg == nil || isVendored(pkg.PkgPath) {
			continue
		}
		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "MethodByName" || len(call.Args) != 1 {
					return true
				}
				literal, ok := call.Args[0].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					return true
				}
				if name, err := strconv.Unquote(literal.Value); err == nil && name != "" {
					names[name] = true
				}
				return true
			})
		}
	}
	return names
}

// deref strips one pointer layer so pointer and value receivers share a key.
func deref(t types.Type) types.Type {
	if t == nil {
		return nil
	}
	if pointer, ok := t.(*types.Pointer); ok {
		return pointer.Elem()
	}
	return t
}

// typeKey names a named type unambiguously: aliases are resolved first and the key is the
// package path plus the type name, so two packages' `helper` types never collide.
func typeKey(t types.Type) string {
	if t == nil {
		return ""
	}
	t = types.Unalias(t)
	named, ok := t.(*types.Named)
	if !ok {
		return ""
	}
	return named.Obj().Pkg().Path() + "." + named.Obj().Name()
}

// isVendored keeps third-party Go that happens to live in the tree out of the analysis, so the
// local and CI package sets agree.
func isVendored(path string) bool {
	return strings.Contains(path, "/node_modules/") || strings.Contains(path, "/vendor/")
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
// entries that describe a real, still-unreferenced method. It is pure so the rules can be tested
// without loading packages.
func Validate(dead []Method, exceptions []Exception, now time.Time, reviewDates map[string]string) []string {
	deadSet := map[string]bool{}
	for _, method := range dead {
		deadSet[method.Key()] = true
	}
	registered := map[string]bool{}
	var findings []string
	for _, exception := range exceptions {
		key := exception.key()
		switch {
		case exception.Package == "" || exception.Receiver == "" || exception.Method == "":
			findings = append(findings, "exception entry with an empty package, receiver or method")
		case registered[key]:
			findings = append(findings, fmt.Sprintf("duplicate exception entry for %s", key))
		default:
			registered[key] = true
			findings = append(findings, validateException(exception, key, deadSet, now, reviewDates)...)
		}
	}
	for _, method := range dead {
		if !registered[method.Key()] {
			findings = append(findings, fmt.Sprintf("%s: no reference, no interface member and no exception entry", method.Key()))
		}
	}
	return findings
}

func validateException(exception Exception, key string, deadSet map[string]bool, now time.Time, reviewDates map[string]string) []string {
	var findings []string
	if strings.TrimSpace(exception.Reason) == "" {
		findings = append(findings, fmt.Sprintf("%s: exception needs a reason", key))
	}
	if !deadSet[key] {
		return append(findings, fmt.Sprintf("%s: exception is stale -- the method is referenced, an interface member, or gone, delete the entry", key))
	}
	if exception.ReviewBy == "" {
		return append(findings, fmt.Sprintf("%s: exception needs review_by", key))
	}
	reviewBy, parseErr := time.Parse("2006-01-02", exception.ReviewBy)
	if parseErr != nil {
		return append(findings, fmt.Sprintf("%s: review_by %q is not a YYYY-MM-DD date", key, exception.ReviewBy))
	}
	if reviewDates != nil {
		reviewDates[key] = exception.ReviewBy
	}
	if reviewBy.Before(now) {
		findings = append(findings, fmt.Sprintf("%s: review_by %s has passed -- delete the method or re-justify the entry", key, exception.ReviewBy))
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
