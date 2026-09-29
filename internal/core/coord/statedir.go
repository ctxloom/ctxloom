package coord

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// State layout (all 0700 dirs / 0600 files — journals carry message bodies
// and credential hashes):
//
//	~/.ctxloom/coord/<project-key>/
//	    owner.lock         exclusive-owner lock, a kernel file lock held for
//	                       the owner's lifetime (single writer per journal
//	                       is per PROCESS too — see claimOwner)
//	    owner.json         the holder's stamp: pid, session, mode, start —
//	                       for display and the orphan test, never liveness
//	    runs.jsonl         run registry / spawn queue / roster journal
//	    mailbox.jsonl      role mailboxes + consume cursors
//	    interactions.jsonl audit journal (no projection)
//	    endpoint.json      last-bound ports, re-bound on relaunch so
//	                       adopted children re-Hello a stable endpoint
//
// The "coord" segment and the per-project directory it composes both live in
// internal/core/paths (CoordDirName, CoordProjectStateDir) — the declarative
// source of truth for ctxloom path segments (docs/architecture/core/
// paths.md). discover, the layout owner for endpoint.json's SHAPE (see its
// package doc), globs this same directory via paths.HomeCoordDir.
const coordDirName = paths.CoordDirName

// OwnerLockFileName is the exclusive-owner lock's file name inside a project
// state dir. The file persists across owners: whether it is LOCKED is the
// ownership fact (ProbeOwner), its presence only says a claim was once made.
const OwnerLockFileName = "owner.lock"

// ProjectStateDir resolves a project's coordinator state dir without creating
// or claiming it: the stable project id when one resolved, otherwise a key
// derived from projectDir. It is the dir New claims and ProbeOwner reads.
func ProjectStateDir(projectID, projectDir string) (string, error) {
	return stateDirPath(projectKey(projectID, projectDir))
}

// projectKey is the state-dir key for a project: its id, or a path-derived
// key when no id resolved.
func projectKey(projectID, projectDir string) string {
	if projectID != "" {
		return projectID
	}
	return pathDerivedProjectKey(projectDir)
}

func stateDirPath(projectKey string) (string, error) {
	dir, err := paths.CoordProjectStateDir(sanitizeKey(projectKey))
	if err != nil {
		return "", fmt.Errorf("coord: state dir: %w", err)
	}
	return dir, nil
}

// stateDirForProject resolves and creates the coordinator state dir, keyed by
// project (plan: durability first, keyed by project — a fresh `ctxloom run`
// adopts orphaned state from disk).
func stateDirForProject(projectKey string) (string, error) {
	dir, err := stateDirPath(projectKey)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("coord: state dir: %w", err)
	}
	return dir, nil
}

// pathDerivedProjectKey is the fallback project key when no stable project
// identity resolves: the project's base name, disambiguated by a digest of its
// absolute path (two checkouts named "main" are two projects). Production takes
// this path whenever taskops cannot resolve a project identity.
func pathDerivedProjectKey(projectDir string) string {
	return filepath.Base(projectDir) + "-" + pathDigest(projectDir)[:pathDigestKeyLen]
}

// pathDigestKeyLen is how much of the digest the key carries — short enough to
// keep the directory name readable, long enough that a collision between two
// checkout paths is not a practical concern.
const pathDigestKeyLen = 12

// pathDigest hashes a filesystem path for use in a state-dir NAME.
//
// Deliberately its own function rather than creds.go's hashToken, whose doc
// binds it to a different contract entirely ("the persisted form of a bearer
// token"). Sharing one function coupled the on-disk layout to the credential
// format: hardening the token hash — a security change, made for security
// reasons — would silently rename every project's state dir and orphan the
// journals inside it. The algorithm is the same today; the point is that the
// two can now move independently, and pathDerivedProjectKey's golden test
// notices if this one does.
func pathDigest(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])
}

// defaultProjectKey is the state-dir segment a key that names no usable
// project identity resolves to.
const defaultProjectKey = "default"

// sanitizeKey makes a project key filesystem-safe as ONE path segment.
//
// A key that reduces to a DOT-ONLY segment is not a path segment at all: it
// resolves stateDirForProject to the coord root itself, so every project taking
// that key would share one set of journals AND collide with the per-project
// directories discover.List globs out of that same root. Separator mapping
// alone does not catch it — ".." is replaced but a bare "." is not — so the
// RESULT is checked, and anything that is only dots falls back to the same
// bucket an empty key gets.
func sanitizeKey(k string) string {
	if k == "" {
		return defaultProjectKey
	}
	repl := strings.NewReplacer("/", "-", "\\", "-", ":", "-", "..", "-")
	out := repl.Replace(k)
	if strings.Trim(out, ".") == "" {
		return defaultProjectKey
	}
	return out
}
