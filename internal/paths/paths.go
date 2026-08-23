// Package paths provides shared path constants for ctxloom.
package paths

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	harpid "github.com/ctxloom/ctxloom/internal/shared/harp"
)

const (
	// AppDirName is the name of the ctxloom directory.
	AppDirName = ".ctxloom"

	// CacheDir is the subdirectory for REGENERABLE data: pulled remote bundle
	// copies, git clone caches, assembled context files, and the refused-advance
	// record. Every path under it is TierDerived and names the command that
	// rebuilds it (see Layout), so the whole directory may be deleted freely.
	// A gitignored path that NOTHING rebuilds is not cache — it belongs under
	// StateDir; see Tier's doc for why that, and not gitignore status, is the
	// test.
	CacheDir = "cache"

	// ConfigFileName is the name of the config file (without extension).
	ConfigFileName = "config"

	// BundlesDir is the subdirectory for bundles.
	BundlesDir = "bundles"

	// RemotesFileName is the name of the remotes file (without extension).
	RemotesFileName = "remotes"

	// TrustFileName is the "trust" path segment. Despite the name it is NOT a
	// file: no .ctxloom/trust.yaml exists and nothing in this package builds
	// one. Its sole use is as the DIRECTORY segment in TrustObjectsPath
	// (state/trust/objects), the approved-content snapshot store — and in
	// LegacyTrustObjectsPath, the pre-relocation cache/trust/objects the
	// one-time migration reads.
	TrustFileName = "trust"

	// AllowedSignersFileName is the name of the trust-root file: the set of
	// public keys authorized to make signed assertions, in the OpenSSH
	// `allowed_signers` format verbatim (ssh-keygen(1), ALLOWED SIGNERS).
	// It carries no extension because it is not ctxloom's format — it is
	// OpenSSH's, and it must stay hand-editable by anyone who already knows
	// that format (signature-envelope spec §7).
	AllowedSignersFileName = "allowed_signers"

	// DistrustedSignersFileName is the name of the LOCAL embedded-key
	// suppression record (without extension) — see DistrustedSignersPath. A
	// plain one-principal-per-line list, deliberately NOT the OpenSSH
	// allowed_signers format: this store asserts no trust of its own, only a
	// negative record TrustRoot() subtracts from the embedded root.
	DistrustedSignersFileName = "distrusted_signers"

	// ApprovalsDirName is the name of the countersignature store directory
	// (signature-envelope spec §9.2): one armored .sig file per approve/reject
	// countersignature. It replaces trust.yaml as the review-decision record —
	// the signature IS the approval, not a row a plain-file write can forge.
	// Two physical stores share this name, at different roots: the user store
	// (~/.ctxloom/approvals, personal) and the project store (.ctxloom/approvals,
	// committable) — see HomeApprovalsPath / ApprovalsPath.
	ApprovalsDirName = "approvals"

	// CompanionConsentFileName is the name (without extension) of the
	// trust-on-first-use record for EXECUTING a companion binary — see
	// HomeCompanionConsentPath. It is deliberately a PERSONAL-only file with no
	// project/committable twin: approvals answer "may this content be shown to
	// the agent", which a team can legitimately decide once and share, whereas
	// this answers "may ctxloom exec this file on THIS machine", which is a
	// property of the machine's filesystem and cannot be delegated to a repo. A
	// committable form would let a clone arrive carrying pre-approved binaries.
	CompanionConsentFileName = "companion_consent"

	// LockFileName is the name of the lock file (without extension).
	LockFileName = "lock"

	// ProfilesDir is the subdirectory for profiles.
	ProfilesDir = "profiles"

	// AgentsDir is the RETIRED per-agent definition directory. Agent bindings
	// live under the `agents:` key of config.yaml and nowhere else; this
	// constant survives only so config.retiredAgentsDirSignpost can name the
	// location it refuses to read, and it is deliberately absent from
	// Layout() — ctxloom neither writes it nor classifies it.
	AgentsDir = "agents"

	// ContentDir is the subdirectory for project-authored, version-controlled
	// content referenced via the ctxloom:local source. Unlike CacheDir it is
	// committed with the project (NOT gitignored, NOT regeneratable), and unlike
	// remote content it is read from the working copy rather than a clone.
	// This is also the ONE on-disk home for authored/publishable content: a
	// dedicated bundle repo lays it out identically under RepoContentPrefix
	// (.ctxloom/content/<kind>/<name>.yaml) — one layout, not two.
	ContentDir = "content"

	// StateDir is the THIRD tier under .ctxloom, beside ContentDir (committed,
	// TierCommitted) and CacheDir (derived, TierDerived): LOCAL-ONLY state,
	// gitignored, that nothing can reconstruct. Before this tier had a name its
	// files were placed ad hoc — some at the .ctxloom root, one inside cache/,
	// one loose — each looking like it belonged to one of the other two
	// (config-layer-scope design doc, "The .ctxloom classification"). A file
	// under here is a fact about THIS checkout on THIS machine that must never
	// be committed (a clone would arrive carrying somebody else's answer) and
	// that a cache wipe or a `deps pull` cannot regenerate — see Tier's doc
	// for why that distinction, not mere gitignore status, is what earns a path
	// a place in this directory instead of cache/.
	StateDir = "state"

	// SessionHomeDirName is the subdirectory of ONE session's state root
	// (SessionStatePath) that holds the engine config-home INSTANCE for that
	// session — see SessionHomePath. Every engine's leaf hangs off this one
	// directory rather than each engine owning a sibling of it, because the
	// leaves (".codex", "claude", "kiro") are pairwise distinct by
	// construction and one root per session is what makes the instance
	// disposable as a unit.
	SessionHomeDirName = "home"

	// ContextCacheDir is the CacheDir subdirectory holding assembled context
	// files, one per content hash (agent.WriteContextFile). The leaf lived in
	// internal/shared/agent because this package had no helper left for it;
	// it belongs here, with every other .ctxloom segment, so Layout can
	// classify the directory without either side inventing the name twice.
	ContextCacheDir = "context"

	// LocksDir is the StateDir subdirectory holding the advisory lock sidecars
	// that guard project-scoped files (ProjectPathFor, lockpath.go). It is state,
	// not cache: a lock file is a fact about THIS machine's concurrent
	// processes and nothing rebuilds it — though nothing is lost either when it
	// is absent, which is why it earns no Layout row (see Layout's doc on what
	// a row means to doctor).
	LocksDir = "locks"

	// RepoContentPrefix is the repo-relative path prefix under which a remote
	// repo's authored content lives: .ctxloom/content/<kind>/<name>.yaml. It is
	// the canonical (non-local) counterpart to LocalPath — every fetcher/
	// publisher that builds a repo-relative content path routes through this
	// constant so there is exactly one on-disk layout, not a scattered literal.
	RepoContentPrefix = ".ctxloom/content"

	// ReposCacheDir is the subdirectory for cached git repo clones.
	ReposCacheDir = "repos"

	// RefusedAdvancesFileName is the name (without extension) of the record of
	// pin advances `ctxloom deps upgrade` DECLINED to make because the
	// content at the proposed commit carried a publisher signature that does
	// not verify over its bytes — see RefusedAdvancesPath.
	RefusedAdvancesFileName = "refused_advances"

	// DirtyTreeCommitAckFileName is the name (without extension) of the
	// per-checkout record that a human authorized ctxloom to auto-commit a
	// dirty tree on their behalf (dirty_tree_handler: "commit") — see
	// DirtyTreeCommitAckPath. It moved out of config.yaml (config-layer-scope
	// design doc, "Already wrong #1"): a config value the env layer or
	// --config-set can also set is not a durable human act, and the project
	// config file is committed and multi-author, so a value living there
	// would ship a prior authorization to every clone.
	DirtyTreeCommitAckFileName = "dirty_tree_commit_ack"

	// ProjectIDFileName is the name of the gitignored project-identity marker
	// at .ctxloom/project-id (ADR 0025) — the key to this project's task log,
	// ~/.ctxloom/tasks/<project-id>.jsonl (internal/shared/tasks/paths owns
	// the canonical resolution; this constant exists so Layout can name the
	// path without an unnamed string literal — see
	// TestPathSegments_ComeFromNamedConstants).
	ProjectIDFileName = "project-id"

	// TriggersDir is the cache/ subdirectory holding ctxloom's cached
	// revive-trigger verdicts, one file per project (see TriggerCacheDir).
	TriggersDir = "triggers"

	// TrustObjectsDir is the leaf directory, under the TrustFileName segment,
	// holding content-addressed copies of the bytes a human approved at review
	// (see TrustObjectsPath). Named separately from TrustFileName because the
	// two segments are independently meaningful: "trust" groups the store,
	// "objects" says the store is content-addressed.
	TrustObjectsDir = "objects"

	// SessionsDir is the subdirectory for per-session state (index, harp dirs).
	SessionsDir = "sessions"

	// LogsDir is the subdirectory holding the process logger's output. Home
	// rooted rather than per-project because a ctxloom process logs from the
	// moment its logger is installed — before any project root is resolved,
	// and for commands (hooks, `mcp`, `acp`) that may have no project at all.
	LogsDir = "logs"

	// LogFileName is the file every ctxloom process appends its structured log
	// to. One file for all of them: the entries carry the caller, and a reader
	// diagnosing "what did ctxloom do when my editor started" wants the hook,
	// the MCP server and the CLI interleaved in one timeline, not scattered
	// across per-command files they would have to merge by hand.
	LogFileName = "ctxloom.log"

	// IndexFileName is the name of the home-rooted session index file.
	IndexFileName = "index.yaml"

	// EssenceFileName is the name of a harp's distilled session essence.
	EssenceFileName = "essence.md"

	// PlanFileExt is the suffix for a session's plan documents. Plans live
	// in the harp's persist/ subdirectory as <descriptive-name>.plan.md files
	// (HarpPlansDir); a session may hold several.
	PlanFileExt = ".plan.md"

	// EphemeralDirName is the per-session subdirectory for REGENERABLE state
	// (worktree/container scratch, rendered config overlays): discarding it
	// never loses session history, so teardown/cleanup may remove it freely.
	// The lifetime split against PersistDirName is the container-mount policy.
	EphemeralDirName = "ephemeral"

	// PersistDirName is the per-session subdirectory that MUST survive
	// workspace teardown — engine transcripts and session-scoped artifacts. A
	// containerized run bind-mounts subtrees of it read-write so in-container
	// writes land here on the host instead of dying with the container.
	PersistDirName = "persist"

	// TranscriptStoreDirName is the persist/ subdirectory bind-mounted to a
	// containerized engine's native transcript STORE ROOT (~/.claude/projects
	// and friends). The transcript leaf name is runtime-generated by the
	// engine, so the store root — not a leaf file — is the bind target;
	// whatever leaf the engine creates lands here, harp-addressable by
	// location after teardown.
	TranscriptStoreDirName = "transcripts"

	// CanonicalTranscriptFileName is the persist/ leaf holding ctxloom's OWN
	// captured transcript (internal/transcript.Recorder's output): one JSONL
	// line per agent.ChatEvent, engine-agnostic. Deliberately a DIFFERENT name
	// from TranscriptStoreDirName so the two never collide — that one is a
	// bind-mount DIRECTORY holding an engine's native file(s); this one is a
	// single file ctxloom itself writes. This is authored session memory, not
	// derived cache: it persists under persist/, never gitignored.
	//
	// Named "transcript.jsonl", NOT "transcript.acp.jsonl" (the pre-rename
	// name): the file is fed by every structured/ACP engine AND the oneshot
	// regime (transcript.RecordOneshot) AND the vendor readers
	// (internal/transcript/vendorreader/*) — engine-agnostic by construction, per
	// this constant's own doc comment above. The old name read as "an ACP
	// artifact" to anyone browsing a session's persist/ dir, which it never
	// was. See LegacyCanonicalTranscriptFileName for the back-compat reader
	// fallback this rename requires.
	CanonicalTranscriptFileName = "transcript.jsonl"

	// LegacyCanonicalTranscriptFileName is CanonicalTranscriptFileName's
	// pre-rename value. Read-only: nothing ever writes this name again (every
	// writer targets HarpCanonicalTranscriptPath, i.e. the current name), but
	// sessions captured before the rename have ONLY this file on disk, so
	// ResolveHarpCanonicalTranscriptPath falls back to it rather than
	// treating an already-captured pre-rename session as if it had no
	// canonical transcript at all.
	LegacyCanonicalTranscriptFileName = "transcript.acp.jsonl"

	// CoordDirName is the per-user directory holding one subdirectory of
	// in-process coordinator state per project: ~/.ctxloom/coord/<project-key>/
	// (owner lock, run/mailbox/interaction journals, last-bound endpoint) — see
	// HomeCoordDir / CoordProjectStateDir. Keyed by a project identity resolved
	// outside this package (internal/agentcoord/coord.stateDirForProject), not
	// by harp: a project's coordinator state outlives any one session.
	CoordDirName = "coord"

	// CoordEndpointFileName is the discovery file inside a project's
	// coordinator state dir (~/.ctxloom/coord/<project-key>/endpoint.json):
	// the ports a coordinator last bound, re-minted every Serve() so a
	// relaunched coordinator re-binds the SAME endpoint and a separate CLI
	// invocation (internal/agentcoord/discover.List) can find it. 0600 and
	// host-local — it also carries the read-only consumer credential.
	CoordEndpointFileName = "endpoint.json"

	// SegmentsDirName is the harp-dir leaf holding per-rotation artifacts, one
	// pair per displaced session ID in a harp's sessions.Entry.Rotations
	// lineage: the converted-once canonical <sessionID>.jsonl
	// (ResolveHarpSegmentPath) and that rotation's distilled
	// <sessionID>.md (ResolveHarpSegmentEssencePath), keyed alike and
	// distinguished by extension.
	//
	// A sibling of persist/ and ephemeral/, not nested under either — neither
	// artifact is the bind-mounted vendor store (persist/transcripts), and
	// neither is purely regenerable: re-deriving a segment means re-locating a
	// vendor file that may already be gone (see
	// operations.RefreshVendorTranscript), and re-deriving an essence means
	// that AND an LLM call. So they get their own root.
	//
	// The harp's own essence.md is the CURRENT one and is overwritten by every
	// distill; these are the record of what each earlier session was about,
	// which is otherwise erased by the next /clear.
	SegmentsDirName = "segments"

	// HomeLocksDirName is the home-rooted directory holding advisory-lock
	// sidecars for FOREIGN files a ctxloom-family binary (ctxloom, ltk,
	// taskloom) does not own — see HomePathFor (lockpath.go). It shares its
	// STRING VALUE with LocksDir (both are "locks"), but the two constants
	// name DIFFERENT directories at different roots and must not be
	// collapsed into one: this one sits directly under ~/.ctxloom
	// (RootHome, see Layout's HomeLocksDirName row below); LocksDir sits
	// under a PROJECT .ctxloom's state/ (RootProject, via
	// ProjectPathFor/LocksPath).
	//
	// PathFor, ProjectPathFor and HomePathFor (lockpath.go) all live in this
	// package and reference this constant directly — the former split
	// across a package boundary (filelock carrying its own hand-synced
	// copy to dodge this package's path-authority gate) is gone now that
	// the lock-path derivation and the constant it depends on are both
	// here.
	HomeLocksDirName = "locks"

	// HomeRecordsDirName is the home-rooted directory holding hew §9.7
	// application records: one file per successful `util config-write`
	// apply against a JSON target, naming what changed, to what bytes,
	// from which patch (see internal/cli/util_config_write.go's record
	// builder). It is the audit trail distinct-bullpen's "config-write has
	// no recovery path for a foreign file" asked for, to the degree hew's
	// v0 library currently supports (a full `hew revert` is future work per
	// the spec's §9.7, not built here). Siblings HomeLocksDirName under
	// RootHome for the same reason: a record about a FOREIGN file — one
	// ctxloom does not own and so must never write ctxloom-internal state
	// beside — belongs in ctxloom's own home tree, not next to the file it
	// describes.
	HomeRecordsDirName = "records"

	// EngineTranscriptLinkPrefix names the leaf every per-vendor-log
	// convenience symlink at a harp dir's ROOT starts with (see
	// HarpEngineTranscriptLinkPath). A harp accumulates one vendor transcript
	// per engine binding it has ever had — one per /clear rotation, and one
	// per engine if a harp is ever rebound to a different engine — and each
	// gets its OWN immutable link, never a single mutable name repointed on
	// every rebind (that was the retired transcript.jsonl symlink; see
	// HarpEngineTranscriptLinkPath's doc). Exported so a lineage-listing
	// reader, if one is ever written, has one place to recognize the family
	// without re-deriving the naming scheme.
	EngineTranscriptLinkPrefix = "engine-transcript-"
)

