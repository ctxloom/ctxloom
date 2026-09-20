package buildpins

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The pin-direction gate, scripts/lint-pins. The other tests in this package
// prove the pin FILES AGREE with each other; this one proves a pin cannot go
// BACKWARDS between HEAD and the index without a named override. It drives
// the real script over a throwaway git repository carrying the same pin
// files the real tree does, so the test exercises the exact parsers and the
// exact `git show HEAD:` / `git show :` seam the pre-commit hook runs.

const lintPinsScript = "../../../scripts/lint-pins"

// downgradeOverrideEnv is the one environment variable the gate reads. The
// name is asserted here so a rename of the script's variable cannot leave a
// reviewer's `CTXLOOM_ALLOW_PIN_DOWNGRADE=... git commit` silently ignored.
const downgradeOverrideEnv = "CTXLOOM_ALLOW_PIN_DOWNGRADE"

// pinRepo is a git repository whose HEAD carries one version of every pin
// file the gate reads. stage rewrites a file and adds it to the index.
type pinRepo struct {
	t   *testing.T
	dir string
}

func newPinRepo(t *testing.T) *pinRepo {
	t.Helper()
	dir := t.TempDir()
	r := &pinRepo{t: t, dir: dir}
	r.git("init", "-q")
	r.git("config", "user.email", "pins@example.invalid")
	r.git("config", "user.name", "pins")
	r.git("config", "commit.gpgsign", "false")
	r.write(".devcontainer/tool-versions.env", "# build pins\nGO_VERSION=1.26.8\nBUF_VERSION=1.47.2\n")
	r.write(".github/engine-versions.env", "CLAUDE_CODE_CLI_VERSION=2.1.0\n")
	r.write("go.mod", "module example.invalid/pins\n\ngo 1.26.0\n\ntoolchain go1.26.8\n")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "pins at HEAD")
	return r
}

func (r *pinRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = scrubbedEnv()
	out, err := cmd.CombinedOutput()
	require.NoError(r.t, err, "git %v: %s", args, out)
	return string(out)
}

func (r *pinRepo) write(rel, content string) {
	r.t.Helper()
	path := filepath.Join(r.dir, rel)
	require.NoError(r.t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(r.t, os.WriteFile(path, []byte(content), 0o644))
}

func (r *pinRepo) stage(rel, content string) {
	r.t.Helper()
	r.write(rel, content)
	r.git("add", rel)
}

// run executes the gate in the repo with the given override value ("" means
// unset) and returns its exit code and combined output.
func (r *pinRepo) run(override string) (int, string) {
	r.t.Helper()
	script, err := filepath.Abs(lintPinsScript)
	require.NoError(r.t, err)
	cmd := exec.Command(script)
	cmd.Dir = r.dir
	cmd.Env = scrubbedEnv()
	if override != "" {
		cmd.Env = append(cmd.Env, downgradeOverrideEnv+"="+override)
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	var exitErr *exec.ExitError
	require.ErrorAs(r.t, err, &exitErr, "lint-pins did not run: %v\n%s", err, out)
	return exitErr.ExitCode(), string(out)
}

// scrubbedEnv drops the variables git exports into hook processes and the
// override itself, so the gate under test sees only what the test hands it.
func scrubbedEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		switch key {
		case "GIT_INDEX_FILE", "GIT_DIR", "GIT_WORK_TREE", downgradeOverrideEnv:
			continue
		}
		env = append(env, kv)
	}
	return env
}

func TestLintPins_NothingStaged_Passes(t *testing.T) {
	r := newPinRepo(t)
	code, out := r.run("")
	assert.Equal(t, 0, code, out)
}

func TestLintPins_Upgrade_Passes(t *testing.T) {
	r := newPinRepo(t)
	r.stage(".devcontainer/tool-versions.env", "# build pins\nGO_VERSION=1.26.9\nBUF_VERSION=1.47.2\n")
	r.stage("go.mod", "module example.invalid/pins\n\ngo 1.26.0\n\ntoolchain go1.26.9\n")
	code, out := r.run("")
	assert.Equal(t, 0, code, out)
}

func TestLintPins_SameValueRewritten_Passes(t *testing.T) {
	r := newPinRepo(t)
	// A comment-only edit: every pin keeps its value.
	r.stage(".devcontainer/tool-versions.env", "# build pins, reworded\nBUF_VERSION=1.47.2\nGO_VERSION=1.26.8\n")
	code, out := r.run("")
	assert.Equal(t, 0, code, out)
}

func TestLintPins_Downgrade_FailsNamingPinAndValues(t *testing.T) {
	r := newPinRepo(t)
	r.stage(".devcontainer/tool-versions.env", "# build pins\nGO_VERSION=1.26.7\nBUF_VERSION=1.47.2\n")
	code, out := r.run("")
	assert.Equal(t, 1, code, out)
	assert.Contains(t, out, "GO_VERSION")
	assert.Contains(t, out, "1.26.8")
	assert.Contains(t, out, "1.26.7")
	assert.Contains(t, out, downgradeOverrideEnv, "the failure must tell the committer how to override deliberately")
}

