// Package bundles provides types and utilities for ctxloom bundles.
// Bundles are the primary content unit that group related fragments,
// prompts, MCP server configurations, and profiles with a single version.
package bundles

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/ident"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
)

// Bundle represents a versioned collection of related content.
// All items within a bundle share the same version.
// Each fragment and prompt is distilled individually with bundle context.
type Bundle struct {
	// Metadata
	Version      string   `yaml:"version"`
	Tags         []string `yaml:"tags,omitempty"`
	Author       string   `yaml:"author,omitempty"`
	Description  string   `yaml:"description,omitempty"`
	Notes        string   `yaml:"notes,omitempty"`        // Human-readable, not sent to AI
	Installation string   `yaml:"installation,omitempty"` // Setup instructions, shown on install

	// Content maps (keyed by name)
	Fragments map[string]BundleFragment `yaml:"fragments,omitempty"`
	Commands  map[string]BundleCommand  `yaml:"commands,omitempty"`
	MCP       map[string]BundleMCP      `yaml:"mcp,omitempty"` // MCP servers

	// Skills are Agent Skill packages (SKILL.md dir + optional scripts/assets),
	// model-invoked via progressive disclosure — a different concept from
	// Commands (user-invoked slash templates). A command-shaped entry (an
	// inline `content:`) under `skills:` is refused by the strict decode.
	Skills map[string]BundleSkill `yaml:"skills,omitempty"`

	// Profiles shipped with this bundle, keyed by name. A profile is an
	// ungated, COMPOUND item — it composes leaves (fragments/commands/mcp/hooks/
	// llm/parents/variables) into a runnable context unit — so a bundle that
	// ships fragments can also ship the profiles that compose them, as one unit.
	// Addressed by "<bundle>#profiles/<name>" (refuri.ProfileSelector) and seeded
	// into the shared profile loader so a bundle profile resolves/runs exactly
	// like a top-level or local profile (config bundle-profile seed). The profile
	// DEFINITION is never trust-gated (no ident.ItemKind for profiles, never
	// baselined); its constituent fragments/commands still gate at content
	// assembly and any mcp/hooks it pulls in still gate at the exec choke.
	Profiles map[string]BundleProfile `yaml:"profiles,omitempty"`

	// Hooks shipped with this bundle (e.g. PostFileEdit plan-stamping).
	// Hooks land in backend settings via ApplyHooks → ResolveBundleHooks.
	// A remote-sourced bundle's hooks change only when its pin moves, and
	// `deps upgrade` discloses every hook it would change before --yes.
	Hooks BundleHooks `yaml:"hooks,omitempty"`

	// Name is the bundle's DECLARED identity. A bundle that declares `name:`
	// answers to that name; one that declares nothing falls back to the name
	// its location implies (ExtractBundleName for a file on disk, the ref for a
	// pinned or companion source). The declared name WINS: a reader may write
	// its location-derived fallback only into an empty Name — see
	// localFSReader.readBundle, repoFSReader, and companionReader.read.
	Name string `yaml:"name,omitempty"`

	// Internal fields (not serialized)
	//
	// Path is OVERLOADED, and callers must not treat it as a filesystem path
	// without asking. It is a real path to the bundle.yaml for an on-disk
	// bundle, but it is "" for a companion-seeded bundle (config/companions.go
	// sets Name and the signer and never a Path) and a synthetic sentinel
	// ("<remote>:…", "<remote-version>:…", "<seeded>:…") for the seeded kinds.
	// Anything that needs the DIRECTORY a bundle's files live in must go
	// through FSDir, which refuses the non-filesystem values instead of
	// silently yielding "." (see FSDir's doc for what that cost).
	Path string `yaml:"-"` // File path for saving; see FSDir before using as one
	// sourceRef is the bundle's LOCATION-DERIVED canonical ref, and it is the
	// sole input to contentSourceRef — the content trust key. Every shape it
	// takes is decided by WHERE the bundle was found, never by what it says
	// about itself: the class-appropriate minter's BundleRef for a remote
	// (cloned) source, a companion loadout (ident.CompanionRef), and the
	// path-relative resolution
	// name for a bundle in the project's own tree (ident.LocalRef) — the
	// last of those is what lets project content auto-trust.
	//
	// newRead stamps the resolution ref here whenever a reader left it empty,
	// so it is never empty on a read a reader emitted. That backstop is a
	// SECURITY property, not tidiness: Bundle.Name is declared in the bundle's
	// own YAML (`name:`), so falling back to it would let the content being
	// judged choose its own trust key — a project bundle declaring
	// `name: ctxloom:companion@ltk` would claim the companion's trust identity, and
	// a bundle that renamed itself would move off its own recorded decisions.
	// The declared name is CONTENT, and therefore never an input to the
	// identity it is keyed under.
	//
	// It is an ident.BundleRef, not a string: the field used to carry BOTH a
	// string rendering and this structured counterpart, set by the same call
	// sites through the matching class minter — so the two could never
	// independently disagree on what a bundle's source IS, only on how it was
	// spelled. Collapsing to one typed field removes the spelling question
	// entirely rather than keeping two representations in sync.
	//
	// Every call site that sets sourceRef does so through a class minter —
	// localFSReader (builtin and project), newRead's local fallback,
	// companionReader, repoFSReader, and
	// loader_version.go's bundleAtVersion for a version-pinned read. The zero
	// BundleRef is therefore a REACHABLE, meaningful value — a mint that
	// failed (an unfoldable repo-URL spelling — see Ref.AsBundleRef's doc) —
	// not merely "unset"; BundleRead.SourceRef reports it as-is rather than
	// guessing, and a producer minting an item ref from it gets ItemRefFor's
	// error and drops that item (warnUnmintableSource has already named the
	// spelling that failed) rather than inventing a stand-in address.
	//
	// Because the zero value is reachable and meaningful, "has a reader
	// already stamped this" cannot be read off sourceRef itself — a failed
	// mint and an untouched field are the same ident.BundleRef{}. sourceRefSet
	// is that separate sentinel: newRead's local fallback (the only caller
	// that ever asks) checks it, not sourceRef's value, so an UNMINTABLE
	// canonical ref from repoFSReader stays unaddressable rather than being
	// silently re-stamped as a local bundle of that name.
	sourceRef    ident.BundleRef `yaml:"-"`
	sourceRefSet bool            `yaml:"-"`

	// self marks ctxloom's OWN companion loadout (CompanionLoadout.Self).
	// It is INTRINSIC content: nobody
	// installed it and nobody can remove it, so a listing of what the user
	// installed leaves it out, while it stays addressable by its ref.
	self bool `yaml:"-"`
}

