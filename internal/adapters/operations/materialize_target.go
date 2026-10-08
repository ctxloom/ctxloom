package operations

import (
	"errors"
	"fmt"
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
// target or names target itself.
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
	}
	if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s is not inside %s", ErrContextFileOutsideTarget, dest, target)
	}
	return filepath.ToSlash(rel), nil
}
