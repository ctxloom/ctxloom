package agent

import (
	"errors"
	"fmt"
)

// This file is the vocabulary an ENGINE PACKAGE uses to declare the facts
// internal/adapters/isolation needs about it: how its global home relocates, what
// credential material seeds an isolated home, and how a containerized run of
// it is built and authenticated. The engine authors VALUES of these types in
// its own package; isolation interprets them by NAME after the registry has
// pushed them, and never imports the engine. The types live here, not in
// isolation, because an engine package must be able to author them without
// linking the isolation machinery.
//
// Every optional part is a Declared slot, so "this engine has no X" is a
// stated value with a reason, never a nil that reads as either.

// EngineHome describes how an engine's global config/credential home is
// relocated to a per-agent instance.
type EngineHome struct {
	// Vars are the env vars that relocate the home, each pointed at Subdir
	// under the per-agent config home. An engine whose whole home moves with
	// one var has one entry; an engine that splits config and data across
	// separate XDG vars contributes one entry per var.
	Vars []HomeVar
	// Credentials declares whether the relocated home carries the engine's
	// CREDENTIALS too (Provide: seed them from the host, per CredentialSeed)
	// or the credential store lives somewhere no home var moves (Absent, with
	// the reason naming where).
	Credentials Declared[CredentialSeed]
}

// HomeVar is one env-var-to-subdir mapping. The leaf name is load-bearing:
// an engine that composes its own home path from a project-dir-shaped value
// must land on this exact directory, so Subdir must match whatever leaf the
// engine's own resolution appends.
type HomeVar struct {
	EnvVar string
	Subdir string
}

// CredentialSeed is the host credential material copied into an isolated
// home, and the two facts a fail-loud "nothing to seed" message needs: the
// env var that carries usable auth instead (EnvTrigger — seeding is skipped
// when it is set), and the command that makes the credential file exist
// (LoginHint).
type CredentialSeed struct {
	// Subdir is the config-home subdirectory the seed lands in. It must be a
	// Subdir one of the engine's Vars names, or the seed lands where the
	// engine never looks.
	Subdir string
	// EnvTrigger, when set in the process env, means auth rides the env and
	// nothing is seeded. "" when the engine has no such bypass.
	EnvTrigger string
	// LoginHint is the command that creates the credential file
	// (e.g. "claude login") — the fix a "nothing seedable" refusal names.
	LoginHint string
	// Files are copied in order. At least one must be Required: its absence
	// is the fail-loud case; an optional file is copied when present.
	Files []SeedFile
}

// SeedFile is one host file a CredentialSeed copies.
type SeedFile struct {
	// HostRelHome is the source path relative to the host user's home, in
	// slash form (e.g. ".claude/.credentials.json").
	HostRelHome string
	// DestName is the file name under the seed's Subdir.
	DestName string
	// Required marks the file whose absence means nothing is seedable.
	Required bool
}

// Validate refuses a declaration isolation could not act on correctly.
func (h EngineHome) Validate() error {
	if len(h.Vars) == 0 {
		return errors.New("EngineHome: no home var declared; an engine with no relocatable home declares Home absent instead")
	}
	subdirs := map[string]bool{}
	for i, v := range h.Vars {
		if v.EnvVar == "" {
			return fmt.Errorf("EngineHome: Vars[%d].EnvVar is empty", i)
		}
		if v.Subdir == "" {
			return fmt.Errorf("EngineHome: Vars[%d].Subdir is empty", i)
		}
		subdirs[v.Subdir] = true
	}
	if !h.Credentials.Decided() {
		return errors.New("EngineHome: Credentials is undeclared; provide a seed or declare it absent with the reason")
	}
	seed, ok := h.Credentials.Get()
	if !ok {
		return nil
	}
	if !subdirs[seed.Subdir] {
		return fmt.Errorf("EngineHome: CredentialSeed.Subdir %q is not a Subdir any home var points at", seed.Subdir)
	}
	if seed.LoginHint == "" {
		return errors.New("EngineHome: CredentialSeed.LoginHint is empty; a seed that can fail must name the command that fixes it")
	}
	required := false
	for i, f := range seed.Files {
		if f.HostRelHome == "" || f.DestName == "" {
			return fmt.Errorf("EngineHome: CredentialSeed.Files[%d] has an empty path", i)
		}
		required = required || f.Required
	}
	if !required {
		return errors.New("EngineHome: CredentialSeed has no Required file; without one there is no fail-loud case")
	}
	return nil
}

