package refuri

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsRefControlRune_ExhaustiveC0AndDEL is a direct, white-box test of the
// predicate itself — the refuri mirror of
// remote.TestStripRefControlChars_ExhaustiveC0AndDEL. Every one of the 33 code
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
		[]SourceClass{ClassGit, ClassFile, ClassBuiltin, ClassLocal, ClassCompanion},
		Classes(),
		"every source class must be enumerated by Classes()")

	for _, class := range Classes() {
		raw := SchemePrefix + string(class) + ":name"
		if class == ClassGit {
			raw = SchemePrefix + string(class) + "://host/repo//bundles/name"
		} else if class == ClassFile {
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
		"ctxloom+registry:dev",
		"ctxloom:local@bundles/dev",
		"ctxloom:companion@ltk",
		"https://github.com/acme/repo@bundles/dev",
		"git@github.com:acme/repo@bundles/dev",
		"file:///srv/repo@bundles/dev",
		"ctxloom+local",
		"ctxloom+localish:dev",
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
		"ctxloom+builtin:ltk",
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
