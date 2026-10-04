// Package paths provides shared path constants for ctxloom.
package paths

import (
	"fmt"
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

	// GitignoreFileName is the name of the ignore file ctxloom owns INSIDE the
	// .ctxloom directory (internal/adapters/gitignore's EnsureNested writes it). Unlike
	// every other generated path here it is TierCommitted on purpose: tracking
	// it is what carries the private-state rules into clones and into linked
	// worktrees, which a rule living only in the superproject's root .gitignore
	// would never reach.
	GitignoreFileName = ".gitignore"

	// DistrustedSignersFileName is the name of the LOCAL embedded-key
	// suppression record (without extension) — see DistrustedSignersPath. A
	// plain one-principal-per-line list, deliberately NOT the OpenSSH
	// allowed_signers format: this store asserts no trust of its own, only a
	// negative record the trust root (configload) subtracts from the embedded root.
	DistrustedSignersFileName = "distrusted_signers"

	// ApprovalsDirName is the name of the countersignature store directory
	// (signature-envelope spec §9.2): one armored .sig file per approve/reject
	// countersignature. It replaces trust.yaml as the review-decision record —
	// the signature IS the approval, not a row a plain-file write can forge.
	// Two physical stores share this name, at different roots: the user store
	// (~/.ctxloom/approvals, personal) and the project store (.ctxloom/approvals,
	// committable) — see HomeApprovalsPath / ApprovalsPath.
	ApprovalsDirName = "approvals"

	// ApprovalsPlaceholderName is the empty file `ctxloom init` writes into the
	// project approvals store. Git does not track an empty directory, so
	// without it a project that has recorded no decision yet would arrive in a
	// fresh clone with no store at all — and an absent project store withholds
	// everything, because absence is indistinguishable from a store that went
	// away (countersign.Store.Readable).
	ApprovalsPlaceholderName = ".gitkeep"

	// LockFileName is the name of the lock file (without extension).
	LockFileName = "lock"

	// ProfilesDir is the directory a bundle tree keeps its profile items in
	// (<bundle>/profiles/<name>.yaml). Directly under an app directory it is
	// the RETIRED standalone profiles location (ProfilesPath), which loading
	// refuses rather than reads.
	ProfilesDir = "profiles"

	// ProjectBundleName is the reserved name of the PROJECT BUNDLE: the local
	// bundle holding a project's own profiles. A selector-less profile name
	// resolves to this bundle's profile of that name, and home uses the same
	// rule under its own app directory.
	ProjectBundleName = "project"

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
	// one loose — each looking like it belonged to one of the other two. A file
	// under here is a fact about THIS checkout on THIS machine that must never
	// be committed (a clone would arrive carrying somebody else's answer) and
	// that a cache wipe or a `deps pull` cannot regenerate — see Tier's doc
	// for why that distinction, not mere gitignore status, is what earns a path
	// a place in this directory instead of cache/.
	StateDir = "state"

	// SessionEngineHomesDirName is the session-dir member holding the
	// session's engine config-home INSTANCES — see HarpSessionEngineHomes.
	// Every engine's leaf hangs off this one directory rather than each
	// engine owning a sibling of it, because the leaves are pairwise
	// distinct by construction (their HomeVar.Subdir) and one root per
	// session is what makes each instance disposable as a unit.
	SessionEngineHomesDirName = "home"

	// ContextCacheDir is the CacheDir subdirectory holding assembled context
	// files, one per content hash (agent.WriteContextFile). The leaf lived in
	// internal/core/agent because this package had no helper left for it;
	// it belongs here, with every other .ctxloom segment, so Layout can
	// classify the directory without either side inventing the name twice.
	ContextCacheDir = "context"

	// CompanionPinCacheDir is the CacheDir subdirectory holding the admitted
	// companions' verified bytes and signatures, one directory per admitted
	// set's digest (companions.PinAdmittedCompanions) — what a host launch puts
	// first on the engine's PATH.
	CompanionPinCacheDir = "companions"

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
	// DirtyTreeCommitAckPath. It moved out of config.yaml: a config value the env layer or
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

	// IndexFileName is the name the RETIRED global session index was kept
	// under at the sessions root. Nothing reads or writes it any more; it is
	// named so the walkers over the sessions root know the file for what it
	// is.
	IndexFileName = "index.yaml"

	// MigratedIndexFileName is the name an older binary's index migration
	// left a consumed index.yaml under. Nothing reads it; it is named for
	// the same reason IndexFileName is.
	MigratedIndexFileName = "index.yaml.migrated"

	// SessionSidecarFileName is the per-session record at the top level of
	// each ~/.ctxloom/sessions/<harp>/: the facts a session directory cannot
	// recover from its own contents (which project launched it, which engine
	// owns it, its bound and rotated session ids, when it was purged). A
	// directory under the sessions root IS a session exactly when it carries
	// this file — sessions.IsSessionDir is the one predicate for that.
	SessionSidecarFileName = "session.yaml"

	// SessionKeepMarkerFileName is the hand-placed exemption from the aged
	// session sweep (operations.SweepSessions): a plain file of this
	// name at the top level of ~/.ctxloom/sessions/<harp>/ takes the whole
	// session out of every scope of that sweep. Its contents are ignored;
	// its presence is the decision. Named as a word rather than a dotfile
	// so it is visible in a plain listing — the person who placed it is
	// the one who will later wonder why the session was never reclaimed.
	SessionKeepMarkerFileName = "keep"

	// EssenceFileName is the name of a harp's distilled session essence, at
	// the top of the session's OUTPUT dir (sessions.Entry.OutputDir): it is a
	// readable output, so it lives in the human root, never the machine tree.
	EssenceFileName = "essence.md"

	// NextStepFileName is the name of a harp's captured next step: what the
	// agent said it was about to do, written by the TurnEnd hook and
	// OVERWRITTEN every turn, so the file holds the LAST turn's statement.
	//
	// It sits beside EssenceFileName because it is consumed with it: the
	// essence is what the session was, this is what it intended next, and
	// distillation reads the second to decide what to keep of the first.
	NextStepFileName = "next-step.md"

	// PlanFileExt is the suffix for a session's plan documents. Plans live
	// at the top of the session's OUTPUT dir as <descriptive-name>.plan.md
	// files; a session may hold several.
	PlanFileExt = ".plan.md"

	// ScratchDirName is the per-session subdirectory for PER-RUN scratch only:
	// isolation scratch roots (ctxloom-iso-*), toolchain temp dirs
	// (ctxloom-tmp-*) and the disk fallback for secret dirs
	// (ctxloom-secret-*). Each run removes its own at Cleanup; Close and the
	// session sweep are the backstop for a run that died.
	ScratchDirName = "scratch"

	// WorkDirName is the per-session subdirectory holding the session's
	// worktree checkouts (ctxloom-wt-*). Unlike scratch it can hold the only
	// copy of an agent's work, so it is triaged, never removed blind.
	WorkDirName = "work"

	// NativeDirName is the per-session subdirectory holding each engine's
	// NATIVE conversation history, <harp>/native/<engine leaf>/, reached from
	// the disposable engine home through a relative symlink (the engine's
	// engine.HomeSpec.TranscriptStoreRel). It sits beside home/ at the same
	// depth so the one relative link resolves on the host and in a container
	// that mounts home/<leaf> and native/ as siblings.
	NativeDirName = "native"

	// PackageDirName is the per-session subdirectory the claim store stows a
	// launch's encoded package under, content-addressed by its digest
	// (<harp>/package/<digest>).
	PackageDirName = "package"

	// SpoolDirName is the per-session subdirectory holding the session's mail
	// spool (internal/core/spool): a member a containerized run MUST reach,
	// which is why its HarpMembers row is Mounted.
	SpoolDirName = "spool"

	// TranscriptsDirName is the per-session subdirectory holding ctxloom's
	// RAW transcript forms: the canonical transcript.jsonl and the
	// per-rotation segment .jsonl files. Machine data, so machine-side: the
	// readable distillations of them live in the output dir.
	TranscriptsDirName = "transcripts"

	// CanonicalTranscriptFileName is the transcripts/ leaf holding ctxloom's
	// OWN captured transcript (internal/adapters/transcript.Recorder's
	// output): one JSONL line per agent.ChatEvent, engine-agnostic. The file
	// is fed by every structured/ACP engine AND the oneshot regime
	// (transcript.RecordOneshot) AND the vendor readers
	// (internal/adapters/transcript/vendorreader/*), so its name carries no
	// engine or protocol. This is the ONLY name a canonical transcript is read
	// or written under.
	CanonicalTranscriptFileName = "transcript.jsonl"

	// DiagnosticsLogFileName is the per-session log a terminal-UI session
	// diverts its clidiag warnings to while the TUI owns stderr.
	DiagnosticsLogFileName = "diagnostics.log"

	// ContextMetricsFileName is the per-session context-occupancy series
	// (internal/adapters/contextmetrics): one JSON object per line,
	// append-only, oldest first.
	ContextMetricsFileName = "context-metrics.jsonl"

	// OutputDirName is the leaf of the default output base:
	// <Documents>/ctxloom. A session's output dir is
	// <base>/<project>/<harp>/ (DefaultOutputBase, sessions.Entry.OutputDir).
	OutputDirName = "ctxloom"

	// CoordDirName is the per-user directory holding in-process coordinator
	// state: ~/.ctxloom/coord/<project-key>/<root-harp>/ (owner lock,
	// run/mailbox/interaction journals, last-bound endpoint) — see
	// HomeCoordDir / CoordRootStateDir. One project holds one ROOT per
	// independent coordinator tree, keyed by the harp of the session that
	// founded it; the project key is resolved outside this package
	// (internal/core/coord.RootStateDir).
	CoordDirName = "coord"

	// CoordEndpointFileName is the discovery file inside a coordinator root's
	// state dir (~/.ctxloom/coord/<project-key>/<root-harp>/endpoint.json):
	// the ports a coordinator last bound, re-minted every Serve() so a
	// relaunched coordinator re-binds the SAME endpoint and a separate CLI
	// invocation (internal/adapters/coordgrpc/discover.List) can find it. 0600 and
	// host-local — it also carries the read-only consumer credential.
	CoordEndpointFileName = "endpoint.json"

	// SegmentsDirName holds per-rotation artifacts, one pair per displaced
	// session ID in a harp's sessions.Entry.Rotations lineage, split by kind:
	// the converted-once canonical <sessionID>.jsonl (ResolveHarpSegmentPath)
	// and its watermark are machine data under transcripts/segments/; that
	// rotation's distilled <sessionID>.md is a readable output under the
	// output dir's segments/ (OutputSegmentEssencePath).
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
	// from which patch (see internal/adapters/cli/util_config_write.go's record
	// builder). It is the audit trail distinct-bullpen's "config-write has
	// no recovery path for a foreign file" asked for, to the degree hew's
	// v0 library currently supports (a full `hew revert` is future work per
	// the spec's §9.7, not built here). Siblings HomeLocksDirName under
	// RootHome for the same reason: a record about a FOREIGN file — one
	// ctxloom does not own and so must never write ctxloom-internal state
	// beside — belongs in ctxloom's own home tree, not next to the file it
	// describes.
	HomeRecordsDirName = "records"
)

