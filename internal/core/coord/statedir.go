package coord

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	harpid "github.com/ctxloom/ctxloom/internal/shared/harp"
	"github.com/ctxloom/ctxloom/internal/shared/owneronly"
)

// State layout (all 0700 dirs / 0600 files — journals carry message bodies
// and credential hashes):
//
//	~/.ctxloom/coord/<project-key>/<root-harp>/
//	    owner.lock         exclusive-owner lock, a kernel file lock held for
//	                       the owner's lifetime (single writer per journal
//	                       is per PROCESS too — see claimOwner)
//	    owner.json         the holder's stamp: pid, session, mode, start —
//	                       for display and the orphan test, never liveness
//	    runs.jsonl         run registry / spawn queue / roster journal
//	    items.jsonl        plane-1 item events (counted, not materialized)
//	    interactions.jsonl audit journal (no projection)
//	    endpoint.json      last-bound ports, re-bound on relaunch so
//	                       adopted children re-Hello a stable endpoint
//
// One project holds one ROOT per independent coordinator tree. A root is
// named by the harp of the session that founded it and is adopted, on
// resume, by whichever process names that harp (Options.RootHarp). Roots
// share no mutable state: two sessions in one project are two trees, each
// with its own journals, spool reactor, roster and lifetime.
//
// The "coord" segment and the per-root directory it composes both live in
// internal/core/paths (CoordDirName, CoordRootStateDir) — the declarative
// source of truth for ctxloom path segments (docs/architecture/core/
// paths.md). discover, the layout owner for endpoint.json's SHAPE (see its
// package doc), globs these same directories via paths.HomeCoordDir.
const coordDirName = paths.CoordDirName

// OwnerLockFileName is the exclusive-owner lock's file name inside a root
// state dir. The file persists across owners: whether it is LOCKED is the
// ownership fact (ProbeOwner), its presence only says a claim was once made —
// which is also what makes a directory a root (ListRoots).
const OwnerLockFileName = "owner.lock"

// RootStateDir resolves one coordinator root's state dir without creating or
// claiming it: the project's key (its stable id when one resolved, otherwise
// a key derived from projectDir) and, under it, the root's harp. It is the
// dir New claims for Options.RootHarp and ProbeOwner reads.
func RootStateDir(projectID, projectDir, rootHarp string) (string, error) {
	dir, err := paths.CoordRootStateDir(sanitizeKey(projectKey(projectID, projectDir)), rootHarp)
	if err != nil {
		return "", fmt.Errorf("coord: state dir: %w", err)
	}
	return dir, nil
}

// projectKey is the state-dir key for a project: its id, or a path-derived
// key when no id resolved.
func projectKey(projectID, projectDir string) string {
	if projectID != "" {
		return projectID
	}
	return pathDerivedProjectKey(projectDir)
}

// ensureRootStateDir resolves and creates a root's state dir.
func ensureRootStateDir(projectID, projectDir, rootHarp string) (string, error) {
	dir, err := RootStateDir(projectID, projectDir, rootHarp)
	if err != nil {
		return "", err
	}
	if err := owneronly.EnsureDir(dir); err != nil {
		return "", fmt.Errorf("coord: state dir: %w", err)
	}
	return dir, nil
}

// RootStatus is one coordinator root of a project, as a probe sees it.
type RootStatus struct {
	// Dir is the root's state dir; RootHarp the harp it is named by.
	Dir, RootHarp string
	// Owner is ProbeOwner's answer for Dir: a live holder, a provably
	// abandoned one (Owner.Orphan), or none (Owner.Held false) — a root its
	// owner left with runs that had not ended, which `ctxloom run --session
	// <RootHarp>` adopts.
	Owner OwnerStatus
}

// ListRoots reports every coordinator root of a project, in harp order,
// without claiming any. A root is a directory a coordinator claimed — one
// carrying an owner lock file — so anything else under the project (a
// directory never claimed, a stray file) is not listed. A project no
// coordinator ever stood up in has no roots, which is not an error. A root
// whose owner cannot be probed is left out and its failure joined into the
// error; the roots that could be probed are returned beside it.
func ListRoots(projectID, projectDir string) ([]RootStatus, error) {
	home, err := paths.HomeCoordDir()
	if err != nil {
		return nil, fmt.Errorf("coord: list roots: %w", err)
	}
	parent := filepath.Join(home, sanitizeKey(projectKey(projectID, projectDir)))
	entries, err := os.ReadDir(parent)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("coord: list roots: %w", err)
	}
	var (
		out  []RootStatus
		errs []error
	)
	for _, e := range entries {
		if !e.IsDir() || harpid.Validate(e.Name()) != nil {
			continue
		}
		dir := filepath.Join(parent, e.Name())
		if _, err := os.Lstat(filepath.Join(dir, OwnerLockFileName)); err != nil {
			continue
		}
		st, err := ProbeOwner(dir)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, RootStatus{Dir: dir, RootHarp: e.Name(), Owner: st})
	}
	return out, errors.Join(errs...)
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
// resolves a project's roots to the coord root itself, so every project taking
// that key would share one set of roots AND collide with the per-project
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
