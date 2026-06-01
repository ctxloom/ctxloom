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
			bundlePath := filepath.Join(appDir, "bundles", strings.Replace(ref, "/", string(filepath.Separator), 1)+".yaml")
			if _, statErr := fs.Stat(bundlePath); os.IsNotExist(statErr) {
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
		if !strings.Contains(bundleRef, "/") {
			result.Invalid = append(result.Invalid, fmt.Sprintf("%s (in %s)", bundle, filepath.Base(path)))
			continue
		}
		if ref, err := remote.ParseReference(bundleRef); err == nil && ref.IsCanonical {
			bundleRef = ref.ToLocalName()
		}
		referenced[bundleRef] = true
	}
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
	Removed  []string // local paths actually deleted
	Warnings []string
	Saved    bool // whether the lockfile was rewritten
}

// RemoveLocalItems deletes the local files for items the remote dropped, prunes
// their lockfile entries, and persists the lockfile. Missing files are not
// errors; other removal failures are warnings. The frontend reports; the
// deletion + lockfile save live here.
func RemoveLocalItems(req RemoveLocalItemsRequest) (*RemoveLocalItemsResult, error) {
	fs := getFS(req.FS)
	res := &RemoveLocalItemsResult{}
	for _, item := range req.Items {
		localPath := filepath.Join(req.AppDir, item.Type.DirName(), strings.Replace(item.Ref, "/", string(filepath.Separator), 1)+".yaml")
		if err := fs.Remove(localPath); err != nil {
			if !os.IsNotExist(err) {
				res.Warnings = append(res.Warnings, fmt.Sprintf("failed to remove %s: %v", localPath, err))
			}
		} else {
			res.Removed = append(res.Removed, localPath)
		}
		req.Lockfile.RemoveEntry(item.Type, item.Ref)
	}
	if len(res.Removed) > 0 {
		if err := req.LockManager.Save(req.Lockfile); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("failed to update lockfile: %v", err))
		} else {
			res.Saved = true
		}
	}
	return res, nil
}