// HomeSessionsDir returns ~/.ctxloom/sessions — the home-rooted directory
// that holds the per-harp session dirs. This is the
// single source of truth for the sessions root; both the task store and the
// memory compactor resolve harp paths through it so they cannot diverge.
// HomeConfigDir returns the user's home ctxloom directory (~/.ctxloom).
//
// This is the STAGE-1 bootstrap primitive for the HOME side of value
// layering (home < project < env < CLI — see internal/shared/confload):
// config.resolveConfigLayerPaths calls it to find home's config.yaml so
// loadLayeredConfig can read it as the lower-precedence layer underneath
// whatever project config.yaml findAppDir resolved. It does no filesystem
// I/O itself (a pure path join, like the Home* helpers below) — callers
// decide whether/how to read what's there.
func HomeConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, AppDirName), nil
}

// homeUnderErrFormat is the shape EVERY Home*/cache accessor's resolution
// failure takes, and the store descriptions below are the only part that
// varies. Both are constants for one reason: a test asserting this text must
// name the SAME string the code emits. A hand-copied expectation is a second
// copy of the message that goes on passing after the real one is reworded.
const homeUnderErrFormat = "resolve %s ~/%s/%s: %w"

// What each accessor calls the store it failed to resolve. The remedy for one
// of these errors is stated in terms of the store the caller wanted, never in
// terms of os.UserHomeDir.
const (
	whatHomeSessions      = "the home sessions root"
	whatHomeLogs          = "the home logs root"
	whatTriggerCache      = "the trigger verdict cache"
	whatHomeCoord         = "the coordinator state root"
	whatHomeLocks         = "the home lock directory"
	whatHomeApprovals     = "the user countersignature store"
	whatAllowedSigners    = "the user trust root"
	whatDistrustedSigners = "the user distrust record"
	whatHomeRecords       = "the home records directory"
	whatCompanionPin      = "the admitted-companion pin"
)

