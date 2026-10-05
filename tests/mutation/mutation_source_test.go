// No build tag, deliberately: mutation_source.sh decides which bytes the
// container mutation run is handed, and a guard that only runs during the
// hour-long run it guards is not a guard.
package mutation

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := taskstest.GitCmd(dir, []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t"}, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The container mutation run is handed a COPY of the module, streamed in as a
// tar, so that nothing the run writes — a mutant included, however the run
// ends — can reach the working tree.
// The copy is the module's source — tracked and untracked files, plus ignored
// Go files (generated code the build needs) — and nothing else the checkout
// carries: build output and caches, Go files inside them included, are what
// makes a checkout gigabytes.
func TestMutationSource_StreamsTheModuleSourceAndNothingElse(t *testing.T) {
	script := repoInput(t, "tests/mutation/mutation_source.sh")[0]
	repo := t.TempDir()
	mustGit(t, repo, "init", "-q", "-b", "main")
	writeFile(t, repo, ".gitignore", "*.pb.go\nbin/\n.cache/\n")
	writeFile(t, repo, "go.mod", "module x\n")
	writeFile(t, repo, "a.go", "package x\n")
	writeFile(t, repo, "gone.go", "package x\n")
	mustGit(t, repo, "add", ".")
	mustGit(t, repo, "commit", "-q", "-m", "init")
	if err := os.Remove(filepath.Join(repo, "gone.go")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "new.go", "package x\n")
	writeFile(t, repo, "pb/x.pb.go", "package pb\n")
	writeFile(t, repo, "bin/ctxloom", "binary")
	writeFile(t, repo, ".cache/blob", "cache")
	writeFile(t, repo, ".cache/mod/dep/dep.go", "package dep\n")

	cmd := exec.Command("bash", script)
	cmd.Dir = repo
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("mutation_source.sh: %v\n%s", err, stderr.String())
	}

	var got []string
	tr := tar.NewReader(bytes.NewReader(out))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		if h.Typeflag == tar.TypeReg {
			got = append(got, strings.TrimPrefix(h.Name, "./"))
		}
	}
	slices.Sort(got)
	want := []string{".gitignore", "a.go", "go.mod", "new.go", "pb/x.pb.go"}
	if !slices.Equal(got, want) {
		t.Fatalf("archive files\n got %q\nwant %q", got, want)
	}
}

// The recipe must not bind the checkout into the container at all: the copy
// arrives on stdin. A bind of {{TOP}} — read-write or otherwise — hands the
// working tree back to the run.
func TestMutationContainerRecipe_DoesNotMountTheCheckout(t *testing.T) {
	body := recipeBody(t, repoInput(t, "justfile")[0], "test-mutation-container")
	if strings.Contains(body, "{{TOP}}:") || strings.Contains(body, "src={{TOP}}") {
		t.Fatalf("test-mutation-container binds the checkout into the container:\n%s", body)
	}
	if !strings.Contains(body, "mutation_source.sh") {
		t.Fatalf("test-mutation-container does not stream its source through mutation_source.sh:\n%s", body)
	}
}

// recipeBody returns the lines of a justfile recipe, from its header to the
// next unindented line.
func recipeBody(t *testing.T, justfile, name string) string {
	t.Helper()
	data, err := os.ReadFile(justfile)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	in := false
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, name+":"):
			in = true
		case in && line != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t"):
			return b.String()
		}
		if in {
			b.WriteString(line + "\n")
		}
	}
	if !in {
		t.Fatalf("recipe %s not found in %s", name, justfile)
	}
	return b.String()
}
