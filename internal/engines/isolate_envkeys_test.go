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
// every shipped engine declares — its home vars and every var its auth modes
// read, set, unset or point a store at — and fails on any EnvKeys misses. Such a var leaks
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

// engineDeclaredEnv is every var the engine's home relocates plus, for each
// auth mode it declares, every var its Credentials consults in the launching
// env, sets, unsets, or points a shared store at. Auth never names its vars
// directly, so they are observed through the shell it is handed.
func engineDeclaredEnv(t *testing.T, e engine.Engine) []string {
	t.Helper()
	var out []string
	home := e.Home()
	for _, v := range home.Vars {
		out = append(out, v.Name)
	}
	a, ok := home.Auth.Get()
	if !ok {
		return out
	}
	for _, mode := range a.Modes() {
		shell := func(name string) (string, bool) {
			out = append(out, name)
			return "", false
		}
		creds, err := a.Credentials(mode, shell, storedCredential{})
		if err != nil && !errors.Is(err, engine.ErrNoCredential) {
			require.NoError(t, err, "mode %s", mode)
		}
		for k := range creds.Env {
			out = append(out, k)
		}
		out = append(out, creds.Unset...)
		for _, st := range creds.Stores {
			if st.Var != "" {
				out = append(out, st.Var)
			}
		}
	}
	return out
}

// storedCredential holds a credential for every mode, so Credentials takes
// each mode's stored path and names the var it would set.
type storedCredential struct{}

func (storedCredential) Read(engine.AuthMode) ([]byte, error) {
	return []byte("fixture-credential"), nil
}
