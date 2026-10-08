package opencode

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// APIKeyEnv is the one variable an opencode label's endpoint key is read
// from. The key reaches opencode only as the provider config's
// {env:CTXLOOM_OPENCODE_API_KEY} reference, never on disk or argv. It is
// fixed per engine: Auth never sees the label.
const APIKeyEnv = "CTXLOOM_OPENCODE_API_KEY"

const keyRemedy = "export " + APIKeyEnv + " with the endpoint's API key (one line of the model server's --api-key-file)"

// opencodeAuth authenticates every run with the one endpoint key.
type opencodeAuth struct{}

func (opencodeAuth) Modes() []engine.AuthMode { return []engine.AuthMode{engine.AuthToken} }

// Credentials sets the key from the launching env; unset, the run is refused
// with the remedy.
func (opencodeAuth) Credentials(mode engine.AuthMode, shell func(string) (string, bool)) (engine.Credentials, error) {
	if mode != engine.AuthToken {
		return engine.Credentials{}, fmt.Errorf("%s: %w: %q", EngineName, engine.ErrAuthModeUnsupported, mode)
	}
	key, ok := shell(APIKeyEnv)
	if !ok || key == "" {
		return engine.Credentials{}, report.Errorf(keyRemedy, "%s %s: %s is not exported: %w", EngineName, mode, APIKeyEnv, engine.ErrNoCredential)
	}
	return engine.Credentials{Mode: mode, Env: map[string]string{APIKeyEnv: key}}, nil
}
