// Package bundles provides types and utilities for ctxloom bundles.
// Bundles are the primary content unit that group related fragments,
// prompts, MCP server configurations, and profiles with a single version.
package bundles

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
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
	// Commands (user-invoked slash templates). The `skills:` key was freed for
	// this meaning by the skill->command rename (Part A of the skill/command
	// split); see detectLegacySkillsKey for the migration guard that keeps a
	// leftover legacy (command-shaped) entry from being silently misread.
	Skills map[string]BundleSkill `yaml:"skills,omitempty"`

	// Profiles shipped with this bundle, keyed by name. A profile is an
	// ungated, COMPOUND item — it composes leaves (fragments/commands/mcp/hooks/
	// llm/parents/variables) into a runnable context unit — so a bundle that
	// ships fragments can also ship the profiles that compose them, as one unit.
	// Addressed by "<bundle>#profiles/<name>" (remote.ProfileSelector) and seeded
	// into the shared profile loader so a bundle profile resolves/runs exactly
	// like a top-level or local profile (config bundle-profile seed). The profile
	// DEFINITION is never trust-gated (no trust.ItemKind for profiles, never
	// baselined); its constituent fragments/commands still gate at content
	// assembly and any mcp/hooks it pulls in still gate at the exec choke.
	Profiles map[string]BundleProfile `yaml:"profiles,omitempty"`

	// Hooks shipped with this bundle (e.g. PostFileEdit plan-stamping).
	// Hooks land in backend settings via ApplyHooks → ResolveBundleHooks.
	// Bundle-shipped hooks are subject to the same review gate as bundle
	// fragments/commands/MCP: a remote-sourced bundle whose SHA changed must
	// be acknowledged before its hooks fire (see docs/bundle-review-plan.md).
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
	// (cloned) source, a bundle embedded in the binary (trust.BuiltinRef), a
	// companion loadout (trust.CompanionRef), and the path-relative resolution
	// name for a bundle in the project's own tree (trust.LocalRef) — the
	// last of those is what lets project content auto-trust.
	//
	// newRead stamps the resolution ref here whenever a reader left it empty,
	// so it is never empty on a read a reader emitted. That backstop is a
	// SECURITY property, not tidiness: Bundle.Name is declared in the bundle's
	// own YAML (`name:`), so falling back to it would let the content being
	// judged choose its own trust key — a project bundle declaring
	// `name: builtin:isolation` would claim the builtin's trust identity, and
	// a bundle that renamed itself would move off its own recorded decisions.
	// The declared name is CONTENT: covered by the signature and by review, and
	// therefore never an input to the decision that establishes that trust.
	//
	// It is a trust.BundleRef, not a string: the field used to carry BOTH a
	// string rendering and this structured counterpart, set by the same call
	// sites through the matching class minter — so the two could never
	// independently disagree on what a bundle's source IS, only on how it was
	// spelled. Collapsing to one typed field removes the spelling question
	// entirely rather than keeping two representations in sync.
	//
	// Every call site that sets sourceRef does so through a class minter —
	// localFSReader (builtin and project), newRead's local fallback,
	// companionReader, repoFSReader (single-file and tree form), and
	// loader_version.go's bundleAtVersion for a version-pinned read. The zero
	// BundleRef is therefore a REACHABLE, meaningful value — a mint that
	// failed (an unfoldable repo-URL spelling — see Ref.AsBundleRef's doc) —
	// not merely "unset"; BundleRead.SourceRef reports it as-is rather than
	// guessing, and a producer minting an item ref from it must degrade to a
	// stable, well-formed address rather than withhold silently (see
	// operations.CountersignRef's identical fallback).
	//
	// Because the zero value is reachable and meaningful, "has a reader
	// already stamped this" cannot be read off sourceRef itself — a failed
	// mint and an untouched field are the same trust.BundleRef{}. sourceRefSet
	// is that separate sentinel: newRead's local fallback (the only caller
	// that ever asks) checks it, not sourceRef's value, so an UNMINTABLE
	// canonical ref from repoFSReader stays unaddressable rather than being
	// silently re-stamped as a local bundle of that name.
	sourceRef    trust.BundleRef `yaml:"-"`
	sourceRefSet bool            `yaml:"-"`

	// signer is the VERIFIED publisher identity of this bundle's file bytes: the
	// principal of the allowed_signers entry whose key made a valid publish
	// signature over exactly those bytes (signing.VerifyPublisher), or the
	// synthetic "builtin:ctxloom" for a bundle compiled into this binary. Empty
	// means UNSIGNED — no signature, or one by a key this machine does not trust
	// to publish — which is legal, ordinary, and takes the review path.
	//
	// It is unexported and yaml:"-" ON PURPOSE, and that is a security property,
	// not a style choice. A bundle file cannot set its own signer: writing
	// `signer: releases@ctxloom.dev` into a YAML document does exactly nothing
	// (TestParseBundle_YAMLCannotForgeSigner). The only way this field becomes
	// non-empty is StampSigner, called by a load path that has already VERIFIED a
	// signature against the trust root. Anyone can write a string into a file;
	// nobody can forge a signature. This is implementer trap #3.
	signer string `yaml:"-"`

	// untrustedSignerFingerprint is the SHA256 fingerprint of the key that made
	// a publish signature over this bundle's bytes WHEN THIS MACHINE DOES NOT
	// TRUST THAT KEY — the case signer above cannot describe, because
	// signing.VerifyPublisher deliberately reports "no signature" and "signed by
	// a key you do not trust" as the same quiet "". It is empty for a genuinely
	// unsigned bundle and empty for a verified one (signer carries the identity
	// there), so the pair (signer, untrustedSignerFingerprint) spells exactly
	// three states and no more.
	//
	// DISPLAY ONLY, and never an identity: see
	// signing.SignatureKeyFingerprint's doc for why a key named by the blob
	// that carries it is a claim, not a fact. Nothing may gate exposure on
	// this, key a trust record on it, or write it into a trust store; its one
	// consumer is the pending-review listing, which prints it beside the words
	// saying the key is NOT trusted, for a human to compare against what the
	// publisher told them out of band.
	//
	// Unexported and yaml:"-" for the same reason signer is: a bundle file
	// must not be able to write its own answer here
	// (TestParseBundle_YAMLCannotForgeUntrustedSignerFingerprint).
	untrustedSignerFingerprint string `yaml:"-"`
}

