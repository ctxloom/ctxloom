package spool

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const testHarp = "ugly-icy-squid"

// hostHome points $HOME at a temp dir so every paths.* lookup in this test
// resolves inside it, and returns that root.
func hostHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// TestRefValidate_RefusesTraversal is the chokepoint test: refs arrive from a
// less-trusted peer, so a name or harp that escapes its directory must be
// REFUSED, never sanitised into something plausible.
//
// ".." is called out on its own because it contains no path separator: a
// validator that only rejects separators lets it through, and it resolves to
// the spool root's parent.
func TestRefValidate_RefusesTraversal(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()

	hostile := []struct {
		what string
		ref  Ref
	}{
		{"bare dot-dot name", Ref{Harp: testHarp, Dir: DirIn, Name: ".."}},
		{"dot-dot with separator", Ref{Harp: testHarp, Dir: DirIn, Name: "../../etc/passwd"}},
		{"nested name", Ref{Harp: testHarp, Dir: DirIn, Name: "sub/evil.md"}},
		{"backslash name", Ref{Harp: testHarp, Dir: DirIn, Name: `..\evil.md`}},
		{"single dot name", Ref{Harp: testHarp, Dir: DirIn, Name: "."}},
		{"empty name", Ref{Harp: testHarp, Dir: DirIn, Name: ""}},
		{"dotfile name", Ref{Harp: testHarp, Dir: DirIn, Name: ".hidden.md"}},
		{"control char name", Ref{Harp: testHarp, Dir: DirIn, Name: "a\nb.md"}},
		{"traversing harp", Ref{Harp: "../../../etc", Dir: DirIn, Name: "a.md"}},
		{"dot-dot harp", Ref{Harp: "..", Dir: DirIn, Name: "a.md"}},
		{"empty harp", Ref{Harp: "", Dir: DirIn, Name: "a.md"}},
		{"unknown dir", Ref{Harp: testHarp, Dir: Dir("in/../.."), Name: "a.md"}},
		{"unspecified dir", Ref{Harp: testHarp, Dir: Dir(""), Name: "a.md"}},
	}
	for _, tc := range hostile {
		t.Run(tc.what, func(t *testing.T) {
			require.Error(t, tc.ref.Validate(), "Ref.Validate must refuse %s", tc.what)

			path, err := m.Resolve(tc.ref)
			require.Error(t, err, "Resolve must refuse %s rather than join it", tc.what)
			require.Empty(t, path, "a refused ref must yield no path at all")
		})
	}
}

// TestResolve_KeepsHostileNameInsideTheSpool is the property behind the
// per-case list: whatever a peer sends, a resolved path never escapes the
// session's own spool root.
func TestResolve_KeepsHostileNameInsideTheSpool(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	root, err := Root(m, testHarp)
	require.NoError(t, err)
	require.NotEmpty(t, root)

	for _, name := range []string{"..", "../..", "sub/../../x.md", `..\..`} {
		path, err := m.Resolve(Ref{Harp: testHarp, Dir: DirIn, Name: name})
		if err != nil {
			continue // refused, which is the required outcome
		}
		t.Fatalf("Resolve accepted hostile name %q and produced %q (root %q)", name, path, root)
	}
}

// TestHomeMapper_CrossViewResolution pins the mount contract the whole
// substrate rests on: the SAME implementation must render the host view under
// the host's home and the container view under the container's home, because
// the persist bind target is home-relative and identically shaped. A mapper
// resolving against anything project-scoped would break that symmetry and the
// two sides would name different files.
func TestHomeMapper_CrossViewResolution(t *testing.T) {
	m := NewHomeMapper()
	ref := Ref{Harp: testHarp, Dir: DirIn, Name: "00000000000000000001.00000001.coord.md"}
	tail := filepath.Join(".ctxloom", "sessions", testHarp, "persist", "spool", "in", ref.Name)

	hostRoot := t.TempDir()
	t.Setenv("HOME", hostRoot)
	hostPath, err := m.Resolve(ref)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(hostRoot, tail), hostPath,
		"host view must be $HOME-relative under the persist dir")

	containerRoot := filepath.Join(t.TempDir(), "container-home")
	t.Setenv("HOME", containerRoot)
	containerPath, err := m.Resolve(ref)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(containerRoot, tail), containerPath,
		"container view must be the same home-relative layout under the container home")

	require.NotEqual(t, hostPath, containerPath, "the two views must differ, or this proves nothing")
	require.True(t, strings.HasSuffix(hostPath, tail) && strings.HasSuffix(containerPath, tail),
		"both views must end in the identical home-relative tail: host=%q container=%q tail=%q",
		hostPath, containerPath, tail)
}

