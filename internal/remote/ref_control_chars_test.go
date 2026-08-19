package remote

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// A ctxloom reference cannot carry a control character (see isRefControlChar).
// These tests pin the two halves of that rule at the ingest boundary: the
// character is gone from every ref-producing entry point, and a clean ref comes
// back byte for byte.

func TestStripRefControlChars(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		stripped       bool
	}{
		{"clean", "my-tools#fragments/go-testing", "my-tools#fragments/go-testing", false},
		{"empty", "", "", false},
		{"lf", "bundle\nform: fragment/raw\n", "bundleform: fragment/raw", true},
		{"cr", "bundle\rform: fragment/raw", "bundleform: fragment/raw", true},
		{"crlf", "bundle\r\n#fragments/a", "bundle#fragments/a", true},
		{"tab", "bun\tdle", "bundle", true},
		{"nul", "bun\x00dle", "bundle", true},
		{"esc", "bundle\x1b[2Kevil", "bundle[2Kevil", true},
		{"del", "bundle\x7f", "bundle", true},
		{"non-ascii kept", "bündle#fragments/ä", "bündle#fragments/ä", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, stripped := StripRefControlChars(tc.in)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.stripped, stripped)
		})
	}
}

// TestNormalizeRef_WarnsOnlyWhenSomethingWasStripped is the silent-normalisation
// guard: a ref that was quietly rewritten is either a bug upstream or an attack,
// and either way the user is told. The warning is additive — it never changes
// what NormalizeRef returns.
func TestNormalizeRef_WarnsOnlyWhenSomethingWasStripped(t *testing.T) {
	t.Run("clean ref is silent and untouched", func(t *testing.T) {
		var buf bytes.Buffer
		defer clidiag.SetSink(&buf)()
		const clean = "https://github.com/owner/repo@bundles/core#fragments/a"
		assert.Equal(t, clean, NormalizeRef(clean))
		assert.Empty(t, buf.String())
	})

	t.Run("stripped ref warns", func(t *testing.T) {
		var buf bytes.Buffer
		defer clidiag.SetSink(&buf)()
		// A ref unique to this test, so WarnOnce's process-wide dedup cannot
		// swallow the line because an earlier test already emitted it.
		got := NormalizeRef("warn-probe-bundle\nform: fragment/raw\n#fragments/a")
		assert.Equal(t, "warn-probe-bundleform: fragment/raw#fragments/a", got)
		out := buf.String()
		require.NotEmpty(t, out, "a strip must not be silent")
		assert.Contains(t, out, "control characters")
		// The diagnostic quotes both refs with %q, so the characters it is
		// complaining about cannot themselves reach the terminal raw.
		assert.NotContains(t, out, "\nform: fragment/raw\n")
		assert.Contains(t, out, `\nform: fragment/raw\n`)
	})
}