// HomeSessionsDir returns ~/.ctxloom/sessions — the home-rooted directory
// that holds the session index and per-harp session dirs. This is the
// single source of truth for the sessions root; both the task store and the
// memory compactor resolve harp paths through it so they cannot diverge.
func HomeSessionsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve the home sessions root ~/%s/%s: %w", AppDirName, SessionsDir, err)
	}
	return filepath.Join(home, AppDirName, SessionsDir), nil
}

// HomeLogsDir returns ~/.ctxloom/logs — where every ctxloom process writes its
// structured log. A pure path join like its neighbours here: the caller decides
// whether to create the directory (cmd/ctxloom does, at logger construction).
func HomeLogsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve the home logs root ~/%s/%s: %w", AppDirName, LogsDir, err)
	}
	return filepath.Join(home, AppDirName, LogsDir), nil
}

// HomeLogFilePath returns ~/.ctxloom/logs/ctxloom.log — the STRUCTURED
// logger's sink. Deliberately NOT stderr: for hooks and the statusline command,
// stderr is a protocol surface the calling engine renders (Claude Code displays
// SessionStart hook stderr as an error, and statusline stderr lands on the
// terminal outside the alt-screen, destroying scrollback), so warn-level zap
// JSON there corrupts the user's session rather than informing anyone.
//
// Only the structured channel moves. Human-readable diagnostics (clidiag) stay
// on stderr for every command, hooks included: those are written FOR a person
// and say what to do about the problem, and a hook that did nothing must still
// be able to say so out loud rather than swallow it.
func HomeLogFilePath() (string, error) {
	dir, err := HomeLogsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, LogFileName), nil
}

