package remote

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// The publish-remote consent store changed shape: it used to be a MARKER
// DIRECTORY (~/.ctxloom/publish-remotes/, paths.LegacyPublishRemotesDirName),
// one <sha256>.confirmed file per confirmed remote, the file's EXISTENCE
// being the whole record. It is now a single YAML file written through
// admission.Store (see DefaultPublishRemoteStore). NOTHING MIGRATES — this
// project carries no compatibility shims, and a user who had confirmed
// remotes under the old shape is simply asked once more — but the old
// directory was being left behind forever, swept by nothing, with nobody
// told it was dead. sweepLegacyPublishRemotesDir is that sweep.
//
// It runs from DefaultPublishRemoteStore, i.e. exactly when publish consent
// is actually consulted (`ctxloom remote trusted/allow/deny/forget`, and
// the publish gate itself) — never from unconditional startup, so a session
// that never touches publish never pays for a stat call.
//
// THIS DELETES FROM A USER'S HOME DIRECTORY, so every check below is load-
// bearing, not defensive dressing:
//
//  1. $HOME must resolve to a non-empty, ABSOLUTE path. filepath.Join("", x)
//     == x, so an unresolvable (or, in principle, relative) home would
//     otherwise make the legacy path resolve relative to the process's
//     working directory, and RemoveAll would then delete a directory out of
//     whatever repo the user happens to be standing in. See
//     admission.Store.configured for the same discipline applied to the NEW
//     store.
//  2. The directory's CONTENTS must match the legacy shape exactly — nothing
//     but files matching legacyPublishRemoteMarker. Any other entry (a
//     subdirectory, a file with an unexpected name, anything this sweep did
//     not write) is a signal the assumption is wrong, and the whole directory
//     is left alone rather than partially cleaned.
//  3. The legacy path itself, and every entry inside it, must NOT be a
//     symlink. A symlinked legacy path is left alone, both the link and
//     whatever it points at.
//  4. A missing legacy directory is the ordinary case: silent, no warning, no
//     error. Every other outcome — swept, or left alone with a reason — is
//     reported exactly once per process via clidiag.WarnOnce, which dedups on
//     the fully formatted line, so a store rebuilt on every `trust publish`
//     invocation does not repeat itself.
//
// This function never touches PublishRemotesFileName, the new store — it
// only ever looks at, and only ever removes, the legacy directory.

// legacyPublishRemoteMarker matches the pre-admission marker-file shape
// exactly: a lowercase 64-character hex sha256 (of a remote's identity),
// followed by ".confirmed". That shape, and only that shape, is what the old
// store ever wrote.
var legacyPublishRemoteMarker = regexp.MustCompile(`^[0-9a-f]{64}\.confirmed$`)

// sweepLegacyPublishRemotesDir removes the pre-admission publish-remote
// marker directory if, and only if, every safety check documented above
// passes. See the package-level doc above for what each check guards
// against.
func sweepLegacyPublishRemotesDir() {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !filepath.IsAbs(home) {
		// Unresolvable (or, on some platform, resolvable to something
		// non-absolute): do NOTHING. There is nothing to warn about here —
		// the identical unresolvable-$HOME fault already surfaces, loudly,
		// at the new store this sweep is a companion to
		// (DefaultPublishRemoteStore / admission.Store.configured).
		return
	}
	dir := filepath.Join(home, paths.AppDirName, paths.LegacyPublishRemotesDirName)

	info, statErr := os.Lstat(dir)
	if statErr != nil {
		// Absent is the ordinary case (nothing to sweep, nobody ever had the
		// legacy store, or it was already swept). Any other stat failure
		// (permission denied, a broken parent) is likewise left silent here:
		// this sweep is a courtesy cleanup, not a gate, and its own
		// unreadability is not something a publish operation should warn
		// about on every invocation.
		return
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		clidiag.WarnOnce("ctxloom", "the old publish-remote confirmation directory %s is a symlink; "+
			"leaving it alone rather than deleting through it or the file it points to", dir)
		return
	}
	if !info.IsDir() {
		clidiag.WarnOnce("ctxloom", "%s exists but is not a directory (expected the old publish-remote "+
			"confirmation store); leaving it alone", dir)
		return
	}

	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		clidiag.WarnOnce("ctxloom", "could not read the old publish-remote confirmation directory %s: %v; "+
			"leaving it alone", dir, readErr)
		return
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !legacyPublishRemoteMarker.MatchString(e.Name()) {
			clidiag.WarnOnce("ctxloom", "the old publish-remote confirmation directory %s holds an unexpected "+
				"entry %q; leaving the whole directory alone rather than guessing what else belongs there", dir, e.Name())
			return
		}
	}

	if rmErr := os.RemoveAll(dir); rmErr != nil {
		clidiag.WarnOnce("ctxloom", "could not remove the old publish-remote confirmation directory %s: %v", dir, rmErr)
		return
	}
	clidiag.WarnOnce("ctxloom", "%s", legacySweptMessage(dir))
}

// legacySweptMessage is the user-facing sentence for a completed sweep,
// pulled out so the test pinning it and the call site cannot drift apart. It
// names the removed path and says plainly what it means for the user: any
// remote they remember confirming under the old store will be asked about
// again, exactly once, under the new one.
func legacySweptMessage(dir string) string {
	newPath, err := paths.HomePublishRemotesPath()
	if err != nil || newPath == "" {
		newPath = "~/" + paths.AppDirName + "/" + paths.PublishRemotesFileName + ".yaml"
	}
	return fmt.Sprintf(
		"removed the old publish-remote confirmation directory %s (superseded by %s); "+
			"any remote you confirmed there will be asked about again",
		dir, newPath)
}
