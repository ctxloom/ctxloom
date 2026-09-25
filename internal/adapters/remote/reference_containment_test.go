package remote

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// TestLocalWorktreePath_AlwaysContainedInCacheRoot is the CLASS gate for the
// install-path traversal escape.
//
// Reference.LocalWorktreePath is the one function that turns an attacker-influenceable
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
// Blind spot, stated: this covers the path LocalWorktreePath COMPUTES. It
// does not prove callers use it rather than assembling their own path, and it
// says nothing about symlinks already on disk under the cache root.
func TestLocalWorktreePath_AlwaysContainedInCacheRoot(t *testing.T) {
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

	ordinary := map[string]bool{
		"https://github.com/owner/repo": true,
		"git@github.com:owner/repo":     true,
		"file:///home/u/content-repo":   true,
	}
	for _, u := range urls {
		t.Run(u, func(t *testing.T) {
			r := &Reference{URL: u, Path: "victim"}
			got, gerr := r.LocalWorktreePath(base)
			if gerr != nil {
				// No directory at all is contained. An ordinary shape must
				// still resolve, though, or the gate passes by refusing
				// everything.
				if ordinary[u] {
					t.Fatalf("LocalWorktreePath(%q): %v", u, gerr)
				}
				return
			}

			rel, err := filepath.Rel(root, got)
			if err != nil {
				t.Fatalf("LocalWorktreePath(%q) = %q: not relatable to the cache root %q: %v", u, got, root, err)
			}
			if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				t.Fatalf("LocalWorktreePath(%q) = %q escapes the bundle cache root %q (rel %q)", u, got, root, rel)
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

// TestLocalTreePath_ItemPathNeverEscapesTheCacheRoot is the item-path half of
// the class gate above. validateItemPath guards the item path only where a
// reference is PARSED; a Reference is also a plain struct, and its identity is
// minted by refuri, which RESOLVES dot segments rather than refusing them. So a
// Path of "../../x" mints the clean identity of bundle "x" while the path
// builders join the raw field. The gate is stated over both builders' RESULTS,
// for a reference however it was built: refused, or contained.
func TestLocalTreePath_ItemPathNeverEscapesTheCacheRoot(t *testing.T) {
	base := filepath.Join("/proj", ".ctxloom")
	root := filepath.Join(base, paths.CacheDir, paths.BundlesDir)

	contained := func(t *testing.T, what, got string) {
		t.Helper()
		rel, err := filepath.Rel(root, got)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("%s = %q is not strictly inside the bundle cache root %q (rel %q, %v)", what, got, root, rel, err)
		}
	}
	check := func(t *testing.T, r *Reference) {
		t.Helper()
		wt, werr := r.LocalWorktreePath(base)
		tree, terr := r.LocalTreePath(base)
		if werr == nil {
			contained(t, "LocalWorktreePath", wt)
		}
		if terr == nil {
			contained(t, "LocalTreePath", tree)
		}
	}

	// Paths the parser refuses (validateItemPath) are refused by the builder
	// too, typed, whether or not the join would happen to stay inside.
	refused := []string{
		"../../../../../../etc",
		"../../../../../../../../tmp/pwn",
		"a/../../../../../../../x",
		"/etc/passwd",
		"/",
		"..",
		".",
		`..\..\..\x`,
		"",
	}
	// Percent-encoded dots are NOT decoded on the way to the filesystem: they
	// are literal bytes of a directory name, so they must stay contained.
	literal := []string{
		"%2e%2e/%2e%2e/%2e%2e/%2e%2e/%2e%2e/x",
		"%2E%2E/x",
	}
	for _, url := range []string{"https://github.com/o/r", "file:///srv/x/r", "git@github.com:o/r"} {
		for _, p := range refused {
			t.Run("literal/"+url+"/"+p, func(t *testing.T) {
				r := &Reference{URL: url, Path: p, ItemType: ItemTypeBundle}
				_, err := r.LocalWorktreePath(base)
				require.ErrorIs(t, err, ErrNoCacheDirectory)
				_, err = r.LocalTreePath(base)
				require.ErrorIs(t, err, ErrNoCacheDirectory)
			})
		}
		for _, p := range literal {
			t.Run("literal/"+url+"/"+p, func(t *testing.T) {
				check(t, &Reference{URL: url, Path: p, ItemType: ItemTypeBundle})
			})
		}
	}
	// A reference with no remote repository has no cache directory at all —
	// not even an ordinary-looking one, which is where a traversal name would
	// be joined: a local ref, a companion ref, and a URL naming no repository.
	for _, p := range append([]string{"lang/go", "kit"}, refused...) {
		for name, r := range map[string]*Reference{
			"local":      {IsLocal: true, Path: p, ItemType: ItemTypeBundle},
			"companion":  {IsCompanion: true, URL: CompanionSource, Path: p, ItemType: ItemTypeBundle},
			"unreadable": {URL: "unknown://weird:url", Path: p, ItemType: ItemTypeBundle},
		} {
			t.Run(name+"/"+p, func(t *testing.T) {
				_, err := r.LocalWorktreePath(base)
				require.ErrorIs(t, err, ErrNoCacheDirectory)
			})
		}
	}

	// The same escapes through every parser arm: refused at parse, or
	// contained.
	parsed := []string{
		"https://github.com/o/r@bundles/../../../../../x",
		"https://github.com/o/r@bundles/%2e%2e/%2e%2e/%2e%2e/x",
		"https://github.com/o/r@bundles//etc/passwd",
		"file:///srv/x/r@bundles/../../../../x",
		"git@github.com:o/r@bundles/../../../../x",
		"ctxloom+git://github.com/o/r//bundles/../../../../../x",
		"ctxloom+git://github.com/o/r//bundles/%2e%2e/%2e%2e/%2e%2e/%2e%2e/x",
		"ctxloom+git://github.com/o/r//bundles/%2E%2E/%2E%2E/%2E%2E/x",
		"ctxloom+file:///srv/x/r//bundles/../../../../x",
		"ctxloom+file:///../../../..//bundles/x",
		"ctxloom+file:///%2e%2e/%2e%2e//bundles/x",
		"ctxloom+git://github.com/%2e%2e/%2e%2e//bundles/x",
	}
	for _, s := range parsed {
		t.Run("parsed/"+s, func(t *testing.T) {
			r, err := ParseReference(s)
			if err != nil {
				return
			}
			check(t, r)
		})
	}

	// Control: an ordinary nested bundle path still resolves under the root.
	ok := &Reference{URL: "https://github.com/o/r", Path: "lang/go", ItemType: ItemTypeBundle}
	tree, err := ok.LocalTreePath(base)
	if err != nil {
		t.Fatalf("ordinary nested bundle refused: %v", err)
	}
	contained(t, "LocalTreePath(lang/go)", tree)
}
