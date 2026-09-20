package engine

import (
	"errors"
	"fmt"

	"github.com/spf13/afero"
)

// This file is the vocabulary an engine's Home() and Container() speak: how
// its global config/credential home relocates into a session home and what
// seeds it, and how a containerized run of it is built and authenticated.
// The engine package authors VALUES of these types; the cells adapter reads
// them off the Engine it was handed and never imports the engine. The types
// live on the port, not in the adapter, because an engine package must be
// able to author them without linking the isolation machinery.
//
// Every optional part is a Declared slot, so "this engine has no X" is a
// stated value with a reason, never a nil that reads as either.

// HomeSpec says how the engine's config/credential home relocates into the
// session home. The zero value is the NULL OBJECT: no var relocates
// anything, nothing is seeded, no instance config is generated — an engine
// that keeps no engine-global state returns it and the cells adapter has
// nothing to do.
type HomeSpec struct {
	// Vars are the env vars that relocate the home, each pointed at Subdir
	// under the session home. An engine whose whole home moves with one var
	// has one entry; an engine that splits config and data across separate
	// XDG vars contributes one entry per var.
	Vars []HomeVar
	// Credentials declares whether the relocated home carries the engine's
	// CREDENTIALS too (Provide: seed them from the host, per CredentialSeed)
	// or the credential store lives somewhere no home var moves (Absent,
	// with the reason naming where). Undecided is legal ONLY on the zero
	// spec; Validate refuses it once a var is declared.
	Credentials Declared[CredentialSeed]
	// InstanceConfig is the engine's own generator of its top-level config
	// file inside a session home the cells adapter provisioned; nil when the
	// engine has no config file of its own.
	InstanceConfig InstanceConfigWriter
}

// HomeVar is one env-var-to-subdir mapping. The leaf name is load-bearing:
// an engine that composes its own home path from a project-dir-shaped value
// must land on this exact directory, so Subdir must match whatever leaf the
// engine's own resolution appends.
type HomeVar struct {
	Name   string
	Subdir string
}

// Relocates reports whether the spec moves anything: the zero spec does not.
func (h HomeSpec) Relocates() bool { return len(h.Vars) > 0 }

// CredentialSeed is the host credential material copied into a session
// home, the deliveries the engine ACCEPTS for it, and the two facts a
// fail-loud "nothing to seed" message needs: the env var that carries usable
// auth instead (EnvTrigger — seeding is skipped when it is set), and the
// command that makes the credential file exist (LoginHint).
type CredentialSeed struct {
	// Subdir is the home subdirectory the seed lands in. It must be a Subdir
	// one of the spec's Vars names, or the seed lands where the engine never
	// looks.
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
	// Accept is the material deliveries the engine takes, best first. A seed
	// with nothing it accepts is refused by Validate: the material's
	// existence and the way it may reach the instance are ONE declaration,
	// so "material to place but no delivery it accepts" cannot be authored.
	Accept []MaterialDelivery
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

// Validate refuses a non-zero spec the cells adapter could not act on
// correctly. The zero spec is valid: it declares nothing.
func (h HomeSpec) Validate() error {
	if !h.Relocates() {
		if _, ok := h.Credentials.Get(); ok {
			return errors.New("HomeSpec: a credential seed with no home var to land under")
		}
		return nil
	}
	subdirs := map[string]bool{}
	for i, v := range h.Vars {
		if v.Name == "" {
			return fmt.Errorf("HomeSpec: Vars[%d].Name is empty", i)
		}
		if v.Subdir == "" {
			return fmt.Errorf("HomeSpec: Vars[%d].Subdir is empty", i)
		}
		subdirs[v.Subdir] = true
	}
	if !h.Credentials.Decided() {
		return errors.New("HomeSpec: Credentials is undeclared; provide a seed or declare it absent with the reason")
	}
	seed, ok := h.Credentials.Get()
	if !ok {
		return nil
	}
	if !subdirs[seed.Subdir] {
		return fmt.Errorf("HomeSpec: CredentialSeed.Subdir %q is not a Subdir any home var points at", seed.Subdir)
	}
	if seed.LoginHint == "" {
		return errors.New("HomeSpec: CredentialSeed.LoginHint is empty; a seed that can fail must name the command that fixes it")
	}
	required := false
	for i, f := range seed.Files {
		if f.HostRelHome == "" || f.DestName == "" {
			return fmt.Errorf("HomeSpec: CredentialSeed.Files[%d] has an empty path", i)
		}
		required = required || f.Required
	}
	if !required {
		return errors.New("HomeSpec: CredentialSeed has no Required file; without one there is no fail-loud case")
	}
	return validateAccept(seed.Accept)
}

// InstanceConfigWriter generates the engine's own top-level config file
// inside a provisioned session home: the engine decides what a single byte
// of it says; the cells adapter decides that one is generated at all.
type InstanceConfigWriter interface {
	WriteInstanceConfig(req InstanceConfigRequest, fs afero.Fs) (InstanceConfigReport, error)
}

// InstanceConfigRequest is what the writer is handed: the host user's real
// home (the ambient values it may copy), the session home it writes under,
// and the run's working directory.
type InstanceConfigRequest struct {
	HostHome     string
	InstanceHome string
	WorkDir      string
}

// InstanceConfigReport is what it wrote and what it skipped.
type InstanceConfigReport struct {
	Wrote    []string
	Warnings []string
}

// ContainerSpec says how a containerized run of the engine is built and
// authenticated. Engine.Container returns it, or refuses with ErrUnsupported
// when the engine has no image — so a container binding fails at Resolve,
// never later.
type ContainerSpec struct {
	// Install is the engine's composable RUN-layer Containerfile fragment:
	// its own official installer on an arbitrary base, hard-gated by a
	// validate step. nil = no known installer (the engine cannot be composed
	// into an agent image; only an overlay onto a base that already ships it
	// works).
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
	// TranscriptStoreRel is the engine's native transcript store ROOT
	// relative to the container home, bind-mapped so transcripts survive
	// teardown. "" when the engine keeps no transcripts.
	TranscriptStoreRel string
}

// Validate refuses a container declaration that cannot be built or resolved.
func (c ContainerSpec) Validate() error {
	if len(c.Install) > 0 && c.ValidateCommand == "" {
		return errors.New("ContainerSpec: Install is set but ValidateCommand is empty; an install fragment must be gated by a command that proves the client runs")
	}
	if !c.Auth.Decided() {
		return errors.New("ContainerSpec: Auth is undeclared; provide a resolver or declare it absent with the reason")
	}
	if a, ok := c.Auth.Get(); ok {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("ContainerSpec: %w", err)
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

// TranscriptReader is one version-scoped reader of the engine's own
// transcript store, as the port sees it: the version range it covers. The
// transcript adapter that consumes readers knows their richer shape; the
// port only says which exist.
type TranscriptReader interface {
	Versions() (min, max string)
}

// HookCodec decodes the engine's native hook payloads into the unified
// event. An engine that fires no hooks returns a codec whose Decode refuses
// with ErrUnsupported — unreachable, since no payload arrives.
type HookCodec interface {
	Decode(event string, payload []byte) (HookEvent, error)
}

// HookEvent is one native hook payload, decoded: the unified event name,
// the engine's own session key and the transcript path it named.
type HookEvent struct {
	Event         string
	NativeSession string
	Transcript    string
}
