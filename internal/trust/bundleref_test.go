package trust

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- grammar: all five classes parse and round-trip -------------------------

func TestBundleRef_AllFiveClassesRoundTrip(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		class    SourceClass
		host     string
		repoPath string
		bundle   string
		version  string
		kind     ItemKind
		item     string
	}{
		{
			name:  "git with version and selector",
			in:    "ctxloom+git://github.com/acme/repo//bundles/code-quality@v1.2.0#fragments/solid",
			class: ClassGit, host: "github.com", repoPath: "/acme/repo",
			bundle: "code-quality", version: "v1.2.0", kind: KindFragment, item: "solid",
		},
		{
			name:  "git with host:port",
			in:    "ctxloom+git://git.example.com:2222/acme/repo//bundles/x#mcp/pg",
			class: ClassGit, host: "git.example.com:2222", repoPath: "/acme/repo",
			bundle: "x", kind: KindMCP, item: "pg",
		},
		{
			name:  "file with nested bundle and skill selector",
			in:    "ctxloom+file:///srv/content//bundles/lang/go#skills/kit",
			class: ClassFile, repoPath: "/srv/content", bundle: "lang/go",
			kind: KindSkill, item: "kit",
		},
		{
			name:  "builtin",
			in:    "ctxloom+builtin:isolation#fragments/isolation-axes",
			class: ClassBuiltin, bundle: "isolation", kind: KindFragment, item: "isolation-axes",
		},
		{
			name:  "local nested",
			in:    "ctxloom+local:lang/go#fragments/a",
			class: ClassLocal, bundle: "lang/go", kind: KindFragment, item: "a",
		},
		{
			name:  "companion",
			in:    "ctxloom+companion:taskloom#prompts/plan",
			class: ClassCompanion, bundle: "taskloom", kind: KindPrompt, item: "plan",
		},
		{
			name:  "bundle-only reference carries no selector",
			in:    "ctxloom+git://github.com/acme/repo//bundles/code-quality",
			class: ClassGit, host: "github.com", repoPath: "/acme/repo", bundle: "code-quality",
		},
		{
			name:  "hook item name carries an inner slash",
			in:    "ctxloom+local:tooling#hooks/PreToolUse/0",
			class: ClassLocal, bundle: "tooling", kind: KindHook, item: "PreToolUse/0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseBundleRef(tt.in)
			require.NoError(t, err)

			assert.Equal(t, tt.class, got.Class, "class")
			assert.Equal(t, tt.host, got.Host, "host")
			assert.Equal(t, tt.repoPath, got.RepoPath, "repo path")
			assert.Equal(t, tt.bundle, got.Bundle, "bundle")
			assert.Equal(t, tt.version, got.Version, "version")
			assert.Equal(t, tt.kind, got.Kind, "kind")
			assert.Equal(t, tt.item, got.Item, "item")

			// Round-trip: the rendered form must be the input byte-for-byte,
			// and reparsing it must yield an EQUAL struct. Asserting only
			// "String() parses" would pass for a String() that dropped a field.
			assert.Equal(t, tt.in, got.String(), "String() is not the input")
			again, err := ParseBundleRef(got.String())
			require.NoError(t, err)
			assert.Equal(t, got, again, "parse is not idempotent")
		})
	}
}

// TestBundleRef_VariableDepthBothSides pins the case that forces the "//"
// separator to exist at all: GitLab subgroups make the repo path variable
// depth, and nested bundles make the bundle path variable depth, so no segment
// count can split them.
func TestBundleRef_VariableDepthBothSides(t *testing.T) {
	const in = "ctxloom+git://gitlab.com/a/b/c/repo//bundles/lang/go"

	got, err := ParseBundleRef(in)
	require.NoError(t, err)

	assert.Equal(t, "/a/b/c/repo", got.RepoPath, "repo half not recovered")
	assert.Equal(t, "lang/go", got.Bundle, "bundle half not recovered")
	assert.Equal(t, in, got.String())
}

// --- R1: normalize at the parse boundary ------------------------------------

