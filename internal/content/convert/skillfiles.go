package convert

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/content"
)

// SkillFilesFromDir is the Options.SkillFiles reader for a bundle authored on a
// filesystem: it reads each declared skill's package out of the bundle
// directory and DERIVES every file's declared mode from the mode that file
// carries on disk.
//
// Deriving is the point. A skill file's executability is DECLARED in the
// sidecar (content.SkillFile.Mode, written out as the `executable:` list) and
// never read from a filesystem at delivery time, because a POSIX mode is not
// portable and the digest excludes mode bits — that is what keeps a signature
// platform-independent. But a publisher authoring a package does not think in
// declarations; they `chmod +x scripts/run.sh` and commit it. Left to state the
// declaration by hand they forget, and the result is a package whose manifest
// says 0644, whose script the model cannot run, and whose only symptom is
// silence. Reading the committed mode HERE, at the one point where the
// filesystem and the declaration are both in scope, means the two cannot
// diverge at the source.
//
// Two costs were accepted when this was decided, and neither is solved here: an
// authoring filesystem with no exec bit (Windows) derives nothing and the
// author declares by hand, and a file that is 0755 only by umask accident gets
// declared executable.
//
// The package directory comes from bundles.ResolveSkillDir, so a skill with an
// explicit `path:` is honoured and a path escaping the bundle directory is
// refused — this reader must not be a second, weaker answer to "where does this
// skill live".
func SkillFilesFromDir(fsys afero.Fs, bundleDir string, b *bundles.Bundle) func(name string) ([]content.SkillFile, error) {
	return func(name string) ([]content.SkillFile, error) {
		if b == nil {
			return nil, fmt.Errorf("convert: no bundle to resolve skill %q against", name)
		}
		dir, err := bundles.ResolveSkillDir(bundleDir, name, b.Skills[name])
		if err != nil {
			return nil, err
		}
		return readSkillDir(fsys, dir, name)
	}
}

// readSkillDir walks one package directory into content.SkillFiles.
//
// Anything that is not a regular file is a hard error rather than a skip. A
// symlink read as a file would inline its target's bytes under the link's name
// — content the publisher never wrote, attested as if they had — and a skipped
// entry is a package that converts and signs while missing a file, which is
// this codebase's characteristic silent no-op.
func readSkillDir(fsys afero.Fs, dir, name string) ([]content.SkillFile, error) {
	var files []content.SkillFile
	err := afero.Walk(fsys, dir, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("convert: skill %q: walking %s: %w", name, p, walkErr)
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("convert: skill %q: %s is not a regular file (mode %v); a skill package carries files, "+
				"and converting anything else would attest bytes the publisher did not write", name, p, info.Mode())
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return fmt.Errorf("convert: skill %q: locating %s within the package: %w", name, p, rerr)
		}
		data, readErr := afero.ReadFile(fsys, p)
		if readErr != nil {
			return fmt.Errorf("convert: skill %q: reading %s: %w", name, rel, readErr)
		}
		files = append(files, content.SkillFile{
			Path:  filepath.ToSlash(rel),
			Mode:  declaredModeOf(info.Mode()),
			Bytes: data,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// declaredModeOf turns a committed POSIX mode into the declaration that will be
// written into the sidecar.
//
// ANY exec bit counts, not just the owner's: git records a blob as 100755 or
// 100644 and a checkout applies the umask to the former, so owner-only and
// world-readable variants of "the publisher made this executable" both arrive
// here and must produce the same declaration.
func declaredModeOf(m os.FileMode) content.ComponentMode {
	if m.Perm()&0o111 != 0 {
		return content.ModeExecutable
	}
	return content.ModeRegular
}
