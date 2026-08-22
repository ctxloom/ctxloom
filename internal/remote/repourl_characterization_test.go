package remote

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is the characterization suite for the repo-URL grammar: one table
// covering the whole input space, asserting all four renderings of every input
// side by side.
//
// Asserting them SIDE BY SIDE is the point. The defect this refactor exists to
// close was invisible in any single-function test, because
// each function was individually self-consistent — NormalizeURL and
// normalizeCloneURL each had a passing test suite while disagreeing about what
// "gitlab.com/alice/repo" meant. A table with one column per concern makes a
// disagreement a diff you can read.
//
// Rows marked wantIdentityAndTransportDiffer are the ONLY inputs where the
// identity and transport renderings may legitimately differ, and the suite
// asserts equality everywhere else — so a future edit that reintroduces a
// per-consumer special case fails here rather than in production.

type repoURLCase struct {
	name string
	in   string

	identity  string // NormalizeURL      — trust namespace, remotes.yaml, lockfile keys
	transport string // normalizeCloneURL — the git clone argument
	cacheDir  string // RepoDirForURL     — clone cache path, "" means "must error"
	kind      SourceKind

	// scpTransportDiffers marks the one form where identity and transport are
	// meant to disagree: git clones scp syntax natively, so transport keeps it
	// while identity folds it onto the repo's https spelling.
	scpTransportDiffers bool
}

