package opencode

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// APIKeyEnv is the one variable an opencode label's endpoint key is read
// from. The key reaches opencode only as the provider config's
// {env:CTXLOOM_OPENCODE_API_KEY} reference, never on disk or argv. It is
// fixed per engine: Auth never sees the label.
const APIKeyEnv = "CTXLOOM_OPENCODE_API_KEY"

// opencodeAuth authenticates a run with the endpoint key when the launching
// env exports one, and keyless otherwise. The key is OPTIONAL because a label
// without provider.base_url names one of opencode's built-in providers, whose
// free hosted models need no credential; only a label with an endpoint needs
// it, and Auth, which never sees the label, cannot tell the two apart.
type opencodeAuth struct{}

func (opencodeAuth) Modes() []engine.AuthMode { return []engine.AuthMode{engine.AuthToken} }

// Credentials sets the key from the launching env when it is exported;
// unset or empty, the run is keyless.
func (opencodeAuth) Credentials(mode engine.AuthMode, shell func(string) (string, bool)) (engine.Credentials, error) {
	if mode != engine.AuthToken {
		return engine.Credentials{}, fmt.Errorf("%s: %w: %q", EngineName, engine.ErrAuthModeUnsupported, mode)
	}
	key, ok := shell(APIKeyEnv)
	if !ok || key == "" {
		return engine.Credentials{Mode: mode}, nil
	}
	return engine.Credentials{Mode: mode, Env: map[string]string{APIKeyEnv: key}}, nil
}
