package operations

import (
	"errors"
	"os"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
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
	return resolveCredentials(reg, in, os.LookupEnv)
}

// EnvLookup reads one variable of the environment a credential is resolved
// against: os.LookupEnv for the process, a map in a test.
type EnvLookup = func(string) (string, bool)

// resolveCredentials is resolveRunAuth over lookup: the single resolution
// every credential question (a run, doctor, init) is answered by.
func resolveCredentials(reg engine.Registry, in runAuth, lookup EnvLookup) (engine.Credentials, error) {
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
	creds, err := a.Credentials(in.Mode, lookup)
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

// CredentialStatus is the one answer to "can backend authenticate in mode
// from lookup's environment": nil when it can — including an engine that
// declares no auth, or an unknown one — else the engine's own refusal and
// fix: a credential not exported, a login store not on this host, a mode the
// engine lacks. A run (CheckRunCredential), doctor (ConfiguredCredentialChecks)
// and init (AgentCredentialMissing) all ask it, so they cannot disagree about
// whether a credential is there or how to supply one.
func CredentialStatus(reg engine.Registry, backend string, mode engine.AuthMode, lookup EnvLookup) error {
	creds, err := resolveCredentials(reg, runAuth{Backend: backend, Mode: mode}, lookup)
	if err != nil {
		return err
	}
	return isolation.CheckStores(backend, creds.Stores)
}

// AgentCredentialMissing is CredentialStatus narrowed to init's question: the
// engine's own refusal when the credential mode needs is not exported in
// lookup's environment (engine.ErrNoCredential), nil otherwise. mode is the
// one the config declares, never an assumed token: an engine asked for a
// login must not be answered with the token's variable.
func AgentCredentialMissing(reg engine.Registry, backend string, mode engine.AuthMode, lookup EnvLookup) error {
	if err := CredentialStatus(reg, backend, mode, lookup); errors.Is(err, engine.ErrNoCredential) {
		return err
	}
	return nil
}

// CheckRunCredential is CredentialStatus for a run, read from the process
// environment, without building anything. It is what a real run hands
// launch.Deps.CheckCredential, so a run that cannot authenticate is refused
// before anything is established for it.
func CheckRunCredential(reg engine.Registry, backend string, mode engine.AuthMode) error {
	return CredentialStatus(reg, backend, mode, os.LookupEnv)
}

// CredentialCheck is one distinct (backend, mode) the configuration
// authenticates with, and CredentialStatus's answer for it.
type CredentialCheck struct {
	Backend string
	Mode    engine.AuthMode
	// Err is nil when the credential is there.
	Err error
}

// ConfiguredCredentialChecks asks CredentialStatus once per distinct
// (backend, mode) the configuration authenticates with: every backend a
// configured label or agent resolves to, in the mode the human's session is
// configured to use (config.SessionAuth). Sorted by backend. nil cfg yields
// none.
func ConfiguredCredentialChecks(reg engine.Registry, cfg *config.Config, lookup EnvLookup) []CredentialCheck {
	if cfg == nil {
		return nil
	}
	backends := map[string]bool{}
	for _, label := range cfg.GetLLMLabels() {
		if b, _ := cfg.ResolveLLM(label); b != "" {
			backends[b] = true
		}
	}
	for _, a := range cfg.GetConfiguredAgents() {
		if b, _ := ResolveBackend(reg, cfg, a.LLM); b != "" {
			backends[b] = true
		}
	}
	mode := cfg.SessionAuth()
	out := make([]CredentialCheck, 0, len(backends))
	for _, b := range collections.SortedKeys(backends) {
		out = append(out, CredentialCheck{Backend: b, Mode: mode, Err: CredentialStatus(reg, b, mode, lookup)})
	}
	return out
}
