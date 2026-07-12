package signing

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
)

// These tests pin the exact byte-for-byte shape of the countersign payload
// framing defined in signature-envelope.spec.md §3.2:
//
//	"ctxloom-countersign/1\n"
//	"assertion: " <approve|reject> "\n"
//	"kind: " <fragments|skills|mcp|hooks> "\n"
//	"ref: " <canonical item ref, or empty> "\n"
//	"form: " <raw|distilled|exec, or empty> "\n"
//	"len: " <decimal length of payload_bytes> "\n"
//	"\n"
//	<payload_bytes>
//
// This is a public contract (§12): a third party must be able to reproduce
// these bytes from the spec text alone. Any deviation here is a contract
// break, not a style choice.

func TestCountersignPayload_ExactBytes(t *testing.T) {
	got := CountersignPayload(CountersignHeader{
		Assertion: AssertionApprove,
		Kind:      KindFragments,
		Ref:       "my-tools#fragments/go-testing",
		Form:      FormRaw,
	}, []byte("hello fragment"))

	want := "ctxloom-countersign/1\n" +
		"assertion: approve\n" +
		"kind: fragments\n" +
		"ref: my-tools#fragments/go-testing\n" +
		"form: raw\n" +
		"len: 14\n" +
		"\n" +
		"hello fragment"

	assert.Equal(t, want, string(got))
}

func TestCountersignPayload_EmptyRefAndForm(t *testing.T) {
	got := CountersignPayload(CountersignHeader{
		Assertion: AssertionReject,
		Kind:      KindFragments,
		Ref:       "",
		Form:      FormNone,
	}, nil)

	want := "ctxloom-countersign/1\n" +
		"assertion: reject\n" +
		"kind: fragments\n" +
		"ref: \n" +
		"form: \n" +
		"len: 0\n" +
		"\n"

	assert.Equal(t, want, string(got))
}

func TestCountersignPayload_LenIsPayloadByteLength(t *testing.T) {
	// A multi-byte UTF-8 payload: len must be the BYTE length, not rune count.
	payload := []byte("héllo") // 6 bytes, 5 runes
	got := CountersignPayload(CountersignHeader{
		Assertion: AssertionApprove,
		Kind:      KindSkills,
		Ref:       "r",
		Form:      FormDistilled,
	}, payload)

	assert.Contains(t, string(got), "len: 6\n")
}

func TestCountersignPayload_HeaderIsNotAffectedByPayloadContent(t *testing.T) {
	// A payload that itself contains header-shaped lines must not confuse the
	// framing: len makes the boundary unambiguous regardless of content.
	payload := []byte("ref: not-the-real-ref\nlen: 999\n\nmore data")
	got := CountersignPayload(CountersignHeader{
		Assertion: AssertionApprove,
		Kind:      KindFragments,
		Ref:       "real-ref",
		Form:      FormRaw,
	}, payload)

	want := "ctxloom-countersign/1\n" +
		"assertion: approve\n" +
		"kind: fragments\n" +
		"ref: real-ref\n" +
		"form: raw\n" +
		"len: " + strconv.Itoa(len(payload)) + "\n" +
		"\n" +
		string(payload)

	assert.Equal(t, want, string(got))
}

// =============================================================================
// The asymmetric constructors — implementer trap #1 (spec §14.1): approve and
// reject are deliberately NOT symmetric. Approve binds the ref; content-reject
// omits it. The API shape itself must make the wrong thing impossible to
// write, not just documented against.
// =============================================================================

func TestApproveCountersignPayload_BindsRef(t *testing.T) {
	payloadA := ApproveCountersignPayload(KindFragments, "bundle#fragments/a", FormRaw, []byte("same bytes"))
	payloadB := ApproveCountersignPayload(KindFragments, "bundle#fragments/b", FormRaw, []byte("same bytes"))

	// Identical content bytes at two different refs produce DIFFERENT signed
	// payloads: an approval at ref A must not verify against ref B.
	assert.NotEqual(t, payloadA, payloadB)
	assert.Contains(t, string(payloadA), "ref: bundle#fragments/a\n")
	assert.Contains(t, string(payloadB), "ref: bundle#fragments/b\n")
}

func TestContentRejectCountersignPayload_OmitsRef(t *testing.T) {
	// Same bytes exposed under two different logical refs produce the SAME
	// content-reject payload: a rejection of these bytes follows them
	// wherever they appear (rename-immunity, spec §5.3).
	got := ContentRejectCountersignPayload(KindFragments, FormRaw, []byte("malicious bytes"))

	assert.Contains(t, string(got), "ref: \n")
	assert.NotContains(t, string(got), "bundle#fragments/")
}

func TestRefRejectCountersignPayload_EmptyFormAndPayload(t *testing.T) {
	got := RefRejectCountersignPayload(KindFragments, "bundle#fragments/evil")

	want := "ctxloom-countersign/1\n" +
		"assertion: reject\n" +
		"kind: fragments\n" +
		"ref: bundle#fragments/evil\n" +
		"form: \n" +
		"len: 0\n" +
		"\n"
	assert.Equal(t, want, string(got))
}
