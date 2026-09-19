package isolation

import (
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/version"
)

// dirtyMarker is the version stamp's tracked-dirty suffix.
//
// GIT-DESCRIBE SEMANTICS, deliberately, and NOT versionator's {{Dirty}}: the
// build recipe sets this marker from `git describe --always --dirty`, which
// counts TRACKED modifications only. versionator calls a tree dirty when its
// ONLY change is an untracked file, and ctxloom writes untracked files into
// its own checkout — so keying on that definition would force an image rebuild
// on nearly every local build, which is exactly the churn this key exists to
// remove, and would deliver reuse only to CI.
//
// ACCEPTED COST: an untracked-but-embedded asset would not force a rebuild.
// That holds only while everything embedded stays tracked; if that stops being
// true, this definition is what breaks.
const dirtyMarker = "-dirty"

// versionCommitKey is the COMMIT half of a version stamp: semver plus short
// sha, with the build timestamp cut off. "" when s is not a whole stamp.
//
// Cutting the timestamp is the entire point. version.Version is a STAMP shaped
// by version.ValidStamp as v<semver>-<sha>-<YYYYMMDDTHHMMSS>[-dirty], so it
// embeds the BUILD TIME and changes on every build even when nothing else did.
// Keying an image on the whole stamp would rebuild every image every build —
// reproducing the churn rather than removing it. Keying on what the build IS,
// its commit, lets one version's image be REUSED, and lets two different
// versions hold images side by side instead of overwriting one shared tag.
func versionCommitKey(s string) string {
	if !version.ValidStamp(s) {
		return ""
	}
	s = strings.TrimSuffix(s, dirtyMarker)
	return s[:strings.LastIndex(s, "-")]
}

// versionProvenanceKey is the BUILD half: versionCommitKey for a clean build,
// and the WHOLE stamp — build timestamp included — for a dirty one. "" when s
// is not a whole stamp.
//
// A dirty tree has no stable identity: two builds of one commit can carry
// different code, so nothing about the commit tells them apart. Falling back to
// the timestamp makes every dirty build a fresh key, which is what makes a
// dirty tree FORCE a rebuild.
//
// It rides the PROVENANCE and not the tag on purpose. A per-build key in the
// tag would mint — and leak — a whole new image for every dirty build; in the
// provenance it rebuilds over the same tag instead.
func versionProvenanceKey(s string) string {
	if !version.ValidStamp(s) {
		return ""
	}
	if strings.HasSuffix(s, dirtyMarker) {
		return s
	}
	return versionCommitKey(s)
}
