package refuri

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsRefControlRune_ExhaustiveC0AndDEL is a direct, white-box test of the
// predicate itself — the refuri mirror of
// remote.TestNormalizeRef_ExhaustiveC0AndDEL. Every one of the 33 code
// points named by the comment (C0 0x00-0x1F, plus DEL 0x7F) must match, and
// nothing else in the 0x00-0xFF byte range may. This is the exact parity claim
// the audit exists to pin: the two predicates share one formula
// (r < 0x20 || r == 0x7f) in two packages (refuri and remote), and a divergence between them would
// mean the two ref grammars disagree about what a "safe" reference can carry.
func TestIsRefControlRune_ExhaustiveC0AndDEL(t *testing.T) {
	for r := 0; r < 0x100; r++ {
		r := rune(r)
		want := r < 0x20 || r == 0x7f
		assert.Equal(t, want, isRefControlRune(r), "isRefControlRune(%#U) mismatch", r)
	}
}

// TestHasScheme_KnowsEveryClass holds the cheap scheme recogniser and the real
// parser to the same set of classes.
//
// The direction of the failure is what makes this worth pinning: HasScheme is
// what a guard consults to decide whether a string is a REFERENCE at all, so a
// class it does not know is not "unparsed", it is read as a BARE NAME — and a
// bare name is first-party by construction and takes the local exemption. A
// missing class therefore fails OPEN. Driving the table off Classes() rather
// than a literal list is what makes a class added to the grammar without a
// recogniser arm fail here instead of at a trust gate.
func TestHasScheme_KnowsEveryClass(t *testing.T) {
	// The five names are spelled out rather than read back from Classes(),
	// because a table driven ENTIRELY off the thing under test cannot notice a
	// class going missing from it — the row disappears with the class and the
	// assertion stays green. This literal is what makes the loop below a real
	// exhaustiveness claim.
	assert.ElementsMatch(t,
		[]SourceClass{ClassGit, ClassFile, ClassLocal, ClassCompanion},
		Classes(),
		"every source class must be enumerated by Classes()")

	for _, class := range Classes() {
		raw := SchemePrefix + string(class) + ":name"
		switch class {
		case ClassGit:
			raw = SchemePrefix + string(class) + "://host/repo//bundles/name"
		case ClassFile:
			raw = SchemePrefix + string(class) + ":///repo//bundles/name"
		}
		assert.True(t, HasScheme(raw), "HasScheme(%q) must recognize class %q", raw, class)

		// Whatever HasScheme claims to recognize, Parse must actually accept:
		// a scheme the recogniser knows and the parser refuses would be
		// reported as a malformed reference forever.
		p, err := Parse(raw)
		require.NoError(t, err, "Parse(%q)", raw)
		assert.Equal(t, class, p.Class)
	}
}

// TestHasScheme_RejectsNonMembers pins the other side: a bare name, a
// same-shaped but unknown class, and the pre-canonical spellings must NOT be
// read as canonical references.
func TestHasScheme_RejectsNonMembers(t *testing.T) {
	for _, raw := range []string{
		"",
		"dev",
		"lang/go",
		"ctxloom:local@bundles/dev",
		"ctxloom:companion@ltk",
		"https://github.com/acme/repo@bundles/dev",
		"git@github.com:acme/repo@bundles/dev",
		"file:///srv/repo@bundles/dev",
	} {
		assert.False(t, HasScheme(raw), "HasScheme(%q) must be false", raw)
	}
}

// TestParse_RoundTripsEveryClass pins that Render is Parse's inverse for every
// class, version and selector combination — the property every identity
// comparison downstream rests on.
func TestParse_RoundTripsEveryClass(t *testing.T) {
	for _, raw := range []string{
		"ctxloom+git://github.com/acme/repo//bundles/lang/go",
		"ctxloom+git://github.com/acme/repo//bundles/tooling@v1.2.3",
		"ctxloom+git://github.com/acme/repo//bundles/tooling#fragments/x",
		"ctxloom+file:///srv/repo//bundles/tooling",
		"ctxloom+local:my-tools#profiles/dev",
		"ctxloom+companion:ltk@v2",
	} {
		p, err := Parse(raw)
		require.NoError(t, err, "Parse(%q)", raw)
		assert.Equal(t, raw, p.Render(true), "Render must invert Parse")

		again, err := Parse(p.Render(true))
		require.NoError(t, err)
		assert.Equal(t, p, again, "parse ∘ render ∘ parse must be stable")
	}
}

// TestHasScheme_AdmitsUnknownClassesSoParseCanRefuseThem pins the width of the
// recogniser against the narrowness of the parser. A reference naming a class
// this build does not implement must reach Parse and be REFUSED there; read as
// a bare name instead, it would take the first-party local exemption — a
// newer grammar's reference silently granted more trust than an older one's.
func TestHasScheme_AdmitsUnknownClassesSoParseCanRefuseThem(t *testing.T) {
	for _, raw := range []string{
		"ctxloom+registry:dev",
		"ctxloom+localish:dev",
		"ctxloom+local",
	} {
		assert.True(t, HasScheme(raw), "%q claims the family and must not be read as a bare name", raw)
		_, err := Parse(raw)
		assert.ErrorIs(t, err, ErrSyntax, "%q names no known class and must be refused", raw)
	}
}