// Component-wise, not lexical: 1.26.10 is newer than 1.26.9.
func TestLintPins_SemverCompare_IsNumericPerComponent(t *testing.T) {
	r := newPinRepo(t)
	r.stage(".github/engine-versions.env", "CLAUDE_CODE_CLI_VERSION=2.1.10\n")
	code, out := r.run("")
	assert.Equal(t, 0, code, out)

	r.stage(".github/engine-versions.env", "CLAUDE_CODE_CLI_VERSION=2.0.99\n")
	code, out = r.run("")
	assert.Equal(t, 1, code, out)
	assert.Contains(t, out, "CLAUDE_CODE_CLI_VERSION")
}

func TestLintPins_GoModDirectiveDowngrade_Fails(t *testing.T) {
	r := newPinRepo(t)
	r.stage("go.mod", "module example.invalid/pins\n\ngo 1.25.0\n\ntoolchain go1.26.8\n")
	code, out := r.run("")
	assert.Equal(t, 1, code, out)
	assert.Contains(t, out, "go.mod:go")

	r.stage("go.mod", "module example.invalid/pins\n\ngo 1.26.0\n\ntoolchain go1.26.7\n")
	code, out = r.run("")
	assert.Equal(t, 1, code, out)
	assert.Contains(t, out, "go.mod:toolchain")
}

// A pin that is not a dotted-numeric version has no order the gate can
// compute, so any change to it is refused and the output names the parser
// that declined to rank it. The override is the way through.
func TestLintPins_NonSemverChange_FailsNamingParser(t *testing.T) {
	r := newPinRepo(t)
	r.stage(".devcontainer/tool-versions.env", "# build pins\nGO_VERSION=1.26.8\nBUF_VERSION=latest\n")
	code, out := r.run("")
	assert.Equal(t, 1, code, out)
	assert.Contains(t, out, "BUF_VERSION")
	assert.Contains(t, out, "not a dotted-numeric version")

	code, out = r.run("BUF_VERSION")
	assert.Equal(t, 0, code, out)
}

func TestLintPins_Override_AllowsNamedPinAndEchoesIt(t *testing.T) {
	r := newPinRepo(t)
	r.stage(".devcontainer/tool-versions.env", "# build pins\nGO_VERSION=1.26.7\nBUF_VERSION=1.47.2\n")
	code, out := r.run("GO_VERSION")
	assert.Equal(t, 0, code, out)
	assert.Contains(t, out, downgradeOverrideEnv+"=GO_VERSION", "the reviewer must see the override in the hook's output")
	assert.Contains(t, out, "1.26.8")
	assert.Contains(t, out, "1.26.7")
}

func TestLintPins_Override_NamesOnlyItsPin(t *testing.T) {
	r := newPinRepo(t)
	r.stage(".devcontainer/tool-versions.env", "# build pins\nGO_VERSION=1.26.7\nBUF_VERSION=1.47.1\n")
	code, out := r.run("GO_VERSION")
	assert.Equal(t, 1, code, out)
	assert.Contains(t, out, "BUF_VERSION")

	code, out = r.run("GO_VERSION,BUF_VERSION")
	assert.Equal(t, 0, code, out)
}

// A pin file that is new in the index, or absent from it, has nothing to
// compare against: creation and deletion are not downgrades.
func TestLintPins_AddedOrRemovedPinFile_Passes(t *testing.T) {
	r := newPinRepo(t)
	r.git("rm", "-q", ".github/engine-versions.env")
	code, out := r.run("")
	assert.Equal(t, 0, code, out)

	r = newPinRepo(t)
	r.git("rm", "-q", "--cached", ".devcontainer/tool-versions.env")
	r.git("commit", "-q", "-m", "drop the build pins")
	r.stage(".devcontainer/tool-versions.env", "GO_VERSION=1.0.0\n")
	code, out = r.run("")
	assert.Equal(t, 0, code, out)
}

// --list prints every pin the gate reads from HEAD, one `name value` per
// line. Run against the REAL repository, it is the checked statement of what
// the gate covers: a pin source the script stops reading disappears here.
func TestLintPins_List_CoversEveryPinSourceOfTheRealTree(t *testing.T) {
	script, err := filepath.Abs(lintPinsScript)
	require.NoError(t, err)
	cmd := exec.Command(script, "--list")
	cmd.Dir = "../../.."
	cmd.Env = scrubbedEnv()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)

	listed := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name, value, ok := strings.Cut(line, " ")
		require.True(t, ok, "--list line %q is not `name value`", line)
		listed[name] = value
	}

	tools := parseToolVersionsEnv(t, toolVersionsPath)
	for k, v := range tools {
		assert.Equal(t, v, listed[k], "tool-versions.env pin %s is not covered by the gate", k)
	}
	engines := parseToolVersionsEnv(t, "../../../.github/engine-versions.env")
	for k, v := range engines {
		assert.Equal(t, v, listed[k], "engine-versions.env pin %s is not covered by the gate", k)
	}
	assert.NotEmpty(t, listed["go.mod:go"], "go.mod's go directive is not covered by the gate")
	assert.Len(t, listed, len(tools)+len(engines)+goModDirectiveCount(t), "the gate lists a pin no source declares")
}

// goModDirectiveCount is how many of go.mod's version directives (`go`,
// `toolchain`) the real go.mod carries, so the census above stays honest
// whether or not a toolchain line is present.
func goModDirectiveCount(t *testing.T) int {
	t.Helper()
	raw := readFile(t, "../../../go.mod")
	n := 0
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "go ") || strings.HasPrefix(line, "toolchain ") {
			n++
		}
	}
	return n
}
