package operations

import (
	"errors"
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

// AgentTokenMissing is the engine's own refusal when the token every agent
// it runs authenticates with is not exported in lookup's environment, and nil
// otherwise — including for an engine that declares no auth, or an unknown
// one. It is init's question; its refusal is the engine's own (the one
// `ctxloom auth` lists, and CheckRunCredential returns for a token run), so a
// human reads the same fix wherever they meet it.
func AgentTokenMissing(reg engine.Registry, backend string, lookup func(string) (string, bool)) error {
	kind, ok := reg.Lookup(engine.Name(backend))
	if !ok {
		return nil
	}
	a, ok := kind.Home().Auth.Get()
	if !ok {
		return nil
	}
	if _, err := a.Credentials(engine.AuthToken, lookup); errors.Is(err, engine.ErrNoCredential) {
		return err
	}
	return nil
}

// CheckRunCredential is resolveRunAuth's refusal for a run of backend in
// mode, without building anything: nil when the credential that run will
// authenticate with is there, else the engine's own refusal and fix — a token
// not exported, a mode the engine lacks. It is what a real run hands
// launch.Deps.CheckCredential, so a run that cannot authenticate is refused
// before anything is established for it.
func CheckRunCredential(reg engine.Registry, backend string, mode engine.AuthMode) error {
	_, err := resolveRunAuth(reg, runAuth{Backend: backend, Mode: mode})
	return err
}