// homeUnder resolves ~/<AppDirName>/<segments...>, naming what failed in the
// caller's own terms.
//
// Every Home*/cache accessor below shares this body exactly; it is one function
// so they cannot drift apart. The `what` string is the caller's,
// because the remedy for a failure here is stated in terms of the store the
// caller wanted, not of os.UserHomeDir.
func homeUnder(what string, segments ...string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf(homeUnderErrFormat, what, AppDirName, filepath.Join(segments...), err)
	}
	return filepath.Join(append([]string{home, AppDirName}, segments...)...), nil
}

// HomeCompanionPinDir returns ~/.ctxloom/cache/companions — the store
// companions.PinAdmittedCompanions writes admitted companions into.
func HomeCompanionPinDir() (string, error) {
	return homeUnder(whatCompanionPin, CacheDir, CompanionPinCacheDir)
}

func HomeSessionsDir() (string, error) {
	return homeUnder(whatHomeSessions, SessionsDir)
}

// HomeLogsDir returns ~/.ctxloom/logs — where every ctxloom process writes its
// structured log. A pure path join like its neighbours here: the caller decides
// whether to create the directory (logsink.Open does, on the first write).
func HomeLogsDir() (string, error) {
	return homeUnder(whatHomeLogs, LogsDir)
}

// HomeLogFilePath returns ~/.ctxloom/logs/<prog>.log — the STRUCTURED
// logger's sink for the binary named prog. One file per binary, not one for
// the family: every ctxloom subcommand shares ctxloom.log (the entries carry the
// caller, so a hook, the MCP server and the CLI read as one timeline), while
// each companion binary keeps its own, so its record is found under its name.
//
// Deliberately NOT stderr: for hooks and the statusline command, stderr is a
// protocol surface the calling engine renders (Claude Code displays
// SessionStart hook stderr as an error, and statusline stderr lands on the
// terminal outside the alt-screen, destroying scrollback), so warn-level zap
// JSON there corrupts the user's session rather than informing anyone.
//
// Only the structured channel moves. Human-readable diagnostics (clidiag) stay
// on stderr for every command, hooks included: those are written FOR a person
// and say what to do about the problem, and a hook that did nothing must still
// be able to say so out loud rather than swallow it.
func HomeLogFilePath(prog string) (string, error) {
	dir, err := HomeLogsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, prog+".log"), nil
}