// Self reports whether this bundle is ctxloom's own companion loadout —
// intrinsic content, never installed; see companionReader.read.
func (b *Bundle) Self() bool { return b.self }

// contentSourceRef returns the bundle's honest source ref for content trust
// gating: the canonical ref of a seeded (cloned) bundle, the CompanionRef of
// a companion loadout, or the LocalRef of a project (fs) bundle. Locality
// flows from this into the trust cascade, so a clone's TEXT gates like its
// executables, a companion's TEXT carries the SAME identity whether it was
// selected by ref or delivered unconditionally (two identities for one item
// is how a rejection via one route fails to withhold the other), and a
// project bundle's bare token keys IsLocal and auto-trusts. "Text to an LLM
// is executable."
//
// It reads sourceRef and NOTHING ELSE. In particular it must never fall back
// to Bundle.Name: Name is DECLARED in the bundle's own YAML, so a fallback
// would make the content being judged an input to its own trust key — a
// project bundle declaring `name: ctxloom:companion@ltk` would key as the companion
// and inherit its grants. newRead stamps the location-derived resolution ref
// into sourceRef for every read a reader emits, so there is nothing for a
// fallback to do but reopen that hole (outdated-recoil).
//
// That last sentence is measured, not assumed, and it is why NO test can be
// written that dies to restoring the fallback here on its own: with newRead
// stamping, the fallback is unreachable. Its removal is trap removal — the
// stamp is the load-bearing half, and the tests that die are the ones that die
// when the stamp goes (internal/adapters/operations/declared_name_trust_test.go).
func (b *Bundle) contentSourceRef() ident.BundleRef {
	if b == nil {
		return ident.BundleRef{}
	}
	return b.sourceRef
}

// nonFilesystemPathPrefixes are the synthetic Bundle.Path sentinels. None of
// them is a filesystem path, and none may be handed to filepath.Dir and used
// as one. They are listed here rather than at their producers so a new sentinel
// added anywhere fails FSDir loudly instead of silently reopening this hazard —
// the values are matched by prefix, and every current producer uses the
// "<word>:" shape (the repofs reader's remotePathSentinel "<remote>:",
// loader_version.go's "<remote-version>:").
var nonFilesystemPathPrefixes = []string{remotePathSentinel, "<remote-version>:"}

// FSDir returns the DIRECTORY this bundle's files live in, or an error when the
// bundle has none — which is the common case, not an exotic one: companion-,
// remote- and seeded-bundle values of Path are not filesystem paths at all.
//
// Callers used filepath.Dir(b.Path) directly, and the two ways that fails are
// both silent:
//
//   - filepath.Dir("") is ".", and so is filepath.Dir("<seeded>:some-bundle")
//     (no separator in it). A companion or seeded bundle's files therefore
//     resolved against the PROCESS WORKING DIRECTORY — whatever happened to sit
//     at ./skills/<name> was loaded, trust-gated and materialized AS that
//     bundle's content. Arbitrary project-local files, adopted under a
//     bundle's identity.
//   - filepath.Dir("<remote>:some/bundle@sha") is "<remote>:some", a garbage
//     relative path that simply does not exist, so a remote bundle's skills
//     were unloadable with no diagnostic.
//
// Refusing is the correct answer for both: a bundle with no directory has no
// files to resolve, and guessing at one is how the first case happened. The
// error names the offending value so the caller's warning is diagnosable.
func (b *Bundle) FSDir() (string, error) {
	if b == nil {
		return "", fmt.Errorf("bundle is nil")
	}
	if b.Path == "" {
		return "", fmt.Errorf("bundle %q has no filesystem path (companion-seeded bundles carry none), so it has no directory to resolve files against", b.Name)
	}
	for _, prefix := range nonFilesystemPathPrefixes {
		if strings.HasPrefix(b.Path, prefix) {
			return "", fmt.Errorf("bundle %q has the synthetic path %q, not a filesystem path, so it has no directory to resolve files against", b.Name, b.Path)
		}
	}
	return filepath.Dir(b.Path), nil
}

