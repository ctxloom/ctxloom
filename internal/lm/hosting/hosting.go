// Package hosting is the instance half of the engine port as it stands
// until the runner lands: the record (Hosting) an engine package authors so
// internal/lm/backends can RUN its kind — the backend constructor, the
// writers, the export projections, the home and container stories, the
// transcript readers. Everything DECLARATIVE lives on the kind
// (engine.Definition); a Hosting is paired with its kind by name at
// registration. Nothing here names an engine. The package dies with
// lm/backends.
package hosting

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/ctxloom/ctxloom/internal/adapters/engineversion"
	"github.com/ctxloom/ctxloom/internal/adapters/transcript/vendorreader"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// Hosting is one engine's hosting record. Every optional capability is an
// agent.Declared slot, so absence is a stated reason rather than a nil,
// and Validate refuses a slot nobody decided.
type Hosting struct {
	// Engine names the kind this record hosts: the registry key, and what
	// the backend's Name() reports. Register refuses a record whose kind is
	// not in the Registry, and a kind with no record.
	Engine engine.Name

	// NewBackend constructs a fresh backend, with the registry's launcher
	// injected — the substrate that execs processes lives with the registry,
	// not the engine.
	NewBackend func(agent.Launcher) agent.Backend
	// NewConfig returns the engine's zero typed config; the registry decodes
	// a labeled LLM entry's body into it.
	NewConfig func() agent.BackendConfig
	// Surfaces is the named-form table today's launch path constructs
	// writers from by selection: for an engine whose typed approaches carry
	// agent.Forms it is agent.DeclarationOf(kind.Root().Surfaces()); the
	// mock backend's forms are its own writers here. Required; an empty
	// Declaration is the declared "no surfaces". Retires with the seam that
	// constructs by name.
	Surfaces agent.Declaration

	// SettingsWriter constructs the engine's settings writer.
	SettingsWriter agent.Declared[func(agent.SettingsOptions) agent.SettingsWriter]
	// InstanceConfig constructs the engine-owned generator of its own
	// top-level config file inside a config home ctxloom provisioned.
	InstanceConfig agent.Declared[func(agent.SettingsOptions) agent.InstanceConfigWriter]
	// HookGlobalScope is the project/global settings-path collision guard
	// `manage hooks install` applies. Absent = audited, the global path never
	// collapses onto the project path.
	HookGlobalScope agent.Declared[HookGlobalScope]
	// VersionCommand is how to ask the engine's binary for its version.
	// Absent = the engine cannot be asked (no binary).
	VersionCommand agent.Declared[engineversion.Command]
	// Home is how the engine's global config/credential home relocates per
	// agent. Absent = the engine keeps no engine-global state to isolate.
	Home agent.Declared[agent.EngineHome]
	// Provisioning is what the engine will accept as the way its credential
	// material reaches a per-session instance home, in preference order.
	// Absent = the engine keeps no material that needs provisioning, with the
	// reason. It is a DECLARATION only: nothing in the launch path reads it
	// yet, and it is declared first so that when something does, no engine is
	// silently given a mechanism nobody chose for it.
	Provisioning agent.Declared[agent.ProvisioningPolicy]
	// Container is how a containerized run of the engine is built and
	// authenticated. Absent = no container story; a `runtime: container`
	// binding is refused and a run fails closed.
	Container agent.Declared[agent.EngineContainer]
	// TranscriptReaders are the version-scoped adapters that read the
	// engine's own transcript store back into a canonical transcript.
	TranscriptReaders agent.Declared[[]vendorreader.VersionedAdapter]

	// NoHooksReason declares the engine has NO hook mechanism at all. Empty =
	// it carries hooks.
	NoHooksReason string
	// LaunchOnlySettingsReason declares the engine's settings/prompt/skill
	// surfaces exist only inside a per-session engine home, so a static
	// materialize has nowhere to write them. Empty = cwd-keyed paths exist.
	LaunchOnlySettingsReason string
	// UnsupportedHookKinds names, per unified hook kind, why the engine's
	// mechanism lacks a native event for it. nil = every kind is carried.
	UnsupportedHookKinds map[string]string
	// NoLegacyHistoryReason declares the engine's legacy per-engine session
	// scraper was RETIRED: its backend's History() is nil, and canonical
	// capture is the ONLY transcript source, so a session-source builder
	// must not construct a legacy leg for it. Empty = the engine keeps a
	// legacy leg. The backend registry holds the two in agreement.
	NoLegacyHistoryReason string
}

