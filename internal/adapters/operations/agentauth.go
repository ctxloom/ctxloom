package operations

import (
	"os"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// runAuth is one run's claim on a credential: which engine, in which
// DECLARED auth mode ("" undeclared). Where it runs and its engine home are
// deliberately absent: auth is purely per agent, so a container run and a
// run on the human's real home (engine_home: host) authenticate in their
// declared mode like any other, and the environment that prepares the run
// makes the credentials true where the engine executes.
type runAuth struct {
	Backend string
	// Declared is the agent's auth mode as written; checkAgentAuth parses it.
	Declared string
}

// checkAgentAuth is engine.CheckAuth over the named engine: the ONE check of
// an auth selection, run by `agent create/edit` (validateAgentAuth) and by
// every launch (resolveRunAuth); config load runs engine.CheckAuth itself.
// A nil Auth with no error is an engine that authenticates on its own.
func checkAgentAuth(reg engine.Registry, backend, declared string) (engine.Auth, engine.AuthMode, error) {
	kind, ok := reg.Lookup(engine.Name(backend))
	if !ok {
		return nil, "", nil
	}
	mode, err := engine.CheckAuth(kind.Root().Name, kind.Home().Auth, declared)
	if err != nil {
		return nil, "", err
	}
	a, ok := kind.Home().Auth.Get()
	if !ok {
		return nil, "", nil
	}
	return a, mode, nil
}

// resolveRunAuth is what a run authenticates with: the engine's own
// Auth.Credentials for the agent's mode, read from the launching env, which
// sets the mode's credential, unsets the others and declares the stores it
// shares. Engine-blind and runtime-blind: which vars those are is the
// engine's answer, and how each is made true is the environment's. Zero for
// an engine that declares no auth. A credential the launching env does not
// hold is the engine's own refusal, naming how the human supplies one:
// ctxloom never prompts for, mints or stores a credential.
func resolveRunAuth(reg engine.Registry, in runAuth) (engine.Credentials, error) {
	a, mode, err := checkAgentAuth(reg, in.Backend, in.Declared)
	if err != nil || a == nil {
		return engine.Credentials{}, err
	}
	return a.Credentials(mode, os.LookupEnv)
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
