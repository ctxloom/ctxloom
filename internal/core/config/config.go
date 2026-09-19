package config

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/adapters/agents"
	"github.com/ctxloom/ctxloom/internal/adapters/content/remotetree"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// Re-export path constants for backwards compatibility
const (
	AppDirName     = paths.AppDirName
	ConfigFileName = paths.ConfigFileName
	BundlesDir     = paths.BundlesDir
)

// ConfigSource indicates where the configuration was loaded from.
type ConfigSource int

const (
	// SourceProject means config was loaded from a project .ctxloom directory.
	SourceProject ConfigSource = iota
	// SourceHome means config was loaded from user home ~/.ctxloom directory.
	SourceHome
)

// Config holds the ctxloom configuration.
//
// EVERY field is unexported (v0.7.0-pre1 config-manager rework, Phase 3):
// Load() hands the SAME *Config pointer to ~45 call sites, so an exported
// field would let ANY holder mutate what every other holder sees — exactly
// the bug that motivated this rework (operations.SetAgent used to do
// `cfg.agents[name] = ...` directly on the shared instance). Reads go through
// the Get<Field> accessors in accessors.go (copy-on-read); writes go through
// Manager.Update's Draft (see config_manager.go). This is a COMPILE ERROR,
// not a convention: nothing outside this package can even name these fields.
//
// yaml (de)serialization used to rely on encoding/yaml's reflection walking
// these fields' exported names + tags directly; that no longer works once
// they're unexported (reflection cannot see unexported field VALUES, even
// from code in this same package — it's a language rule, not a package
// boundary). MarshalYAML/UnmarshalYAML below round-trip through configDoc, an
// exported-field mirror with the same tags, so every existing
// yaml.Marshal(cfg)/yaml.Unmarshal(data, cfg) call site keeps working
// unchanged.
//
// NIL RECEIVERS: a *Config method is NOT nil-safe unless its own doc says so.
// The type has 93 methods (73 of them exported) and exactly five tolerate a nil
// receiver —
// MarshalYAML, IsolationImageFor, IsolationBaseContainerfilePath,
// IsolationDevcontainerBaseEnabled and DefaultAgentProfiles. That set is
// deliberate and closed, not the start of a migration: a nil *Config means
// "config was never loaded", which is a caller bug everywhere except where a
// zero-valued answer is genuinely the right one (an unmarshaler handed a nil
// pointer; the isolation-image accessors, whose composite
// operations.IsolationImageConfig already guards nil one level up; the default
// agent set, which is legitimately empty before any config exists). Making the
// other 88 nil-tolerant would convert those caller bugs into silently empty
// behaviour, which is this codebase's characteristic failure. The closed set is
// pinned by TestConfig_NilReceiverContract.
type Config struct {
	version  int            // config schema version (integer; distinct from app version)
	lm       LMConfig       //
	editor   EditorConfig   //
	settings SettingsConfig //
	sync     SyncConfig     //
	// agents is the LOCAL-ONLY engine↔profile binding map, and the ONE source
	// of the agent entity. Keyed by agent name. It is NEVER a bundle item kind
	// and NEVER remote — there is no Bundle.Agents and no remote path. Read it
	// through LoadAgents / Agent, which clone what they hand out.
	agents map[string]agents.Agent
	// defaultAgent names the always-bound default agent: the key in agents
	// whose binding a bare `ctxloom run` (no --agent,
	// no -p/-f/-t) resolves — its composed profiles become the context and its
	// engine + runtime + permissions the transport. It replaces the retired
	// profiles.defaults: "the default profile set" is now whatever this agent
	// composes (DefaultAgentProfiles). Empty or naming an undefined agent degrades
	// to empty context (a warning, never a hard stop — CLAUDE.md fault tolerance).
	defaultAgent string
	// workspace is the project-wide DEFAULT for the SESSION-level workspace
	// axis (none | worktree): where a session's working directory lives.
	// Empty means "none" (the shared live project dir — today's behaviour).
	// A session-creating invocation (run/acp `--workspace`, an agent_run
	// spawn's workspace field) overrides
	// it per session. Deliberately NOT an agent trait: needing a private cwd
	// is a property of how a session is launched, not of who the agent is.
	workspace string
	// dirtyTreeHandler is the project-wide DEFAULT for what a delegated
	// agent_run spawn does when it resolves to worktree isolation while the
	// PARENT tree (this project's own live checkout) is dirty: "commit" |
	// "copy" | "stale" | "fail". Empty means "commit" (the built-in
	// default — see operations.defaultDirtyTreeHandler). A per-call
	// agent_run "dirty_tree_handler" parameter overrides this default,
	// mirroring workspace's own project-default/per-call split. See
	// operations.handleDirtyParentTree for what each value does.
	dirtyTreeHandler string
	// The dirty-tree-commit human acknowledgement used to live here as a
	// config-only bool field. It moved to paths.DirtyTreeCommitAckPath (an
	// internal/shared/admission.Store file under .ctxloom/state/) — see
	// config.DirtyTreeCommitAcknowledged/SetDirtyTreeCommitAck and the
	// config-layer-scope design doc's "Consent leaves the chain": a config
	// key is reachable from THREE channels an agent can write (a home file,
	// an environment variable, an argv), and prior human consent needs a
	// home with none. ScopeNever in internal/adapters/configload/layerscope names the
	// scope this key would have needed and why no layer may carry it.
	// runtime is the project-wide DEFAULT for the AGENT-level runtime axis
	// (host | container): where an agent's engine process executes. Empty
	// means "host". An agent binding's own `runtime:` overrides it; the
	// precedence (agent → this default → host) is resolved in
	// operations.resolveAgentBinding. The two axes are independent and meet
	// only at launch (isolation.Axes).
	runtime string
	// permissions is the project-wide DEFAULT launch-time permission posture
	// (default | acceptEdits | plan | bypass) for engines launched in THIS
	// project directory — the per-project consent knob: "in this directory, an
	// agent starts at this posture unless something narrower says otherwise".
	// Empty means undeclared, which falls through to the engine's own built-in
	// default (bypass for the claude-code host stopgap, prompt elsewhere).
	//
	// It sits BELOW every explicit declaration (--permissions flag > the agent
	// binding's own `permissions` > the engine label's `permissions` > this) and
	// ABOVE the engine fallback, so a narrower posture declared anywhere always
	// wins and a declared project posture beats a silent engine default.
	// Resolution lives in cli.resolvePermissionMode / operations.RunOneshot /
	// operations.ResolveAgent.
	//
	// LAYER-SCOPED TO THE PROJECT FILE. layerscope assigns it ScopeShared, so a
	// ~/.ctxloom/config.yaml carrying it is DROPPED with a warning rather than
	// gap-filling a project that declared nothing, and CTXLOOM_CONFIG_PERMISSIONS
	// cannot carry it either. That restriction is the feature, not an
	// implementation detail: a home-wide permissive default already exists as the
	// claude-code host stopgap, and a second one would silently re-grant every
	// project on the machine the posture a human granted exactly one of them.
	permissions string
	// delegation groups the two agent-delegation limits — see
	// DelegationConfig's doc for why they are grouped (both are limits ON
	// delegation) despite differing in kind (one a resource ceiling, the
	// other structural/correctness). Renamed from the flat agent_turn_cap:
	// "turn cap" read as a per-run quota, which it never was — a child
	// parked in agent_recv yields its slot, so it bounds CONCURRENCY, not
	// turns. The retired spelling is REFUSED at load (UnmarshalYAML), not
	// silently ignored — see errRetiredAgentTurnCapKey.
	delegation DelegationConfig
	// isolationImages maps a backend name (claude-code | kiro | ...) to a
	// USER-PROVIDED agent image for containerized runs. An entry overrides the
	// built-in per-backend default tag and is run AS-IS: never locally built or
	// overlaid (the user owns it), so an absent override degrades with a warning
	// instead of triggering the on-the-fly build. Missing entries keep the
	// built-in default (which IS auto-built when absent).
	isolationImages map[string]string
	// isolationBaseContainerfile is a USER-PROVIDED base Containerfile for
	// locally-built agent images: the on-the-fly build (and `ctxloom container
	// build`) layers the engine's agent stage onto a base built from this file
	// instead of an auto-detected devcontainer / the embedded default base
	// (container/base/Containerfile). Relative paths resolve against the
	// project root. Beats devcontainer auto-detection (locked decision 8).
	isolationBaseContainerfile string
	// isolationDevcontainerBase toggles auto-detecting the project's
	// .devcontainer/devcontainer.json (or .devcontainer.json) as the
	// locally-built agent image's BASE: "an isolated agent should run in the
	// environment the human develops in". Default true
	// (nil = enabled); set false to opt out and keep the embedded default
	// base (or an explicit isolation_base_containerfile) instead. A tri-state
	// pointer like ui.Surround — a plain bool's zero value would default to
	// disabled.
	isolationDevcontainerBase *bool
	// isolationDevcontainerService names the docker-compose service to adopt
	// as the agent image's base when the detected devcontainer.json declares
	// dockerComposeFile — a multi-service compose project does not map to
	// ONE agent container, so this (or the devcontainer.json's own "service"
	// key) is required to resolve one; its absence is a fail-loud finding,
	// never a silent fallback to the default base.
	isolationDevcontainerService string
	// isolationEngines selects which engine fragments compose into the
	// shared multi-engine agent image (locked decision 3: "all engines CAN
	// be present, composition is per build") —
	// claude-code, codex, kiro, opencode today (each via its OWN official
	// installer, one independently-cacheable Containerfile RUN layer). Empty/unset = every
	// known engine (the biggest image, "one instance runs any engine"); an
	// unrecognized name is dropped with a warning, never silently promoted to
	// "use everything".
	isolationEngines []string
	// ui configures the interactive-run terminal layer (the prefix-key viewer
	// and the persistent surround bar). Flag/env never lives here — only
	// presentation preferences; `run --plain-terminal` disables the layer
	// entirely regardless of this section.
	ui UIConfig
	// sessionReapAge is how old a session must be before `ctxloom clean`
	// reclaims its disposable store (~/.ctxloom/sessions/<harp>/ephemeral),
	// in the age grammar `clean --older-than` takes ("30d", "12w", "720h").
	// Empty means the built-in default (DefaultSessionReapAge). A fact about
	// this machine's disk, not project policy: the sessions root is
	// home-global, so this is honoured from the home file and never from
	// the committed project file (layerscope: ScopeMachine).
	sessionReapAge string

	// Runtime-only fields: populated during Load, never part of the persisted
	// config — configDoc (their yaml counterpart) simply omits them, which
	// keeps them out of every marshal exactly like their old yaml:"-" tag did:
	// notably `config show`, which would otherwise dump resolved paths, load
	// warnings, and (worst) the pendingUpgrade's raw []byte config as an
	// integer array.
	appPaths []string     // Resolved .ctxloom directory (at most one)
	appRoot  string       // Project root (parent of .ctxloom directory)
	appDir   string       // Full path to the .ctxloom directory
	source   ConfigSource // Where the configuration was loaded from
	warnings []Warning    // Kind-tagged warnings collected during load

	// pendingUpgrade is set when Load upgraded an older on-disk schema to the
	// current one in memory. The upgraded bytes are NOT persisted automatically;
	// an interactive caller may prompt the user and call CommitUpgrade. Nil when
	// the file was already current. This tracks the PROJECT (or, when no
	// project was found, home) layer only — the same file identity this field
	// named before layering existed — so every existing CommitUpgrade caller
	// keeps working unchanged.
	pendingUpgrade *PendingUpgrade

	// homePendingUpgrade is pendingUpgrade's counterpart for the HOME layer,
	// populated only when a project layer ALSO exists (so home is being read
	// as the lower-precedence layer, not as the effective single source —
	// that case populates pendingUpgrade instead, exactly as before layering).
	// CommitHomeUpgrade persists it, on the same consent rule as
	// PendingUpgrade: the caller prompts and the prompt names the path, so
	// home is never rewritten as a silent side effect of a project-scoped
	// run. Before that existed, home was upgraded in memory on every load and
	// never written back — visible, but never converging (long-ice).
	homePendingUpgrade *PendingUpgrade

	fs afero.Fs // Filesystem for file operations (nil = OS filesystem)

	// injectedFS records whether fs was EXPLICITLY provided (WithFS at Load
	// time, or a later SetFS call) as opposed to defaulted. This exists
	// SOLELY so Save/Manager.Update can tell "a real caller pointed this at a
	// test filesystem, skip the cross-process advisory lock — there are no
	// other processes reading an in-memory fs" apart from "this is the OS
	// filesystem, take the lock" — a distinction c.fs itself can no longer
	// make: loadUncached ALWAYS populates c.fs with a concrete value
	// (afero.NewOsFs() by default), so a "c.fs == nil" check — Save's
	// original guard — is false for EVERY Load()-produced Config, meaning
	// the advisory lock this field exists to gate had never actually fired
	// for a real, on-disk config (found while building Manager.Update's own
	// lock guard: TestUpdate_SerializesConcurrentWritersInProcess lost 13 of
	// 20 concurrent writes with the naive c.fs==nil check, because it always
	// skipped locking). c.fs itself is untouched — every existing consumer
	// that reads it directly (agents.GetAgentDirs, profiles.GetProfileDirs,
	// bundles.WithFS, the remote registry/lockfile options, ...) keeps
	// exactly the same value it always got.
	injectedFS bool

	// execGate gates the bundle EXECUTABLE surfaces (bundle MCP servers + bundle
	// hooks resolved by ResolveBundleMCPServers/ResolveBundleHooks, and prompt
	// command-file exports via LoadCommandExports) when set. nil means UNSET, and
	// ExecutableTrustGate turns that into bundles.AdmitAll — the gate-free
	// management/listing shape, named rather than implied. Read it through that
	// accessor, never directly: a nil reaching bundles.Decide withholds. The
	// operations/run consumers inject it before writing backend settings (TR5);
	// operations can't be imported here, so the gate is a plain bundles.Authorizer
	// func. Never persisted.
	execGate bundles.Authorizer

	// companionSeed memoizes the companion loadout probe for this Config's
	// LIFETIME: probing execs a subprocess per discovered companion (and can
	// PROMPT for consent to do so), and BundleLoader is called repeatedly
	// within one process (hooks, MCP, fragments, assembly) — without this, each
	// call would re-pay that cost and re-ask that question. Deliberately
	// per-Config (not a package var):
	// tests construct fresh Configs and must never observe another test's
	// fake companion output.
	//
	// A VALUE field, and that makes Config NON-COPYABLE — which is the point,
	// not a side effect. govet copylocks now refuses any attempt to copy a
	// Config, so the pass-by-pointer rule this codebase already follows
	// everywhere is enforced by a tool instead of by convention.
	//
	// It was previously a pointer guarded by a package-level mutex, because a
	// test double rebuilt a Config in place (`*cfg = *rebuilt`) to make a
	// profile definition appear mid-run. That simulated a channel production
	// cannot use — config-defined profiles are fixed at load, since nothing
	// rewrites .ctxloom/config.yaml during a run — so it was the test that was
	// wrong, not this field. Production reveals new state exactly one way: bytes
	// land on disk and the next read lists them through a fresh loader.
	companionSeed companionSeedState

	// bundleLoader memoizes the default-shape read-path loader for this
	// Config's lifetime; see BundleLoader and InvalidateBundleLoader. A value
	// mutex is safe here because Config is non-copyable (companionSeed's
	// sync.Once makes it so, and govet copylocks enforces it).
	bundleLoaderMu sync.Mutex
	bundleLoader   *bundles.Loader

	// catalog is the generation's resolved bundle catalog, bound by the Owner
	// (bindGeneration) before the Snapshot carrying this Config is published.
	catalog *bundles.Catalog

	// companionProbe overrides companion-loadout discovery; nil means the real
	// ProbeCompanionLoadouts. The real probe execs whatever companion binaries
	// happen to be on the HOST's PATH, so any test that sets AppPaths (the only
	// guard) silently inherits the developer's machine: the same test passes
	// where ltk is not installed and fails where it is. Tests that assert on an
	// exact command/bundle set must pin this (see DisableCompanionProbe) so the
	// result depends on the fixture, never the host.
	companionProbe bundles.CompanionProber

	// lmDefaultOverlay snapshots what mergeDefaultConfig overlaid into LM (nil
	// when the user configured their own registry). Save strips values that
	// still match it: the overlay is a runtime fallback, and persisting it
	// would pin the user to a snapshot of shipped model defaults.
	lmDefaultOverlay *LMConfig
}