// SessionIndexPath returns ~/.ctxloom/sessions/index.yaml — the home-rooted
// session index that binds harp names to backend sessions.
func SessionIndexPath() (string, error) {
	root, err := HomeSessionsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, IndexFileName), nil
}

// HarpDir returns ~/.ctxloom/sessions/<harp>/. Errors when the home dir
// can't be resolved; callers fall back to the legacy layout in that case.
//
// harp is validated here (harp.Validate) because this is the chokepoint every
// harp-derived path is built from — essence, ephemeral, canonical transcript,
// and the harp dir itself all layer on this one function. A harp
// name is a user-renameable string that becomes a single path COMPONENT, so
// `ctxloom session edit <old> --name ../..` otherwise reached MkdirAll/Symlink on
// a traversed path. Validating at each caller would have been seven chances
// to forget; validating here means no harp-derived path can be built from a
// name that escapes the sessions root.
func HarpDir(harp string) (string, error) {
	if err := harpid.Validate(harp); err != nil {
		return "", err
	}
	root, err := HomeSessionsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, harp), nil
}

// HarpEssencePath returns ~/.ctxloom/sessions/<harp>/essence.md — the distilled
// session essence for a harp-named session.
func HarpEssencePath(harp string) (string, error) {
	dir, err := HarpDir(harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, EssenceFileName), nil
}

// HarpEphemeralDir returns ~/.ctxloom/sessions/<harp>/ephemeral — the
// session's regenerable-state root (see EphemeralDirName).
func HarpEphemeralDir(harp string) (string, error) {
	dir, err := HarpDir(harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, EphemeralDirName), nil
}

// HarpPersistDir returns ~/.ctxloom/sessions/<harp>/persist — the session
// state that must survive workspace teardown (see PersistDirName).
func HarpPersistDir(harp string) (string, error) {
	dir, err := HarpDir(harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, PersistDirName), nil
}

// HarpPlansDir returns the directory a session's *.plan.md documents are
// WRITTEN to: ~/.ctxloom/sessions/<harp>/persist.
//
// It is persist/ and not the harp top level because a containerized run only
// ever gets persist/ bind-mounted (isolation.Container.sessionStateMounts).
// A plan authored at the harp top level by an agent running in a container is
// written into container-ephemeral overlay space and is GONE at teardown — the
// write succeeds, the file exists for the length of the run, and nothing
// survives. Naming the location once, here, is what keeps the instruction the
// agent is given (mcp.sessionInstructions) and the readers that later collect
// plans (plans.SessionPlanPaths) from drifting apart.
func HarpPlansDir(harp string) (string, error) {
	return HarpPersistDir(harp)
}

