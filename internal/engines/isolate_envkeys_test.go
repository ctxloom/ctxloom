package engines

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestEnvKeys_CoverEveryEngineDeclaredVar makes testsupport.EnvKeys' engine
// entries a CHECKED binding. Isolate cannot derive them itself: its body lives
// in the shared tree, which never imports an engine, and testsupport cannot
// either (claude's in-package tests import testsupport, so the edge would be a
// cycle). So the vars are listed there by hand, and this test derives the set
// every shipped engine declares — its home, token, shared-login and container
// passthrough vars — and fails on any that EnvKeys misses. Such a var leaks
// from the developer's live session into every isolated test: CLAUDE_CONFIG_DIR
// did, and a test passed locally only because of it.
func TestEnvKeys_CoverEveryEngineDeclaredVar(t *testing.T) {
	reg, err := Build()
	require.NoError(t, err)

	known := make(map[string]bool, len(testsupport.EnvKeys))
	for _, k := range testsupport.EnvKeys {
		known[k] = true
	}
	for _, name := range reg.Names(nil) {
		e, _ := reg.Lookup(name)
		seen := map[string]bool{}
		for _, v := range engineDeclaredEnv(t, e) {
			if !known[v] && !seen[v] {
				seen[v] = true
				t.Errorf("engine %s declares %s but testsupport.EnvKeys does not list it, so Isolate leaks it from the host session", name, v)
			}
		}
	}
}

func engineDeclaredEnv(t *testing.T, e engine.Engine) []string {
	t.Helper()
	var out []string
	home := e.Home()
	for _, v := range home.Vars {
		out = append(out, v.Name)
	}
	if a, ok := home.Auth.Get(); ok {
		out = append(append(out, a.TokenVar), a.EnvTriggers...)
	}
	if s, ok := home.SharedLogin.Get(); ok {
		out = append(out, s.Var, s.FallbackVar)
	}
	c, err := e.Container()
	var unsupported engine.ErrUnsupported
	if errors.As(err, &unsupported) {
		return out
	}
	require.NoError(t, err)
	if a, ok := c.Auth.Get(); ok {
		out = append(append(out, a.EnvTriggers...), a.EnvPassthrough...)
	}
	return out
}
