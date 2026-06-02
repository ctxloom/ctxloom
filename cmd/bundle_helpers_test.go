// Tests for the pure helpers in cmd/bundle.go. The distill-related
// helpers (cleanDistilledOutput, isStructuredContent, buildSiblingContext)
// are decision-dense and produce user-/LLM-visible output, so they're
// the highest-value coverage target in this file — the surrounding
// cobra RunE bodies do IO and network and aren't worth extracting
// without a bigger refactor.
package cmd

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/compression"
)

// =============================================================================
// cleanDistilledOutput
// =============================================================================

func TestCleanDistilledOutput_NoPreambleIsUnchanged(t *testing.T) {
	in := "func Foo() {}\n"
	got := cleanDistilledOutput(in)
	assert.Equal(t, "func Foo() {}", got, "trailing whitespace trimmed; body unchanged")
}

func TestCleanDistilledOutput_StripsConversationalPrefixLine(t *testing.T) {
	// Configs often prepend a chatty opener; we strip the first line when
	// it begins with a known conversational pattern.
	cases := []struct {
		name string
		in   string
	}{
		{"here_is", "Here is the compressed version:\nactual content"},
		{"below_is", "Below is the compressed version:\nactual content"},
		{"ive_compressed", "I've compressed the content:\nactual content"},
		{"the_following", "The following is compressed:\nactual content"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cleanDistilledOutput(tc.in)
			assert.Equal(t, "actual content", got)
		})
	}
}

func TestCleanDistilledOutput_StripsSeparatorAfterPreamble(t *testing.T) {
	// When a preamble is followed by a markdown horizontal rule, both
	// the preamble line and the rule are removed.
	in := "Here is the compressed version:\n---\nfunc Foo() {}"
	got := cleanDistilledOutput(in)
	assert.Equal(t, "func Foo() {}", got)
}

func TestCleanDistilledOutput_DoesNotStripSeparatorWithoutPreamble(t *testing.T) {
	// A separator on its own (no preamble line) is left alone — it
	// might be part of the legitimate content.
	in := "func Foo() {}\n---\nfunc Bar() {}"
	got := cleanDistilledOutput(in)
	assert.Equal(t, in, got, "separator without preamble must be preserved")
}

func TestCleanDistilledOutput_StripsCodeFences(t *testing.T) {
	in := "```go\nfunc Foo() {}\n```"
	got := cleanDistilledOutput(in)
	assert.Equal(t, "func Foo() {}", got)
}

func TestCleanDistilledOutput_StripsCodeFenceWithoutLang(t *testing.T) {
	in := "```\nfunc Foo() {}\n```"
	got := cleanDistilledOutput(in)
	assert.Equal(t, "func Foo() {}", got)
}

func TestCleanDistilledOutput_PreambleAndCodeFenceCombined(t *testing.T) {
	in := "Here's the compressed version:\n```go\nfunc Foo() {}\n```"
	got := cleanDistilledOutput(in)
	assert.Equal(t, "func Foo() {}", got)
}

func TestCleanDistilledOutput_TrailingFenceMustBeAlone(t *testing.T) {
	// The closing fence is only stripped when it's the last token (whitespace
	// after is fine). A fence in the middle should not be paired up.
	in := "```\nfunc Foo() {}\n``` // sneaky"
	got := cleanDistilledOutput(in)
	assert.Contains(t, got, "func Foo() {}")
	assert.Contains(t, got, "// sneaky", "trailing non-whitespace after fence prevents pairing")
}

// =============================================================================
// isStructuredContent
// =============================================================================

func TestIsStructuredContent(t *testing.T) {
	structured := []compression.ContentType{
		compression.ContentTypeGo,
		compression.ContentTypePython,
		compression.ContentTypeJavaScript,
		compression.ContentTypeTypeScript,
		compression.ContentTypeRust,
		compression.ContentTypeJava,
		compression.ContentTypeJSON,
	}
	for _, ct := range structured {
		assert.True(t, isStructuredContent(ct), "%s should be structured", ct)
	}

	unstructured := []compression.ContentType{
		compression.ContentTypeYAML,
		compression.ContentTypeMarkdown,
		compression.ContentTypeText,
		compression.ContentTypeUnknown,
	}
	for _, ct := range unstructured {
		assert.False(t, isStructuredContent(ct), "%s should not be structured", ct)
	}
}

