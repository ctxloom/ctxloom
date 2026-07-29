package operations

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/triggers"
)

// triggerCacheEntry is one task's cached verdict, keyed by harp id in
// triggerVerdictCache. Fingerprint is what makes it a CACHE and not just a
// log: a lookup only reuses Verdict when the current fingerprint (see
// fingerprintTask) still matches this one.
type triggerCacheEntry struct {
	Fingerprint string           `json:"fingerprint"`
	Verdict     triggers.Verdict `json:"verdict"`
}

// triggerVerdictCache is the whole per-project cache file's shape.
type triggerVerdictCache struct {
	Tasks map[string]triggerCacheEntry `json:"tasks"`
}

// fingerprintTask hashes every input that could change a verdict for ti: its
// trigger text and task text, its deferred-at time, the commit/file evidence
// gathered for it, AND the repo-global state (existence-style triggers like
// "once package X exists" depend on repo, not just this task's own commit
// window — see triggers.RepoState's doc comment). Two calls with identical
// inputs always produce the same fingerprint; any change to any of these
// inputs changes it, which is exactly the cache invalidation rule.
func fingerprintTask(ti triggers.TaskInput, repo triggers.RepoState) string {
	h := sha256.New()
	fmt.Fprintf(h, "trigger=%s\x00text=%s\x00deferred_at=%s\x00",
		ti.Trigger, ti.Text, ti.DeferredAt.UTC().Format(time.RFC3339))
	for _, c := range ti.CommitsSince {
		fmt.Fprintf(h, "commit=%s|%s|%s\x00", c.SHA, c.Date.UTC().Format(time.RFC3339), c.Subject)
	}
	for _, f := range ti.ChangedFiles {
		fmt.Fprintf(h, "file=%s\x00", f)
	}
	for _, d := range repo.Dirs {
		fmt.Fprintf(h, "dir=%s\x00", d)
	}
	for _, ch := range repo.WorkingChanges {
		fmt.Fprintf(h, "change=%s\x00", ch)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// triggerCacheFilePath resolves the cache file for a project. The project id
// is HASHED into the filename rather than used directly: it can arrive from
// several sources (a harp name, CTXLOOM_PROJECT_ID, an in-tree marker) with
// varying trustworthiness, and hashing sidesteps ever needing to re-litigate
// which of those are safe path segments — the cache is pure derived data, so
// there's nothing lost by keying it opaquely.
func triggerCacheFilePath(projectID string) (string, error) {
	dir, err := paths.TriggerCacheDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(projectID))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".json"), nil
}

// loadTriggerCache best-effort reads the project's verdict cache. A missing
// file, an empty project id, or a corrupt/unreadable file all degrade to an
// empty cache (never an error, never a panic) — per the cache's design, it
// must be safe to delete or find absent at any time.
func loadTriggerCache(projectID string) triggerVerdictCache {
	empty := triggerVerdictCache{Tasks: map[string]triggerCacheEntry{}}
	if projectID == "" {
		return empty
	}
	path, err := triggerCacheFilePath(projectID)
	if err != nil {
		return empty
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return empty
	}
	var c triggerVerdictCache
	if err := json.Unmarshal(data, &c); err != nil {
		clidiag.Warn("ctxloom", "trigger cache is corrupt, ignoring: %v", err)
		return empty
	}
	if c.Tasks == nil {
		c.Tasks = map[string]triggerCacheEntry{}
	}
	return c
}

// saveTriggerCache best-effort writes the project's verdict cache. A no-op
// for an empty project id (nothing to key it by); any I/O failure warns and
// returns rather than failing the evaluation — a verdict cache write is
// advisory, never load-bearing for correctness.
func saveTriggerCache(projectID string, c triggerVerdictCache) {
	if projectID == "" {
		return
	}
	path, err := triggerCacheFilePath(projectID)
	if err != nil {
		clidiag.Warn("ctxloom", "trigger cache: resolve path: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		clidiag.Warn("ctxloom", "trigger cache: create dir: %v", err)
		return
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		clidiag.Warn("ctxloom", "trigger cache: encode: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		clidiag.Warn("ctxloom", "trigger cache: write: %v", err)
	}
}
