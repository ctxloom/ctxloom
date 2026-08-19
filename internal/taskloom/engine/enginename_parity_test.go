package engine

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	ltkengine "github.com/ctxloom/ctxloom/internal/ltk/engine"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// engineNameCorpus is the FLOOR of spellings every registry in
// parityRegistries must agree about: the ones it has to accept, and the shapes
// it has to refuse. It is a floor, not the whole set — derivedAliasCases below
// adds every spelling the shared alias table currently declares, so an alias
// added to agent.engineAliases is covered without an edit here.
var engineNameCorpus = []struct {
	in   string
	want string // canonical name, or "" when the spelling names no engine
}{
	{"claude-code", "claude-code"},
	{"CLAUDE-CODE", "claude-code"},
	{"claudecode", "claude-code"},
	{"claude", "claude-code"},
	{"CLAUDE", "claude-code"},
	{"antigravity", ""}, // removed engine (0.7.0): no longer resolves anywhere
	{"agy", ""},         // its former alias, also removed
	{"antigravity-cli", ""},
	{"", ""},
	{"claude-", ""},
	{"clau", ""},
	{"antigrav", ""},
	{"nonsense", ""},
}

// engineRegistry is one name -> engine registry the shared vocabulary has to
// reach. resolve returns the canonical name the registry landed on, or an
// error when it refuses the spelling; exists is the registry's cheap
// membership predicate where it has one, and must never disagree with resolve
// (a caller that gates on the predicate and then dereferences the lookup is
// the shape that turns a refusal into a nil).
type engineRegistry struct {
	// pkg is the registry's import path. It is what the coverage gate
	// (TestEngineNameResolvers_AreAllUnderParity) matches against, so it must
	// be the real path, not a nickname.
	pkg     string
	resolve func(in string) (string, error)
	exists  func(in string) bool
}

// parityRegistries is THE coverage list: every registry that turns a
// user-typed engine name into an engine. A registry missing from it is not
// silently uncovered — the coverage gate below fails on any production package
// that resolves engine names and is named neither here nor in
// nonRegistryCanonicalUsers.
func parityRegistries() []engineRegistry {
	return []engineRegistry{
		{
			pkg: "github.com/ctxloom/ctxloom/internal/taskloom/engine",
			resolve: func(in string) (string, error) {
				e, err := Get(in)
				if err != nil {
					return "", err
				}
				return e.Name(), nil
			},
		},
		{
			pkg: "github.com/ctxloom/ctxloom/internal/ltk/engine",
			resolve: func(in string) (string, error) {
				e, err := ltkengine.Get(in)
				if err != nil {
					return "", err
				}
				return e.Name(), nil
			},
		},
		{
			pkg: "github.com/ctxloom/ctxloom/internal/lm/backends",
			resolve: func(in string) (string, error) {
				b := backends.Get(in)
				if b == nil {
					return "", fmt.Errorf("unknown engine %q", in)
				}
				return b.Name(), nil
			},
			exists: backends.Exists,
		},
	}
}

// nonRegistryCanonicalUsers are the production packages that consult the
// shared alias table WITHOUT being a name -> engine registry, each with the
// reason it cannot carry a resolve func. The list exists so the coverage gate
// forces a decision — registry or not — instead of letting a new consumer of
// engine names go unexamined.
var nonRegistryCanonicalUsers = map[string]string{
	// The table itself.
	"github.com/ctxloom/ctxloom/internal/shared/agent": "declares the alias table",
	// A write boundary, not a lookup: it canonicalizes the engine name being
	// PERSISTED so the stored config matches the schema's per-backend const.
	"github.com/ctxloom/ctxloom/internal/operations": "canonicalizes on write into config, resolves nothing",
	// A test engine binary: it maps a name to a mimicked PERSONALITY, not to a
	// registered engine, so it has no membership to be in parity about.
	"github.com/ctxloom/ctxloom/cmd/mockengine": "selects a mimicked personality, not a registered engine",
	// Several engine-keyed TABLES (credential seeds, instance-config writers,
	// credential projectors, container specs), not one name -> engine
	// registry: there is no single membership to resolve against, and each
	// table asserts its own alias coverage from the shared table in
	// internal/lm/isolation's own tests. It canonicalizes at every lookup and
	// pins canonical keys at registration.
	"github.com/ctxloom/ctxloom/internal/lm/isolation": "canonicalizes lookups into several keyed tables, no single membership",
}

// TestEngineNameVocabularyParity pins every engine registry to ONE spelling
// vocabulary. The engine names are shared vocabulary — a user types the same
// name as ltk's --engine, as taskloom's --engine and as ctxloom's --type — and
// each registry deciding independently is how a spelling resolves under one
// binary and errors under another.
func TestEngineNameVocabularyParity(t *testing.T) {
	registries := parityRegistries()
	require.NotEmpty(t, registries)

	for _, tc := range append(slices.Clone(engineNameCorpus), derivedAliasCases(t, registries)...) {
		assert.Equal(t, tc.want, canonicalOrEmpty(tc.in), "agent.CanonicalEngineName(%q)", tc.in)

		for _, r := range registries {
			got, err := r.resolve(tc.in)
			if tc.want == "" {
				assert.Error(t, err, "%s must reject %q", r.pkg, tc.in)
			} else {
				require.NoError(t, err, "%s must resolve %q", r.pkg, tc.in)
				assert.Equal(t, tc.want, got, "%s resolved %q", r.pkg, tc.in)
			}
			if r.exists == nil {
				continue
			}
			assert.Equal(t, err == nil, r.exists(tc.in),
				"%s: membership predicate disagrees with the lookup for %q", r.pkg, tc.in)
		}
	}
}

