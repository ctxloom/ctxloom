package launch_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// Who runs decides the auth mode, never the binding: the human's own
// session (depth 0, not a one-shot) runs in the configured top-level `auth:`;
// every run ctxloom spawns -- a delegated child, a one-shot, an internal
// one-shot -- runs in the token, whatever the human configured for
// themselves.
func TestResolve_Auth_OnlyTheHumansOwnSessionTakesTheConfiguredMode(t *testing.T) {
	child := func(id sessions.Identity) sessions.Identity { id.Depth = 1; return id }
	oneShot := func(id sessions.Identity) sessions.Identity { id.OneShot = true; id.Leaf = true; return id }
	same := func(id sessions.Identity) sessions.Identity { return id }
	cases := []struct {
		name     string
		session  engine.AuthMode
		identity func(sessions.Identity) sessions.Identity
		internal bool
		want     engine.AuthMode
	}{
		{"the human's session, login configured", engine.AuthLogin, same, false, engine.AuthLogin},
		{"the human's session, nothing configured", "", same, false, engine.AuthToken},
		{"a delegated child, login configured", engine.AuthLogin, child, false, engine.AuthToken},
		{"a one-shot, login configured", engine.AuthLogin, oneShot, false, engine.AuthToken},
		{"an internal one-shot, login configured", engine.AuthLogin, oneShot, true, engine.AuthToken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := launchtest.Deps(t, launchtest.WithAgent("dev"), launchtest.SessionAuth(tc.session))
			src := launch.Source{Identity: tc.identity(env.Identity), Agent: "dev", Mode: engine.Structured, Permission: "bypass", Prompt: "x", WorkDir: env.Project}
			if tc.internal {
				src = launch.Source{Identity: tc.identity(env.Identity), Internal: true, Mode: engine.Structured, Permission: "bypass", Prompt: "x", WorkDir: env.Project}
			}
			l, err := launch.Resolve(context.Background(), env.Deps, src)
			require.NoError(t, err)
			t.Cleanup(func() { _ = launch.Discard(context.Background(), l) })
			require.Equal(t, tc.want, env.LastCellRequest().Auth)
		})
	}
}