// TestHomeMapper_RefOfRoundTrip pins the inverse. Wire ref and file layout are
// two things that must agree; a skew between them degrades to "the sweep
// still delivers, slower", i.e. silently.
func TestHomeMapper_RefOfRoundTrip(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()

	for _, dir := range []Dir{DirIn, DirOut, DirInConsumed, DirOutConsumed, DirInWithdrawn} {
		ref := Ref{Harp: testHarp, Dir: dir, Name: "00000000000000000042.00000007.coord.md"}
		path, err := m.Resolve(ref)
		require.NoError(t, err)
		require.NotEmpty(t, path)

		back, err := m.RefOf(path)
		require.NoError(t, err, "RefOf(%q)", path)
		require.Equal(t, ref, back, "ref must survive Resolve->RefOf for %s", dir)

		again, err := m.Resolve(back)
		require.NoError(t, err)
		require.Equal(t, path, again, "path must survive RefOf->Resolve for %s", dir)
	}
}

func TestHomeMapper_RefOfRejectsForeignPaths(t *testing.T) {
	home := hostHome(t)
	m := NewHomeMapper()

	bad := map[string]string{
		"outside the sessions root": filepath.Join(home, "elsewhere", "x.md"),
		"relative path":             filepath.Join("sessions", testHarp, "persist", "spool", "in", "x.md"),
		"not under persist/spool":   filepath.Join(home, ".ctxloom", "sessions", testHarp, "ephemeral", "in", "x.md"),
		"unknown spool dir":         filepath.Join(home, ".ctxloom", "sessions", testHarp, "persist", "spool", "elsewhere", "x.md"),
		"too shallow":               filepath.Join(home, ".ctxloom", "sessions", testHarp, "persist", "spool", "x.md"),
	}
	for what, path := range bad {
		t.Run(what, func(t *testing.T) {
			_, err := m.RefOf(path)
			require.Error(t, err, "RefOf must refuse a path %s", what)
		})
	}
}

func TestDir_ClosedEnum(t *testing.T) {
	for _, d := range []Dir{DirIn, DirOut, DirInConsumed, DirOutConsumed, DirInWithdrawn} {
		require.True(t, d.Valid(), "%s must be in the closed set", d)
		require.NoError(t, d.Validate())
	}
	for _, d := range []Dir{"", "IN", "in/", "quarantine", "in/consumed/deeper", "tmp"} {
		require.False(t, Dir(d).Valid(), "%q must not be in the closed set", d)
		require.Error(t, Dir(d).Validate())
	}
}

func TestDir_TerminalDirs(t *testing.T) {
	consumed, err := DirIn.Consumed()
	require.NoError(t, err)
	require.Equal(t, DirInConsumed, consumed)

	consumed, err = DirOut.Consumed()
	require.NoError(t, err)
	require.Equal(t, DirOutConsumed, consumed)

	withdrawn, err := DirIn.Withdrawn()
	require.NoError(t, err)
	require.Equal(t, DirInWithdrawn, withdrawn)

	// out/ has no withdrawn state, and the terminal dirs are not themselves
	// consumable: those are caller bugs, and must say so.
	_, err = DirOut.Withdrawn()
	require.Error(t, err)
	for _, d := range []Dir{DirInConsumed, DirOutConsumed, DirInWithdrawn} {
		_, err := d.Consumed()
		require.Error(t, err, "%s must not be consumable", d)
	}
}

func TestEnsureDirs_CreatesTheWholeLayout(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	require.NoError(t, EnsureDirs(m, testHarp))

	root, err := Root(m, testHarp)
	require.NoError(t, err)
	for _, rel := range []string{"in", "out", "in/consumed", "out/consumed", "in/withdrawn", "tmp"} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		st, err := statDir(path)
		require.NoError(t, err, "EnsureDirs must create %s", rel)
		require.True(t, st, "%s must be a directory", rel)
	}
	// Idempotent: a second call on both sides of a mount is normal.
	require.NoError(t, EnsureDirs(m, testHarp))
}

func TestEnsureDirs_RefusesInvalidHarp(t *testing.T) {
	hostHome(t)
	require.Error(t, EnsureDirs(NewHomeMapper(), "../escape"))
}
