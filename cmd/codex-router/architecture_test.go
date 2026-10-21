package main

// Architecture fitness: docs/architecture.md ("Packages") states the dependency
// direction, and Go rejects cycles but not layer inversions, so the rules below
// gate the acyclic shortcuts the compiler accepts. The graph is read with
// `go list -json` from the production Imports, never TestImports, so a test may
// import os, net/http or net/http/httptest without tripping the guard.
//
// Every check lives under TestArchitectureDependencies, so one command covers
// the guard: go test ./cmd/codex-router -run '^TestArchitectureDependencies$'.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const modulePath = "github.com/alexei-led/codex-router"

// archRule is one guarded package and the internal packages it may import.
// Standard library imports are otherwise free. Composition roots
// (cmd/codex-router, brain, launcher, service, setup, control, tui, hook,
// sessions, proxy, link, listen, appserver) are exempt on purpose: assembly
// needs the concrete transports, processes and files.
type archRule struct {
	name    string   // internal package name, as in the docs table
	allowed []string // internal packages it may import; nil means none
	pure    bool     // computes from its inputs: no network, filesystem or process
}

// archRules is the intended direction of docs/architecture.md, Packages.
var archRules = []archRule{
	// config imports nothing in the module, and every package may import it.
	{name: "config"},
	// policy is pure in its own files, but it imports classifier, whose model
	// client reaches net/http. The rule is a direct-import rule on purpose; see
	// the classifier-transitive-io check below instead of pretending that build
	// is I/O-free.
	{name: "policy", allowed: []string{"classifier", "config"}, pure: true},
	{name: "facts", allowed: []string{"responses"}, pure: true},
	{name: "cost", allowed: []string{"config", "responses"}, pure: true},
	// The engine decides with the model, policy and state vocabulary it is given,
	// and must not reach the transports (control, tui, proxy, link, listen,
	// appserver), the process assembly (launcher, brain, service, setup,
	// sessions) or the entry point.
	{name: "route", allowed: []string{"catalog", "classifier", "config", "cost", "facts", "home", "policy", "responses"}},
}

// guardedPackages is the fixed set archRules must cover, sorted. A rule deleted
// by accident fails the coverage check instead of quietly shrinking the guard.
var guardedPackages = []string{"config", "cost", "facts", "policy", "route"}

// ioRoots are standard library packages, and package roots with their
// subpackages, through which a pure package would reach the network, the
// filesystem, the environment or another process itself.
//
// path/filepath is here even though it is not an os alias: Glob, Walk, WalkDir
// and EvalSymlinks read the filesystem. Its sibling path only splits
// slash-separated strings, so path stays allowed.
var ioRoots = []string{"io/ioutil", "net", "os", "path/filepath", "syscall"}

// parsedOnlyImports only parse or format addresses, so they are not I/O and a
// pure package may use them. Banning harmless standard library packages would
// make the rule noise.
var parsedOnlyImports = []string{"net/netip", "net/url"}

