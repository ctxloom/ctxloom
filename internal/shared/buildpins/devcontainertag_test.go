package buildpins

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const devcontainerTagScript = "../../../scripts/devcontainer-tag.sh"

// devcontainerTree is a throwaway build context: the two files every tag keys
// on, plus whatever the case adds.
type devcontainerTree struct {
	t    *testing.T
	root string
}

func newDevcontainerTree(t *testing.T, dockerfile string) devcontainerTree {
	t.Helper()
	tr := devcontainerTree{t: t, root: t.TempDir()}
	tr.write(".devcontainer/tool-versions.env", "GO_VERSION=1.26.0\n")
	tr.write(".devcontainer/Dockerfile", dockerfile)
	return tr
}

func (tr devcontainerTree) write(rel, body string) {
	tr.t.Helper()
	p := filepath.Join(tr.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		tr.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		tr.t.Fatal(err)
	}
}

func (tr devcontainerTree) tag() (string, error) {
	out, err := exec.Command("bash", devcontainerTagScript, tr.root).Output()
	return strings.TrimSpace(string(out)), err
}

func (tr devcontainerTree) mustTag() string {
	tr.t.Helper()
	got, err := tr.tag()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		tr.t.Fatalf("devcontainer-tag.sh failed: %v\n%s", err, stderr)
	}
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(got) {
		tr.t.Fatalf("tag %q is not 12 hex digits", got)
	}
	return got
}

const baseDockerfile = "FROM debian:bookworm\n" +
	"COPY --from=ghcr.io/astral-sh/uv:0.9.22 /uv /usr/local/bin/uv\n" +
	"COPY build/a.txt \\\n" +
	"     build/sub /opt/\n" +
	"ADD [\"conf/c.json\", \"/etc/c.json\"]\n"

func baseTree(t *testing.T) devcontainerTree {
	tr := newDevcontainerTree(t, baseDockerfile)
	tr.write("build/a.txt", "a")
	tr.write("build/sub/b.txt", "b")
	tr.write("conf/c.json", "{}")
	return tr
}

// Every input that decides the image's bytes must move its tag. The tag is
// shared machine-wide, so an input left out of the key lets one tree rebuild
// the image under a name every other tree is gating against.
func TestDevcontainerTag_ChangesWithEveryImageInput(t *testing.T) {
	base := baseTree(t).mustTag()

	cases := map[string]func(devcontainerTree){
		"pins":                   func(tr devcontainerTree) { tr.write(".devcontainer/tool-versions.env", "GO_VERSION=1.26.1\n") },
		"one Dockerfile byte":    func(tr devcontainerTree) { tr.write(".devcontainer/Dockerfile", baseDockerfile+" ") },
		"a COPY source file":     func(tr devcontainerTree) { tr.write("build/a.txt", "A") },
		"a file in a COPY dir":   func(tr devcontainerTree) { tr.write("build/sub/b.txt", "B") },
		"a new file in COPY dir": func(tr devcontainerTree) { tr.write("build/sub/new.txt", "n") },
		"an exec-form ADD file":  func(tr devcontainerTree) { tr.write("conf/c.json", "[]") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			tr := baseTree(t)
			mutate(tr)
			if got := tr.mustTag(); got == base {
				t.Errorf("changing %s left the tag at %s — that input is not keyed", name, got)
			}
		})
	}

	t.Run("unrelated file", func(t *testing.T) {
		tr := baseTree(t)
		tr.write("README", "not copied")
		if got := tr.mustTag(); got != base {
			t.Errorf("a file the Dockerfile never copies moved the tag %s -> %s; identical images must share a tag", base, got)
		}
	})
}

// A key that silently omits an input is the bug the tag exists to prevent, so
// an input the script cannot resolve must fail, not be skipped.
func TestDevcontainerTag_FailsOnAnInputItCannotKey(t *testing.T) {
	for name, dockerfile := range map[string]string{
		"missing COPY source": "FROM x\nCOPY nope.txt /\n",
		"heredoc COPY":        "FROM x\nCOPY <<EOF /f\nhi\nEOF\n",
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := newDevcontainerTree(t, dockerfile).tag(); err == nil {
				t.Errorf("want failure, got tag %q", got)
			}
		})
	}
}

// The real tree must key cleanly: every COPY/ADD in the real Dockerfile has to
// resolve, or every recipe in the root justfile fails to evaluate.
func TestDevcontainerTag_KeysTheRealTree(t *testing.T) {
	tr := devcontainerTree{t: t, root: "../../.."}
	tr.mustTag()
}

// One definition: the root justfile's devcontainer_tag is the script, and
// every reference to the image in the justfile names it through that variable
// — never a hand-spelled tag or a second hash expression.
func TestDevcontainerTag_IsTheJustfilesOnlyDefinition(t *testing.T) {
	content := readFile(t, justfilePath)

	def := regexp.MustCompile("(?m)^devcontainer_tag := `scripts/devcontainer-tag\\.sh`$")
	if !def.MatchString(content) {
		t.Errorf("%s: devcontainer_tag must be exactly `scripts/devcontainer-tag.sh`, the one definition of the image key", justfilePath)
	}

	refs := regexp.MustCompile(`\{\{devcontainer_image\}\}(:\{\{devcontainer_tag\}\})?`).FindAllStringSubmatch(content, -1)
	if len(refs) == 0 {
		t.Fatalf("%s: no {{devcontainer_image}} references — this gate would check nothing", justfilePath)
	}
	for _, m := range refs {
		if m[1] == "" {
			t.Errorf("%s: {{devcontainer_image}} used without :{{devcontainer_tag}} — that names an image the key does not govern", justfilePath)
		}
	}
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") && strings.Contains(line, "ctxloom-devcontainer:") {
			t.Errorf("%s: a hand-spelled ctxloom-devcontainer:<tag> bypasses devcontainer_tag:\n%s", justfilePath, line)
		}
	}
}
