package isolation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ciLayer is a GitHub Actions job container's view, as its daemon reports
// it: the workspace and the runner's temp under /__w, a volume, and a nested
// bind that shadows part of the outer one.
var ciLayer = Layer{mounts: []LayerMount{
	{Host: "/home/runner/work", View: "/__w"},
	{Host: "/home/runner/work/_temp/_github_home", View: "/github/home"},
	{Host: "/var/lib/docker/volumes/ctxhome/_data", View: "/root/.ctxloom"},
	{Host: "/srv/override", View: "/__w/ctxloom/ctxloom/.cache"},
}}

func TestLayer_ReverseAndMapTable(t *testing.T) {
	for name, tc := range map[string]struct {
		view, host string
	}{
		"the mount root itself":               {view: "/__w", host: "/home/runner/work"},
		"under a mount":                       {view: "/__w/ctxloom/ctxloom", host: "/home/runner/work/ctxloom/ctxloom"},
		"a volume":                            {view: "/root/.ctxloom/sessions", host: "/var/lib/docker/volumes/ctxhome/_data/sessions"},
		"the nested mount wins over its host": {view: "/__w/ctxloom/ctxloom/.cache/x", host: "/srv/override/x"},
	} {
		t.Run(name, func(t *testing.T) {
			host, err := ciLayer.Reverse(tc.view)
			require.NoError(t, err)
			assert.Equal(t, tc.host, host)
			view, err := ciLayer.Map(tc.host)
			require.NoError(t, err)
			assert.Equal(t, tc.view, view)
		})
	}
}

// A path no mount covers has no name on the other side: refused, never
// passed through as if the layers shared it.
func TestLayer_UncoveredIsErrUnmapped(t *testing.T) {
	for _, view := range []string{"/tmp/x", "/__weird", "/", "/github"} {
		_, err := ciLayer.Reverse(view)
		require.ErrorIs(t, err, ErrUnmapped, view)
	}
	for _, host := range []string{"/home/runner/workspace", "/srv", "/var/lib/docker/volumes/other/_data"} {
		_, err := ciLayer.Map(host)
		require.ErrorIs(t, err, ErrUnmapped, host)
	}
}

// The host layer shares the daemon's namespace: both directions are
// identity, and nothing is ever unmapped.
func TestHostLayer_IsIdentity(t *testing.T) {
	for _, p := range []string{"/a/b", "/", `C:\Users\ben`} {
		got, err := HostLayer().Reverse(p)
		require.NoError(t, err)
		assert.Equal(t, p, got)
		got, err = HostLayer().Map(p)
		require.NoError(t, err)
		assert.Equal(t, p, got)
	}
}

// primaryLayer is the controller's own view: nil self is the host layer, and
// a self's daemon-reported mounts are its mounts.
func TestPrimaryLayer_FromSelf(t *testing.T) {
	assert.Equal(t, HostLayer(), primaryLayer(nil))
	l := primaryLayer(&selfContainer{mounts: []selfMount{{source: "/home/runner/work", destination: "/__w"}}})
	host, err := l.Reverse("/__w/x")
	require.NoError(t, err)
	assert.Equal(t, "/home/runner/work/x", host)
}

// The PRIMARY layer's views are this process's own paths, so Reverse
// resolves a symlink before translating: the daemon must bind the file the
// link names, not a link that dangles on its side. A child layer's views are
// not ours to resolve.
func TestLayer_PrimaryReverseResolvesSymlinks(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(real, link))

	primary := primaryLayer(&selfContainer{mounts: []selfMount{{source: "/daemon/side", destination: real}}})
	got, err := primary.Reverse(link)
	require.NoError(t, err)
	assert.Equal(t, "/daemon/side", got)

	child := Layer{mounts: []LayerMount{{Host: "/daemon/side", View: real}}}
	_, err = child.Reverse(link)
	require.ErrorIs(t, err, ErrUnmapped, "a child view is never resolved against the controller's filesystem")
}

// threePaths is the owner's worked case: host dir H, the controller sees it
// at /ctl/work, the child at /agent/work.
func threePaths(t *testing.T) Crossing {
	t.Helper()
	primary := Layer{mounts: []LayerMount{{Host: "/srv/H", View: "/ctl/work"}}}
	child, err := childLayer(primary, []mount{{Host: "/ctl/work", Container: "/agent/work"}})
	require.NoError(t, err)
	return Crossing{Primary: primary, Child: child}
}

// The child layer is DERIVED from the mount plan: each mount's source is
// the primary's reversal of its controller path, so a daemon bind source is
// always a host path.
func TestChildLayer_SourcesAreHostPaths(t *testing.T) {
	c := threePaths(t)
	require.Len(t, c.Child.mounts, 1)
	assert.Equal(t, LayerMount{Host: "/srv/H", View: "/agent/work"}, c.Child.mounts[0])

	_, err := childLayer(c.Primary, []mount{{Host: "/not/mounted", Container: "/x"}})
	require.ErrorIs(t, err, ErrUnmapped, "a mount the daemon has no name for is refused while deriving the layer")
}

func TestCrossing_ToChildThenFromChild(t *testing.T) {
	c := threePaths(t)
	child, host, err := c.ToChild("/ctl/work/out.txt")
	require.NoError(t, err)
	assert.Equal(t, "/agent/work/out.txt", child)
	assert.Equal(t, "/srv/H/out.txt", host)

	ctl, err := c.FromChild("/agent/work/out.txt")
	require.NoError(t, err)
	assert.Equal(t, "/ctl/work/out.txt", ctl)
}

// Round trip: every controller path a child mount covers comes back to
// itself, and a child-private path has no controller name.
func TestCrossing_RoundTripProperty(t *testing.T) {
	primary := ciLayer
	child, err := childLayer(primary, []mount{
		{Host: "/__w/ctxloom/ctxloom", Container: "/agent/work"},
		{Host: "/root/.ctxloom/sessions/h", Container: "/home/ctxloom/.ctxloom/sessions/h"},
		{Host: "/__w/ctxloom/ctxloom/.cache", Container: "/agent/cache"},
	})
	require.NoError(t, err)
	c := Crossing{Primary: primary, Child: child}
	for _, p := range []string{
		"/__w/ctxloom/ctxloom",
		"/__w/ctxloom/ctxloom/a/b.go",
		"/root/.ctxloom/sessions/h/transcripts/t.jsonl",
		"/__w/ctxloom/ctxloom/.cache/k",
	} {
		got, _, err := c.ToChild(p)
		require.NoError(t, err, p)
		back, err := c.FromChild(got)
		require.NoError(t, err, got)
		assert.Equal(t, p, back, "round trip through %s", got)
	}
	for _, private := range []string{"/tmp/scratch", "/home/ctxloom/.claude.json", "/agent"} {
		_, err := c.FromChild(private)
		require.ErrorIs(t, err, ErrUnmapped, private)
	}
	_, _, err = c.ToChild("/tmp/not-mounted")
	require.ErrorIs(t, err, ErrUnmapped)
}
