// Package engine defines what an engine DECLARES about itself: the one
// record an engine package authors, in its own package, for the backend
// registry to install. Nothing here names an engine. The registry
// (internal/lm/backends) reads descriptors; engine packages write them; the
// composition root (internal/lm/engines) is the only production code that
// holds the list.
//
// Every optional capability is an agent.Declared slot, so absence is a stated
// reason rather than a nil, and Validate refuses a slot nobody decided.
package engine

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/engineversion"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/transcript/vendorreader"
)

// Descriptor is one engine's complete registration record.
type Descriptor struct {
	// Name is the registry key: lowercase, and what the backend's Name()
	// reports.
	Name string
	// Aliases are the alternate spellings that resolve to Name, repo-wide
	// (ltk and taskloom included). Empty is the honest "none".
	Aliases []string
	// TestOnly marks a test/development double: registered and reachable at
	// runtime, never offered to a user as a choice.
	TestOnly bool

	// NewBackend constructs a fresh backend, with the registry's launcher
	// injected — the substrate that execs processes lives with the registry,
	// not the engine.
	NewBackend func(agent.Launcher) agent.Backend
	// NewConfig returns the engine's zero typed config; the registry decodes
	// a labeled LLM entry's body into it.
	NewConfig func() agent.BackendConfig
	// Surfaces is the engine's static declaration of the approaches it
	// delivers, per surface kind. Required; an empty Declaration is the
	// declared "no surfaces".
	Surfaces agent.Declaration

	// SettingsWriter constructs the engine's settings writer.
	SettingsWriter agent.Declared[func(agent.SettingsOptions) agent.SettingsWriter]
	// InstanceConfig constructs the engine-owned generator of its own
	// top-level config file inside a config home ctxloom provisioned.
	InstanceConfig agent.Declared[func(agent.SettingsOptions) agent.InstanceConfigWriter]
	// CredentialProjector constructs the transform applied to a COPY of one
	// host credential file as it crosses into an instance home. Absent =
	// copied verbatim.
	CredentialProjector agent.Declared[func() agent.CredentialProjector]
	// CommandExports maps loaded bundle content to this engine's slash-command
	// exports, resolving per-prompt enablement and metadata.
	CommandExports agent.Declared[func([]*bundles.LoadedContent) []agent.CommandExport]
	// SkillExports maps loaded bundle skills to this engine's Agent Skill
	// package exports. Absent = the engine carries no skills surface.
	SkillExports agent.Declared[func([]*bundles.LoadedSkill) []agent.SkillExport]
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
	// Container is how a containerized run of the engine is built and
	// authenticated. Absent = no container story; a `runtime: container`
	// binding is refused and a run fails closed.
	Container agent.Declared[agent.EngineContainer]
	// TranscriptReaders are the version-scoped adapters that read the
	// engine's own transcript store back into a canonical transcript.
	TranscriptReaders agent.Declared[[]vendorreader.VersionedAdapter]

	// EnforcesReadOnlyPlan is true when the engine maps agent.PermissionPlan
	// to a genuinely read-only, non-prompting mode. false = no such tier; the
	// run resolver collapses plan to default.
	EnforcesReadOnlyPlan bool
	// ResolveModel translates a configured model string into the id the
	// engine's launch path accepts. nil = pass through untouched — a positive
	// default, not an absence.
	ResolveModel func(model string) (resolved string, ok bool)
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

// Validate refuses a descriptor the registry could install only by guessing.
// It is total: every rule is checked here, so a registration error names the
// exact field.
func (d Descriptor) Validate() error {
	if d.Name == "" {
		return errors.New("descriptor: Name is empty")
	}
	if d.Name != strings.ToLower(d.Name) {
		return fmt.Errorf("descriptor %s: Name must be lowercase", d.Name)
	}
	seen := map[string]bool{}
	for _, a := range d.Aliases {
		switch {
		case a == d.Name:
			return fmt.Errorf("descriptor %s: alias %q equals the name", d.Name, a)
		case a != strings.ToLower(a):
			return fmt.Errorf("descriptor %s: alias %q must be lowercase", d.Name, a)
		case seen[a]:
			return fmt.Errorf("descriptor %s: alias %q declared twice", d.Name, a)
		}
		seen[a] = true
	}
	if d.NewBackend == nil {
		return fmt.Errorf("descriptor %s: NewBackend is nil", d.Name)
	}
	if d.NewConfig == nil {
		return fmt.Errorf("descriptor %s: NewConfig is nil", d.Name)
	}
	if d.Surfaces == nil {
		return fmt.Errorf("descriptor %s: Surfaces is nil; declare an empty Declaration for an engine with none", d.Name)
	}
	if name, ok := d.undeclaredSlot(); ok {
		return fmt.Errorf("descriptor %s: %s is undeclared; Provide it or declare it Absent with the reason", d.Name, name)
	}
	if err := d.validateProvided(); err != nil {
		return fmt.Errorf("descriptor %s: %w", d.Name, err)
	}
	return nil
}

// undeclaredSlot reports the first Declared field nobody decided, found by
// reflection so a new slot is gated without an edit here.
func (d Descriptor) undeclaredSlot() (string, bool) {
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
func (d Descriptor) validateProvided() error {
	if f, ok := d.SettingsWriter.Get(); ok && f == nil {
		return errors.New("SettingsWriter is provided as nil")
	}
	if f, ok := d.InstanceConfig.Get(); ok && f == nil {
		return errors.New("InstanceConfig is provided as nil")
	}
	if f, ok := d.CredentialProjector.Get(); ok && f == nil {
		return errors.New("CredentialProjector is provided as nil")
	}
	if f, ok := d.CommandExports.Get(); ok && f == nil {
		return errors.New("CommandExports is provided as nil")
	}
	if f, ok := d.SkillExports.Get(); ok && f == nil {
		return errors.New("SkillExports is provided as nil")
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
			return errors.New("Home declares more than one home var; the in-tree home derivation reads exactly one home var and no engine needs more yet — lift it when one does")
		}
	}
	if c, ok := d.Container.Get(); ok {
		if err := c.Validate(); err != nil {
			return err
		}
	}
	return nil
}
