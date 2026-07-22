package coord

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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
const coordDirName = "coord"

// stateDirForProject resolves the coordinator state dir, keyed by project
// (plan: durability first, keyed by project — a fresh `ctxloom run` adopts
// orphaned state from disk). projectKey should be the stable project id when
// one resolves; the caller may fall back to a path-derived key.
func stateDirForProject(projectKey string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("coord: state dir: %w", err)
	}
	dir := filepath.Join(home, paths.AppDirName, coordDirName, sanitizeKey(projectKey))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("coord: state dir: %w", err)
	}
	return dir, nil
}

// sanitizeKey makes a project key filesystem-safe as ONE path segment.
func sanitizeKey(k string) string {
	if k == "" {
		return "default"
	}
	repl := strings.NewReplacer("/", "-", "\\", "-", ":", "-", "..", "-")
	return repl.Replace(k)
}

// errStateOwned reports the project state dir is exclusively owned by another
// live coordinator process.
var errStateOwned = errors.New("coord: project state is owned by another live coordinator")

// claimOwner takes the project state dir's exclusive-owner lock. The journal
// discipline demands a single writer per journal, and that holds ACROSS
// processes too: two concurrent session-owning processes for one project must
// not share journals. The second claimant gets errStateOwned and falls back
// to an ephemeral per-session state dir (no adoption, warned by the caller).
// A dead owner's stale lock is replaced (liveness = signal 0 probe).
func claimOwner(dir string) (release func(), err error) {
	lock := filepath.Join(dir, "owner.pid")
	for range 2 {
		f, err := os.OpenFile(lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			_ = f.Close()
			return func() { _ = os.Remove(lock) }, nil
		}
		raw, rerr := os.ReadFile(lock)
		if rerr != nil {
			return nil, errStateOwned
		}
		pid, perr := strconv.Atoi(strings.TrimSpace(string(raw)))
		if perr == nil && pid > 0 && PidAlive(pid) && pid != os.Getpid() {
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