// Signer returns the bundle's verified publisher identity, or "" when the bundle
// is unsigned (see the signer field). A non-empty value means: a key trusted by
// THIS machine for the publish namespace made a signature over exactly this
// bundle's file bytes, and that signature verified. It is never an unverified
// claim, and it never comes from the bundle's own content.
func (b *Bundle) Signer() string {
	if b == nil {
		return ""
	}
	return b.signer
}

// StampSigner records the verified publisher identity for this bundle. Call it
// ONLY from a load path that has actually verified a signature against the trust
// root (or that is stamping the synthetic builtin identity). Stamping an
// unverified string here would forge a trusted publisher, which is the whole
// attack this design exists to prevent.
//
// Forgetting to call it is fail-SAFE by construction: the bundle stays unsigned,
// and unsigned content is withheld until a human reviews it. The failure mode of
// forgetting is "more review", never "more exposure".
func (b *Bundle) StampSigner(signer string) {
	if b == nil {
		return
	}
	b.signer = signer
}

// UntrustedSignerFingerprint returns the display-only fingerprint of the key
// that signed this bundle's bytes when that key is NOT trusted to publish here,
// or "" when the bundle is unsigned or verified (see the field's doc).
//
// A non-empty value means strictly less than it looks like: a signature exists,
// it names a key, and this machine does not trust that key. It is not a
// publisher, not an endorsement, and not usable as an input to any decision —
// only as a string for a human to compare out of band.
func (b *Bundle) UntrustedSignerFingerprint() string {
	if b == nil {
		return ""
	}
	return b.untrustedSignerFingerprint
}

// StampUntrustedSignerFingerprint records that fingerprint. Call it ONLY from a
// load path that has already asked the trust root and been told this key is not
// trusted — i.e. alongside StampSigner(""), never instead of a verification.
//
// Unlike StampSigner, getting this WRONG cannot grant exposure: nothing reads
// it but the review listing's wording. Getting it wrong can only mislead a
// human, which is why the listing that prints it never presents it as an
// identity.
func (b *Bundle) StampUntrustedSignerFingerprint(fingerprint string) {
	if b == nil {
		return
	}
	b.untrustedSignerFingerprint = fingerprint
}

// contentSourceRef returns the bundle's honest source ref for content trust
// gating: the canonical ref of a seeded (cloned) bundle, the BuiltinRef of a
// bundle embedded in the binary, the CompanionRef of a companion loadout, or
// the LocalRef of a project (fs) bundle. Locality/builtin-ness flows from this
// into the trust cascade, so a clone's TEXT gates like its executables, a
// builtin's TEXT carries the SAME identity whether it was selected by ref
// through the loader or injected unconditionally (a builtin read through
// localFSReader once keyed as LOCAL, a different trust identity than the
// builtin ref injection uses for the identical item — a rejection via one
// route did not withhold the other; see crispy-scoop), and a project bundle's
// bare token keys IsLocal and auto-trusts. "Text to an LLM is executable."
//
// It reads sourceRef and NOTHING ELSE. In particular it must never fall back
// to Bundle.Name: Name is DECLARED in the bundle's own YAML, so a fallback
// would make the content being judged an input to its own trust key — a
// project bundle declaring `name: builtin:isolation` would key as the builtin
// and inherit its grants. newRead stamps the location-derived resolution ref
// into sourceRef for every read a reader emits, so there is nothing for a
// fallback to do but reopen that hole (outdated-recoil).
//
// That last sentence is measured, not assumed, and it is why NO test can be
// written that dies to restoring the fallback here on its own: with newRead
// stamping, the fallback is unreachable. Its removal is trap removal — the
// stamp is the load-bearing half, and the tests that die are the ones that die
// when the stamp goes (internal/operations/declared_name_trust_test.go).
func (b *Bundle) contentSourceRef() trust.BundleRef {
	if b == nil {
		return trust.BundleRef{}
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
	Matcher string `yaml:"matcher,omitempty"`
	Command string `yaml:"command,omitempty"`
	Type    string `yaml:"type,omitempty"`
	Prompt  string `yaml:"prompt,omitempty"`
	Timeout int    `yaml:"timeout,omitempty"`
	Async   bool   `yaml:"async,omitempty"`
	// PreToolFallback (session_start only): the hook is idempotent and may
	// fire on PreToolUse instead on an agent without a session-start event.
	// See wire.Hook.PreToolFallback.
	PreToolFallback bool `yaml:"pre_tool_fallback,omitempty"`

	// Order sequences this hook against its siblings WITHIN its event, sparsely
	// (see wire.HookOrderStep). It does NOT sequence against other bundles:
	// across bundles hooks still merge by pure APPEND, and the merge sequence is
	// the bundles' order, not any hook's. A bundle says where ITS hooks go
	// relative to each other and nothing more.
	//
	// A POINTER, because "declared 0" and "declared nothing" resolve differently
	// (wire.HookOrderLess sorts an undeclared hook LAST) and a plain int could
	// not tell them apart. It is also why this field is invisible to
	// ContentPayload: the executable preimage names its fields explicitly, so
	// adding order here changes no hook's content hash and stales no approval —
	// order is scheduling, not behaviour.
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
	PreShell     []BundleHook `yaml:"pre_shell,omitempty"`
	PostFileEdit []BundleHook `yaml:"post_file_edit,omitempty"`
}

// HasAny reports whether the bundle ships any hooks. Used by the loader to
// skip the merge cost for hookless bundles.
func (h BundleHooks) HasAny() bool {
	return len(h.PreTool)+len(h.PostTool)+len(h.SessionStart)+len(h.SessionEnd)+len(h.PreShell)+len(h.PostFileEdit) > 0
}

// Hook event names. They double as the stable event component of a bundle
// hook's trust identity ("<bundle>#hooks/<event>/<index>") and as the canonical
// iteration order below, and match the BundleHooks YAML field tags.
const (
	HookEventPreTool      = "pre_tool"
	HookEventPostTool     = "post_tool"
	HookEventSessionStart = "session_start"
	HookEventSessionEnd   = "session_end"
	HookEventPreShell     = "pre_shell"
	HookEventPostFileEdit = "post_file_edit"
)

// hookEventOrder is the canonical event order for hook identity + enumeration.
// Entries() and the trust gate both walk it so a baselined hook's ref matches
// the one the gate evaluates.
var hookEventOrder = []string{
	HookEventPreTool, HookEventPostTool, HookEventSessionStart,
	HookEventSessionEnd, HookEventPreShell, HookEventPostFileEdit,
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
	}
	return nil
}