// BundleHook is the bundle-authoring shape of a hook: the wire.Hook fields a
// bundle may declare, minus the SCM marker (bundle hooks are stamped at the
// operations boundary, not hand-authored). The conversion to wire.Hook lives in
// config.ResolveBundleHooks.
type BundleHook struct {
	// Tool narrows a tool event to one neutral tool class (wire.ToolClass —
	// shell, file_edit, skill). It is the ONLY narrowing a bundle declares:
	// an engine-native matcher would name one engine's tools, so each
	// engine's hooks approach maps the class to its own (agent.BindHooks).
	Tool    wire.ToolClass `yaml:"tool,omitempty"`
	Command string         `yaml:"command,omitempty"`
	// Args, when set, runs the hook in exec form: Command is the executable,
	// spawned with these arguments and no shell (wire.Hook.Args).
	Args    []string `yaml:"args,omitempty"`
	Type    string   `yaml:"type,omitempty"`
	Prompt  string   `yaml:"prompt,omitempty"`
	Timeout int      `yaml:"timeout,omitempty"`
	Async   bool     `yaml:"async,omitempty"`
	// PreToolFallback (session_start only): the hook is idempotent and may
	// fire on PreToolUse instead on an agent without a session-start event.
	// See wire.Hook.PreToolFallback.
	PreToolFallback bool `yaml:"pre_tool_fallback,omitempty"`
	// Tags are merged with the bundle's and evaluated by the host — a link
	// group membership (links.go) rides here.
	Tags []string `yaml:"tags,omitempty"`

	// Order sequences this hook against its siblings WITHIN its event, sparsely
	// (see wire.HookOrderStep). It does NOT sequence against other bundles:
	// across bundles hooks still merge by pure APPEND, and the merge sequence is
	// the bundles' order, not any hook's. A bundle says where ITS hooks go
	// relative to each other and nothing more.
	//
	// A POINTER, because "declared 0" and "declared nothing" resolve differently
	// (wire.HookOrderLess sorts an undeclared hook LAST) and a plain int could
	// not tell them apart. Order is scheduling, not behaviour.
	Order *int `yaml:"order,omitempty"`
}

// BundleHooks mirrors wire.UnifiedHooks. Same lifecycle events; backend-
// specific hooks are deliberately not supported in bundles (would couple
// authoring to a particular backend's tool naming).
type BundleHooks struct {
	PreTool      []BundleHook `yaml:"pre_tool,omitempty"`
	PostTool     []BundleHook `yaml:"post_tool,omitempty"`
	SessionStart []BundleHook `yaml:"session_start,omitempty"`
	SessionEnd   []BundleHook `yaml:"session_end,omitempty"`
	TurnEnd      []BundleHook `yaml:"turn_end,omitempty"`
	PreShell     []BundleHook `yaml:"pre_shell,omitempty"`
	PostFileEdit []BundleHook `yaml:"post_file_edit,omitempty"`
	TurnStart    []BundleHook `yaml:"turn_start,omitempty"`
}

// HasAny reports whether the bundle ships any hooks. Used by the loader to
// skip the merge cost for hookless bundles.
func (h BundleHooks) HasAny() bool {
	for _, event := range hookEventOrder {
		if len(h.eventHooks(event)) > 0 {
			return true
		}
	}
	return false
}

// Hook event names. They double as the stable event component of a bundle
// hook's identity ("<bundle>#hooks/<event>/<index>") and as the canonical
// iteration order below, and match the BundleHooks YAML field tags.
const (
	HookEventPreTool      = wire.HookEventPreTool
	HookEventPostTool     = wire.HookEventPostTool
	HookEventSessionStart = wire.HookEventSessionStart
	HookEventSessionEnd   = wire.HookEventSessionEnd
	HookEventPreShell     = wire.HookEventPreShell
	HookEventPostFileEdit = wire.HookEventPostFileEdit
	// HookEventTurnEnd and HookEventTurnStart are APPENDED to hookEventOrder
	// rather than slotted in beside their siblings: that order is part of a
	// hook's identity ("<bundle>#hooks/<event>/<index>" is per-event, but
	// Entries() walks this slice), and inserting an event mid-list would
	// renumber nothing while still reordering every hook report against the
	// previous one. TestBundleHooks_IdentityIsStableUnderVocabularyGrowth
	// holds the baseline.
	HookEventTurnEnd   = wire.HookEventTurnEnd
	HookEventTurnStart = wire.HookEventTurnStart
)

// hookEventOrder is the canonical event order for hook identity + enumeration.
// Entries() and hook extraction both walk it so a reported hook's ref matches
// the one extraction addresses. A new event goes LAST.
var hookEventOrder = []string{
	HookEventPreTool, HookEventPostTool, HookEventSessionStart,
	HookEventSessionEnd, HookEventPreShell, HookEventPostFileEdit,
	HookEventTurnEnd, HookEventTurnStart,
}

// checkHookTools refuses a hook narrowed to a tool class outside the neutral
// vocabulary (wire.ToolClasses), naming the hook and the classes it may use.
func (h BundleHooks) checkHookTools() error {
	for _, event := range hookEventOrder {
		for i, hook := range h.eventHooks(event) {
			if hook.Tool != "" && !hook.Tool.Known() {
				return fmt.Errorf("hooks.%s[%d]: tool %q is not a tool class; use one of %s", event, i, hook.Tool, wire.ToolClassList())
			}
		}
	}
	return nil
}

// eventHooks returns the hook slice for an event (nil for an unknown event).
func (h BundleHooks) eventHooks(event string) []BundleHook {
	switch event {
	case HookEventPreTool:
		return h.PreTool
	case HookEventPostTool:
		return h.PostTool
	case HookEventSessionStart:
		return h.SessionStart
	case HookEventSessionEnd:
		return h.SessionEnd
	case HookEventPreShell:
		return h.PreShell
	case HookEventPostFileEdit:
		return h.PostFileEdit
	case HookEventTurnEnd:
		return h.TurnEnd
	case HookEventTurnStart:
		return h.TurnStart
	}
	return nil
}

