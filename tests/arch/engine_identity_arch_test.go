//go:build arch

// T12, tightened: "no special casing or if/then between engines, except at
// initial setup." Engine-specific behaviour is reached ONLY through the
// engine.Definition / approach / capability seams; outside initial setup no
// production code may name or choose a specific engine.
//
// TestArch_EngineIdentity_OnlyInitialSetupNamesAnEngine is the gate. Its
// contract, over every production (non-_test.go) Go file of the module:
//
//   - NO IMPORT of a concrete engine package (a package under
//     internal/engines/<x>). The registry root itself, internal/engines, is the
//     port's composition and stays importable.
//   - NO STRING CONSTANT EQUAL TO AN ENGINE'S IDENTITY: a registered engine
//     name (operations.EngineNames over the composed registry) or an engine ID
//     (the Go package a registered kind is declared in — "claude", "mock").
//     Both sets are read LIVE from the registry, so a new engine needs no edit
//     here. The comparison is case-insensitive, and it is made against every
//     string literal AND every constant expression the gate can fold — a
//     concatenation, a parenthesised or converted operand, a reference to a
//     constant of the same package — so renaming a literal into a constant,
//     splitting it into pieces, or converting it to engine.Name moves the
//     violation, it does not hide it. A literal roster of engines is a set of
//     such constants, so it fails here too: a roster must be a DERIVED VIEW
//     over the registry (see the roster tests below).
//
// INITIAL SETUP — the only code exempt — is defined by structure, never by a
// list of files:
//
//   - internal/engines and everything below it: the composition root
//     (engines.Build, the registry wiring) and the engine packages themselves;
//   - every `package main`: a binary's own composition root (config `type` →
//     engine selection reaches the registry from there);
//   - an engine's FAMILY: a package whose last path element is a registered
//     engine's ID (internal/adapters/transcript/vendorreader/<id>), the
//     engine-specific adapter the composition root hands that engine;
//   - a lean binary's own engine registry, internal/<bin>/engine for a binary
//     cmd/<bin>: ltk and taskloom cannot link ctxloom's registry, so each
//     composes its own there (and holds its own engine adapters).
//
// Test code is exempt: _test.go files, tests/, internal/testsupport/ and the
// Go-convention test-double packages (a last path element ending in "test").
//
// RESIDUAL GAPS — stated, not hidden. The gate is syntactic plus constant
// folding; it cannot see:
//
//   - an identity built at RUN time (strings.Join, fmt.Sprintf, a []byte or
//     rune literal, a value read from a file or the environment) and then
//     compared;
//   - a constant of ANOTHER package that is itself folded from pieces: it is
//     caught where it is DEFINED, not where it is used, so the report names
//     the definition;
//   - an identity check dressed as a capability: a Definition field that only
//     one engine sets, branched on as though it were a capability. Whether a
//     declared fact is a real capability is a review judgement, not a parse;
//   - a native wire shape decoded outside the engine (a struct whose json tags
//     spell one engine's payload): it names no engine. The hook verbs read
//     payloads through engine.HookCodec for exactly this reason.
//
// The roster tests below remain: they hold every DERIVED roster to the
// registry in both directions.
package arch

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines"
)

// rosterCheck is one of T12's four engine-identity rosters: a named source
// (for error messages) and the backend names it currently lists.
type rosterCheck struct {
	source  string
	members []string
}

