package buildpins

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The pre-commit formatter step, scripts/fmt staged <kind>. Each test drives
// the real script over a throwaway repository: a fully staged file is fixed
// and re-staged, and a partially staged one is only checked, never rewritten,
// because rewriting it moves the lines its unstaged hunks apply to.

const fmtScript = "../../../scripts/fmt"

// Go source as gofmt leaves it, and the same with one unformatted line.
const (
	goFormatted   = "package x\n\nvar a = 1\n\nfunc g() {\n}\n"
	goUnformatted = "package x\n\nvar a = 1\nvar   b = 2\n\nfunc g() {\n}\n"
	goStagedWIP   = "package x\n\nvar a = 1\nvar   b = 2\n\nfunc g() {\n\t// WIP\n}\n"
	goFormattedB  = "package x\n\nvar a = 1\nvar b = 2\n\nfunc g() {\n}\n"
)

// hookRepo is a throwaway repository for driving a pre-commit script.
type hookRepo struct {
	t   *testing.T
	dir string
}

func newHookRepo(t *testing.T) *hookRepo {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	r := &hookRepo{t: t, dir: dir}
	r.git("init", "-q")
	r.git("config", "user.email", "hook@example.invalid")
	r.git("config", "user.name", "hook")
	r.git("config", "commit.gpgsign", "false")
	return r
}

func (r *hookRepo) git(args ...string) string {
	r.t.Helper()
	cmd := taskstest.GitCmd(r.dir, nil, args...)
	cmd.Env = taskstest.HermeticGitEnv(scrubbedEnv())
	out, err := cmd.CombinedOutput()
	require.NoError(r.t, err, "git %v: %s", args, out)
	return string(out)
}