func TestBundleRef_R1_NormalizationAtParseBoundary(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			// url.Parse REJECTS a percent-escape in the authority, so this
			// only works because decodeUnreservedEscapes runs BEFORE the parse.
			name: "unreserved escape in host is decoded",
			in:   "ctxloom+git://github%2Ecom/acme/repo//bundles/x",
			want: "ctxloom+git://github.com/acme/repo//bundles/x",
		},
		{
			name: "unreserved escape in bundle name is decoded",
			in:   "ctxloom+local:co%64e#fragments/a",
			want: "ctxloom+local:code#fragments/a",
		},
		{
			name: "lowercase percent hex is uppercased",
			in:   "ctxloom+file:///srv/we%7cird//bundles/a%7cb",
			want: "ctxloom+file:///srv/we%7Cird//bundles/a%7Cb",
		},
		{
			// The case above does NOT exercise upperHex: a repo/bundle path is
			// stored DECODED and re-encoded by url.URL, whose encoder already
			// emits uppercase hex. Version is the one field kept escaped and
			// emitted verbatim, so §6.2.2.2 reaches it only via upperHex —
			// neutering upperHex is invisible everywhere else and visible here.
			name: "lowercase percent hex is uppercased in a version",
			in:   "ctxloom+git://github.com/acme/repo//bundles/x@v1%7cx",
			want: "ctxloom+git://github.com/acme/repo//bundles/x@v1%7Cx",
		},
		{
			name: "unreserved escape in a version is decoded",
			in:   "ctxloom+git://github.com/acme/repo//bundles/x@v1%2Ex",
			want: "ctxloom+git://github.com/acme/repo//bundles/x@v1.x",
		},
		{
			name: "raw pipe in a version is percent-encoded",
			in:   "ctxloom+git://github.com/acme/repo//bundles/x@v1|x",
			want: "ctxloom+git://github.com/acme/repo//bundles/x@v1%7Cx",
		},
		{
			name: "scheme is lowercased",
			in:   "CTXLOOM+GIT://github.com/acme/repo//bundles/x",
			want: "ctxloom+git://github.com/acme/repo//bundles/x",
		},
		{
			name: "host is lowercased",
			in:   "ctxloom+git://Git.Example.COM/acme/repo//bundles/x",
			want: "ctxloom+git://git.example.com/acme/repo//bundles/x",
		},
		{
			name: "trailing slash is dropped",
			in:   "ctxloom+git://github.com/acme/repo//bundles/x/",
			want: "ctxloom+git://github.com/acme/repo//bundles/x",
		},
		{
			name: "empty query is dropped",
			in:   "ctxloom+git://github.com/acme/repo//bundles/x?",
			want: "ctxloom+git://github.com/acme/repo//bundles/x",
		},
		{
			// "|" is not unreserved, not a sub-delim and not a pchar. It must
			// be percent-encoded on output, never passed through: it is the
			// character the old CanonicalURL()+"|"+Key() join used as a
			// delimiter (see R5).
			name: "raw pipe in a file repo path is percent-encoded",
			in:   "ctxloom+file:///srv/we|ird//bundles/x",
			want: "ctxloom+file:///srv/we%7Cird//bundles/x",
		},
		{
			name: "raw pipe in an item name is percent-encoded",
			in:   "ctxloom+local:tooling#fragments/a|b",
			want: "ctxloom+local:tooling#fragments/a%7Cb",
		},
		{
			// An internal class's name gets its escaping ONLY from render's
			// explicit call: url.URL emits Opaque verbatim.
			name: "raw pipe in an internal-class bundle name is percent-encoded",
			in:   "ctxloom+companion:we|ird#fragments/a",
			want: "ctxloom+companion:we%7Cird#fragments/a",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseBundleRef(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.String())

			// The point of normalizing at the boundary is that the two
			// spellings become ONE store address. Assert that directly:
			// asserting only the rendered string would still pass if
			// Identity() rendered from unnormalized state.
			canonical, err := ParseBundleRef(tt.want)
			require.NoError(t, err)
			assert.Equal(t, canonical.Identity(), got.Identity(),
				"two spellings of one reference did not collapse to one identity")
		})
	}
}

// TestBundleRef_R1_PipeAndControlCharactersNeverPassThrough pins the negative
// half of R1: the characters that cannot be carried are refused or encoded,
// never emitted raw.
func TestBundleRef_R1_PipeAndControlCharactersNeverPassThrough(t *testing.T) {
	t.Run("pipe never survives into a rendered reference", func(t *testing.T) {
		for _, in := range []string{
			"ctxloom+file:///srv/we|ird//bundles/x",
			"ctxloom+file:///srv/repo//bundles/a|b",
			"ctxloom+local:tooling#fragments/a|b",
			"ctxloom+builtin:we|ird",
		} {
			got, err := ParseBundleRef(in)
			require.NoError(t, err, in)
			assert.NotContains(t, got.String(), "|", "raw pipe survived rendering of %q", in)
			assert.NotContains(t, got.Identity(), "|", "raw pipe survived Identity of %q", in)
		}
	})

	t.Run("control characters are refused, not stripped", func(t *testing.T) {
		// A newline is the one that forges the countersign frame: it closes
		// the "ref:" line early and lets the rest of the ref write the header
		// lines that follow.
		for _, in := range []string{
			"ctxloom+local:tool\ning#fragments/a",
			"ctxloom+local:tooling#fragments/a\rb",
			"ctxloom+file:///srv/re\x00po//bundles/x",
			"ctxloom+builtin:iso\x7flation",
		} {
			_, err := ParseBundleRef(in)
			require.Error(t, err, "accepted a control character in %q", in)
			assert.ErrorIs(t, err, ErrRefSyntax)
		}
	})

	t.Run("encoded slash is refused", func(t *testing.T) {
		// %2F is the one escape whose decoding changes structure, so it cannot
		// round-trip through a decoded field. Both hex cases must be caught.
		for _, in := range []string{
			"ctxloom+file:///srv/a%2Fb//bundles/x",
			"ctxloom+file:///srv/a%2fb//bundles/x",
			"ctxloom+local:a%2Fb",
		} {
			_, err := ParseBundleRef(in)
			require.Error(t, err, "accepted an encoded slash in %q", in)
			assert.ErrorIs(t, err, ErrRefSyntax)
		}
	})
}