func repoURLCases() []repoURLCase {
	return []repoURLCase{
		// --- GitHub shorthand -------------------------------------------------
		{name: "shorthand", in: "owner/repo",
			identity: "https://github.com/owner/repo", transport: "https://github.com/owner/repo",
			cacheDir: "/base/github.com/owner/repo", kind: SourceKindRemote},
		{name: "shorthand with dash", in: "my-org/my-repo",
			identity: "https://github.com/my-org/my-repo", transport: "https://github.com/my-org/my-repo",
			cacheDir: "/base/github.com/my-org/my-repo", kind: SourceKindRemote},
		// ".git" is stripped before the shorthand TEST, or the dot it
		// contributes disqualifies the token and `git clone` reads it as a
		// path. It is put back for RENDERING: the parse needs it gone, the
		// identity needs it kept. cacheDir stays stripped so the two spellings
		// of one repo still share one clone directory.
		{name: "shorthand with .git", in: "owner/repo.git",
			identity: "https://github.com/owner/repo.git", transport: "https://github.com/owner/repo.git",
			cacheDir: "/base/github.com/owner/repo", kind: SourceKindRemote},
		// A dot in a LATER segment is part of the repo NAME and must not
		// disqualify shorthand. normalizeCloneURL used to reject this.
		{name: "shorthand with dotted repo name", in: "owner/repo.js",
			identity: "https://github.com/owner/repo.js", transport: "https://github.com/owner/repo.js",
			cacheDir: "/base/github.com/owner/repo.js", kind: SourceKindRemote},
		{name: "shorthand three segments", in: "a/b/c",
			identity: "https://github.com/a/b/c", transport: "https://github.com/a/b/c",
			cacheDir: "/base/github.com/a/b/c", kind: SourceKindRemote},

		// --- host-qualified, scheme omitted (ruled) ----------------------------
		// A dot in the FIRST segment means it is a hostname: GitHub owner names
		// cannot contain one. NormalizeURL used to prefix these onto github.com.
		{name: "host-qualified other forge", in: "gitlab.com/alice/repo",
			identity: "https://gitlab.com/alice/repo", transport: "https://gitlab.com/alice/repo",
			cacheDir: "/base/gitlab.com/alice/repo", kind: SourceKindRemote},
		{name: "host-qualified github", in: "github.com/owner/repo",
			identity: "https://github.com/owner/repo", transport: "https://github.com/owner/repo",
			cacheDir: "/base/github.com/owner/repo", kind: SourceKindRemote},
		{name: "host-qualified with .git", in: "gitlab.com/alice/repo.git",
			identity: "https://gitlab.com/alice/repo.git", transport: "https://gitlab.com/alice/repo.git",
			cacheDir: "/base/gitlab.com/alice/repo", kind: SourceKindRemote},

		// --- bare host, no path ----------------------------------------------
		{name: "bare host", in: "gitlab.com",
			identity: "https://gitlab.com", transport: "https://gitlab.com",
			cacheDir: "/base/gitlab.com", kind: SourceKindRemote},
		{name: "bare internal host", in: "git.company.internal",
			identity: "https://git.company.internal", transport: "https://git.company.internal",
			cacheDir: "/base/git.company.internal", kind: SourceKindRemote},
		// No path, so this ".git" is part of a HOST NAME — "example.com.git"
		// is a different DNS name from "example.com", not one repository
		// spelled twice. Trimming it invented a host nobody named.
		{name: "bare host with .git", in: "example.com.git",
			identity: "https://example.com.git", transport: "https://example.com.git",
			cacheDir: "/base/example.com.git", kind: SourceKindRemote},
		{name: "bare token no dot", in: "owner",
			identity: "https://owner", transport: "https://owner",
			cacheDir: "/base/owner", kind: SourceKindRemote},

		// --- https ------------------------------------------------------------
		{name: "https", in: "https://github.com/owner/repo",
			identity: "https://github.com/owner/repo", transport: "https://github.com/owner/repo",
			cacheDir: "/base/github.com/owner/repo", kind: SourceKindRemote},
		{name: "https with .git", in: "https://github.com/owner/repo.git",
			identity: "https://github.com/owner/repo.git", transport: "https://github.com/owner/repo.git",
			cacheDir: "/base/github.com/owner/repo", kind: SourceKindRemote},
		{name: "https trailing slash", in: "https://github.com/owner/repo/",
			identity: "https://github.com/owner/repo", transport: "https://github.com/owner/repo",
			cacheDir: "/base/github.com/owner/repo", kind: SourceKindRemote},
		// Trailing slash AFTER .git: the suffixes have to be recognized in the
		// order slash -> .git -> slash or one hides the other. The slash is
		// dropped (RFC 3986 syntax-based normalization); the ".git" comes back.
		{name: "https .git then trailing slash", in: "https://github.com/owner/repo.git/",
			identity: "https://github.com/owner/repo.git", transport: "https://github.com/owner/repo.git",
			cacheDir: "/base/github.com/owner/repo", kind: SourceKindRemote},
		{name: "http kept as http", in: "http://github.com/owner/repo",
			identity: "http://github.com/owner/repo", transport: "http://github.com/owner/repo",
			cacheDir: "/base/github.com/owner/repo", kind: SourceKindRemote},
		// Uppercase SCHEME is folded (RFC 3986 6.2.2.1 makes scheme and host
		// case-insensitive); the host and path case here are NOT, because
		// every other component is case-sensitive by that same section.
		{name: "uppercase scheme", in: "HTTPS://GitHub.com/Owner/Repo.git",
			identity: "https://GitHub.com/Owner/Repo.git", transport: "https://GitHub.com/Owner/Repo.git",
			cacheDir: "/base/github.com/Owner/Repo", kind: SourceKindRemote},
		{name: "https with port", in: "https://git.example.com:8443/group/sub/repo.git",
			identity: "https://git.example.com:8443/group/sub/repo.git", transport: "https://git.example.com:8443/group/sub/repo.git",
			// the port names a server, not a repository: one clone dir either way
			cacheDir: "/base/git.example.com/group/sub/repo", kind: SourceKindRemote},
		{name: "https with userinfo", in: "https://user:pw@github.com/owner/repo",
			identity: "https://user:pw@github.com/owner/repo", transport: "https://user:pw@github.com/owner/repo",
			cacheDir: "/base/github.com/owner/repo", kind: SourceKindRemote},
		// Query and fragment survive here and are dropped by
		// trust.CanonicalRepoURL: a clone argument may legitimately need them,
		// a trust key never may.
		{name: "https with query and fragment", in: "https://github.com/owner/repo?ref=x#frag",
			identity: "https://github.com/owner/repo?ref=x#frag", transport: "https://github.com/owner/repo?ref=x#frag",
			cacheDir: "/base/github.com/owner/repo", kind: SourceKindRemote},

		// --- scp-like ---------------------------------------------------------
		// The ONE form where identity and transport are meant to differ.
		{name: "scp", in: "git@github.com:owner/repo",
			identity: "https://github.com/owner/repo", transport: "git@github.com:owner/repo",
			cacheDir: "/base/github.com/owner/repo", kind: SourceKindRemote, scpTransportDiffers: true},
		{name: "scp with .git", in: "git@github.com:owner/repo.git",
			identity: "https://github.com/owner/repo.git", transport: "git@github.com:owner/repo.git",
			// the two spellings of one repo share one clone dir; they used to
			// get two, one of them literally named "…/repo.git"
			cacheDir: "/base/github.com/owner/repo", kind: SourceKindRemote, scpTransportDiffers: true},
		{name: "scp other forge", in: "git@gitlab.com:group/project.git",
			identity: "https://gitlab.com/group/project.git", transport: "git@gitlab.com:group/project.git",
			cacheDir: "/base/gitlab.com/group/project", kind: SourceKindRemote, scpTransportDiffers: true},
		{name: "scp trailing slash", in: "git@github.com:owner/repo/",
			identity: "https://github.com/owner/repo", transport: "git@github.com:owner/repo",
			cacheDir: "/base/github.com/owner/repo", kind: SourceKindRemote, scpTransportDiffers: true},
		// Git's scp syntax is "[user@]host:path" for ANY user, not just "git".
		// gitolite and gerrit conventionally use their own, and classifying
		// those as an opaque local path is fail-open at the trust gate: local
		// is the auto-trusted classification, so an untrusted remote would be
		// admitted without a trust decision. They must reach SourceKindRemote
		// and produce a real cache dir, exactly as the "git" user does.
		{name: "scp non-git user", in: "forge@gitlab.example.com:group/repo.git",
			identity: "https://gitlab.example.com/group/repo.git", transport: "forge@gitlab.example.com:group/repo.git",
			cacheDir: "/base/gitlab.example.com/group/repo", kind: SourceKindRemote, scpTransportDiffers: true},
		{name: "scp gerrit-style user", in: "gerrit@review.example.org:platform/core",
			identity: "https://review.example.org/platform/core", transport: "gerrit@review.example.org:platform/core",
			cacheDir: "/base/review.example.org/platform/core", kind: SourceKindRemote, scpTransportDiffers: true},

		// --- non-http transports: byte-preserved ------------------------------
		// These name repositories that REALLY EXIST, so their trust keys must
		// not move: a moved key drops any REJECTION recorded under the old
		// spelling, and EffectiveTrust step 5 can then allow the item on its
		// publisher signature. The ".git" is a real path component here (a bare
		// repository is literally named "<name>.git"), not a forge cosmetic —
		// normalizeCloneURL used to strip it and hand git a path that does not
		// exist.
		{name: "file url with .git", in: "file:///srv/repos/foo.git",
			identity: "file:///srv/repos/foo.git", transport: "file:///srv/repos/foo.git",
			cacheDir: "/base/srv/repos/foo", kind: SourceKindRemote},
		{name: "file url", in: "file:///srv/repos/foo",
			identity: "file:///srv/repos/foo", transport: "file:///srv/repos/foo",
			cacheDir: "/base/srv/repos/foo", kind: SourceKindRemote},
		{name: "file url trailing slash", in: "file:///srv/repos/foo/",
			identity: "file:///srv/repos/foo/", transport: "file:///srv/repos/foo/",
			cacheDir: "/base/srv/repos/foo", kind: SourceKindRemote},
		{name: "file url mixed case", in: "file:///srv/repos/Foo",
			identity: "file:///srv/repos/Foo", transport: "file:///srv/repos/Foo",
			cacheDir: "/base/srv/repos/Foo", kind: SourceKindRemote},
		{name: "ssh url with .git", in: "ssh://git@github.com/owner/repo.git",
			identity: "ssh://git@github.com/owner/repo.git", transport: "ssh://git@github.com/owner/repo.git",
			cacheDir: "/base/github.com/owner/repo", kind: SourceKindRemote},
		{name: "git url with .git", in: "git://github.com/owner/repo.git",
			identity: "git://github.com/owner/repo.git", transport: "git://github.com/owner/repo.git",
			cacheDir: "/base/github.com/owner/repo", kind: SourceKindRemote},

		// --- sentinels --------------------------------------------------------
		// Not URLs. NormalizeURL used to mangle them into "https://ctxloom:local",
		// which trust.CanonicalRepoURL had to intercept with early returns
		// before ever calling it.
		{name: "local sentinel", in: LocalSource,
			identity: LocalSource, transport: LocalSource, cacheDir: "", kind: SourceKindLocal},
		{name: "companion sentinel", in: CompanionSource,
			identity: CompanionSource, transport: CompanionSource, cacheDir: "", kind: SourceKindCompanion},

		// --- degenerate -------------------------------------------------------
		{name: "whitespace padded", in: "  https://github.com/owner/repo  ",
			identity: "https://github.com/owner/repo", transport: "https://github.com/owner/repo",
			cacheDir: "/base/github.com/owner/repo", kind: SourceKindRemote},
		{name: "bare scheme", in: "https://",
			identity: "https://", transport: "https://", cacheDir: "", kind: SourceKindRemote},
		{name: "dot", in: ".",
			identity: "https://.", transport: "https://.", cacheDir: "", kind: SourceKindRemote},
		{name: "dotdot", in: "..",
			identity: "https://..", transport: "https://..", cacheDir: "", kind: SourceKindRemote},
	}
}

