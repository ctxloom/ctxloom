package operations

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// BundleAnalysis is the result of cross-checking a lockfile against the bundle
// references declared by local profiles.
type BundleAnalysis struct {
	Orphans  []string // in lockfile but referenced by no profile
	Missing  []string // referenced by a profile but not in lockfile (and not on disk)
	Invalid  []string // malformed bundle references
	Warnings []string // non-fatal issues encountered during analysis
}

// AnalyzeBundleReferencesRequest is the input for AnalyzeBundleReferences.
type AnalyzeBundleReferencesRequest struct {
	Lockfile *remote.Lockfile
	AppDir   string // base under which profiles/ and bundles/ live; "" → ".ctxloom"

	// FS is an optional filesystem (defaults to the OS filesystem).
	FS afero.Fs
}

// AnalyzeBundleReferences walks local profiles and reports orphaned, missing,
// and invalid bundle references against the lockfile. Read-only; partial
// failures accumulate in Warnings so a frontend can surface them.
func AnalyzeBundleReferences(req AnalyzeBundleReferencesRequest) *BundleAnalysis {
	fs := getFS(req.FS)
	appDir := req.AppDir
	if appDir == "" {
		appDir = paths.AppDirName
	}
	result := &BundleAnalysis{}
	referenced := make(map[string]bool)

	profileDir := filepath.Join(appDir, "profiles")
	err := afero.Walk(fs, profileDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("error accessing %s: %v", path, err))
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		content, rerr := afero.ReadFile(fs, path)
		if rerr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("error reading %s: %v", path, rerr))
			return nil
		}
		collectProfileBundleRefs(path, content, result, referenced)
		return nil
	})
	if err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("error walking profiles directory: %v", err))
	}

	for ref := range req.Lockfile.Bundles {
		if !referenced[ref] {
			result.Orphans = append(result.Orphans, ref)
		}
	}
	for ref := range referenced {
		if _, exists := req.Lockfile.Bundles[ref]; !exists {
			if _, statErr := fs.Stat(localBundlePath(appDir, ref)); os.IsNotExist(statErr) {
				result.Missing = append(result.Missing, ref)
			}
		}
	}
	return result
}

// collectProfileBundleRefs parses a profile file's bundle list, recording each
// valid (normalized to local-name) reference in referenced and noting malformed
// or unparseable entries on result.
func collectProfileBundleRefs(path string, content []byte, result *BundleAnalysis, referenced map[string]bool) {
	var profile struct {
		Bundles []string `yaml:"bundles"`
	}
	if err := yaml.Unmarshal(content, &profile); err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("invalid YAML in %s: %v", path, err))
		return
	}
	for _, bundle := range profile.Bundles {
		if bundle == "" {
			continue
		}
		bundleRef, _, _ := strings.Cut(bundle, "#")
		ref, err := remote.ParseReference(bundleRef)
		if err != nil || !ref.IsCanonical() {
			result.Invalid = append(result.Invalid, fmt.Sprintf("%s (in %s)", bundle, filepath.Base(path)))
			continue
		}
		// Key by the canonical ref so it matches lockfile keys.
		referenced[ref.CanonicalString()] = true
	}
}

// localItemPath returns the on-disk install path for a canonical item ref,
// using the same layout as the puller (Reference.LocalPath). Returns "" when
// the ref is not a parseable canonical ref.
func localItemPath(appDir string, itemType remote.ItemType, canonicalRef string) string {
	ref, err := remote.ParseReference(canonicalRef)
	if err != nil || !ref.IsCanonical() {
		return ""
	}
	return ref.LocalPath(appDir, itemType)
}

// localBundlePath is localItemPath for a bundle ref.
func localBundlePath(appDir, canonicalRef string) string {
	return localItemPath(appDir, remote.ItemTypeBundle, canonicalRef)
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
	LockManager *remote.LockfileManager

	// FS is an optional filesystem (defaults to the OS filesystem).
	FS afero.Fs
}

// RemoveLocalItemsResult reports the cleanup outcome.
type RemoveLocalItemsResult struct {
	Removed  []string // local paths actually deleted (legacy materialized copies)
	Pruned   []string // lockfile entries pruned (canonical refs)
	Warnings []string
	Saved    bool // whether the lockfile was rewritten
}

// RemoveLocalItems prunes the lockfile entries for items the remote dropped
// and persists the pruned lockfile. Remote items are pure references — nothing
// is materialized to disk — so the lockfile prune is the real cleanup; the
// save is gated on entries pruned, never on files deleted. File removal is
// best-effort cleanup of copies materialized by the pre-reference-only model:
// a missing file is the normal case, other removal failures are warnings.
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
		if err := req.LockManager.Save(req.Lockfile); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("failed to update lockfile: %v", err))
		} else {
			res.Saved = true
		}
	}
	return res, nil
}