// TestIsRefControlRune_ExhaustiveC0AndDEL is a direct, white-box test of the
// predicate itself — the trust-package mirror of
// remote.TestStripRefControlChars_ExhaustiveC0AndDEL. Every one of the 33 code
// points named by the comment (C0 0x00-0x1F, plus DEL 0x7F) must match, and
// nothing else in the 0x00-0xFF byte range may. This is the exact parity claim
// the audit exists to pin: the two predicates share one formula
// (r < 0x20 || r == 0x7f) in two packages, and a divergence between them would
// mean the two ref grammars disagree about what a "safe" reference can carry.
func TestIsRefControlRune_ExhaustiveC0AndDEL(t *testing.T) {
	for r := 0; r < 0x100; r++ {
		r := rune(r)
		want := r < 0x20 || r == 0x7f
		assert.Equal(t, want, isRefControlRune(r), "isRefControlRune(%#U) mismatch", r)
	}
}

// TestBundleRef_ControlCharacters_ExhaustiveRefusal is the end-to-end
// counterpart: every control code point must make ParseBundleRef fail,
// wherever in the raw ref string it appears.
//
// The two positions below are NOT equally strong evidence for isRefControlRune
// specifically, and the difference matters enough to spell out. Confirmed by
// mutating isRefControlRune to always return false (restored after):
//
//   - bundle-name position still refused every case — net/url.Parse carries
//     its OWN, entirely independent control-byte check (stringContainsCTLByte)
//     over the pre-'#' portion of the string, so this half is redundant
//     defense-in-depth, not proof isRefControlRune fired.
//   - item-name position (after '#') stopped being refused for EVERY control
//     code point with isRefControlRune disabled. net/url.Parse cuts the
//     fragment off BEFORE running its control-byte check (see url.Parse's
//     "Cut off #frag" step) and setFragment never re-checks it — an
//     unescaped control byte there is percent-escaped on render, not refused.
//     isRefControlRune is the ONLY guard standing between argv/an advisory and
//     a control character surviving into an item name.
//
// So the item-name-position half is the one that actually pins isRefControlRune
// down; the bundle-name-position half is kept because losing the net/url
// redundancy would also be a real regression worth catching, just a different
// and less critical one.
func TestBundleRef_ControlCharacters_ExhaustiveRefusal(t *testing.T) {
	isControl := func(r rune) bool { return r < 0x20 || r == 0x7f }

	for r := rune(0); r < 0x80; r++ {
		if !isControl(r) {
			continue
		}
		t.Run(fmt.Sprintf("bundle-name position %#U", r), func(t *testing.T) {
			in := "ctxloom+local:tool" + string(r) + "ing#fragments/a"
			_, err := ParseBundleRef(in)
			require.Error(t, err, "accepted control character %#U in the bundle name", r)
			assert.ErrorIs(t, err, ErrRefSyntax)
		})
		t.Run(fmt.Sprintf("item-name position %#U", r), func(t *testing.T) {
			in := "ctxloom+local:tooling#fragments/a" + string(r) + "b"
			_, err := ParseBundleRef(in)
			require.Error(t, err, "accepted control character %#U in the item name", r)
			assert.ErrorIs(t, err, ErrRefSyntax)
		})
	}
}

// --- R2: repo-path case — PRESERVED byte-exact on every host ---------------