func TestRepoURL_Characterization(t *testing.T) {
	cache := NewRepoCache("/base", AuthConfig{})
	for _, tc := range repoURLCases() {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.identity, NormalizeURL(tc.in), "identity (NormalizeURL)")
			assert.Equal(t, tc.transport, normalizeCloneURL(tc.in), "transport (normalizeCloneURL)")

			dir, err := cache.RepoDirForURL(tc.in)
			if tc.cacheDir == "" {
				assert.Error(t, err, "a URL naming no repository must never resolve to a cache path — the result is RemoveAll'd")
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.cacheDir, dir, "filesystem (RepoDirForURL)")
			}

			parsed, perr := ParseRepoURL(tc.in)
			require.NoError(t, perr)
			assert.Equal(t, tc.kind, parsed.Kind(), "dispatch (Kind)")
		})
	}
}

// TestRepoURL_IdentityAndTransportAgree is the assertion the old code could not
// have made. Every input except the scp-like form must render IDENTICALLY for
// identity and transport: they are different concerns, but they are not allowed
// to disagree about what repository a string names.
//
// It bites: before the shared parse, "owner/repo.js" rendered as
// "https://github.com/owner/repo.js" for identity and the bare relative path
// "owner/repo.js" for transport, and "gitlab.com/alice/repo" named two
// different repositories depending on which helper you asked.
func TestRepoURL_IdentityAndTransportAgree(t *testing.T) {
	for _, tc := range repoURLCases() {
		if tc.scpTransportDiffers {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, NormalizeURL(tc.in), normalizeCloneURL(tc.in),
				"identity and transport must name the same repository for %q", tc.in)
		})
	}
}

