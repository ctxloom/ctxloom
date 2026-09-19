package operations

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// CleanTarget is one regenerable path, and what re-creating it would cost.
type CleanTarget struct {
	// Rel is the path as paths.Layout names it, relative to the project root.
	Rel string `json:"rel"`
	// Path is where it actually resolves on this machine.
	Path string `json:"path"`
	// Present reports whether it is there at all — an absent target is
	// reported rather than hidden, so the plan accounts for every path the
	// layout classifies rather than only the ones that happen to exist.
	Present bool `json:"present"`
	// Bytes is what removing it reclaims. Zero when absent.
	Bytes int64 `json:"bytes"`
	// Rebuild is the command that reconstructs it, from the layout entry
	// itself. Never empty: a derived path that named no rebuild command would
	// be a layout bug, not a clean target.
	Rebuild string `json:"rebuild"`
	// Removed reports whether this run actually deleted it.
	Removed bool `json:"removed"`
}

// CleanResult is one clean run: what it would remove, or did.
type CleanResult struct {
	Applied bool          `json:"applied"`
	Targets []CleanTarget `json:"targets"`
	Bytes   int64         `json:"bytes"`
}

// CleanCache removes this project's REGENERABLE state, and nothing else.
//
// WHAT IT SELECTS, and why it is not "every TierDerived entry". The selection
// is paths.Layout's own classification, narrowed to the cache: an entry that is
// TierDerived AND lives under .ctxloom/cache. That directory's contract is
// exactly this ("REGENERABLE data ... every path under it is TierDerived"), so
// the two agree by construction and a cache path added later is covered without
// touching this function.
//
// lock.yaml is why the narrowing exists rather than keying on the tier alone.
// It is TierDerived — `ctxloom remote lock` regenerates it — and it is also
// COMMITTED, deliberately, because a lockfile the next clone does not get is
// worthless. Deleting it would not free a byte worth having; it would dirty the
// caller's working tree. Tier answers "can this be rebuilt", not "may I delete
// it", and those differ for exactly one entry today.
//
// TierLocal is never touched, and that is the load-bearing exclusion: nothing
// rebuilds those paths (Entry.Rebuild is empty precisely for that tier), so a
// sweep that took them would destroy the session records, approvals and
// application records a clone cannot restore. `clean` means "costs time", never
// "costs history".
func CleanCache(appDir, home string, apply bool) (CleanResult, error) {
	res := CleanResult{Applied: apply}
	cachePrefix := filepath.Join(paths.AppDirName, paths.CacheDir) + string(filepath.Separator)

	for _, e := range paths.Layout() {
		if e.Tier != paths.TierDerived {
			continue
		}
		if !strings.HasPrefix(e.Rel, cachePrefix) {
			continue // lock.yaml: derived, but committed — see this function's doc.
		}
		full := filepath.Join(e.ResolveRoot(appDir, home), e.Rel)
		size, present, err := treeSize(full)
		if err != nil {
			return res, fmt.Errorf("clean: measure %s: %w", full, err)
		}
		t := CleanTarget{Rel: e.Rel, Path: full, Present: present, Bytes: size, Rebuild: e.Rebuild}
		if apply && present {
			if err := os.RemoveAll(full); err != nil {
				return res, fmt.Errorf("clean: remove %s: %w", full, err)
			}
			t.Removed = true
		}
		res.Bytes += size
		res.Targets = append(res.Targets, t)
	}
	return res, nil
}

// treeSize totals the bytes under path, reporting whether it exists at all.
// A path that is not there is not an error: the cache is built lazily, so a
// project that has never pulled legitimately has none of it.
func treeSize(path string) (int64, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if !info.IsDir() {
		return info.Size(), true, nil
	}
	var total int64
	err = filepath.WalkDir(path, func(_ string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		fi, statErr := d.Info()
		if statErr != nil {
			// A file that vanished mid-walk is not a measurement failure —
			// something else reaped it, and its bytes are already reclaimed.
			if os.IsNotExist(statErr) {
				return nil
			}
			return statErr
		}
		total += fi.Size()
		return nil
	})
	return total, true, err
}