func TestBundleRef_R2_RepoPathCase(t *testing.T) {
	t.Run("uppercase is PRESERVED on a case-folding forge, not refused and not rewritten", func(t *testing.T) {
		// A forge that serves "Foo/Bar" and "foo/bar" as one repository is
		// host-specific knowledge this grammar does not have. Folding on it
		// merges two identities onto one trust key; refusing makes a real
		// repository unaddressable. Neither: the spelling is preserved.
		for _, host := range []string{"github.com", "gitlab.com", "bitbucket.org"} {
			in := "ctxloom+git://" + host + "/Acme/Repo//bundles/x"

			got, err := ParseBundleRef(in)
			require.NoError(t, err, "%s refused an uppercase repo path", host)
			assert.Equal(t, "/Acme/Repo", got.RepoPath, "%s rewrote the repo path", host)
			assert.Equal(t, in, got.String(), "%s did not round-trip the spelling", host)
		}
	})

	t.Run("two case spellings on a case-folding forge are DIFFERENT identities", func(t *testing.T) {
		upper, err := ParseBundleRef("ctxloom+git://github.com/Acme/Repo//bundles/x")
		require.NoError(t, err)
		lower, err := ParseBundleRef("ctxloom+git://github.com/acme/repo//bundles/x")
		require.NoError(t, err)
		assert.NotEqual(t, upper.Identity(), lower.Identity(),
			"two spellings collapsed onto one trust key")
	})

	t.Run("case is PRESERVED on a case-sensitive git server", func(t *testing.T) {
		// The conformant behaviour (RFC 3986 6.2.2.1: a URI path is
		// case-sensitive). These servers must stay addressable.
		got, err := ParseBundleRef("ctxloom+git://git.example.com/Acme/Repo//bundles/x")
		require.NoError(t, err)
		assert.Equal(t, "/Acme/Repo", got.RepoPath)
		assert.Equal(t, "ctxloom+git://git.example.com/Acme/Repo//bundles/x", got.String())
	})

	t.Run("two case spellings on a case-sensitive server are DIFFERENT identities", func(t *testing.T) {
		upper, err := ParseBundleRef("ctxloom+git://git.example.com/Acme/Repo//bundles/x")
		require.NoError(t, err)
		lower, err := ParseBundleRef("ctxloom+git://git.example.com/acme/repo//bundles/x")
		require.NoError(t, err)
		assert.NotEqual(t, upper.Identity(), lower.Identity(),
			"folded two distinct repositories on a case-sensitive server onto one key")
	})

	t.Run("file paths keep their case", func(t *testing.T) {
		got, err := ParseBundleRef("ctxloom+file:///srv/Content//bundles/x")
		require.NoError(t, err)
		assert.Equal(t, "/srv/Content", got.RepoPath)
	})

	t.Run("lowercase on a folding forge is accepted unchanged", func(t *testing.T) {
		got, err := ParseBundleRef("ctxloom+git://github.com/acme/repo//bundles/x")
		require.NoError(t, err)
		assert.Equal(t, "/acme/repo", got.RepoPath)
	})
}

// --- R3: bundle/item name case preserved; same-fold collisions refused ------

func TestBundleRef_R3_NameCasePreserved(t *testing.T) {
	got, err := ParseBundleRef("ctxloom+git://github.com/acme/repo//bundles/Code-Quality#fragments/SOLID")
	require.NoError(t, err)

	assert.Equal(t, "Code-Quality", got.Bundle, "bundle name case was not preserved")
	assert.Equal(t, "SOLID", got.Item, "item name case was not preserved")
	assert.Equal(t, "ctxloom+git://github.com/acme/repo//bundles/Code-Quality#fragments/SOLID", got.String())
}

