package isolation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// TestBuildRunSpec_WiresAuthHandshakeAndMounts: buildRunSpec keeps the curated
// go-plugin handshake env (socket dir overridden to the container path), threads
// the SCOPED auth env in ON TOP, drops host secrets/HOME, and layers the auth
// credential mounts + config overlays after the project + socket mounts. Rendering
// through Docker confirms the read-only credential mount and the auth -e survive.
func TestBuildRunSpec_WiresAuthHandshakeAndMounts(t *testing.T) {
	hostEnv := []string{
		pb.HandshakeConfig.MagicCookieKey + "=ai-backend-v1",
		"PLUGIN_PROTOCOL_VERSIONS=1",
		"PLUGIN_UNIX_SOCKET_DIR=/tmp/host-sock", // overridden to the container path
		"HOME=/home/babbitt",                    // host env — must be dropped
		"ANTHROPIC_API_KEY=leak",                // host secret — must NOT cross via handshake curation
	}
	extraEnv := []string{"ANTHROPIC_API_KEY=scoped", "ANTHROPIC_BASE_URL=https://example"}
	credMount := Mount{Host: "/h/.claude/.credentials.json", Container: "/root/.claude/.credentials.json", ReadOnly: true}
	overlayMount := Mount{Host: "/scratch/cfg0", Container: "/proj/.claude"}

	spec := buildRunSpec("img", "name", "/proj", "/root",
		[]string{"/usr/local/bin/ctxloom", "llm", "serve", "claudecode"},
		"/run/ctxloom/plugin", "/tmp/host-sock/plugin123",
		hostEnv, extraEnv, []Mount{credMount, overlayMount}, 0)

	assert.Equal(t, "/proj", spec.WorkDir)
	assert.Equal(t, "/root", spec.Home)

	// Env: handshake kept + socket overridden + scoped auth added; host HOME + the
	// host ANTHROPIC_API_KEY=leak curated out.
	assert.Contains(t, spec.Env, "IS_SANDBOX=1", "container base env signals the sandbox (root approval-bypass)")
	assert.Contains(t, spec.Env, pb.HandshakeConfig.MagicCookieKey+"=ai-backend-v1")
	assert.Contains(t, spec.Env, "PLUGIN_UNIX_SOCKET_DIR=/run/ctxloom/plugin")
	assert.Contains(t, spec.Env, "ANTHROPIC_API_KEY=scoped", "scoped auth env threaded in")
	assert.Contains(t, spec.Env, "ANTHROPIC_BASE_URL=https://example")
	assert.NotContains(t, spec.Env, "ANTHROPIC_API_KEY=leak", "host secret not blanket-passed")
	assert.NotContains(t, spec.Env, "HOME=/home/babbitt", "host HOME not crossed")

	// Mounts: project (identical-path) + socket + auth cred + overlay.
	assert.Contains(t, spec.Mounts, Mount{Host: "/proj", Container: "/proj"})
	assert.Contains(t, spec.Mounts, Mount{Host: "/tmp/host-sock/plugin123", Container: "/run/ctxloom/plugin"})
	assert.Contains(t, spec.Mounts, credMount, "credential mount threaded in")
	assert.Contains(t, spec.Mounts, overlayMount, "config overlay threaded in")

	// Rendered argv: the credential mount is read-only; the auth env is an -e.
	argv := strings.Join(Docker{rootless: true}.RunArgs(spec), " ")
	assert.Contains(t, argv, "--mount type=bind,source=/h/.claude/.credentials.json,target=/root/.claude/.credentials.json,readonly")
	assert.Contains(t, argv, "-e ANTHROPIC_API_KEY=scoped")
	assert.Contains(t, argv, "--mount type=bind,source=/scratch/cfg0,target=/proj/.claude")
}

// TestBuildRunSpec_LoopbackPortPublishesLoopbackTCP pins the NON-ZERO
// loopback-port path (the non-Linux / Docker-Desktop transport): a port > 0 is
// carried into RunSpec.PublishPort and renders a `-p 127.0.0.1:PORT:PORT`
// publish — loopback only, never 0.0.0.0 — so the host go-plugin client can dial
// the in-container TCP listener across the Docker Desktop VM boundary. The 0
// case (Linux unix-socket transport, no publish) is covered by
// TestBuildRunSpec_WiresAuthHandshakeAndMounts and asserted absent here.
func TestBuildRunSpec_LoopbackPortPublishesLoopbackTCP(t *testing.T) {
	const port = 54321
	spec := buildRunSpec("img", "name", "/proj", "/root",
		[]string{"/usr/local/bin/ctxloom", "llm", "serve", "mock"},
		"/run/ctxloom/plugin", "/tmp/host-sock/plugin1",
		nil, nil, nil, port)

	assert.Equal(t, port, spec.PublishPort, "a non-zero loopback port is carried into the run spec")
	assert.Contains(t, spec.Env, "PLUGIN_LISTEN_TCP=1", "a loopback port forces the container plugin onto TCP")

	argv := strings.Join(Docker{rootless: true}.RunArgs(spec), " ")
	assert.Contains(t, argv, "-p 127.0.0.1:54321:54321", "the loopback TCP port is published on 127.0.0.1 only")
	assert.NotContains(t, argv, "0.0.0.0", "the plugin RPC port is never published on all interfaces")

	// Contrast: the 0 (Linux) case publishes nothing.
	unixSpec := buildRunSpec("img", "name", "/proj", "/root",
		[]string{"/usr/local/bin/ctxloom", "llm", "serve", "mock"},
		"/run/ctxloom/plugin", "/tmp/host-sock/plugin1",
		nil, nil, nil, 0)
	assert.Equal(t, 0, unixSpec.PublishPort, "the Linux unix-socket transport publishes no port")
	assert.NotContains(t, strings.Join(Docker{rootless: true}.RunArgs(unixSpec), " "), "-p ",
		"no -p publish under the unix-socket transport")
}

// TestContainerConfigOverlay_ShadowsManagedPaths: the overlay produces one
// writable bind mount per managed-config directory, each backed by a real scratch
// dir under the root, whose container target shadows the project path — keeping
// the host project clean of ctxloom's per-run config writes.
func TestContainerConfigOverlay_ShadowsManagedPaths(t *testing.T) {
	proj := t.TempDir()
	root := t.TempDir()
	mounts, err := containerConfigOverlay(Docker{}, proj, root, defaultOverlayDirs)
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

	mounts, err := containerConfigOverlay(Docker{}, proj, root, defaultOverlayDirs)
	require.NoError(t, err)
	require.Len(t, mounts, len(defaultOverlayDirs))

	for _, rel := range defaultOverlayDirs {
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
	mounts, err := containerConfigOverlay(Docker{}, proj, root, defaultOverlayDirs)
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
