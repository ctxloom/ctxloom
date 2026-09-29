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
var ciMounts = selfMountSource{mounts: []selfMount{
	{source: "/home/runner/work", destination: "/__w"},
	{source: "/home/runner/work/_temp", destination: "/__w/_temp"},
	{source: "/srv/cache", destination: "/__w/.cache"},
	{source: "/tmp", destination: "/tmp"},
	{source: "/var/lib/docker/volumes/home/_data", destination: "/root/.ctxloom"},
}}

// TestSelfMountSource_ToDaemon: the bind SOURCE is the daemon's name for the
// file this process sees at p — the longest of self's mount destinations that
// is p or a /-bounded prefix of it, with the rest of p kept.
func TestSelfMountSource_ToDaemon(t *testing.T) {
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
			got, err := ciMounts.toDaemon(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestSelfMountSource_UncoveredIsRefused: a path no mount covers has no name
// on the daemon — including a sibling that merely shares a destination's
// leading characters (/__wx is not under /__w) and a tmpfs, which decodeSelf
// drops because it names nothing a bind can.
func TestSelfMountSource_UncoveredIsRefused(t *testing.T) {
	for _, p := range []string{"/__wx/a", "/home/u/.ctxloom", "/run/scratch/x", "/"} {
		_, err := ciMounts.toDaemon(p)
		require.ErrorIs(t, err, errNoDaemonSource, p)
		assert.Contains(t, err.Error(), p)
	}
}

// TestSelfMountSource_ResolvesSymlinksFirst: a mount destination is a real
// path, so a symlinked path is matched by what it points at.
func TestSelfMountSource_ResolvesSymlinksFirst(t *testing.T) {
	real, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(real, "sub"), 0o755))
	link := filepath.Join(t.TempDir(), "ln")
	require.NoError(t, os.Symlink(real, link))
	src := selfMountSource{mounts: []selfMount{{source: "/daemon/side", destination: real}}}
	got, err := src.toDaemon(filepath.Join(link, "sub"))
	require.NoError(t, err)
	assert.Equal(t, "/daemon/side/sub", got)
}

func TestSources_SelfDecides(t *testing.T) {
	assert.Equal(t, sharedSource{}, ociRuntime{}.sources(), "not a container of the daemon: it shares our mount namespace")
	s := selfContainer{id: selfID, mounts: ciMounts.mounts}
	assert.Equal(t, ciMounts, ociRuntime{self: &s}.sources())
	got, err := sharedSource{}.toDaemon("/any/path")
	require.NoError(t, err)
	assert.Equal(t, "/any/path", got)
}

// TestMountArgs_SourceTranslatedTargetKept: only the SOURCE is the daemon's
// path; the TARGET stays the path this process sees, so ctxloom and the
// runner name every file by the same path (the cross-view invariant spool
// refs, present.Mapped and delivery rest on).
func TestMountArgs_SourceTranslatedTargetKept(t *testing.T) {
	args, err := mountArgs([]mount{{Host: "/__w/ctxloom/ctxloom", Container: "/__w/ctxloom/ctxloom"}}, ciMounts)
	require.NoError(t, err)
	assert.Equal(t, []string{"--mount", "type=bind,source=/home/runner/work/ctxloom/ctxloom,target=/__w/ctxloom/ctxloom"}, args)
}

// TestRunArgs_AnUncoveredMountIsAnError: a run whose mount the daemon has no
// name for is refused while it is rendered, never launched to bind the wrong
// (auto-created, empty) directory.
func TestRunArgs_AnUncoveredMountIsAnError(t *testing.T) {
	s := selfContainer{id: selfID, network: selfNetwork{"n", "172.18.0.2"}, mounts: ciMounts.mounts}
	spec := sampleSpec()
	spec.Mounts = []mount{{Host: "/home/u/.ctxloom/x", Container: "/home/u/.ctxloom/x"}}
	for _, rt := range []Runtime{Docker{ociRuntime: withSelf(s)}, Podman{ociRuntime: withSelf(s)}} {
		_, err := rt.RunArgs(spec)
		require.ErrorIs(t, err, errNoDaemonSource, rt.Name())
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