// configDoc mirrors Config's persisted shape with EXPORTED fields and the
// exact same yaml tags Config's fields carried before Phase 3 unexported
// them. It exists SOLELY so yaml.v3's reflection-based (de)serialization has
// something with visible fields to walk: reflection cannot read or write an
// unexported struct field's VALUE, even from code inside this same package —
// that's a language-level rule enforced by the runtime, not a compile-time
// package-boundary check a same-package helper could route around.
//
// Config's MarshalYAML/UnmarshalYAML below round-trip through configDoc, so
// EVERY existing yaml.Marshal(cfg)/yaml.Unmarshal(data, cfg) call site — both
// of this package's own (loadLayeredConfig, ParseConfig) and the one external
// site (internal/adapters/cli/config.go's renderConfigYAML) — keeps working completely
// unchanged, with byte-identical output, because yaml.v3 automatically
// prefers a type's Marshaler/Unmarshaler methods over reflecting its fields
// directly.
//
// Runtime-only fields (appPaths, appRoot, appDir, source, warnings,
// pendingUpgrade, homePendingUpgrade) are deliberately absent here, exactly
// mirroring their old yaml:"-" tag: configDoc IS the persisted-fields subset.
type configDoc struct {
	Version                      int                     `yaml:"version"`
	LM                           LMConfig                `yaml:"llm"`
	Editor                       EditorConfig            `yaml:"editor,omitempty"`
	Settings                     SettingsConfig          `yaml:"config,omitempty"`
	Sync                         SyncConfig              `yaml:"sync,omitempty"`
	Agents                       map[string]agents.Agent `yaml:"agents,omitempty"`
	DefaultAgent                 string                  `yaml:"default_agent,omitempty"`
	Workspace                    string                  `yaml:"workspace,omitempty"`
	DirtyTreeHandler             string                  `yaml:"dirty_tree_handler,omitempty"`
	Runtime                      string                  `yaml:"runtime,omitempty"`
	Permissions                  string                  `yaml:"permissions,omitempty"`
	Delegation                   DelegationConfig        `yaml:"delegation,omitempty"`
	IsolationImages              map[string]string       `yaml:"isolation_images,omitempty"`
	IsolationBaseContainerfile   string                  `yaml:"isolation_base_containerfile,omitempty"`
	IsolationDevcontainerBase    *bool                   `yaml:"isolation_devcontainer_base,omitempty"`
	IsolationDevcontainerService string                  `yaml:"isolation_devcontainer_service,omitempty"`
	IsolationEngines             []string                `yaml:"isolation_engines,omitempty"`
	UI                           UIConfig                `yaml:"ui,omitempty"`
	SessionReapAge               string                  `yaml:"session_reap_age,omitempty"`
}