// TestBuilders_RenderCanonicalStrings pins what each class-builder produces, as
// literals. These are the strings a trust grant is keyed on once a layer above
// adds an item selector, so they are asserted byte-exact rather than
// round-tripped.
func TestBuilders_RenderCanonicalStrings(t *testing.T) {
	cases := []struct {
		name string
		mint func() (Parts, error)
		want string
	}{
		{"git", func() (Parts, error) { return Git("github.com", "/acme/repo", "tooling") },
			"ctxloom+git://github.com/acme/repo//bundles/tooling"},
		{"file", func() (Parts, error) { return File("/srv/repo", "lang/go") },
			"ctxloom+file:///srv/repo//bundles/lang/go"},
		{"local", func() (Parts, error) { return Local("my-tools") }, "ctxloom+local:my-tools"},
		{"companion", func() (Parts, error) { return Companion("ltk") }, "ctxloom+companion:ltk"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := tc.mint()
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if got := p.Render(true); got != tc.want {
				t.Errorf("%s renders %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestMint_RefusesWhatParseRefuses is the round-trip discipline: a reference
// built in Go must clear exactly the gate a reference typed as text clears, or
// a builder becomes a way around the grammar.
func TestMint_RefusesWhatParseRefuses(t *testing.T) {
	if _, err := Mint(Parts{Class: ClassLocal}); err == nil {
		t.Error("an empty bundle name must be refused")
	}
	if _, err := Mint(Parts{Class: "registry", Bundle: "x"}); err == nil {
		t.Error("a class outside the vocabulary must be refused, not rendered")
	}
	if _, err := Mint(Parts{Class: ClassGit, RepoPath: "/acme/repo", Bundle: "x"}); err == nil {
		t.Error("ctxloom+git without a host must be refused")
	}
	// A name carrying the version delimiter survives as data rather than
	// silently splitting into name + version.
	p, err := Mint(Parts{Class: ClassLocal, Bundle: "na@me"})
	if err != nil {
		t.Fatalf("a literal @ in a bundle name must round-trip: %v", err)
	}
	if p.Bundle != "na@me" || p.Version != "" {
		t.Errorf("got bundle %q version %q, want bundle \"na@me\" and no version", p.Bundle, p.Version)
	}
}

// constraintVersions is one expression per construct Masterminds semver's
// NewConstraint accepts (the grammar remote.resolveSemver resolves against),
// plus ctxloom's own "kind:" selector prefixes (remote.parseSelector). Every
// one of them carries at least one character net/url percent-encodes in a
// path, or a delimiter the grammar must not confuse with structure.
var constraintVersions = []string{
	"1.2.3", "=1.2.3", "!=1.2.3", ">1.2", "<2", ">=1.2", "=>1.2", "<=2", "=<2",
	"~1.2", "~>1.2", "^1.2", "1.x", "1.X", "1.*", "*",
	">=1.2, <2", ">=1.2 <2", "^1 || ^2", "1.2 - 1.4", "1.2.3-rc.1+build.5",
	"version:^1.2", "tag:v1.0", "branch:feature/x", "sha:deadbeef",
}

// TestParse_VersionIsDecodedAndRoundTrips pins the version segment to ONE
// value however it is spelled: the typed form ("^1.2") and the escaped form
// ("%5E1.2") must parse to the same Version, the decoded expression, and
// rendering it must parse back to equal Parts. A version read from the escaped
// path and never decoded is a different string from the one the user typed,
// and the resolver hands it to the constraint parser verbatim.
func TestParse_VersionIsDecodedAndRoundTrips(t *testing.T) {
	bases := []string{
		"ctxloom+git://github.com/o/r//bundles/x",
		"ctxloom+file:///srv/has space/repo//bundles/x",
		"ctxloom+local:x",
	}
	for _, base := range bases {
		for _, ver := range constraintVersions {
			typed := base + "@" + ver
			p, err := Parse(typed)
			require.NoError(t, err, typed)
			assert.Equal(t, ver, p.Version, "typed %q", typed)

			escaped := base + "@" + escapePath(ver)
			pe, err := Parse(escaped)
			require.NoError(t, err, escaped)
			assert.Equal(t, p, pe, "escaped spelling %q must parse like %q", escaped, typed)

			back, err := Parse(p.Render(true))
			require.NoError(t, err, p.Render(true))
			assert.Equal(t, p, back, "Parse(Render(%q))", typed)
		}

		// An "@" inside a version can only arrive escaped; decoded, it must be
		// re-escaped on render or the next parse splits at the wrong "@".
		p, err := Parse(base + "@branch:a%40b")
		require.NoError(t, err)
		assert.Equal(t, "branch:a@b", p.Version)
		back, err := Parse(p.Render(true))
		require.NoError(t, err)
		assert.Equal(t, p, back, "Parse(Render) with an @ in the version")
	}
}

// A version is not a path: dot segments inside it must never reach the
// bundle half. "x@a/../b" is bundle x at branch "a/../b" (a name git itself
// refuses), never bundle "b" — resolving the ".." across the "@" would let a
// version string choose which bundle the reference names.
func TestParse_DotSegmentsInAVersionNeverReachTheBundle(t *testing.T) {
	for _, ver := range []string{"a/../b", "a/./b", "../b", "a/.."} {
		raw := "ctxloom+git://github.com/o/r//bundles/x@" + ver
		p, err := Parse(raw)
		require.NoError(t, err, raw)
		assert.Equal(t, "x", p.Bundle, "the version %q moved the bundle name", ver)
		assert.Equal(t, ver, p.Version, raw)

		back, err := Parse(p.Render(true))
		require.NoError(t, err, p.Render(true))
		assert.Equal(t, p, back, "Parse(Render(%q))", raw)
	}
}