// HookEntry is one bundle hook paired with its stable trust identity: the event
// it fires on and its index within that event's list. Bundle hooks are an
// ordered list with no author-given name, so (event, index) is the addressable
// identity the per-item trust gate keys on (trust rework, TR5).
type HookEntry struct {
	Event string
	Index int
	Hook  BundleHook
}

// ID returns the stable per-hook identity "<event>/<index>", the <id> in the
// trust ref "<bundle>#hooks/<id>". The index is the hook's authored position in
// its event list.
func (e HookEntry) ID() string {
	return e.Event + "/" + strconv.Itoa(e.Index)
}

// Entries returns every bundle hook with its identity, in canonical event order
// then authored index. The trust gate (config.extractHooksFromBundle) and the
// migration baseline both enumerate hooks through this scheme so the refs agree.
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
// reports ok=false for a malformed id or an out-of-range index — fail-closed: an
// unresolvable hook hashes to nothing and gates.
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

// BundleMCP defines an MCP server within a bundle.
type BundleMCP struct {
	Command      string            `yaml:"command"`
	Args         []string          `yaml:"args,omitempty"`
	Env          map[string]string `yaml:"env,omitempty"`
	Notes        string            `yaml:"notes,omitempty"`        // Human-readable notes, not sent to AI
	Installation string            `yaml:"installation,omitempty"` // Setup/installation instructions, not sent to AI (surfaced to the user only, e.g. review/pull/list output)
	ContentHash  string            `yaml:"content_hash,omitempty"` // Hash of the executable surface (Command+Args+Env+Installation)
}

// BundleFragment defines a fragment within a bundle.
type BundleFragment struct {
	Tags         []string `yaml:"tags,omitempty"`         // Additional tags (merged with bundle tags)
	Notes        string   `yaml:"notes,omitempty"`        // Human-readable notes, not sent to AI
	Installation string   `yaml:"installation,omitempty"` // Setup/installation instructions, not sent to AI (surfaced to the user only, e.g. review/pull/list output)
	Content      string   `yaml:"content"`
	ContentHash  string   `yaml:"content_hash,omitempty"`
	Distilled    string   `yaml:"distilled,omitempty"`
	DistilledBy  string   `yaml:"distilled_by,omitempty"`
	NoDistill    bool     `yaml:"no_distill,omitempty"`
}

// BundleCommand defines a slash command within a bundle.
type BundleCommand struct {
	Description  string     `yaml:"description,omitempty"`
	Tags         []string   `yaml:"tags,omitempty"`
	Notes        string     `yaml:"notes,omitempty"`        // Human-readable notes, not sent to AI
	Installation string     `yaml:"installation,omitempty"` // Setup/installation instructions, not sent to AI (surfaced to the user only, e.g. review/pull/list output)
	Content      string     `yaml:"content"`
	ContentHash  string     `yaml:"content_hash,omitempty"`
	Distilled    string     `yaml:"distilled,omitempty"`
	DistilledBy  string     `yaml:"distilled_by,omitempty"`
	NoDistill    bool       `yaml:"no_distill,omitempty"`
	LLM          LLMExports `yaml:"llm,omitempty"` // Per-LLM export settings (e.g. claude-code slash-command config)
}

// BundleSkill defines an Agent Skill package within a bundle: a directory
// (default "skills/<name>", overridable via Path) containing a required
// SKILL.md — YAML frontmatter name+description, instructions body — plus
// arbitrary sibling files (scripts/, assets, references). Unlike
// BundleFragment/BundleCommand, a skill carries NO inline `content:` and NO
// distillation: SKILL.md's description IS the progressive-disclosure
// mechanism a model reads before deciding to load the rest of the package, and
// distilling a model-facing capability description would defeat that. This
// shape difference (no `content:`) is exactly what lets detectLegacySkillsKey
// tell a real skill entry apart from a legacy command entry still sitting
// under the reserved `skills:` key.
//
// Files is the GENERATED per-file manifest (populated by `ctxloom skill sync`/
// `sign`, not hand-authored): relative path -> {sha256, mode}. It is what B2's
// signing covers and B1b's archive codec packs; ParseSkillPackage computes the
// authoritative version of it fresh from the source tree.
type BundleSkill struct {
	Path  string                   `yaml:"path,omitempty"`  // dir relative to bundle dir; default "skills/<name>"
	Tags  []string                 `yaml:"tags,omitempty"`  // Additional tags (merged with bundle tags)
	Notes string                   `yaml:"notes,omitempty"` // Human-readable notes, not sent to AI
	Files map[string]SkillFileMeta `yaml:"files,omitempty"` // GENERATED per-file manifest
	LLM   SkillLLMExports          `yaml:"llm,omitempty"`   // Per-engine enablement only (name/description live in SKILL.md)
}