// toDoc copies c's persisted fields into a configDoc for marshaling.
//
// Like its twin ToFixture (fixture.go), it clones every map and slice rather
// than aliasing c's own. The strongest reason is Draft: Manager.Update hands
// the doc this builds to an arbitrary caller's fn as the package's documented
// WRITE surface, and an fn that mutates a container in place must not be able
// to reach back into the Config the draft was taken from. Cloning also keeps
// the two conversions honest with each other — they are near-identical
// 20-field copies, and one of them silently having weaker ownership than the
// other is exactly how that class of bug happens.
func (c *Config) toDoc() configDoc {
	return configDoc{
		Version:                      c.version,
		LM:                           cloneLMConfig(c.lm),
		Editor:                       cloneEditor(c.editor),
		Settings:                     cloneSettings(c.settings),
		Sync:                         cloneSync(c.sync),
		Agents:                       cloneAgentsMap(c.agents),
		DefaultAgent:                 c.defaultAgent,
		Workspace:                    c.workspace,
		DirtyTreeHandler:             c.dirtyTreeHandler,
		Runtime:                      c.runtime,
		Permissions:                  c.permissions,
		Delegation:                   c.delegation,
		IsolationImages:              maps.Clone(c.isolationImages),
		IsolationBaseContainerfile:   c.isolationBaseContainerfile,
		IsolationDevcontainerBase:    cloneBoolPtr(c.isolationDevcontainerBase),
		IsolationDevcontainerService: c.isolationDevcontainerService,
		IsolationEngines:             slices.Clone(c.isolationEngines),
		UI:                           cloneUIConfig(c.ui),
		SessionReapAge:               c.sessionReapAge,
	}
}

// fromDoc copies a decoded configDoc's fields into c, leaving c's
// runtime-only fields (appPaths, source, warnings, ...) untouched — callers
// that decode INTO an existing partially-populated Config (loadLayeredConfig
// decodes into a cfg that already carries appPaths/appDir/appRoot/source from
// bootstrap) rely on exactly that.
func (c *Config) fromDoc(doc configDoc) {
	c.version = doc.Version
	c.lm = doc.LM
	c.editor = doc.Editor
	c.settings = doc.Settings
	c.sync = doc.Sync
	c.agents = doc.Agents
	c.defaultAgent = doc.DefaultAgent
	c.workspace = doc.Workspace
	c.dirtyTreeHandler = doc.DirtyTreeHandler
	c.runtime = doc.Runtime
	c.permissions = doc.Permissions
	c.delegation = doc.Delegation
	c.isolationImages = doc.IsolationImages
	c.isolationBaseContainerfile = doc.IsolationBaseContainerfile
	c.isolationDevcontainerBase = doc.IsolationDevcontainerBase
	c.isolationDevcontainerService = doc.IsolationDevcontainerService
	c.isolationEngines = doc.IsolationEngines
	c.ui = doc.UI
	c.sessionReapAge = doc.SessionReapAge

	// lm.Configs is pre-populated before every decode precisely so downstream
	// code may write into it, and a document is free to null it back out.
	// Restoring it here rather than at each decode site is what covers the
	// layered Load path as well as ParseConfig, which is where the guard used
	// to live alone.
	if c.lm.Configs == nil {
		c.lm.Configs = make(map[string]LLMConfig)
	}
}

// MarshalYAML implements yaml.Marshaler so yaml.Marshal(cfg) — cli/config.go's
// `config show`/`config get` and this package's own layer-remarshal step —
// keeps producing the same shape it always has, now that Config's fields are
// unexported and no longer reflectable. See configDoc's doc for why this is
// necessary rather than optional.
func (c *Config) MarshalYAML() (any, error) {
	if c == nil {
		return nil, nil
	}
	return c.toDoc(), nil
}

// UnmarshalYAML implements yaml.Unmarshaler so yaml.Unmarshal(data, cfg) —
// ParseConfig and loadLayeredConfig's merged-layer decode — keeps populating
// Config exactly as it did when its fields were exported. See configDoc's doc
// for why this is necessary rather than optional. Only the persisted fields
// configDoc carries are touched; runtime-only fields already set on c
// (appPaths, source, ...) are left alone.
//
// doc is seeded from c's CURRENT state (c.toDoc()), not a zero value, before
// decoding — reproducing yaml.v3's decode-into-existing-value semantics: a
// key absent from the document leaves the corresponding field exactly as it
// was, rather than resetting it to zero. loadUncached relies on this: it
// pre-populates cfg's LM.Configs with a non-nil empty map before this
// Unmarshal runs, specifically so a document that never mentions "llm" still
// leaves that map non-nil for downstream
// code that assumes so. Decoding into a fresh zero-value doc would silently
// discard that pre-population whenever a key was absent — the same
// silent-no-op shape this codebase treats as its characteristic bug.
func (c *Config) UnmarshalYAML(node *yaml.Node) error {
	if name, found := findRetiredAgentKey(node, agents.RetiredLLMKey); found {
		return fmt.Errorf("agent %q: %w", name, agents.ErrRetiredLLMKey)
	}
	if name, found := findRetiredAgentKey(node, agents.RetiredCoordinatorKey); found {
		return fmt.Errorf("agent %q: %w", name, agents.ErrRetiredCoordinatorKey)
	}
	if mappingValue(node, retiredAgentTurnCapKey) != nil {
		return errRetiredAgentTurnCapKey
	}
	if label, found := findRetiredEntryKey(mappingValue(mappingValue(node, "llm"), "configs"), RetiredLLMEnvKey); found {
		return fmt.Errorf("llm config %q: %w", label, ErrRetiredLLMEnvKey)
	}
	doc := c.toDoc()
	if err := node.Decode(&doc); err != nil {
		return err
	}
	c.fromDoc(doc)
	return nil
}

// retiredAgentTurnCapKey is the pre-rename, flat-top-level spelling of
// DelegationConfig.Concurrency ("turn cap" read as a per-run quota; the field
// is a concurrency ceiling, not a turn count — see DelegationConfig's doc).
// Refused at load rather than ignored, for the same reason agents.RetiredLLMKey
// is: this decode path is lenient (no KnownFields), so an untouched
// `agent_turn_cap:` would be dropped in silence and the concurrency ceiling
// would silently fall back to the built-in default — the same silent-ignore
// shape that has already cost real diagnosis time on a different renamed key
// in this codebase.
const retiredAgentTurnCapKey = "agent_turn_cap"

// errRetiredAgentTurnCapKey names the current spelling, because a rename that
// leaves people guessing has moved the cost rather than paid it.
var errRetiredAgentTurnCapKey = errors.New(
	"config uses the retired key 'agent_turn_cap:'; it is now 'delegation.concurrency:' — " +
		"same resource ceiling (concurrently EXECUTING delegated child turns), correctly named and grouped under 'delegation:'")

// RetiredLLMEnvKey is the REMOVED per-label environment map,
// llm.configs.<label>.env. A removal, not a rename: ctxloom no longer carries
// an engine's environment or credentials in its config at all. Every engine
// authenticates itself, and the process ctxloom launches inherits the ambient
// environment (an isolated run forwards it across the boundary), so a
// variable exported in the shell that runs ctxloom reaches the engine with
// ctxloom neither seeing nor storing it. The key was retired because its only
// documented use was credentials, and the project config file it invited
// them into is committed.
//
// Refused at load for the same reason agents.RetiredLLMKey is: this decode
// path is lenient, so an untouched `env:` would decode into a Body key that
// nothing reads, and a user would believe their variable reached the engine.
// The mock's test-control knobs, which once rode this key, live under their
// own key (see backends.MockConfig.Control).
const RetiredLLMEnvKey = "env"

// ErrRetiredLLMEnvKey says what replaced the key rather than only that it is
// gone, since "unknown key" leaves the reader to guess where their variable
// should go instead.
var ErrRetiredLLMEnvKey = errors.New(
	"llm config uses the removed key 'env:'; ctxloom no longer carries engine credentials or environment " +
		"in its config — the engine reads them from the ambient environment, so export the variable in the " +
		"shell that runs ctxloom and delete the key")

// findRetiredAgentKey returns the first agent carrying the named retired key,
// and whether one was found. It walks the NODE rather than the decoded value
// because the decode is what loses the information: this path does not set
// KnownFields, so an untouched retired key is dropped in silence — `engine:`
// leaves the binding falling back to the profiles' llm (a different model,
// chosen by nobody), and `coordinator:` leaves a binding written to delegate
// quietly unable to.
//
// Walking the tree is also what separates a KEY from the same word appearing
// as a profile name, a model string, or prose.
func findRetiredAgentKey(node *yaml.Node, key string) (string, bool) {
	return findRetiredEntryKey(mappingValue(node, "agents"), key)
}

// findRetiredEntryKey returns the first entry of a name-keyed section
// (agents.<name>, llm.configs.<label>) whose mapping carries key, and whether
// one was found. A nil or non-mapping section finds nothing.
func findRetiredEntryKey(section *yaml.Node, key string) (string, bool) {
	if section == nil || section.Kind != yaml.MappingNode {
		return "", false
	}
	// Content pairs as [key, value, key, value, ...]; entries are already in
	// document order, so the name reported is stable across runs.
	for i := 0; i+1 < len(section.Content); i += 2 {
		name := section.Content[i].Value
		if mappingValue(section.Content[i+1], key) != nil {
			return name, true
		}
	}
	return "", false
}

// mappingValue returns the value node for key in a mapping node, or nil when
// the node is not a mapping or has no such key.
func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil {
		return nil
	}
	// A document node wraps its single mapping child.
	if node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
		node = node.Content[0]
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// EditorConfig holds editor-related configuration.
type EditorConfig struct {
	Command string   `mapstructure:"command" yaml:"command,omitempty"` // Editor command (default: nano)
	Args    []string `mapstructure:"args" yaml:"args,omitempty"`       // Additional arguments
}