// TestArch_EngineIdentityRosters_MembersAreRegisteredBackends is the roster
// half of T12's fix: every name any of the four independently-maintained
// engine-identity rosters lists must be a real, CURRENTLY-registered
// composed engine name — never a typo, and never a stale reference left
// behind when a backend was renamed or removed from the canonical registry
// (the composed engine registry, engines.Registry, the source of truth every
// one of these rosters is a purpose-scoped VIEW over, per
// docs/adr/0026-ports-and-adapters.md). Reads operations.EngineNames() live rather
// than naming backends here, so a new, correctly-registered backend never
// requires updating this test.
func TestArch_EngineIdentityRosters_MembersAreRegisteredBackends(t *testing.T) {
	known := operations.EngineNames(engines.Registry())
	if len(known) == 0 {
		t.Fatal("operations.EngineNames() returned nothing — the canonical registry did not populate; the gate has " +
			"nothing to validate against")
	}

	rosters := []rosterCheck{
		{source: "internal/adapters/operations.VendorReaderEngineNames (vendorReaderRegistry)", members: operations.VendorReaderEngineNames(engines.Registry())},
		{source: "internal/adapters/isolation.ComposableEngines (pushed engine.Descriptor.Container)", members: isolation.ComposableEngines()},
	}

	for _, r := range rosters {
		if len(r.members) == 0 {
			t.Errorf("%s reported zero members — either the roster is genuinely empty (update this test to say so "+
				"explicitly) or its accessor is broken", r.source)
			continue
		}
		for _, name := range r.members {
			if !slices.Contains(known, name) {
				t.Errorf("%s lists backend %q, which is not a currently-registered composed engine name "+
					"(known: %v) — a typo, or a stale entry from a rename/removal in the canonical registry",
					r.source, name, known)
			}
		}
	}
}

// derivedRoster is a roster that is a VIEW over the registry: members are
// the registered backends whose descriptor provides the fact, and absence
// reads back the declared reason for every backend that is not a member.
type derivedRoster struct {
	source  string
	members []string
	absence func(name string) string
}

// transcriptAbsence explains a registered backend outside the vendor-reader
// roster: its kind supplies no readers (an empty slice, the port's absence).
func transcriptAbsence(name string) string {
	if _, ok := operations.VendorReaderAdaptersFor(engines.Registry(), name); ok {
		return ""
	}
	return name + " supplies no transcript readers (Engine.Transcripts is empty)"
}

// TestArch_DerivedEngineRosters_CoverEveryRegisteredBackend is the reverse of
// the floor above, for the rosters that are derived from the registry: every
// registered backend either appears in the roster or its descriptor declares
// the fact absent WITH A REASON. A forgotten entry cannot exist by
// construction; what this catches is the derivation itself dropping a member
// (a filter that skips an engine the descriptor provides for) — the
// mutation "remove an engine from the derived roster" dies here.
func TestArch_DerivedEngineRosters_CoverEveryRegisteredBackend(t *testing.T) {
	known := operations.EngineNames(engines.Registry())
	if len(known) == 0 {
		t.Fatal("operations.EngineNames() returned nothing — the canonical registry did not populate; the gate has " +
			"nothing to validate against")
	}

	rosters := []derivedRoster{
		{
			source:  "internal/adapters/operations.VendorReaderEngineNames (Engine.Transcripts)",
			members: operations.VendorReaderEngineNames(engines.Registry()),
			absence: transcriptAbsence,
		},
		{
			source:  "internal/adapters/isolation.ComposableEngines (Engine.Container + Distribution)",
			members: isolation.ComposableEngines(),
			absence: containerAbsence(func(c engine.ContainerSpec, dist engine.Distribution) string {
				switch {
				case c.Install == nil:
					return "declares no container installer"
				case dist != engine.DistributionDefault:
					return "ships " + dist.String() + ", so it is not default-composed"
				}
				return ""
			}),
		},
		{
			source:  "internal/adapters/isolation.ContainerStoryEngines (Engine.Container + Distribution)",
			members: isolation.ContainerStoryEngines(),
			absence: containerAbsence(func(_ engine.ContainerSpec, dist engine.Distribution) string {
				if dist == engine.DistributionTestOnly {
					return "a test double is never offered"
				}
				return ""
			}),
		},
	}

	for _, r := range rosters {
		for _, name := range known {
			member := slices.Contains(r.members, name)
			reason := r.absence(name)
			switch {
			case member && reason != "":
				t.Errorf("%s lists %q, whose descriptor declares the fact ABSENT (%q) — the derivation is not "+
					"reading the declaration", r.source, name, reason)
			case !member && reason == "":
				t.Errorf("%s omits registered backend %q, and its descriptor gives no reason for the absence — "+
					"either the derivation dropped a member, or the engine's declaration is not reaching the roster",
					r.source, name)
			}
		}
	}
}

