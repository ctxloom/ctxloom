package configload

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// errRetiredProfilesDir is an app directory still holding the retired
// standalone profiles directory (paths.ProfilesPath).
var errRetiredProfilesDir = errors.New("retired standalone profiles directory")

// projectBundleVersion is the version the fix line's project bundle envelope
// declares: a bundle with no items must declare one to be a bundle at all.
const projectBundleVersion = "1.0.0"

// refuseRetiredProfilesDir refuses to load while any participating app
// directory holds a standalone profiles directory. Profiles are the items of
// the project bundle (paths.ProjectBundleName), and that directory is no
// longer read — loading past it would launch every agent that names one of
// those profiles on context that silently vanished. The move is demanded, not
// performed: ctxloom does not rewrite content it did not author in this run.
func refuseRetiredProfilesDir(fs afero.Fs, appPaths []string) error {
	for _, appPath := range appPaths {
		dir := paths.ProfilesPath(appPath)
		exists, err := afero.DirExists(fs, dir)
		if err != nil || !exists {
			continue
		}
		fix, err := profilesMoveFix(appPath)
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: %s — profiles are items of the %q bundle now, and this directory is no longer read. Move them by hand:\n%s",
			errRetiredProfilesDir, dir, paths.ProjectBundleName, fix)
	}
	return nil
}

// profilesMoveFix is the exact manual move for appPath: the profile files into
// the project bundle's profiles directory, an envelope when the bundle has
// none, and the emptied directory removed.
func profilesMoveFix(appPath string) (string, error) {
	from := paths.ProfilesPath(appPath)
	bundle := filepath.Join(paths.LocalBundlesPathFor(appPath, paths.LayoutV2), paths.ProjectBundleName)
	to := filepath.Join(bundle, paths.ProfilesDir)
	envelope, err := bundles.TreeEnvelope(&bundles.Bundle{Version: projectBundleVersion})
	if err != nil {
		return "", err
	}
	manifest := filepath.Join(bundle, bundles.DirectoryFormManifest)
	return fmt.Sprintf("  mkdir -p %s\n"+
		"  git mv %s/*.yaml %s/\n"+
		"  test -f %s || printf '%%b' %q > %s\n"+
		"  rmdir %s\n"+
		"(a .yml profile is renamed to .yaml, and a profile in a subdirectory moves up to a single-segment name: a profile name is one path segment)",
		to, from, to, manifest, string(envelope), manifest, from), nil
}