// UIConfig holds the interactive-run terminal-layer preferences.
type UIConfig struct {
	// PrefixKey is the keystroke that engages the agent-observation viewer
	// during an interactive run ("ctrl-]" by default; press it twice to send
	// one literal prefix byte to the engine). Control keys only — a printable
	// prefix would swallow ordinary typing.
	PrefixKey string `mapstructure:"prefix_key" yaml:"prefix_key,omitempty"`
	// Surround toggles the persistent bottom status bar (harp · agent · engine
	// │ children digest │ prefix hint). Default true; nil means unset.
	Surround *bool `mapstructure:"surround" yaml:"surround,omitempty"`
}

// DelegationConfig groups the project-wide agent-delegation settings. They are
// grouped under one key because each governs DELEGATION — not because they
// share a mechanism, which they do not: Concurrency is a resource ceiling,
// Depth is a structural/correctness limit. See each field's doc.
type DelegationConfig struct {
	// Concurrency is the maximum number of delegated child turns EXECUTING
	// at once (agentcoord/coord's execution-slot cap — each is a live engine
	// process; a child waiting on a message yields its slot). <= 0 means
	// "use the built-in default" (coord.agentConcurrencyCap). This bounds
	// resource load only, not correctness — the coordinator's own state is
	// safe under real concurrency by construction. Raise it for more
	// delegation parallelism; lower it on a small machine.
	Concurrency int `yaml:"concurrency,omitempty"`
	// Depth is the maximum nesting depth of the delegation tree: the
	// session owner is depth 0, its subagents depth 1, theirs depth 2, and
	// so on. <= 0 means "use the built-in default" (coord.agentDepthCap,
	// currently 1: flat fan-out, no grandchildren). A run AT the cap may not
	// itself call agent_run, and its runner is a LEAF — it never receives
	// the coordinator-only MCP tools (agent_run/roster/agent_stop/
	// agent_fetch_artifact). Unlike Concurrency this IS a correctness
	// setting: raising it above 1 gives those tools to non-root agents,
	// which can leave an agent holding an inbox plus a child roster waiting
	// on children it never spawned.
	Depth int `yaml:"depth,omitempty"`
}

// DefaultDelegationDepth is the built-in default for delegation.depth (flat
// fan-out: the session owner may spawn subagents, a subagent may not spawn
// further) — the SOLE numeric source both GetDelegationDepth (this package)
// and coord.agentDepthCap resolve from, so a runner (which loads its own
// *config.Config independently of the coordinator process) and the
// coordinator itself always agree on the resolved cap without ever
// exchanging it over the wire.
const DefaultDelegationDepth = 1

// DefaultSessionReapAge is the built-in default for session_reap_age: the
// age past which `ctxloom clean` reclaims a session's disposable store when
// neither the home config nor --older-than states one. Thirty days is long
// enough that a session someone is still resuming keeps its scratch, and
// short enough that the store — which every run now adds a directory to —
// stays bounded.
const DefaultSessionReapAge = "30d"

// SessionReapAge returns the configured session_reap_age, defaulting to
// DefaultSessionReapAge. The value is an age in `clean --older-than`'s
// grammar and is parsed there, not here: one grammar, one parser.
func (c *Config) SessionReapAge() string {
	if c.sessionReapAge == "" {
		return DefaultSessionReapAge
	}
	return c.sessionReapAge
}

// DefaultUIPrefixKey is the default viewer prefix key (decision O2 of the
// agent-io-observation plan: Ctrl-], explicitly not ESC).
const DefaultUIPrefixKey = "ctrl-]"

// UIPrefixKey returns the configured viewer prefix key, defaulting to Ctrl-].
func (c *Config) UIPrefixKey() string {
	if c.ui.PrefixKey == "" {
		return DefaultUIPrefixKey
	}
	return c.ui.PrefixKey
}

// UISurroundEnabled reports whether the persistent surround bar is enabled
// (default true; `ui.surround: false` opts out).
func (c *Config) UISurroundEnabled() bool {
	return c.ui.Surround == nil || *c.ui.Surround
}

// IsolationImageFor returns the user-provided agent image override for the
// named backend's containerized runs, or "" when the backend keeps the built-in
// default image (nil-safe).
func (c *Config) IsolationImageFor(backend string) string {
	if c == nil {
		return ""
	}
	return c.isolationImages[backend]
}

// IsolationBaseContainerfilePath returns the user-provided base Containerfile
// for locally-built agent images, resolved against the project root when
// relative ("" = the embedded default base; nil-safe).
func (c *Config) IsolationBaseContainerfilePath() string {
	if c == nil || c.isolationBaseContainerfile == "" {
		return ""
	}
	p := c.isolationBaseContainerfile
	if !filepath.IsAbs(p) && c.appRoot != "" {
		p = filepath.Join(c.appRoot, p)
	}
	return p
}

// IsolationDevcontainerBaseEnabled reports whether devcontainer auto-detection
// is enabled for locally-built agent images (default true — nil means unset;
// nil-safe).
func (c *Config) IsolationDevcontainerBaseEnabled() bool {
	return c == nil || c.isolationDevcontainerBase == nil || *c.isolationDevcontainerBase
}

// GetEditorCommand returns the editor binary and arguments to use. This is the
// single editor-resolution policy: config (editor.command, with editor.args
// appended), then the VISUAL and EDITOR environment variables, then nano.
// Multi-word values like "code --wait" are whitespace-split into binary +
// leading args (strings.Fields — full shell quoting is not supported).
func (c *Config) GetEditorCommand() (string, []string) {
	if bin, args := splitEditorCommand(c.editor.Command); bin != "" {
		return bin, append(args, c.editor.Args...)
	}
	return EditorFromEnv()
}

// EditorFromEnv resolves the editor from the environment alone: VISUAL, then
// EDITOR, then nano. It exists for callers that must run BEFORE any config is
// loaded (e.g. `config edit`, which edits a possibly-broken config), so
// they share the env half of GetEditorCommand's policy instead of duplicating
// it. Values are whitespace-split like GetEditorCommand.
func EditorFromEnv() (string, []string) {
	for _, key := range []string{"VISUAL", "EDITOR"} {
		if bin, args := splitEditorCommand(os.Getenv(key)); bin != "" {
			return bin, args
		}
	}
	return "nano", nil
}

// splitEditorCommand splits an editor value into binary + args on whitespace.
// Quoting is intentionally not supported; a binary whose path contains spaces
// must be configured via editor.command + editor.args instead. An empty or
// blank value returns "".
func splitEditorCommand(value string) (string, []string) {
	fields := strings.Fields(value)
	switch len(fields) {
	case 0:
		return "", nil
	case 1:
		return fields[0], nil
	default:
		return fields[0], fields[1:]
	}
}

// DefaultAgentProfiles returns the profiles composed by the always-bound
// default agent (Config.DefaultAgent) — the single "the default profile set"
// accessor that replaced GetDefaultProfiles/ExplicitDefaultProfiles after
// profiles.defaults was retired. It resolves through Config.Agent, the same
// lookup operations.ResolveAgent takes, so the default set is exactly what a
// bare `ctxloom run` binds. Returns nil when no default agent is configured or
// the named agent is not defined.
func (c *Config) DefaultAgentProfiles() []string {
	if c == nil || c.defaultAgent == "" {
		return nil
	}
	sub, ok := c.Agent(c.defaultAgent)
	if !ok {
		return nil
	}
	return sub.Profiles
}

// PrimaryLabel returns the config label playing the primary (coding/
// interactive) role. Fallback chain: the configured defaults.primary, else
// the sole configured label if exactly one exists, else "" — callers then
// resolve to the built-in default backend.
func (c *Config) PrimaryLabel() string {
	if c.lm.Defaults.Primary != "" {
		return c.lm.Defaults.Primary
	}
	if len(c.lm.Configs) == 1 {
		for label := range c.lm.Configs {
			return label
		}
	}
	return ""
}

// FastLabel returns the config label playing the fast (compression) role.
// Fallback chain: defaults.fast → defaults.primary (via PrimaryLabel).
func (c *Config) FastLabel() string {
	if c.lm.Defaults.Fast != "" {
		return c.lm.Defaults.Fast
	}
	return c.PrimaryLabel()
}

// ResolveLLM looks a config label up in the registry and returns the backend
// type and model it specifies. The model is read only from the entry's own body
// — never by branching on the backend name.
//
// An empty label means "no label was named" (a bare invocation, e.g.
// `container check` with no backend argument) and is resolved through
// PrimaryLabel() rather than looked up directly — c.lm.Configs[""] is never a
// real entry, so without this a bare invocation silently fell through to the
// built-in default backend instead of the project's configured primary
// (unsent-refinish). This is a single substitution, not a loop: PrimaryLabel()
// itself may also return "" (no defaults.primary and not exactly one
// configured label), in which case the lookup below simply misses.
//
// A MISS RETURNS DefaultLLM AND RAISES NOTHING HERE, and the reason is worth
// keeping: this function cannot tell a broken label from a legitimate one.
//
// A label that is not an `llm:` entry but IS a known BACKEND NAME is fully
// supported — operations.ResolveBackend resolves `llm: mock` to the mock
// backend with no config entry at all. That fact lives in the backend
// registry, which this package does not (and must not) import, so a refusal
// raised here fires on a correct configuration. The degradation audit did
// exactly that for one commit, and the coord spawner suite caught it.
//
// The check therefore lives ONE LAYER UP, in operations.ResolveBackend, which
// is the only place that knows BOTH halves — not an llm entry AND not a known
// backend — and is therefore the only place where "this label names nothing"
// is actually decidable. See that function for the refusal and its reasoning.
func (c *Config) ResolveLLM(label string) (backend, model string) {
	if label == "" {
		label = c.PrimaryLabel()
	}
	entry, ok := c.lm.Configs[label]
	if !ok {
		return DefaultLLM, ""
	}
	backend = entry.EffectiveType()
	if m, ok := entry.Body["model"].(string); ok {
		model = m
	}
	return backend, model
}