func TestBundleRef_R3_SameFoldCollisionsRefused(t *testing.T) {
	mustParse := func(s string) BundleRef {
		t.Helper()
		r, err := ParseBundleRef(s)
		require.NoError(t, err)
		return r
	}

	t.Run("item names differing only by case REFUSE, naming both", func(t *testing.T) {
		refs := []BundleRef{
			mustParse("ctxloom+local:iso#fragments/Isolation"),
			mustParse("ctxloom+local:iso#fragments/isolation"),
		}
		err := CheckBundleRefFoldCollisions(refs)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrRefNameCollision)
		// "naming both" is the whole value: a bare "collision" tells the user
		// nothing about which two files to rename.
		assert.Contains(t, err.Error(), "fragments/Isolation")
		assert.Contains(t, err.Error(), "fragments/isolation")
	})

	t.Run("bundle names differing only by case REFUSE, naming both", func(t *testing.T) {
		refs := []BundleRef{
			mustParse("ctxloom+file:///srv/repo//bundles/Lang"),
			mustParse("ctxloom+file:///srv/repo//bundles/lang"),
		}
		err := CheckBundleRefFoldCollisions(refs)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrRefNameCollision)
		assert.Contains(t, err.Error(), "bundles/Lang")
		assert.Contains(t, err.Error(), "bundles/lang")
	})

	t.Run("identical refs are not a collision", func(t *testing.T) {
		refs := []BundleRef{
			mustParse("ctxloom+local:iso#fragments/isolation"),
			mustParse("ctxloom+local:iso#fragments/isolation"),
		}
		assert.NoError(t, CheckBundleRefFoldCollisions(refs))
	})

	t.Run("genuinely distinct names are not a collision", func(t *testing.T) {
		refs := []BundleRef{
			mustParse("ctxloom+local:iso#fragments/readme"),
			mustParse("ctxloom+local:iso#fragments/license"),
			mustParse("ctxloom+local:other#fragments/readme"),
		}
		assert.NoError(t, CheckBundleRefFoldCollisions(refs))
	})

	t.Run("same name under DIFFERENT sources is not a collision", func(t *testing.T) {
		// Distinct sources are distinct namespaces; folding across them would
		// merge two unrelated items.
		refs := []BundleRef{
			mustParse("ctxloom+local:iso#fragments/isolation"),
			mustParse("ctxloom+builtin:iso#fragments/Isolation"),
		}
		assert.NoError(t, CheckBundleRefFoldCollisions(refs))
	})

	t.Run("repo paths differing by case on a case-sensitive server are NOT a collision", func(t *testing.T) {
		// R2 keeps these addressable as two repositories; R3 must not undo
		// that by reporting them as one name spelled two ways.
		refs := []BundleRef{
			mustParse("ctxloom+git://git.example.com/Acme/repo//bundles/x"),
			mustParse("ctxloom+git://git.example.com/acme/repo//bundles/x"),
		}
		assert.NoError(t, CheckBundleRefFoldCollisions(refs))
	})
}

// --- R4: dot segments, split on "//" first ---------------------------------

// TestBundleRef_R4_SeparatorSurvivesDotSegments is the test named in
// resolveDotSegmentsEachSide's doc. Swapping that function for path.Clean
// makes it fail: path.Clean collapses "//" into "/", merging the repository
// path into the bundle path.
func TestBundleRef_R4_SeparatorSurvivesDotSegments(t *testing.T) {
	// A repository whose own path contains a segment literally named
	// "bundles". The separator is the EMPTY SEGMENT, never the marker word, so
	// a rewrite that resolves the two halves as one string and then re-splits
	// by searching for "bundles/" must not cut here. Searching forwards cuts
	// at /srv and calls "bundles/repo//bundles/x" the bundle path — a second,
	// different reference that still parses.
	t.Run("a literal bundles segment in the repo half is not the separator", func(t *testing.T) {
		got, err := ParseBundleRef("ctxloom+file:///srv/bundles/repo//bundles/x")
		require.NoError(t, err)

		assert.Equal(t, "/srv/bundles/repo", got.RepoPath)
		assert.Equal(t, "x", got.Bundle)
		assert.Equal(t, "ctxloom+file:///srv/bundles/repo//bundles/x", got.String())
	})

	// The mirror image: a bundle whose own path contains a segment named
	// "bundles". Searching BACKWARD through a merged path cuts at the second
	// marker, moving "a" into the repository path and leaving "b" as the
	// bundle — again a different reference that still parses. Together with
	// the case above, the two pin the separator to the empty segment: neither
	// end of a marker search is a legal substitute for it.
	t.Run("a literal bundles segment in the bundle half is not the separator", func(t *testing.T) {
		got, err := ParseBundleRef("ctxloom+file:///srv/repo//bundles/a/bundles/b")
		require.NoError(t, err)

		assert.Equal(t, "/srv/repo", got.RepoPath)
		assert.Equal(t, "a/bundles/b", got.Bundle)
		assert.Equal(t, "ctxloom+file:///srv/repo//bundles/a/bundles/b", got.String())
	})

	t.Run("the separator survives a reference with no dot segments at all", func(t *testing.T) {
		got, err := ParseBundleRef("ctxloom+git://github.com/acme/repo//bundles/lang/go")
		require.NoError(t, err)

		// Assert the two halves SEPARATELY, not just the rendered string: a
		// merged path would still render as some valid-looking reference.
		assert.Equal(t, "/acme/repo", got.RepoPath)
		assert.Equal(t, "lang/go", got.Bundle)
		assert.Equal(t, "ctxloom+git://github.com/acme/repo//bundles/lang/go", got.String())
		assert.Contains(t, got.Identity(), "//bundles/",
			"the repo/bundle separator was collapsed out of the identity")
	})

	t.Run("dot segments resolve within each half", func(t *testing.T) {
		got, err := ParseBundleRef("ctxloom+git://github.com/acme/./sub/../repo//bundles/lang/./sub/../go")
		require.NoError(t, err)

		assert.Equal(t, "/acme/repo", got.RepoPath, "repo half not resolved")
		assert.Equal(t, "lang/go", got.Bundle, "bundle half not resolved")
		assert.Equal(t, "ctxloom+git://github.com/acme/repo//bundles/lang/go", got.String())
	})

	t.Run("a .. in the bundle half cannot climb into the repo half", func(t *testing.T) {
		// Resolved as ONE string, "bundles/../../x" would eat "repo" off the
		// repository path and address a different repository. Resolved per
		// half, the climb runs out of segments inside its own half, loses the
		// "bundles/" marker, and the reference is REFUSED -- it can never
		// reach across the separator.
		for _, in := range []string{
			"ctxloom+git://github.com/acme/repo//bundles/../../../x",
			"ctxloom+git://github.com/acme/repo//bundles/sub/../../x",
			"ctxloom+git://github.com/acme/repo//bundles/../x",
		} {
			got, err := ParseBundleRef(in)
			require.Error(t, err, "accepted a climb out of the bundle half: %q", in)
			assert.ErrorIs(t, err, ErrRefSyntax)
			assert.Equal(t, BundleRef{}, got, "a refused climb must not return a reference")
		}
	})

	t.Run("a .. inside the bundle half resolves without touching the repo half", func(t *testing.T) {
		got, err := ParseBundleRef("ctxloom+git://github.com/acme/repo//bundles/lang/py/../go")
		require.NoError(t, err)
		assert.Equal(t, "/acme/repo", got.RepoPath)
		assert.Equal(t, "lang/go", got.Bundle)
	})

	t.Run("encoded dot segments are resolved, not smuggled past", func(t *testing.T) {
		// %2E is unreserved, so 6.2.2.2 decodes it BEFORE 6.2.2.3 removes dot
		// segments. Getting the order wrong leaves "%2E%2E" as an opaque
		// segment here while a consumer that decodes first sees "..".
		got, err := ParseBundleRef("ctxloom+git://github.com/acme/sub/%2E%2E/repo//bundles/x")
		require.NoError(t, err)
		assert.Equal(t, "/acme/repo", got.RepoPath)
		assert.NotContains(t, got.String(), "%2E")
		assert.NotContains(t, got.String(), "..")
	})

	t.Run("a reference with no separator is refused", func(t *testing.T) {
		_, err := ParseBundleRef("ctxloom+git://github.com/acme/repo/bundles/x")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrRefSyntax)
	})
}

