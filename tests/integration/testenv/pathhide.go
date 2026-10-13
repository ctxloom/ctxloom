package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// PathHiding returns path (a PATH-formatted list) with every executable
// called one of names hidden, for a fixture whose claim is "only these
// programs are installed" on a developer machine that has more. An entry
// holding a hidden name is replaced by a mirror under scratch that links
// every OTHER entry of that directory, so the rest of the toolchain stays
// reachable; an entry holding none is kept as it is. Empty entries (the cwd)
// are dropped.
func PathHiding(path, scratch string, names ...string) (string, error) {
	sep := string(os.PathListSeparator)
	var kept []string
	for _, dir := range strings.Split(path, sep) {
		if dir == "" {
			continue
		}
		if !holdsAny(dir, names) {
			kept = append(kept, dir)
			continue
		}
		mirror, err := mirrorWithout(dir, scratch, names)
		if err != nil {
			return "", err
		}
		kept = append(kept, mirror)
	}
	return strings.Join(kept, sep), nil
}

func holdsAny(dir string, names []string) bool {
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

func mirrorWithout(dir, scratch string, names []string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("hide %v from PATH entry %s: %w", names, dir, err)
	}
	mirror, err := os.MkdirTemp(scratch, "path-mirror-")
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if slices.Contains(names, e.Name()) {
			continue
		}
		if err := os.Symlink(filepath.Join(dir, e.Name()), filepath.Join(mirror, e.Name())); err != nil {
			return "", err
		}
	}
	return mirror, nil
}