// GetDefaultLLM returns the backend type for the primary role's label.
func (c *Config) GetDefaultLLM() string {
	backend, _ := c.ResolveLLM(c.PrimaryLabel())
	return backend
}

// GetCompactionLLM returns the backend type for the fast (compression) role.
func (c *Config) GetCompactionLLM() string {
	backend, _ := c.ResolveLLM(c.FastLabel())
	return backend
}

// GetCompactionModel returns the model for the fast (compression) role.
// Empty means the backend substitutes its own lightweight model.
func (c *Config) GetCompactionModel() string {
	_, model := c.ResolveLLM(c.FastLabel())
	return model
}

// GetToolReflectBytes returns the tool-result size at or above which the
// PostToolUse reflect hook fires, and whether the hook is enabled at all.
//
// The default is agent.DefaultToolReflectBytes, which is shared with the
// distiller so the two enforcement points cannot disagree. A negative value
// disables the hook, which is why "enabled" is returned rather than inferred
// from a zero.
func (c *Config) GetToolReflectBytes() (threshold int, enabled bool) {
	switch {
	case c.settings.ToolReflectBytes < 0:
		return 0, false
	case c.settings.ToolReflectBytes > 0:
		return c.settings.ToolReflectBytes, true
	default:
		return agent.DefaultToolReflectBytes, true
	}
}

// GetEssenceMaxChars returns the target size of a finished session essence,
// defaulting to agent.DefaultEssenceChars.
//
// This is a TARGET handed to the distilling model, not the hard ceiling. The
// ceiling is memory.MaxEssenceChars, which is derived from the tool-result cap
// MCP clients enforce and refuses anything larger. A target above the ceiling
// would instruct the model to produce output the code must then reject, which
// is the exact contradiction the absolute budget replaced; the compactor
// clamps rather than honouring one.
func (c *Config) GetEssenceMaxChars() int {
	if c.settings.EssenceMaxChars > 0 {
		return c.settings.EssenceMaxChars
	}
	return agent.DefaultEssenceChars
}

// ShouldSilenceUnsupported reports whether capability-loss lines are
// suppressed. Defaults to false: a loss stays audible unless the user has said
// they already know.
func (c *Config) ShouldSilenceUnsupported() bool {
	return c.settings.SilenceUnsupported
}

// ShouldUseDistilled reports whether to prefer distilled fragment/prompt
// versions. Defaults to true.
func (c *Config) ShouldUseDistilled() bool {
	return c.settings.ShouldUseDistilled()
}

// ShouldSignByDefault reports whether publish commands (fragment push,
// command push) should sign unless --no-sign is given (spec §7A.3,
// sign.default). Defaults to false.
func (c *Config) ShouldSignByDefault() bool {
	return c.settings.ShouldSignByDefault()
}

// SignKey returns the configured sign.key override (a --key-equivalent
// fingerprint, public key path, or ssh-agent key name/comment), or "" when
// unset — meaning the zero-config discovery chain (internal/adapters/signing/agentkey)
// should be used instead.
func (c *Config) SignKey() string {
	return c.settings.SignKey()
}

// GetProfileLoader returns a profiles.Loader for this config's ctxloom paths.
// It wires a remote resolver from the remotes registry so the loader can qualify
// legacy bare bundle refs with the remote each profile was installed from.
func (c *Config) GetProfileLoader() *profiles.Loader {
	return profiles.NewLoader(profiles.GetProfileDirs(c.fs, c.appPaths), c.ProfileLoaderOptions()...)
}

// ProfileLoaderOptions returns the loader options EVERY profile-loader factory
// over this config must wire, so no two factories can disagree about which
// filesystem is read, how a bundle ref canonicalizes, or which profiles exist.
// A factory differs from GetProfileLoader only in the DIRECTORIES it searches
// (operations.profileLoader synthesizes one for a fresh install); the option set
// is not a place for it to differ.
func (c *Config) ProfileLoaderOptions() []profiles.LoaderOption {
	var opts []profiles.LoaderOption
	if c.fs != nil {
		opts = append(opts, profiles.WithFS(c.fs))
	}
	if resolve := c.ProfileRemoteResolver(); resolve != nil {
		opts = append(opts, profiles.WithRemoteResolver(resolve))
	}
	if resolveURL := c.ProfileRemoteURLResolver(); resolveURL != nil {
		opts = append(opts, profiles.WithRemoteURLResolver(resolveURL))
	}
	// Seed remote profiles read from the git clone cache at their locked SHA, so
	// every consumer of the loader sees them as references without a materialized
	// copy on disk (the profile-side mirror of SeededBundleLoader).
	return append(opts, c.ProfileSeedOptions()...)
}

// ProfileSeedOptions returns the loader option that seeds the profiles shipped
// INSIDE bundles (the ungated, compound bundle item kind), keyed by their
// "<bundle>#profiles/<name>" ref, or nil when there are none. Exposed (like
// ProfileRemoteResolver/ProfileRemoteURLResolver) so other profile-loader
// factories — e.g. operations.profileLoader — wire the exact same seed as
// GetProfileLoader and the two never disagree about which profiles exist.
//
// Top-level remote "<url>@profiles/<name>" distribution was retired: profiles
// now arrive ONLY inside bundles, so this is the sole profile seed source.
func (c *Config) ProfileSeedOptions() []profiles.LoaderOption {
	bundleSeed := c.loadBundleProfileSeed()
	if len(bundleSeed) == 0 {
		return nil
	}
	return []profiles.LoaderOption{profiles.WithSeededProfiles(bundleSeed)}
}

// loadBundleProfileSeed walks every bundle visible to this config — fs-installed
// local bundles plus lockfile-listed remote bundles read from the git clone
// cache — and returns the profiles they ship, parsed and keyed by their
// canonical "<bundle>#profiles/<name>" ref, ready to seed a profiles.Loader.
//
// Profiles are an ungated, COMPOUND bundle item kind: they travel inside the
// bundle YAML, so a pulled bundle's profiles are already on disk / in cache —
// this is the step that surfaces them to the SHARED profile loader, so a bundle
// profile resolves, lists, and runs exactly like a top-level or local profile.
// The profile DEFINITION is never trust-gated here (there is no trust.ItemKind
// for profiles, and nothing is baselined); its constituent fragments/commands
// still gate at content assembly and any mcp/hooks it pulls in still gate at the
// exec choke. Returns nil when no visible bundle ships a profile.
func (c *Config) loadBundleProfileSeed() map[string]*profiles.Profile {
	if len(c.appPaths) == 0 {
		return nil
	}
	loaded := make(map[string]*profiles.Profile)
	// The READS, not a listing plus a path round trip: a companion loadout and a
	// pinned remote document have no file to resolve back through, and their
	// profiles vanished when this asked for one.
	for _, read := range c.BundleLoader().Reads() {
		bundle := read.Bundle
		if bundle.ProfileCount() == 0 {
			continue
		}
		// The read's display name is the bundle's full handle (the canonical
		// ref for pinned remote content, the relative path for a local
		// bundle); bundle.Name is only the file's base, so canonicalize from
		// the display name.
		bundleRef, err := remote.CanonicalBundleRef(read.DisplayName())
		if err != nil {
			// This bundle's profiles are dropped, and the drop is announced:
			// a seed key built on an unparsed source would be a key nothing
			// ever looks up, so the profiles would go missing either way —
			// silently in the first case, diagnosably in this one.
			clidiag.Warn("ctxloom", "bundle %q ships %d profile(s) that cannot be seeded: %v",
				read.DisplayName(), bundle.ProfileCount(), err)
			continue
		}
		sourceURL := bundleProfileSourceURL(bundleRef)
		for _, profName := range bundle.ProfileNames() {
			p := cloneBundleProfile(bundle.Profiles[profName])
			key := bundleRef + remote.ProfileSelector + profName
			// Resolve the profile's short same-repo leaf refs (bundles/fragments/
			// prompts/bundle_items) against the bundle's own source, exactly as a
			// seeded top-level remote profile does; a canonical "<bundle>#profiles/
			// <name>" parent ref passes through unchanged. No version is pinned here:
			// the lockfile already pins the bundle, and the version-agnostic leaf
			// identities let the read path honor that pin.
			p.ResolveShortRefs(sourceURL, "")
			p.Name = key
			// Sentinel path marks the profile read-only (Save/Delete refuse): like a
			// remote profile, a bundle profile is edited at its source, not locally.
			p.Path = profiles.SeededProfilePathPrefix + key
			// The VERIFIED publisher identity of the bundle this profile ships
			// inside (bundle.Signer() — stamped only by a load path that already
			// checked a signature against the trust root; "" for unsigned/
			// untrusted). resolveProfileRecursive threads this into
			// ResolvedProfile.Signer so a trusted-publisher profile's directly-
			// declared hooks/mcp are trusted-signer-allowed exactly like
			// bundle-declared ones (B2, gateProfileExec parity).
			p.Signer = bundle.Signer()
			loaded[key] = &p
		}
	}
	if len(loaded) == 0 {
		return nil
	}
	rewriteRetiredSeedParents(loaded)
	return loaded
}

