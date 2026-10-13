package operations

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrContextFileOutsideTarget refuses a context destination that does not
// name a file inside the target: a section claimed outside the target's
// root would never be released by a later delivery there.
var ErrContextFileOutsideTarget = errors.New("the context destination must name a file inside the target")

// ResolveContextFile resolves a `--surface context=file:DEST` destination
// against target: a relative DEST is taken under target, an absolute one
// must lie inside it. It returns the slash path relative to target ("" for
// an empty DEST: the engine's own file), and refuses anything that escapes
// target or names target itself. A DEST rooted without being absolute
// (Windows `\x` or `C:x`) is outside target too.
func ResolveContextFile(target, dest string) (string, error) {
	if dest == "" {
		return "", nil
	}
	rel := filepath.Clean(dest)
	if filepath.IsAbs(dest) {
		r, err := filepath.Rel(filepath.Clean(target), rel)
		if err != nil {
			return "", fmt.Errorf("%w: %s", ErrContextFileOutsideTarget, dest)
		}
		rel = r
	} else if rootedElsewhere(rel, os.IsPathSeparator, filepath.VolumeName) {
		return "", fmt.Errorf("%w: %s is rooted outside %s", ErrContextFileOutsideTarget, dest, target)
	}
	if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s is not inside %s", ErrContextFileOutsideTarget, dest, target)
	}
	return filepath.ToSlash(rel), nil
}

// rootedElsewhere reports a DEST that is rooted without being absolute: on
// Windows a leading separator (`\x`, the current drive's root) or a volume
// (`C:x`, a drive's current directory). Neither resolves against target, so
// ResolveContextFile counts both as outside it. On Unix every rooted path is
// absolute and a backslash is an ordinary name byte, so this is never true
// there. The separator and volume rules are parameters so the Windows
// behaviour is testable on any host.
func rootedElsewhere(dest string, isSep func(uint8) bool, volume func(string) string) bool {
	return (dest != "" && isSep(dest[0])) || volume(dest) != ""
}
