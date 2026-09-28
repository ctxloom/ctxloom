package isolation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport/fileperm"
)

// TestBuildRunnerSpec_WiresAuthAndMounts: buildRunnerSpec threads the
// workspace's SCOPED auth env in on top of the container base env and layers
// the auth credential mounts + config overlays after the project mount.
// Rendering through Docker confirms the read-only credential mount and the
// auth -e survive.
func TestBuildRunnerSpec_WiresAuthAndMounts(t *testing.T) {
	extraEnv := []string{"ANTHROPIC_API_KEY=scoped", "ANTHROPIC_BASE_URL=https://example"}
	credMount := Mount{Host: "/h/.claude/.credentials.json", Container: "/root/.claude/.credentials.json", ReadOnly: true}
	overlayMount := Mount{Host: "/scratch/cfg0", Container: "/proj/.claude"}

	spec := runnerSpecFor(Docker{}, "claude-code", "/proj", extraEnv, []Mount{credMount, overlayMount})

	assert.Equal(t, "/proj", spec.WorkDir)
	assert.Equal(t, defaultContainerHome, spec.Home)

	assert.Contains(t, spec.Env, "IS_SANDBOX=1", "container base env signals the sandbox (root approval-bypass)")
	assert.Contains(t, spec.Env, "ANTHROPIC_API_KEY=scoped", "scoped auth env threaded in")
	assert.Contains(t, spec.Env, "ANTHROPIC_BASE_URL=https://example")
	for _, e := range spec.Env {
		assert.False(t, strings.HasPrefix(e, "HOME="), "the host HOME never crosses: %s", e)
	}

	// Mounts: project (identical-path) + auth cred + overlay.
	assert.Contains(t, spec.Mounts, Mount{Host: "/proj", Container: "/proj"})
	assert.Contains(t, spec.Mounts, credMount)
	assert.Contains(t, spec.Mounts, overlayMount)

	// Rendered argv: the credential mount is read-only; the auth env is an -e.
	argv := strings.Join(Docker{rootless: true}.RunArgs(spec), " ")
	assert.Contains(t, argv, "--mount type=bind,source=/h/.claude/.credentials.json,target=/root/.claude/.credentials.json,readonly")
	assert.Contains(t, argv, "-e ANTHROPIC_API_KEY=scoped")
	assert.Contains(t, argv, "--mount type=bind,source=/scratch/cfg0,target=/proj/.claude")
}

// TestBuildRunnerSpec_PublishesNoPort: the runner dials home over the
// coordinator's reach-back, so a run spec never publishes a `-p` host port.
func TestBuildRunnerSpec_PublishesNoPort(t *testing.T) {
	spec := runnerSpecFor(Docker{}, "mock", "/proj", nil, nil)
	argv := strings.Join(Docker{rootless: true}.RunArgs(spec), " ")
	assert.NotContains(t, argv, "-p ", "no host port is published")
	assert.NotContains(t, argv, "0.0.0.0", "no port is ever published")
}

// TestContainerConfigOverlay_ShadowsManagedPaths: the overlay produces one
// writable bind mount per managed-config directory, each backed by a real scratch
// dir under the root, whose container target shadows the project path — keeping
// the host project clean of ctxloom's per-run config writes.
func TestContainerConfigOverlay_ShadowsManagedPaths(t *testing.T) {
	proj := t.TempDir()
	root := t.TempDir()
	mounts, err := containerConfigOverlay(Docker{}, proj, root, claudeOverlayDirs(t))
	require.NoError(t, err)
	require.Len(t, mounts, 2)

	targets := map[string]bool{}
	for _, m := range mounts {
		targets[m.Container] = true
		assert.False(t, m.ReadOnly, "config overlays must be writable")
		assert.True(t, strings.HasPrefix(m.Host, root), "overlay host lives under the scratch root")
		info, statErr := os.Stat(m.Host)
		require.NoError(t, statErr, "overlay host scratch dir is created")
		assert.True(t, info.IsDir())
	}
	assert.True(t, targets[filepath.Join(proj, ".claude")], ".claude shadowed")
	assert.True(t, targets[filepath.Join(proj, ".ctxloom/cache")], ".ctxloom/cache shadowed")
}