// rewriteRetiredSeedParents rewrites seeded bundle-profile parents authored in
// the retired top-level "@profiles/" grammar to their bundle-shipped successor.
// Seeded profiles arrive already parsed and never pass through the loader's
// document upgrade pipeline, so this applies the same discovery-based rewrite
// against the full seed: to the one seeded bundle profile the repo ships under
// that name, verbatim when unmatched or ambiguous (profiles/upgrade.go owns the
// rule). In-memory only — a seeded profile is read-only and migrates at its
// source.
func rewriteRetiredSeedParents(loaded map[string]*profiles.Profile) {
	for _, p := range loaded {
		for i, parent := range p.Parents {
			if url, name, ok := remote.SplitRetiredProfileRef(parent); ok {
				if successor, found := profiles.FindBundleProfileKey(loaded, url, name); found {
					p.Parents[i] = successor
				}
			}
		}
	}
}

// cloneBundleProfile returns a copy of a bundle profile safe to mutate
// (ResolveShortRefs rewrites refs in place). The bundle loader caches parsed
// bundles, so the profile's slices are shared with that cache and concurrent
// profile-loader builds — clone exactly the slices ResolveShortRefs touches so
// canonicalization never corrupts the cached bundle or races another reader.
func cloneBundleProfile(bp bundles.BundleProfile) bundles.BundleProfile {
	p := bp
	p.Bundles = append([]string(nil), bp.Bundles...)
	p.Parents = append([]string(nil), bp.Parents...)
	p.Commands = append([]string(nil), bp.Commands...)
	p.Skills = append([]string(nil), bp.Skills...)
	p.BundleItems = append([]string(nil), bp.BundleItems...)
	p.Fragments = append([]profiles.FragmentRef(nil), bp.Fragments...)
	return p
}

// bundleProfileSourceURL returns the source a bundle profile's short same-repo
// refs resolve against: the bundle's repo URL for a remote bundle, or the
// ctxloom:local token for a project-local bundle.
func bundleProfileSourceURL(bundleRef string) string {
	if ref, err := remote.ParseReference(bundleRef); err == nil && ref.URL != "" {
		return ref.URL
	}
	return remote.LocalSource
}

// FS returns the injected filesystem, or nil for the OS default. It lets callers
// outside this package (e.g. operations' trust store + gate) thread the same
// filesystem the config's own loaders use, so a virtualized fs in tests — and
// the OS fs in production — stay consistent across every store read/write.
func (c *Config) FS() afero.Fs {
	return c.fs
}

// registryFSOptions threads the injected filesystem into a remote registry
// constructor (matching the resolvers below). Empty for the OS default.
func (c *Config) registryFSOptions() []remote.RegistryOption {
	if c.fs != nil {
		return []remote.RegistryOption{remote.WithRegistryFS(c.fs)}
	}
	return nil
}

// lockfileFSOptions threads the injected filesystem into a remote lockfile
// manager so lockfile reads honor c.fs alongside the registry reads. Empty for
// the OS default.
func (c *Config) lockfileFSOptions() []remote.LockfileOption {
	if c.fs != nil {
		return []remote.LockfileOption{remote.WithLockfileFS(c.fs)}
	}
	return nil
}

// ProfileRemoteResolver returns a function mapping a profile's local name to the
// short remote it was installed from, backed by the remotes registry. Nil when no
// registry is available (the loader then reads profiles verbatim). Exposed so
// other profile-loader factories (e.g. operations) wire the same qualification.
func (c *Config) ProfileRemoteResolver() func(string) string {
	if len(c.appPaths) == 0 {
		return nil
	}
	registry, err := remote.NewRegistry(paths.RemotesPath(c.appPaths[0]), c.registryFSOptions()...)
	if err != nil {
		return nil
	}
	return func(name string) string {
		short, _ := registry.ResolveItemRemote(name)
		return short
	}
}

// ProfileRemoteURLResolver returns a function mapping a remote alias to its
// canonical repo URL, backed by the remotes registry. Paired with
// ProfileRemoteResolver, it lets the profile loader rewrite a legacy profile's
// bare/alias bundle refs to their canonical URL form on load. Nil when no
// registry is available (the loader then reads bundle refs verbatim).
func (c *Config) ProfileRemoteURLResolver() func(string) string {
	if len(c.appPaths) == 0 {
		return nil
	}
	registry, err := remote.NewRegistry(paths.RemotesPath(c.appPaths[0]), c.registryFSOptions()...)
	if err != nil {
		return nil
	}
	return func(alias string) string {
		rem, err := registry.Get(alias)
		if err != nil || rem == nil {
			return ""
		}
		return rem.URL
	}
}

// ParseConfig unmarshals raw YAML into a Config WITHOUT overlaying the embedded
// default registry. Unlike Load it does not read from disk, validate, upgrade,
// or merge defaults — callers that need the raw registry entries (e.g. init
// reading the shipped default-config) use this so the role markers and exact
// entries survive untouched.
func ParseConfig(data []byte) (*Config, error) {
	cfg := &Config{
		lm: LMConfig{Configs: make(map[string]LLMConfig)},
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}
	return cfg, nil
}

// deepCopyBody clones an LLMConfig.Body recursively (nested maps and slices —
// the shapes yaml.Unmarshal produces), so a copy's mutations never reach the
// original. Nil in, nil out.
func deepCopyBody(body map[string]any) map[string]any {
	if body == nil {
		return nil
	}
	out := make(map[string]any, len(body))
	for k, v := range body {
		out[k] = deepCopyValue(v)
	}
	return out
}

// deepCopyValue clones the YAML-decoded value shapes that can alias storage.
// Scalars are returned as-is.
func deepCopyValue(v any) any {
	switch val := v.(type) {
	case map[string]any:
		return deepCopyBody(val)
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = deepCopyValue(item)
		}
		return out
	default:
		return v
	}
}

// warn appends a formatted warning of kind k. Every load-path degradation in
// this file records one, and the strict-startup gate keys exclusively on this
// slice, so having one spelling of "record and continue" is what keeps a new
// degradation from being written as a zap-only line nothing can see.
func (c *Config) warn(k WarningKind, format string, args ...any) {
	c.warnings = append(c.warnings, Warning{Kind: k, Text: fmt.Sprintf(format, args...)})
}

// GetBundleDirs returns the project's AUTHORED bundle directories — the
// committed content tree (.ctxloom/content/bundles), NOT the gitignored cache.
// This is the set every authored-bundle path resolves against: `bundle create`
// writes here, `bundle list` lists it, and `sign --all` signs exactly it (a
// publishing repo's bundles ARE this directory). The cache
// (paths.CacheBundlesPath) holds remote-pull artifacts the project has no authority
// to author or sign, so it is deliberately absent.
func (c *Config) GetBundleDirs() []string {
	fs := c.getFS()
	var dirs []string
	for _, appPath := range c.appPaths {
		bundleDir := paths.LocalBundlesPath(appPath)
		if info, err := fs.Stat(bundleDir); err == nil && info.IsDir() {
			dirs = append(dirs, bundleDir)
		}
	}
	return dirs
}

// BundleReaderDirs returns the project's authored bundle directories WITHOUT
// filtering on whether they exist yet.
//
// GetBundleDirs filters, and that is right for its callers: they check a path is
// safely under a real directory, or report which directories were searched. It
// is wrong for the READER, because the loader is now built once per Config and
// its readers keep whatever dirs they were handed. A project whose bundles
// directory did not exist at first resolve — `bundle create` in a fresh project
// is exactly that — would give the reader an empty search path that no
// invalidation could repair, since invalidation drops the memoized READS and
// never rebuilds the readers.
//
// Passing the configured dirs unconditionally costs nothing: localFSReader.Read
// already skips a directory that is not there.
func (c *Config) BundleReaderDirs() []string {
	dirs := make([]string, 0, len(c.appPaths))
	for _, appPath := range c.appPaths {
		dirs = append(dirs, paths.LocalBundlesPath(appPath))
	}
	return dirs
}

// BundleLoaderOption configures how a Config builds its bundle loader. It is a
// CONFIG-level option, not a loader one: what a Config assembles is a set of
// READERS, and "which filesystem do the project bundles come from" is a
// question about that assembly rather than about the loader that composes it.
type BundleLoaderOption func(*bundleLoaderConfig)

type bundleLoaderConfig struct {
	fs              afero.Fs
	extraReaders    []bundles.Reader
	versionResolver bundles.BundleVersionResolver
}

// WithBundleLoaderFS overrides the filesystem the PROJECT reader reads from
// (tests that pin a bundle set on a memory fs). It replaces the Config's own
// filesystem rather than adding a second source, so real on-disk bundles cannot
// leak into a fixture's result.
func WithBundleLoaderFS(fsys afero.Fs) BundleLoaderOption {
	return func(c *bundleLoaderConfig) { c.fs = fsys }
}

// WithExtraBundleReaders appends further sources to the loader — the seam a
// test uses to pin content that would otherwise come off the host (a fake
// companion, a synthetic pinned tree). Later readers win a name collision, so
// an extra reader shadows a project bundle of the same name.
func WithExtraBundleReaders(readers ...bundles.Reader) BundleLoaderOption {
	return func(c *bundleLoaderConfig) { c.extraReaders = append(c.extraReaders, readers...) }
}

// WithBundleVersionResolver overrides how the loader materializes a specific
// historical commit-version of a bundle. The default is this Config's own
// resolver (the clone cache for remote refs, the project's git history for
// local ones); a test injects a fake so a pinned "@<commit>" ask is answerable
// without a repository.
func WithBundleVersionResolver(resolver bundles.BundleVersionResolver) BundleLoaderOption {
	return func(c *bundleLoaderConfig) { c.versionResolver = resolver }
}

