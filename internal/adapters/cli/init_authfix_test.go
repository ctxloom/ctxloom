package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/testsupport/enginefixture"
)

// hintAuth is an engine auth with nothing to say beyond existing.
type hintAuth struct{}

func (hintAuth) Modes() []engine.AuthMode { return []engine.AuthMode{engine.AuthToken} }
func (hintAuth) Credentials(engine.AuthMode, func(string) (string, bool)) (engine.Credentials, error) {
	return engine.Credentials{}, nil
}

// An engine that declares auth gets the fix that works for any engine:
// export the credential its mode reads, which `auth status` names — never a
// ctxloom command that takes one, since ctxloom stores none.
func TestEngineAuthFixHint_PointsAtTheEnvironmentForAnEngineWithAuth(t *testing.T) {
	const name = "fixture-authfix"
	reg := enginefixture.Install(t, enginefixture.Kind(name, mock.WithHome(engine.HomeSpec{
		Vars: []engine.HomeVar{{Name: "FIXTURE_HOME", Subdir: "fixture"}},
		Auth: engine.Provide[engine.Auth](hintAuth{}),
	})))
	hint := engineAuthFixHint(reg, name)
	assert.Contains(t, hint, "ctxloom auth status")
	assert.Contains(t, hint, "export")
	assert.NotContains(t, hint, "auth mint")
	assert.NotContains(t, hint, "auth set")
	assert.NotContains(t, hint, "authenticate the engine", "a declared auth never gets the generic fix")
}

// An engine with no auth has no credential command to name; it gets the
// generic fix.
func TestEngineAuthFixHint_EngineWithNoAuthGetsTheGenericFix(t *testing.T) {
	reg := engines.Registry()
	for _, name := range operations.EngineNames(reg) {
		kind, _ := reg.Lookup(engine.Name(name))
		if _, ok := kind.Home().Auth.Get(); ok {
			continue
		}
		assert.Contains(t, engineAuthFixHint(reg, name), "authenticate the engine", name)
	}
	assert.Contains(t, engineAuthFixHint(reg, "never-registered"), "authenticate the engine")
}