// TestRepoURL_ScpDiffersOnlyInTransport pins the one legitimate divergence, so
// that it stays deliberate. Identity folds scp onto https (one repo, one trust
// key, whatever transport you cloned it over); transport keeps scp, because
// rewriting it to https would silently discard the user's ssh credentials.
func TestRepoURL_ScpDiffersOnlyInTransport(t *testing.T) {
	parsed, err := ParseRepoURL("git@github.com:owner/repo.git")
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/owner/repo.git", parsed.Normalized())
	assert.Equal(t, "git@github.com:owner/repo.git", parsed.CloneArg())

	// ...and the https spelling of the same repo agrees on identity, which is
	// what makes an approval portable between a lead who clones over ssh and a
	// developer who clones over https (signature-envelope spec 1.4). Both
	// spellings here are CLONE urls, which is what a forge hands out for both
	// transports and therefore what a user actually pastes; GitHub's ssh and
	// https clone urls both end ".git".
	https, err := ParseRepoURL("https://github.com/owner/repo.git")
	require.NoError(t, err)
	assert.Equal(t, https.Normalized(), parsed.Normalized(),
		"transport must not be part of identity")
}

// TestRepoURL_CloneAndBrowserSpellingsAreDifferentIdentities pins the accepted
// COST of preserving every spelling, so that it is a decision on the record
// rather than a surprise in a support thread.
//
// A forge's clone url ends ".git"; the url in the browser bar does not. Under
// byte-exact identity those are two addresses, so a user who approved content
// under one and later pastes the other is prompted again. That is the trade:
// whether the two reach one repository is host-specific knowledge ctxloom does
// not have, and a wrong guess is not a duplicate prompt but a merged trust key,
// where a rejection of one silently governs the other. A duplicate prompt is
// recoverable; a merged key is not.
//
// The cross-address case that MATTERS is still covered, one layer up: a
// content-reject is signed with the ref omitted, so it follows the bytes.
func TestRepoURL_CloneAndBrowserSpellingsAreDifferentIdentities(t *testing.T) {
	clone, err := ParseRepoURL("https://github.com/owner/repo.git")
	require.NoError(t, err)
	browser, err := ParseRepoURL("https://github.com/owner/repo")
	require.NoError(t, err)
	assert.NotEqual(t, clone.Normalized(), browser.Normalized(),
		"two spellings collapsed onto one identity — that is a merged trust key, not a convenience")

	// They still share ONE clone directory: the filesystem question is "which
	// bytes are on disk", and there the two are the same repository.
	cloneDir, err := clone.CacheSegments()
	require.NoError(t, err)
	browserDir, err := browser.CacheSegments()
	require.NoError(t, err)
	assert.Equal(t, browserDir, cloneDir, "one repository must not be cloned twice")
}

