//go:build !windows

// Drives hermeticHostContainer's #!/bin/sh runtime stub, which a Windows host cannot exec.

package isolation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

const secretVar = "CLAUDE_CODE_OAUTH_TOKEN"

// preparedSecretCell runs the container's real prepare path over a token
// credential: the workspace (bind), the relocator, and the environment that
// materializes the secret. It returns the environment and its runner spec.
func preparedSecretCell(t *testing.T) (Environment, *containerWorkspace, RunSpec) {
	t.Helper()
	ctx := context.Background()
	c := hermeticHostContainer(t, nil)
	c.state = SessionState{Harp: "brisk-teal-otter"}
	creds := engine.Credentials{Env: map[string]string{secretVar: fixtureSecret}}

	ws, err := c.prepareWorkspace(ctx, t.TempDir(), "member-secret")
	require.NoError(t, err)
	pl, roots, err := c.relocator().relocate(layout{cwd: ws.Dir(), creds: creds})
	require.NoError(t, err)
	env, err := c.environment(ws, pl, roots, creds)
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Cleanup() })
	cw := ws.(*containerWorkspace)
	return env, cw, c.buildRunnerSpec("claude", "ctxloom-secret-test", cw, nil)
}

// secretMount is the one mount at secretsTarget.
func secretMount(t *testing.T, spec RunSpec) mount {
	t.Helper()
	var found []mount
	for _, m := range spec.Mounts {
		if m.Container == secretsTarget {
			found = append(found, m)
		}
	}
	require.Len(t, found, 1, "exactly one mount presents the secrets")
	return found[0]
}

// The originator materializes the credential before the container starts:
// the run's one owner-only dotenv secrets file, in an owner-only dir on the
// user's tmpfs ($XDG_RUNTIME_DIR), bound READ-ONLY at secretsTarget — the
// file the Placement names. The value is in neither the run's argv nor its
// env.
func TestContainer_SecretIsWrittenOwnerOnlyAndMountedReadOnly(t *testing.T) {
	testsupport.Isolate(t)
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	env, _, spec := preparedSecretCell(t)
	m := secretMount(t, spec)
	assert.True(t, m.ReadOnly, "the engine side cannot write the secret")
	assert.Equal(t, runtimeDir, filepath.Dir(m.Host), "the secret dir lives on the user's runtime tmpfs")

	file := filepath.Join(m.Host, secretsFileName)
	b, err := os.ReadFile(file)
	require.NoError(t, err)
	got, err := sessions.DecodeSecrets(b)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{secretVar: fixtureSecret}, got, "the exact value")
	fi, err := os.Stat(file)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	di, err := os.Stat(m.Host)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), di.Mode().Perm())

	assert.Equal(t, secretsTarget+"/"+secretsFileName, env.Placement().SecretFiles[secretVar], "the Placement names the mounted file")
	assert.NotContains(t, strings.Join(spec.Env, "\n"), fixtureSecret)
	assert.NotContains(t, strings.Join(spec.Command, "\n"), fixtureSecret)
}

// Teardown removes the secret: the environment's Cleanup releases it.
func TestContainer_CleanupRemovesTheSecret(t *testing.T) {
	testsupport.Isolate(t)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	env, _, spec := preparedSecretCell(t)
	dir := secretMount(t, spec).Host
	require.FileExists(t, filepath.Join(dir, secretsFileName), "premise: materialized")
	require.NoError(t, env.Cleanup())
	assert.NoDirExists(t, dir, "teardown removed the secret")
}

// A crashed originator runs no Cleanup: its secret dir is left behind, but
// its kernel lock died with it, so the next container run's prepare sweeps
// it. A LIVE owner's dir is left alone.
func TestContainer_TheNextPrepareSweepsACrashedRunsSecret(t *testing.T) {
	testsupport.Isolate(t)
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	// The live owner first: creating an owned scratch sweeps the dead ones,
	// so the crash must be the last thing before the prepare under test.
	live, err := newOwnedScratch(runtimeDir, secretScratchPrefix)
	require.NoError(t, err)
	t.Cleanup(live.release)
	crashed, err := newOwnedScratch(runtimeDir, secretScratchPrefix)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(crashed.dir, secretVar), []byte(fixtureSecret), 0o600))
	require.NoError(t, crashed.lock.Unlock(), "the owner dies: its lock goes, its dir stays")

	preparedSecretCell(t)
	assert.NoDirExists(t, crashed.dir, "the dead owner's secret was swept")
	assert.DirExists(t, live.dir, "a live owner's secret is never touched")
}

// With no user runtime dir (macOS, Windows, a session without one) the
// secret dir lives in the session's scratch/ dir beside the run's scratch
// root — not inside it, which is new per run and so would never hold a
// crashed sibling for the next run to reap.
func TestContainer_SecretFallsBackToTheSessionEphemeralDirWithoutARuntimeDir(t *testing.T) {
	testsupport.Isolate(t)
	t.Setenv("XDG_RUNTIME_DIR", "")

	_, cw, spec := preparedSecretCell(t)
	m := secretMount(t, spec)
	assert.Equal(t, filepath.Dir(cw.scratchRoot), filepath.Dir(m.Host))
	assert.FileExists(t, filepath.Join(m.Host, secretsFileName))
}

// A container environment names its secrets file's host path — what a
// restarted coordinator rewrites for the re-adopted run.
func TestContainer_SecretsFileNamesTheMountedFile(t *testing.T) {
	testsupport.Isolate(t)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	env, _, spec := preparedSecretCell(t)
	assert.Equal(t, filepath.Join(secretMount(t, spec).Host, secretsFileName), env.SecretsFile())
}

// TestRefreshSecrets_ARestartRewritesADeadOwnersSecretInPlace: the
// coordinator that launched a container died (its lock went, its dir and the
// container's mount of it stayed). The restarted one takes the dir over —
// rewriting the credential in the SAME file, keeping every other entry, still
// owner-only — and while it holds the dir no prepare sweeps it. Its release
// removes the dir.
func TestRefreshSecrets_ARestartRewritesADeadOwnersSecretInPlace(t *testing.T) {
	testsupport.Isolate(t)
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	env, _, spec := preparedSecretCell(t)
	dir := secretMount(t, spec).Host
	file := env.SecretsFile()
	cw := env.(*containerEnvironment).cw
	require.NoError(t, cw.secrets.put(map[string]string{sessions.EnvCoordCred: "c0ffee"}))
	_, err := RefreshSecrets(file, map[string]string{secretVar: "sk-fresh"})
	require.ErrorIs(t, err, ErrSecretsOwned, "a live owner's secrets are never taken over")
	require.NoError(t, cw.secrets.scratch.lock.Unlock(), "the owner dies: its lock goes, its dir stays")

	release, err := RefreshSecrets(file, map[string]string{secretVar: "sk-fresh"})
	require.NoError(t, err)
	b, err := os.ReadFile(file)
	require.NoError(t, err)
	got, err := sessions.DecodeSecrets(b)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{secretVar: "sk-fresh", sessions.EnvCoordCred: "c0ffee"}, got)
	fi, err := os.Stat(file)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())

	preparedSecretCell(t)
	assert.DirExists(t, dir, "a taken-over dir is live again: no prepare sweeps it")
	release()
	assert.NoDirExists(t, dir, "the release removes it")

	_, err = RefreshSecrets(file, map[string]string{secretVar: "x"})
	assert.Error(t, err, "a dir already gone cannot be refreshed")
}
