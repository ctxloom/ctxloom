package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// pullFixtureMarker is the payload the seeded remote publishes. Asserting it
// reaches the project's clone cache is what separates a pull that moved BYTES
// from one that merely exited 0 with a reassuring line.
const pullFixtureMarker = "SEEDED-REMOTE-FRAGMENT-PAYLOAD"

// initPullGit runs git hermetically (no user/system config, no credential
// prompt) for the fixture below.
func initPullGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
}

// seedBundleRemote creates a bare git repository serving a one-bundle ctxloom
// layout and returns its file:// clone URL. go-git clones and fetches a file://
// remote through the identical transport it uses for https, so this exercises
// the REAL fetch/lock path with no network — the same technique the acceptance
// suite's SeedRemote uses (docs/adr/0015-local-git-test-remote.md).
func seedBundleRemote(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")

	initPullGit(t, "", "init", "--bare", "-b", "main", bare)
	initPullGit(t, "", "init", "-b", "main", work)
	for _, kv := range [][]string{
		{"user.email", "test@example.com"},
		{"user.name", "Test User"},
		{"commit.gpgsign", "false"},
	} {
		initPullGit(t, work, "config", kv[0], kv[1])
	}

	rel := filepath.Join(".ctxloom", "content", "bundles", "demo.yaml")
	full := filepath.Join(work, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	body := "version: \"1.0.0\"\n" +
		"author: test\n" +
		"description: Demo bundle for the init dependency pull\n" +
		"fragments:\n" +
		"  demo-frag:\n" +
		"    tags: [demo]\n" +
		"    content: |\n" +
		"      " + pullFixtureMarker + "\n"
	require.NoError(t, os.WriteFile(full, []byte(body), 0o644))

	initPullGit(t, work, "add", "-A")
	initPullGit(t, work, "commit", "-m", "seed")
	initPullGit(t, work, "remote", "add", "origin", bare)
	initPullGit(t, work, "push", "origin", "main")
	initPullGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")

	return "file://" + bare
}

// seedProjectReferencing scaffolds a .ctxloom whose directory profile depends
// on bundleRef — the shape `ctxloom init` itself seeds (resources/profiles/
// default.yaml declares a REMOTE parent), with the address pointed at a local
// fixture instead of github. Returns the project dir and its .ctxloom.
func seedProjectReferencing(t *testing.T, bundleRef string) (project, appDir string) {
	t.Helper()
	project = t.TempDir()
	appDir = filepath.Join(project, ".ctxloom")
	require.NoError(t, os.MkdirAll(filepath.Join(appDir, "profiles"), 0o755))

	cfgBody, err := operations.BuildInitialConfig("claude-code", "")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"), cfgBody, 0o644))

	profile := "version: \"1.0.0\"\n" +
		"description: seeded default profile\n" +
		"bundles:\n" +
		"  - " + bundleRef + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "profiles", "default.yaml"), []byte(profile), 0o644))
	return project, appDir
}

// withInitFlags pins every `ctxloom init` flag this package keeps in a package
// var for the duration of one test, restoring the caller's values afterwards.
func withInitFlags(t *testing.T, noPull bool) {
	t.Helper()
	origHome, origNon, origSkip, origNoPull := initHome, initNonInteractive, initSkipLaunch, initNoPull
	origRemotes, origForge, origEngine := initRemotes, initForge, initEngine
	initHome, initNonInteractive, initSkipLaunch, initNoPull = false, true, true, noPull
	initRemotes, initForge, initEngine = nil, "", "claude-code"
	t.Cleanup(func() {
		initHome, initNonInteractive, initSkipLaunch, initNoPull = origHome, origNon, origSkip, origNoPull
		initRemotes, initForge, initEngine = origRemotes, origForge, origEngine
	})
}

// initTestCmd is a cobra command carrying a context, as cobra's own
// Execute/ExecuteContext always hands one to a RunE. runInit passes it
// straight through to the fetch machinery, which shells out to git.
func initTestCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	return cmd
}

// lockedRefs returns the canonical references the project's lockfile pins.
func lockedRefs(t *testing.T, appDir string) []string {
	t.Helper()
	lf, err := remote.NewLockfileManager(appDir).Load()
	require.NoError(t, err)
	var refs []string
	for _, e := range lf.AllEntries() {
		refs = append(refs, e.Ref)
	}
	return refs
}

// cachedBundleBytes reports whether the seeded bundle's PAYLOAD actually
// landed under the project's clone cache. The lockfile alone cannot tell a
// real fetch from a pin written over nothing — ctxloom's characteristic defect
// is exit 0 with a success line and zero bytes moved — so the marker string is
// searched for on disk.
func cachedBundleBytes(t *testing.T, appDir string) bool {
	t.Helper()
	found := false
	_ = filepath.WalkDir(filepath.Join(appDir, "cache"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || found {
			return nil //nolint:nilerr // a missing cache dir is "no bytes", not a test error
		}
		data, readErr := os.ReadFile(path)
		if readErr == nil && strings.Contains(string(data), pullFixtureMarker) {
			found = true
		}
		return nil
	})
	return found
}