// TestRepoURL_NonHTTPPathsAreBytePreserved is the fail-open guard. A file://,
// ssh:// or git:// URL names a repository that really exists, so its trust key
// must survive this refactor byte-for-byte: a moved key is a countersignature
// store MISS, and a miss on a REJECTION does not fail closed — EffectiveTrust
// step 1 stops matching and step 5 can allow the item on its publisher
// signature instead.
//
// Only http(s) may be folded, and only in ways trust.CanonicalRepoURL already
// performs downstream (so the key does not move).
func TestRepoURL_NonHTTPPathsAreBytePreserved(t *testing.T) {
	for _, in := range []string{
		"file:///srv/repos/foo/",
		"file:///srv/repos/foo.git",
		"file:///srv/repos/foo.git/",
		"file:///srv/repos/Foo",
		"ssh://git@github.com/owner/repo/",
		"ssh://git@github.com/owner/repo.git",
		"ssh://git@host:2222/owner/repo",
		"git://github.com/owner/repo/",
		"git://github.com/owner/repo.git",
	} {
		assert.Equal(t, in, NormalizeURL(in),
			"%q names a real repository: folding it would move its trust key and drop any rejection recorded under the old spelling", in)
		assert.Equal(t, in, normalizeCloneURL(in),
			"%q is a path on the far side, not a forge URL: git must receive it as written", in)
	}
}

// TestRepoURL_CacheDirNeverResolvesToRoot pins, across the grammar, that the
// returned directory is RemoveAll'd and cloned into, so "equals the cache root"
// is exactly as dangerous as "escapes it", and neither may be answered with a
// silent fallback.
func TestRepoURL_CacheDirNeverResolvesToRoot(t *testing.T) {
	cache := NewRepoCache("/base", AuthConfig{})
	for _, in := range []string{
		"", "https://", ".", "..", "../..", "https://../..",
		LocalSource, CompanionSource, "ctxloom:local@bundles/x",
	} {
		dir, err := cache.RepoDirForURL(in)
		assert.Error(t, err, "RepoDirForURL(%q) must error rather than name a directory", in)
		assert.Empty(t, dir, "RepoDirForURL(%q) must not return a path alongside its error", in)
	}
}

// TestRepoURL_OwnerRepoSharesTheGrammar pins the fifth renderer. It was a
// standalone implementation (named ParseRepoURL) with its own shorthand arm,
// its own scp arm and its own .git handling — which is why shorthand used to
// yield the repo name "ctxloom.git".
func TestRepoURL_OwnerRepoSharesTheGrammar(t *testing.T) {
	for _, tc := range []struct{ in, owner, repo string }{
		{"alice/ctxloom", "alice", "ctxloom"},
		{"alice/ctxloom.git", "alice", "ctxloom"},
		{"https://github.com/alice/ctxloom.git", "alice", "ctxloom"},
		{"https://github.com/alice/ctxloom/", "alice", "ctxloom"},
		{"git@github.com:alice/ctxloom.git", "alice", "ctxloom"},
		{"gitlab.com/alice/ctxloom", "alice", "ctxloom"},
	} {
		owner, repo, err := ParseOwnerRepo(tc.in)
		require.NoError(t, err, tc.in)
		assert.Equal(t, tc.owner, owner, tc.in)
		assert.Equal(t, tc.repo, repo, tc.in)
	}

	// Shorthand stays held to EXACTLY two segments: reading "a/b/c" as
	// ("a","b") would build a plausible forge request for a repository the
	// user did not name.
	_, _, err := ParseOwnerRepo("a/b/c")
	assert.Error(t, err)
}