func (r *hookRepo) write(rel, content string) {
	r.t.Helper()
	path := filepath.Join(r.dir, rel)
	require.NoError(r.t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(r.t, os.WriteFile(path, []byte(content), 0o644))
}

func (r *hookRepo) read(rel string) string {
	r.t.Helper()
	raw, err := os.ReadFile(filepath.Join(r.dir, rel))
	require.NoError(r.t, err)
	return string(raw)
}

// commit writes the files and commits them as the repository's HEAD.
func (r *hookRepo) commit(files map[string]string) {
	r.t.Helper()
	for rel, content := range files {
		r.write(rel, content)
		r.git("add", "--", rel)
	}
	r.git("commit", "-q", "--no-verify", "-m", "fixture")
}

// stage writes a file and adds it to the index.
func (r *hookRepo) stage(rel, content string) {
	r.t.Helper()
	r.write(rel, content)
	r.git("add", "--", rel)
}

// staged is the file's content in the index.
func (r *hookRepo) staged(rel string) string {
	r.t.Helper()
	return r.git("show", ":"+rel)
}

// rawIndex is the index file's bytes: the strongest form of "the index was
// not touched".
func (r *hookRepo) rawIndex() []byte {
	r.t.Helper()
	raw, err := os.ReadFile(filepath.Join(r.dir, ".git", "index"))
	require.NoError(r.t, err)
	return raw
}

// run executes a script (with its arguments) from the repository, returning
// its exit code and combined output.
func (r *hookRepo) run(env []string, script string, args ...string) (int, string) {
	r.t.Helper()
	abs := scriptPath(r.t, script)
	cmd := exec.Command("bash", append([]string{abs}, args...)...)
	cmd.Dir = r.dir
	cmd.Env = append(append(taskstest.HermeticGitEnv(scrubbedEnv()), "GIT_PAGER=cat"), env...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	var exitErr *exec.ExitError
	require.ErrorAs(r.t, err, &exitErr, "%s did not run: %v\n%s", script, err, out)
	return exitErr.ExitCode(), string(out)
}

func TestPreCommitFormat_FullyStagedFileIsFixedAndRestaged(t *testing.T) {
	r := newHookRepo(t)
	r.commit(map[string]string{"x.go": goFormatted})
	r.stage("x.go", goUnformatted)

	code, out := r.run(nil, fmtScript, "staged", "go")

	require.Equal(t, 0, code, out)
	assert.Equal(t, goFormattedB, r.read("x.go"), "the working copy is formatted")
	assert.Equal(t, goFormattedB, r.staged("x.go"), "the formatted bytes are what is staged")
	assert.Contains(t, out, "formatted and re-staged x.go")
}

func TestPreCommitFormat_PartiallyStagedUnformattedFileIsRefusedUntouched(t *testing.T) {
	r := newHookRepo(t)
	r.commit(map[string]string{"x.go": goFormatted})
	r.stage("x.go", goUnformatted)
	r.write("x.go", goStagedWIP)
	index := r.rawIndex()

	code, out := r.run(nil, fmtScript, "staged", "go")

	assert.Equal(t, 1, code, out)
	assert.Contains(t, out, "x.go")
	assert.Contains(t, out, "Run 'just fmt'")
	assert.Equal(t, goStagedWIP, r.read("x.go"), "the unstaged work is where the user left it")
	assert.Equal(t, goUnformatted, r.staged("x.go"), "nothing new was staged")
	assert.Equal(t, index, r.rawIndex(), "the index was not written")
}

func TestPreCommitFormat_PartiallyStagedFormattedFilePasses(t *testing.T) {
	r := newHookRepo(t)
	r.commit(map[string]string{"x.go": goFormatted})
	r.stage("x.go", goFormattedB)
	wip := goFormattedB + "\nvar   unformattedWIP = 3\n"
	r.write("x.go", wip)
	index := r.rawIndex()

	code, out := r.run(nil, fmtScript, "staged", "go")

	assert.Equal(t, 0, code, out)
	assert.Equal(t, wip, r.read("x.go"), "unstaged work is never formatted")
	assert.Equal(t, index, r.rawIndex(), "the index was not written")
}

// Under lefthook the working tree is no evidence: lefthook checks a partially
// staged file out from the index before any step runs, so the file looks
// fully staged, and re-applies its unstaged hunks afterwards by line number.
// The staged content here has blank lines gofmt collapses, so formatting it
// would shift the line the unstaged hunk applies to. Run through the real
// lefthook, the file must be refused and the unstaged line must still be
// inside g, where it was written.
func TestPreCommitFormat_UnderLefthookPartiallyStagedFileKeepsUnstagedWork(t *testing.T) {
	const (
		stagedShifting = "package x\n\n\n\nvar a = 1\n\nfunc g() {\n}\n"
		withWIP        = "package x\n\n\n\nvar a = 1\n\nfunc g() {\n\t// WIP\n}\n"
	)
	lefthook, err := exec.LookPath("lefthook")
	require.NoError(t, err, "lefthook is not on PATH; the devcontainer image installs it, and on a host `go install "+
		"github.com/evilmartians/lefthook@v$LEFTHOOK_VERSION` does (.devcontainer/tool-versions.env pins it)")
	script := scriptPath(t, fmtScript)

	r := newHookRepo(t)
	r.commit(map[string]string{
		"lefthook.yml": "pre-commit:\n  commands:\n    fmt-go:\n      run: bash " + script + " staged go\n",
		"x.go":         goFormatted,
	})
	r.stage("x.go", stagedShifting)
	r.write("x.go", withWIP)

	cmd := exec.Command(lefthook, "run", "pre-commit", "--no-tty")
	cmd.Dir = r.dir
	cmd.Env = append(append(taskstest.HermeticGitEnv(lefthookFreeEnv()), "GIT_PAGER=cat"), "NO_COLOR=1")
	out, err := cmd.CombinedOutput()

	require.Error(t, err, "the partially staged file must be refused, not formatted:\n%s", out)
	assert.Contains(t, string(out), "Run 'just fmt'")
	assert.Equal(t, withWIP, r.read("x.go"), "the unstaged line is back where it was written:\n%s", out)
	assert.Equal(t, stagedShifting, r.staged("x.go"), "nothing new was staged")
	assert.Empty(t, strings.TrimSpace(r.git("stash", "list")), "lefthook dropped its own backup")
}

// A script with no extension is a shell script by its shebang, so the shell
// step must find it.
func TestPreCommitFormat_ShellScriptIsFoundByItsShebang(t *testing.T) {
	r := newHookRepo(t)
	unformatted := "#!/usr/bin/env bash\nif true; then\n\techo hi\nfi\n"
	r.commit(map[string]string{"tool": "#!/usr/bin/env bash\n"})
	r.stage("tool", unformatted)

	code, out := r.run(nil, fmtScript, "staged", "shell")

	require.Equal(t, 0, code, out)
	assert.Equal(t, "#!/usr/bin/env bash\nif true; then\n    echo hi\nfi\n", r.staged("tool"))
}

// scriptPath resolves a script the test executes, and READS it: go test's
// result cache keys on the files the test process itself opens, and a script
// run as a subprocess is not one of them, so without the read an edited script
// would be judged by a cached pass.
func scriptPath(t *testing.T, rel string) string {
	t.Helper()
	abs, err := filepath.Abs(rel)
	require.NoError(t, err)
	readFile(t, abs)
	return abs
}

// lefthookFreeEnv is the scrubbed environment without lefthook's own
// switches, which would otherwise let the developer's LEFTHOOK=0 turn the run
// into a no-op that passes.
func lefthookFreeEnv() []string {
	var env []string
	for _, kv := range scrubbedEnv() {
		key, _, _ := strings.Cut(kv, "=")
		if key == "LEFTHOOK" || strings.HasPrefix(key, "LEFTHOOK_") {
			continue
		}
		env = append(env, kv)
	}
	return env
}