// TestRunInit_PullsRemoteDependencies is the whole point of the automatic
// pull: `ctxloom init` must leave a project whose declared remote dependencies
// are INSTALLED, not merely declared. A seeded profile naming a remote parent
// resolves only through a lockfile entry, so until the pull runs, context
// assembly silently skips it and the freshly initialized project is degraded.
//
// The effect is asserted, never the message: the lockfile gains the entry AND
// the bundle's payload is on disk.
func TestRunInit_PullsRemoteDependencies(t *testing.T) {
	testsupport.Isolate(t)
	url := seedBundleRemote(t)
	ref := url + "@bundles/demo"
	project, appDir := seedProjectReferencing(t, ref)
	t.Chdir(project)
	config.Invalidate()
	t.Cleanup(config.Invalidate)
	withInitFlags(t, false)

	captureStdout(t, func() {
		require.NoError(t, runInit(initTestCmd(), nil))
	})

	assert.Contains(t, lockedRefs(t, appDir), ref,
		"init must pin the remote dependency its seeded profile declares")
	assert.True(t, cachedBundleBytes(t, appDir),
		"init must fetch the dependency's bytes, not just write a pin over nothing")
}

// TestRunInit_RepeatedInitKeepsTheClosureInstalled pins that re-running init
// over an already-initialized project is not made worse by the automatic pull:
// the second run still succeeds and the closure stays installed.
func TestRunInit_RepeatedInitKeepsTheClosureInstalled(t *testing.T) {
	testsupport.Isolate(t)
	url := seedBundleRemote(t)
	ref := url + "@bundles/demo"
	project, appDir := seedProjectReferencing(t, ref)
	t.Chdir(project)
	config.Invalidate()
	t.Cleanup(config.Invalidate)
	withInitFlags(t, false)

	for run := 0; run < 2; run++ {
		config.Invalidate()
		captureStdout(t, func() {
			require.NoError(t, runInit(initTestCmd(), nil), "init run %d must succeed", run+1)
		})
	}

	assert.Contains(t, lockedRefs(t, appDir), ref,
		"a repeated init must leave the pin in place, not drop it")
	assert.True(t, cachedBundleBytes(t, appDir), "the payload must still be installed")
}

// TestRunInit_NoPull_SuppressesTheDependencyPull pins the escape hatch. With
// --no-pull nothing is fetched and nothing is pinned — and init SAYS so, so a
// user is never left believing a suppressed pull happened.
func TestRunInit_NoPull_SuppressesTheDependencyPull(t *testing.T) {
	testsupport.Isolate(t)
	url := seedBundleRemote(t)
	ref := url + "@bundles/demo"
	project, appDir := seedProjectReferencing(t, ref)
	t.Chdir(project)
	config.Invalidate()
	t.Cleanup(config.Invalidate)
	withInitFlags(t, true)

	out := captureStdout(t, func() {
		require.NoError(t, runInit(initTestCmd(), nil))
	})

	assert.NotContains(t, lockedRefs(t, appDir), ref, "--no-pull must not pin anything")
	assert.False(t, cachedBundleBytes(t, appDir), "--no-pull must not fetch anything")
	assert.Contains(t, out, "--no-pull", "init must name the flag that suppressed the pull")
	assert.Contains(t, out, "ctxloom deps pull",
		"init must name the command that installs what it deliberately did not")
}

// TestRunInit_FailedPullKeepsTheProjectAndSaysSo covers the offline / proxied /
// unreachable-remote case. A pull that cannot reach its remote must NOT
// destroy or roll back the init: everything init wrote stays, the command
// still succeeds, and the failure is reported loudly enough to name what is
// missing and what to run.
func TestRunInit_FailedPullKeepsTheProjectAndSaysSo(t *testing.T) {
	testsupport.Isolate(t)
	url := seedBundleRemote(t)
	ref := url + "@bundles/demo"
	project, appDir := seedProjectReferencing(t, ref)

	// Rename the bare repo out from under its URL: every fetch against it now
	// fails exactly as a partition, a proxy or a revoked credential would.
	bare := strings.TrimPrefix(url, "file://")
	require.NoError(t, os.Rename(bare, bare+".unreachable"))

	t.Chdir(project)
	config.Invalidate()
	t.Cleanup(config.Invalidate)
	withInitFlags(t, false)

	var runErr error
	stderr := captureStderr(t, func() {
		captureStdout(t, func() { runErr = runInit(initTestCmd(), nil) })
	})

	require.NoError(t, runErr, "a failed pull must not fail the init")
	assert.FileExists(t, filepath.Join(appDir, "config.yaml"),
		"init must keep everything it wrote when the pull fails")
	assert.FileExists(t, filepath.Join(appDir, "profiles", "default.yaml"),
		"init must keep the seeded profile when the pull fails")
	assert.NotContains(t, lockedRefs(t, appDir), ref, "an unreachable remote must not be pinned")

	assert.Contains(t, stderr, "warning:", "a failed dependency pull is a warning, not silence")
	assert.Contains(t, stderr, "NOT installed",
		"the warning must name the state it left: the dependencies are NOT installed")
	assert.Contains(t, stderr, "ctxloom deps pull",
		"the warning must name the command that finishes the job")
}