// HarpTranscriptStoreDir returns ~/.ctxloom/sessions/<harp>/persist/transcripts
// — the bind-mount source for a containerized engine's native transcript store
// root (see TranscriptStoreDirName).
func HarpTranscriptStoreDir(harp string) (string, error) {
	dir, err := HarpPersistDir(harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, TranscriptStoreDirName), nil
}

// ResolveHarpSegmentsDir returns ~/.ctxloom/sessions/<harp>/segments — the
// cache root for harp's per-rotation canonical segments (see
// SegmentsDirName). This is the single chokepoint every segment path is
// built from; a caller that needs one particular rotation's segment file
// should call ResolveHarpSegmentPath instead of hand-joining onto this.
func ResolveHarpSegmentsDir(harp string) (string, error) {
	dir, err := HarpDir(harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, SegmentsDirName), nil
}

// ResolveHarpSegmentEssencePath returns
// ~/.ctxloom/sessions/<harp>/segments/<sessionID>.md — the distilled essence of
// ONE rotation in harp's lineage, beside that rotation's canonical segment
// (ResolveHarpSegmentPath, the same name with a .jsonl suffix).
//
// The harp dir keeps a single current essence.md, overwritten by each distill.
// A harp accumulates one session id per /clear rotation, so a per-rotation
// essence needs the rotation's own key; segments/ is where this repo already
// keys per-rotation artifacts by session id, and an essence belongs beside the
// transcript it was distilled from rather than in a directory of its own.
//
// It is deliberately NOT project-rooted. Everything ctxloom owns about a
// session — canonical transcript, ACP transcript, segments, essence — lives
// under the harp dir; a per-project copy is a second home for the same fact,
// keyed differently, that nothing reconciles.
func ResolveHarpSegmentEssencePath(harp, sessionID string) (string, error) {
	dir, err := ResolveHarpSegmentsDir(harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sessionID+".md"), nil
}

// ResolveHarpSegmentPath returns
// ~/.ctxloom/sessions/<harp>/segments/<sessionID>.jsonl — the cached
// canonical-form conversion of ONE displaced session ID in harp's rotation
// lineage (sessions.Entry.Rotations). Converted at most once per rotation
// (operations.RefreshVendorTranscript checks this path for an existing file
// before re-running the vendor adapter over that rotation's transcript) since
// a rotation's vendor file is immutable history — the engine will never write
// to it again once a later rotation has superseded it.
func ResolveHarpSegmentPath(harp, sessionID string) (string, error) {
	dir, err := ResolveHarpSegmentsDir(harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sessionID+".jsonl"), nil
}

// HarpEngineTranscriptLinkPath returns
// ~/.ctxloom/sessions/<harp>/engine-transcript-<engine>-<sessionID>.jsonl —
// one IMMUTABLE convenience symlink per vendor log a harp has ever been bound
// to, living at the harp dir's root (see EngineTranscriptLinkPrefix).
//
// This replaces the single mutable `<harp>/transcript.jsonl` symlink
// (formerly created by sessions.linkTranscriptIntoHarpDir, non-atomically
// repointed on every /clear rotation) that name-collided with the canonical
// transcript's OWN leaf name (CanonicalTranscriptFileName, at
// persist/transcript.jsonl — a different file in a different format) and
// could only ever name the SINGLE most recent vendor log, even though a harp
// accumulates several over its life (one per rotation, one per engine).
// Naming each link by engine+session-id instead means the harp dir's own
// listing shows the full lineage, and no later bind ever needs to touch an
// earlier link.
//
// engine and sessionID both become filename components, not path segments —
// same posture as ResolveHarpSegmentPath's bare sessionID+".jsonl" join, and
// like it, deliberately unvalidated beyond non-emptiness: both values
// originate from ctxloom's own engine registry and the backend's own session
// UUID, never from unreviewed user input. sessions.linkEngineTranscript is
// the sole writer of this path today, called from BindSession at the moment
// a binding's session_id/transcript_path first land — never on a later
// rotation-append of an already-existing binding, whose own link was created
// when IT was current.
func HarpEngineTranscriptLinkPath(harp, engine, sessionID string) (string, error) {
	dir, err := HarpDir(harp)
	if err != nil {
		return "", err
	}
	if engine == "" || sessionID == "" {
		return "", fmt.Errorf("engine transcript link path for harp %q: engine (%q) and session id (%q) are both required", harp, engine, sessionID)
	}
	return filepath.Join(dir, fmt.Sprintf("%s%s-%s.jsonl", EngineTranscriptLinkPrefix, engine, sessionID)), nil
}

// HarpCanonicalTranscriptPath returns
// ~/.ctxloom/sessions/<harp>/persist/transcript.jsonl — the canonical,
// engine-agnostic transcript ctxloom captures itself (see
// CanonicalTranscriptFileName). This is the file internal/transcript.Recorder
// appends to and internal/transcript.CanonicalHistory (a later slice) reads
// from; it is distinct from HarpTranscriptStoreDir, which bind-mounts an
// engine's own native store.
//
// Always the CURRENT name, unconditionally — every writer (Recorder,
// ConvertVendorTranscript) targets this path, never the legacy one. A reader
// that needs to find an already-captured transcript regardless of which name
// it was written under should call ResolveHarpCanonicalTranscriptPath
// instead.
func HarpCanonicalTranscriptPath(harp string) (string, error) {
	dir, err := HarpPersistDir(harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, CanonicalTranscriptFileName), nil
}

// ResolveHarpCanonicalTranscriptPath returns the on-disk path to harp's
// captured canonical transcript, preferring HarpCanonicalTranscriptPath (the
// current name) but falling back to the pre-rename
// LegacyCanonicalTranscriptFileName when only THAT one exists on disk — the
// back-compat path for a session captured before the transcript.acp.jsonl ->
// transcript.jsonl rename. Returns the current-name path, unstated, when
// NEITHER file exists, so a caller's own os.Stat still produces a clean
// "not captured yet" against the canonical, current name (matching
// HarpCanonicalTranscriptPath's existing no-file contract).
//
// A stat that fails for any reason OTHER than absence errors instead. Absence
// is a fact this function is entitled to act on; an unanswerable stat is not,
// and returning a path for it would report a guess in the shape of a result.
//
// Unlike every other function in this file, this one does I/O (a stat per
// candidate) — it is reserved for read paths that need "does a captured
// transcript exist, and where" (transcript.CanonicalHistory.GetSession,
// sessions.fillCanonicalTranscript, operations.hasCanonicalTranscript).
// Writers always call HarpCanonicalTranscriptPath directly: this rename is
// forward-only, nothing ever writes the legacy name again.
func ResolveHarpCanonicalTranscriptPath(harp string) (string, error) {
	// One home-dir resolution for both leaf names: the legacy path can only
	// differ from the current one in its file name, so re-deriving the
	// directory (and re-handling an error that already fired) buys nothing.
	dir, err := HarpPersistDir(harp)
	if err != nil {
		return "", err
	}
	current := filepath.Join(dir, CanonicalTranscriptFileName)
	legacy := filepath.Join(dir, LegacyCanonicalTranscriptFileName)

	// Only PLAIN ABSENCE licenses moving on. The fallback's whole precondition
	// is "the current name is not there", and an ELOOP or EACCES does not
	// establish that — it says the question could not be answered. Treating
	// the two alike hands back the pre-rename transcript, with a nil error,
	// while a current one sits on disk unread: the caller cannot tell a
	// resolution from a guess, because both look like a path.
	switch _, statErr := os.Stat(current); {
	case statErr == nil:
		return current, nil
	case !errors.Is(statErr, fs.ErrNotExist):
		return "", fmt.Errorf("stat canonical transcript %s: %w", current, statErr)
	}

	switch _, statErr := os.Stat(legacy); {
	case statErr == nil:
		return legacy, nil
	case !errors.Is(statErr, fs.ErrNotExist):
		return "", fmt.Errorf("stat pre-rename canonical transcript %s: %w", legacy, statErr)
	}

	return current, nil
}

