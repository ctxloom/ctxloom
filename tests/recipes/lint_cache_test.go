package recipes

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runLint runs justfile.container's `lint` from checkout against a fake `go`
// and a fake golangci-lint that prints the GOLANGCI_LINT_CACHE it was handed,
// and returns that value. cache, when non-empty, is set as the caller's
// GOLANGCI_LINT_CACHE; otherwise the variable is stripped.
func runLint(t *testing.T, checkout, cache string) string {
	t.Helper()
	root := repoRoot(t)
	// Read the justfile here so go test's cache keys on it (see runPreflight).
	if _, err := os.ReadFile(filepath.Join(root, "justfile.container")); err != nil {
		t.Fatal(err)
	}
	just, err := exec.LookPath("just")
	if err != nil {
		t.Fatalf("just is not on PATH (%v); this test drives the real lint recipe", err)
	}
	bin := t.TempDir()
	for name, body := range map[string]string{
		"go":            "#!/bin/sh\nexit 0\n",
		"golangci-lint": "#!/bin/sh\necho \"CACHE=[$GOLANGCI_LINT_CACHE]\"\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(just, "--justfile", filepath.Join(root, "justfile.container"),
		"--working-directory", checkout, "lint")
	var env []string
	for _, kv := range scrubbedEnv() {
		if strings.HasPrefix(kv, "GOLANGCI_LINT_CACHE=") || strings.HasPrefix(kv, "PATH=") || strings.HasPrefix(kv, "PWD=") {
			continue
		}
		env = append(env, kv)
	}
	// PWD is the checkout as the developer's shell names it, which for a
	// symlinked checkout is the link: the recipe's shell trusts an inherited
	// PWD that resolves to its working directory.
	env = append(env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "PWD="+checkout)
	if cache != "" {
		env = append(env, "GOLANGCI_LINT_CACHE="+cache)
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("just lint: %v\n%s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(line, "CACHE=["); ok {
			return strings.TrimSuffix(v, "]")
		}
	}
	t.Fatalf("golangci-lint was never run:\n%s", out)
	return ""
}

// wantCheckoutCache is the cache `lint` must pick for a checkout whose
// physical path is root: in-tree, keyed by the first 16 hex chars of the
// path's sha256.
func wantCheckoutCache(root string) string {
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(root, ".cache", "golangci-lint", hex.EncodeToString(sum[:])[:16])
}

func TestLint_CachePerCheckout(t *testing.T) {
	t.Parallel()

	t.Run("an unset cache is kept inside the checkout, keyed by its path", func(t *testing.T) {
		t.Parallel()
		checkout := t.TempDir()
		phys, err := filepath.EvalSymlinks(checkout)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := runLint(t, checkout, ""), wantCheckoutCache(phys); got != want {
			t.Errorf("GOLANGCI_LINT_CACHE = %q, want %q", got, want)
		}
	})

	t.Run("two checkouts never share a cache", func(t *testing.T) {
		t.Parallel()
		a, b := runLint(t, t.TempDir(), ""), runLint(t, t.TempDir(), "")
		if a == b {
			t.Errorf("both checkouts linted against %q", a)
		}
	})

	t.Run("a symlinked checkout is keyed by its physical path", func(t *testing.T) {
		t.Parallel()
		real := t.TempDir()
		phys, err := filepath.EvalSymlinks(real)
		if err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		if got, want := runLint(t, link, ""), wantCheckoutCache(phys); got != want {
			t.Errorf("GOLANGCI_LINT_CACHE = %q, want %q", got, want)
		}
	})

	t.Run("an explicit GOLANGCI_LINT_CACHE wins", func(t *testing.T) {
		t.Parallel()
		explicit := filepath.Join(t.TempDir(), "mine")
		if got := runLint(t, t.TempDir(), explicit); got != explicit {
			t.Errorf("GOLANGCI_LINT_CACHE = %q, want the caller's %q", got, explicit)
		}
	})
}