// SkillFileMeta is one manifest entry as recorded in bundle.yaml: a file's
// content hash and POSIX permission mode. The exec bit in Mode is
// load-bearing for scripts/ entries — it must survive tree -> archive ->
// extract -> materialize (see the skill/command split plan §3.1).
type SkillFileMeta struct {
	SHA256 string `yaml:"sha256"`
	Mode   string `yaml:"mode"` // e.g. "0644", "0755" (octal POSIX perm bits, no "0o" prefix)
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

// ContentForm identifies which materialization of an item's content was hashed
// or served: the raw authored bytes, or the distilled rewrite. Trust grants
// (trust rework, TR0+) bind {effective-content-hash, form} together so a grant
// blessing the raw form can never validate a distilled exposure, and vice-versa.
type ContentForm string

const (
	FormRaw       ContentForm = "raw"
	FormDistilled ContentForm = "distilled"
)

// hashContent is the single sha256 helper every content-hash computation in this
// package routes through. It returns the canonical "sha256:<hex>" digest.
func hashContent(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// HashPayload is the exported name for the one content-hash primitive: it hashes
// a payload produced by a ContentPayload builder, and it is the ONLY way any
// caller outside this package may turn item bytes into a content hash.
//
// The hash is an INDEX, never an authority. It answers "which recorded decision
// might be about these bytes"; it never answers "may these bytes be exposed" —
// that is the decision function's job, and once approvals are countersignatures
// it will be a signature verification. A hash match is a candidate, not a
// verdict (spec §9.3, trap #2).
func HashPayload(payload []byte) string {
	return hashContent(payload)
}

// resolveEffective is the one shared compute primitive for the distillable item
// shape. Fragments and prompts carry the same Content/Distilled/NoDistill fields,
// so this picks the bytes to expose AND reports their form from the same
// predicate — guaranteeing a hash taken over the result covers exactly the served
// bytes, with no raw fallback once distilled is chosen.
func resolveEffective(preferDistilled bool, content, distilled string, noDistill bool) (string, ContentForm) {
	if preferDistilled && distilled != "" && !noDistill {
		return distilled, FormDistilled
	}
	return content, FormRaw
}

// ContentForms is EVERY form of a distillable item the store holds: the
// authored body, its distillation when one exists, and the author's refusal of
// the distilled form. It is what a READ reports — the read stage has no form
// preference and picks nothing (docs/design/engine-delivery-seam.design.md,
// "ALL processing lives in the middle"); the process stage looks at this and
// decides.
//
// The absence of a distilled form is itself information the caller can see
// (Distilled == ""), which is why this is a struct of forms rather than one
// pre-picked body: "there is no distilled form" and "the distilled form was not
// preferred" are different facts about an item and only the first is a read
// fact.
type ContentForms struct {
	Raw       string // the authored body; always present
	Distilled string // the distillation, or "" when the store holds none
	NoDistill bool   // the author forbade serving the distilled form
}

// Select picks the bytes to serve for a form preference and reports the LAYOUT
// form they were selected in. It routes through resolveEffective — the SAME
// single compute primitive ContentPayload and EffectiveContentHash use — so
// what the process stage selects and what the trust preimage covers cannot
// drift. It only ever PREFERS: an item with no distilled form, or one that
// forbids distillation, still serves raw.
func (f ContentForms) Select(preferDistilled bool) ([]byte, ContentForm) {
	content, form := resolveEffective(preferDistilled, f.Raw, f.Distilled, f.NoDistill)
	return []byte(content), form
}

// Forms reports every form of this fragment the store holds.
func (f *BundleFragment) Forms() ContentForms {
	return ContentForms{Raw: f.Content, Distilled: f.Distilled, NoDistill: f.NoDistill}
}

// Forms reports every form of this command the store holds.
func (p *BundleCommand) Forms() ContentForms {
	return ContentForms{Raw: p.Content, Distilled: p.Distilled, NoDistill: p.NoDistill}
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
// feeds the recorded content_hash that drives re-distillation (NeedsDistill); the
// trust gate uses EffectiveContentHash instead.
func (f *BundleFragment) ComputeContentHash() string {
	return hashContent([]byte(f.Content))
}

// NeedsDistill returns true if this fragment needs distillation.
func (f *BundleFragment) NeedsDistill() bool {
	return staleDistill(f.NoDistill, f.Distilled, f.ContentHash, f.Content)
}

// EffectiveContent returns distilled content if available and preferred.
// Falls back to original content if distilled is empty or NoDistill is true.
func (f *BundleFragment) EffectiveContent(preferDistilled bool) string {
	content, _ := resolveEffective(preferDistilled, f.Content, f.Distilled, f.NoDistill)
	return content
}

// ContentPayload returns the exact bytes EffectiveContent(preferDistilled)
// would serve, and the form they were exposed in. This is the SINGLE preimage
// builder for a fragment: EffectiveContentHash below hashes exactly this
// function's output, and nothing else in the codebase — including a future
// countersignature (signature envelope spec §3.2) — is permitted to define
// "the bytes of this fragment" any other way. Two definitions is the bug.
func (f *BundleFragment) ContentPayload(preferDistilled bool) ([]byte, ContentForm) {
	content, form := resolveEffective(preferDistilled, f.Content, f.Distilled, f.NoDistill)
	return []byte(content), form
}

// EffectiveContentHash hashes EXACTLY the bytes EffectiveContent(preferDistilled)
// returns, and reports their form. This is the hash the per-item trust gate binds
// to (trust rework, TR0): it covers the bytes actually exposed to the agent —
// never a raw fallback once distilled is served, and never the author-supplied
// ContentHash field. The form is provenance so a raw-form grant cannot validate a
// distilled exposure.
func (f *BundleFragment) EffectiveContentHash(preferDistilled bool) (string, ContentForm) {
	payload, form := f.ContentPayload(preferDistilled)
	return hashContent(payload), form
}

// ComputeContentHash computes the SHA256 hash of the raw authored content. This
// feeds the recorded content_hash that drives re-distillation (NeedsDistill); the
// trust gate uses EffectiveContentHash instead.
func (p *BundleCommand) ComputeContentHash() string {
	return hashContent([]byte(p.Content))
}

// NeedsDistill returns true if this command needs distillation.
func (p *BundleCommand) NeedsDistill() bool {
	return staleDistill(p.NoDistill, p.Distilled, p.ContentHash, p.Content)
}

// EffectiveContent returns distilled content if available and preferred.
// Falls back to original content if distilled is empty or NoDistill is true.
func (p *BundleCommand) EffectiveContent(preferDistilled bool) string {
	content, _ := resolveEffective(preferDistilled, p.Content, p.Distilled, p.NoDistill)
	return content
}

// ContentPayload returns the exact bytes EffectiveContent(preferDistilled)
// would serve, and the form they were exposed in. See
// BundleFragment.ContentPayload — same single-preimage-builder contract.
func (p *BundleCommand) ContentPayload(preferDistilled bool) ([]byte, ContentForm) {
	content, form := resolveEffective(preferDistilled, p.Content, p.Distilled, p.NoDistill)
	return []byte(content), form
}

// EffectiveContentHash hashes EXACTLY the bytes EffectiveContent(preferDistilled)
// returns, and reports their form. See BundleFragment.EffectiveContentHash — same
// contract for the per-item trust gate (trust rework, TR0).
func (p *BundleCommand) EffectiveContentHash(preferDistilled bool) (string, ContentForm) {
	payload, form := p.ContentPayload(preferDistilled)
	return hashContent(payload), form
}

// ToManifest converts a BundleSkill's authored `files:` map (bundle.yaml's
// GENERATED per-file manifest — see BundleSkill.Files) into the canonical
// SkillManifest shape (a sorted slice) that ParseSkillPackage computes fresh
// from a skill's source tree. Both representations hold the same entries,
// keyed the same way; this is the one conversion between the two so nothing
// else in the codebase re-derives it.
func (s *BundleSkill) ToManifest() SkillManifest {
	m := make(SkillManifest, 0, len(s.Files))
	for path, meta := range s.Files {
		m = append(m, SkillManifestEntry{Path: path, SHA256: meta.SHA256, Mode: meta.Mode})
	}
	return m.sorted()
}

// skillContentPayload is the canonical encoding BundleSkill.ContentPayload
// shares — see mcpContentPayload below for the field-order/versioning
// contract this mirrors exactly. A skill has no raw bytes to sign the way a
// fragment/command does (it is a directory tree, not a blob); its manifest —
// every file's path, sha256, and mode, SKILL.md included — already covers the
// whole package (skill/command split plan §3.1), so the manifest IS this
// preimage. Editing any file in the tree, including a scripts/ script,
// changes the manifest and therefore this payload, re-triggering review/sign.
//
// The Manifest here is always the EFFECTIVE manifest (see
// BundleSkill.EffectiveManifest) — authored if the skill has been synced,
// derived from the source tree if not. It is never empty: an empty manifest
// would make every unsynced skill share one preimage, which is exactly the
// trust hole this design closes.
type skillContentPayload struct {
	Preimage string        `json:"preimage"`
	Manifest SkillManifest `json:"manifest"`
}

// ContentPayload returns the canonical JSON encoding of the skill's manifest —
// the SINGLE preimage builder for a skill, exactly as mcpContentPayload/
// hookContentPayload are for MCP servers and hooks. This is a canonicalization
// (a skill's "content" is structured per-file metadata, not raw bytes), which
// is why it carries the same versioned first field those two use: any change
// to this field set requires bumping signing.ExecPreimageContract, turning a
// silent mass re-review of every skill approval into an announced one.
// EffectiveManifest returns the manifest that BOTH this skill's trust
// preimage covers AND the loader verifies the on-disk tree against. It is the
// single answer to "which files, with which bytes and modes, is this skill?"
//
// Two sources, one meaning:
//
//   - An entry with an authored `files:` manifest (a skill that has been
//     through `ctxloom skill sync`/`sign`) uses that manifest verbatim. This
//     path never touches the filesystem, so a synced skill's preimage is
//     byte-identical to what it has always been and no already-recorded trust
//     decision is disturbed.
//   - An entry with NO authored manifest — the shape `ctxloom skill create`
//     leaves behind, before a sync has run — derives the manifest by parsing
//     the skill's real source tree. It is NOT a constant.
//
// The manifest-less case previously returned an empty manifest, which made
// every unsynced skill in existence share one preimage
// ({"preimage":"ctxloom-exec/1","manifest":[]}) and therefore one trust hash:
// an approval bound nothing, and arbitrary content could be swapped in at an
// approved ref without re-review. Deriving from the tree is what makes the
// approval mean "I approved THESE bytes".
//
// It fails CLOSED. A tree that cannot be resolved or parsed returns an error
// rather than degrading to an empty manifest — degrading is precisely how the
// original defect behaved, and a caller that cannot compute a preimage must
// withhold the skill, never expose it under a placeholder hash.
func (s *BundleSkill) EffectiveManifest(fsys afero.Fs, bundleDir, skillName string) (SkillManifest, error) {
	if len(s.Files) > 0 {
		return s.ToManifest(), nil
	}
	if fsys == nil {
		return nil, fmt.Errorf("skill %q: no authored manifest and no filesystem to derive one from", skillName)
	}
	dir, err := ResolveSkillDir(bundleDir, skillName, *s)
	if err != nil {
		return nil, err
	}
	pkg, err := ParseSkillPackage(fsys, dir, 0)
	if err != nil {
		return nil, fmt.Errorf("skill %q: deriving content manifest from %s: %w", skillName, dir, err)
	}
	return pkg.Manifest, nil
}

// unresolvableSkillDir stands in for "this bundle has no directory, and this
// caller provably does not need one". It is deliberately NOT "" and not ".":
// both of those resolve through ResolveSkillDir onto the PROCESS WORKING
// DIRECTORY, which is exactly the hazard this guards against. A caller that uses
// this value anyway gets a not-found error naming an obviously synthetic path,
// never somebody's cwd.
const unresolvableSkillDir = "<no-bundle-directory>"

// SkillPreimageDir returns the directory a skill's PREIMAGE computation must
// resolve its tree against, for callers that only need EffectiveManifest /
// ContentPayload (review, trust, review snapshots).
//
// The rule is not "always require a directory", because that is not true:
//   - A skill with an AUTHORED manifest (len(Files) > 0) is hashed from that
//     manifest and EffectiveManifest never touches the filesystem. A bundle
//     with no directory — a companion or remote seed — is perfectly able to
//     carry such a skill, and refusing it would break a legitimate case.
//   - A manifest-LESS skill derives its preimage by parsing the real tree, and
//     for that the directory is load-bearing. A bundle without one must fail
//     loudly rather than derive from whatever sits in the process working
//     directory — which, for these callers, means hashing cwd files
//     into a TRUST GRANT.
//
// Callers that genuinely always need the tree (the loader's skillContent,
// which parses and tamper-verifies it; skill create/sync/export/import) must
// use FSDir directly instead — for them a missing directory is always fatal.
func (b *Bundle) SkillPreimageDir(entry BundleSkill) (string, error) {
	dir, err := b.FSDir()
	if err != nil && len(entry.Files) > 0 {
		return unresolvableSkillDir, nil
	}
	return dir, err
}

// skillPayloadFor encodes a resolved manifest into the canonical skill
// preimage. Split out so a caller that already holds the effective manifest
// (the loader, which also verifies the tree against it) builds the payload
// without re-walking the tree — one parse, one manifest, one preimage.
func skillPayloadFor(m SkillManifest) ([]byte, error) {
	return json.Marshal(skillContentPayload{
		Preimage: signing.ExecPreimageContract,
		Manifest: m,
	})
}

func (s *BundleSkill) ContentPayload(fsys afero.Fs, bundleDir, skillName string) ([]byte, error) {
	manifest, err := s.EffectiveManifest(fsys, bundleDir, skillName)
	if err != nil {
		return nil, err
	}
	return skillPayloadFor(manifest)
}

// ComputeContentHash hashes a skill's canonical manifest payload. This is the
// hash a skill trust grant binds to (trust.KindSkill); like MCP/hooks, a skill
// has no distilled form, so there is one hash.
func (s *BundleSkill) ComputeContentHash(fsys afero.Fs, bundleDir, skillName string) string {
	data, err := s.ContentPayload(fsys, bundleDir, skillName)
	if err != nil {
		// REACHABLE since the manifest-less preimage is derived from the tree
		// (EffectiveManifest): an unreadable/unparseable package lands here.
		// The digest must therefore be DISTINCT per skill and per failure — a
		// single shared error digest would be the very defect this fix closes
		// (one constant standing in for many different skills). It can never
		// collide with a real payload hash: no valid payload has this prefix.
		//
		// Callers that gate MUST use ContentPayload and withhold on its error
		// rather than hashing through here; this exists so a hash is always a
		// hash, not so a failure can be exposed.
		return hashContent(fmt.Appendf(nil, "ctxloom:skill-content-hash-error:%s:%s:%v", bundleDir, skillName, err))
	}
	return hashContent(data)
}

// SkillNames returns sorted skill names.
func (b *Bundle) SkillNames() []string {
	return slices.Sorted(maps.Keys(b.Skills))
}

// mcpContentPayload is the canonical encoding shared by ContentPayload; it
// is factored out so the "unreachable JSON error" fallback below can still
// report a stable digest through hashContent without duplicating the struct.
//
// Preimage MUST stay the first field. Go's encoding/json emits struct fields
// in declaration order, so field order here IS the byte order of the preimage,
// and the leading version carrier is part of the public contract (spec §3.3.2
// — "the canonical struct gains a `"preimage":"ctxloom-exec/1"` first field").
// ANY change to the field set below — adding one, removing one, renaming a tag,
// reordering — changes the preimage and therefore invalidates every existing
// MCP approval. That is the moment to bump signing.ExecPreimageContract, which
// is what turns a silent mass re-review into an announced one.
type mcpContentPayload struct {
	Preimage     string            `json:"preimage"`
	Command      string            `json:"command"`
	Args         []string          `json:"args"`
	Env          map[string]string `json:"env"`
	Installation string            `json:"installation"`
}

// ContentPayload returns the canonical JSON encoding of the MCP server's
// executable surface — the ctxloom-exec contract version, then Command, Args
// (order significant), Env (key-sorted), and Installation. Notes are excluded
// (human-only, never executed). encoding/json provides the determinism: struct
// fields emit in declaration order and map keys are sorted, so reordering Env
// yields identical bytes while reordering Args (a slice) does not.
//
// This is the SINGLE preimage builder for an MCP server: ComputeContentHash
// below hashes exactly this function's output, and a countersignature
// (signature envelope spec §3.2/§3.3) covers exactly it too. Unlike the
// fragment/command preimage, this one IS a canonicalization — an existing,
// already-shipped one (spec §3.3.2) — because an MCP server has no "raw bytes";
// it is structured fields with no other faithful serialization. That is
// precisely why it carries a version: see signing.ExecPreimageContract.
func (m *BundleMCP) ContentPayload() ([]byte, error) {
	canonical := mcpContentPayload{
		Preimage:     signing.ExecPreimageContract,
		Command:      m.Command,
		Args:         m.Args,
		Env:          m.Env,
		Installation: m.Installation,
	}
	return json.Marshal(canonical)
}

// ComputeContentHash hashes a canonical encoding of the MCP server's executable
// surface — Command, Args (order significant), Env (key-sorted), and Installation.
// Notes are excluded (human-only, never executed). encoding/json provides the
// determinism: it sorts map keys, so reordering Env yields an identical hash while
// reordering Args (a slice) does not. This is the hash an MCP trust grant binds to
// (trust rework, TR0); an MCP server has no distilled form, so there is one hash.
func (m *BundleMCP) ComputeContentHash() string {
	data, err := m.ContentPayload()
	if err != nil {
		// Unreachable: the struct holds only strings/[]string/map[string]string,
		// none of which json.Marshal can fail on. Fail closed rather than
		// panic — and to a digest DISTINCT per server/failure, not a shared
		// constant: one constant standing in for many different items is
		// exactly the defect this guards against, and an unreachable branch is
		// a poor place to keep its shape alive.
		return hashContent(fmt.Appendf(nil, "ctxloom:mcp-content-hash-error:%s:%v", m.Command, err))
	}
	return hashContent(data)
}

// hookContentPayload is the canonical encoding shared by ContentPayload.
//
// Preimage MUST stay the first field, for the same reason and under the same
// rule as mcpContentPayload above: declaration order is byte order, the leading
// version carrier is the public contract (spec §3.3.2), and any change to this
// field set invalidates every existing hook approval and therefore requires
// bumping signing.ExecPreimageContract.
type hookContentPayload struct {
	Preimage        string `json:"preimage"`
	Matcher         string `json:"matcher"`
	Type            string `json:"type"`
	Command         string `json:"command"`
	Prompt          string `json:"prompt"`
	PreToolFallback bool   `json:"pre_tool_fallback"`
}

// ContentPayload returns the canonical JSON encoding of the hook's executable
// surface — the ctxloom-exec contract version, then Matcher, Type, Command,
// Prompt, and the PreToolFallback flag: the fields that determine what runs and
// how it fires. Timeout/Async (operational knobs) and the firing event are
// excluded — the event is carried by the hook's id, and excluding it keeps the
// content-hash denylist event-agnostic so the same malicious command is blocked
// wherever it is wired. encoding/json provides the determinism (stable field
// order).
//
// This is the SINGLE preimage builder for a hook: ComputeContentHash below
// hashes exactly this function's output, and a countersignature covers exactly
// it too. Mirrors BundleMCP.ContentPayload — same "already-shipped
// canonicalization, not a new one" contract, and the same versioned first field
// (signing.ExecPreimageContract, spec §3.3.2).
func (h *BundleHook) ContentPayload() ([]byte, error) {
	canonical := hookContentPayload{
		Preimage:        signing.ExecPreimageContract,
		Matcher:         h.Matcher,
		Type:            h.Type,
		Command:         h.Command,
		Prompt:          h.Prompt,
		PreToolFallback: h.PreToolFallback,
	}
	return json.Marshal(canonical)
}

// ComputeContentHash hashes a canonical encoding of the hook's executable
// surface. This is the hash a bundle-hook trust grant binds to (trust rework,
// TR5); a hook has no distilled form, so there is one hash. Mirrors
// BundleMCP.ComputeContentHash.
func (h *BundleHook) ComputeContentHash() string {
	data, err := h.ContentPayload()
	if err != nil {
		// Unreachable (only strings + a bool); fail closed to a digest
		// DISTINCT per hook/failure rather than a shared constant, for the
		// reason given in BundleMCP.ComputeContentHash.
		return hashContent(fmt.Appendf(nil, "ctxloom:hook-content-hash-error:%s:%s:%v", h.Matcher, h.Command, err))
	}
	return hashContent(data)
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
func ParseBundle(data []byte) (*Bundle, error) {
	// Migrate older on-disk/remote bundle schemas (e.g. the legacy `prompts:`
	// key → `commands:`) in memory before unmarshal, so old bundles load
	// instead of silently dropping renamed keys. No-op for already-current
	// bundles.
	if upgraded, applied := bundleUpgrades.Run(data); len(applied) > 0 {
		data = upgraded
	}

	// `skills:` is reserved for a future, different item-kind (Agent Skills,
	// SKILL.md packages) that never carries an inline `content:` field. Detect
	// any leftover legacy (command-shaped) entry under `skills:` and fail loud
	// — a permanent rewrite is impossible here (it would corrupt real Agent
	// Skills once that item-kind exists), and a silent unmarshal-drop would be
	// exactly the silent-no-op this codebase treats as a bug.
	if err := detectLegacySkillsKey(data); err != nil {
		return nil, err
	}

	// STRICT: a key the Bundle schema does not model is refused, not dropped.
	// The default unmarshal ignored it, so `hoooks:` or `promts:` loaded
	// clean and shipped a bundle quietly missing whatever its author wrote
	// under that key — this codebase's characteristic bug (exit 0, success
	// message, nothing happened). strictDecodeError names the key, where it
	// sat, and what it probably meant; the FILE comes from the caller's wrap.
	//
	// This runs AFTER bundleUpgrades above, and the order is load-bearing: an
	// older bundle's legacy `prompts:` key is migrated to `commands:` first,
	// so strictness refuses only keys that are wrong TODAY and never keys a
	// past schema generation made legal.
	var bundle Bundle
	if err := yamlx.DecodeStrict(data, &bundle); err != nil {
		return nil, strictDecodeError(err)
	}

	// Initialize maps if nil
	if bundle.Fragments == nil {
		bundle.Fragments = make(map[string]BundleFragment)
	}
	if bundle.Commands == nil {
		bundle.Commands = make(map[string]BundleCommand)
	}
	if bundle.MCP == nil {
		bundle.MCP = make(map[string]BundleMCP)
	}
	if bundle.Profiles == nil {
		bundle.Profiles = make(map[string]BundleProfile)
	}
	if bundle.Skills == nil {
		bundle.Skills = make(map[string]BundleSkill)
	}

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
			"comment-only or `null` file parses as valid YAML but is not a bundle", len(data))
	}

	return &bundle, nil
}

// declaresNothing reports whether the bundle carries neither a version nor a
// single item of any kind — see ParseBundle for why that specific predicate.
func (b *Bundle) declaresNothing() bool {
	return b.Version == "" &&
		len(b.Fragments) == 0 && len(b.Commands) == 0 && len(b.Skills) == 0 &&
		len(b.MCP) == 0 && len(b.Profiles) == 0 && !b.Hooks.HasAny()
}

// detectLegacySkillsKey inspects the raw bundle YAML for a top-level
// `skills:` key and fails loud on any entry still shaped like the legacy
// command item (a scalar `content:` field) rather than letting the default
// YAML unmarshal silently misparse it into a BundleSkill with a stray
// content-shaped map dropped. `skills:` used to be this codebase's name for
// the command item-kind; it now means a real Agent Skill package (BundleSkill:
// Path/Tags/Notes/Files/LLM), which never carries an inline `content:` field —
// that shape difference is the deterministic discriminator this function uses.
//
// An entry under `skills:` that is NOT content-shaped is a genuine new-shape
// skill reference and is left alone here; the normal unmarshal below parses it
// into Bundle.Skills. Only a content-shaped (legacy) entry errors, with a
// precise, actionable message naming the offending entries.
func detectLegacySkillsKey(data []byte) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil // let the normal parse path below surface the real error
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	root := doc.Content[0]

	// No root `name:` is consulted: Bundle.Name is yaml:"-", the schema has no
	// such key, and the caller already knows which document this is (LoadFile
	// wraps this error with the file path).
	var skillsNode *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "skills" {
			skillsNode = root.Content[i+1]
		}
	}
	if skillsNode == nil || skillsNode.Kind != yaml.MappingNode {
		return nil
	}

	var legacyNames []string
	for i := 0; i+1 < len(skillsNode.Content); i += 2 {
		entryName := skillsNode.Content[i].Value
		entryNode := skillsNode.Content[i+1]
		if entryNode.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(entryNode.Content); j += 2 {
			if entryNode.Content[j].Value == "content" {
				legacyNames = append(legacyNames, entryName)
				break
			}
		}
	}

	if len(legacyNames) > 0 {
		return fmt.Errorf(
			"`skills:` now means Agent Skills (SKILL.md packages); "+
				"entr(y/ies) %v are shaped like slash commands (they carry `content:`) — "+
				"rename the `skills:` key to `commands:` (skill→command rename, v0.7.0) or re-init the bundle",
			legacyNames)
	}

	return nil
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

// DirectoryFormManifest is the file a DIRECTORY-form bundle's manifest lives in:
// "<name>/bundle.yaml", as opposed to the single-file "<name>.yaml".
//
// The name is what distinguishes the two shapes everywhere — the loader's search
// order, ExtractBundleName's parent-directory rule, the skills-require-a-
// directory refusal, and the move guard that refuses to strand a directory's
// other files. It was a literal at each of those, which is one spelling per site
// of a fact that has to agree at all of them.
//
// internal/remote states it a SECOND time (remote.BundleManifestName) and must:
// bundles imports remote, so remote cannot import this back. That duplication is
// structural rather than careless, and the two are pinned to each other by the
// fetch/read tests rather than by a shared symbol.
const DirectoryFormManifest = "bundle.yaml"

// ExtractBundleName derives a bundle's name from its file path: the parent
// directory name for a "bundle.yaml" leaf, else the filename without
// extension. Exported so other packages addressing a bundle FILE as a
// trust.Ref{IsLocal:true} item (e.g. operations.DistillBundleFile's
// re-distill invalidation check) key it identically to how the loader itself
// names a bundle — one definition, not two that can drift apart.
func ExtractBundleName(path string) string {
	base := filepath.Base(path)

	// If it's bundle.yaml, use parent directory name
	if base == DirectoryFormManifest {
		return filepath.Base(filepath.Dir(path))
	}

	// Otherwise use filename without extension
	return strings.TrimSuffix(base, filepath.Ext(base))
}
