package launch

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// fits reports that a socket leaf of the longest length the contract allows
// (engine.SocketLeafMax) binds under root within the socket budget.
func fits(t *testing.T, root string) {
	t.Helper()
	sock := filepath.Join(root, strings.Repeat("s", engine.SocketLeafMax))
	assert.LessOrEqual(t, len(sock), engine.SocketPathBudget, "%s (%d bytes) overflows the socket budget", sock, len(sock))
}

// A session home nested arbitrarily deep still yields a socket root a socket
// binds under: the root is a short private base and a fixed-length hash of
// the home, never the home itself. Every tier is forced: the user's runtime
// dir, the temp dir when there is none, and /tmp when both are too long to
// leave room.
func TestSocketRoot_ADeepSessionHomeStillFitsTheSocketBudget(t *testing.T) {
	deep := "/" + strings.Repeat("a-deep-session-home-segment/", 20)
	long := "/" + strings.Repeat("x", engine.SocketPathBudget)
	cases := []struct {
		name, xdg, tmp, wantBase string
	}{
		{"the runtime dir", "/run/user/1000", "/var/tmp", "/run/user/1000"},
		{"the temp dir without a runtime dir", "", "/var/tmp", "/var/tmp"},
		{"the temp dir when the runtime dir is too long", long, "/var/tmp", "/var/tmp"},
		{"/tmp when both are too long", long, long, "/tmp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(k string) string {
				if k == "XDG_RUNTIME_DIR" {
					return tc.xdg
				}
				return ""
			}
			root := socketRoot(getenv, func() string { return tc.tmp }, deep)
			assert.Equal(t, filepath.FromSlash(tc.wantBase), filepath.Dir(root))
			fits(t, root)
		})
	}
}

// The root is the session home's own: the same home always names the same
// root (the engine and the runner that fires its wake agree), another home
// names another, and no session home names none.
func TestSocketRoot_IsTheSessionHomesOwn(t *testing.T) {
	getenv := func(string) string { return "" }
	tmp := func() string { return "/var/tmp" }
	a := socketRoot(getenv, tmp, "/s/one/home/oc")
	assert.Equal(t, a, socketRoot(getenv, tmp, "/s/one/home/oc"))
	assert.NotEqual(t, a, socketRoot(getenv, tmp, "/s/two/home/oc"))
	assert.True(t, strings.HasPrefix(filepath.Base(a), "ctxloom-"), a)
	assert.Empty(t, socketRoot(getenv, tmp, ""))
}
