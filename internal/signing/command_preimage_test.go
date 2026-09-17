package signing

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin the exact byte-for-byte shape of the command preimage
// (signature-envelope.spec.md §3.3.4):
//
//	"ctxloom-command/1\n"
//	"description-len: " <decimal byte length of description> "\n"
//	"exports-len: " <decimal byte length of exports> "\n"
//	"content-len: " <decimal byte length of content> "\n"
//	"\n"
//	<description> "\n"
//	<exports> "\n"
//	<content>
//
// Like the fragment framing, this is a public contract: a third party must be
// able to reproduce these bytes from the spec text alone.

func TestCommandPreimage_ExactBytes(t *testing.T) {
	got := CommandPreimage("review a diff", []byte(`{"claude-code":{}}`), []byte("look at $1"))

	want := "ctxloom-command/1\n" +
		"description-len: 13\n" +
		"exports-len: 18\n" +
		"content-len: 10\n" +
		"\n" +
		"review a diff\n" +
		`{"claude-code":{}}` + "\n" +
		"look at $1"

	assert.Equal(t, want, string(got))
}

func TestCommandPreimage_ContractOpensThePreimage(t *testing.T) {
	got := CommandPreimage("", nil, nil)
	require.True(t, strings.HasPrefix(string(got), CommandPreimageContract+"\n"),
		"the contract version must be the FIRST line: position is part of the contract")
}

// A command with no description and no export config — the whole corpus,
// before either existed — is framed on the same contract with zero-length
// fields. There is no "bare bytes" arm for the common case: a second shape
// would be a second definition of the bytes of a command.
func TestCommandPreimage_EmptyFieldsAreStillFramed(t *testing.T) {
	got := CommandPreimage("", nil, []byte("body"))

	want := "ctxloom-command/1\n" +
		"description-len: 0\n" +
		"exports-len: 0\n" +
		"content-len: 4\n" +
		"\n" +
		"\n" +
		"\n" +
		"body"

	assert.Equal(t, want, string(got))
}

// The framing is injective: two distinct (description, exports, content)
// triples never frame to the same bytes, even when their concatenations are
// identical or a field carries text that looks like the preamble. Lengths are
// declared up front, so nothing inside any field can shift a boundary.
func TestCommandPreimage_IsInjectiveOverTheSplit(t *testing.T) {
	type triple struct{ description, exports, content string }
	cases := [][2]triple{
		{{"ab", "", "c"}, {"a", "", "bc"}},
		{{"a", "b", "c"}, {"a", "", "bc"}},
		{{"a\n", "", "b"}, {"a", "", "\nb"}},
		{{"", "x\ny", ""}, {"", "x", "y"}},
		{{"d\ncontent-len: 1\n\nb", "", ""}, {"d", "", "b"}},
	}
	for _, c := range cases {
		left := CommandPreimage(c[0].description, []byte(c[0].exports), []byte(c[0].content))
		right := CommandPreimage(c[1].description, []byte(c[1].exports), []byte(c[1].content))
		assert.NotEqual(t, string(left), string(right),
			"%+v and %+v framed identically", c[0], c[1])
	}
}

// Lengths are BYTE lengths, not rune counts: a multi-byte description declares
// the number of bytes a verifier must consume.
func TestCommandPreimage_LensAreByteLengths(t *testing.T) {
	got := string(CommandPreimage("é", []byte("ü"), []byte("ß")))
	assert.Contains(t, got, "description-len: 2\n")
	assert.Contains(t, got, "exports-len: 2\n")
	assert.Contains(t, got, "content-len: 2\n")
}

// Every kind's preimage opens with its own contract string, and no two kinds
// share one: this is what makes cross-kind byte equality — a text item whose
// payload IS an executable's preimage — impossible by construction rather
// than merely keyed apart by the attestation form. The contracts are pairwise
// distinct AND prefix-free, because a contract that is a prefix of another
// would let one kind's opening line be read as the other's.
func TestPreimageContracts_AreDistinctAndPrefixFree(t *testing.T) {
	contracts := []string{ExecPreimageContract, FragmentPreimageContract, CommandPreimageContract, SkillPreimageContract}
	for i, a := range contracts {
		for j, b := range contracts {
			if i == j {
				continue
			}
			assert.NotEqual(t, a, b)
			assert.False(t, strings.HasPrefix(b, a), "%q is a prefix of %q", a, b)
		}
	}
}