// containerAbsence explains why a registered backend is outside a
// container roster: its kind refuses Container (that refusal), or the
// roster's own filter — capability or policy — excludes it, per why.
func containerAbsence(why func(engine.ContainerSpec, engine.Distribution) string) func(string) string {
	return func(name string) string {
		kind, ok := engines.Registry().Lookup(engine.Name(name))
		if !ok {
			return ""
		}
		c, err := kind.Container()
		if err != nil {
			return err.Error()
		}
		return why(c, kind.Root().Distribution)
	}
}

// transcriptSchemaRelPath is the published canonical-transcript schema whose
// `engine` enum is one more engine-identity roster — the one external readers
// of a transcript see.
const transcriptSchemaRelPath = "docs/transcript.schema.json"

// TestArch_TranscriptSchemaEngineEnum_EqualsBackendRegistry holds the schema's
// `engine` enum to EQUALITY with operations.EngineNames(), not just the floor the
// rosters gate above applies. The recorder writes the registered backend name
// verbatim (internal/adapters/transcript.Record.Engine) and every registered backend
// reaches it (a oneshot run records under whatever `--llm` resolved to), so
// the set of names a transcript can carry IS the registry: a name in the enum
// that nothing registers admits fixtures no writer could produce, and a
// registered name missing from the enum makes a real transcript fail
// validation. Reads both sides live so neither a new backend nor a removal
// needs an edit here — only the schema does.
func TestArch_TranscriptSchemaEngineEnum_EqualsBackendRegistry(t *testing.T) {
	registered := operations.EngineNames(engines.Registry())
	if len(registered) == 0 {
		t.Fatal("operations.EngineNames() returned nothing — the canonical registry did not populate; the gate has " +
			"nothing to validate against")
	}

	data, err := os.ReadFile(filepath.Join(moduleRoot(t), transcriptSchemaRelPath))
	if err != nil {
		t.Fatalf("read %s: %v", transcriptSchemaRelPath, err)
	}
	var schema struct {
		Properties struct {
			Engine struct {
				Enum []string `json:"enum"`
			} `json:"engine"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("parse %s: %v", transcriptSchemaRelPath, err)
	}
	enum := slices.Clone(schema.Properties.Engine.Enum)
	if len(enum) == 0 {
		t.Fatalf("%s declares no engine enum — the roster this gate checks is gone", transcriptSchemaRelPath)
	}

	sort.Strings(enum)
	if !slices.Equal(enum, registered) {
		t.Errorf("%s `engine` enum %v != operations.EngineNames() %v — the enum must name exactly the registered "+
			"backends: a member nothing registers admits fixtures no writer produces, and a registered "+
			"backend missing from it makes a real transcript fail validation",
			transcriptSchemaRelPath, enum, registered)
	}
}

// engineIdentities is every spelling the gate refuses outside initial setup,
// lower-cased: each registered engine's name and each registered kind's ID
// (the last element of the Go package the kind's type is declared in). Read
// live from the composed registry, so a new engine needs no edit here.
func engineIdentities(t *testing.T) (identities map[string]bool, ids map[string]bool) {
	t.Helper()
	reg := engines.Registry()
	names := operations.EngineNames(reg)
	if len(names) == 0 {
		t.Fatal("operations.EngineNames() returned nothing — the canonical registry did not populate; the gate has nothing to refuse")
	}
	identities, ids = map[string]bool{}, map[string]bool{}
	for _, n := range names {
		identities[strings.ToLower(n)] = true
		kind, ok := reg.Lookup(engine.Name(n))
		if !ok {
			t.Fatalf("registered name %q does not look up", n)
		}
		pkgPath := reflect.TypeOf(kind).PkgPath()
		if pkgPath == "" {
			t.Fatalf("engine %q is not a named type; its ID cannot be derived", n)
		}
		id := strings.ToLower(path.Base(pkgPath))
		identities[id] = true
		ids[id] = true
	}
	return identities, ids
}

// identityFile is one parsed production file and the package it belongs to.
type identityFile struct {
	rel  string // module-relative file path
	dir  string // module-relative package directory
	file *ast.File
}

// parseProductionFiles parses every non-test Go file of the module.
func parseProductionFiles(t *testing.T, fset *token.FileSet) []identityFile {
	t.Helper()
	root := moduleRoot(t)
	var out []identityFile
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && skippedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !isNonTestGoFile(d.Name()) {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		f, perr := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Errorf("parse %s: %v", rel, perr)
			return nil
		}
		out = append(out, identityFile{rel: rel, dir: path.Dir(rel), file: f})
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("the scan parsed no production file — the gate is looking at the wrong tree")
	}
	return out
}

// isTestCodeDir reports a directory that holds test code only: tests/, the
// testsupport tree, and the Go-convention test-double packages.
func isTestCodeDir(dir string) bool {
	return dir == "tests" || strings.HasPrefix(dir, "tests/") ||
		strings.Contains("/"+dir+"/", "/testsupport/") ||
		strings.HasSuffix(path.Base(dir), "test")
}

// isInitialSetup reports a package that may name an engine: the engines
// tree, a binary's main package, an engine's family package, or a lean
// binary's own engine registry (see the file doc).
func isInitialSetup(dir, pkgName string, ids map[string]bool, binaries map[string]bool) bool {
	switch {
	case dir == "internal/engines" || strings.HasPrefix(dir, "internal/engines/"):
		return true
	case pkgName == "main":
		return true
	case ids[strings.ToLower(path.Base(dir))]:
		return true
	}
	parts := strings.Split(dir, "/")
	return len(parts) == 3 && parts[0] == "internal" && parts[2] == "engine" && binaries[parts[1]]
}

// binaryNames are the binaries the module builds: cmd/<bin>.
func binaryNames(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(moduleRoot(t), "cmd"))
	if err != nil {
		t.Fatalf("read cmd/: %v", err)
	}
	out := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			out[e.Name()] = true
		}
	}
	return out
}

// packageConsts maps each package directory to its constants' defining
// expressions, by name, for the fold.
func packageConsts(files []identityFile) map[string]map[string]ast.Expr {
	out := map[string]map[string]ast.Expr{}
	for _, f := range files {
		m := out[f.dir]
		if m == nil {
			m = map[string]ast.Expr{}
			out[f.dir] = m
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			gd, ok := n.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				return true
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if i < len(vs.Values) {
						m[name.Name] = vs.Values[i]
					}
				}
			}
			return true
		})
	}
	return out
}

// foldString folds a constant string expression: a literal, a concatenation,
// a parenthesis, a one-argument conversion, or a reference to a constant of
// the same package. ok is false for anything that is not foldable here.
func foldString(e ast.Expr, consts map[string]ast.Expr, depth int) (string, bool) {
	if depth > 16 {
		return "", false
	}
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(x.Value)
		return v, err == nil
	case *ast.ParenExpr:
		return foldString(x.X, consts, depth+1)
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		l, lok := foldString(x.X, consts, depth+1)
		r, rok := foldString(x.Y, consts, depth+1)
		return l + r, lok && rok
	case *ast.CallExpr:
		if len(x.Args) != 1 {
			return "", false
		}
		return foldString(x.Args[0], consts, depth+1)
	case *ast.Ident:
		if def, ok := consts[x.Name]; ok {
			return foldString(def, consts, depth+1)
		}
	}
	return "", false
}

// identityViolations reports, for one file outside initial setup, every
// concrete-engine import and every literal or folded constant expression
// spelling an engine identity.
func identityViolations(fset *token.FileSet, f identityFile, consts map[string]ast.Expr, identities map[string]bool) []string {
	var out []string
	for _, spec := range f.file.Imports {
		ip, err := strconv.Unquote(spec.Path.Value)
		if err == nil && strings.HasPrefix(ip, modulePath+"/internal/engines/") {
			out = append(out, fmt.Sprintf("%s imports the concrete engine package %s", fset.Position(spec.Pos()), ip))
		}
	}
	skip := map[ast.Node]bool{}
	for _, spec := range f.file.Imports {
		skip[spec.Path] = true
	}
	ast.Inspect(f.file, func(n ast.Node) bool {
		if n == nil || skip[n] {
			return false
		}
		switch x := n.(type) {
		case *ast.Field:
			if x.Tag != nil {
				skip[x.Tag] = true
			}
		case *ast.BasicLit, *ast.BinaryExpr:
			if v, ok := foldString(x.(ast.Expr), consts, 0); ok && identities[strings.ToLower(v)] {
				out = append(out, fmt.Sprintf("%s spells the engine identity %q", fset.Position(x.Pos()), v))
				return false
			}
		}
		return true
	})
	return out
}

// TestArch_EngineIdentity_OnlyInitialSetupNamesAnEngine is the gate the file
// doc describes: outside initial setup, no production code imports a
// concrete engine package or spells an engine's name or ID.
func TestArch_EngineIdentity_OnlyInitialSetupNamesAnEngine(t *testing.T) {
	identities, ids := engineIdentities(t)
	binaries := binaryNames(t)
	fset := token.NewFileSet()
	files := parseProductionFiles(t, fset)
	consts := packageConsts(files)

	checked := 0
	for _, f := range files {
		if isTestCodeDir(f.dir) || isInitialSetup(f.dir, f.file.Name.Name, ids, binaries) {
			continue
		}
		checked++
		for _, v := range identityViolations(fset, f, consts[f.dir], identities) {
			t.Errorf("%s — outside initial setup an engine is reached only through the engine.Definition / "+
				"approach / capability seams (the registry, Engine.Hooks(), Engine.Transcripts(), ...), never by name", v)
		}
	}
	if checked == 0 {
		t.Fatal("every production file was classified as initial setup or test code — the gate checked nothing")
	}
}

// TestArch_EngineIdentity_GateSeesWhatItRefuses proves the detector on
// synthetic source: each shape a violation can take is reported, and the
// shapes that are not violations are not.
func TestArch_EngineIdentity_GateSeesWhatItRefuses(t *testing.T) {
	identities, _ := engineIdentities(t)
	var name string
	for n := range identities {
		if len(n) > 2 {
			name = n
			break
		}
	}
	half := len(name) / 2
	cases := []struct {
		label, src string
		want       bool
	}{
		{"a literal", fmt.Sprintf("package p\nfunc f(s string) bool { return s == %q }\n", name), true},
		{"an upper-cased literal", fmt.Sprintf("package p\nvar x = %q\n", strings.ToUpper(name)), true},
		{"a split constant", fmt.Sprintf("package p\nconst a = %q\nconst b = a + %q\n", name[:half], name[half:]), true},
		{"a converted literal", fmt.Sprintf("package p\ntype N string\nvar x = N(%q)\n", name), true},
		{"a roster", fmt.Sprintf("package p\nvar roster = []string{%q}\n", name), true},
		{"a concrete engine import", "package p\nimport _ \"" + modulePath + "/internal/engines/x\"\n", true},
		{"a struct tag", fmt.Sprintf("package p\ntype T struct{ F int `json:%q` }\n", name), false},
		{"a longer string", fmt.Sprintf("package p\nvar x = %q\n", name+" is mentioned"), false},
		{"the registry root import", "package p\nimport _ \"" + modulePath + "/internal/engines\"\n", false},
	}
	for _, c := range cases {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "p.go", c.src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("%s: %v", c.label, err)
		}
		files := []identityFile{{rel: "p.go", dir: "p", file: f}}
		got := identityViolations(fset, files[0], packageConsts(files)["p"], identities)
		if (len(got) > 0) != c.want {
			t.Errorf("%s: violations %v, want reported=%v", c.label, got, c.want)
		}
	}
}
