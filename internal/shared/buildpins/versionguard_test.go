package buildpins

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// The release pair in build/ci.justfile: ci.yml's Version Guard runs
// version-untagged-check, and auto-release.yml runs release-tag after every
// green push. Each case drives the real recipe against a throwaway repository,
// so what is pinned is the recipe's verdict rather than its text.

// releaseJustfile resolves the recipes. Both root justfiles import the same
// build/ci.justfile; the container one is used because the host one evaluates
// cwd-relative backticks (devcontainer_tag) at load, which fail in a
// throwaway repository before any recipe runs.
const releaseJustfile = "../../../justfile.container"

// noCommit is the `before` SHA GitHub sends for the first push of a branch.
const noCommit = "0000000000000000000000000000000000000000"

type releaseRepo struct {
	t    *testing.T
	root string
}

func newReleaseRepo(t *testing.T, version string) releaseRepo {
	t.Helper()
	r := releaseRepo{t: t, root: t.TempDir()}
	r.git("init", "-q", "-b", "line")
	r.git("config", "user.email", "test@example.invalid")
	r.git("config", "user.name", "test")
	r.git("config", "commit.gpgsign", "false")
	r.git("config", "tag.gpgsign", "false")
	r.commit("VERSION", version+"\n")
	return r
}

func (r releaseRepo) git(args ...string) string {
	r.t.Helper()
	return taskstest.Git(r.t, r.root, nil, args...)
}

// commit writes one file and commits it, returning the new HEAD.
func (r releaseRepo) commit(rel, body string) string {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.root, rel), []byte(body), 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.git("add", rel)
	r.git("commit", "-q", "-m", "change "+rel)
	return r.git("rev-parse", "HEAD")
}

func (r releaseRepo) just(recipe string, args ...string) (string, error) {
	r.t.Helper()
	argv := append([]string{"--justfile", mustAbs(r.t, releaseJustfile), "--working-directory", r.root, recipe}, args...)
	cmd := exec.Command("just", argv...)
	// The recipes run git, and release-tag can tag and push: an inherited
	// GIT_DIR would aim them at the real checkout instead of this fixture.
	cmd.Env = taskstest.HermeticGitEnv(os.Environ())
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (r releaseRepo) guard(base string) (string, error) {
	r.t.Helper()
	return r.just("version-untagged-check", base)
}

func mustPass(t *testing.T, what, out string, err error) {
	t.Helper()
	if err != nil {
		t.Errorf("%s: want pass, got %v\n%s", what, err, out)
	}
}

// mustRefuse requires the recipe's OWN refusal: any other failure (a recipe
// that does not parse, a missing tool) would otherwise pass as a refusal.
func mustRefuse(t *testing.T, what, out string, err error, reason string) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: want refusal, got pass\n%s", what, out)
		return
	}
	if !strings.Contains(out, reason) {
		t.Errorf("%s: want a refusal saying %q, got %v\n%s", what, reason, err, out)
	}
}

const (
	alreadyTagged = "is already tagged"
	taggedOffLine = "does not contain"
)

// An ordinary push after a release leaves VERSION alone, so its tag already
// exists; the guard must not hold that against the push.
func TestVersionGuard_PassesAPushThatLeavesVersionAlone(t *testing.T) {
	r := newReleaseRepo(t, "1.0.0")
	r.git("tag", "v1.0.0")
	before := r.git("rev-parse", "HEAD")
	r.commit("code.go", "package x\n")

	out, err := r.guard(before)
	mustPass(t, "push not touching VERSION", out, err)
}

func TestVersionGuard_JudgesAPushThatChangesVersion(t *testing.T) {
	r := newReleaseRepo(t, "1.0.0")
	r.git("tag", "v1.0.0")
	r.commit("VERSION", "0.9.0\n")
	r.git("tag", "v0.9.0")
	before := r.git("rev-parse", "HEAD")

	r.commit("VERSION", "1.0.0\n")
	out, err := r.guard(before)
	mustRefuse(t, "bump to an already-tagged VERSION", out, err, alreadyTagged)

	r.commit("VERSION", "1.1.0\n")
	out, err = r.guard(before)
	mustPass(t, "bump to a fresh VERSION", out, err)
}

// A merge is judged by the tree it leaves, not by the commit that made it:
// merging a branch that moved VERSION is a VERSION change on this line.
func TestVersionGuard_JudgesWhatAMergeDoesToVersion(t *testing.T) {
	r := newReleaseRepo(t, "1.0.0")
	r.git("tag", "v1.0.0")
	fork := r.git("rev-parse", "HEAD")

	r.git("checkout", "-q", "-b", "code", fork)
	r.commit("code.go", "package x\n")
	r.git("checkout", "-q", "-b", "stale-bump", fork)
	r.commit("VERSION", "0.9.0\n")
	r.git("tag", "v0.9.0")
	r.git("checkout", "-q", "line")
	r.commit("other.go", "package y\n")
	before := r.git("rev-parse", "HEAD")

	r.git("merge", "-q", "--no-edit", "code")
	out, err := r.guard(before)
	mustPass(t, "merge not touching VERSION", out, err)

	r.git("merge", "-q", "--no-edit", "stale-bump")
	out, err = r.guard(before)
	mustRefuse(t, "merge setting an already-tagged VERSION", out, err, alreadyTagged)
}

// When the base cannot show that VERSION was left alone — the first push of a
// branch, a base this clone does not hold (a force push), no base at all —
// the guard checks rather than assumes.
func TestVersionGuard_FailsClosedWithoutAUsableBase(t *testing.T) {
	r := newReleaseRepo(t, "1.0.0")
	r.git("tag", "v1.0.0")
	for name, base := range map[string]string{
		"first push":     noCommit,
		"unknown commit": "1111111111111111111111111111111111111111",
		"empty":          "",
	} {
		out, err := r.guard(base)
		mustRefuse(t, name+" with a tagged VERSION", out, err, alreadyTagged)
	}

	r.commit("VERSION", "2.0.0\n")
	out, err := r.guard(noCommit)
	mustPass(t, "first push with a fresh VERSION", out, err)
}

// auto-release runs release-tag after EVERY green push, so on all but the
// push that set VERSION the tag is already there — cut earlier on this line.
func TestReleaseTag_SkipsAVersionThisLineAlreadyReleased(t *testing.T) {
	r := newReleaseRepo(t, "1.0.0")
	r.git("tag", "v1.0.0")
	r.commit("code.go", "package x\n")
	head := r.git("rev-parse", "HEAD")

	out, err := r.just("release-tag")
	mustPass(t, "tag already on an ancestor", out, err)
	if got := r.git("rev-list", "-n", "1", "v1.0.0"); got == head {
		t.Errorf("release-tag re-pointed v1.0.0 at HEAD")
	}
}

// A tag of this VERSION that this line does not contain names a release cut
// from somewhere else; publishing under it would mislabel this tree.
func TestReleaseTag_RefusesAVersionTaggedOffThisLine(t *testing.T) {
	r := newReleaseRepo(t, "1.0.0")
	fork := r.git("rev-parse", "HEAD")
	r.git("checkout", "-q", "-b", "elsewhere", fork)
	r.commit("code.go", "package x\n")
	r.git("tag", "v1.0.0")
	r.git("checkout", "-q", "line")
	r.commit("other.go", "package y\n")

	out, err := r.just("release-tag")
	mustRefuse(t, "tag on a commit this line does not contain", out, err, taggedOffLine)
}
