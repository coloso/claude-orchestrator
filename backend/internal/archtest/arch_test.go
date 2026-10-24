// Package archtest enforces the hexagonal/DDD dependency rules on every `go test ./...`.
//
// A bounded context lives in internal/<context>/ with three layers:
//
//	domain/        aggregates, value objects, domain errors, repository interfaces
//	app/           use cases (inbound port interface + service)
//	adapter/<x>/   driving (httpapi) and driven (memory, postgres, ...) adapters
//
// Dependencies point inwards: adapter -> app -> domain. Contexts never import each other.
// Only internal/handler (embeds HTTP adapters) and internal/bootstrap (composition root)
// may import a context from outside.
package archtest

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const module = "orch-test/backend"

// Standard library packages the inner layers must not use: they are transport or storage concerns.
var forbiddenInnerStd = []string{"net/http", "database/sql", "encoding/json", "os/exec"}

// Third-party modules the inner layers may use. Keep this short and deliberate.
var allowedInnerThirdParty = []string{}

type layer struct {
	context string // e.g. "todo"; empty for non-context packages
	name    string // "domain", "app", "adapter"
	adapter string // adapter name for the adapter layer, e.g. "httpapi"
}

func classify(importPath string) layer {
	rel, ok := strings.CutPrefix(importPath, module+"/internal/")
	if !ok {
		return layer{}
	}
	parts := strings.Split(rel, "/")
	if len(parts) < 2 {
		return layer{}
	}
	switch parts[1] {
	case "domain", "app":
		return layer{context: parts[0], name: parts[1]}
	case "adapter":
		l := layer{context: parts[0], name: "adapter"}
		if len(parts) > 2 {
			l.adapter = parts[2]
		}
		return l
	}
	return layer{}
}

func isStd(path string) bool { return !strings.Contains(strings.SplitN(path, "/", 2)[0], ".") }

func hasPrefixPath(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// violations returns the dependency-rule violations of one package's (non-test) imports.
func violations(pkg string, imports []string) []string {
	var out []string
	from := classify(pkg)
	for _, imp := range imports {
		if err := check(pkg, from, imp); err != "" {
			out = append(out, fmt.Sprintf("%s imports %s: %s", pkg, imp, err))
		}
	}
	return out
}

func check(pkg string, from layer, imp string) string {
	to := classify(imp)
	project := hasPrefixPath(imp, module)

	// Outside any context: only the composition points may reach into contexts.
	if from.context == "" {
		if to.context == "" {
			return ""
		}
		switch {
		case pkg == module+"/internal/bootstrap":
			return ""
		case pkg == module+"/internal/handler" && to.name == "adapter" && to.adapter == "httpapi":
			return ""
		}
		return "only internal/bootstrap (and internal/handler, for httpapi adapters) may import a bounded context"
	}

	if to.context != "" && to.context != from.context {
		return fmt.Sprintf("bounded context %q must not import context %q", from.context, to.context)
	}

	inner := from.name == "domain" || from.name == "app"
	if inner {
		if project && to.context == "" {
			return from.name + " must not depend on infrastructure packages (oas, server, ...)"
		}
		if !project && isStd(imp) {
			for _, f := range forbiddenInnerStd {
				if hasPrefixPath(imp, f) {
					return from.name + " must not use " + f + "; that belongs in an adapter"
				}
			}
		}
		if !project && !isStd(imp) {
			for _, a := range allowedInnerThirdParty {
				if hasPrefixPath(imp, a) {
					return ""
				}
			}
			return from.name + " must not use third-party modules (see allowedInnerThirdParty)"
		}
	}

	switch from.name {
	case "domain":
		if to.context != "" && to.name != "domain" {
			return "domain must not import " + to.name
		}
	case "app":
		if to.context != "" && to.name == "adapter" {
			return "app must not import adapters; depend on ports instead"
		}
	case "adapter":
		if to.name == "adapter" && to.adapter != from.adapter {
			return "adapters must not import each other"
		}
		if hasPrefixPath(imp, module+"/internal/handler") || hasPrefixPath(imp, module+"/internal/bootstrap") {
			return "adapters must not import the composition packages"
		}
	}
	return ""
}

// packageImports parses the module's non-test Go files and returns import path -> imports.
// It reads the files directly (not via `go list`) so `go test` caching notices every change.
func packageImports(root string) (map[string][]string, error) {
	pkgs := map[string]map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != root && (strings.HasPrefix(name, ".") || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.ImportsOnly)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, filepath.Dir(path))
		pkg := module
		if rel != "." {
			pkg += "/" + filepath.ToSlash(rel)
		}
		if pkgs[pkg] == nil {
			pkgs[pkg] = map[string]bool{}
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			pkgs[pkg][p] = true
		}
		return nil
	})
	out := map[string][]string{}
	for pkg, set := range pkgs {
		for imp := range set {
			out[pkg] = append(out[pkg], imp)
		}
		sort.Strings(out[pkg])
	}
	return out, err
}

func TestDependencyRules(t *testing.T) {
	pkgs, err := packageImports("../..")
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) == 0 {
		t.Fatal("no packages found")
	}
	names := make([]string, 0, len(pkgs))
	for pkg := range pkgs {
		names = append(names, pkg)
	}
	sort.Strings(names)
	for _, pkg := range names {
		for _, v := range violations(pkg, pkgs[pkg]) {
			t.Error(v)
		}
	}
}

func TestRules(t *testing.T) {
	m := module + "/internal/"
	tests := []struct {
		pkg, imp string
		ok       bool
	}{
		{m + "todo/domain", "errors", true},
		{m + "todo/domain", "net/http", false},
		{m + "todo/domain", m + "oas", false},
		{m + "todo/domain", m + "todo/app", false},
		{m + "todo/domain", "github.com/google/uuid", false},
		{m + "todo/app", m + "todo/domain", true},
		{m + "todo/app", m + "todo/adapter/memory", false},
		{m + "todo/app", "encoding/json", false},
		{m + "todo/adapter/httpapi", m + "todo/app", true},
		{m + "todo/adapter/httpapi", m + "oas", true},
		{m + "todo/adapter/httpapi", "net/http", true},
		{m + "todo/adapter/httpapi", m + "todo/adapter/memory", false},
		{m + "todo/adapter/httpapi", m + "handler", false},
		{m + "todo/adapter/memory", m + "todo/domain", true},
		{m + "todo/app", m + "billing/domain", false},
		{m + "handler", m + "todo/adapter/httpapi", true},
		{m + "handler", m + "todo/app", false},
		{m + "server", m + "todo/domain", false},
		{m + "bootstrap", m + "todo/adapter/memory", true},
		{module + "/e2e", m + "todo/domain", false},
		{module + "/e2e", m + "bootstrap", true},
		{m + "todo/domain", m + "platform/mongodb", false},
		{m + "todo/app", "go.mongodb.org/mongo-driver/v2/mongo", false},
		{m + "todo/adapter/mongodb", m + "platform/mongodb", true},
		{m + "todo/adapter/mongodb", "go.mongodb.org/mongo-driver/v2/mongo", true},
		{m + "todo/adapter/mongodb", m + "todo/domain", true},
		{m + "todo/adapter/mongodb", m + "todo/adapter/memory", false},
		{m + "platform/mongodb", m + "todo/domain", false},
	}
	for _, tt := range tests {
		got := len(violations(tt.pkg, []string{tt.imp})) == 0
		if got != tt.ok {
			t.Errorf("%s -> %s: allowed = %v, want %v", tt.pkg, tt.imp, got, tt.ok)
		}
	}
}