// TriggerCacheDir returns ~/.ctxloom/cache/triggers — the home-rooted
// directory holding ctxloom's cached revive-trigger verdicts, one file per
// project (see internal/operations' verdict cache). It deliberately lives
// OUTSIDE any project tree and outside taskloom's own store
// (~/.ctxloom/tasks/<project-id>.jsonl, internal/shared/tasks/paths): a
// verdict cache is pure derived scratch, safe to delete at any time, and
// keeping it off both the repo and the task log means neither a git clone nor
// a `taskloom` operation can ever touch it.
func TriggerCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve the trigger verdict cache ~/%s/%s/%s: %w", AppDirName, CacheDir, TriggersDir, err)
	}
	return filepath.Join(home, AppDirName, CacheDir, TriggersDir), nil
}

// HomeCoordDir returns ~/.ctxloom/coord — the per-user root holding one
// subdirectory of coordinator state per project (see CoordDirName,
// CoordProjectStateDir). internal/agentcoord/discover.List globs one level
// below this root for every project's endpoint.json.
func HomeCoordDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve the coordinator state root ~/%s/%s: %w", AppDirName, CoordDirName, err)
	}
	return filepath.Join(home, AppDirName, CoordDirName), nil
}

// CoordProjectStateDir returns ~/.ctxloom/coord/<projectKey> — one project's
// coordinator state directory. projectKey is assumed to already be a single
// safe path segment (internal/agentcoord/coord.sanitizeKey's job, not this
// package's: a coordinator project key is not a harp, so it gets no
// HarpDir-style traversal validation here); this function only composes the
// path.
func CoordProjectStateDir(projectKey string) (string, error) {
	dir, err := HomeCoordDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, projectKey), nil
}

// HomeLocksDir returns ~/.ctxloom/locks — the home-rooted directory holding
// advisory-lock sidecars for FOREIGN files a ctxloom-family binary does not
// own (see HomePathFor, lockpath.go, and HomeLocksDirName's doc).
func HomeLocksDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve the home lock directory ~/%s/%s: %w", AppDirName, HomeLocksDirName, err)
	}
	return filepath.Join(home, AppDirName, HomeLocksDirName), nil
}

// HomeRecordsDir returns ~/.ctxloom/records — the home-rooted directory
// holding hew §9.7 application records for FOREIGN files `util
// config-write` merges into (see HomeRecordsDirName's doc).
func HomeRecordsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve the home records directory ~/%s/%s: %w", AppDirName, HomeRecordsDirName, err)
	}
	return filepath.Join(home, AppDirName, HomeRecordsDirName), nil
}

// CachePath returns the cache subdirectory path for the given app path.
// Cache contains regeneratable content: bundles, vendor, context, memory.
func CachePath(appPath string) string {
	return filepath.Join(appPath, CacheDir)
}

// ConfigPath returns the path to the config file (at appPath root).
func ConfigPath(appPath string) string {
	return filepath.Join(appPath, ConfigFileName+".yaml")
}

// RemotesPath returns the path to the remotes file (at appPath root).
func RemotesPath(appPath string) string {
	return filepath.Join(appPath, RemotesFileName+".yaml")
}

// ApprovalsPath returns the path to the PROJECT (committable) countersignature
// store directory, at appPath root next to the extensionless allowed_signers
// trust root (AllowedSignersFileName — OpenSSH's format, not ctxloom's, so it
// carries no .yaml suffix). "Our team's
// approvals": a lead reviews, commits the signatures here, and every developer
// / CI run who trusts the lead's key (via the project allowed_signers)
// inherits the approval without re-reviewing (spec §9.2).
func ApprovalsPath(appPath string) string {
	return filepath.Join(appPath, ApprovalsDirName)
}

// HomeApprovalsPath returns ~/.ctxloom/approvals — the user-scoped
// countersignature store. "My approvals follow me": the default write target
// of `ctxloom review`, never committed, never shared (spec §9.2).
func HomeApprovalsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve the user countersignature store ~/%s/%s: %w", AppDirName, ApprovalsDirName, err)
	}
	return filepath.Join(home, AppDirName, ApprovalsDirName), nil
}

// HomeCompanionConsentPath returns ~/.ctxloom/companion_consent.yaml — the
// user-scoped record of which companion binaries the human agreed ctxloom may
// EXECUTE (config.CompanionConsentStore). There is deliberately no project
// counterpart: see CompanionConsentFileName.
func HomeCompanionConsentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve the companion consent record ~/%s/%s.yaml: %w", AppDirName, CompanionConsentFileName, err)
	}
	return filepath.Join(home, AppDirName, CompanionConsentFileName+".yaml"), nil
}

// AllowedSignersPath returns the path to the trust-root file (at appPath root,
// next to the approvals/ directory). Committable: a team distributes "trust
// our lead's approve key / our org's publish key" by checking this file in,
// which is trust-on-first-clone and strictly inside a boundary the clone
// already crossed (spec §7.3, path A).
func AllowedSignersPath(appPath string) string {
	return filepath.Join(appPath, AllowedSignersFileName)
}

// HomeAllowedSignersPath returns ~/.ctxloom/allowed_signers — the user-scoped
// trust root, which follows the developer across every project and is where an
// enterprise MDM channel drops the org's keys (spec §7.3, path B).
func HomeAllowedSignersPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve the user trust root ~/%s/%s: %w", AppDirName, AllowedSignersFileName, err)
	}
	return filepath.Join(home, AppDirName, AllowedSignersFileName), nil
}

// DistrustedSignersPath returns the path to the LOCAL embedded-key suppression
// record (at appPath root, next to allowed_signers): the negative counterpart
// to it. allowed_signers is purely additive — there is no way
// to write a "no longer trust this key" entry into it — so a distrusted
// embedded principal is recorded HERE instead, one principal per line, and
// Config.TrustRoot() (trustroot.go) subtracts any embedded entry matching a
// line in this file before unioning the trust root. It never edits
// allowed_signers itself, and it can never remove a key that isn't ctxloom's
// own compiled-in one — `signer remove` only writes here when the principal
// named matches an embedded entry (see operations.RemoveSigner).
func DistrustedSignersPath(appPath string) string {
	return filepath.Join(appPath, DistrustedSignersFileName)
}