// derivedAliasCases expands the corpus with every spelling the shared alias
// table declares, for each canonical name EVERY registry holds. The registries
// carry deliberately different memberships (ltk drives only claude-code;
// backends also holds acp, opencode and the mock), so a name only some of them
// know cannot be asserted in parity — but for the ones they share, an alias
// added to agent.engineAliases must not need an edit in this file to be tested.
func derivedAliasCases(t *testing.T, registries []engineRegistry) []struct {
	in   string
	want string
} {
	t.Helper()
	var cases []struct {
		in   string
		want string
	}
	shared := 0
	for _, name := range backends.List() {
		if !resolvedEverywhere(name, registries) {
			continue
		}
		shared++
		spellings := append([]string{name}, agent.EngineNameAliases(name)...)
		for _, s := range spellings {
			cases = append(cases, struct {
				in   string
				want string
			}{s, name}, struct {
				in   string
				want string
			}{strings.ToUpper(s), name})
		}
	}
	require.NotZero(t, shared, "no engine name resolves in every registry — the parity assertion would be vacuous")
	return cases
}

// resolvedEverywhere reports whether name resolves to itself in every registry.
func resolvedEverywhere(name string, registries []engineRegistry) bool {
	for _, r := range registries {
		if got, err := r.resolve(name); err != nil || got != name {
			return false
		}
	}
	return true
}

// TestEngineNameResolvers_AreAllUnderParity makes an omission from
// parityRegistries DETECTABLE. The defect this whole file guards was not a
// registry that resolved names wrongly — it was a registry nobody had added to
// the list, so nothing ever asked. Here every production package that consults
// agent.CanonicalEngineName must be declared: a covered registry, or an
// explicitly reasoned non-registry.
//
// The residual gap is stated rather than hidden: a registry that never
// consults the shared table AT ALL is invisible to a scan keyed on calls to
// it. Nothing syntactic distinguishes such a package from any other map lookup.
// What this does buy is that the moment such a registry is fixed to consult the
// table, it must join this list or fail here.
func TestEngineNameResolvers_AreAllUnderParity(t *testing.T) {
	covered := make(map[string]bool, len(parityRegistries()))
	for _, r := range parityRegistries() {
		covered[r.pkg] = true
	}

	callers := canonicalEngineNameCallers(t)
	require.NotEmpty(t, callers, "the scan found no callers of agent.CanonicalEngineName — it is looking at the wrong tree")

	for _, pkg := range callers {
		if covered[pkg] {
			continue
		}
		if _, exempt := nonRegistryCanonicalUsers[pkg]; exempt {
			continue
		}
		t.Errorf("package %s resolves engine names through agent.CanonicalEngineName but is in neither "+
			"parityRegistries() nor nonRegistryCanonicalUsers — add it to the parity coverage list so its "+
			"vocabulary is asserted, or declare why it is not a registry", pkg)
	}
}

// canonicalEngineNameCallers returns the import path of every production
// (non-_test.go) package under the module that calls agent.CanonicalEngineName,
// sorted. Parsed from source rather than matched as text so a mention in a
// comment or a string is not a caller.
func canonicalEngineNameCallers(t *testing.T) []string {
	t.Helper()
	root := moduleRoot(t)
	seen := map[string]bool{}

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != root && (name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		if !callsCanonicalEngineName(file) {
			return nil
		}
		rel, rerr := filepath.Rel(root, filepath.Dir(path))
		if rerr != nil {
			return rerr
		}
		seen[path2ImportPath(rel)] = true
		return nil
	})
	require.NoError(t, err)

	pkgs := make([]string, 0, len(seen))
	for p := range seen {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	return pkgs
}

// callsCanonicalEngineName reports whether file contains a call whose callee is
// a selector named CanonicalEngineName, or a bare call to it from inside the
// declaring package itself.
func callsCanonicalEngineName(file *ast.File) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || found {
			return !found
		}
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			found = fn.Sel.Name == "CanonicalEngineName"
		case *ast.Ident:
			found = fn.Name == "CanonicalEngineName"
		}
		return !found
	})
	return found
}

// modulePath is the module this repo builds as; the scan turns directories
// relative to the module root back into import paths with it.
const modulePath = "github.com/ctxloom/ctxloom"

// path2ImportPath renders a module-root-relative directory as an import path.
func path2ImportPath(rel string) string {
	if rel == "." {
		return modulePath
	}
	return modulePath + "/" + filepath.ToSlash(rel)
}

// moduleRoot walks up from the test's working directory to the directory
// holding go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "walked to the filesystem root without finding go.mod")
		dir = parent
	}
}

// canonicalOrEmpty reports the canonical engine name for in, or "" when in
// names no engine this registry holds.
func canonicalOrEmpty(in string) string {
	got := agent.CanonicalEngineName(in)
	for _, e := range All() {
		if e.Name() == got {
			return got
		}
	}
	return ""
}
