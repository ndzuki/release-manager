// Package storesurface gates the store interface surface: every method declared by an
// interface in internal/store must be called through that interface in shipping code, unless
// it is registered in storesurface.exceptions.yaml with a reason and a future review date.
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
	called := map[int]bool{}
	for _, pkg := range pkgs {
		markCalls(pkg, targets, byName, called)
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
//   - a concrete receiver that IMPLEMENTS the interface calls the same name with an identical
//     signature. The store package calls its own concrete types inside units of work, and a
//     method those call is not dead just because no caller goes through the interface.
//
// Both shapes come from go/types, so comments and string literals cannot keep a method alive,
// and a same-named method on an unrelated type cannot either.
func markCalls(pkg *packages.Package, targets []target, byName map[string][]int, called map[int]bool) {
	if pkg == nil || pkg.TypesInfo == nil {
		return
	}
	for _, file := range pkg.Syntax {
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			markSelection(pkg.TypesInfo, selector, targets, byName, called)
			markUse(pkg.TypesInfo, selector, targets, byName, called)
			return true
		})
	}
}

// markSelection covers method calls and method values: either the receiver IS the interface,
// or its type implements the interface and the name and signature match (the store package
// calls its own concrete types inside units of work).
func markSelection(
	info *types.Info, selector *ast.SelectorExpr,
	targets []target, byName map[string][]int, called map[int]bool,
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
			selection.Recv() != nil && types.Implements(selection.Recv(), targets[index].iface):
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