// HomeDistrustedSignersPath returns ~/.ctxloom/distrusted_signers — the
// user-scoped counterpart to DistrustedSignersPath, mirroring
// HomeAllowedSignersPath (follows the developer across every project).
func HomeDistrustedSignersPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve the user distrust record ~/%s/%s: %w", AppDirName, DistrustedSignersFileName, err)
	}
	return filepath.Join(home, AppDirName, DistrustedSignersFileName), nil
}

// LockPath returns the path to the lock file (at appPath root).
func LockPath(appPath string) string {
	return filepath.Join(appPath, LockFileName+".yaml")
}

// ProfilesPath returns the path to the profiles directory (at appPath root).
func ProfilesPath(appPath string) string {
	return filepath.Join(appPath, ProfilesDir)
}

// AgentsPath returns the RETIRED agents directory (at appPath root). Nothing
// reads definitions from it; config.retiredAgentsDirSignpost uses this to name
// the files a user must move into config.yaml's `agents:` key.
func AgentsPath(appPath string) string {
	return filepath.Join(appPath, AgentsDir)
}

// CacheBundlesPath returns the CACHE bundles directory (.ctxloom/cache/bundles)
// — the install root for remote-pulled bundle artifacts (Reference.LocalPath).
// It is GITIGNORED and regenerable: nothing under it is ever committed, and
// anything ctxloom writes there can be deleted and re-derived.
//
// It is NOT where project-authored bundles live: authored content belongs in
// the COMMITTED content tree, LocalBundlesPath. Callers that mean "the
// project's own bundles" (create/import/export/list/sign) must use that one —
// wiring them here is the bug this split exists to prevent.
func CacheBundlesPath(appPath string) string {
	return filepath.Join(CachePath(appPath), BundlesDir)
}

// LocalPath returns the path to the committed content directory (at
// appPath/content, NOT under cache/). It is the working-copy root for
// ctxloom:local references.
func LocalPath(appPath string) string {
	return filepath.Join(appPath, ContentDir)
}

// LocalBundlesPath returns the COMMITTED authored-bundles directory
// (.ctxloom/content/bundles) — the one on-disk home for a project's own
// bundles, and the tree a publishing repo ships. It is the local half of the
// same layout a remote repo exposes under RepoContentPrefix, so a bundle repo
// and a consuming project lay their bundles out identically.
func LocalBundlesPath(appPath string) string {
	return filepath.Join(LocalPath(appPath), BundlesDir)
}

// ReposCachePath returns the path to the repos cache directory (under cache/).
func ReposCachePath(appPath string) string {
	return filepath.Join(CachePath(appPath), ReposCacheDir)
}

// TrustObjectsPath returns the approved-content snapshot directory (under
// state/): content-addressed copies of the bytes a human approved at review,
// keyed by a payload hash. The review porcelain diffs an UPDATE against them.
//
// STATE, not cache, and the distinction is the whole of Tier's doc: nothing
// rebuilds these. They are the bytes that existed at the moment a human said
// yes, and once they are gone no pull, sync or re-derivation brings them back —
// every later update review degrades from a diff to a full-content dump, which
// is a quieter loss than an error and therefore an easier one to cause. Under
// cache/ they sat in a directory whose whole contract is "delete me freely",
// which is an invitation to exactly that.
//
// Losing them is not a correctness failure: the countersignature stores remain
// authoritative about what was approved. It is a review-quality failure, which
// is why this is TierLocal-with-a-Lost-string rather than something that fails
// loud.
func TrustObjectsPath(appPath string) string {
	return filepath.Join(StatePath(appPath), TrustFileName, TrustObjectsDir)
}

// LegacyTrustObjectsPath returns the pre-relocation snapshot directory under
// cache/. It exists for ONE reader — the one-time migration in
// internal/operations' snapshot store — so the retired location is named once,
// beside its replacement, instead of being re-derived as a literal wherever
// somebody remembers it. Nothing writes here.
func LegacyTrustObjectsPath(appPath string) string {
	return filepath.Join(CachePath(appPath), TrustFileName, TrustObjectsDir)
}

// RefusedAdvancesPath returns the refused-advance record (under cache/): what
// the last `deps upgrade` round declined to advance, and the pin it kept
// instead, so an inspector run days later can still say why a revision is not
// here. Without it the refusal exists only in the transient stdout of the sync
// that produced it.
//
// PROJECT-scoped, not user-scoped, and that is the whole reason it is not
// beside the personal admission stores under ~/.ctxloom: a refusal is a fact
// about ONE lockfile's pin. Two checkouts on the same machine depending on the
// same bundle can legitimately be in different states — one advanced, one
// refused — and a home-scoped record keyed by bundle identity would report the
// wrong one's problem in the other's directory.
//
// Under cache/ because it is DERIVED and regenerable: re-running `ctxloom
// deps upgrade` reproduces it exactly, deleting it only costs the after-the-
// fact advisory (the sync still says so at the moment it refuses), and nothing
// about it should be committed — it describes what one machine saw upstream at
// one moment, not a decision the team shares.
func RefusedAdvancesPath(appPath string) string {
	return filepath.Join(CachePath(appPath), RefusedAdvancesFileName+".yaml")
}

// DefaultRemotesPath returns the default remotes path relative to current directory.
func DefaultRemotesPath() string {
	return RemotesPath(AppDirName)
}

// StatePath returns the THIRD .ctxloom tier (under appPath/state): local-only,
// gitignored, unrebuildable checkout state — see StateDir's doc.
func StatePath(appPath string) string {
	return filepath.Join(appPath, StateDir)
}

// LocksPath returns the project's advisory-lock directory (under state/) — one
// flat directory holding every lock sidecar guarding a file in this .ctxloom
// tree. ProjectPathFor (lockpath.go) owns the protected-path→lock-name
// mapping; this function owns only WHERE that mapping puts its results, so
// the location moves in one place if it ever moves again.
func LocksPath(appPath string) string {
	return filepath.Join(StatePath(appPath), LocksDir)
}

// SessionStatePath returns <appPath>/state/<harp> — the project-scoped,
// gitignored root for ONE session's local files. Nothing else lives under it
// today but the engine config-home instance (SessionHomePath); reserved empty
// siblings are deliberately not created.
//
// harp is VALIDATED here for exactly the reason HarpDir validates: a harp is a
// user-renameable string (`ctxloom session edit <old> --name ../..`) that
// becomes a single path COMPONENT, and this is the chokepoint every
// session-scoped project path is built from. An empty or traversing harp is an
// error, never a fallback to some shared project-wide path — a shared fallback
// is precisely the durable per-project engine home the per-session instance
// model retired.
func SessionStatePath(appPath, harp string) (string, error) {
	if err := harpid.Validate(harp); err != nil {
		return "", err
	}
	return filepath.Join(StatePath(appPath), harp), nil
}

