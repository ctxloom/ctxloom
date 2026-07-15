// Package paths provides shared path constants for ctxloom.
package paths

import (
	"os"
	"path/filepath"
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

	// VendorDir is the subdirectory for vendored dependencies.
	VendorDir = "vendor"

	// ContextDir is the subdirectory for context files.
	ContextDir = "context"

	// RemotesFileName is the name of the remotes file (without extension).
	RemotesFileName = "remotes"

	// TrustFileName is the name of the per-item trust store file (without
	// extension). It is a committed/shared persistent item, sitting at the
	// .ctxloom root next to remotes.yaml (NOT under a nested persistent/ dir,
	// matching the rest of ctxloom's persistent items — see paths_test.go).
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

	// RepoContentPrefix is the repo-relative path prefix under which a remote
	// repo's authored content lives: .ctxloom/content/<kind>/<name>.yaml. It is
	// the canonical (non-local) counterpart to LocalPath — every fetcher/
	// publisher that builds a repo-relative content path routes through this
	// constant so there is exactly one on-disk layout, not a scattered literal.
	RepoContentPrefix = ".ctxloom/content"

	// MemoryDir is the subdirectory for memory/session files.
	MemoryDir = "memory"

	// ReposCacheDir is the subdirectory for cached git repo clones.
	ReposCacheDir = "repos"

	// SessionsDir is the subdirectory for per-session state (index, harp dirs).
	SessionsDir = "sessions"

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
)

// HomeSessionsDir returns ~/.ctxloom/sessions — the home-rooted directory
// that holds the session index and per-harp session dirs. This is the
// single source of truth for the sessions root; both the task store and the
// memory compactor resolve harp paths through it so they cannot diverge.
func HomeSessionsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, AppDirName, SessionsDir), nil
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
func HarpDir(harp string) (string, error) {
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

// ProjectSessionsDir returns the project-rooted directory holding distilled
// session .md files. It is distinct from the home-rooted HomeSessionsDir: this
// is per-project state under the app dir. Resolution prefers the configured app
// dir, then <cwd>/.ctxloom/sessions, then a bare relative .ctxloom/sessions when
// even the working directory can't be resolved.
func ProjectSessionsDir(appDir string) string {
	if appDir != "" {
		return filepath.Join(appDir, SessionsDir)
	}
	if wd, err := os.Getwd(); err == nil {
		return filepath.Join(wd, AppDirName, SessionsDir)
	}
	return filepath.Join(AppDirName, SessionsDir)
}

// HarpPlanPath returns ~/.ctxloom/sessions/<harp>/<name>.plan.md — a plan
// document for a harp-named session. Plans sit directly in the session
// directory next to tasks.md; name is the descriptive base (no extension),
// and a session may have several distinctly named plans.
func HarpPlanPath(harp, name string) (string, error) {
	dir, err := HarpDir(harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+PlanFileExt), nil
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
		return "", err
	}
	return filepath.Join(home, AppDirName, CacheDir, "triggers"), nil
}

// GetCacheDir returns the cache subdirectory path for the given app path.
// Cache contains regeneratable content: bundles, vendor, context, memory.
func GetCacheDir(appPath string) string {
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
// store directory, at appPath root next to allowed_signers.yaml. "Our team's
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
		return "", err
	}
	return filepath.Join(home, AppDirName, ApprovalsDirName), nil
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
		return "", err
	}
	return filepath.Join(home, AppDirName, AllowedSignersFileName), nil
}

// DistrustedSignersPath returns the path to the LOCAL embedded-key suppression
// record (at appPath root, next to allowed_signers): the negative counterpart
// to it (oozy-plod (b)). allowed_signers is purely additive — there is no way
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
		return "", err
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
	return filepath.Join(GetCacheDir(appPath), BundlesDir)
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

// VendorPath returns the path to the vendor directory (under cache/).
func VendorPath(appPath string) string {
	return filepath.Join(GetCacheDir(appPath), VendorDir)
}

// ContextPath returns the path to the context directory (under cache/).
func ContextPath(appPath string) string {
	return filepath.Join(GetCacheDir(appPath), ContextDir)
}

// MemoryPath returns the path to the memory directory (under cache/).
func MemoryPath(appPath string) string {
	return filepath.Join(GetCacheDir(appPath), MemoryDir)
}

// ReposCachePath returns the path to the repos cache directory (under cache/).
func ReposCachePath(appPath string) string {
	return filepath.Join(GetCacheDir(appPath), ReposCacheDir)
}

// TrustObjectsPath returns the approved-content snapshot directory (under
// cache/): content-addressed copies of the bytes a human approved at review,
// keyed by a payload hash. The review porcelain diffs an UPDATE against them.
// Pure cache: deleting it only degrades update review from a diff to a
// full-content display (the countersignature stores stay authoritative).
func TrustObjectsPath(appPath string) string {
	return filepath.Join(GetCacheDir(appPath), TrustFileName, "objects")
}

// DefaultAppDir returns the default app directory path relative to current directory.
func DefaultAppDir() string {
	return AppDirName
}

// DefaultRemotesPath returns the default remotes path relative to current directory.
func DefaultRemotesPath() string {
	return RemotesPath(AppDirName)
}

// DefaultLockPath returns the default lock path relative to current directory.
func DefaultLockPath() string {
	return LockPath(AppDirName)
}

// DefaultVendorPath returns the default vendor path relative to current directory.
func DefaultVendorPath() string {
	return VendorPath(AppDirName)
}
