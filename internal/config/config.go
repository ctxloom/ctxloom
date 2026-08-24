package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/spf13/afero"
	"github.com/spf13/pflag"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config/layerscope"
	"github.com/ctxloom/ctxloom/internal/content"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/schema"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/internal/shared/confload"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
	"github.com/ctxloom/ctxloom/resources"
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
	// home with none. ScopeNever in internal/config/layerscope names the
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
	pendingUpgrade *upgrade.Pending

	// homePendingUpgrade is pendingUpgrade's counterpart for the HOME layer,
	// populated only when a project layer ALSO exists (so home is being read
	// as the lower-precedence layer, not as the effective single source —
	// that case populates pendingUpgrade instead, exactly as before layering).
	// CommitHomeUpgrade persists it, on the same consent rule as
	// PendingUpgrade: the caller prompts and the prompt names the path, so
	// home is never rewritten as a silent side effect of a project-scoped
	// run. Before that existed, home was upgraded in memory on every load and
	// never written back — visible, but never converging (long-ice).
	homePendingUpgrade *upgrade.Pending

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
// site (internal/cli/config.go's renderConfigYAML) — keeps working completely
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
		IsolationImages:              cloneStringMap(c.isolationImages),
		IsolationBaseContainerfile:   c.isolationBaseContainerfile,
		IsolationDevcontainerBase:    cloneBoolPtr(c.isolationDevcontainerBase),
		IsolationDevcontainerService: c.isolationDevcontainerService,
		IsolationEngines:             cloneStrings(c.isolationEngines),
		UI:                           cloneUIConfig(c.ui),
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
	agentsNode := mappingValue(node, "agents")
	if agentsNode == nil {
		return "", false
	}
	// Content pairs as [key, value, key, value, ...]; agents are already in
	// document order, so the name reported is stable across runs.
	for i := 0; i+1 < len(agentsNode.Content); i += 2 {
		name := agentsNode.Content[i].Value
		if mappingValue(agentsNode.Content[i+1], key) != nil {
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

// LoadOption is a functional option for Load.
type LoadOption func(*loadOptions)

type loadOptions struct {
	fs        afero.Fs
	appDir    string // Override ctxloom directory discovery
	overrides *confload.Overrides
}

// WithFS sets the filesystem for config operations.
func WithFS(fs afero.Fs) LoadOption {
	return func(o *loadOptions) {
		o.fs = fs
	}
}

// WithAppDir sets a specific ctxloom directory instead of discovering it.
func WithAppDir(dir string) LoadOption {
	return func(o *loadOptions) {
		o.appDir = dir
	}
}

// WithOverrides installs the env/CLI overrides THIS load resolves, instead of
// whatever SetOverrides last installed process-wide. It exists as a TEST
// SEAM: production code sets overrides once, process-wide, via SetOverrides
// (see its doc); a test that wants a specific Overrides resolved against a
// specific load — without mutating the process-wide value every other test in
// the same binary would also see — passes it here instead.
func WithOverrides(o confload.Overrides) LoadOption {
	return func(opt *loadOptions) {
		opt.overrides = &o
	}
}

// SetOverrides installs o as the process-wide env/CLI overrides every
// subsequent Load/LoadFresh resolves (until the next SetOverrides call) —
// internal/cli/root.go's PersistentPreRun calls it once, right after flags
// are parsed, before any config Load. The actual storage lives in
// confload.SetProcessOverrides (see that function's doc for why: mainly so
// internal/testsupport can reset it without an import cycle through config's
// own test files). SetOverrides additionally invalidates the ambient memo so
// the change is visible on the very next Load even if no config FILE changed
// in the meantime — the memo's own stat check has nothing to key on for an
// override, only for a file (see ambientStamp's doc for why Overrides.Stamp
// is ALSO folded into the stamp itself: belt and suspenders against exactly
// this kind of miss).
func SetOverrides(o confload.Overrides) {
	confload.SetProcessOverrides(o)
	Invalidate()
}

// currentOverrides returns the process-wide overrides SetOverrides last
// installed (the zero Overrides{} if none ever was).
func currentOverrides() confload.Overrides {
	return confload.ProcessOverrides()
}

// ResetOverrides clears the process-wide overrides back to the zero
// Overrides{} ("none installed"). internal/testsupport.Isolate resets the
// same underlying state directly via confload.ResetProcessOverrides (not
// this function, to avoid an import cycle — see confload.SetProcessOverrides'
// doc); this wrapper exists for internal/config's own callers/tests.
func ResetOverrides() {
	SetOverrides(confload.Overrides{})
}

// ctxloomProduct builds the confload.Product describing ctxloom's own
// on-disk/env conventions. EnvPrefix is CTXLOOM_CONFIG_ — deliberately NOT a
// bare CTXLOOM_, which is already claimed by bootstrap/process-selection vars
// (CTXLOOM_ROOT, CTXLOOM_PROJECT_ID, CTXLOOM_SESSION_HARP, CTXLOOM_DEGRADED,
// CTXLOOM_VERBOSE, ...) that select WHICH config is read rather than being a
// value inside it. validator may be nil (schema failed to load — Load's own
// fault-tolerant fallback), and a nil validator leaves Product.KnownPath NIL,
// which is confload's own documented "no schema knowledge available"
// degradation. Passing validator.KnownPath unconditionally would defeat that
// path entirely: a method value on a nil pointer is never a nil func, so
// confload's `if p.KnownPath != nil` guard would always be taken and the
// branch its doc describes would be unreachable from either product. The
// resolved outcome is identical either way — a predicate that answers false
// for everything and an absent predicate both land on resolvePath's case
// 4 — but only the nil form skips enumerating every partition of the name to
// ask a question that has no answer. ConfigValidator.KnownPath stays
// nil-receiver-safe regardless, so a caller that does pass it cannot panic.
// newConfigValidatorFn is a test seam over schema.NewConfigValidator: the
// embedded schema it compiles is a fixed build artifact, so there is no other
// way to exercise the "schema failed to compile" fallback below without
// corrupting a real resource. Production always uses the real constructor;
// only tests substitute a failing stub.
var newConfigValidatorFn = schema.NewConfigValidator

func ctxloomProduct(validator *schema.ConfigValidator) confload.Product {
	p := confload.Product{
		Name:      "ctxloom",
		DirName:   AppDirName,
		FileName:  ConfigFileName,
		EnvPrefix: "CTXLOOM_CONFIG_",
		// ScopeAllows closes the env/--config-set escalation paths (design
		// doc "Already wrong #1"/"#2"): an override that cannot carry a key's
		// scope is dropped rather than applied. MergeFunc closes the
		// same-named-agent escalation ("Already wrong #2"'s home-fills-gap
		// half): an agent binding is defined entirely by whichever layer
		// names it, never fused field-by-field across layers.
		ScopeAllows: scopeAllows,
		MergeFunc:   agentBindingMergeFunc,
	}
	if validator != nil {
		p.KnownPath = validator.KnownPath
		// The override channels' own schema gate. A file layer is validated by
		// loadConfigLayer before it merges; without this, an env/--config-set
		// value merged in afterwards was checked against nothing, so the same
		// bad value was refused through one door and silent through the other.
		p.ValidateValue = validator.ValidateAt
	}
	return p
}

// InstallOverridesFromFlags is the CLI's own hook into the override chain:
// internal/cli/root.go's PersistentPreRun calls this ONCE, right after cobra
// has parsed the invoked command's flags, so it sees every --config-set value given
// on THIS invocation (see confload.ConfigSetFlagName's doc for why --config-set, not the
// invoked command's flags in general, is the only CLI-layer source). It
// builds ctxloom's own confload.Product (a fresh schema.ConfigValidator, for
// KnownPath — schema validation failing here degrades to "no schema
// knowledge", matching loadUncached's own fault tolerance, never a hard
// failure), reads env+--config-set overrides via confload.ReadOverrides, and
// installs them process-wide via SetOverrides.
//
// The returned error reports a malformed --config-set entry (missing "=", or an
// empty path) eagerly, right here — unlike an ambiguous-override collision,
// which needs a config base ReadOverrides does not have yet and so is only
// raised later, at each Load. Callers follow this codebase's fault-tolerance
// convention and downgrade it to a warning rather than fail startup.
func InstallOverridesFromFlags(fs *pflag.FlagSet) error {
	validator, err := newConfigValidatorFn()
	if err != nil {
		// The embedded schema is a build-time resource, so a compile failure
		// here "can't happen" in a healthy build. If it ever does, it must be
		// visible rather than silently discarded — matching loadUncached's
		// sibling fallback below, which also zap-warns — so KnownPath
		// degrading to "nothing recognized" is a fact the log carries, not a
		// silent one.
		zap.L().Warn("failed to create config validator for override resolution", zap.Error(err))
		validator = nil
	}
	o, readErr := ctxloomProduct(validator).ReadOverrides(fs)
	SetOverrides(o)
	return readErr
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
// Depth is a structural/correctness limit, SpoolTee is a substrate rollout
// switch. See each field's doc.
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
	// SpoolTee turns on the SHADOW TEE of coordinator<->child mail onto the
	// file spool (~/.ctxloom/sessions/<harp>/persist/spool): every mailbox
	// delivery is ADDITIONALLY written as a spool message file and announced
	// with a doorbell, while every read still comes from the mailbox. It
	// changes no delivery behaviour by design — it exists so the file
	// substrate can soak under real traffic before anything reads from it,
	// and so a fidelity gap between the two representations shows up as a
	// diverging file rather than as a lost message after a cutover.
	//
	// DEFAULT FALSE, and false must mean literally nothing happens: no spool
	// directory is created, no doorbell is rung. A tee that half-runs when
	// disabled would make "the flag is off" an untrustworthy statement about
	// every incident that followed.
	//
	// It is a plain bool rather than a *bool because there is no third state
	// to distinguish: unset and false both mean the tee is off, and the key
	// is pruned from a saved config in both cases.
	SpoolTee bool `yaml:"spool_tee,omitempty"`
	// SpoolDelivery CUTS COORDINATOR<->CHILD MAIL OVER onto the file spool:
	// the coordinator's write into a child's in/ becomes the ONLY write (no
	// mailbox twin), the child's runner DELIVERS from that file and consumes
	// it by renaming it into in/consumed/, and the child's own sends are
	// written into out/ and routed by the coordinator. The gRPC doorbell
	// carries a reference and bounds latency; the durable truth is the file,
	// so a lost doorbell costs a sweep interval and never a message.
	//
	// It is a SEPARATE key from SpoolTee, not a mode of it, because the two
	// have opposite risk profiles and must be independently settable: the tee
	// changes no delivery and can be left on to gather evidence, while this
	// one IS the delivery. Turning this on with the tee off is the cutover;
	// turning both on is the same cutover (mail delivered by file is not
	// additionally teed — there is nothing left to shadow).
	//
	// DEFAULT FALSE, and false means byte-identical pre-spool behaviour: the
	// mailbox queues, pushes, parks and drains exactly as it always has.
	//
	// SCOPE: only runner-backed (StartRun) children are delivered by file.
	// Mail to the session owner's own in-process mailbox and to a FROZEN
	// legacy go-plugin child keeps the mailbox — neither has a runner that
	// sweeps a spool, so a file written for them would sit in a directory
	// nothing ever reads.
	SpoolDelivery bool `yaml:"spool_delivery,omitempty"`
}

// DefaultDelegationDepth is the built-in default for delegation.depth (flat
// fan-out: the session owner may spawn subagents, a subagent may not spawn
// further) — the SOLE numeric source both GetDelegationDepth (this package)
// and coord.agentDepthCap resolve from, so a runner (which loads its own
// *config.Config independently of the coordinator process) and the
// coordinator itself always agree on the resolved cap without ever
// exchanging it over the wire.
const DefaultDelegationDepth = 1

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
// type and model it specifies. A missing label or empty type degrades to the
// built-in default backend with no model (backend default). The model is read
// only from the entry's own body — never by branching on the backend name.
func (c *Config) ResolveLLM(label string) (backend, model string) {
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

// GetDefaultLLMModel returns the model for the primary role's label.
// Empty means the backend uses its own default.
func (c *Config) GetDefaultLLMModel() string {
	_, model := c.ResolveLLM(c.PrimaryLabel())
	return model
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

// GetCompactionChunkSize returns the target chunk size for compaction.
// Defaults to 8000 tokens.
func (c *Config) GetCompactionChunkSize() int {
	if c.settings.CompactionChunks > 0 {
		return c.settings.CompactionChunks
	}
	return 8000
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
// unset — meaning the zero-config discovery chain (internal/signing/agentkey)
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

// Load returns the AMBIENT project config: the one config a process reads.
//
// The no-arg call is memoized, so the ~35 call sites across the CLI share one
// parse instead of each re-walking the directory tree, re-parsing the YAML and
// re-running schema validation. The memo is validated against config.yaml's
// stat (mtime+size) on every call rather than frozen at first read, because two
// behaviours depend on seeing a rewritten file WITHIN one process:
//
//   - read-after-write: init scaffolds config.yaml between two Loads;
//   - hot reload: agent_run re-loads on every spawn so edited agent definitions
//     take effect mid-session.
//
// Validating by stat (rather than invalidating at each writer) is deliberate:
// it self-corrects for ANY writer — Save, init's scaffold, another process, a
// user's editor — so a missed invalidation cannot serve a stale config.
//
// Passing options (WithAppDir/WithFS) means a DIFFERENT config — an explicit
// --app-dir, a worktree's .ctxloom, an injected fs — so those loads are never
// served from, nor written into, the ambient memo.
//
// Callers that MUTATE a config before Save must use LoadFresh: mutating the
// shared ambient instance would let a mutation abandoned on an error path leak
// into every later reader.
//
// Resolution is two-stage (see internal/shared/confload's package doc for
// the general primitive this implements):
//
//  1. Bootstrap — WHICH directories participate. findAppDir resolves the
//     project .ctxloom (CTXLOOM_ROOT override, else walking up from cwd), or
//     falls back to the user home ~/.ctxloom when no project is found. This
//     is a directory-selection decision, not a config VALUE, so it happens
//     first and is never itself layered.
//  2. Value layering — home < project, deep-merged key by key (see
//     loadLayeredConfig): a project inherits every home key it does not set,
//     and an explicitly-set project value (including its zero value) always
//     wins over an inherited one. When no project is found, home IS the
//     single effective source and this stage is a no-op over one file,
//     matching pre-layering behavior exactly.
func Load(opts ...LoadOption) (*Config, error) {
	// An explicit target/fs is a different config: never cached.
	if len(opts) > 0 {
		return loadUncached(opts...)
	}

	ambientMu.Lock()
	defer ambientMu.Unlock()

	stamp := ambientStamp()
	if ambientCfg != nil && stamp == ambientAt {
		return ambientCfg, nil
	}

	cfg, err := loadUncached()
	ambientCfg, ambientAt = cfg, stamp
	return cfg, err
}

// LoadFresh loads a config WITHOUT consulting or populating the ambient memo.
// It is the mutator's entry point: Load hands back a shared instance, so a
// caller that mutates before Save (agent/llm/mcp/tooling writes) must own its
// own copy or an abandoned mutation would poison every later reader.
func LoadFresh(opts ...LoadOption) (*Config, error) {
	return loadUncached(opts...)
}

// Invalidate drops the memoized ambient config, so the next Load re-reads from
// disk. Load's stat check already covers ordinary writes; this is the explicit
// escape hatch (and what tests use to isolate from one another).
func Invalidate() {
	ambientMu.Lock()
	defer ambientMu.Unlock()
	ambientCfg, ambientAt = nil, ""
}

// ambient* memoize the no-arg Load. ambientAt is the stat stamp of the
// config.yaml the memo was built from; a mismatch (or an unfindable file) means
// re-read. Errors are NOT memoized as successes: a failed load leaves
// ambientCfg nil, so a failed load re-attempts.
var (
	ambientMu  sync.Mutex
	ambientCfg *Config
	ambientAt  string
)

// ambientStamp returns a cheap identity for the config file(s) the ambient
// memo was built from: path + mtime + size per participating layer. A
// found-but-configless app dir (an app dir exists — e.g. bundle content was
// seeded — but no config.yaml has been written into it yet) is still keyed by
// its OWN appPath ("missing:" + appPath), not a bare constant: two DIFFERENT
// app dirs that both currently lack a config.yaml must never collide on the
// same stamp and serve each other's stale memoized *Config (this collision
// made TestShowItem_NonInteractiveStdoutUnchanged serve a PRIOR test
// iteration's already-cleaned-up t.TempDir config on ~every rerun once
// memoized, in the same process, at -count>1 — GetConfig()'s ambient memo is
// a package-level var shared across every test in the binary). No
// discoverable app dir at all ("" from findAppDir) stays a bare "" — that
// state has no path to key on, and is legitimately shared (loadUncached's own
// fallback resolves the identical way for any cwd with no project at all).
//
// When a project layer is active, the HOME layer's config.yaml is now also a
// live input to the resolved config (D2/D3 layering) — editing it must bust
// the memo too, exactly like editing the project file does, so its stamp
// component is appended whenever resolveConfigLayerPaths finds a genuine
// (distinct) home layer. The single-file case (no project found, or an
// explicit home==project dedup) stays byte-for-byte the original single-stamp
// string, so every pre-layering memo test is unaffected.
// Overrides.Stamp() is folded into the RETURNED string too (see the field's
// doc on processOverridesVal / SetOverrides): a memo built before overrides
// changed must not be served forever just because no config FILE changed in
// the meantime — a stat check has nothing to key on for an override, only for
// a file. Folding the override stamp in here means it self-corrects the same
// way an ordinary file edit does, on top of (not instead of) SetOverrides'
// own explicit Invalidate() call — belt and suspenders.
func ambientStamp() string {
	overridesStamp := currentOverrides().Stamp()
	fs := afero.NewOsFs()
	appPath, source := findAppDir(fs)
	if appPath == "" {
		return "no-app-dir|" + overridesStamp
	}
	projectConfigPath, homeConfigPath := resolveConfigLayerPaths(appPath, source)
	stamp := fileStamp(fs, appPath, projectConfigPath)
	if homeConfigPath != "" {
		stamp += "|" + fileStamp(fs, filepath.Dir(homeConfigPath), homeConfigPath)
	}
	return stamp + "|" + overridesStamp
}

// fileStamp is ambientStamp's single-file stat identity: path + mtime + size,
// or "missing:"+appPath when the file does not (yet) exist — appPath, not
// configPath, so two different configless app dirs never collide (see
// ambientStamp's doc).
func fileStamp(fs afero.Fs, appPath, configPath string) string {
	info, err := fs.Stat(configPath)
	if err != nil {
		return "missing:" + appPath
	}
	return fmt.Sprintf("%s|%d|%d", configPath, info.ModTime().UnixNano(), info.Size())
}

// loadUncached is the real loader: it always reads and parses from disk.
func loadUncached(opts ...LoadOption) (*Config, error) {
	// Apply options
	options := &loadOptions{}
	for _, opt := range opts {
		opt(options)
	}

	// Use provided FS or default to OS filesystem. injectedFS records which:
	// see its doc for why that distinction (not fs's own nilness, which is
	// never nil after this point) is what Save/Manager.Update gate their
	// advisory cross-process lock on.
	fs := options.fs
	injectedFS := fs != nil
	if fs == nil {
		fs = afero.NewOsFs()
	}

	cfg := &Config{
		lm: LMConfig{
			Configs: make(map[string]LLMConfig),
		},
		fs:         fs,
		injectedFS: injectedFS,
	}

	// Create config validator for schema validation
	configValidator, err := newConfigValidatorFn()
	if err != nil {
		zap.L().Warn("failed to create config validator", zap.Error(err))
		// Previously this degraded to "everything is valid" — zap-only,
		// invisible to the strict-startup gate, which keys exclusively on
		// cfg.warnings. A schema-compile failure means every config in this
		// process loads with ZERO validation and every env/--config-set
		// override is silently reclassified from "known, set silently" to
		// "unknown, warn" (KnownPath degrades to false for everything). The
		// embedded schema is a build artifact, so this is a build defect,
		// not a user condition — surface it as fatal-class.
		cfg.warnings = append(cfg.warnings, Warning{Kind: WarnKindValidate,
			Text: fmt.Sprintf("config schema failed to compile — config validation and override-key checking are DISABLED for this process: %v", err)})
		configValidator = nil
	}

	// Find or use provided .ctxloom directory
	var appPath string
	var source ConfigSource
	if options.appDir != "" {
		appPath = options.appDir
		source = SourceProject
		// An explicit appDir that IS the home directory is home acting
		// alone, not an arbitrary "project" — resolveConfigLayerPaths
		// already treats this exact case specially on the READ side (its
		// home/project dedup collapses to a single file). The WRITE side
		// must agree: saveLocked's layerscope filter strips ScopeMachine
		// values (llm.configs.*.env, credentials — "a committed value is a
		// leaked secret") whenever source is SourceProject, on the theory
		// that the file is a committed project file every clone shares. A
		// caller that deliberately targets ~/.ctxloom (the only way today to
		// write a ScopeMachine value at all, since there is no separate
		// "write home" API) must not have that write silently stripped
		// because it happened to arrive via an explicit option instead of
		// findAppDir's own home fallback.
		if homeAppDir, herr := HomeConfigDir(); herr == nil && filepath.Clean(appPath) == filepath.Clean(homeAppDir) {
			source = SourceHome
		}
	} else {
		appPath, source = findAppDir(fs)
	}
	cfg.appPaths = []string{appPath}
	cfg.appDir = appPath
	cfg.appRoot = filepath.Dir(appPath) // Project root is parent of .ctxloom
	cfg.source = source

	// Env/CLI overrides are applied HERE, inside the single funnel every Load
	// entry point reaches (the len(opts)>0 bypass, the memoized path, and
	// LoadFresh all end up here) — never bolted onto Load itself. An explicit
	// WithOverrides wins for this one call (the test seam); otherwise the
	// process-wide value SetOverrides last installed applies, so a worktree
	// load via WithAppDir/WithFS gets overrides exactly like the ambient
	// project does, and a memo re-read triggered by nothing but a file's mtime
	// changing still carries them (both were silent-loss channels — see
	// SetOverrides' and Load's own doc).
	overrides := currentOverrides()
	if options.overrides != nil {
		overrides = *options.overrides
	}

	projectConfigPath, homeConfigPath := resolveConfigLayerPaths(appPath, source)
	if err := loadLayeredConfig(cfg, homeConfigPath, projectConfigPath, configValidator, fs, ctxloomProduct(configValidator), overrides); err != nil {
		return nil, err
	}

	// Overlay the shipped default config so an empty user config still resolves
	// a primary + fast role (and so model names live in DATA, not Go). User keys
	// always win; defaults only fill gaps the user left empty.
	mergeDefaultConfig(cfg)

	return cfg, nil
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

// mergeDefaultConfig fills any LLM-role gaps from the embedded default config.
// Per CLAUDE.md fault tolerance a malformed/unreadable default never blocks
// startup — the merge is skipped silently. User config always wins: a default
// label is added only when absent, and a role default only when the user set
// none.
func mergeDefaultConfig(cfg *Config) {
	data, err := resources.GetDefaultConfig()
	if err != nil {
		return
	}
	var def Config
	if err := yaml.Unmarshal(data, &def); err != nil {
		zap.L().Warn("default_config_parse_failed", zap.Error(err))
		return
	}
	// The embedded default is a whole-registry fallback for users who configured
	// no LLMs — not a per-key overlay. Injecting default labels into a non-empty
	// user registry would defeat the single-entry selection rule (one configured
	// entry is the one used), so a non-empty user registry is left untouched.
	if len(cfg.lm.Configs) > 0 {
		return
	}
	// cfg gets its own DEEP copy: the overlay snapshot must stay pristine so a
	// later in-place registry mutation isn't mistaken for "still the default"
	// and stripped by Save. A top-level maps.Copy is not enough — each entry's
	// Body map would still be shared with the snapshot, so mutating e.g.
	// Body["model"] in place would also rewrite the overlay and defeat the
	// userAuthoredLM comparison.
	overlay := LMConfig{Configs: def.lm.Configs}
	cfg.lm.Configs = make(map[string]LLMConfig, len(def.lm.Configs))
	for label, entry := range def.lm.Configs {
		entry.Body = deepCopyBody(entry.Body)
		cfg.lm.Configs[label] = entry
	}
	if cfg.lm.Defaults.Primary == "" {
		cfg.lm.Defaults.Primary = def.lm.Defaults.Primary
		overlay.Defaults.Primary = def.lm.Defaults.Primary
	}
	if cfg.lm.Defaults.Fast == "" {
		cfg.lm.Defaults.Fast = def.lm.Defaults.Fast
		overlay.Defaults.Fast = def.lm.Defaults.Fast
	}
	cfg.lmDefaultOverlay = &overlay
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

// resolveConfigLayerPaths computes the STAGE-2 (value layering) inputs from
// the STAGE-1 (bootstrap) result findAppDir (or an explicit WithAppDir)
// already produced: the project config.yaml path always, and — only when
// source is SourceProject, i.e. a genuine project dir was actually resolved —
// the home config.yaml path too, so home participates as the lower-precedence
// layer underneath it.
//
// homeConfigPath is "" (no separate home layer) in two cases: source is
// already SourceHome (findAppDir fell all the way back to home — home IS the
// effective single source, nothing to layer it under), or home's config.yaml
// resolves to the exact same path as the project's (a home-rooted appPath, or
// an explicit --app-dir/CTXLOOM_ROOT pointed straight at ~/.ctxloom) — reading
// it twice would double-apply its warnings and upgrade pipeline for no
// benefit, so it stays a single-file resolution exactly as before.
func resolveConfigLayerPaths(appPath string, source ConfigSource) (projectConfigPath, homeConfigPath string) {
	projectConfigPath = paths.ConfigPath(appPath)
	if source != SourceProject {
		return projectConfigPath, ""
	}
	homeAppDir, err := HomeConfigDir()
	if err != nil {
		return projectConfigPath, ""
	}
	candidate := paths.ConfigPath(homeAppDir)
	if candidate == projectConfigPath {
		return projectConfigPath, ""
	}
	return projectConfigPath, candidate
}

// loadLayeredConfig is Load's stage-2 entry point: it reads every
// participating config.yaml layer (home, then project — ascending
// precedence), deep-merges their decoded values via confload.Merge (home <
// project; D1 lists replace, D3 explicit-zero beats inheritance), and
// populates cfg from the merged result exactly once. homeConfigPath == ""
// means no separate home layer exists (see resolveConfigLayerPaths) — that
// degrades to reading projectConfigPath alone, byte-for-byte the pre-layering
// single-source behavior.
//
// Each layer is upgraded, schema-validated, and warned about INDEPENDENTLY
// (via loadConfigLayer) before merging — never the merged result — so an
// unknown key or a stale schema generation is diagnosed with its own file's
// path regardless of which layer it lives in, and a key valid in one layer
// but not the other still fails loudly instead of the validity of one
// masking a problem in the other.
//
// After the two file layers merge, product.ApplyOverrides resolves overrides
// (env then CLI flags) against the result — the SAME funnel every file layer
// goes through, so an override is visible however this particular Load was
// reached (ambient memo, an explicit WithAppDir/WithFS worktree load, or
// LoadFresh) and however many/few config.yaml files exist, INCLUDING when
// neither file layer exists at all (a fresh project with only env/CLI
// values set): the len(layers)==0 fast path only applies when overrides are
// ALSO empty, so it stays byte-for-byte the pre-override behavior in the
// common case while still closing the "nothing to merge into" corner.
func loadLayeredConfig(cfg *Config, homeConfigPath, projectConfigPath string, validator *schema.ConfigValidator, fs afero.Fs, product confload.Product, overrides confload.Overrides) error {
	var layers []map[string]any
	appPath := filepath.Dir(projectConfigPath)
	homeAppPath := ""
	if homeConfigPath != "" {
		homeAppPath = filepath.Dir(homeConfigPath)
	}

	if homeConfigPath != "" {
		homeValues, pending, err := loadConfigLayer(cfg, layerscope.LayerHome, appPath, homeAppPath, homeConfigPath, validator, fs)
		if err != nil {
			return err
		}
		cfg.homePendingUpgrade = pending
		if homeValues != nil {
			layers = append(layers, homeValues)
		}
	}

	// The single-file case (homeConfigPath == "") is tagged LayerProject by
	// default, but that is wrong when this ONE file genuinely IS home acting
	// alone (cfg.source == SourceHome — findAppDir fell all the way back to
	// home, or an explicit WithAppDir named ~/.ctxloom directly): tagging it
	// LayerProject would make dropLayerScopeViolations strip every
	// ScopeMachine value (llm.configs.*.env, credentials) from a file that
	// was never a committed project file to begin with. The home==project
	// DEDUP case (resolveConfigLayerPaths' other homeConfigPath=="" arm,
	// cfg.source == SourceProject) is unaffected — that file really is being
	// read AS the project, home just happens to sit at the same path, so it
	// keeps the ordinary project scope check.
	layer := layerscope.LayerProject
	if homeConfigPath == "" && cfg.source == SourceHome {
		layer = layerscope.LayerHome
	}
	projectValues, pending, err := loadConfigLayer(cfg, layer, appPath, homeAppPath, projectConfigPath, validator, fs)
	if err != nil {
		return err
	}
	cfg.pendingUpgrade = pending
	if projectValues != nil {
		layers = append(layers, projectValues)
	}

	if len(layers) == 0 && len(overrides.Env) == 0 && len(overrides.Flags) == 0 {
		// Neither layer had a config.yaml on disk at all, and there is no
		// override to apply either — cfg keeps the zero-value defaults
		// loadUncached constructed it with, exactly as the pre-layering
		// single-file loadConfigFile left it.
		return nil
	}

	return decodeMergedLayers(cfg, layers, product, overrides)
}

// splitJoinedErrors unwraps an errors.Join result (or a plain single error)
// into its constituent errors, so a caller can classify each one by TYPE
// (errors.As) rather than treat a whole multi-error blob as one kind. A nil
// err yields a nil slice; an error with no Unwrap() []error (a single,
// non-joined error) yields a one-element slice carrying err itself.
func splitJoinedErrors(err error) []error {
	if err == nil {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		return joined.Unwrap()
	}
	return []error{err}
}

// decodeMergedLayers is loadLayeredConfig's second half: merge the file layers,
// resolve overrides against the result, and decode it into cfg. Split out
// because the two halves fail differently and that distinction is the whole
// contract here — a layer that cannot be READ is fatal and returns an error
// (the caller must not proceed on a config it could not see), while everything
// from the merge onward degrades to a warning on cfg.warnings, because by then
// the files have been read and validated and the remaining steps are ours, not
// the user's.
func decodeMergedLayers(cfg *Config, layers []map[string]any, product confload.Product, overrides confload.Overrides) error {
	merged, mergeErr := product.MergeLayers(layers...)
	if mergeErr != nil {
		return fmt.Errorf("merging config layers: %w", mergeErr)
	}
	merged, overrideErr := product.ApplyOverrides(merged, overrides)
	if overrideErr != nil {
		// A ScopeAllows-driven drop (env/--config-set attempting a key that
		// layer may not carry) is classified as WarnKindLayerScope — the SAME
		// fatal-class finding the per-file-layer check records — so the
		// strict startup gate reports both routes to the identical
		// disallowed-layer problem identically, and a ValidateValue refusal is
		// classified as WarnKindValidate for the identical reason. Every OTHER
		// override fault (an ambiguous case-2 collision, a malformed
		// --config-set entry) keeps the existing, coarser WarnKindParse
		// classification: this is splitting one joined error by TYPE, not
		// inventing a new pass over anything.
		for _, sub := range splitJoinedErrors(overrideErr) {
			var scopeErr *confload.ScopeViolationError
			var schemaErr *confload.SchemaViolationError
			switch {
			case errors.As(sub, &scopeErr):
				cfg.warn(WarnKindLayerScope, "config override resolution: %v", sub)
			case errors.As(sub, &schemaErr):
				// The SAME kind a config FILE's schema breakage gets, so one bad
				// value reads identically whichever door it came through — which
				// is the whole point of validating the override layer at all.
				cfg.warn(WarnKindValidate, "config override resolution: %v", sub)
			default:
				cfg.warn(WarnKindParse, "config override resolution: %v", sub)
			}
		}
		zap.L().Warn("config_override_warning", zap.Error(overrideErr))
	}

	mergedYAML, err := yaml.Marshal(merged)
	if err != nil {
		cfg.warn(WarnKindParse, "failed to remarshal layered config: %v", err)
		zap.L().Warn("config_layer_remarshal_warning", zap.Error(err))
		return nil
	}

	// Parse with yaml directly, NOT viper. Viper lowercases every key it decodes,
	// which corrupts the case-sensitive keys captured by LLMConfig.Body's
	// `,remain`/`,inline` map: a backend `env: {GEMINI_API_KEY: ...}` would reach
	// the launched process as `gemini_api_key`, so the engine never sees its
	// credential. yaml.Unmarshal preserves key case and matches ParseConfig (the
	// init path), so both entry points decode a config identically. This is
	// also why overrides above are resolved into a plain map first (via
	// confload, backed by yaml.Unmarshal file reads) rather than any
	// viper-driven decode of the config document itself.
	if err := yaml.Unmarshal(mergedYAML, cfg); err != nil {
		cfg.warn(WarnKindParse, "failed to parse layered config: %v", err)
		zap.L().Warn("config_parse_warning", zap.Error(err))
	}
	return nil
}

// warn appends a formatted warning of kind k. Every load-path degradation in
// this file records one, and the strict-startup gate keys exclusively on this
// slice, so having one spelling of "record and continue" is what keeps a new
// degradation from being written as a zap-only line nothing can see.
func (c *Config) warn(k WarningKind, format string, args ...any) {
	c.warnings = append(c.warnings, Warning{Kind: k, Text: fmt.Sprintf(format, args...)})
}

// loadConfigLayer reads and processes ONE config.yaml layer — upgrade
// pipeline, schema validation, warnings, all identical to (and unchanged
// from) the pre-layering single-source treatment — and returns its decoded
// VALUES as a presence-tracked map[string]any rather than populating cfg
// directly. loadLayeredConfig merges N such maps (home < project) before cfg
// is ever touched, which is what makes "project silently inherits a home key
// it never mentions" possible: a key simply absent from this layer's map
// never overwrites a lower layer's value during the merge.
//
// pending is this layer's own upgrade.Pending (nil when the file is current
// or absent) — the caller decides which of cfg's two pending-upgrade fields
// (PendingUpgrade / HomePendingUpgrade) it belongs to; this function never
// writes to cfg.pendingUpgrade itself.
//
// Non-fatal errors (malformed YAML, schema validation) are collected as
// warnings directly onto cfg (shared across every layer, same as before).
// Returns an error only for I/O failures (except missing file, which is OK).
func loadConfigLayer(cfg *Config, layer layerscope.Layer, appPath, homeAppPath, configPath string, validator *schema.ConfigValidator, fs afero.Fs) (values map[string]any, pending *upgrade.Pending, err error) {
	data, readErr := afero.ReadFile(fs, configPath)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			// Config file is optional
			return nil, nil, nil
		}
		// An existing-but-unreadable config (EACCES, a directory in its place, a
		// transient I/O error) degrades to the default-overlaid empty config with
		// a kind-tagged warning; the strict startup gate (fail-loudly) turns it
		// into a fatal finding, while --degraded launches anyway.
		cfg.warnings = append(cfg.warnings, Warning{Kind: WarnKindRead, Text: fmt.Sprintf("failed to read config at %s: %v", configPath, readErr)})
		zap.L().Warn("config_read_warning", zap.String("path", configPath), zap.Error(readErr))
		return nil, nil, nil
	}

	// Upgrade older on-disk schema generations to the current one *in memory*
	// before validation/parse, so old configs neither warn nor silently drop
	// settings. We do NOT rewrite the file here: an interactive caller may prompt
	// the user and persist via CommitUpgrade (see cmd/run.go). This keeps
	// non-interactive contexts (MCP server, scripts) from silently rewriting a
	// user's config — the exact failure mode that motivated this layer.
	// The registry-free schema upgrades (configUpgrades) plus the registry-aware
	// agent-profile canonicalization compose into one pipeline so the load parses
	// and re-encodes the document exactly once. The canonicalization step is
	// threaded the alias→URL resolver here (it depends on .ctxloom/remotes.yaml,
	// which the static pipeline cannot reach); a nil resolver makes it a no-op.
	// Thread a FRESH sink per load so a lossy upgrade's warning stays attached
	// to THIS config, never crossed with or lost to a concurrent load draining
	// a shared package buffer (U049-F14).
	migSink := &migrationSink{}
	pipeline := append(upgrade.Pipeline{}, newConfigUpgrades(migSink)...)
	pipeline = append(pipeline, agentProfileCanonicalizeUpgrade{aliasToURL: cfg.ProfileRemoteURLResolver()})
	if upgraded, applied := pipeline.Run(data); len(applied) > 0 {
		data = upgraded
		pending = &upgrade.Pending{Path: configPath, Data: upgraded, Applied: applied}
		zap.L().Info("config_upgrade_pending", zap.String("path", configPath), zap.Strings("applied", applied))
	}
	// A config older than the current schema is REFUSED, not repaired. The
	// in-place upgraders are gone (see CurrentConfigVersion): rewriting a config
	// toward a shape nobody has exercised in years is how one retired step came
	// to migrate toward a backend the registry no longer carries, producing a
	// file that could not load at all. A missing `version` is the pre-versioning
	// generation and is equally too old.
	//
	// Reported as a finding rather than returned as an error, like every other
	// class here, so the startup gate decides whether it is fatal and the user
	// gets the whole picture rather than the first problem encountered.
	if v, declared := declaredConfigVersion(data); v < CurrentConfigVersion {
		spelled := "no `version` key, i.e. the pre-versioning generation"
		if declared {
			spelled = fmt.Sprintf("`version: %d`", v)
		}
		strictness.FailOnce(strictness.ClassMigration,
			fmt.Sprintf("back up %s, then re-run `ctxloom init` to scaffold a current one and re-apply your settings", configPath),
			"%s carries %s but this ctxloom requires config schema version %d, and in-place upgrades have been removed — an old config is no longer rewritten on load",
			configPath, spelled, CurrentConfigVersion)
	}

	// A lossy upgrade (a dropped user-set value) is collected by the pipeline
	// rather than printed inline, so the loader can tag it with its kind and
	// the strict startup gate can abort on it (fail-loudly).
	for _, lost := range migSink.drain() {
		cfg.warnings = append(cfg.warnings, Warning{Kind: WarnKindMigrationLossy, Text: lost})
		zap.L().Warn("config_migration_lossy", zap.String("path", configPath), zap.String("warning", lost))
	}

	// Validate against schema before parsing — warn but continue on failure.
	// This runs AFTER the upgrade pipeline above, deliberately: a key an older
	// config legitimately carries is migrated forward first, so only a key the
	// CURRENT schema truly does not know can be reported. The schema is authored
	// additionalProperties:false throughout, so an unknown key is a violation;
	// classifyValidationError splits those out into named, actionable unknown-key
	// warnings (kind unknown-key → a fatal finding in strict mode) and leaves any
	// other schema breakage as the plain validate warning it has always been.
	if validator != nil {
		if err := validator.ValidateBytes(data); err != nil {
			cfg.warnings = append(cfg.warnings, classifyValidationError(configPath, validator, err)...)
			zap.L().Warn("config_validation_warning", zap.String("path", configPath), zap.Error(err))
		}
	}

	// Decode into a generic, presence-tracked map rather than cfg directly —
	// loadLayeredConfig merges this layer's map against its sibling layer(s)
	// before cfg is populated. yaml.Unmarshal (not viper) preserves key case,
	// matching ParseConfig; see the case-sensitivity note on loadLayeredConfig.
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		cfg.warnings = append(cfg.warnings, Warning{Kind: WarnKindParse, Text: fmt.Sprintf("failed to parse config at %s: %v", configPath, err)})
		zap.L().Warn("config_parse_warning", zap.String("path", configPath), zap.Error(err))
		// Return nil values - the layer contributes nothing, but the warning
		// still surfaces so the strict startup gate can act on it.
		return nil, pending, nil
	}

	// Layer-scope check: does THIS layer carry a key whose value cannot be a
	// fact about it (a machine path in the committed project file, a
	// project-scoped privilege grant filled in from home, ...)? Beside the
	// schema validation above, not a new pass over the merged result — see
	// dropLayerScopeViolations' doc. Every dropped value is gone from raw
	// BEFORE it ever reaches loadLayeredConfig's merge, so it cannot survive
	// via a lower layer's contribution either.
	for _, v := range dropLayerScopeViolations(layer, raw) {
		cfg.warnings = append(cfg.warnings, Warning{Kind: WarnKindLayerScope, Text: v.Message(appPath, homeAppPath)})
		zap.L().Warn("config_layer_scope_warning", zap.String("path", configPath), zap.Strings("key", v.Path))
	}

	zap.L().Debug("config_loaded", zap.String("path", configPath))
	return raw, pending, nil
}

// findAppDir locates the .ctxloom directory.
// Priority:
//  1. CTXLOOM_ROOT override (when set and a valid directory)
//  2. Walk up from cwd looking for .ctxloom directory
//  3. Fall back to user home ~/.ctxloom directory
//
// Always returns a path (creates user home .ctxloom if needed).
func findAppDir(fs afero.Fs) (string, ConfigSource) {
	// CTXLOOM_ROOT is authoritative when valid: the user named the root
	// explicitly, so resolve config at $CTXLOOM_ROOT/.ctxloom and create it if
	// absent, mirroring the home fallback below. A failed MkdirAll warns and
	// continues — the path is still returned so the run isn't blocked.
	if root, ok := projectroot.FromEnv(fs); ok {
		appPath := filepath.Join(root, AppDirName)
		if err := fs.MkdirAll(appPath, 0755); err != nil {
			zap.L().Warn("failed to create CTXLOOM_ROOT .ctxloom directory", zap.String("path", appPath), zap.Error(err))
		}
		return appPath, SourceProject
	}

	// The walk-up-from-cwd loop below has one deliberate boundary: the OS
	// shared temp directory (os.TempDir(), typically /tmp). That directory is
	// multi-tenant scratch space, not a project root — anyone (another
	// process, another test run, a leftover from days ago) can have left a
	// `.ctxloom` sitting directly in it, and walking past a bare temp dir
	// with no boundary would silently adopt that unrelated directory as THIS
	// process's project config. The boundary check runs BEFORE the .ctxloom
	// stat for that one directory only — subdirectories under the temp root
	// (an ordinary t.TempDir(), a real project checked out under /tmp) are
	// still walked and still honor their OWN .ctxloom marker exactly as
	// before; only the temp root itself is excluded from consideration.
	tempRoot := filepath.Clean(os.TempDir())

	// Try to find project .ctxloom by walking up from cwd
	pwd, err := os.Getwd()
	if err == nil {
		if appPath, ok := walkUpForAppDir(fs, pwd, tempRoot); ok {
			return appPath, SourceProject
		}
	}

	// Fall back to user home ~/.ctxloom
	home, err := os.UserHomeDir()
	if err != nil {
		zap.L().Warn("failed to get home directory", zap.Error(err))
		return lastResortAppDir(fs, pwd), SourceProject
	}

	homeApp := filepath.Join(home, AppDirName)

	// Ensure the directory exists
	if err := fs.MkdirAll(homeApp, 0755); err != nil {
		zap.L().Warn("failed to create home .ctxloom directory", zap.Error(err))
	}

	return homeApp, SourceHome
}

// walkUpForAppDir walks from dir toward the filesystem root looking for a
// directory that carries its own .ctxloom, reporting the first one found.
//
// It stops at tempRoot — see findAppDir's boundary note — and signposts any
// linked git worktree it passes through on the way.
func walkUpForAppDir(fs afero.Fs, dir, tempRoot string) (string, bool) {
	// Loop condition (not an if/break at the top): reached the shared OS
	// temp root without finding a project .ctxloom anywhere beneath it —
	// stop here rather than resolving to whatever (if anything) lives at
	// tempRoot itself, and let the caller fall through to its home
	// fallback.
	for filepath.Clean(dir) != tempRoot {
		appPath := filepath.Join(dir, AppDirName)
		if info, err := fs.Stat(appPath); err == nil && info.IsDir() {
			return appPath, true
		}

		// dir has no .ctxloom of its own. If dir is the root of a LINKED
		// git worktree, that is a signpost, not a silent walk-past:
		// resolving straight through to some unrelated ancestor's (or
		// home's) .ctxloom would silently land the session on the wrong
		// project — empty config, no profiles, no agents (an earlier
		// revision had linked worktrees INHERIT the main worktree's project
		// identity; that inheritance design was withdrawn in favor of this
		// signpost).
		// worktreeSignpost records a fatal finding through strictness and
		// the walk continues exactly as it always has — the choke owners
		// (`ctxloom run`/`mcp`/`acp`) abort on it pre-launch unless
		// --degraded; management commands surface the stderr warning and
		// proceed on the fallback. The main worktree (.git is a
		// directory) and every non-worktree ancestor pass through
		// untouched.
		worktreeSignpost(fs, dir)

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached root
			break
		}
		dir = parent
	}
	return "", false
}

// lastResortAppDir answers when the home directory itself is unresolvable: the
// cwd's .ctxloom, resolved ABSOLUTELY and created, matching both of findAppDir's
// other returns. loadUncached derives appRoot as filepath.Dir of this, so a
// relative result would resolve the whole project to "." and make every path
// built from it — bundles, agents, sessions, the config file — depend on
// whatever cwd the process holds when it is used. This is the branch reached
// when the environment is already degraded; it must not degrade the answer
// further. pwd is "" when os.Getwd() failed too.
func lastResortAppDir(fs afero.Fs, pwd string) string {
	appPath := filepath.Join(pwd, AppDirName)
	if pwd == "" {
		if abs, aerr := filepath.Abs(AppDirName); aerr == nil {
			appPath = abs
		} else {
			appPath = AppDirName
		}
	}
	if err := fs.MkdirAll(appPath, 0755); err != nil {
		zap.L().Warn("failed to create fallback .ctxloom directory", zap.String("path", appPath), zap.Error(err))
	}
	return appPath
}

// worktreeSignpost records a fatal ClassConfig finding when dir is the root of
// a LINKED git worktree carrying no .ctxloom of its own — naming the resolved
// main worktree root and both remediation paths (run from the main worktree,
// or `ctxloom init` here to make this worktree a deliberately separate
// project). FailOnce, because findAppDir runs on every config.Load and a
// single process loads config several times — the finding must not stack up
// in one startup window. No-op (walk continues to today's fallback) when dir
// is not such a worktree root.
//
// A linked worktree WITH its own .ctxloom never reaches this call: the walk in
// findAppDir already returned on the .ctxloom check for that same dir. That is
// the one, load-bearing precedence rule for this feature — own .ctxloom always
// wins, no further worktree inspection.
func worktreeSignpost(fs afero.Fs, dir string) {
	info, err := projectroot.DetectWorktree(fs, dir)
	if err != nil {
		strictness.FailOnce(strictness.ClassConfig,
			"check permissions on the .git file in this directory",
			"%s: could not read git worktree metadata: %v", dir, err)
		return
	}
	if !info.Linked {
		return
	}
	if !info.MainRootExists {
		strictness.FailOnce(strictness.ClassConfig,
			fmt.Sprintf("restore the main worktree at %s, or prune this stale linked worktree (`git worktree prune` from a healthy checkout), or run `ctxloom init` here to make this worktree a deliberately separate project", info.MainRoot),
			"%s is a linked git worktree, but its main worktree at %s is missing or unreadable", dir, info.MainRoot)
		return
	}
	strictness.FailOnce(strictness.ClassConfig,
		fmt.Sprintf("run ctxloom from %s, or run `ctxloom init` here to make this worktree a deliberately separate project", info.MainRoot),
		"this is a linked git worktree of the project at %s (no .ctxloom of its own)", info.MainRoot)
}

// GetBundleDirs returns the project's AUTHORED bundle directories — the
// committed content tree (.ctxloom/content/bundles), NOT the gitignored cache.
// This is the set every authored-bundle path resolves against: `bundle create`
// writes here, `bundle list` lists it, and `sign --all` signs exactly it (a
// publishing repo's bundles ARE this directory). The cache
// (paths.CacheBundlesPath) holds remote-pull artifacts the project has no authority
// to author or sign, so it is deliberately absent.
//
// A cache/bundles left holding AUTHORED work from the pre-content layout is
// not silently skipped — that would delete a user's bundles from view. See
// legacyCacheBundlesSignpost.
func (c *Config) GetBundleDirs() []string {
	fs := c.getFS()
	var dirs []string
	for _, appPath := range c.appPaths {
		c.legacyCacheBundlesSignpost(fs, appPath)
		bundleDir := paths.LocalBundlesPath(appPath)
		if info, err := fs.Stat(bundleDir); err == nil && info.IsDir() {
			dirs = append(dirs, bundleDir)
		}
	}
	return dirs
}

// bundleReaderDirs returns the project's authored bundle directories WITHOUT
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
func (c *Config) bundleReaderDirs() []string {
	fs := c.getFS()
	dirs := make([]string, 0, len(c.appPaths))
	for _, appPath := range c.appPaths {
		c.legacyCacheBundlesSignpost(fs, appPath)
		dirs = append(dirs, paths.LocalBundlesPath(appPath))
	}
	return dirs
}

// legacyCacheBundlesSignpost records a fatal ClassMigration finding when
// .ctxloom/cache/bundles still holds AUTHORED bundles — bundles written there
// by the pre-content-tree `bundle create`, which the authored read/write path
// no longer looks at. Ignoring them silently would make a user's own work
// vanish from `bundle list` and `sign --all` with no explanation, so the move
// is demanded, not performed: ctxloom does not rewrite content it did not
// author in this run (no-backward-compat-shims — re-place, don't shim).
//
// Remote-pull artifacts in the same tree (identified by a `_source.sha`) are
// genuine cache and
// never fire this: they are regenerable from the lockfile + clone cache.
//
// A pulled DIRECTORY-FORM bundle is regenerable cache too, and it carries no
// `_source` block — it cannot: its files are the publisher's exact bytes, and
// stamping a marker into them would mean the tree a consumer holds is not the
// tree that was published. The lockfile is asked instead, which is the actual
// authority on what was pulled, and it is asked LAZILY: only once a walk has
// already found something it would otherwise complain about, so the ordinary
// path (nothing stranded) still costs no lockfile read.
//
// FailOnce, because GetBundleDirs is called many times per process (every
// loader build) and the finding must not stack up inside one startup window.
func (c *Config) legacyCacheBundlesSignpost(fs afero.Fs, appPath string) {
	cacheBundles := paths.CacheBundlesPath(appPath)
	stranded := strandedAuthoredBundles(fs, cacheBundles)
	if len(stranded) == 0 {
		return
	}
	stranded = withoutPulledTrees(stranded, c.pulledTreeRoots(appPath))
	if len(stranded) == 0 {
		return
	}
	strictness.FailOnce(strictness.ClassMigration,
		fmt.Sprintf("move them into the committed content tree: mkdir -p %s && git mv %s/* %s/ (or plain mv outside git)",
			paths.LocalBundlesPath(appPath), cacheBundles, paths.LocalBundlesPath(appPath)),
		"%s holds %d authored bundle(s) (%s) but authored bundles now live in %s — the cache is gitignored and is no longer read, so these are invisible to `bundle list`, `run`, and `sign --all`",
		cacheBundles, len(stranded), strings.Join(stranded, ", "), paths.LocalBundlesPath(appPath))
}

// pulledTreeRoots returns the cache-relative directories that lockfile-pinned
// DIRECTORY-FORM bundles install into, as forward-slash prefixes.
//
// A failure to read the lockfile yields nothing, which makes the caller's
// filter a no-op and every cache YAML stranded again. That is the safe
// direction: the signpost's whole bias is that anything it cannot prove is
// regenerable gets named, and a lockfile it could not read proves nothing.
func (c *Config) pulledTreeRoots(appPath string) []string {
	lock, err := remote.NewLockfileManager(appPath, c.lockfileFSOptions()...).Load()
	if err != nil || lock == nil {
		return nil
	}
	cacheBundles := paths.CacheBundlesPath(appPath)
	var roots []string
	for key := range lock.Bundles {
		ref, perr := remote.ParseReference(key)
		if perr != nil || !ref.IsCanonical() {
			continue
		}
		rel, rerr := filepath.Rel(cacheBundles, ref.LocalTreePath(appPath))
		if rerr != nil {
			continue
		}
		roots = append(roots, filepath.ToSlash(rel)+"/")
	}
	return roots
}

// withoutPulledTrees drops every stranded entry that lies inside one of the
// pulled tree roots. Matching is on the SLASH-TERMINATED root so a bundle named
// "atelier" cannot swallow a stranded "atelier-notes.yaml" beside it.
func withoutPulledTrees(stranded, roots []string) []string {
	if len(roots) == 0 {
		return stranded
	}
	kept := stranded[:0:0]
	for _, s := range stranded {
		pulled := false
		for _, root := range roots {
			if strings.HasPrefix(s, root) {
				pulled = true
				break
			}
		}
		if !pulled {
			kept = append(kept, s)
		}
	}
	return kept
}

// strandedAuthoredBundles walks a legacy cache/bundles tree and returns the
// base names of every YAML that is NOT a remote-pull artifact — i.e. every file
// that can only have been authored locally. Unreadable/unparseable files are
// treated as authored: a file we cannot prove is regenerable cache is work we
// must not tell the user to ignore.
func strandedAuthoredBundles(fs afero.Fs, cacheBundles string) []string {
	if info, err := fs.Stat(cacheBundles); err != nil || !info.IsDir() {
		return nil
	}
	var stranded []string
	rel := func(path string) string {
		r, rerr := filepath.Rel(cacheBundles, path)
		if rerr != nil {
			return filepath.Base(path)
		}
		return filepath.ToSlash(r)
	}
	walkErr := afero.Walk(fs, cacheBundles, func(path string, info os.FileInfo, err error) error {
		if strandedCacheEntry(fs, path, info, err) {
			stranded = append(stranded, rel(path))
		}
		return nil //nolint:nilerr // one unreadable entry never aborts the scan
	})
	if walkErr != nil {
		// The callback never returns an error, so this is defence against a
		// future one rather than a live path — but a scan that stopped early
		// under-reports, and under-reporting is precisely what this function
		// must not do silently.
		clidiag.Warn("ctxloom", "scan of %s stopped early (%v); the stranded-bundle list may be incomplete", cacheBundles, walkErr)
	}
	sort.Strings(stranded)
	return stranded
}

// strandedCacheEntry decides whether one walked entry under a legacy
// cache/bundles tree is authored work rather than regenerable cache.
//
// The bias is stated in strandedAuthoredBundles' doc and this is where it lives:
// anything we cannot PROVE is regenerable counts. An entry we could not even
// read is the strongest such case — a directory we cannot enumerate hides an
// unknown number of authored bundles, and naming it is the only honest thing
// left to say — while anything that is neither a directory nor a YAML was never
// a bundle and stays out.
func strandedCacheEntry(fs afero.Fs, path string, info os.FileInfo, err error) bool {
	if err != nil {
		return (info != nil && info.IsDir()) || strings.HasSuffix(path, ".yaml")
	}
	if info == nil || info.IsDir() || !strings.HasSuffix(info.Name(), ".yaml") {
		return false
	}
	data, rerr := afero.ReadFile(fs, path)
	if rerr != nil {
		return true // unreadable: cannot be shown to be cache, so it is authored
	}
	// Legacy remote-pull artifacts embed a `_source` block; a non-empty SHA
	// there unambiguously marks one.
	var meta struct {
		Source struct {
			SHA string `yaml:"sha"`
		} `yaml:"_source"`
	}
	return yaml.Unmarshal(data, &meta) != nil || meta.Source.SHA == ""
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
	// It goes AFTER the project reader, and that is not cosmetic. Loader.FS()
	// returns the first reader that has a filesystem, and the builtin reader has
	// one — the EMBEDDED fs. Listing it first made FS() report the embedded
	// filesystem, so every project skill's trust preimage was derived from a
	// tree that does not exist there and the skill was silently withheld. That
	// is precisely the hazard Loader.FS()'s own doc warns about, reached by
	// reader order alone.
	readers := []bundles.Reader{bundles.NewProjectReader(fsys, c.bundleReaderDirs(), bundles.WithTrustRoot(c.TrustRoot()))}
	readers = append(readers, bundles.NewBuiltinReader(bundles.WithTrustRoot(c.TrustRoot())))
	readers = append(readers, c.remoteBundleReaders()...)
	readers = append(readers, c.companionReader())
	readers = append(readers, lc.extraReaders...)

	loader := bundles.NewLoader(readers...)
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
		probe = ProbeCompanionLoadouts
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
//   - remote/canonical ref → the FetchItem primitive over the local git clone
//     cache (remote.FetchRefBytes), exactly as before;
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
		data, err := remote.FetchRefBytes(context.Background(), factory, auth, ref, commit)
		if err != nil {
			return nil, err
		}
		return bundles.ParseBundle(data)
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
// its publisher signed — so it is classed as a trust failure, like the
// single-file tamper branch it mirrors. A fix line that cannot fix the thing it
// is attached to is worse than no fix line at all.
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
	rawBytes, failures := remote.LoadAllBytes(ctx, reader)

	// The trust root (embedded + user + project allowed_signers) is resolved once
	// for the whole set and handed to every reader, so no two pinned bundles are
	// judged against different roots.
	root := c.TrustRoot()

	out := make([]bundles.Reader, 0, len(rawBytes))
	for _, canonical := range collections.SortedKeys(rawBytes) {
		if _, ok := lock.Bundles[canonical]; !ok {
			continue
		}
		tree, terr := documentTree(canonical, rawBytes[canonical], signatureFor(ctx, reader, canonical))
		if terr != nil {
			failures[canonical] = terr
			continue
		}
		out = append(out, bundles.NewRepoFSReader(tree, canonical,
			bundles.WithTrustRoot(root),
			bundles.WithPinnedRevision(lock.Bundles[canonical].SHA)))
	}
	out = append(out, c.treeBundleReaders(lock, root, failures)...)
	reportBundleLoadFailures(failures)
	return out
}

// signatureFor reads a bundle's detached `.sig` sibling. A MISSING signature
// (the common case) is unsigned content, not an error, and any other read
// failure is degraded the same way rather than blocking the bundle: the
// fail-safe direction is "more review", and unsigned remote content is withheld
// until a human reviews it anyway. What must NOT happen is a signature that
// EXISTS being reported as absent — that is the reader's business, and it only
// ever sees bytes that were actually there.
func signatureFor(ctx context.Context, src remote.BundleSignatureSource, canonical string) []byte {
	sig, err := src.ReadBundleSignature(ctx, canonical)
	if err != nil {
		return nil
	}
	return sig
}

// documentTree presents one single-file remote bundle as the pinned tree its
// reader reads: the document under the ref's own leaf name, with its detached
// signature beside it — which is exactly the shape those bytes have in the
// publisher's repository.
func documentTree(canonical string, data, sig []byte) (bundles.TreeFS, error) {
	leaf := path.Base(strings.TrimSuffix(canonical, "/"))
	files := map[string][]byte{leaf + ".yaml": data}
	if len(sig) > 0 {
		files[leaf+".yaml"+bundles.SigSuffix] = sig
	}
	tree, err := content.NewMapTreeFS(files)
	if err != nil {
		return nil, fmt.Errorf("present the pinned bytes of %q as a tree: %w", canonical, err)
	}
	return tree, nil
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