func TestArchitectureDependencies(t *testing.T) {
	patterns := make([]string, 0, len(archRules))
	for _, rule := range archRules {
		patterns = append(patterns, rule.importPath())
	}
	listed := goList(t, patterns...)

	t.Run("covers every guarded package", func(t *testing.T) {
		names := make([]string, 0, len(archRules))
		for _, rule := range archRules {
			names = append(names, rule.name)
		}
		slices.Sort(names)
		if !slices.Equal(names, guardedPackages) {
			t.Fatalf("guarded packages = %v; want %v", names, guardedPackages)
		}
	})

	// The real graph: one allowlist check per guarded package, plus the
	// no-I/O denylist for the pure ones.
	for _, rule := range archRules {
		t.Run(rule.name, func(t *testing.T) {
			pkg, ok := listed[rule.importPath()]
			if !ok {
				t.Fatalf("%s is missing from the go list output for %v", rule.importPath(), patterns)
			}
			if pkg.Name != rule.name {
				t.Fatalf("%s has package name %q, want %q", pkg.ImportPath, pkg.Name, rule.name)
			}
			if extra := forbiddenInternalImports(pkg.Imports, rule.allowed); len(extra) > 0 {
				t.Fatalf("%s imports %v; allowed internal imports are %v (docs/architecture.md, Packages)", rule.name, extra, rule.allowed)
			}
			if !rule.pure {
				return
			}
			if outside := forbiddenPureImports(pkg.Imports); len(outside) > 0 {
				t.Fatalf("pure %s imports %v; pass the data in through its inputs and keep I/O in a transport or state package", rule.name, outside)
			}
		})
	}

	t.Run("rejects forbidden internal edges", func(t *testing.T) {
		tests := []struct {
			name  string
			guard string // the package the import graph is checked as
			edge  string // module-relative path it must not import
		}{
			{"config imports facts", "config", "internal/facts"},
			{"config imports route", "config", "internal/route"},
			{"policy imports route", "policy", "internal/route"},
			{"policy imports cost", "policy", "internal/cost"},
			{"facts imports catalog", "facts", "internal/catalog"},
			{"cost imports policy", "cost", "internal/policy"},
			{"route imports control", "route", "internal/control"},
			{"route imports tui", "route", "internal/tui"},
			{"route imports proxy", "route", "internal/proxy"},
			{"route imports appserver", "route", "internal/appserver"},
			{"route imports launcher", "route", "internal/launcher"},
			{"route imports the entry point", "route", "cmd/codex-router"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				rule := archRuleFor(t, tt.guard)
				// The package's current internal imports plus the one forbidden edge,
				// so the rule rejects exactly the edge and not today's graph.
				imports := append(internalPaths(rule.allowed), "slices", moduleImport(tt.edge))
				want := []string{moduleImport(tt.edge)}
				if got := forbiddenInternalImports(imports, rule.allowed); !slices.Equal(got, want) {
					t.Fatalf("forbiddenInternalImports(%v) = %v; want %v", imports, got, want)
				}
			})
		}
	})

	t.Run("rejects direct I/O in pure packages", func(t *testing.T) {
		tests := []struct {
			name    string
			imports []string
			want    []string
		}{
			{"network client", []string{"encoding/json", "net/http"}, []string{"net/http"}},
			{"network socket", []string{"net", "strings"}, []string{"net"}},
			{"http test server", []string{"net/http/httptest"}, []string{"net/http/httptest"}},
			{"files and environment", []string{"os", "slices"}, []string{"os"}},
			{"process", []string{"os/exec"}, []string{"os/exec"}},
			{"system call", []string{"syscall"}, []string{"syscall"}},
			// path/filepath is the filesystem-aware sibling of path: Glob, Walk,
			// WalkDir and EvalSymlinks read names off the disk, so it is I/O even
			// though it is not an os alias.
			{"filesystem glob, walk and symlinks", []string{"path/filepath"}, []string{"path/filepath"}},
			// The parse-only allowance covers path, net/url and net/netip, and it
			// must not stretch to filepath just because the import paths look alike.
			{"parse-only path stays allowed beside filesystem paths", []string{"path", "path/filepath", "net/url", "net/netip"}, []string{"path/filepath"}},
			{"third-party client", []string{"github.com/coder/websocket"}, []string{"github.com/coder/websocket"}},
			{"parsed addresses stay allowed", []string{"net/netip", "net/url", "path", "hash/fnv"}, nil},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := forbiddenPureImports(tt.imports); !slices.Equal(got, tt.want) {
					t.Fatalf("forbiddenPureImports(%v) = %v; want %v", tt.imports, got, tt.want)
				}
			})
		}
	})

	t.Run("test imports are not read", func(t *testing.T) {
		// A package whose only I/O imports are test-only passes, because the guard
		// decodes production Imports only. The second half proves those names would
		// fail the pure rule if they ever became production imports.
		var sample = `{
			"ImportPath": "sample-policy",
			"Name": "policy",
			"Imports": ["slices", "` + moduleImport("internal/config") + `"],
			"TestImports": ["net/http/httptest", "os", "testing"],
			"XTestImports": ["os/exec"]
		}`
		pkg, ok := decodePackages(t, strings.NewReader(sample))["sample-policy"]
		if !ok {
			t.Fatal("sample package was not decoded")
		}
		if want := []string{"slices", moduleImport("internal/config")}; !slices.Equal(pkg.Imports, want) {
			t.Fatalf("decoded imports = %v; want %v", pkg.Imports, want)
		}
		if got := forbiddenPureImports(pkg.Imports); len(got) > 0 {
			t.Fatalf("production-only imports %v were rejected: %v", pkg.Imports, got)
		}
		testOnly := []string{"net/http/httptest", "os", "testing"}
		if got, want := forbiddenPureImports(testOnly), []string{"net/http/httptest", "os"}; !slices.Equal(got, want) {
			t.Fatalf("forbiddenPureImports(%v) = %v; want %v", testOnly, got, want)
		}
	})

	t.Run("non-pure config keeps filesystem access", func(t *testing.T) {
		// config assembles files (it imports path/filepath today), so it is
		// deliberately not pure and never runs the denylist above. Pinning the
		// flag keeps a future tightening from breaking config instead of the
		// rule: move the I/O out first, then mark it pure.
		if rule := archRuleFor(t, "config"); rule.pure {
			t.Fatalf("config is marked pure, but it composes files; allowed internal imports are %v", rule.allowed)
		}
	})

	t.Run("classifier-transitive-io", func(t *testing.T) {
		// policy's importer chain reaches net/http through classifier's model
		// client. The pure rule above is deliberately direct-only: policy's own
		// files stay pure while the I/O stays in the classifier. This check records
		// the difference, so neither the guard nor a report claims that the build
		// behind policy is I/O-free.
		closure := goList(t, "-deps", moduleImport("internal/policy"))
		if _, ok := closure["net/http"]; !ok {
			t.Fatalf("policy's dependency closure no longer contains net/http; classify whether the direct-only pure rule is still the right claim")
		}
		direct := forbiddenPureImports(listed[moduleImport("internal/policy")].Imports)
		if len(direct) > 0 {
			t.Fatalf("policy imports %v directly", direct)
		}
		transitive := make([]string, 0, len(closure))
		for path := range closure {
			transitive = append(transitive, path)
		}
		if len(forbiddenPureImports(transitive)) == 0 {
			t.Fatalf("policy's dependency closure has no I/O import; the direct-only pure rule needs a new justification")
		}
	})
}

