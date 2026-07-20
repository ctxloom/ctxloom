package config

import (
	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// Fixture is an exported, direct mirror of every Config field (persisted and
// runtime-only). NewFixture is the ONLY way to turn one into a *Config, and
// exists so tests (internal/operations, internal/cli, internal/lm/backends,
// ...) can keep building an independent, fully-controlled Config value
// in-memory — as ~80 test files across the tree already did before Config's
// fields were unexported (v0.7.0-pre1 config-manager rework, Phase 3) —
// without paying for a real config.yaml + the full Load() pipeline (schema
// validation, upgrade migration, default-registry merge) when a test's whole
// point is to pin an exact, hand-picked field combination.
//
// DESIGN NOTE (flag for review): this is in real tension with "no exported
// constructor, no assignable exported value" — NewFixture IS an exported
// constructor, and Fixture IS a directly-constructible exported value. It
// does not reopen the bug the rest of this package closes, though: that bug
// is a MUTATOR corrupting the ONE shared instance every Load()/Current()
// holder sees. A Fixture-built Config is never fed into the ambient memo,
// never aliases another Load() result, and every NewFixture call returns a
// brand-new, independently-owned value — exactly as safe as two Load() calls
// with different WithAppDir options already were. It exists purely so ~80
// pre-existing test files did not need a much larger, riskier rewrite (real
// config.yaml fixtures + Load, which can also silently change what a test
// exercises via schema validation / the default-registry overlay). If this
// tradeoff is wrong, the alternative is rewriting those tests onto
// Load(WithFS(...)/WithAppDir(...)) with a real written config.yaml, which is
// a materially larger, separate piece of work.
type Fixture struct {
	Version                      int
	LM                           LMConfig
	Editor                       EditorConfig
	Settings                     SettingsConfig
	Sync                         SyncConfig
	Hooks                        wire.HooksConfig
	MCP                          wire.MCPConfig
	Profiles                     ProfilesConfig
	Agents                       map[string]agents.Agent
	DefaultAgent                 string
	Workspace                    string
	Runtime                      string
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
// the ambient Load()/Current() instance), change a handful of fields, and
// re-marshal. operations.BuildInitialConfig does exactly this: it parses the
// embedded init scaffold, overrides llm/default_agent/agents for the chosen
// engine, and marshals the result — none of which touches the shared ambient
// config, so amending fields on this INDEPENDENT value is not the bug the
// rest of this package guards against.
func (c *Config) ToFixture() Fixture {
	return Fixture{
		Version:                      c.version,
		LM:                           c.lm,
		Editor:                       c.editor,
		Settings:                     c.settings,
		Sync:                         c.sync,
		Hooks:                        c.hooks,
		MCP:                          c.mcp,
		Profiles:                     c.profiles,
		Agents:                       c.agents,
		DefaultAgent:                 c.defaultAgent,
		Workspace:                    c.workspace,
		Runtime:                      c.runtime,
		IsolationImages:              c.isolationImages,
		IsolationBaseContainerfile:   c.isolationBaseContainerfile,
		IsolationDevcontainerBase:    c.isolationDevcontainerBase,
		IsolationDevcontainerService: c.isolationDevcontainerService,
		IsolationEngines:             c.isolationEngines,
		UI:                           c.ui,
		AppPaths:                     c.appPaths,
		AppRoot:                      c.appRoot,
		AppDir:                       c.appDir,
		Source:                       c.source,
		Warnings:                     c.warnings,
		PendingUpgrade:               c.pendingUpgrade,
		HomePendingUpgrade:           c.homePendingUpgrade,
	}
}

// NewFixture builds an independent *Config directly from f, bypassing Load's
// file/schema/upgrade/default-merge pipeline entirely. See Fixture's doc for
// exactly what this does and does not guarantee. The filesystem defaults to
// nil (OS fs); call SetFS on the result to inject one, exactly as before.
func NewFixture(f Fixture) *Config {
	return &Config{
		version:                      f.Version,
		lm:                           f.LM,
		editor:                       f.Editor,
		settings:                     f.Settings,
		sync:                         f.Sync,
		hooks:                        f.Hooks,
		mcp:                          f.MCP,
		profiles:                     f.Profiles,
		agents:                       f.Agents,
		defaultAgent:                 f.DefaultAgent,
		workspace:                    f.Workspace,
		runtime:                      f.Runtime,
		isolationImages:              f.IsolationImages,
		isolationBaseContainerfile:   f.IsolationBaseContainerfile,
		isolationDevcontainerBase:    f.IsolationDevcontainerBase,
		isolationDevcontainerService: f.IsolationDevcontainerService,
		isolationEngines:             f.IsolationEngines,
		ui:                           f.UI,
		appPaths:                     f.AppPaths,
		appRoot:                      f.AppRoot,
		appDir:                       f.AppDir,
		source:                       f.Source,
		warnings:                     f.Warnings,
		pendingUpgrade:               f.PendingUpgrade,
		homePendingUpgrade:           f.HomePendingUpgrade,
	}
}
