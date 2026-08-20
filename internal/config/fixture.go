package config

import (
	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
)

// Fixture is a direct mirror of every Config field, persisted and
// runtime-only. NewFixture is the only way to turn one into a *Config.
//
// Config's fields are unexported precisely so a loaded config cannot be
// mutated or replaced from outside this package. Fixture is the deliberate
// exception, and it does not reopen that hole: the hazard is a mutator
// corrupting the ONE shared instance every Load/Current holder sees, and a
// Fixture-built Config never enters the ambient memo and never aliases a
// Load result. Every call yields a separately-owned value.
//
// "Separately owned" means the CONTAINERS too, not just the struct. Both
// directions of the round trip clone every map and slice they carry, using
// accessors.go's clone helpers, so this type obeys the same copy-on-read
// policy the Get* accessors do — see TestToFixture_NeverAliasesConfigContainers
// for the reflective gate that keeps a newly added field honest.
//
// ONE deliberate exception, matching GetPendingUpgrade: PendingUpgrade and
// HomePendingUpgrade are carried as the same *upgrade.Pending. They are a
// handle on a pending on-disk schema upgrade that CommitPendingUpgrade
// consumes, not user data a caller amends, and duplicating one would hand out
// a second commit token for a single upgrade.
type Fixture struct {
	Version                      int
	LM                           LMConfig
	Editor                       EditorConfig
	Settings                     SettingsConfig
	Sync                         SyncConfig
	Profiles                     ProfilesConfig
	Agents                       map[string]agents.Agent
	DefaultAgent                 string
	Workspace                    string
	DirtyTreeHandler             string
	Runtime                      string
	Permissions                  string
	Delegation                   DelegationConfig
	IsolationImages              map[string]string
	IsolationBaseContainerfile   string
	IsolationDevcontainerBase    *bool
	IsolationDevcontainerService string
	IsolationEngines             []string
	UI                           UIConfig

	// Runtime-only fields, mirroring Config's own (see Config's doc).
	AppPaths           []string
	AppRoot            string
	AppDir             string
	Source             ConfigSource
	Warnings           []Warning
	PendingUpgrade     *upgrade.Pending
	HomePendingUpgrade *upgrade.Pending
}

// ToFixture returns a Fixture carrying a copy of every one of c's fields —
// the read half of the NewFixture round-trip, for callers that need to take
// an independent Config they already hold (e.g. one built by ParseConfig, not
// the ambient Load() instance), change a handful of fields, and
// re-marshal. operations.BuildInitialConfig does exactly this: it parses the
// embedded init scaffold, overrides llm/default_agent/agents for the chosen
// engine, and marshals the result — none of which touches the shared ambient
// config, so amending fields on this INDEPENDENT value is not the bug the
// rest of this package guards against.
func (c *Config) ToFixture() Fixture {
	return Fixture{
		Version:                      c.version,
		LM:                           cloneLMConfig(c.lm),
		Editor:                       cloneEditor(c.editor),
		Settings:                     cloneSettings(c.settings),
		Sync:                         cloneSync(c.sync),
		Profiles:                     ProfilesConfig{Definitions: cloneProfilesMap(c.profiles.Definitions)},
		Agents:                       cloneAgentsMap(c.agents),
		DefaultAgent:                 c.defaultAgent,
		Workspace:                    c.workspace,
		DirtyTreeHandler:             c.dirtyTreeHandler,
		Runtime:                      c.runtime,
		Permissions:                  c.permissions,
		Delegation:                   c.delegation,
		IsolationImages:              cloneStringMap(c.isolationImages),
		IsolationBaseContainerfile:   c.isolationBaseContainerfile,
		IsolationDevcontainerBase:    cloneBoolPtr(c.isolationDevcontainerBase),
		IsolationDevcontainerService: c.isolationDevcontainerService,
		IsolationEngines:             cloneStrings(c.isolationEngines),
		UI:                           cloneUIConfig(c.ui),
		AppPaths:                     cloneStrings(c.appPaths),
		AppRoot:                      c.appRoot,
		AppDir:                       c.appDir,
		Source:                       c.source,
		Warnings:                     cloneWarnings(c.warnings),
		PendingUpgrade:               c.pendingUpgrade,
		HomePendingUpgrade:           c.homePendingUpgrade,
	}
}

// NewFixture builds an independent *Config directly from f, bypassing Load's
// file/schema/upgrade/default-merge pipeline entirely. See Fixture's doc for
// exactly what this does and does not guarantee. The filesystem defaults to
// nil (the OS fs); call SetFS on the result to inject one.
//
// This solves one problem: producing a Config when there is no loadable one.
// Scaffolding a config.yaml before any file exists, and falling back so a
// user still reaches their LLM when resolution fails, are the shapes that
// qualify. Load cannot do either.
//
// Everywhere else, use Load / Current / Reload. Those are the only paths that
// apply schema validation, the upgrade pipeline, the default-registry merge
// and home<project layering. A Fixture skips all of them, so what it builds
// is not the config a user gets — and a check written against one can pass
// while the real thing is broken.
//
// If you do not already know you need this, you almost certainly do not.
func NewFixture(f Fixture) *Config {
	return &Config{
		version:                      f.Version,
		lm:                           cloneLMConfig(f.LM),
		editor:                       cloneEditor(f.Editor),
		settings:                     cloneSettings(f.Settings),
		sync:                         cloneSync(f.Sync),
		profiles:                     ProfilesConfig{Definitions: cloneProfilesMap(f.Profiles.Definitions)},
		agents:                       cloneAgentsMap(f.Agents),
		defaultAgent:                 f.DefaultAgent,
		workspace:                    f.Workspace,
		dirtyTreeHandler:             f.DirtyTreeHandler,
		runtime:                      f.Runtime,
		permissions:                  f.Permissions,
		delegation:                   f.Delegation,
		isolationImages:              cloneStringMap(f.IsolationImages),
		isolationBaseContainerfile:   f.IsolationBaseContainerfile,
		isolationDevcontainerBase:    cloneBoolPtr(f.IsolationDevcontainerBase),
		isolationDevcontainerService: f.IsolationDevcontainerService,
		isolationEngines:             cloneStrings(f.IsolationEngines),
		ui:                           cloneUIConfig(f.UI),
		appPaths:                     cloneStrings(f.AppPaths),
		appRoot:                      f.AppRoot,
		appDir:                       f.AppDir,
		source:                       f.Source,
		warnings:                     cloneWarnings(f.Warnings),
		pendingUpgrade:               f.PendingUpgrade,
		homePendingUpgrade:           f.HomePendingUpgrade,
	}
}
