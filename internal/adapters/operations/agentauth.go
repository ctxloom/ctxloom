package operations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"golang.org/x/term"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// errLoginNotInContainer: an agent declaring the human's login runs in a
// container, where that login is not reachable. The container half of the
// login design (the human's credential storage mounted at the container's
// $HOME) is not built, and a container that cannot reach the login would
// start logged out rather than fail.
var errLoginNotInContainer = errors.New("auth login shares the human's own login, which no container can reach yet")

// runAuth is one run's claim on a credential: which engine, in which auth
// mode, and where it runs.
type runAuth struct {
	Backend string
	Mode    engine.AuthMode
	// OnHost is whether the engine runs on the host rather than in a
	// container.
	OnHost bool
	// HomeMode is the run's effective engine-home policy. A host run on the
	// human's real home (agents.HomeModeHost) authenticates as the human's
	// own engine does, in place, and ctxloom resolves nothing for it.
	HomeMode agents.HomeMode
}

// attendedTerminal is the human's terminal when this process has one to mint
// at: stdin and stderr both a terminal. The mint's own output goes to stderr
// so this process's stdout stays the command's output. A package var so a
// test hands in a fake terminal, or takes it away.
var attendedTerminal = func() (engine.Terminal, bool) {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stderr.Fd())) {
		return engine.Terminal{}, false
	}
	return engine.Terminal{In: os.Stdin, Out: os.Stderr, Err: os.Stderr}, true
}

// mintMu serializes minting in this process: a fan-out whose members all
// lack the same credential mints it once, and the rest read what was stored.
var mintMu sync.Mutex

// resolveRunAuth is the env a run authenticates with: the engine's own
// Auth.LaunchEnv for the agent's mode, which sets the mode's credential and
// blanks the others. Engine-blind: which vars those are is the engine's
// answer. nil for an engine that declares no auth and for a host run on the
// human's real home.
//
// A credential the mode needs that is neither exported nor stored is MINTED
// when the human is at a terminal (Auth.Mint, then stored owner-only), and
// refused with the remedy when they are not: an unattended run never starts
// logged out and never waits on a prompt nobody can see.
func resolveRunAuth(ctx context.Context, reg engine.Registry, in runAuth) (map[string]string, error) {
	kind, ok := reg.Lookup(engine.Name(in.Backend))
	if !ok {
		return nil, nil
	}
	a, ok := kind.Home().Auth.Get()
	if !ok || (in.OnHost && in.HomeMode == agents.HomeModeHost) {
		return nil, nil
	}
	if !engine.SupportsMode(a, in.Mode) {
		return nil, fmt.Errorf("%s auth %s: %w", in.Backend, in.Mode, engine.ErrAuthModeUnsupported)
	}
	if in.Mode == engine.AuthLogin && !in.OnHost {
		return nil, fmt.Errorf("%s: %w; declare auth: token or api-key for a container agent", in.Backend, errLoginNotInContainer)
	}
	store := isolation.StoredCredentials(in.Backend)
	env, err := a.LaunchEnv(in.Mode, os.LookupEnv, store)
	if !errors.Is(err, engine.ErrNoCredential) {
		return env, err
	}
	return mintAndLaunchEnv(ctx, a, in)
}

// mintAndLaunchEnv mints the missing credential at the human's terminal,
// stores it, and resolves the env again; unattended, it refuses with the
// commands that would provide one.
func mintAndLaunchEnv(ctx context.Context, a engine.Auth, in runAuth) (map[string]string, error) {
	mintMu.Lock()
	defer mintMu.Unlock()
	store := isolation.StoredCredentials(in.Backend)
	env, err := a.LaunchEnv(in.Mode, os.LookupEnv, store)
	if !errors.Is(err, engine.ErrNoCredential) {
		return env, err
	}
	remedy := credentialRemedy(in.Backend, in.Mode)
	t, ok := attendedTerminal()
	if !ok {
		return nil, fmt.Errorf("%w: %s auth %s needs a stored credential and this run has no terminal to mint one at; %s", engine.ErrNoCredential, in.Backend, in.Mode, remedy)
	}
	fmt.Fprintf(t.Err, "ctxloom: %s auth %s has no stored credential; starting %s's own flow to mint one\n", in.Backend, in.Mode, in.Backend)
	secret, err := a.Mint(ctx, in.Mode, t)
	if err != nil {
		return nil, fmt.Errorf("%s auth %s: mint: %w; %s", in.Backend, in.Mode, err, remedy)
	}
	path, err := isolation.StoreEngineCredential(in.Backend, in.Mode, secret)
	if err != nil {
		return nil, fmt.Errorf("%s auth %s: store the minted credential: %w", in.Backend, in.Mode, err)
	}
	fmt.Fprintf(t.Err, "ctxloom: stored the %s %s credential in %s (owner-only)\n", in.Backend, in.Mode, path)
	return a.LaunchEnv(in.Mode, os.LookupEnv, store)
}

// credentialRemedy names the commands that store a credential for mode.
func credentialRemedy(backend string, mode engine.AuthMode) string {
	return fmt.Sprintf("run `ctxloom auth mint --engine %s --mode %s` at a terminal, or store one you already have with `ctxloom auth set --engine %s --mode %s`", backend, mode, backend, mode)
}