// --- R5: no pipe-join; one injective canonical string ----------------------

// TestBundleRef_R5_IdentityIsInjectiveWhereThePipeJoinWasNot reproduces the
// framing hazard the "|" join carried and shows Identity does not have it.
func TestBundleRef_R5_IdentityIsInjectiveWhereThePipeJoinWasNot(t *testing.T) {
	// Two DIFFERENT references. Under CanonicalURL()+"|"+Key() the "|" is a
	// delimiter that either component may itself contain, so the pair below
	// renders one string for two tuples -- and remote.NormalizeRef does not
	// stop it, because it strips CONTROL characters and "|" is 0x7C.
	const (
		sourceA, keyA = "S", "a|b"
		sourceB, keyB = "S|a", "b"
	)
	require.Equal(t, sourceA+"|"+keyA, sourceB+"|"+keyB,
		"premise broken: the pipe join no longer collides, so this test proves nothing")

	// The same two tuples in the new grammar: source "S" holding bundle "a|b",
	// and source "S|a" holding bundle "b".
	refA, err := ParseBundleRef("ctxloom+file:///S//bundles/a|b")
	require.NoError(t, err)
	refB, err := ParseBundleRef("ctxloom+file:///S|a//bundles/b")
	require.NoError(t, err)

	assert.NotEqual(t, refA.Identity(), refB.Identity(),
		"two distinct references collapsed onto one store address")
	assert.NotContains(t, refA.Identity(), "|", "an unescaped pipe reached the signature preimage")
	assert.NotContains(t, refB.Identity(), "|", "an unescaped pipe reached the signature preimage")
}

func TestBundleRef_R5_IdentityCarriesSourceBundleAndItemInOneString(t *testing.T) {
	// The join existed because Key() alone omits the repo, so two repos
	// publishing a same-named bundle would collide. One canonical URI already
	// carries all three components, which is what makes the join redundant.
	a, err := ParseBundleRef("ctxloom+git://github.com/acme/repo//bundles/x#fragments/f")
	require.NoError(t, err)
	b, err := ParseBundleRef("ctxloom+git://github.com/other/repo//bundles/x#fragments/f")
	require.NoError(t, err)

	assert.NotEqual(t, a.Identity(), b.Identity(),
		"same-named bundles from different repositories collided")
	assert.Contains(t, a.Identity(), "acme")
	assert.Contains(t, a.Identity(), "bundles/x")
	assert.Contains(t, a.Identity(), "fragments/f")
}

