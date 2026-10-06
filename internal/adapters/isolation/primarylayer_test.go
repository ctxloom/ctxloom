//go:build !windows

// Primary-layer translation is docker-outside-of-docker from a Linux container:
// only there does findSelf ever resolve a self (containerprobe.SelfIDCandidates
// proposes nothing elsewhere), and its mounts are Linux paths a Windows
// filesystem cannot resolve.

package isolation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ciMounts is the CI job container's daemon-side mounts (see
// ciJobContainerInspect): a nested bind, /tmp shared at the same path, a
// named volume — plus a nested bind whose source is NOT under its parent's,
// the only shape that tells the longest match from the first.
var ciMounts = []selfMount{
	{source: "/home/runner/work", destination: "/__w"},
	{source: "/home/runner/work/_temp", destination: "/__w/_temp"},
	{source: "/srv/cache", destination: "/__w/.cache"},
	{source: "/tmp", destination: "/tmp"},
	{source: "/var/lib/docker/volumes/home/_data", destination: "/root/.ctxloom"},
}

// ciPrimary is the CI job container's own layer.
var ciPrimary = primaryLayer(&selfContainer{id: selfID, mounts: ciMounts})

// TestPrimaryLayer_Reverse: the bind SOURCE is the daemon's name for the
// file this process sees at p — the longest of self's mount destinations that
// is p or a /-bounded prefix of it, with the rest of p kept.
func TestPrimaryLayer_Reverse(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"under a bind", "/__w/ctxloom/ctxloom", "/home/runner/work/ctxloom/ctxloom"},
		{"the nested bind wins over its parent", "/__w/_temp/run-1/home", "/home/runner/work/_temp/run-1/home"},
		{"the nested bind wins even when its source is elsewhere", "/__w/.cache/go", "/srv/cache/go"},
		{"exactly a destination", "/__w", "/home/runner/work"},
		{"identity-shared /tmp", "/tmp/TestX123/001", "/tmp/TestX123/001"},
		{"a volume names its daemon-side data dir", "/root/.ctxloom/sessions/a", "/var/lib/docker/volumes/home/_data/sessions/a"},
		{"cleaned first", "/__w/./ctxloom//x/../y", "/home/runner/work/ctxloom/y"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ciPrimary.Reverse(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestPrimaryLayer_UncoveredIsRefused: a path no mount covers has no name
// on the daemon — including a sibling that merely shares a destination's
// leading characters (/__wx is not under /__w) and a tmpfs, which decodeSelf
// drops because it names nothing a bind can.
func TestPrimaryLayer_UncoveredIsRefused(t *testing.T) {
	for _, p := range []string{"/__wx/a", "/home/u/.ctxloom", "/run/scratch/x", "/"} {
		_, err := ciPrimary.Reverse(p)
		require.ErrorIs(t, err, ErrUnmapped, p)
		assert.Contains(t, err.Error(), p)
	}
}

// TestPrimaryLayer_ResolvesSymlinksFirst: a mount destination is a real
// path, so a symlinked path is matched by what it points at.
func TestPrimaryLayer_ResolvesSymlinksFirst(t *testing.T) {
	real, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(real, "sub"), 0o755))
	link := filepath.Join(t.TempDir(), "ln")
	require.NoError(t, os.Symlink(real, link))
	src := primaryLayer(&selfContainer{mounts: []selfMount{{source: "/daemon/side", destination: real}}})
	got, err := src.Reverse(filepath.Join(link, "sub"))
	require.NoError(t, err)
	assert.Equal(t, "/daemon/side/sub", got)
}

