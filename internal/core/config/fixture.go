package config

import (
	"maps"
	"slices"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// Fixture is a direct mirror of every Config field, persisted and
// runtime-only. NewFixture is the only way to turn one into a *Config.
//
// Config's fields are unexported precisely so a loaded config cannot be
// mutated or replaced from outside this package. Fixture is the deliberate
// exception, and it does not reopen that hole: the hazard is a mutator
// corrupting the ONE published generation every Current holder sees, and a
// Fixture-built Config is never published by an Owner and never aliases a
// generation's value. Every call yields a separately-owned value.
//
// "Separately owned" means the CONTAINERS too, not just the struct. Both
// directions of the round trip clone every map and slice they carry, using
// accessors.go's clone helpers, so this type obeys the same copy-on-read
// policy the Get* accessors do — see TestToFixture_NeverAliasesConfigContainers
// for the reflective gate that keeps a newly added field honest.
type Fixture struct {
	SchemaVersion                int
	LM                           LMConfig
	Editor                       EditorConfig
	Settings                     SettingsConfig
	Sync                         SyncConfig
	Agents                       map[string]agents.Agent
	DefaultAgent                 string
	Workspace                    string
	DirtyTreeHandler             string
	Runtime                      string
	Permissions                  agents.NeutralPermissions
	Delegation                   DelegationConfig
	IsolationImages              map[string]string
	IsolationBase                string
	IsolationDevcontainerService string
	IsolationEngines             []string
	OutputDir                    string
	UI                           UIConfig
	SessionReapAge               string
	SessionPurgeAge              string
	Auth                         engine.AuthMode

	// Runtime-only fields, mirroring Config's own (see Config's doc).
	AppPaths []string
	AppRoot  string
	AppDir   string
	Source   ConfigSource
	Warnings []Warning

	// VersionResolver is the generation's pinned-version resolver (bound by
	// the reader in production); carried so a fixture can exercise a
	// version-pinned read.
	VersionResolver bundles.BundleVersionResolver
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
//
// The persisted half is taken from toDoc, so the two conversions share ONE
// clone discipline: a Fixture cannot own its containers more weakly than
// the document Owner.Update hands out, because it holds that document's.
func (c *Config) ToFixture() Fixture {
	d := c.toDoc()
	return Fixture{
		SchemaVersion:                d.SchemaVersion,
		LM:                           d.LM,
		Editor:                       d.Editor,
		Settings:                     d.Settings,
		Sync:                         d.Sync,
		Agents:                       d.Agents,
		DefaultAgent:                 d.DefaultAgent,
		Workspace:                    d.Workspace,
		DirtyTreeHandler:             d.DirtyTreeHandler,
		Runtime:                      d.Runtime,
		Permissions:                  d.Permissions,
		Delegation:                   d.Delegation,
		IsolationImages:              d.IsolationImages,
		IsolationBase:                d.IsolationBase,
		IsolationDevcontainerService: d.IsolationDevcontainerService,
		IsolationEngines:             d.IsolationEngines,
		OutputDir:                    d.OutputDir,
		UI:                           d.UI,
		SessionReapAge:               d.SessionReapAge,
		SessionPurgeAge:              d.SessionPurgeAge,
		Auth:                         d.Auth,
		AppPaths:                     slices.Clone(c.appPaths),
		AppRoot:                      c.appRoot,
		AppDir:                       c.appDir,
		Source:                       c.source,
		Warnings:                     cloneWarnings(c.warnings),
		VersionResolver:              c.versionResolver,
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
		schemaVersion:                f.SchemaVersion,
		lm:                           cloneLMConfig(f.LM),
		editor:                       cloneEditor(f.Editor),
		settings:                     cloneSettings(f.Settings),
		sync:                         cloneSync(f.Sync),
		agents:                       cloneAgentsMap(f.Agents),
		defaultAgent:                 f.DefaultAgent,
		workspace:                    f.Workspace,
		dirtyTreeHandler:             f.DirtyTreeHandler,
		runtime:                      f.Runtime,
		permissions:                  f.Permissions.Clone(),
		delegation:                   f.Delegation,
		isolationImages:              maps.Clone(f.IsolationImages),
		isolationBase:                f.IsolationBase,
		isolationDevcontainerService: f.IsolationDevcontainerService,
		isolationEngines:             slices.Clone(f.IsolationEngines),
		outputDir:                    f.OutputDir,
		ui:                           cloneUIConfig(f.UI),
		sessionReapAge:               f.SessionReapAge,
		sessionPurgeAge:              f.SessionPurgeAge,
		auth:                         f.Auth,
		appPaths:                     slices.Clone(f.AppPaths),
		appRoot:                      f.AppRoot,
		appDir:                       f.AppDir,
		source:                       f.Source,
		warnings:                     cloneWarnings(f.Warnings),
		versionResolver:              f.VersionResolver,
	}
}