// SessionHomePath returns <appPath>/state/<harp>/home — the engine config-home
// INSTANCE for that session. Each engine appends its own leaf (".codex" /
// "claude" / "kiro"), distinct by construction, so one root hosts every engine
// a session runs without collision.
//
// The instance is created at instance time and is disposable: everything in it
// is either regenerated by ctxloom's managed-block writers, synthesized by the
// engine packages, or one-way COPIED IN from the user's real host home. The
// durable truth stays the real host home (~/.claude, ~/.codex, ~/.kiro), which
// ctxloom never writes. Deleting an instance costs nothing, which is why
// state/<harp> gets no Layout row: a per-session directory's absence is the
// normal case, and Layout enumerates paths whose absence doctor reports.
func SessionHomePath(appPath, harp string) (string, error) {
	root, err := SessionStatePath(appPath, harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, SessionHomeDirName), nil
}

// DirtyTreeCommitAckPath returns the record that a human authorized ctxloom to
// commit on their behalf in THIS checkout (see DirtyTreeCommitAckFileName). It
// is an internal/shared/admission.Store file, the same mechanism
// HomeCompanionConsentPath uses for "may ctxloom act on this machine without
// asking again" — except this one is PROJECT-scoped (a fact about one
// checkout's branch, not the user), so it lives under appPath/state rather
// than the home directory.
func DirtyTreeCommitAckPath(appPath string) string {
	return filepath.Join(StatePath(appPath), DirtyTreeCommitAckFileName+".yaml")
}

// Tier classifies one .ctxloom path by WHAT A FRESH CLONE GETS — the question
// that matters for "can I lose this" and "does a clone start from the same
// place I did", not merely "is it gitignored" (TierLocal and the gitignored
// half of TierDerived are BOTH gitignored; only asking a clone tells them
// apart).
type Tier uint8

const (
	// TierCommitted paths are checked in: a clone has them, byte for byte.
	TierCommitted Tier = iota
	// TierDerived paths are REBUILDABLE by a named command from committed pins
	// (a lockfile, a remote) — deleting one only costs the time to re-run that
	// command. Rebuildability is the whole of the definition; being gitignored
	// is the usual CONSEQUENCE of it, not part of it.
	//
	// lock.yaml is the deliberate exception, and the reason the two are stated
	// separately: `ctxloom remote lock` regenerates it, so it is derived — and
	// it is COMMITTED anyway, because a lockfile whose whole job is pinning
	// versions for the next clone is worthless if the clone does not get it.
	// Derived-and-committed is a coherent position; "gitignored" was never the
	// test.
	TierDerived
	// TierLocal paths are gitignored and NOTHING rebuilds them: a fact about
	// this checkout on this machine that a clone simply does not have, and
	// that no sync/pull/install command reconstructs. Entry.Rebuild is empty
	// exactly for this tier — an empty Rebuild is what makes an absence worth
	// reporting instead of shrugging at.
	TierLocal
)

// String names t for a diagnostic (e.g. doctor's TierLocal report).
func (t Tier) String() string {
	switch t {
	case TierCommitted:
		return "committed"
	case TierDerived:
		return "derived"
	case TierLocal:
		return "local"
	default:
		return "unknown"
	}
}

// RootKind names which of the package's two roots (see the package doc,
// "the two roots") an Entry.Rel resolves under.
type RootKind uint8

const (
	// RootProject entries resolve relative to the project app dir's parent —
	// today's sole behavior, and the zero value, so every Entry declared
	// before RootKind existed is unchanged.
	RootProject RootKind = iota
	// RootHome entries resolve relative to the user's home directory
	// (os.UserHomeDir()), matching the Home* function family (HomeSessionsDir,
	// HomeApprovalsPath, TriggerCacheDir, HomeCoordDir, ...).
	RootHome
)

// String names r for a diagnostic.
func (r RootKind) String() string {
	switch r {
	case RootProject:
		return "project"
	case RootHome:
		return "home"
	default:
		return "unknown"
	}
}

// Presence classifies whether an Entry's absence is worth a doctor warning —
// an axis independent of Tier (which is about REBUILDABILITY of content, not
// about whether skipping it is normal). The two happen to correlate for
// every RootProject entry today (each is created by project setup, so a
// missing one is a genuine loss), which is why they were never split apart
// until a RootHome entry needed to say something different: a home-rooted
// store is shared across every project on the machine and created lazily by
// a specific feature (a session run anywhere, a countersignature given, a
// signer trusted, ...), so a fresh install — or a long-lived one that simply
// never exercised that feature — legitimately has none of it yet, and that
// is not a loss doctor should report.
type Presence uint8

const (
	// PresenceMustExist entries are expected to exist once basic project
	// setup has happened; absence is a genuine loss worth a doctor warning.
	// The zero value, so every Entry declared before Presence existed keeps
	// today's warn-on-absence behavior unchanged.
	PresenceMustExist Presence = iota
	// PresenceIfUsed entries are created lazily by exercising a specific
	// feature; their absence is never reported, but their PRESENCE is (see
	// doctorCheckLocalTierState), so doctor can still say what it found.
	PresenceIfUsed
)

// Entry is one classified .ctxloom path, as Layout enumerates them.
type Entry struct {
	// Rel is the path relative to the root Root names: the project app dir's
	// parent for RootProject (e.g. ".ctxloom/cache/bundles"), the user's home
	// directory for RootHome (e.g. ".ctxloom/sessions" under ~).
	Rel  string
	Root RootKind
	Tier Tier
	// Rebuild names the command that reconstructs this path from committed
	// pins. Empty if and only if Tier is TierLocal — see Tier's doc.
	Rebuild string
	// Lost is TierLocal-only: what a clone (RootProject) or this machine
	// (RootHome) does not have, in a user's words — the text doctor's
	// absent-TierLocal-entry check surfaces, subject to Presence.
	Lost string
	// Presence decides whether Lost is ever surfaced for a TierLocal entry
	// missing on disk. See Presence's doc.
	Presence Presence
}

// ResolveRoot returns the directory Entry.Rel should be joined onto: for
// RootProject entries, the project app dir's parent (appDir is the same
// value ConfigPath/StatePath/etc. take as appPath, joined with AppDirName by
// the caller); for RootHome entries, home (typically os.UserHomeDir(), the
// caller's job to resolve since this package does no I/O). Both arguments
// are used as given — this function does no I/O and does not validate them.
func (e Entry) ResolveRoot(appDir, home string) string {
	if e.Root == RootHome {
		return home
	}
	return filepath.Dir(appDir)
}