// HookGlobalScope resolves an engine's project-scoped config path (under a
// workDir) and its user-global path, for an engine whose two can collide.
type HookGlobalScope struct {
	Paths func(workDir string) (projectPath, globalPath string, err error)
	// Label is the human-facing name of the global scope, read into the
	// refusal/warning message.
	Label string
}

// decided is the interface every agent.Declared instantiation satisfies; it
// is how Validate finds the Declared slots without a list.
type decided interface{ Decided() bool }

// Validate refuses a record the registry could install only by guessing.
// It is total: every rule is checked here, so a registration error names the
// exact field. The declarative rules (name, distribution, modes, grammars,
// approaches) are the kind's, checked once by its constructor.
func (d Hosting) Validate() error {
	if d.Engine == "" {
		return errors.New("hosting: Engine is empty")
	}
	if d.NewBackend == nil {
		return fmt.Errorf("hosting %s: NewBackend is nil", d.Engine)
	}
	if d.NewConfig == nil {
		return fmt.Errorf("hosting %s: NewConfig is nil", d.Engine)
	}
	if d.Surfaces == nil {
		return fmt.Errorf("hosting %s: Surfaces is nil; declare an empty Declaration for an engine with none", d.Engine)
	}
	if name, ok := d.undeclaredSlot(); ok {
		return fmt.Errorf("hosting %s: %s is undeclared; Provide it or declare it Absent with the reason", d.Engine, name)
	}
	if err := d.validateProvided(); err != nil {
		return fmt.Errorf("hosting %s: %w", d.Engine, err)
	}
	return nil
}

// undeclaredSlot reports the first Declared field nobody decided, found by
// reflection so a new slot is gated without an edit here.
func (d Hosting) undeclaredSlot() (string, bool) {
	v := reflect.ValueOf(d)
	for i := 0; i < v.NumField(); i++ {
		slot, ok := v.Field(i).Interface().(decided)
		if ok && !slot.Decided() {
			return v.Type().Field(i).Name, true
		}
	}
	return "", false
}

// validateProvided checks each PROVIDED slot's value: a nil func is the one
// omission Declared cannot see, and the compound facts carry their own rules.
func (d Hosting) validateProvided() error {
	if f, ok := d.SettingsWriter.Get(); ok && f == nil {
		return errors.New("SettingsWriter is provided as nil")
	}
	if f, ok := d.InstanceConfig.Get(); ok && f == nil {
		return errors.New("InstanceConfig is provided as nil")
	}
	if h, ok := d.HookGlobalScope.Get(); ok && (h.Paths == nil || h.Label == "") {
		return errors.New("HookGlobalScope is provided without Paths or Label")
	}
	if c, ok := d.VersionCommand.Get(); ok && (c.Parse == nil || len(c.Args) == 0) {
		return errors.New("VersionCommand is provided without Args or Parse")
	}
	if r, ok := d.TranscriptReaders.Get(); ok && len(r) == 0 {
		return errors.New("TranscriptReaders is provided empty")
	}
	if h, ok := d.Home.Get(); ok {
		if err := h.Validate(); err != nil {
			return err
		}
		if len(h.Vars) != 1 {
			return errors.New("Home.Vars must declare exactly one home var; the in-tree home derivation reads exactly one and no engine needs more yet — lift it when one does")
		}
	}
	if p, ok := d.Provisioning.Get(); ok {
		if err := p.Validate(); err != nil {
			return err
		}
	}
	if c, ok := d.Container.Get(); ok {
		if err := c.Validate(); err != nil {
			return err
		}
	}
	return nil
}
