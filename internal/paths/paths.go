// Package paths provides shared path constants for ctxloom.
package paths

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	harpid "github.com/ctxloom/ctxloom/internal/shared/harp"
)

const (
	// AppDirName is the name of the ctxloom directory.
	AppDirName = ".ctxloom"

	// CacheDir is the subdirectory for cached/regeneratable data (bundles, vendor, context).
	// These can be deleted and re-fetched from remotes.
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
	// (cache/trust/objects), the approved-content snapshot store.
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

	// AgentsDir is the subdirectory for local-only agent definitions
	// (.ctxloom/agents/<name>.yaml). Agents are an end-user, LOCAL-only
	// engine↔profile binding — never shipped in bundles or remotes — so this
	// directory has no remote/cache analog, unlike ProfilesDir.
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

	// EnginesDir is the StateDir subdirectory grouping the per-engine config
	// homes ctxloom points a project-scoped engine at — see EngineStateHome.
	// One segment per engine hangs off it so two home-keyed engines can never
	// share a home (and so a future engine plugs into one named place rather
	// than inventing a location of its own).
	EnginesDir = "engines"

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
	// directly in the harp session directory as <descriptive-name>.plan.md
	// files; a session may hold several.
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
	// was. See legacyCanonicalTranscriptFileName for the back-compat reader
	// fallback this rename requires.
	CanonicalTranscriptFileName = "transcript.jsonl"

	// legacyCanonicalTranscriptFileName is CanonicalTranscriptFileName's
	// pre-rename value. Read-only: nothing ever writes this name again (every
	// writer targets HarpCanonicalTranscriptPath, i.e. the current name), but
	// sessions captured before the rename have ONLY this file on disk, so
	// ResolveHarpCanonicalTranscriptPath falls back to it rather than
	// treating an already-captured pre-rename session as if it had no
	// canonical transcript at all.
	legacyCanonicalTranscriptFileName = "transcript.acp.jsonl"
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
// legacyCanonicalTranscriptFileName when only THAT one exists on disk — the
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
	legacy := filepath.Join(dir, legacyCanonicalTranscriptFileName)

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

// ProjectSessionsDir returns the project-rooted directory holding distilled
// session .md files. It is distinct from the home-rooted HomeSessionsDir: this
// is per-project state under the app dir. Resolution prefers the configured app
// dir, then <cwd>/.ctxloom/sessions, then a bare relative .ctxloom/sessions when
// even the working directory can't be resolved.
//
// The first two results are ANCHORED — absolute, fixed at the moment of the
// call. The last is not: a relative path is resolved by whoever uses it,
// against whatever the working directory is then, so a caller that changes
// directory between this call and its read or write addresses somewhere else
// entirely. Nothing downstream can tell the two kinds of answer apart from the
// string, which is why the degradation is announced here rather than inferred
// there. It stays a warning, not an error: an unanchorable sessions dir must
// not block startup.
func ProjectSessionsDir(appDir string) string {
	if appDir != "" {
		return filepath.Join(appDir, SessionsDir)
	}
	wd, err := os.Getwd()
	if err == nil {
		return filepath.Join(wd, AppDirName, SessionsDir)
	}
	relative := filepath.Join(AppDirName, SessionsDir)
	clidiag.Warn("ctxloom", "cannot resolve the working directory (%v); the project sessions directory falls back to the relative %s, which resolves against the caller's current directory", err, relative)
	return relative
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

// AgentsPath returns the path to the local agents directory (at appPath
// root). Local-only: there is no cache/remote counterpart.
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
// cache/): content-addressed copies of the bytes a human approved at review,
// keyed by a payload hash. The review porcelain diffs an UPDATE against them.
// Pure cache: deleting it only degrades update review from a diff to a
// full-content display (the countersignature stores stay authoritative).
func TrustObjectsPath(appPath string) string {
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

// EngineStateHome is the project-scoped config home ctxloom points an engine's
// HOME-RELOCATION variable at — $CODEX_HOME (codex, every axis),
// $CLAUDE_CONFIG_DIR (claude-code) and $KIRO_HOME (kiro) on an in-tree AGENT
// run. It holds what the engine keeps in its own home: config, global
// prompts/skills/steering, session state, and seeded credentials.
//
// An engine's CWD-KEYED surfaces (claude's CLAUDE.md/.claude, kiro's .kiro,
// opencode's opencode.json, the shared AGENTS.md) are NOT relocated here: those
// live where the engine natively looks, and ctxloom does not get a vote. Only
// the home moves, and only because ctxloom is the one choosing where it points.
//
// State tier, deliberately (see StateDir): the directory holds seeded
// credentials and a user's own hand-edited engine config, so it is gitignored
// AND unrebuildable — no `deps pull` or cache wipe reconstructs it, which is
// exactly what disqualifies cache/ and what makes the single .ctxloom/state/
// ignore rule cover a seeded auth.json.
//
// It is the ONE owner of the location. Each engine reaches it through its own
// package's StateHome helper — internal/codex.StateHome,
// internal/claude.StateHome, internal/kiro.StateHome — and every run path and
// static writer for that engine resolves through that helper, so a run and a
// materialize can never target different roots. That is the writer split this
// helper exists to close.
func EngineStateHome(appPath, engine string) string {
	return filepath.Join(StatePath(appPath), EnginesDir, engine)
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
// place I did", not merely "is it gitignored" (TierLocal and the derived half
// of TierDerived are BOTH gitignored; only asking a clone tells them apart).
type Tier uint8

const (
	// TierCommitted paths are checked in: a clone has them, byte for byte.
	TierCommitted Tier = iota
	// TierDerived paths are gitignored but REBUILT by a named command from
	// committed pins (a lockfile, a remote) — deleting one only costs the time
	// to re-run that command.
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

// Entry is one classified .ctxloom path, as Layout enumerates them.
type Entry struct {
	// Rel is the path relative to appPath, e.g. ".ctxloom/cache/bundles".
	Rel  string
	Tier Tier
	// Rebuild names the command that reconstructs this path from committed
	// pins. Empty if and only if Tier is TierLocal — see Tier's doc.
	Rebuild string
	// Lost is TierLocal-only: what a clone does not have, in a user's words —
	// the text doctor's absent-TierLocal-entry check surfaces.
	Lost string
}

// Layout is the classification of every path this tree's own writers produce
// under .ctxloom, each appearing exactly once — the table the
// config-layer-scope design doc's ".ctxloom classification" section derived by
// hand, given a name so a doctor check (and any future arch test) has
// something to walk instead of re-deriving it by inspection every time.
func Layout() []Entry {
	return []Entry{
		{Rel: filepath.Join(AppDirName, ConfigFileName+".yaml"), Tier: TierCommitted},
		{Rel: filepath.Join(AppDirName, RemotesFileName+".yaml"), Tier: TierCommitted},
		{Rel: filepath.Join(AppDirName, LockFileName+".yaml"), Tier: TierDerived, Rebuild: "ctxloom remote lock"},
		{Rel: filepath.Join(AppDirName, ContentDir), Tier: TierCommitted},
		{Rel: filepath.Join(AppDirName, ProfilesDir), Tier: TierCommitted},
		{Rel: filepath.Join(AppDirName, AgentsDir), Tier: TierCommitted},
		{Rel: filepath.Join(AppDirName, AllowedSignersFileName), Tier: TierCommitted},
		{Rel: filepath.Join(AppDirName, DistrustedSignersFileName), Tier: TierCommitted},
		{Rel: filepath.Join(AppDirName, ApprovalsDirName), Tier: TierCommitted},
		{Rel: filepath.Join(AppDirName, CacheDir, BundlesDir), Tier: TierDerived, Rebuild: "ctxloom deps pull"},
		{Rel: filepath.Join(AppDirName, CacheDir, ReposCacheDir), Tier: TierDerived, Rebuild: "ctxloom deps pull"},
		{Rel: filepath.Join(AppDirName, CacheDir, RefusedAdvancesFileName+".yaml"), Tier: TierDerived, Rebuild: "ctxloom deps upgrade"},
		{
			Rel: filepath.Join(AppDirName, CacheDir, TrustFileName, TrustObjectsDir), Tier: TierLocal,
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
		{
			Rel: filepath.Join(AppDirName, StateDir, EnginesDir), Tier: TierLocal,
			Lost: "the project-scoped config homes ctxloom points relocated engines at (codex's CODEX_HOME on every axis; claude-code's CLAUDE_CONFIG_DIR and kiro's KIRO_HOME on in-tree agent runs — see EngineStateHome): the credentials seeded from this machine's host login, plus whatever the user hand-edited into the engine's own config there. Nothing rebuilds it; a fresh clone re-seeds from the host on the next run, and any hand-edits are simply gone",
		},
	}
}
