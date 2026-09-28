package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/testsupport/enginefixture"
)

// hintAuth is an engine auth with nothing to say beyond existing.
type hintAuth struct{}

func (hintAuth) Modes() []engine.AuthMode { return []engine.AuthMode{engine.AuthToken} }
func (hintAuth) Credentials(engine.AuthMode, func(string) (string, bool), engine.CredentialReader) (engine.Credentials, error) {
	return engine.Credentials{}, nil
}
func (hintAuth) Mint(context.Context, engine.AuthMode, engine.Terminal) ([]byte, error) {
	return nil, engine.ErrMintUnsupported
}

// An engine that declares auth gets the fix that works for any engine: the
// commands that mint or store its credential, named for THAT engine.
func TestEngineAuthFixHint_NamesTheCredentialCommandsForTheEngine(t *testing.T) {
	const name = "fixture-authfix"
	enginefixture.Install(t, enginefixture.Kind(name, mock.WithHome(engine.HomeSpec{
		Vars: []engine.HomeVar{{Name: "FIXTURE_HOME", Subdir: "fixture"}},
		Auth: engine.Provide[engine.Auth](hintAuth{}),
	})))
	hint := engineAuthFixHint(name)
	assert.Contains(t, hint, "ctxloom auth mint --engine "+name)
	assert.Contains(t, hint, "ctxloom auth set --engine "+name)
	assert.NotContains(t, hint, "authenticate the engine", "a declared auth never gets the generic fix")
}

// An engine with no auth has no credential command to name; it gets the
// generic fix.
func TestEngineAuthFixHint_EngineWithNoAuthGetsTheGenericFix(t *testing.T) {
	for _, name := range operations.EngineNames(engines.Registry()) {
		if _, ok := isolation.AuthFor(name); ok {
			continue
		}
		assert.Contains(t, engineAuthFixHint(name), "authenticate the engine", name)
	}
	assert.Contains(t, engineAuthFixHint("never-registered"), "authenticate the engine")
}