// HookEntry is one bundle hook paired with its stable identity: the event
// it fires on and its index within that event's list. Bundle hooks are an
// ordered list with no author-given name, so (event, index) is the addressable
// identity a hook is addressed by.
type HookEntry struct {
	Event string
	Index int
	Hook  BundleHook
}

// ID returns the stable per-hook identity "<event>/<index>", the <id> in the
// ref "<bundle>#hooks/<id>". The index is the hook's authored position in
// its event list.
func (e HookEntry) ID() string {
	return e.Event + "/" + strconv.Itoa(e.Index)
}

// Entries returns every bundle hook with its identity, in canonical event order
// then authored index. Every hook report (bundle listing, pin-change diffs,
// links) enumerates hooks through this scheme so the refs agree.
func (h BundleHooks) Entries() []HookEntry {
	var out []HookEntry
	for _, event := range hookEventOrder {
		for i, hook := range h.eventHooks(event) {
			out = append(out, HookEntry{Event: event, Index: i, Hook: hook})
		}
	}
	return out
}

// EntryByID resolves a hook identity ("<event>/<index>") back to its entry. It
// reports ok=false for a malformed id or an out-of-range index rather than
// guessing at a neighbouring hook.
func (h BundleHooks) EntryByID(id string) (HookEntry, bool) {
	event, idxStr, found := strings.Cut(id, "/")
	if !found {
		return HookEntry{}, false
	}
	idx, err := strconv.Atoi(idxStr)
	if err != nil || idx < 0 {
		return HookEntry{}, false
	}
	hooks := h.eventHooks(event)
	if idx >= len(hooks) {
		return HookEntry{}, false
	}
	return HookEntry{Event: event, Index: idx, Hook: hooks[idx]}, true
}