// HarpSidecarPath returns ~/.ctxloom/sessions/<harp>/session.yaml — the
// session's own record (SessionSidecarFileName).
func HarpSidecarPath(harp string) (string, error) {
	dir, err := HarpDir(harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, SessionSidecarFileName), nil
}

// HarpSidecarLockPath returns the cooperative lock every sidecar mutation
// takes: ~/.ctxloom/sessions/<harp>.session.lock. It sits BESIDE the harp
// dir for the reason HarpLockPath gives, and it is a DIFFERENT file from
// HarpLockPath because that one is the session's liveness lock: a probe that
// found it held would read a millisecond sidecar write as a running session.
func HarpSidecarLockPath(harp string) (string, error) {
	dir, err := HarpDir(harp)
	if err != nil {
		return "", err
	}
	return PathFor(dir + ".session"), nil
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

// harpMember is <harp dir>/<name>, through HarpDir's validation.
func harpMember(harp, name string) (string, error) {
	dir, err := HarpDir(harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// HarpScratchDir returns ~/.ctxloom/sessions/<harp>/scratch — the session's
// per-run scratch root (see ScratchDirName).
func HarpScratchDir(harp string) (string, error) { return harpMember(harp, ScratchDirName) }

// HarpWorkDir returns ~/.ctxloom/sessions/<harp>/work — where the session's
// worktree checkouts live (see WorkDirName).
func HarpWorkDir(harp string) (string, error) { return harpMember(harp, WorkDirName) }

// HarpNativeDir returns ~/.ctxloom/sessions/<harp>/native — the root of every
// engine's native conversation history for the session (see NativeDirName).
func HarpNativeDir(harp string) (string, error) { return harpMember(harp, NativeDirName) }

// HarpTranscriptsDir returns ~/.ctxloom/sessions/<harp>/transcripts — the raw
// transcript forms (see TranscriptsDirName).
func HarpTranscriptsDir(harp string) (string, error) { return harpMember(harp, TranscriptsDirName) }

// HarpDiagnosticsLogPath returns ~/.ctxloom/sessions/<harp>/diagnostics.log.
func HarpDiagnosticsLogPath(harp string) (string, error) {
	return harpMember(harp, DiagnosticsLogFileName)
}

// HarpContextMetricsPath returns ~/.ctxloom/sessions/<harp>/context-metrics.jsonl.
func HarpContextMetricsPath(harp string) (string, error) {
	return harpMember(harp, ContextMetricsFileName)
}

// HarpSessionEngineHomes returns ~/.ctxloom/sessions/<harp>/home — the
// session's engine config-home CONTAINER (SessionEngineHomesDirName). Each
// engine appends its own leaf below this root to get its own config-home
// INSTANCE (the session home), distinct by construction, so one container
// hosts every engine a session runs without collision.
//
// Each instance under it is created at session-creation time and is
// disposable: everything in it is either regenerated by ctxloom's managed
// writers, synthesized by the engine packages, or one-way COPIED IN from the
// user's real host home. The durable truth stays the real host home, which
// ctxloom never writes.
//
// Rides HarpDir, so an empty or traversing harp is an error, never a fallback
// to some shared path — a shared fallback is precisely the durable per-project
// engine home the per-session instance model retired.
func HarpSessionEngineHomes(harp string) (string, error) {
	dir, err := HarpDir(harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, SessionEngineHomesDirName), nil
}

// HarpLockPath returns ~/.ctxloom/sessions/<harp>.lock — the session
// LIVENESS lock: the process that owns the session holds an exclusive lock
// on this file for as long as it runs, and a sweeper that can acquire it
// knows the owner is gone (see internal/shared/sessionlock).
//
// It is PathFor applied to the harp dir, so it sits BESIDE
// ~/.ctxloom/sessions/<harp>/ and never inside it. That placement is
// load-bearing, not tidiness: on Windows an open handle blocks deletion, so
// a lock file INSIDE the harp dir would stop the very sweep holding it from
// removing that dir. Beside it, the sweeper holds the lock while it reclaims
// and the harp dir remains removable.
func HarpLockPath(harp string) (string, error) {
	dir, err := HarpDir(harp)
	if err != nil {
		return "", err
	}
	return PathFor(dir), nil
}

// ResolveHarpSegmentsDir returns ~/.ctxloom/sessions/<harp>/transcripts/segments
// — the cache root for harp's per-rotation canonical segments (see
// SegmentsDirName). This is the single chokepoint every segment path is
// built from; a caller that needs one particular rotation's segment file
// should call ResolveHarpSegmentPath instead of hand-joining onto this.
func ResolveHarpSegmentsDir(harp string) (string, error) {
	dir, err := HarpTranscriptsDir(harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, SegmentsDirName), nil
}

// OutputSegmentEssencePath returns <outputDir>/segments/<sessionID>.md — the
// distilled essence of ONE rotation in a session's lineage. outputDir is the
// session's recorded output dir (sessions.Entry.OutputDir): the essence is a
// readable output, so it lives in the human root while the rotation's raw
// segment stays machine-side (ResolveHarpSegmentPath).
func OutputSegmentEssencePath(outputDir, sessionID string) string {
	return filepath.Join(outputDir, SegmentsDirName, sessionID+".md")
}

// ResolveHarpSegmentPath returns
// ~/.ctxloom/sessions/<harp>/transcripts/segments/<sessionID>.jsonl — the cached
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

// ResolveHarpSegmentWatermarkPath returns
// ~/.ctxloom/sessions/<harp>/transcripts/segments/<sessionID>.watermark.json — where a
// refresh of harp's canonical transcript resumes from while sessionID is the
// LIVE binding (operations.RefreshVendorTranscript). It sits beside the
// per-rotation segment caches because it is the same kind of thing: derived,
// keyed by the vendor session it was read from, and safe to delete — without
// it the next refresh converts in full.
func ResolveHarpSegmentWatermarkPath(harp, sessionID string) (string, error) {
	dir, err := ResolveHarpSegmentsDir(harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sessionID+".watermark.json"), nil
}

// HarpCanonicalTranscriptPath returns
// ~/.ctxloom/sessions/<harp>/transcripts/transcript.jsonl — the canonical,
// engine-agnostic transcript ctxloom captures itself (see
// CanonicalTranscriptFileName). Pure, like its neighbours: readers stat it
// themselves, and "no file" is their answer for "never captured".
func HarpCanonicalTranscriptPath(harp string) (string, error) {
	dir, err := HarpTranscriptsDir(harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, CanonicalTranscriptFileName), nil
}

// TriggerCacheDir returns ~/.ctxloom/cache/triggers — the home-rooted
// directory holding ctxloom's cached revive-trigger verdicts, one file per
// project (see internal/adapters/operations' verdict cache). It deliberately lives
// OUTSIDE any project tree and outside taskloom's own store
// (~/.ctxloom/tasks/<project-id>.jsonl, internal/shared/tasks/paths): a
// verdict cache is pure derived scratch, safe to delete at any time, and
// keeping it off both the repo and the task log means neither a git clone nor
// a `taskloom` operation can ever touch it.
func TriggerCacheDir() (string, error) {
	return homeUnder(whatTriggerCache, CacheDir, TriggersDir)
}

// HomeCoordDir returns ~/.ctxloom/coord — the per-user root holding every
// project's coordinator roots (see CoordDirName, CoordRootStateDir).
// internal/adapters/coordgrpc/discover.List globs two levels below this root
// for every root's endpoint.json.
func HomeCoordDir() (string, error) {
	return homeUnder(whatHomeCoord, CoordDirName)
}

// CoordRootStateDir returns ~/.ctxloom/coord/<projectKey>/<rootHarp> — one
// coordinator ROOT's state directory: the tree a session founded (or adopted)
// in that project. projectKey is assumed to already be a single safe path
// segment (internal/core/coord.sanitizeKey's job, not this package's: a
// coordinator project key is not a harp). rootHarp IS a harp, so it gets the
// same traversal validation HarpDir gives one.
func CoordRootStateDir(projectKey, rootHarp string) (string, error) {
	if err := harpid.Validate(rootHarp); err != nil {
		return "", err
	}
	dir, err := HomeCoordDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, projectKey, rootHarp), nil
}

// HomeLocksDir returns ~/.ctxloom/locks — the home-rooted directory holding
// advisory-lock sidecars for FOREIGN files a ctxloom-family binary does not
// own (see HomePathFor, lockpath.go, and HomeLocksDirName's doc).
//
// Guarded under a test binary: every foreign-file lock resolves through here,
// and a lock file outlives the run that took it, so an unsandboxed test
// package would leave one in the developer's real home per locked write. The
// guard is accountHomeError, not UnsandboxedHomeError, because a test may
// derive a container's lock path by pointing HOME at a home that is not this
// account's (see accountHomeError).
func HomeLocksDir() (string, error) {
	if override := homeLocksOverride.get(); override != "" {
		return override, nil
	}
	dir, err := homeUnder(whatHomeLocks, HomeLocksDirName)
	if err != nil {
		return "", err
	}
	if err := accountHomeError("home lock directory", dir,
		"testsupport.SandboxedMain / testsupport.Isolate, so HOME points at a temp root"); err != nil {
		return "", err
	}
	return dir, nil
}

// HomeRecordsDir returns ~/.ctxloom/records — the home-rooted directory
// holding hew §9.7 application records for FOREIGN files `util
// config-write` merges into (see HomeRecordsDirName's doc).
func HomeRecordsDir() (string, error) {
	if override := homeRecordsOverride.get(); override != "" {
		return override, nil
	}
	dir, err := homeUnder(whatHomeRecords, HomeRecordsDirName)
	if err != nil {
		return "", err
	}
	// Guarded for the same reason the approvals store is, and because this
	// store is where the omission was actually paid: a record names a FOREIGN
	// file by absolute path and outlives the run that wrote it, so a test
	// applying to its own temp dir still deposits a durable record in the
	// developer's real home. Found there: 1046 of them.
	if err := UnsandboxedHomeError("application-record store", dir,
		"testsupport.SandboxedMain / testsupport.Isolate, so HOME points at a temp root"); err != nil {
		return "", err
	}
	return dir, nil
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
	return homeUnder(whatHomeApprovals, ApprovalsDirName)
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
	return homeUnder(whatAllowedSigners, AllowedSignersFileName)
}

// DistrustedSignersPath returns the path to the LOCAL embedded-key suppression
// record (at appPath root, next to allowed_signers): the negative counterpart
// to it. allowed_signers is purely additive — there is no way
// to write a "no longer trust this key" entry into it — so a distrusted
// embedded principal is recorded HERE instead, one principal per line, and
// the configload trust root (signerFiles.trustStore) subtracts any embedded entry matching a
// line in this file before unioning the trust root. It never edits
// allowed_signers itself, and it can never remove a key that isn't ctxloom's
// own compiled-in one — `signer untrust` only writes here when the principal
// named matches an embedded entry (see operations.RemoveSigner).
func DistrustedSignersPath(appPath string) string {
	return filepath.Join(appPath, DistrustedSignersFileName)
}

// HomeDistrustedSignersPath returns ~/.ctxloom/distrusted_signers — the
// user-scoped counterpart to DistrustedSignersPath, mirroring
// HomeAllowedSignersPath (follows the developer across every project).
func HomeDistrustedSignersPath() (string, error) {
	return homeUnder(whatDistrustedSigners, DistrustedSignersFileName)
}

// LockPath returns the path to the lock file (at appPath root).
func LockPath(appPath string) string {
	return filepath.Join(appPath, LockFileName+".yaml")
}

// ProfilesPath returns the RETIRED standalone profiles directory (at appPath
// root). Nothing reads profiles from it; config refuses to load while it
// exists, naming the move into the project bundle.
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
// (.ctxloom/content/bundles). It is the PARENT of the per-format roots, not a
// directory any bundle lives directly in: each format occupies a sibling
// subtree beneath it (LocalBundlesPathFor), and localFSReader.searchRoots
// expands this parent into exactly those siblings — so nothing ever reads the
// parent itself.
//
// That makes it a SEARCH ROOT and nothing else. Pass it to a layered reader,
// which resolves the formats below it. A caller that WRITES a bundle must name
// the format root instead — LocalBundlesPathFor(appPath, layout) — because a
// file written directly here lands where no reader looks: it does not fail, it
// is silently never found.
//
// It is the local half of the same layout a remote repo exposes under
// RepoContentPrefix, so a bundle repo and a consuming project lay their
// bundles out identically; RepoBundlesPrefixFor is the publishing half.
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
// internal/adapters/operations' snapshot store — so the retired location is named once,
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
// gitignored, unrebuildable checkout state — see StateDir's doc. It holds
// project-local residents only (locks, the dirty-tree acknowledgement, trust
// objects); a session's members live under HarpDir.
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

// DirtyTreeCommitAckPath returns the record that a human authorized ctxloom to
// commit on their behalf in THIS checkout (see DirtyTreeCommitAckFileName). It
// is an internal/shared/admission.Store file, recording "may ctxloom act here
// without asking again". It is PROJECT-scoped (a fact about one checkout's
// branch, not the user), so it lives under appPath/state rather than the home
// directory.
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
// under .ctxloom, each appearing exactly once — a classification once derived
// by hand, given a name so a doctor check (and any future arch test) has
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
		{Rel: filepath.Join(AppDirName, GitignoreFileName), Tier: TierCommitted},
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
		// The admitted companions a host launch puts first on the engine's
		// PATH (companions.PinAdmittedCompanions). Content-addressed by the
		// admitted set, so sessions share one copy; every launch re-verifies
		// and rewrites whatever is missing.
		{
			Rel: filepath.Join(AppDirName, CacheDir, CompanionPinCacheDir), Tier: TierDerived,
			Rebuild: "ctxloom run (every host launch re-pins the admitted companions)",
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
		// No row is per-SESSION: a session's members (HarpMembers) live under
		// the home-rooted sessions store below, and a row cannot name a harp
		// that does not exist yet anyway. TestArch_LayoutHasNoHarpKeyedRows
		// keeps that true.

		// --- RootHome: the home-rooted stores, added by C13 (fs-consolidation
		// plan) so doctor can finally see them. Each names a STORE ROOT only —
		// never a harp- or project-key-keyed subpath (HarpDir, CoordRootStateDir
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
