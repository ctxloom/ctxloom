package remote

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// TestLocalPath_AlwaysContainedInCacheRoot is the CLASS gate for the
// LocalPath traversal escape.
//
// Reference.LocalPath is the one function that turns an attacker-influenceable
// string (a remote URL out of a lockfile) into an on-disk path, and callers
// hand that path to fs.Remove / MkdirAll / WriteFile. The item-path half is
// already guarded by validateItemPath; the escape was exclusively via the URL,
// through LocalRemoteName → httpHostPath (path.Join CLEANS, so "https://x/../.."
// yields "..") and → sanitizePath (which rewrites "://", ":" and "@" but strips
// no ".." at all).
//
// The gate is stated over the RESULT rather than over any one helper, so it
// keeps holding if the URL→name mapping is rewritten: whatever the URL, the
// computed install path must stay under <baseDir>/cache/bundles.
//
// Blind spot, stated: this covers the path LocalPath COMPUTES. It does not
// prove callers use LocalPath rather than assembling their own path, and it
// says nothing about symlinks already on disk under the cache root.
func TestLocalPath_AlwaysContainedInCacheRoot(t *testing.T) {
	base := filepath.Join("/proj", ".ctxloom")
	root := filepath.Join(base, paths.CacheDir, paths.BundlesDir)

	urls := []string{
		// The reported escape: path.Join cleans "x" + "/../.." to "..".
		"https://x/../..",
		"https://x/../../..",
		"https://host/a/../../../../..",
		"http://h/../..",
		// sanitizePath's fall-through — it strips no traversal at all.
		"weird://../../../etc",
		"../../../etc",
		"..",
		"git@h:../../..",
		"file:///../../..",
		"file://../..",
		// Ordinary shapes, which must keep working.
		"https://github.com/owner/repo",
		"git@github.com:owner/repo",
		"file:///home/u/content-repo",
	}

	for _, u := range urls {
		t.Run(u, func(t *testing.T) {
			r := &Reference{URL: u, Path: "victim"}
			got := r.LocalPath(base, ItemTypeBundle)

			rel, err := filepath.Rel(root, got)
			if err != nil {
				t.Fatalf("LocalPath(%q) = %q: not relatable to the cache root %q: %v", u, got, root, err)
			}
			if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				t.Fatalf("LocalPath(%q) = %q escapes the bundle cache root %q (rel %q)", u, got, root, rel)
			}
		})
	}
}

// TestLocalRemoteName_NeverYieldsTraversal pins the root cause directly, so a
// future caller that uses LocalRemoteName for some other path join inherits
// the guarantee rather than having to rediscover it.
func TestLocalRemoteName_NeverYieldsTraversal(t *testing.T) {
	for _, u := range []string{
		"https://x/../..", "http://h/../../..", "weird://../../../etc",
		"../../../etc", "..", "git@h:../..", "file:///../../..",
	} {
		r := &Reference{URL: u}
		name := r.LocalRemoteName()
		for _, seg := range strings.Split(filepath.ToSlash(name), "/") {
			if seg == ".." {
				t.Fatalf("LocalRemoteName(%q) = %q contains a %q segment", u, name, "..")
			}
		}
	}
}

// TestLocalRemoteName_OrdinaryURLsUnchanged is the control: the containment
// guard must not quietly relocate every real remote's cache directory.
func TestLocalRemoteName_OrdinaryURLsUnchanged(t *testing.T) {
	cases := map[string]string{
		"https://github.com/owner/repo": "github.com/owner/repo",
		"git@github.com:owner/repo":     "github.com/owner/repo",
		"file:///path/to/repo":          "to/repo",
	}
	for u, want := range cases {
		r := &Reference{URL: u}
		if got := r.LocalRemoteName(); got != want {
			t.Errorf("LocalRemoteName(%q) = %q, want %q", u, got, want)
		}
	}
}
