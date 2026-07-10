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
	re := regexp.MustCompile(`os\.(?:Getenv|LookupEnv)\("(CTXLOOM_[A-Z0-9_]+)"\)`)
	known := make(map[string]bool, len(EnvKeys))
	for _, k := range EnvKeys {
		known[k] = true
	}

	uncovered := make(map[string][]string)
	for _, sub := range []string{"internal", "cmd"} {
		_ = filepath.WalkDir(filepath.Join(root, sub), func(path string, d fs.DirEntry, err error) error {
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
			for _, m := range re.FindAllStringSubmatch(string(data), -1) {
				if !known[m[1]] {
					rel, _ := filepath.Rel(root, path)
					uncovered[m[1]] = append(uncovered[m[1]], rel)
				}
			}
			return nil
		})
	}

	for key, files := range uncovered {
		t.Errorf("production reads %s but it is missing from testsupport.EnvKeys, so Isolate won't clear it (seen in %v)", key, files)
	}
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