// TestContainerConfigOverlay_PrecreatesTargets: the overlay TARGET (projectDir/rel)
// is nested inside the identical-path project bind, so if it does not yet exist a
// rootful docker daemon would create the bind mountpoint AS ROOT — landing a
// root-owned dir in the real HOST project and EACCES-ing every later host run's
// managed-config writers. Over a FRESH project (no .claude / .ctxloom), ctxloom
// must pre-create each target itself (as the invoking user), so docker finds it
// existing and never root-creates it.
func TestContainerConfigOverlay_PrecreatesTargets(t *testing.T) {
	proj := t.TempDir() // fresh: no .claude, no .ctxloom
	root := t.TempDir()

	mounts, err := containerConfigOverlay(Docker{}, proj, root, claudeOverlayDirs(t))
	require.NoError(t, err)
	require.Len(t, mounts, len(claudeOverlayDirs(t)))

	for _, rel := range claudeOverlayDirs(t) {
		target := filepath.Join(proj, rel)
		info, statErr := os.Stat(target)
		require.NoError(t, statErr, "ctxloom pre-creates overlay target %q so docker never root-creates it in the host project", target)
		assert.True(t, info.IsDir(), "%q is a directory", target)
	}
	for _, m := range mounts {
		assert.True(t, strings.HasPrefix(m.Container, proj), "overlay target is nested under the identical-path project bind")
		info, statErr := os.Stat(m.Container)
		require.NoError(t, statErr, "the nested mountpoint exists on the host before the container starts")
		assert.True(t, info.IsDir())
	}
}

// TestContainerConfigOverlay_SeedsFromProject: each overlay scratch dir starts
// as a COPY of the project's corresponding managed-config directory — the
// engine sees the user's existing commands/settings instead of an empty shadow,
// while writes still land in scratch. An overlay dir absent from the project
// (the fresh-project case) seeds nothing and still mounts.
func TestContainerConfigOverlay_SeedsFromProject(t *testing.T) {
	proj := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(proj, ".claude", "commands"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(proj, ".claude", "settings.json"), []byte(`{"user":true}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(proj, ".claude", "commands", "mine.md"), []byte("hand-written"), 0o644))
	// .ctxloom/cache deliberately absent from the project.

	root := t.TempDir()
	mounts, err := containerConfigOverlay(Docker{}, proj, root, claudeOverlayDirs(t))
	require.NoError(t, err)
	require.Len(t, mounts, 2)

	seeded, err := os.ReadFile(filepath.Join(mounts[0].Host, "settings.json"))
	require.NoError(t, err)
	assert.Equal(t, `{"user":true}`, string(seeded), "top-level file seeded into the overlay")
	nested, err := os.ReadFile(filepath.Join(mounts[0].Host, "commands", "mine.md"))
	require.NoError(t, err)
	assert.Equal(t, "hand-written", string(nested), "nested user-authored content seeded")

	entries, err := os.ReadDir(mounts[1].Host)
	require.NoError(t, err)
	assert.Empty(t, entries, "absent project dir seeds an empty overlay")
}

// TestContainerConfigOverlay_LeavesAnExistingHostTargetUntouched: the overlay
// exists to keep the HOST project clean, and the target pre-create runs against
// the user's REAL project directory. When that directory already exists it must
// come out byte- and mode-identical: no content removed, no chmod, nothing
// written into it. PrecreatesTargets only covers the fresh-project case, so a
// pre-create that recreated or re-permissioned an existing target would pass it
// while wiping or loosening the user's own config.
func TestContainerConfigOverlay_LeavesAnExistingHostTargetUntouched(t *testing.T) {
	proj := t.TempDir()
	claudeDir := filepath.Join(proj, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))
	require.NoError(t, os.Chmod(claudeDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{"user":true}`), 0o600))

	_, err := containerConfigOverlay(Docker{}, proj, t.TempDir(), claudeOverlayDirs(t))
	require.NoError(t, err)

	info, err := os.Stat(claudeDir)
	require.NoError(t, err)
	fileperm.Equal(t, 0o700, info.Mode(), "an existing target is never chmod-ed")
	entries, err := os.ReadDir(claudeDir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "nothing is written into, or removed from, the host project's own config dir")
	body, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	require.NoError(t, err)
	assert.Equal(t, `{"user":true}`, string(body))
}

// TestHostTerminalEnv_ForwardsOnlySetVars: the host's TERM/COLORTERM cross into
// the container run env verbatim; unset vars are omitted so the image default
// applies. Nothing else is ever forwarded here.
func TestHostTerminalEnv_ForwardsOnlySetVars(t *testing.T) {
	tests := []struct {
		name     string
		env      map[string]string
		expected []string
	}{
		{"both set", map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}, []string{"TERM=xterm-256color", "COLORTERM=truecolor"}},
		{"term only", map[string]string{"TERM": "screen"}, []string{"TERM=screen"}},
		{"none set", map[string]string{}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hostTerminalEnv(func(k string) string { return tt.env[k] })
			assert.Equal(t, tt.expected, got)
		})
	}
}