func TestBundleRef_R5_IdentityOmitsVersion(t *testing.T) {
	// Grants pin by content hash, not by commit, so identity must not move
	// with the version -- matching Ref.Key's own omission.
	pinned, err := ParseBundleRef("ctxloom+git://github.com/acme/repo//bundles/x@v1.2.0#fragments/f")
	require.NoError(t, err)
	floating, err := ParseBundleRef("ctxloom+git://github.com/acme/repo//bundles/x#fragments/f")
	require.NoError(t, err)

	assert.Equal(t, "v1.2.0", pinned.Version)
	assert.Equal(t, floating.Identity(), pinned.Identity(), "version leaked into identity")
	assert.NotContains(t, pinned.Identity(), "v1.2.0")

	// String, unlike Identity, keeps it.
	assert.Contains(t, pinned.String(), "@v1.2.0")
}

func TestBundleRef_BundleIdentityDropsTheItem(t *testing.T) {
	item, err := ParseBundleRef("ctxloom+git://github.com/acme/repo//bundles/x#fragments/f")
	require.NoError(t, err)
	assert.Equal(t, "ctxloom+git://github.com/acme/repo//bundles/x", item.BundleIdentity())
	assert.True(t, item.IsItem())

	bundle, err := ParseBundleRef("ctxloom+git://github.com/acme/repo//bundles/x")
	require.NoError(t, err)
	assert.False(t, bundle.IsItem())
	assert.Equal(t, bundle.Identity(), item.BundleIdentity())
}

// --- version delimiter ------------------------------------------------------

// TestBundleRef_AtInANameIsDataNotAVersion pins that escapeAt is load-bearing:
// without it a bundle literally named "na@me" round-trips as bundle "na" at
// version "me".
func TestBundleRef_AtInANameIsDataNotAVersion(t *testing.T) {
	got, err := ParseBundleRef("ctxloom+git://github.com/acme/repo//bundles/na%40me")
	require.NoError(t, err)
	assert.Equal(t, "na@me", got.Bundle)
	assert.Empty(t, got.Version)

	again, err := ParseBundleRef(got.String())
	require.NoError(t, err)
	assert.Equal(t, "na@me", again.Bundle, "the @ was re-read as a version delimiter")
	assert.Empty(t, again.Version)
}

// TestBundleRef_VersionOnInternalClassesIsUniform pins U3's decision on the
// one grammar divergence left open by U2: "@<version>" is accepted on the
// three internal classes (builtin/local/companion) exactly as it is on
// git/file, rather than being a git/file-only affordance. The grammar stays
// ONE rule ("at most one unescaped '@' before any '#' is a version, on every
// class") instead of a per-class carve-out, and Identity/BundleIdentity
// already drop Version uniformly, so accepting it here costs nothing: a
// companion or builtin ref MAY carry a diagnostic version (e.g. the
// companion binary's own release, or ctxloom's own build) without that
// version ever entering what the reference keys as.
func TestBundleRef_VersionOnInternalClassesIsUniform(t *testing.T) {
	for _, tt := range []struct {
		class SourceClass
		in    string
	}{
		{ClassBuiltin, "ctxloom+builtin:isolation@1.2.3"},
		{ClassLocal, "ctxloom+local:lang/go@1.2.3"},
		{ClassCompanion, "ctxloom+companion:taskloom@1.2.3"},
	} {
		t.Run(string(tt.class), func(t *testing.T) {
			got, err := ParseBundleRef(tt.in)
			require.NoError(t, err)
			assert.Equal(t, "1.2.3", got.Version)
			assert.Equal(t, tt.in, got.String())
			assert.NotContains(t, got.Identity(), "1.2.3",
				"Identity must omit the version even on an internal class")
		})
	}
}

// --- selector aliasing ------------------------------------------------------

func TestBundleRef_SelectorAliasCollapsesToOneIdentity(t *testing.T) {
	// "#commands/x" is the current spelling and "#prompts/x" the legacy alias;
	// both are KindPrompt, so both must key identically or a rejection
	// recorded under one spelling would miss the other.
	current, err := ParseBundleRef("ctxloom+local:tooling#commands/review")
	require.NoError(t, err)
	legacy, err := ParseBundleRef("ctxloom+local:tooling#prompts/review")
	require.NoError(t, err)

	assert.Equal(t, KindPrompt, current.Kind)
	assert.Equal(t, legacy.Identity(), current.Identity())
	assert.Equal(t, "ctxloom+local:tooling#prompts/review", current.String())
}