// TestPrimaryLayer_ResolvesSymlinkedDestinations: the daemon reports a mount
// destination as it was requested, and that path may run through a link in
// this container (the image's /var/run -> /run). Reverse resolves the path it
// is asked about, so the destinations must be resolved too, or the socket
// mounted at /var/run/docker.sock has no name. A path not created yet under
// such a mount is still named, and a child's mount through it is sourced from
// the host (ToChild reverses through the primary; the child's view is never
// resolved).
func TestPrimaryLayer_ResolvesSymlinkedDestinations(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	run := filepath.Join(root, "run")
	require.NoError(t, os.MkdirAll(filepath.Join(run, "work"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(run, "docker.sock"), nil, 0o600))
	varRun := filepath.Join(root, "var-run")
	require.NoError(t, os.Symlink(run, varRun))

	primary := primaryLayer(&selfContainer{mounts: []selfMount{
		{source: "/host/docker.sock", destination: filepath.Join(varRun, "docker.sock")},
		{source: "/host/work", destination: filepath.Join(varRun, "work")},
	}})
	for _, p := range []string{filepath.Join(varRun, "docker.sock"), filepath.Join(run, "docker.sock")} {
		got, err := primary.Reverse(p)
		require.NoError(t, err, "%s names the mounted socket", p)
		assert.Equal(t, "/host/docker.sock", got)
	}
	got, err := primary.Reverse(filepath.Join(varRun, "work", "new", "out.txt"))
	require.NoError(t, err, "a path not created yet is named through its existing parent")
	assert.Equal(t, "/host/work/new/out.txt", got)

	c, err := crossingOver(layerRuntime{layer: primary}, mount{Host: filepath.Join(varRun, "work"), Container: "/agent/work"})
	require.NoError(t, err)
	assert.Equal(t, LayerMount{Host: "/host/work", View: "/agent/work"}, c.Child.mounts[0], "the child's bind source is the host path")
	child, host, err := c.ToChild(filepath.Join(run, "work", "f"))
	require.NoError(t, err)
	assert.Equal(t, "/agent/work/f", child)
	assert.Equal(t, "/host/work/f", host)
}

func TestPrimary_SelfDecidesTheLayer(t *testing.T) {
	assert.Equal(t, HostLayer(), ociRuntime{}.primary(), "not a container of the daemon: it shares our mount namespace")
	s := selfContainer{id: selfID, mounts: ciMounts}
	assert.Equal(t, ciPrimary, ociRuntime{self: &s}.primary())
	got, err := HostLayer().Reverse("/any/path")
	require.NoError(t, err)
	assert.Equal(t, "/any/path", got)
}

// TestMountArgs_SourceTranslatedTargetKept: only the SOURCE is the daemon's
// path; the TARGET stays the path this process sees, so ctxloom and the
// runner name every file by the same path (the cross-view invariant spool
// refs, present.Mapped and delivery rest on).
func TestMountArgs_SourceTranslatedTargetKept(t *testing.T) {
	child, err := childLayer(ciPrimary, []mount{{Host: "/__w/ctxloom/ctxloom", Container: "/__w/ctxloom/ctxloom"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"--mount", "type=bind,source=/home/runner/work/ctxloom/ctxloom,target=/__w/ctxloom/ctxloom"}, mountArgs(child))
}

// TestRunArgs_AnUncoveredMountIsAnError: a run whose mount the daemon has no
// name for is refused while it is rendered, never launched to bind the wrong
// (auto-created, empty) directory.
func TestRunArgs_AnUncoveredMountIsAnError(t *testing.T) {
	s := selfContainer{id: selfID, network: selfNetwork{"n", "172.18.0.2"}, mounts: ciMounts}
	spec := sampleSpec()
	spec.Mounts = []mount{{Host: "/home/u/.ctxloom/x", Container: "/home/u/.ctxloom/x"}}
	for _, rt := range []Runtime{Docker{ociRuntime: withSelf(s)}, Podman{ociRuntime: withSelf(s)}} {
		_, err := rt.RunArgs(spec)
		require.ErrorIs(t, err, ErrUnmapped, rt.Name())
	}
}

// TestProbeOneRoot_AnUncoveredRootIsADefinitiveSharingVerdict: the shared-fs
// gate renders every mount root before anything launches, so an uncovered
// root is refused THERE — a definitive (memoizable) sharing verdict naming the
// path and the DooD remedy, never "could not run", and no container is run.
func TestProbeOneRoot_AnUncoveredRootIsADefinitiveSharingVerdict(t *testing.T) {
	calls := scriptExec(t, nil)
	root := t.TempDir()
	s := selfContainer{id: selfID, mounts: []selfMount{{source: "/daemon/elsewhere", destination: "/nowhere-this-test-uses"}}}
	err := probeOneRoot(context.Background(), Docker{ociRuntime: withSelf(s)}, "img", root)
	var mism *sharedFSMismatch
	require.True(t, errors.As(err, &mism), "got %v", err)
	assert.True(t, definitiveProbe(err))
	assert.Contains(t, err.Error(), root)
	assert.Contains(t, err.Error(), "volume")
	assert.Empty(t, *calls, "no probe container runs for a root the daemon cannot name")
}

// TestProbeOneRoot_ACoveredRootProbesTheDaemonSidePath: under DooD the probe
// binds the daemon's name for the marker dir, so what it verifies is the
// translated mount a real run would render.
func TestProbeOneRoot_ACoveredRootProbesTheDaemonSidePath(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	var argv []string
	orig := probeExec
	probeExec = func(_ context.Context, _ string, args []string) (string, error) {
		argv = args
		for i, a := range args {
			if a == "--mount" {
				for _, f := range strings.Split(args[i+1], ",") {
					if src, ok := strings.CutPrefix(f, "source="); ok {
						b, rerr := os.ReadFile(filepath.Join(strings.Replace(src, "/daemon/side", root, 1), "marker"))
						return string(b), rerr
					}
				}
			}
		}
		return "", errors.New("no mount")
	}
	t.Cleanup(func() { probeExec = orig })
	s := selfContainer{id: selfID, mounts: []selfMount{{source: "/daemon/side", destination: root}}}
	require.NoError(t, probeOneRoot(context.Background(), Docker{ociRuntime: withSelf(s)}, "img", root))
	assert.Contains(t, strings.Join(argv, " "), "source=/daemon/side/"+probeScratchPrefix)
}
