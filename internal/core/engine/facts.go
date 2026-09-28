package engine

import (
	"errors"
	"fmt"

	"github.com/spf13/afero"
)

// This file is the vocabulary an engine's Home() and Container() speak: how
// its global config home relocates into a session home and how the engine
// authenticates there, and how a containerized run of it is built and
// authenticated.
// The engine package authors VALUES of these types; the cells adapter reads
// them off the Engine it was handed and never imports the engine. The types
// live on the port, not in the adapter, because an engine package must be
// able to author them without linking the isolation machinery.
//
// Every optional part is a Declared slot, so "this engine has no X" is a
// stated value with a reason, never a nil that reads as either.

// HomeSpec says how the engine's config home relocates into the session
// home. The zero value is the NULL OBJECT: no var relocates anything, no
// instance config is generated — an engine that keeps no engine-global state
// returns it and the cells adapter has nothing to do.
type HomeSpec struct {
	// Vars are the env vars that relocate the home, each pointed at Subdir
	// under the session home. An engine whose whole home moves with one var
	// has one entry; an engine that splits config and data across separate
	// XDG vars contributes one entry per var.
	Vars []HomeVar
	// Auth is the engine's authentication capability (modes, the env each
	// mode launches with, minting), or Absent with the reason for an engine
	// that needs no credential. Nothing is ever copied into a session home
	// to authenticate it. Undecided is legal ONLY on the zero spec; Validate
	// refuses it once a var is declared.
	Auth Declared[Auth]
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

// Validate refuses a non-zero spec the cells adapter could not act on
// correctly. The zero spec is valid: it declares nothing.
func (h HomeSpec) Validate() error {
	if !h.Relocates() {
		return h.validateWithoutHome()
	}
	if err := h.validateVars(); err != nil {
		return err
	}
	return h.validateAuth()
}

// validateWithoutHome refuses auth on a spec that relocates nothing.
func (h HomeSpec) validateWithoutHome() error {
	if _, ok := h.Auth.Get(); ok {
		return errors.New("HomeSpec: auth with no home var; an engine that relocates nothing declares no auth here")
	}
	return nil
}

// validateVars requires every home var to name its var and subdir.
func (h HomeSpec) validateVars() error {
	for i, v := range h.Vars {
		if v.Name == "" {
			return fmt.Errorf("HomeSpec: Vars[%d].Name is empty", i)
		}
		if v.Subdir == "" {
			return fmt.Errorf("HomeSpec: Vars[%d].Subdir is empty", i)
		}
	}
	return nil
}

// validateAuth requires Auth decided, and valid when present.
func (h HomeSpec) validateAuth() error {
	if !h.Auth.Decided() {
		return errors.New("HomeSpec: Auth is undeclared; provide the engine's auth or declare it absent with the reason")
	}
	if a, ok := h.Auth.Get(); ok {
		if err := validateAuth(a); err != nil {
			return fmt.Errorf("HomeSpec: %w", err)
		}
	}
	return nil
}

// InstanceConfigWriter generates the engine's own top-level config file
// inside a provisioned session home: the engine decides what a single byte
// of it says; the cells adapter decides that one is generated at all.
type InstanceConfigWriter interface {
	WriteInstanceConfig(req InstanceConfigRequest, fs afero.Fs) (InstanceConfigReport, error)
}

// InstanceConfigRequest is what the writer is handed: the host user's real
// home (the ambient values it may copy), the session home it writes into —
// the directory the engine's home var names, placed by launch.SessionHome,
// with no leaf of the writer's own appended — and the run's working
// directory.
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

// ContainerSpec says how a containerized run of the engine is built. How it
// authenticates is not a container question: the run's Credentials
// (Auth.Credentials) are satisfied by whichever environment runs it. Engine.Container returns it, or refuses with ErrUnsupported
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