// --- minters ----------------------------------------------------------------

func TestBundleRef_MintersProduceParseableRefs(t *testing.T) {
	t.Run("GitRef", func(t *testing.T) {
		r, err := GitRef("GitHub.com", "acme/repo", "code-quality")
		require.NoError(t, err)
		assert.Equal(t, "ctxloom+git://github.com/acme/repo//bundles/code-quality", r.String())
	})

	t.Run("FileRef", func(t *testing.T) {
		r, err := FileRef("/srv/content", "lang/go")
		require.NoError(t, err)
		assert.Equal(t, "ctxloom+file:///srv/content//bundles/lang/go", r.String())
	})

	t.Run("BuiltinRef, LocalRef, CompanionRef", func(t *testing.T) {
		b, err := BuiltinRef("isolation")
		require.NoError(t, err)
		assert.Equal(t, "ctxloom+builtin:isolation", b.String())

		l, err := LocalRef("lang/go")
		require.NoError(t, err)
		assert.Equal(t, "ctxloom+local:lang/go", l.String())

		c, err := CompanionRef("taskloom")
		require.NoError(t, err)
		assert.Equal(t, "ctxloom+companion:taskloom", c.String())
	})

	t.Run("WithItem and WithVersion", func(t *testing.T) {
		r, err := GitRef("github.com", "acme/repo", "x")
		require.NoError(t, err)
		r, err = r.WithItem(KindFragment, "solid")
		require.NoError(t, err)
		r, err = r.WithVersion("v1.2.0")
		require.NoError(t, err)

		assert.Equal(t, "ctxloom+git://github.com/acme/repo//bundles/x@v1.2.0#fragments/solid", r.String())
		assert.Equal(t, "ctxloom+git://github.com/acme/repo//bundles/x#fragments/solid", r.Identity())
	})

	t.Run("a minter preserves a spelling the parser preserves", func(t *testing.T) {
		// The minting path and the parse path must agree on what identity a
		// spelling has, or a Go caller and a typed ref would key differently.
		r, err := GitRef("github.com", "Acme/Repo", "x")
		require.NoError(t, err)
		assert.Equal(t, "ctxloom+git://github.com/Acme/Repo//bundles/x", r.String())
	})

	t.Run("a minter escapes a hostile name rather than emitting it raw", func(t *testing.T) {
		r, err := LocalRef("we|ird")
		require.NoError(t, err)
		assert.Equal(t, "ctxloom+local:we%7Cird", r.String())
		assert.Equal(t, "we|ird", r.Bundle)
	})

	t.Run("minters refuse an empty name and a half-set selector", func(t *testing.T) {
		_, err := LocalRef("")
		assert.ErrorIs(t, err, ErrRefSyntax)

		r, err := LocalRef("x")
		require.NoError(t, err)
		_, err = r.WithItem(KindFragment, "")
		assert.ErrorIs(t, err, ErrRefSyntax)
	})
}

// --- syntax refusals --------------------------------------------------------

func TestBundleRef_SyntaxRefusals(t *testing.T) {
	for _, tt := range []struct{ name, in string }{
		{"empty", ""},
		{"unknown scheme", "https://github.com/acme/repo//bundles/x"},
		{"unknown ctxloom class", "ctxloom+svn://h/a//bundles/x"},
		{"git without a host", "ctxloom+git:///acme/repo//bundles/x"},
		{"file with a host", "ctxloom+file://elsewhere/srv//bundles/x"},
		{"missing bundles marker", "ctxloom+git://github.com/acme/repo//frags/x"},
		{"empty bundle name", "ctxloom+git://github.com/acme/repo//bundles/"},
		{"empty repo path", "ctxloom+file://///bundles/x"},
		{"empty internal name", "ctxloom+local:"},
		{"userinfo", "ctxloom+git://alice@github.com/acme/repo//bundles/x"},
		{"query string", "ctxloom+git://github.com/acme/repo//bundles/x?ref=main"},
		{"unknown item kind", "ctxloom+local:tooling#widgets/x"},
		{"selector without a name", "ctxloom+local:tooling#fragments"},
		{"truncated percent escape", "ctxloom+local:too%"},
		{"invalid percent escape", "ctxloom+local:too%zz"},
		{"path on an internal class", "ctxloom+builtin://host/x"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseBundleRef(tt.in)
			require.Error(t, err, "accepted %q", tt.in)
			assert.ErrorIs(t, err, ErrRefSyntax, "unexpected error for %q: %v", tt.in, err)
		})
	}
}
