package testsupport

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestIsolate_ClearsHostEnvAndRootsHome(t *testing.T) {
	// Simulate a dirty ambient session: a value set before Isolate must not
	// survive into the test.
	t.Setenv("CTXLOOM_PROJECT_ID", "poison")

	home := Isolate(t)

	for _, k := range EnvKeys {
		if got := os.Getenv(k); got != "" {
			t.Errorf("after Isolate, %s = %q, want cleared", k, got)
		}
	}
	got, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	if got != home {
		t.Errorf("UserHomeDir = %q, want the isolated temp home %q", got, home)
	}
}

func TestProjectDir_ChangesAndRestoresCwd(t *testing.T) {
	before := evalCwd(t)

	t.Run("inner", func(t *testing.T) {
		dir := ProjectDir(t)
		if got := evalCwd(t); got != evalPath(t, dir) {
			t.Errorf("cwd = %q, want project dir %q", got, dir)
		}
	})

	// The inner subtest's cleanup runs when t.Run returns; cwd is restored.
	if after := evalCwd(t); after != before {
		t.Errorf("cwd not restored: before=%q after=%q", before, after)
	}
}

func TestChangeDir_ChangesAndRestoresCwd(t *testing.T) {
	before := evalCwd(t)
	target := t.TempDir() // a directory ChangeDir did not mint itself

	t.Run("inner", func(t *testing.T) {
		ChangeDir(t, target)
		if got := evalCwd(t); got != evalPath(t, target) {
			t.Errorf("cwd = %q, want target dir %q", got, target)
		}
	})

	if after := evalCwd(t); after != before {
		t.Errorf("cwd not restored: before=%q after=%q", before, after)
	}
}

// TestEnvKeysCoversProductionReads fails if production code reads a CTXLOOM_*
// environment variable that EnvKeys does not list — which would mean Isolate
// silently fails to clear it and tests could inherit it from the host session.
func TestEnvKeysCoversProductionReads(t *testing.T) {
	root := moduleRoot(t)
	known := make(map[string]bool, len(EnvKeys))
	for _, k := range EnvKeys {
		known[k] = true
	}

	uncovered := findUncoveredEnvReads(t, []string{filepath.Join(root, "internal"), filepath.Join(root, "cmd")}, known)

	for key, files := range uncovered {
		t.Errorf("production reads %s but it is missing from testsupport.EnvKeys, so Isolate won't clear it (seen in %v)", key, files)
	}
}

// TestFindUncoveredEnvReads_CatchesConstantIdentifierReads pins U142-F01: a
// CTXLOOM_* variable read only through a named constant
// (os.Getenv(pkg.EnvFoo), the exact shape of coord.EnvMCPSocket and friends)
// must be just as visible to the sweep as a literal os.Getenv("CTXLOOM_FOO")
// call. Before this widened, the sweep's regex only matched string literals,
// so a *new* constant-read variable could go live in production with no
// signal anywhere — the incident CTXLOOM_MCP_SOCKET's own comment records.
func TestFindUncoveredEnvReads_CatchesConstantIdentifierReads(t *testing.T) {
	dir := t.TempDir()
	src := `package fixture

import "os"

const EnvFixtureThing = "CTXLOOM_FIXTURE_THING"

func read() string {
	return os.Getenv(EnvFixtureThing)
}
`
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	uncovered := findUncoveredEnvReads(t, []string{dir}, map[string]bool{})

	if _, ok := uncovered["CTXLOOM_FIXTURE_THING"]; !ok {
		t.Fatalf("expected CTXLOOM_FIXTURE_THING (read only via a named constant) to be reported uncovered; got %v", uncovered)
	}
}

// TestFindUncoveredEnvReads_StillCatchesLiterals is the literal-string
// regression the pre-widening sweep already covered; the widened version
// must not lose it.
func TestFindUncoveredEnvReads_StillCatchesLiterals(t *testing.T) {
	dir := t.TempDir()
	src := `package fixture

import "os"

func read() string {
	return os.Getenv("CTXLOOM_LITERAL_THING")
}
`
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	uncovered := findUncoveredEnvReads(t, []string{dir}, map[string]bool{})

	if _, ok := uncovered["CTXLOOM_LITERAL_THING"]; !ok {
		t.Fatalf("expected CTXLOOM_LITERAL_THING (read via a string literal) to be reported uncovered; got %v", uncovered)
	}
}

// findUncoveredEnvReads walks dirs recursively (skipping _test.go files) and
// reports every CTXLOOM_* variable read via os.Getenv/os.LookupEnv that is
// absent from known, whether the read names the variable as a string literal
// or via a package-level constant declared anywhere among dirs.
func findUncoveredEnvReads(t *testing.T, dirs []string, known map[string]bool) map[string][]string {
	t.Helper()
	literalRe := regexp.MustCompile(`os\.(?:Getenv|LookupEnv)\("(CTXLOOM_[A-Z0-9_]+)"\)`)
	identRe := regexp.MustCompile(`os\.(?:Getenv|LookupEnv)\(([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?)\)`)
	constRe := regexp.MustCompile(`(?m)^\s*(?:const\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*(?:string\s*)?=\s*"(CTXLOOM_[A-Z0-9_]+)"`)

	// constValues maps a bare identifier (the last component of a possibly
	// package-qualified name) to the CTXLOOM_* string it was declared equal
	// to, gathered from every file under dirs before reads are resolved —
	// declaration and use can be in different packages entirely (e.g.
	// coord.EnvMCPSocket declared in internal/agentcoord/coord, read from
	// internal/mcp).
	constValues := map[string]string{}
	type hit struct {
		literal string // resolved CTXLOOM_* value for a literal read, else ""
		ident   string // bare identifier for an identifier read, else ""
		file    string
	}
	var hits []hit

	for _, dir := range dirs {
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			text := string(data)
			for _, m := range constRe.FindAllStringSubmatch(text, -1) {
				constValues[m[1]] = m[2]
			}
			for _, m := range literalRe.FindAllStringSubmatch(text, -1) {
				hits = append(hits, hit{literal: m[1], file: path})
			}
			for _, m := range identRe.FindAllStringSubmatch(text, -1) {
				name := m[1]
				if i := strings.LastIndex(name, "."); i >= 0 {
					name = name[i+1:]
				}
				hits = append(hits, hit{ident: name, file: path})
			}
			return nil
		})
	}

	uncovered := make(map[string][]string)
	for _, h := range hits {
		key := h.literal
		if key == "" {
			v, ok := constValues[h.ident]
			if !ok {
				// Identifier does not resolve to a known CTXLOOM_* constant
				// among the scanned dirs (could be a non-CTXLOOM var like
				// os.Getenv(homeVar), or a constant declared outside the
				// scanned tree) — nothing to flag.
				continue
			}
			key = v
		}
		if !known[key] {
			uncovered[key] = append(uncovered[key], h.file)
		}
	}
	return uncovered
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found walking up from cwd")
		}
		dir = parent
	}
}

func evalCwd(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return evalPath(t, cwd)
}

// evalPath resolves symlinks so comparisons hold on platforms where TempDir
// lives under a symlinked root (e.g. macOS /tmp -> /private/tmp).
func evalPath(t *testing.T, p string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return resolved
}