// BundleLoader returns the read-path bundle loader: a bundles.Loader composed
// of one reader per SOURCE this session can see — the project's own bundle
// directories, every remote bundle in the active lockfile (pinned, read out of
// the local clone cache or its installed tree), and every discovered
// companion's loadout.
//
// This is what replaced the anonymous seed map. The old shape gathered remote
// bundles, companion loadouts and their trust facts into one
// map[string]*Bundle behind a "<seeded>:" path sentinel, which meant the
// content of three sources arrived with their origins erased and their
// signature facts already collapsed into a single stamped string. Each source
// is now a reader that reports what it read, on the record, on three axes.
//
// Failures are degraded gracefully (CLAUDE.md fault tolerance): a missing
// lockfile, unregistered remote, single bad SHA, or unreachable/invalid
// companion loadout produces a diagnostic and the loader serves the rest.
//
// It takes NO form preference: raw-vs-distilled is a PROCESS-stage decision
// (docs/design/engine-delivery-seam.design.md), so a caller that reads content
// names the form it wants at the read itself — see ShouldUseDistilled.
func (c *Config) BundleLoader(opts ...BundleLoaderOption) *bundles.Loader {
	// Memoized for the DEFAULT shape only. This factory was called 16 times
	// across the tree and `ctxloom doctor` went through 22 loader builds in one
	// run, each re-walking the bundle directories and re-parsing every bundle to
	// produce the same answer.
	//
	// Only the no-option shape is shared, and that is not a compromise: exactly
	// ONE production caller passes an option at all (operations/hooks.go, which
	// overrides the filesystem). An option-bearing call asks for a DIFFERENT set
	// of sources, so it builds its own — and the option fields are a func and a
	// slice, neither usable as a cache key, so keying on them is not merely
	// unnecessary but impossible.
	//
	// Anything that changes what is on disk must call InvalidateBundleLoader.
	// A fresh loader used to pick up such a change BY ACCIDENT, because it
	// re-read; sharing one removes the accident and makes the obligation
	// explicit.
	if len(opts) == 0 {
		c.bundleLoaderMu.Lock()
		defer c.bundleLoaderMu.Unlock()
		if c.bundleLoader == nil {
			c.bundleLoader = c.buildBundleLoader()
		}
		return c.bundleLoader
	}
	return c.buildBundleLoader(opts...)
}

// InvalidateBundleLoader drops the memoized loader so the next BundleLoader
// re-reads every source.
//
// Callers are the paths that CHANGE what the readers would see: a bundle
// written or deleted locally, and a remote pull landing new pinned content. A
// missed call yields stale content with exit 0, which is this codebase's
// characteristic failure, so each caller carries a test.
func (c *Config) InvalidateBundleLoader() {
	c.bundleLoaderMu.Lock()
	defer c.bundleLoaderMu.Unlock()
	if c.bundleLoader != nil {
		// IN PLACE, keeping the pointer. The bundle store now reads and writes
		// through this same loader, so replacing the object would leave the
		// store holding one nothing else refers to — reintroducing, quietly,
		// the two-views-of-one-thing split that sharing it removed.
		c.bundleLoader.Invalidate()
	}
}

func (c *Config) buildBundleLoader(opts ...BundleLoaderOption) *bundles.Loader {
	var lc bundleLoaderConfig
	for _, opt := range opts {
		opt(&lc)
	}
	fsys := lc.fs
	if fsys == nil {
		// Thread the injected filesystem so fs-installed local bundle discovery
		// and reads honor it, matching GetProfileLoader's profiles.WithFS(c.fs).
		fsys = c.getFS()
	}

	// Order is precedence: a later reader wins a name collision, so pinned
	// remote content still shadows a stale extracted copy on disk, and a
	// companion's own ref (which nothing else can claim) is last.
	//
	// The builtin reader's presence here is what makes a builtin bundle
	// resolvable BY REF — a profile naming `isolation#fragments/isolation-axes`
	// reaches it through the ordinary loader rather than only through the
	// unconditional injection route. A builtin and a project bundle may share a
	// declared name without either displacing the other: the catalog keys on the
	// canonical URI, so the two are separate entries and a BARE name that matches
	// both is refused as ambiguous rather than silently resolved to one. The
	// builtin still reaches the session by injection either way — the two routes
	// are collapsed by the ingest identity rule, not by the catalog.
	//
	// Its position here is NAME precedence only. It once also decided which
	// filesystem Loader.FS() reported — the builtin reader has one, the
	// EMBEDDED fs — so listing it first derived every project skill's trust
	// preimage from a tree that does not exist there and withheld the skill in
	// silence. bundles.readersFS now selects by provenance instead, so that
	// failure is no longer reachable by reordering this slice.
	//
	// A SOURCE, not a slice: the remote readers are one per lockfile entry, and
	// a pull adds entries while this loader is alive (it is shared for the
	// Config's life and the sync builds it before any fetch). Deriving the set
	// on every (re)index is what makes InvalidateBundleLoader mean what it says
	// — "re-reads every source" — for the lockfile too, not only for the
	// content behind the readers that already existed.
	source := func() []bundles.Reader {
		readers := []bundles.Reader{bundles.NewProjectReader(fsys, c.BundleReaderDirs(), bundles.WithTrustRoot(c.TrustRoot()))}
		readers = append(readers, bundles.NewBuiltinReader(bundles.WithTrustRoot(c.TrustRoot())))
		readers = append(readers, c.remoteBundleReaders()...)
		readers = append(readers, c.companionReader())
		return append(readers, lc.extraReaders...)
	}

	loader := bundles.NewLoaderFrom(source)
	// Multi-version coexistence (trust rework, TR5): give every read-path loader
	// the capability to materialize a specific historical commit-version of a
	// remote bundle via FetchItem. This is opt-in at the loader's version-aware
	// methods only — the default (lockfile-pinned) path is unaffected — so wiring
	// it everywhere is free until a caller asks for an "@<commit>" version.
	resolver := lc.versionResolver
	if resolver == nil {
		resolver = c.bundleVersionResolver()
	}
	if resolver != nil {
		loader.WithVersionResolver(resolver)
	}
	return loader
}

// companionReader builds the reader that contributes every discovered
// companion's loadout, seeded under its ctxloom:companion@<bin> ref.
//
// The reader owns the trust facts (it verifies any signature against THIS
// config's full trust root — embedded + user + project allowed_signers, the
// same root the pinned-remote readers use); this function owns only WHICH
// prober it reads through, and the memoization of that prober's result.
func (c *Config) companionReader() bundles.Reader {
	return bundles.NewCompanionReader(c.companionProber(), bundles.WithTrustRoot(c.TrustRoot()))
}

// companionProber returns the exec seam the companion reader reads through:
// the loadouts of every companion this machine's human has agreed ctxloom may
// execute, probed at most ONCE per Config.
//
// The memoization is not an optimization detail. Probing execs a subprocess per
// discovered companion and can PROMPT for consent to do so, and a loader is
// built repeatedly within one process (hooks, MCP, fragments, assembly) — so
// without it the same question would be asked several times in one run.
//
// Skipped entirely when there is no project directory: companion content only
// matters for a real project session, and this keeps a bare/management Config —
// the shape most unit tests construct — from spawning companion subprocesses it
// has no use for.
func (c *Config) companionProber() bundles.CompanionProber {
	if len(c.appPaths) == 0 {
		return nil
	}
	// No lazy allocation and no package-level lock: the state is a value field,
	// so it exists as soon as the Config does, and its own sync.Once is the only
	// synchronization the probe needs.
	state := &c.companionSeed
	probe := c.companionProbe

	// Otherwise a Config's own override (the test seam) wins over the real probe,
	// so a parallel test can pin its own fixture without touching the global.
	if probe == nil {
		// Adapter, so bundles.CompanionProber stays a plain func(ctx): the
		// trust root is CONFIG-provided, and this closure is the point where a
		// Config is in scope. TrustRoot() reads allowed-signers files and never
		// probes companions, so consulting it here cannot recurse back into
		// this probe.
		root := c.TrustRoot()
		probe = func(ctx context.Context) (bundles.CompanionProbe, error) {
			return ProbeCompanionLoadouts(ctx, root)
		}
	}
	return func(ctx context.Context) (bundles.CompanionProbe, error) {
		// The process-wide switch (--no-companions / CTXLOOM_NO_COMPANIONS) wins
		// over everything, INCLUDING an injected probe: "off" must mean no
		// companion code runs, not "off unless something wired an override".
		// Disabled short-circuits before any probe is called, so no companion
		// subprocess is executed and no loadout is contributed — skipping the
		// exec, not discarding its result, is the point, since probing shells out
		// to whatever companion binaries happen to be on the host's PATH.
		if CompanionsDisabled() {
			return bundles.CompanionProbe{}, nil
		}
		var err error
		state.once.Do(func() {
			state.cache, err = probe(ctx)
		})
		return state.cache, err
	}
}

// DisableCompanionProbe makes companion-loadout discovery a no-op for this
// Config. Companion probing execs the companion binaries found on the host's
// PATH, which makes any assertion over an exact command set depend on what the
// developer happens to have installed. Tests that pin such a set call this so
// the fixture — not the machine — decides the result.
func (c *Config) DisableCompanionProbe() {
	c.companionProbe = func(context.Context) (bundles.CompanionProbe, error) { return bundles.CompanionProbe{}, nil }
}

// SetCompanionProbeForTesting pins the companion loadouts this Config will see,
// so a test's fixture — never the developer's PATH — decides what companion
// content a session carries.
func (c *Config) SetCompanionProbeForTesting(probe bundles.CompanionProber) {
	c.companionProbe = probe
}

// companionSeedState is the memoized result of one Config's companion-loadout
// probe, held as a VALUE on Config.companionSeed. Its sync.Once is the whole
// synchronization story: the state exists as soon as the Config does, so there
// is no allocation to guard and no second lock.
type companionSeedState struct {
	once  sync.Once
	cache bundles.CompanionProbe
}

