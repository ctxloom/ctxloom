package signing

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin the exact byte-for-byte shape of the fragment preimage
// (signature-envelope.spec.md §3.3.3):
//
//	"ctxloom-fragment/1\n"
//	"premise-len: " <decimal byte length of premise> "\n"
//	"content-len: " <decimal byte length of content> "\n"
//	"\n"
//	<premise> "\n"
//	<content>
//
// Like the countersign framing, this is a public contract: a third party must
// be able to reproduce these bytes from the spec text alone.

func TestFragmentPreimage_ExactBytes(t *testing.T) {
	got := FragmentPreimage("when removing a worktree", []byte("never force-remove"))

	want := "ctxloom-fragment/1\n" +
		"premise-len: 24\n" +
		"content-len: 18\n" +
		"\n" +
		"when removing a worktree\n" +
		"never force-remove"

	assert.Equal(t, want, string(got))
}

func TestFragmentPreimage_ContractOpensThePreimage(t *testing.T) {
	got := FragmentPreimage("", nil)
	require.True(t, strings.HasPrefix(string(got), FragmentPreimageContract+"\n"),
		"the contract version must be the FIRST line: position is part of the contract")
}

// An unpremised fragment — the whole corpus, before premises existed — is
// framed on the same contract as a premised one, with a zero-length premise.
// There is no "bare bytes" arm for the common case: a second shape would be a
// second definition of the bytes of a fragment.
func TestFragmentPreimage_EmptyPremiseIsStillFramed(t *testing.T) {
	got := FragmentPreimage("", []byte("body"))

	want := "ctxloom-fragment/1\n" +
		"premise-len: 0\n" +
		"content-len: 4\n" +
		"\n" +
		"\n" +
		"body"

	assert.Equal(t, want, string(got))
}

// The framing is injective: two distinct (premise, content) pairs never frame
// to the same bytes, even when their concatenations are identical or the
// premise carries text that looks like the preamble. Lengths are declared up
// front, so nothing inside either field can shift the boundary.
func TestFragmentPreimage_IsInjectiveOverTheSplit(t *testing.T) {
	cases := [][2]struct {
		premise string
		content string
	}{
		{{"ab", "c"}, {"a", "bc"}},
		{{"a\n", "b"}, {"a", "\nb"}},
		{{"", "x\ny"}, {"x", "y"}},
		{{"p\ncontent-len: 1\n\nb", ""}, {"p", "b"}},
	}
	for _, c := range cases {
		left := FragmentPreimage(c[0].premise, []byte(c[0].content))
		right := FragmentPreimage(c[1].premise, []byte(c[1].content))
		assert.NotEqual(t, string(left), string(right),
			"(%q,%q) and (%q,%q) framed identically", c[0].premise, c[0].content, c[1].premise, c[1].content)
	}
}

// Lengths are BYTE lengths, not rune counts: a multi-byte premise declares
// the number of bytes a verifier must consume.
func TestFragmentPreimage_LensAreByteLengths(t *testing.T) {
	got := string(FragmentPreimage("é", []byte("ü")))
	assert.Contains(t, got, "premise-len: 2\n")
	assert.Contains(t, got, "content-len: 2\n")
}