// BundleMCP is the bundle-authoring shape of an MCP server: the wire.MCPServer
// fields a bundle may declare, minus the SCM marker (bundle servers are stamped
// at the resolve boundary, not hand-authored). The conversion to wire.MCPServer
// lives in config.extractMCPFromBundle.
//
// A server is EXACTLY ONE of two things, and the rule is wire.MCPServer's:
// a stdio server ctxloom launches (Command, with Args/Env) or a network-hosted
// server an engine dials (URL, with Headers), or served by the running
// session's endpoint (ServedBy). Command is therefore NOT
// required — the `omitempty` on it is load-bearing for the remote case — and
// the one-of rule is checked at LOAD by Bundle.checkMCPTargets, which calls
// wire.MCPServer.Validate so there is one definition of it rather than two.
//
// There is deliberately NO transport field, for the reason stated on
// wire.MCPServer: the URL's scheme already names the protocol, and the engine
// writers derive the discriminator from it at write time rather than storing it.
type BundleMCP struct {
	Command string            `yaml:"command,omitempty"`
	Args    []string          `yaml:"args,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
	URL     string            `yaml:"url,omitempty"`     // Endpoint of a network-hosted server; its scheme is the transport
	Headers map[string]string `yaml:"headers,omitempty"` // HTTP headers sent when dialing URL (e.g. Authorization)
	// ServedBy is the companion's DYNAMIC declaration (wire.ServedBySessionEndpoint):
	// the running session's endpoint serves this entry, and the bundle
	// contributes nothing executable — the host renders the endpoint through
	// the engine's dynamic approach at delivery. It is host-evaluated
	// ROUTING.
	ServedBy     string   `yaml:"served_by,omitempty"`
	Tags         []string `yaml:"tags,omitempty"`         // Additional tags (merged with bundle tags); host-evaluated routing, never executed
	Notes        string   `yaml:"notes,omitempty"`        // Human-readable notes, not sent to AI
	Installation string   `yaml:"installation,omitempty"` // Setup/installation instructions; presented to the user
}

// AsWire converts to the wire shape for validation. It deliberately does NOT
// stamp SCM: that marker names the bundle a server was RESOLVED from, which is
// not a thing a bundle author can say about their own file, and
// config.extractMCPFromBundle is where it is applied.
func (m BundleMCP) AsWire() wire.MCPServer {
	return wire.MCPServer{
		Command:  m.Command,
		Args:     m.Args,
		Env:      m.Env,
		URL:      m.URL,
		Headers:  m.Headers,
		ServedBy: m.ServedBy,
	}
}

// ItemBody is the payload a fragment and a command carry IDENTICALLY: the
// authored prose plus the metadata that travels with it. It is one type
// because the two were one shape maintained twice — the pair drifted, and a
// reader that copied every field but one dropped a fragment's premise on the
// way in with nothing to show for it.
//
// It is EMBEDDED and `yaml:",inline"`, so the wire form is unchanged: a
// fragment and a command serialize the same keys they always did. What differs
// between the two kinds — a fragment's Premise, a command's Description and
// Exports — stays on the kind that has it, which is now the only thing either
// declares.
//
// BundleMCP deliberately does NOT embed this: it shares several field names but
// carries no Content and no distillation, so folding it in would mean a type
// whose fields are meaningless for a third of its users.
//
// What reaches the agent is decided by the surface model (FragmentSurface,
// CommandSurface), not by this struct: a field the surface does not carry
// never reaches the agent.
type ItemBody struct {
	Tags         []string `yaml:"tags,omitempty"`         // Additional tags (merged with bundle tags); host-evaluated routing, never shown
	Notes        string   `yaml:"notes,omitempty"`        // Human-readable notes, not sent to AI
	Installation string   `yaml:"installation,omitempty"` // Setup/installation instructions, not sent to AI (surfaced to the user only, e.g. pull/list output)
	Content      string   `yaml:"content"`
	ContentHash  string   `yaml:"content_hash,omitempty"` // recorded hash of Content; circular to sign
	Distilled    string   `yaml:"distilled,omitempty"`
	DistilledBy  string   `yaml:"distilled_by,omitempty"` // which model produced Distilled
	NoDistill    bool     `yaml:"no_distill,omitempty"`
}

// BundleFragment defines a fragment within a bundle.
type BundleFragment struct {
	ItemBody `yaml:",inline"`
	// Premise states, in prose, WHEN this fragment applies and who it is for.
	// It is the fragment's own applicability condition, addressed to the acting
	// agent, because the great majority of guidance conditions ("you are about
	// to remove a worktree") are knowable only at the moment of action and not
	// by the host assembling the context.
	//
	// EMPTY MEANS ALWAYS LOADED. Absence is not "no opinion", it is the
	// assertion that the fragment applies unconditionally, and that is the
	// safety property the whole mechanism rests on: a corpus with no premise
	// anywhere assembles exactly as it did before. A fragment is opted OUT of
	// unconditional loading only by being given a premise, never by omission.
	//
	// It is PRESENTED — the premise index puts it in front of the agent
	// (FragmentSurface). It stays outside the RECORDED content hash
	// (ComputeContentHash), which drives re-distillation of the body alone.
	Premise string `yaml:"premise,omitempty"`
}

// BundleCommand defines a slash command within a bundle.
//
// Description and Exports are PRESENTED: the description is advertised to
// the agent as the command's help text, and an engine writes its own export
// block into the command file it reads — a block can carry a capability
// grant (CommandSurface).
type BundleCommand struct {
	ItemBody    `yaml:",inline"`
	Description string       `yaml:"description,omitempty"`
	Exports     EngineBlocks `yaml:"exports,omitempty"` // per engine name, opaque; that engine decodes its block
}

// BundleSkill defines an Agent Skill package within a bundle: a directory
// (default "skills/<name>", overridable via Path) containing a required
// SKILL.md — YAML frontmatter name+description, instructions body — plus
// arbitrary sibling files (scripts/, assets, references). Unlike
// BundleFragment/BundleCommand, a skill carries NO inline `content:` and NO
// distillation: SKILL.md's description IS the progressive-disclosure
// mechanism a model reads before deciding to load the rest of the package, and
// distilling a model-facing capability description would defeat that.
//
// The package's files are not declared here: the skill directory IS the
// package, and ParseSkillPackage reads its manifest from the tree.
//
// Exports is PRESENTED: an engine's block decides whether the package is
// offered to it at all.
type BundleSkill struct {
	Path    string       `yaml:"path,omitempty"`    // dir relative to bundle dir; default "skills/<name>" — where the host finds the tree, never shown
	Tags    []string     `yaml:"tags,omitempty"`    // Additional tags (merged with bundle tags); host-evaluated routing, never shown
	Notes   string       `yaml:"notes,omitempty"`   // Human-readable notes, not sent to AI
	Exports EngineBlocks `yaml:"exports,omitempty"` // per engine name, opaque; that engine decodes its block (name/description live in SKILL.md)
}

// BundleProfile is the shape of a profile shipped inside a bundle. It is the
// SAME type as a directory/top-level profile (profiles.Profile), so a bundle
// profile and an inline/config or remote profile resolve through one code path:
// the bundle-profile seed parses these straight into the shared profile loader.
// Reusing the type (rather than mirroring its fields) keeps the two from
// drifting and means a profile composes the same leaves wherever it is authored.
// The bundle YAML map key supplies the profile's Name (the struct's Name/Path
// are yaml:"-"); the seed sets Name to the "<bundle>#profiles/<name>" identity.
type BundleProfile = profiles.Profile

// ContentForm is ident.ContentForm: which materialization of an item's content
// was hashed or served. This package aliases it so every content-hash site
// here names the same type as the identity package does.
type ContentForm = ident.ContentForm

const (
	FormRaw       = ident.FormRaw
	FormDistilled = ident.FormDistilled
)

// hashContent is the single sha256 helper every content-hash computation in this
// package routes through. It returns the canonical "sha256:<hex>" digest.
func hashContent(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// HashPayload is the exported name for the one content-hash primitive, the
// ONLY way any caller outside this package turns item bytes into a content
// hash ("sha256:<hex>").
func HashPayload(payload []byte) string {
	return hashContent(payload)
}

// resolveEffective is the one shared compute primitive for the distillable item
// shape. Fragments and prompts carry the same Content/Distilled/NoDistill fields,
// so this picks the bytes to expose AND reports their form from the same
// predicate, with no raw fallback once distilled is chosen.
func resolveEffective(preferDistilled bool, content, distilled string, noDistill bool) (string, ContentForm) {
	if preferDistilled && distilled != "" && !noDistill {
		return distilled, FormDistilled
	}
	return content, FormRaw
}

// ItemSurface is one resolution of a distillable item for a form preference:
// the bytes the process stage SERVES and the layout form they were selected
// in, from a single call on the item so the two cannot drift.
type ItemSurface struct {
	Body []byte      // the bytes served to the agent
	Form ContentForm // the layout form Body was selected in
}

// Resolve is the process-stage resolution of this fragment: it only ever
// PREFERS — a fragment with no distilled form, or one that forbids
// distillation, still serves raw.
func (f *BundleFragment) Resolve(preferDistilled bool) ItemSurface {
	s := f.Surface(preferDistilled)
	return ItemSurface{Body: []byte(s.Body()), Form: s.Form()}
}

// Resolve is the process-stage resolution of this command, built on Surface
// exactly as the fragment's is: the gate decides on the framed surface, the
// agent is served the body.
func (p *BundleCommand) Resolve(preferDistilled bool) ItemSurface {
	s := p.Surface(preferDistilled)
	return ItemSurface{Body: []byte(s.Body()), Form: s.Form()}
}

// staleDistill is the one shared compare primitive: it reports whether a
// distillable item's distilled form is stale relative to its raw content (the
// re-distillation check). It compares the RECORDED hash against a freshly
// computed one — independent of trust, which never reads the author-supplied
// recorded field. Sharing it keeps fragments and prompts from drifting.
func staleDistill(noDistill bool, distilled, recordedHash, content string) bool {
	if noDistill {
		return false
	}
	if distilled == "" {
		return true
	}
	if recordedHash == "" {
		return true
	}
	return recordedHash != hashContent([]byte(content))
}

// ComputeContentHash computes the SHA256 hash of the raw authored content. This
// feeds the recorded content_hash that drives re-distillation (NeedsDistill).
func (f *BundleFragment) ComputeContentHash() string {
	return hashContent([]byte(f.Content))
}

// NeedsDistill returns true if this fragment needs distillation.
func (f *BundleFragment) NeedsDistill() bool {
	return staleDistill(f.NoDistill, f.Distilled, f.ContentHash, f.Content)
}

// FragmentSurface is the MODEL of what an agent is shown of a fragment: the
// premise it selects on and the body it receives, in the form the body was
// selected in. A field reaches the agent only by being on the surface.
//
// Its fields are unexported and read through getters so a delivery path
// cannot reach a human-only field (Notes, Installation) through the agent-
// surface API: the boundary "not sent to AI" is then a property of the type,
// not a comment on the field.
type FragmentSurface struct {
	premise string
	body    string
	form    ContentForm
}

// Surface resolves what the agent is shown of this fragment for a form
// preference. Resolve (the process stage's entry point) is built on it, so the
// body here is exactly the body the pipeline serves.
func (f *BundleFragment) Surface(preferDistilled bool) FragmentSurface {
	body, form := resolveEffective(preferDistilled, f.Content, f.Distilled, f.NoDistill)
	return FragmentSurface{premise: f.Premise, body: body, form: form}
}

// Premise is the applicability condition the agent evaluates; "" means the
// fragment loads unconditionally.
func (s FragmentSurface) Premise() string { return s.premise }

// Body is the text the agent receives, in Form.
func (s FragmentSurface) Body() string { return s.body }

// Form reports which materialization Body is.
func (s FragmentSurface) Form() ContentForm { return s.form }

// EffectiveContent returns distilled content if available and preferred.
// Falls back to original content if distilled is empty or NoDistill is true.
func (f *BundleFragment) EffectiveContent(preferDistilled bool) string {
	return f.Surface(preferDistilled).Body()
}

// ComputeContentHash computes the SHA256 hash of the raw authored content. This
// feeds the recorded content_hash that drives re-distillation (NeedsDistill).
func (p *BundleCommand) ComputeContentHash() string {
	return hashContent([]byte(p.Content))
}

// NeedsDistill returns true if this command needs distillation.
func (p *BundleCommand) NeedsDistill() bool {
	return staleDistill(p.NoDistill, p.Distilled, p.ContentHash, p.Content)
}

// CommandSurface is the MODEL of what an agent is shown of a command: the
// description it is advertised under, the per-engine export config the engine
// writes into the command file (help text, argument hint, tool grant, model,
// enablement), and the body it receives in the form it was selected in.
//
// Its fields are unexported and read through getters so a delivery path
// cannot reach a human-only field (Notes, Installation) through the agent-
// surface API.
type CommandSurface struct {
	description string
	exports     EngineBlocks
	body        string
	form        ContentForm
}

// Surface resolves what the agent is shown of this command for a form
// preference. Resolve (the process stage's entry point) is built on it, so the
// body here is exactly the body the pipeline serves.
func (p *BundleCommand) Surface(preferDistilled bool) CommandSurface {
	body, form := resolveEffective(preferDistilled, p.Content, p.Distilled, p.NoDistill)
	return CommandSurface{description: p.Description, exports: p.Exports, body: body, form: form}
}

// Description is the help text the command is advertised under.
func (s CommandSurface) Description() string { return s.description }

// Exports are the per-engine blocks, opaque; each engine decodes its own.
func (s CommandSurface) Exports() EngineBlocks { return s.exports }

// Body is the text the agent receives, in Form.
func (s CommandSurface) Body() string { return s.body }

// Form reports which materialization Body is.
func (s CommandSurface) Form() ContentForm { return s.form }

// EffectiveContent returns distilled content if available and preferred.
// Falls back to original content if distilled is empty or NoDistill is true.
func (p *BundleCommand) EffectiveContent(preferDistilled bool) string {
	return p.Surface(preferDistilled).Body()
}

// SkillNames returns sorted skill names.
func (b *Bundle) SkillNames() []string {
	return slices.Sorted(maps.Keys(b.Skills))
}

// Line is the hook's command as the one shell line it runs (wire.Hook.Line):
// Command itself in shell form, and in exec form the quoted argv.
func (h *BundleHook) Line() string {
	return wire.Hook{Command: h.Command, Args: h.Args}.Line()
}

// HasMCP returns true if bundle includes any MCP servers.
func (b *Bundle) HasMCP() bool {
	return len(b.MCP) > 0
}

// MCPCount returns the number of MCP servers in the bundle.
func (b *Bundle) MCPCount() int {
	return len(b.MCP)
}

// MCPNames returns sorted MCP server names.
func (b *Bundle) MCPNames() []string {
	return slices.Sorted(maps.Keys(b.MCP))
}

// FragmentCount returns the number of fragments in the bundle.
func (b *Bundle) FragmentCount() int {
	return len(b.Fragments)
}

// CommandCount returns the number of commands in the bundle.
func (b *Bundle) CommandCount() int {
	return len(b.Commands)
}

// FragmentNames returns sorted fragment names.
func (b *Bundle) FragmentNames() []string {
	return slices.Sorted(maps.Keys(b.Fragments))
}

// PromptNames returns sorted command names.
func (b *Bundle) PromptNames() []string {
	return slices.Sorted(maps.Keys(b.Commands))
}

// ProfileCount returns the number of profiles in the bundle.
func (b *Bundle) ProfileCount() int {
	return len(b.Profiles)
}

// ProfileNames returns sorted profile names.
func (b *Bundle) ProfileNames() []string {
	return slices.Sorted(maps.Keys(b.Profiles))
}

// ParseBundle parses raw YAML into a Bundle.
//
// The envelope's format generation (schemaver.Key) is read off the RAW bytes
// and an older one is migrated in memory (envelopeKind) before anything else
// looks at the document; a newer one is refused.
func ParseBundle(raw []byte) (*Bundle, error) {
	upgraded, err := envelopeKind.Upgrade(raw)
	if err != nil {
		return nil, err
	}

	// STRICT: a key the Bundle schema does not model is refused, not dropped.
	// The default unmarshal ignored it, so `hoooks:` or `promts:` loaded
	// clean and shipped a bundle quietly missing whatever its author wrote
	// under that key — this codebase's characteristic bug (exit 0, success
	// message, nothing happened). strictDecodeError names the key, where it
	// sat, and what it probably meant; the FILE comes from the caller's wrap.
	//
	// This runs AFTER the envelopeKind upgrade above, and the order is
	// load-bearing: an older bundle's legacy `prompts:` key is migrated to
	// `commands:` first, so strictness refuses only keys that are wrong TODAY
	// and never keys a past schema generation made legal.
	doc, err := decodeEnvelope(raw, upgraded)
	if err != nil {
		return nil, strictDecodeError(err)
	}
	bundle := doc.Bundle

	// A link id on exactly one item is the other side's typo, and left alone
	// it would deliver that item beside the tool it lacks. Refused here, at
	// the one place every bundle passes through, so it cannot be silent.
	if err := bundle.checkLinks(); err != nil {
		return nil, err
	}

	// An MCP entry that names neither a command nor a url — or both — is not
	// something a later stage can resolve by guesswork. Refused here, at the
	// one place every bundle passes through, for the same reason checkLinks is:
	// a malformed entry that loads cleanly reaches an engine writer as a server that cannot be launched or dialed, and the failure
	// surfaces far from the typo that caused it.
	if err := bundle.checkMCPTargets(); err != nil {
		return nil, err
	}

	// A tool class no engine maps would fail every engine's hook binding at
	// delivery, far from the typo. Refused here for the same reason.
	if err := bundle.Hooks.checkHookTools(); err != nil {
		return nil, err
	}

	bundle.initMaps()

	// A document that declares NOTHING is a truncated/empty file, not a bundle.
	// gopkg.in/yaml.v3 returns a nil error for "", whitespace, a comment-only
	// document, a bare `---`, `null` and `{}` alike — every one unmarshals into
	// a zero-value Bundle — so without this guard a truncated bundle.yaml, an
	// empty companion loadout payload, or an empty remote blob SUCCEEDED and
	// contributed zero items with no diagnostic anywhere: Loader reported a
	// successful load, and expandBundleRef only warns when Load errors. This is
	// the root cause under the publish-side guard in operations.PushBundle.
	//
	// The predicate is "no version AND no items". A version-only skeleton is
	// deliberately still valid: CreateBundle writes exactly that, and claiming a
	// name with one is authoring, not failure. So is an item with no version
	// (an authored bundle mid-edit). Only the document that says nothing at all
	// is refused. Metadata alone (name/description/tags/author) does not count
	// as saying something — it declares no content and carries no version, which
	// is precisely the truncated-file signature.
	if bundle.declaresNothing() {
		return nil, fmt.Errorf("bundle is empty: %d bytes parsed to a document declaring no version "+
			"and no items (fragments, commands, skills, mcp, profiles or hooks) — an empty, truncated, "+
			"comment-only or `null` file parses as valid YAML but is not a bundle", len(raw))
	}

	return &bundle, nil
}

// envelopeDocument is a bundle document as the strict decode and TreeEnvelope
// see it: the Bundle plus the format generation (schemaver.Key), which Bundle
// does not carry because ParseBundle consumes it — a parsed Bundle is always
// current.
type envelopeDocument struct {
	SchemaVersion int `yaml:"schema_version"`
	Bundle        `yaml:",inline"`
}

// decodeEnvelope strictly decodes an envelope that envelopeKind upgraded.
//
// When the migration edited nothing but the stamp, the RAW bytes are decoded,
// so a refused key is reported on the line the author's file holds it: the
// migrated document is re-encoded with the stamp added, which moves every
// line. That is the common case — every envelope written before
// schemaver.Key existed is generation 0, and most spell no retired key.
func decodeEnvelope(raw []byte, upgraded schemaver.Result) (envelopeDocument, error) {
	src := upgraded.Data
	if len(upgraded.Applied) > 0 && !stepsEdit(raw, upgraded.From) {
		src = raw
	}
	var doc envelopeDocument
	err := yamlx.DecodeStrict(src, &doc)
	return doc, err
}

// stepsEdit reports whether envelopeKind's steps from generation from edit
// raw's tree, by replaying them on a throwaway parse.
func stepsEdit(raw []byte, from int) bool {
	doc, err := upgrade.DecodeSingle(raw)
	if errors.Is(err, io.EOF) {
		return false
	}
	if err != nil {
		return true
	}
	before, err := upgrade.Encode(&doc)
	if err != nil {
		return true
	}
	for _, step := range envelopeKind.StepsAbove(from) {
		step.Apply(doc.Content[0])
	}
	after, err := upgrade.Encode(&doc)
	return err != nil || !bytes.Equal(before, after)
}

// initMaps replaces every nil content map with an empty one, so a consumer
// can range and index without a nil check per map.
func (b *Bundle) initMaps() {
	if b.Fragments == nil {
		b.Fragments = make(map[string]BundleFragment)
	}
	if b.Commands == nil {
		b.Commands = make(map[string]BundleCommand)
	}
	if b.MCP == nil {
		b.MCP = make(map[string]BundleMCP)
	}
	if b.Profiles == nil {
		b.Profiles = make(map[string]BundleProfile)
	}
	if b.Skills == nil {
		b.Skills = make(map[string]BundleSkill)
	}
}

// emptyBundle is a bundle declaring nothing, with its maps initialized — the
// RUN loadout of a companion that only speaks at setup.
func emptyBundle() *Bundle {
	b := &Bundle{}
	b.initMaps()
	return b
}

// checkMCPTargets enforces wire.MCPServer's one-of-Command|URL rule over every
// MCP entry the bundle declares, by asking wire.MCPServer.Validate itself. The
// rule is NOT restated here: a second copy of it would drift from the one the
// engine writers branch on, and the stale copy would keep its authority while
// admitting entries the wire type refuses.
//
// Entries are visited in sorted order so a bundle with several bad ones always
// reports the same first offender, rather than a different name per run.
func (b *Bundle) checkMCPTargets() error {
	for _, name := range slices.Sorted(maps.Keys(b.MCP)) {
		if err := b.MCP[name].AsWire().Validate(); err != nil {
			return fmt.Errorf("bundle mcp server %q: %w", name, err)
		}
	}
	return nil
}

// declaresNothing reports whether the bundle carries neither a version nor a
// single item of any kind — see ParseBundle for why that specific predicate.
func (b *Bundle) declaresNothing() bool {
	return b.Version == "" &&
		len(b.Fragments) == 0 && len(b.Commands) == 0 && len(b.Skills) == 0 &&
		len(b.MCP) == 0 && len(b.Profiles) == 0 && !b.Hooks.HasAny()
}

// ValidateBundleName rejects bundle names that would escape the bundles
// directory when joined to a base path. This is the single chokepoint for
// path-traversal defense — every caller that builds a filesystem path from a
// user/AI-supplied bundle name MUST run names through this first.
//
// Rejected:
//   - Empty names.
//   - Names containing null bytes (would truncate the path on some C APIs).
//   - Names that resolve to a path starting with ".." after filepath.Clean
//     (e.g. "../etc/passwd", "foo/../../escape", "../"+anything).
//   - Absolute paths ("/etc/passwd", "C:\\Windows\\…").
//
// Accepted:
//   - Simple names: "my-bundle".
//   - Slash-separated names: "personal/foo", "alice/go-tools". These are
//     used to place bundles under a remote subdirectory; filepath.Join keeps
//     them under the bundles root.
//   - Names that contain ".." in the middle without escaping after Clean
//     (e.g. "foo/../bar" cleans to "bar" — safe but probably an AI bug).
//
// Threat model: MCP tools accept names from AI clients, so an untrusted name
// could request ".." segments. Without this check, filepath.Join silently
// resolves "../../../tmp/evil" into a path outside the bundles root and
// Bundle.Save would write to it. Empirically verified during the bundle-MCP
// review on feat/bundle-mcp-tools.
//
// Not covered by this function: symlinks already on disk inside the bundles
// tree. ValidateBundleName only inspects the string; it doesn't lstat path
// components. The operations layer pairs this with a requireSafeBundlePath
// walk that rejects symlinked directory components and symlinked bundle
// files — both defenses are needed.
func ValidateBundleName(name string) error {
	if name == "" {
		return fmt.Errorf("empty bundle name")
	}

	// Check for null bytes first (before any path operations)
	if strings.ContainsAny(name, "\x00") {
		return fmt.Errorf("invalid bundle name: null bytes not allowed")
	}

	// Normalize path first
	cleaned := filepath.Clean(name)

	// Check for traversal after cleaning (catches "....", "foo/../bar", etc.)
	if strings.HasPrefix(cleaned, "..") || filepath.IsAbs(cleaned) {
		return fmt.Errorf("invalid bundle name: path traversal not allowed")
	}

	return nil
}

// DirectoryFormManifest is the file a bundle's envelope lives in:
// "<name>/bundle.yaml".
//
// The name itself belongs to internal/core/paths, the declarative source of truth
// for on-disk layout, and remote.BundleManifestName names the same constant.
// bundles imports remote, so remote could never import this back; a package
// BELOW both is the only place one spelling can serve both sides.
const DirectoryFormManifest = paths.BundleManifestName

// ExtractBundleName derives a bundle's name from the path of its envelope: the
// name of the directory holding it. Exported so other packages addressing a bundle FILE as a
// ident.Ref{IsLocal:true} item (e.g. operations.DistillBundleFile's
// re-distill invalidation check) key it identically to how the loader itself
// names a bundle — one definition, not two that can drift apart.
func ExtractBundleName(path string) string {
	return filepath.Base(filepath.Dir(path))
}