// Layout is the classification of every path this tree's own writers produce
// under .ctxloom, each appearing exactly once — the table the
// config-layer-scope design doc's ".ctxloom classification" section derived by
// hand, given a name so a doctor check (and any future arch test) has
// something to walk instead of re-deriving it by inspection every time.
//
// docs/layout.md is the user-facing account of the same classification — what a
// clone gets, what may be deleted, and what each deletion costs. The two must
// agree; this is the source.
func Layout() []Entry {
	return []Entry{
		{Rel: filepath.Join(AppDirName, ConfigFileName+".yaml"), Tier: TierCommitted},
		{Rel: filepath.Join(AppDirName, RemotesFileName+".yaml"), Tier: TierCommitted},
		{Rel: filepath.Join(AppDirName, LockFileName+".yaml"), Tier: TierDerived, Rebuild: "ctxloom remote lock"},
		{Rel: filepath.Join(AppDirName, ContentDir), Tier: TierCommitted},
		{Rel: filepath.Join(AppDirName, ProfilesDir), Tier: TierCommitted},
		{Rel: filepath.Join(AppDirName, AllowedSignersFileName), Tier: TierCommitted},
		{Rel: filepath.Join(AppDirName, DistrustedSignersFileName), Tier: TierCommitted},
		{Rel: filepath.Join(AppDirName, ApprovalsDirName), Tier: TierCommitted},
		{Rel: filepath.Join(AppDirName, CacheDir, BundlesDir), Tier: TierDerived, Rebuild: "ctxloom deps pull"},
		{Rel: filepath.Join(AppDirName, CacheDir, ReposCacheDir), Tier: TierDerived, Rebuild: "ctxloom deps pull"},
		{Rel: filepath.Join(AppDirName, CacheDir, RefusedAdvancesFileName+".yaml"), Tier: TierDerived, Rebuild: "ctxloom deps upgrade"},
		// The assembled context files (agent.WriteContextFile), one per content
		// hash. Derived, and it stays in cache/ deliberately: the file is
		// content-ADDRESSED — a function of the fragment set, not of the
		// session — so two sessions that assemble the same context share one
		// file, which is a cache's defining property rather than an accident.
		//
		// The Rebuild command names `ctxloom manage hooks install` rather than
		// `ctxloom run`, though a run rewrites it too: there is no `ctxloom
		// context` command to point at, and of the two writers only the hook
		// apply is a thing a user can run ON PURPOSE to get the directory back.
		{
			Rel: filepath.Join(AppDirName, CacheDir, ContextCacheDir), Tier: TierDerived,
			Rebuild: "ctxloom manage hooks install (the next ctxloom run also rewrites it)",
		},
		{
			Rel: filepath.Join(AppDirName, StateDir, TrustFileName, TrustObjectsDir), Tier: TierLocal,
			Lost: "the content-addressed snapshots review diffed an update against; update review degrades from a diff to a full-content dump, but committed approval signatures still verify",
		},
		{
			Rel: filepath.Join(AppDirName, ProjectIDFileName), Tier: TierLocal,
			Lost: "the key to this project's task log (~/.ctxloom/tasks/<project-id>.jsonl); without it a fresh clone mints a NEW project id and starts an empty log, and every task the team logged stays on disk under the old id, unreachable from the clone",
		},
		{
			Rel: filepath.Join(AppDirName, SessionsDir), Tier: TierLocal,
			Lost: "this machine's distilled session records",
		},
		{Rel: filepath.Join(AppDirName, StateDir), Tier: TierLocal, Lost: "local-only checkout state, e.g. the dirty-tree-commit acknowledgement — see DirtyTreeCommitAckPath"},
		// state/<harp> (SessionStatePath) deliberately gets NO row: it is
		// per-SESSION, created at instance time and disposable, so its absence
		// is the normal case rather than a loss worth reporting — and a row
		// cannot name a harp that does not exist yet anyway. TestArch_
		// LayoutHasNoHarpKeyedRows keeps that true.

		// --- RootHome: the home-rooted stores, added by C13 (fs-consolidation
		// plan) so doctor can finally see them. Each names a STORE ROOT only —
		// never a harp- or project-key-keyed subpath (HarpDir, CoordProjectStateDir
		// and friends stay unrepresented, same reasoning as state/<harp> above).
		// Every one of them is TierLocal (no ctxloom command reconstructs their
		// content — see Tier's doc) and PresenceIfUsed: a home-rooted store is
		// shared across every project on the machine and created lazily by
		// exercising a specific feature, so a fresh install (or one that simply
		// never touched that feature) has none of it yet, and that absence is
		// never reported. When present, doctorCheckLocalTierState lists it — see
		// Presence's doc for the full reasoning.
		{
			Rel: filepath.Join(AppDirName, SessionsDir), Root: RootHome, Tier: TierLocal, Presence: PresenceIfUsed,
			Lost: "this machine's distilled record of every ctxloom session, across every project",
		},
		{
			Rel: filepath.Join(AppDirName, ApprovalsDirName), Root: RootHome, Tier: TierLocal, Presence: PresenceIfUsed,
			Lost: "the user-scoped countersignature store (HomeApprovalsPath); update review degrades from a diff to a full-content dump for approvals only this store held, though committed approval signatures still verify",
		},
		{
			Rel: filepath.Join(AppDirName, AllowedSignersFileName), Root: RootHome, Tier: TierLocal, Presence: PresenceIfUsed,
			Lost: "every signing key you personally trusted (ctxloom signer trust); each must be re-trusted by hand",
		},
		{
			Rel: filepath.Join(AppDirName, DistrustedSignersFileName), Root: RootHome, Tier: TierLocal, Presence: PresenceIfUsed,
			Lost: "every embedded signing key you personally distrusted (ctxloom signer untrust); each suppression must be re-recorded by hand",
		},
		{
			Rel: filepath.Join(AppDirName, CacheDir, TriggersDir), Root: RootHome, Tier: TierLocal, Presence: PresenceIfUsed,
			Lost: "cached revive-trigger verdicts; nothing durable is lost — the next trigger check silently recomputes them — but re-checking a large deferred-task backlog cold costs more time",
		},
		{
			Rel: filepath.Join(AppDirName, CoordDirName), Root: RootHome, Tier: TierLocal, Presence: PresenceIfUsed,
			Lost: "coordinator state for every project (owner locks, journals); a LIVE coordinator loses its lock and journal outright, and a recent-but-exited one's history becomes unrecoverable",
		},
		{
			Rel: filepath.Join(AppDirName, CompanionConsentFileName+".yaml"), Root: RootHome, Tier: TierLocal, Presence: PresenceIfUsed,
			Lost: "the record of which companion binaries you agreed ctxloom may execute; you are asked again",
		},
		// Added by the home-lock-dir fix (fs-consolidation N1/undated-bronco
		// closeout), same C13 shape as the seven RootHome rows above it.
		{
			Rel: filepath.Join(AppDirName, HomeLocksDirName), Root: RootHome, Tier: TierLocal, Presence: PresenceIfUsed,
			Lost: "cross-binary lock sidecars for foreign engine-settings files (HomePathFor, lockpath.go); harmless — a lock file carries no data and is recreated on next use, though a write in flight when it disappears loses its mutual exclusion for that one operation",
		},
		// Added alongside `util config-write`'s hew adoption (P5 slice 1):
		// same RootHome/PresenceIfUsed shape as the locks row above it, for
		// the parallel reason — this is state ABOUT a foreign file, so it
		// cannot live beside that file.
		{
			Rel: filepath.Join(AppDirName, HomeRecordsDirName), Root: RootHome, Tier: TierLocal, Presence: PresenceIfUsed,
			Lost: "the audit trail of what `util config-write` changed in foreign JSON config files (hew §9.7 application records) — the files themselves are unaffected; only the record of having changed them is gone",
		},
	}
}
