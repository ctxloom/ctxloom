package operations

import (
	"os"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// runAuth is one run's claim on a credential: which engine, in which mode.
// The mode is already settled by who runs (launch.RunAuth): the token for
// every run ctxloom spawns, the configured `auth:` for the human's own
// session. Where it runs and its engine home are deliberately absent: the
// environment that prepares the run makes the credentials true where the
// engine executes.
type runAuth struct {
	Backend string
	Mode    engine.AuthMode
}

// resolveRunAuth is what a run authenticates with: the engine's own
// Auth.Credentials for the mode, read from the launching env, which sets the
// mode's credential, unsets the others and declares the stores it shares.
// Engine-blind and runtime-blind: which vars those are is the engine's
// answer, and how each is made true is the environment's. Zero for an engine
// that declares no auth (it authenticates on its own). A credential the
// launching env does not hold is the engine's own refusal, naming how the
// human supplies one: ctxloom never prompts for, mints or stores a
// credential.
func resolveRunAuth(reg engine.Registry, in runAuth) (engine.Credentials, error) {
	kind, ok := reg.Lookup(engine.Name(in.Backend))
	if !ok {
		return engine.Credentials{}, nil
	}
	a, ok := kind.Home().Auth.Get()
	if !ok {
		return engine.Credentials{}, nil
	}
	// Every engine that declares auth offers the token (validateAuth), so
	// only the human's own session can land here, on a login the engine
	// lacks.
	if !engine.SupportsMode(a, in.Mode) {
		return engine.Credentials{}, report.Errorf("set `auth: "+string(engine.AuthToken)+"` in your config",
			"%s: auth %s: %w", in.Backend, in.Mode, engine.ErrAuthModeUnsupported)
	}
	creds, err := a.Credentials(in.Mode, os.LookupEnv)
	if err != nil {
		return engine.Credentials{}, err
	}
	creds.Mode = in.Mode
	return creds, nil
}

// previewRunAuth is resolveRunAuth for a --dry-run: the same resolution and
// the same refusal, with every credential VALUE redacted — a preview shows
// the variables a run sets and the stores it shares, never a secret.
func previewRunAuth(reg engine.Registry, in runAuth) (engine.Credentials, error) {
	creds, err := resolveRunAuth(reg, in)
	if err != nil {
		return engine.Credentials{}, err
	}
	for k := range creds.Env {
		creds.Env[k] = redactedCredential
	}
	return creds, nil
}

// redactedCredential stands in for a credential value in a preview.
const redactedCredential = "<redacted>"