// EngineContainer describes how a containerized run of the engine is built
// and authenticated.
type EngineContainer struct {
	// Install is the engine's composable RUN-layer Containerfile fragment: its
	// own official installer on an arbitrary base, hard-gated by a validate
	// step. nil = no known installer (the engine cannot be composed into an
	// agent image; only an overlay onto a base that already ships it works).
	Install []byte
	// ValidateCommand is the in-image command that proves the client runs
	// (`<client> --version`). Required whenever Install is set.
	ValidateCommand string
	// Auth declares how the in-container engine authenticates, or that the
	// question has no answer yet (Absent: the run fails closed rather than
	// inherit another engine's credentials).
	Auth Declared[ContainerAuth]
	// OverlayDirs are the project-relative managed-config DIRECTORIES the
	// engine's writers target under the run's cwd, shadowed by scratch
	// overlays so the host project stays clean. Directories only.
	OverlayDirs []string
	// TranscriptStoreRel is the engine's native transcript store ROOT relative
	// to the container home, bind-mapped so transcripts survive teardown. ""
	// when the engine keeps no transcripts.
	TranscriptStoreRel string
}

// Validate refuses a container declaration that cannot be built or resolved.
func (c EngineContainer) Validate() error {
	if len(c.Install) > 0 && c.ValidateCommand == "" {
		return errors.New("EngineContainer: Install is set but ValidateCommand is empty; an install fragment must be gated by a command that proves the client runs")
	}
	if !c.Auth.Decided() {
		return errors.New("EngineContainer: Auth is undeclared; provide a resolver or declare it absent with the reason")
	}
	if a, ok := c.Auth.Get(); ok {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("EngineContainer: %w", err)
		}
	}
	return nil
}

// ContainerAuth is one engine's in-container authentication plan as DATA:
// prefer env passthrough when any trigger is set in the host env, else bind
// the credential files into the fresh home, else degrade with Hint.
type ContainerAuth struct {
	// Vendorless, when set, declares the engine authenticates against no
	// vendor at all: resolution always succeeds with no env and no mounts.
	// Correct only for an engine with no credential to resolve (a test
	// double); mutually exclusive with every other field.
	Vendorless string
	// EnvTriggers: any one set in the host env selects env passthrough.
	EnvTriggers []string
	// EnvPassthrough is the scoped set of var NAMES forwarded name-only; a
	// value is never stored here. Only the present ones cross.
	EnvPassthrough []string
	// CredentialFiles are bind-mounted from the host home into the container
	// home when no trigger is set. Empty = no mount fallback.
	CredentialFiles []CredentialFile
	// Hint is the degrade diagnostic when nothing resolves — names the
	// trigger var / credential source without leaking values.
	Hint string
}

// CredentialFile is one host credential bind-mounted into a container home.
type CredentialFile struct {
	HostRelHome      string
	ContainerRelHome string
	ReadOnly         bool
}

// Validate refuses an auth plan with no single reading or nothing to resolve.
func (a ContainerAuth) Validate() error {
	if a.Vendorless != "" {
		if len(a.EnvTriggers) > 0 || len(a.EnvPassthrough) > 0 || len(a.CredentialFiles) > 0 || a.Hint != "" {
			return errors.New("ContainerAuth: Vendorless excludes triggers, passthrough, credential files and a hint")
		}
		return nil
	}
	if len(a.EnvTriggers) == 0 && len(a.CredentialFiles) == 0 {
		return errors.New("ContainerAuth: nothing to resolve (no EnvTriggers and no CredentialFiles); declare Auth absent instead")
	}
	if a.Hint == "" {
		return errors.New("ContainerAuth: Hint is empty; a plan that can fail must say what was missing")
	}
	for i, f := range a.CredentialFiles {
		if f.HostRelHome == "" || f.ContainerRelHome == "" {
			return fmt.Errorf("ContainerAuth: CredentialFiles[%d] has an empty path", i)
		}
	}
	return nil
}