// archRuleFor returns the rule of a guarded package, or fails the test when the
// table and the case disagree.
func archRuleFor(t *testing.T, name string) archRule {
	t.Helper()
	for _, rule := range archRules {
		if rule.name == name {
			return rule
		}
	}
	t.Fatalf("%q is not a guarded package; guarded packages are %v", name, guardedPackages)
	return archRule{}
}

func (r archRule) importPath() string { return moduleImport("internal/" + r.name) }

// moduleImport is the full import path of a module-relative path.
func moduleImport(rel string) string { return modulePath + "/" + rel }

// internalPaths is the import paths of internal package names.
func internalPaths(names []string) []string {
	paths := make([]string, 0, len(names))
	for _, name := range names {
		paths = append(paths, moduleImport("internal/"+name))
	}
	return paths
}

// forbiddenInternalImports returns the sorted module-internal imports that are
// not on the allowlist. Imports outside the module are not its business.
func forbiddenInternalImports(imports, allowed []string) []string {
	permitted := make(map[string]bool, len(allowed))
	for _, path := range internalPaths(allowed) {
		permitted[path] = true
	}
	var forbidden []string
	for _, path := range imports {
		if !strings.HasPrefix(path, modulePath+"/") || permitted[path] {
			continue
		}
		forbidden = append(forbidden, path)
	}
	slices.Sort(forbidden)
	return forbidden
}

// forbiddenPureImports returns the sorted imports through which a pure package
// could reach the outside world: an I/O package, or any package outside the
// standard library and this module.
func forbiddenPureImports(imports []string) []string {
	var forbidden []string
	for _, path := range imports {
		if isIOImport(path) || isForeignImport(path) {
			forbidden = append(forbidden, path)
		}
	}
	slices.Sort(forbidden)
	return forbidden
}

func isIOImport(path string) bool {
	if slices.Contains(parsedOnlyImports, path) {
		return false
	}
	for _, root := range ioRoots {
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}

// isForeignImport reports whether path comes from outside the standard library
// and this module. Standard library paths have no dot in their first element.
func isForeignImport(path string) bool {
	if strings.HasPrefix(path, modulePath+"/") {
		return false
	}
	first, _, _ := strings.Cut(path, "/")
	return strings.Contains(first, ".")
}

// listedPackage is one package from `go list -json`. TestImports and
// XTestImports are deliberately absent: the guard reads production dependencies.
type listedPackage struct {
	ImportPath string
	Name       string
	Imports    []string
}

// goList runs `go list -json` with args and returns the packages by import path.
// -json writes one object per package, so the stream is decoded in a loop.
func goList(t *testing.T, args ...string) map[string]listedPackage {
	t.Helper()
	cmd := exec.Command("go", append([]string{"list", "-json"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list %v: %v: %s", args, err, strings.TrimSpace(stderr.String()))
	}
	return decodePackages(t, bytes.NewReader(out))
}

func decodePackages(t *testing.T, r io.Reader) map[string]listedPackage {
	t.Helper()
	packages := map[string]listedPackage{}
	dec := json.NewDecoder(r)
	for {
		var pkg listedPackage
		err := dec.Decode(&pkg)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		if pkg.ImportPath == "" {
			t.Fatal("go list output has a package without an import path")
		}
		packages[pkg.ImportPath] = pkg
	}
	if len(packages) == 0 {
		t.Fatal("go list output is empty")
	}
	return packages
}
