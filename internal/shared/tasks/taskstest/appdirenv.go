package taskstest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvAppDirEscapeError reports the first environment variable that names a
// location inside an app directory outside every test temp root, or nil.
// See envAppDirEscapeError.
func EnvAppDirEscapeError() error {
	return envAppDirEscapeError(os.Environ(), testTempRoots())
}

// envAppDirEscapeError is the third route into a developer's real ~/.ctxloom,
// beside the two appDirIsolationError covers: a path INHERITED THROUGH THE
// ENVIRONMENT. Rooting HOME and cwd does not touch it. A ctxloom-launched
// session prepends its pinned companion store (~/.ctxloom/cache/companions/
// <digest>) to PATH, so companion admission's PATH lookup resolved ltk,
// taskloom and reprise in the developer's real store from inside a fully
// sandboxed test binary — results depended on what that machine had pinned.
//
// It keys on an app-dir path COMPONENT, not on the real home, because by the
// time it is asked the sandbox has already moved HOME, and a re-exec'd child
// never knew the real one. Only absolute entries count: a relative one
// resolves against the sandboxed cwd.
func envAppDirEscapeError(environ, tempRoots []string) error {
	for _, kv := range environ {
		name, value, _ := strings.Cut(kv, "=")
		if _, dropped := splitAppDirEntries(value, tempRoots); len(dropped) > 0 {
			return fmt.Errorf("$%s names %q, inside an app dir outside every recognized temp root %v: "+
				"a lookup through it reads the developer's real ctxloom state", name, dropped[0], tempRoots)
		}
	}
	return nil
}

// ScrubAppDirEnv removes, process-wide, every entry envAppDirEscapeError would
// report: an entry is dropped from its list (PATH keeps the rest of its
// directories), and a variable left with nothing is unset.
func ScrubAppDirEnv() error {
	roots := testTempRoots()
	for _, kv := range os.Environ() {
		name, value, _ := strings.Cut(kv, "=")
		kept, dropped := splitAppDirEntries(value, roots)
		if len(dropped) == 0 {
			continue
		}
		var err error
		if len(kept) == 0 {
			err = os.Unsetenv(name)
		} else {
			err = os.Setenv(name, strings.Join(kept, string(os.PathListSeparator)))
		}
		if err != nil {
			return fmt.Errorf("scrub $%s: %w", name, err)
		}
	}
	return nil
}

// splitAppDirEntries partitions a variable's value, read as a path list, into
// the entries that may stay and the ones that escape into an app dir outside
// roots.
func splitAppDirEntries(value string, roots []string) (kept, dropped []string) {
	for _, entry := range filepath.SplitList(value) {
		if filepath.IsAbs(entry) && hasAppDirComponent(entry) && !underAnyRoot(entry, roots) {
			dropped = append(dropped, entry)
			continue
		}
		kept = append(kept, entry)
	}
	return kept, dropped
}

func hasAppDirComponent(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(filepath.Clean(path)), "/") {
		if part == appDirName {
			return true
		}
	}
	return false
}