// TestRefIngestPointsStripControlChars covers every ref-producing entry point
// this package exposes. A newline is used because it is the character that
// breaks the countersign frame; the strip itself is class-wide.
func TestRefIngestPointsStripControlChars(t *testing.T) {
	const nl = "\n"

	t.Run("ParseReference", func(t *testing.T) {
		r, err := ParseReference("https://github.com/owner/repo@bundles/core" + nl + "evil")
		require.NoError(t, err)
		assert.Equal(t, "coreevil", r.Path)
		assert.NotContains(t, r.CanonicalString(), nl)
	})

	t.Run("ParseReference local", func(t *testing.T) {
		r, err := ParseReference(LocalSource + "@bundles/dev" + nl)
		require.NoError(t, err)
		assert.Equal(t, "dev", r.Path)
		assert.NotContains(t, r.String(), nl)
	})

	t.Run("ResolveRef", func(t *testing.T) {
		r, err := ResolveRef("demo"+nl, "https://github.com/owner/repo", ItemTypeBundle)
		require.NoError(t, err)
		assert.Equal(t, "demo", r.Path)
	})

	t.Run("ResolveRefString passes the ref through even on failure", func(t *testing.T) {
		// No source to expand against: this is the fault-tolerant path that
		// returns the ref unchanged, which is exactly why it must normalise at
		// entry rather than lean on ParseReference.
		got := ResolveRefString("demo"+nl+"evil", "", "", ItemTypeBundle)
		assert.Equal(t, "demoevil", got)
	})

	t.Run("CanonicalizeShortRef leaves a bare name local but clean", func(t *testing.T) {
		got := CanonicalizeShortRef("bare"+nl+"evil", nil, nil)
		assert.Equal(t, "bareevil", got)
	})

	t.Run("CanonicalizeShortRef expands an alias", func(t *testing.T) {
		got := CanonicalizeShortRef("acme/pack"+nl, func(string) string { return "https://example.com/acme/content" }, nil)
		assert.Equal(t, "https://example.com/acme/content@bundles/pack", got)
	})

	t.Run("CanonicalizeProfileShortRef", func(t *testing.T) {
		got := CanonicalizeProfileShortRef("tools"+nl+"#profiles/dev", nil)
		assert.NotContains(t, got, nl)
	})

	t.Run("NormalizeURL", func(t *testing.T) {
		assert.Equal(t, "https://github.com/owner/repo", NormalizeURL("https://github.com/owner/repo"+nl))
	})

	t.Run("CanonicalBundleRef", func(t *testing.T) {
		got, err := CanonicalBundleRef("dev" + nl)
		require.NoError(t, err)
		assert.Equal(t, LocalBundleRef("dev"), got)
	})

	t.Run("LocalBundleRef", func(t *testing.T) {
		assert.Equal(t, LocalSource+"@bundles/dev", LocalBundleRef("dev"+nl))
	})

	t.Run("SplitItemPath", func(t *testing.T) {
		base, item := SplitItemPath("dev" + nl + "#fragments/a" + nl)
		assert.Equal(t, "dev", base)
		assert.Equal(t, "#fragments/a", item)
	})

	t.Run("FragmentName", func(t *testing.T) {
		name, ok := FragmentName("dev#fragments/a" + nl + "form: fragment/raw")
		require.True(t, ok)
		assert.Equal(t, "aform: fragment/raw", name)
	})

	t.Run("SplitFragmentVersion", func(t *testing.T) {
		canonical, _, err := SplitFragmentVersion("dev" + nl + "#fragments/a")
		require.NoError(t, err)
		assert.Equal(t, LocalBundleRef("dev")+FragmentSelector+"a", canonical)
	})

	t.Run("SplitPromptVersion", func(t *testing.T) {
		canonical, _, err := SplitPromptVersion("dev" + nl + "#commands/review")
		require.NoError(t, err)
		assert.Equal(t, LocalBundleRef("dev")+CommandSelector+"review", canonical)
	})

	t.Run("BundleProfileRef", func(t *testing.T) {
		got, err := BundleProfileRef("dev"+nl, "x"+nl)
		require.NoError(t, err)
		assert.Equal(t, LocalBundleRef("dev")+ProfileSelector+"x", got)
	})

	t.Run("CanonicalProfileKey", func(t *testing.T) {
		got, ok := CanonicalProfileKey("dev" + nl + "#profiles/x")
		require.True(t, ok)
		assert.Equal(t, LocalBundleRef("dev")+ProfileSelector+"x", got)
	})

	t.Run("SplitBundleProfileRef", func(t *testing.T) {
		bundle, name, ok := SplitBundleProfileRef("dev" + nl + "#profiles/x" + nl)
		require.True(t, ok)
		assert.Equal(t, "dev", bundle)
		assert.Equal(t, "x", name)
	})

	t.Run("SplitRetiredProfileRef", func(t *testing.T) {
		url, name, ok := SplitRetiredProfileRef("https://example.com/repo@profiles/x" + nl)
		require.True(t, ok)
		assert.Equal(t, "https://example.com/repo", url)
		assert.Equal(t, "x", name)
	})
}

// TestRefIngest_CleanRefsAreUntouched pins the other half: normalisation is a
// no-op on every legal ref shape, byte for byte.
func TestRefIngest_CleanRefsAreUntouched(t *testing.T) {
	for _, ref := range []string{
		"https://github.com/owner/repo@bundles/core",
		"https://github.com/owner/repo@bundles/core@v1.2.3",
		"https://github.com/owner/repo@bundles/core#fragments/a",
		"git@github.com:owner/repo@bundles/core",
		"file:///path/to/repo@bundles/core",
		LocalSource + "@bundles/dev",
		CompanionSource + "@ltk",
		"bare-name",
		"lang/go",
		"tools#profiles/dev",
	} {
		assert.Equal(t, ref, NormalizeRef(ref), "clean ref must survive verbatim")
		assert.Equal(t, ref, CanonicalizeShortRef(ref, nil, nil))
		if strings.Contains(ref, "@bundles/") || strings.HasPrefix(ref, CompanionSource) {
			parsed, err := ParseReference(ref)
			require.NoError(t, err, ref)
			assert.NotEmpty(t, parsed.String())
		}
	}
}

// TestStripRefControlChars_ExhaustiveC0AndDEL is the audit this file's other
// tests sample: every one of the 33 code points isRefControlChar names (the
// full C0 range 0x00-0x1F, plus DEL 0x7F) must be stripped, and every OTHER
// byte in 0x00-0xFF — printable ASCII, the C1 range 0x80-0x9F, and the rest of
// the Latin-1 byte range — must survive untouched. This is the parity claim
// the audit exists to pin: trust.isRefControlRune uses the identical formula
// (r < 0x20 || r == 0x7f), so a test that nails this range down for
// StripRefControlChars nails it down for both.
//
// A single stray byte is embedded mid-string rather than standalone, so the
// assertion also catches a mapper that only special-cases whole-string or
// leading/trailing occurrences.
func TestStripRefControlChars_ExhaustiveC0AndDEL(t *testing.T) {
	for r := 0; r < 0x100; r++ {
		r := rune(r)
		in := "bundle" + string(r) + "name"
		got, stripped := StripRefControlChars(in)
		wantStripped := r < 0x20 || r == 0x7f
		if wantStripped {
			assert.True(t, stripped, "rune %#U (%d) should have been stripped", r, r)
			assert.Equal(t, "bundlename", got, "rune %#U (%d) left residue after stripping", r, r)
		} else {
			assert.False(t, stripped, "rune %#U (%d) should NOT have been stripped", r, r)
			assert.Equal(t, in, got, "rune %#U (%d) was altered though it is not a control character", r, r)
		}
	}
}
