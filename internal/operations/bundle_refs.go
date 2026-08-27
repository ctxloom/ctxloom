package operations

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// localItemPath returns the on-disk install path for a canonical item ref,
// using the same layout as the puller (Reference.LocalPath). Returns "" when
// the ref is not a parseable canonical ref, or when the computed path would
// fall outside the bundle cache root.
//
// The containment check is deliberately redundant with remote's own
// containRemoteName: this is the value RemoveLocalItems hands to fs.Remove, so
// it is the last place a traversal can be stopped before a delete happens, and
// it must not depend on a sibling package keeping its guarantee.
// Returning "" rather than an error keeps cleanup best-effort — a ref that
// cannot yield a safe path simply has no file to remove.
func localItemPath(appDir string, itemType remote.ItemType, canonicalRef string) string {
	ref, err := remote.ParseReference(canonicalRef)
	if err != nil || !ref.IsCanonical() {
		return ""
	}
	p := ref.LocalPath(appDir, itemType)
	if !withinBundleCache(appDir, p) {
		clidiag.Warn("ctxloom",
			"ignoring remote item %q: its install path %q falls outside the bundle cache — refusing to touch it",
			canonicalRef, p)
		return ""
	}
	return p
}

// withinBundleCache reports whether p lies under <appDir>/cache/bundles.
func withinBundleCache(appDir, p string) bool {
	root, err := filepath.Abs(filepath.Join(appDir, paths.CacheDir, paths.BundlesDir))
	if err != nil {
		return false
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// RemovedItem identifies a local item to delete during cleanup.
type RemovedItem struct {
	Type remote.ItemType
	Ref  string
}

// RemoveLocalItemsRequest is the input for RemoveLocalItems.
type RemoveLocalItemsRequest struct {
	AppDir      string
	Items       []RemovedItem
	Lockfile    *remote.Lockfile
	LockManager remote.LockfileStore

	// FS is an optional filesystem (defaults to the OS filesystem).
	FS afero.Fs
}

// RemoveLocalItemsResult reports the cleanup outcome.
type RemoveLocalItemsResult struct {
	Removed  []string `json:"removed"` // local paths actually deleted (legacy materialized copies)
	Pruned   []string `json:"pruned"`  // lockfile entries pruned (canonical refs)
	Warnings []string `json:"warnings"`
	Saved    bool     `json:"saved"` // whether the lockfile was rewritten
}

// RemoveLocalItems prunes the lockfile entries for items the remote dropped
// and persists the pruned lockfile. Remote items are pure references — nothing
// is materialized to disk — so the lockfile prune is the real cleanup; the
// save is gated on entries pruned, never on files deleted. File removal is
// best-effort cleanup of copies materialized by the pre-reference-only model:
// a missing file is the normal case, other removal failures are warnings.
// reprise:ignore — groups with MaterializeProfile on the getFS/result/warnings
// idiom alone; the functions are unrelated and one-sided edits are expected.
func RemoveLocalItems(req RemoveLocalItemsRequest) (*RemoveLocalItemsResult, error) {
	fs := getFS(req.FS)
	res := &RemoveLocalItemsResult{}
	for _, item := range req.Items {
		if localPath := localItemPath(req.AppDir, item.Type, item.Ref); localPath != "" {
			if err := fs.Remove(localPath); err == nil {
				res.Removed = append(res.Removed, localPath)
			} else if !os.IsNotExist(err) {
				res.Warnings = append(res.Warnings, fmt.Sprintf("failed to remove %s: %v", localPath, err))
			}
		}
		req.Lockfile.RemoveEntry(item.Type, item.Ref)
		res.Pruned = append(res.Pruned, item.Ref)
	}
	if len(res.Pruned) > 0 {
		// remote.AllowEmpty: pruning the LAST entry legitimately empties the
		// lockfile, and Save otherwise refuses an empty write over a populated
		// file (that refusal exists to catch callers that computed nothing and
		// wrote it wholesale — see remote.Save). Here every removed entry was
		// named individually, so emptying is the intent, not an accident.
		if err := req.LockManager.Save(req.Lockfile, remote.AllowEmpty()); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("failed to update lockfile: %v", err))
		} else {
			res.Saved = true
		}
	}
	return res, nil
}
