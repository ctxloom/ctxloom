package operations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"

	"golang.org/x/term"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// errLoginNotInContainer: an agent declaring the human's login runs in a
// container, where that login is not reachable. Nothing mounts the human's
// credential storage into a container, and a container that cannot reach
// the login would start logged out rather than fail.
var errLoginNotInContainer = errors.New("auth login shares the human's own login, which no container can reach")

// runAuth is one run's claim on a credential: which engine, in which
// DECLARED auth mode ("" undeclared), and where it runs. The engine home is
// deliberately absent: auth is purely per agent, so a run on the human's
// real home (engine_home: host) authenticates in its declared mode like any
// other.
type runAuth struct {
	Backend string
	// Declared is the agent's auth mode as written; checkAgentAuth parses it.
	Declared string
	// OnHost is whether the engine runs on the host rather than in a
	// container.
	OnHost bool
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

// checkAgentAuth is engine.CheckAuth over the named engine: the ONE check of
// an auth selection, run by `agent create/edit` (validateAgentAuth) and by
// every launch (resolveRunAuth); config load runs engine.CheckAuth itself.
// A nil Auth with no error is an engine that authenticates on its own.
func checkAgentAuth(reg engine.Registry, backend, declared string, onHost bool) (engine.Auth, engine.AuthMode, error) {
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
	if mode == engine.AuthLogin && !onHost {
		others := slices.DeleteFunc(slices.Clone(a.Modes()), func(m engine.AuthMode) bool { return m == engine.AuthLogin })
		return nil, "", report.Errorf(fmt.Sprintf("declare another of the modes %s supports for a container agent: %s, or run it on the host", backend, joinAuthModes(others)),
			"%s: %w", backend, errLoginNotInContainer)
	}
	return a, mode, nil
}

func joinAuthModes(modes []engine.AuthMode) string {
	s := make([]string, len(modes))
	for i, m := range modes {
		s[i] = string(m)
	}
	return strings.Join(s, ", ")
}

// resolveRunAuth is the env a run authenticates with: the engine's own
// Auth.LaunchEnv for the agent's mode, which sets the mode's credential and
// unsets the others. Engine-blind: which vars those are is the engine's
// answer. Zero for an engine that declares no auth.
//
// A MINTED credential (the token) that is neither exported nor stored is
// minted when the human is at a terminal (Auth.Mint, then stored
// owner-only), and refused with the remedy when they are not: an unattended
// run never starts logged out and never waits on a prompt nobody can see.
// Every other missing credential is the engine's own refusal, which names
// what to set or store.
func resolveRunAuth(ctx context.Context, reg engine.Registry, in runAuth) (engine.LaunchEnv, error) {
	a, mode, err := checkAgentAuth(reg, in.Backend, in.Declared, in.OnHost)
	if err != nil || a == nil {
		return engine.LaunchEnv{}, err
	}
	env, err := a.LaunchEnv(mode, os.LookupEnv, isolation.StoredCredentials(in.Backend))
	if !errors.Is(err, engine.ErrNoCredential) || !mode.Minted() {
		return env, err
	}
	return mintAndLaunchEnv(ctx, a, in.Backend, mode)
}

// mintAndLaunchEnv mints the missing credential at the human's terminal,
// stores it, and resolves the env again; unattended, it refuses with the
// commands that would provide one.
func mintAndLaunchEnv(ctx context.Context, a engine.Auth, backend string, mode engine.AuthMode) (engine.LaunchEnv, error) {
	mintMu.Lock()
	defer mintMu.Unlock()
	store := isolation.StoredCredentials(backend)
	env, err := a.LaunchEnv(mode, os.LookupEnv, store)
	if !errors.Is(err, engine.ErrNoCredential) {
		return env, err
	}
	remedy := credentialRemedy(backend, mode)
	t, ok := attendedTerminal()
	if !ok {
		return engine.LaunchEnv{}, report.Errorf(remedy, "%s auth %s needs a stored credential and this run has no terminal to mint one at: %w", backend, mode, engine.ErrNoCredential)
	}
	fmt.Fprintf(t.Err, "ctxloom: %s auth %s has no stored credential; starting %s's own flow to mint one\n", backend, mode, backend)
	secret, err := a.Mint(ctx, mode, t)
	if err != nil {
		return engine.LaunchEnv{}, report.Errorf(remedy, "%s auth %s: mint: %w", backend, mode, err)
	}
	path, err := isolation.StoreEngineCredential(backend, mode, secret)
	if err != nil {
		return engine.LaunchEnv{}, fmt.Errorf("%s auth %s: store the minted credential: %w", backend, mode, err)
	}
	fmt.Fprintf(t.Err, "ctxloom: stored the %s %s credential in %s (owner-only)\n", backend, mode, path)
	return a.LaunchEnv(mode, os.LookupEnv, store)
}

// credentialRemedy names the commands that store a credential for mode.
func credentialRemedy(backend string, mode engine.AuthMode) string {
	return fmt.Sprintf("run `ctxloom auth mint --engine %s --mode %s` at a terminal, or store one you already have with `ctxloom auth set --engine %s --mode %s`", backend, mode, backend, mode)
}