// =============================================================================
// buildSiblingContext
// =============================================================================

func TestBuildSiblingContext_HeaderShowsBundleMeta(t *testing.T) {
	b := &bundles.Bundle{
		Description: "Go coding standards",
		Version:     "1.2.3",
		Tags:        []string{"go", "best-practices"},
	}
	got := buildSiblingContext(b, "fragments/anything")

	assert.Contains(t, got, "Bundle: Go coding standards (v1.2.3)")
	assert.Contains(t, got, "Tags: go, best-practices")
}

func TestBuildSiblingContext_OmitsVersionWhenEmpty(t *testing.T) {
	b := &bundles.Bundle{Description: "Bundle X"}
	got := buildSiblingContext(b, "fragments/anything")
	assert.Contains(t, got, "Bundle: Bundle X\n")
	assert.NotContains(t, got, "(v")
}

func TestBuildSiblingContext_OmitsTagsWhenEmpty(t *testing.T) {
	b := &bundles.Bundle{Description: "X"}
	got := buildSiblingContext(b, "fragments/anything")
	assert.NotContains(t, got, "Tags:")
}

func TestBuildSiblingContext_ListsSiblingFragmentsExcludingTarget(t *testing.T) {
	b := &bundles.Bundle{
		Description: "X",
		Fragments: map[string]bundles.BundleFragment{
			"alpha": {Content: "Alpha first line\nsecond line"},
			"beta":  {Content: "Beta first line"},
			"self":  {Content: "Should be excluded"},
		},
	}
	got := buildSiblingContext(b, "fragments/self")

	assert.Contains(t, got, "Sibling fragments:")
	assert.Contains(t, got, "- alpha: Alpha first line")
	assert.Contains(t, got, "- beta: Beta first line")
	assert.NotContains(t, got, "Should be excluded", "the target fragment must be skipped")
	assert.NotContains(t, got, "- self:", "the target fragment must not be listed")
}

func TestBuildSiblingContext_TruncatesLongFirstLine(t *testing.T) {
	long := strings.Repeat("x", 100)
	b := &bundles.Bundle{
		Description: "X",
		Fragments: map[string]bundles.BundleFragment{
			"verbose": {Content: long},
			"target":  {Content: "ignored"},
		},
	}
	got := buildSiblingContext(b, "fragments/target")

	// 60-char cap: 57 chars + "..." = 60 total
	assert.Contains(t, got, "- verbose: "+strings.Repeat("x", 57)+"...")
}

func TestBuildSiblingContext_PromptsPreferDescriptionOverFirstLine(t *testing.T) {
	b := &bundles.Bundle{
		Description: "X",
		Prompts: map[string]bundles.BundlePrompt{
			"with-desc": {
				Description: "Short curated description",
				Content:     "Verbose body text the user shouldn't see in the sibling list.",
			},
			"no-desc": {Content: "Falls back to first line"},
			"target":  {Content: "ignored"},
		},
	}
	got := buildSiblingContext(b, "prompts/target")

	assert.Contains(t, got, "- with-desc: Short curated description")
	assert.NotContains(t, got, "Verbose body text", "description must win over content first-line")
	assert.Contains(t, got, "- no-desc: Falls back to first line")
}

func TestBuildSiblingContext_SkipsSiblingSectionsWhenAlone(t *testing.T) {
	// A bundle with exactly one fragment and the target IS that fragment
	// shouldn't render an empty "Sibling fragments:" header. Same for prompts.
	b := &bundles.Bundle{
		Description: "X",
		Fragments: map[string]bundles.BundleFragment{
			"only-one": {Content: "alone"},
		},
	}
	got := buildSiblingContext(b, "fragments/only-one")
	assert.NotContains(t, got, "Sibling fragments:", "lone fragment with itself excluded yields no list")
}