// bundleVersionResolver returns a bundles.BundleVersionResolver that materializes
// a bundle at a specific commit and parses the bytes into a Bundle. It dispatches
// by the ref's SOURCE — the loader's multi-version coexistence backed end to end:
//
//   - remote/canonical ref → the whole pinned TREE out of the local git clone
//     cache (bundles.ReadRemoteRef), verified before it is interpreted;
//   - ctxloom:local ref → the file's bytes as of <commit> in the PROJECT'S OWN
//     git history (the committed .ctxloom/content/ tree), via the local working-copy
//     VCS — `git show <commit>:<path>` semantics. The unversioned local path is
//     untouched: the loader only invokes the resolver for an explicit "@<commit>".
//
// Given a version-less canonical ref and an opaque commit, it reads exactly that
// historical version. Returns nil when there is no app dir to anchor either
// source. The fetch is lazy — nothing happens until a version-aware loader method
// actually requests a pinned commit — and any failure (unknown rev, non-git
// project, path-absent-at-rev) fails closed: the caller withholds just that item.
//
// Auth and both git backends are inherently OS-backed (the remote cache shells
// out to git; the local backend opens the on-disk project .git), so they do not
// honor c.fs — matching loadRemoteBundleSeed.
func (c *Config) bundleVersionResolver() bundles.BundleVersionResolver {
	if len(c.appPaths) == 0 {
		return nil
	}
	baseDir := c.appPaths[0]
	// Defer the auth read + clone-cache construction to the FIRST actual remote
	// version fetch: the default (lockfile) path never invokes the resolver, and a
	// local-only pin never touches the remote cache, so neither pays for it.
	var (
		once    sync.Once
		factory remote.FetcherFactory
		auth    remote.AuthConfig
	)
	return func(canonicalRef, commit string) (*bundles.Bundle, error) {
		ref, err := remote.ParseReference(canonicalRef)
		if err != nil {
			return nil, fmt.Errorf("parse %q: %w", canonicalRef, err)
		}

		// Local (project-authored) refs version against the PROJECT'S own git
		// history, not the remote clone cache. The committed .ctxloom/content/ tree
		// is read at <commit> through the working-copy VCS; a non-git project,
		// unknown rev, or path-absent-at-rev errors here and the caller withholds.
		if ref.IsLocal {
			data, err := remote.NewLocalRefFetcher(
				remote.LocalGitVCSFactory(afero.NewOsFs()),
				paths.LocalPath(baseDir),
			).FetchItem(context.Background(), ref, commit)
			if err != nil {
				return nil, err
			}
			return bundles.ParseBundle(data)
		}

		// Remote/canonical refs: FetchItem over the local clone cache (auth +
		// cache built once, lazily, on the first remote pin).
		once.Do(func() {
			auth = remote.LoadAuth(baseDir)
			cache := remote.NewRepoCache(paths.ReposCachePath(baseDir), auth)
			factory = remote.NewCachedFetcherFactory(cache)
		})
		// The WHOLE tree, not its manifest: a tree bundle's fragments, commands
		// and skills are FILES beside its bundle.yaml, so reading the manifest
		// alone resolved every @<commit>-pinned tree bundle to a bundle with
		// zero items — the real product bundle, silently empty.
		return bundles.ReadRemoteRef(context.Background(), factory, auth, ref, commit, remotetree.PullTreeFetcher, c.TrustRoot())
	}
}

// reportBundleLoadFailures records one fatal-class finding per lockfile-active
// bundle whose bytes could not be read.
//
// Fatal-class in strict mode because the user PINNED these: content silently
// missing from a session is exactly the failure fail-loudly exists to catch. It
// warns and continues in degraded mode.
//
// A WITHHELD tree is reported differently, and deliberately: its bytes are on
// disk and re-pulling would fetch the same ones, so the default fix cannot fix
// it. It is also not a delivery problem at all — the content disagrees with what
// its publisher signed — so it is classed as a trust failure rather than a
// delivery one. It no longer mirrors anything: the single-file tamper branch it
// was written against is gone, because single-file bundles are no longer read at
// all, so this is now the only path on which installed remote bytes can be
// refused for disagreeing with their signature. A fix line that cannot fix the
// thing it is attached to is worse than no fix line at all.
func reportBundleLoadFailures(failures map[string]error) {
	for name, err := range failures {
		if errors.Is(err, bundles.ErrTreeBundleWithheld) {
			strictness.FailOnce(strictness.ClassTrust,
				"re-pull the bundle, or investigate the source — the installed tree does not match the manifest its publisher signed",
				"remote bundle %q was installed but withheld: %v", name, err)
			continue
		}
		strictness.FailOnce(strictness.ClassBundle, "ctxloom deps pull (or remove the bundle from its profiles)",
			"failed to load remote bundle %q from cache: %v", name, err)
	}
}

// remoteBundleReaders builds one pinned-tree reader per lockfile-listed bundle:
// the bytes come from the local git clone cache at the pinned SHA (single-file
// bundles) or from the tree `deps pull` installed (directory-form bundles),
// and each reader does its OWN signature checking over exactly those bytes.
//
// Canonical refs are the sole resolution identity: profiles author canonical
// refs and resolve straight to these readers' content, so each reader is
// constructed FOR one canonical ref rather than discovering names from paths.
//
// Returns nil when there is no lockfile or registry — no remote bundles, just
// the project's own.
func (c *Config) remoteBundleReaders() []bundles.Reader {
	if len(c.appPaths) == 0 {
		return nil
	}
	// PINS RIDE A PROJECT, NEVER HOME. When findAppDir fell back to
	// ~/.ctxloom there is no project, and a lockfile there pins a closure
	// nothing declares — home config carries settings (llm configs,
	// delegation), not bundles or profiles.
	//
	// This is not symmetry with the config chain, and deliberately so: config
	// LAYERS because settings merge sensibly, but two dependency closures do
	// not merge — their union is a set neither side asked for. So home may
	// supply CONFIG; only a project supplies a CLOSURE.
	//
	// It is also the only way this state can stay true. `deps` writes at
	// project scope and would never revisit a home lock, so a home lock is
	// read-but-never-written — and that always rots. The one on this machine
	// pinned 33 bundles against ZERO declarations for six weeks, until both
	// pinned revisions stopped parsing against the current bundle schema and
	// every project-less launch aborted on findings no supported command
	// could clear. Refusing to read it here is what makes the reader agree
	// with `deps check`, which already reports nothing installed there.
	if c.source == SourceHome {
		return nil
	}
	baseDir := c.appPaths[0]

	registry, err := remote.NewRegistry(paths.RemotesPath(baseDir), c.registryFSOptions()...)
	if err != nil {
		// A real error here (corrupt remotes.yaml, unreadable dir) is not "no
		// remotes registered" — the doc comment's nil-return case above — so it
		// fails loud instead of silently vanishing every lockfile-pinned remote
		// bundle from assembly/hooks/MCP/commands.
		strictness.FailOnce(strictness.ClassBundle, "check the remotes registry under .ctxloom, or re-run `ctxloom remote add`",
			"failed to open the remotes registry; no remote bundles loaded: %v", err)
		return nil
	}
	lock, err := remote.NewLockfileManager(baseDir, c.lockfileFSOptions()...).Load()
	if err != nil {
		strictness.FailOnce(strictness.ClassBundle, "run `ctxloom deps pull` to regenerate the lockfile, or fix it by hand",
			"failed to load the remote lockfile; no remote bundles loaded: %v", err)
		return nil
	}
	if lock.IsEmpty() {
		return nil
	}
	// Auth config and the git clone cache are inherently OS-backed (the cache
	// shells out to git), so they intentionally do not honor c.fs.
	auth := remote.LoadAuth(baseDir)
	cache := remote.NewRepoCache(paths.ReposCachePath(baseDir), auth)
	factory := remote.NewCachedFetcherFactory(cache)
	// Wrap in the caching decorator so repeated loader constructions within a
	// session don't re-walk the clone for the same SHAs.
	reader := remote.NewCachingBundleReader(remote.NewBundleReader(registry, factory, auth, lock))

	ctx := context.Background()
	_, failures := remote.LoadAllBytes(ctx, reader)

	// The trust root (embedded + user + project allowed_signers) is resolved once
	// for the whole set and handed to every reader, so no two pinned bundles are
	// judged against different roots.
	root := c.TrustRoot()

	// EVERY remote bundle is a TREE, so treeBundleReaders is the whole set.
	//
	// There used to be a document-reader loop here, skipped for tree entries.
	// With the document form removed it would match everything, and presenting
	// a tree's bundle.yaml as a lone document drops the items beside it — the
	// fragments, skills and prompts that live as FILES in the tree — while
	// checking a signature over the manifest alone rather than over the tree.
	// That is not hypothetical: leaving the loop unguarded is exactly what made
	// a published fragment stop reaching the consumer's assistant while every
	// other surface kind still arrived.
	out := c.treeBundleReaders(lock, root, failures)
	reportBundleLoadFailures(failures)
	return out
}

// GetConfigFilePath returns the path to the primary config file.
// Uses the closest project .ctxloom directory.
func (c *Config) GetConfigFilePath() (string, error) {
	if len(c.appPaths) == 0 {
		return "", fmt.Errorf("no .ctxloom directory found; run 'ctxloom init --local' first")
	}
	return paths.ConfigPath(c.appPaths[0]), nil
}

// getFS returns the filesystem to use for file operations.
func (c *Config) getFS() afero.Fs {
	if c.fs != nil {
		return c.fs
	}
	return afero.NewOsFs()
}

// SetFS sets the filesystem for file operations (useful for testing). Also
// marks the filesystem as injected (see injectedFS's doc), so Save/
// Manager.Update skip the cross-process advisory lock for it exactly as they
// would for a WithFS(...) load.
func (c *Config) SetFS(fs afero.Fs) {
	c.fs = fs
	c.injectedFS = true
}
