package coord

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/pidalive"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// State layout (all 0700 dirs / 0600 files — journals carry message bodies
// and credential hashes):
//
//	~/.ctxloom/coord/<project-key>/
//	    owner.pid          exclusive-owner lock (single writer per journal
//	                       is per PROCESS too — see claimOwner)
//	    runs.jsonl         run registry / spawn queue / roster journal
//	    mailbox.jsonl      role mailboxes + consume cursors
//	    interactions.jsonl audit journal (no projection)
//	    endpoint.json      last-bound ports, re-bound on relaunch so
//	                       adopted children re-Hello a stable endpoint
//
// The "coord" segment and the per-project directory it composes both live in
// internal/paths (CoordDirName, CoordProjectStateDir) — the declarative
// source of truth for ctxloom path segments (docs/architecture/core/
// paths.md). discover, the layout owner for endpoint.json's SHAPE (see its
// package doc), globs this same directory via paths.HomeCoordDir.
const coordDirName = paths.CoordDirName

// stateDirForProject resolves the coordinator state dir, keyed by project
// (plan: durability first, keyed by project — a fresh `ctxloom run` adopts
// orphaned state from disk). projectKey should be the stable project id when
// one resolves; the caller may fall back to a path-derived key.
func stateDirForProject(projectKey string) (string, error) {
	dir, err := paths.CoordProjectStateDir(sanitizeKey(projectKey))
	if err != nil {
		return "", fmt.Errorf("coord: state dir: %w", err)
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

// errStateOwned reports the project state dir is exclusively owned by another
// live coordinator process.
var errStateOwned = errors.New("coord: project state is owned by another live coordinator")

// writeOwnerPID stamps pid into a freshly created owner-lock file and closes
// it, reporting the FIRST of the write's or the close's failure.
//
// Indirected so a test can drive that failure: on a real filesystem the write
// and the close only fail on conditions a test cannot provoke (a full or
// failing device), while the consequence of ignoring them — a lock file that
// carries no pid — is exactly what claimOwner must never leave behind.
var writeOwnerPID = func(f *os.File, pid int) error {
	_, err := fmt.Fprintf(f, "%d\n", pid)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// claimOwner takes the project state dir's exclusive-owner lock. The journal
// discipline demands a single writer per journal, and that holds ACROSS
// processes too: two concurrent session-owning processes for one project must
// not share journals. The second claimant gets errStateOwned and falls back
// to an ephemeral per-session state dir (no adoption, warned by the caller).
// A dead owner's stale lock is replaced (liveness = signal 0 probe).
//
// A lock file this function creates but cannot STAMP is removed again before it
// declines: an owner.pid with no pid in it reads to the next
// claimant as a dead owner — strconv.Atoi("") fails, so the lock is treated as
// stale and deleted — and two live coordinators would then share one project's
// journals, the single outcome this lock exists to prevent. Declining a claim
// costs this session adoption; leaving an unstamped lock costs the journal its
// single writer.
func claimOwner(dir string) (release func(), err error) {
	lock := filepath.Join(dir, "owner.pid")
	for range 2 {
		f, err := os.OpenFile(lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if werr := writeOwnerPID(f, os.Getpid()); werr != nil {
				_ = os.Remove(lock)
				clidiag.Warn("ctxloom", "coordinator: could not stamp the project state lock %s (%v); declining the claim rather than leaving a lock that reads as a dead owner", lock, werr)
				return nil, errStateOwned
			}
			return func() { _ = os.Remove(lock) }, nil
		}
		raw, rerr := os.ReadFile(lock)
		if rerr != nil {
			return nil, errStateOwned
		}
		pid, perr := strconv.Atoi(strings.TrimSpace(string(raw)))
		// MaybeAlive (not a bare Alive check) treats an
		// unconfirmable probe the same as a live owner — a false "the owner
		// is dead" here would let a second process share this project's
		// journal, which the single-writer discipline this function exists
		// to enforce can never recover from. A needlessly-ephemeral fallback
		// on a false positive is the safe direction; a shared journal is not.
		if perr == nil && pid > 0 && pidalive.Probe(pid).MaybeAlive() && pid != os.Getpid() {
			return nil, errStateOwned
		}
		// Stale lock from a dead owner: remove and retry once. The remove→
		// create window is racy in theory; the loser of the race lands on
		// errStateOwned and takes the ephemeral fallback — never a shared
		// journal.
		_ = os.Remove(lock)
	}
	return nil, errStateOwned
}
