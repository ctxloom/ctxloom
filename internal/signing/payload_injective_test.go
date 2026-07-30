package signing

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The countersign frame must be INJECTIVE: two distinct (assertion, ref, form,
// payload) tuples may never produce the same signed bytes. If they can, one
// signature verifies for both tuples and countersign.indexHash files them at one
// path.
//
// A newline inside Ref is the whole attack: `ref: ` is an LF-terminated line
// emitted BEFORE `form:` and `len:`, so an embedded LF closes it early and the
// rest of the ref is read back as those later fields. `len:` cannot save the
// frame — it is emitted after `ref:` and never parsed back.
//
// The property is stated over the headers Validate ACCEPTS, which is the set
// that can actually be signed or looked up: CountersignPayload is the pure
// framing primitive and validates nothing, while every production write and
// lookup path (countersign.Store.write, .writeUnsigned, .candidates,
// .hasUnsigned) refuses an invalid header first. frameIfValid mirrors that
// order.
func frameIfValid(h CountersignHeader, payload []byte) (string, error) {
	if err := h.Validate(); err != nil {
		return "", err
	}
	return string(CountersignPayload(h, payload)), nil
}

// refWithForgedTail is a ref whose embedded newlines forge the remaining header
// lines of a frame for the ref "bundle#fragments/a" over a 15-byte payload.
const refWithForgedTail = "bundle#fragments/a\nform: fragment/raw\nlen: 15\n"

// honestPayload is the 15-byte payload the forged tail above claims a length
// for. It is itself shaped like the tail of a header so that, once the forged
// ref has consumed the real `form:`/`len:` lines, the two frames line up
// exactly — the collision is by construction, not by a hash search.
var honestPayload = []byte("form: \nlen: 0\n\n")

func TestCountersignPayload_NewlineInRefCannotForgeTheHeader(t *testing.T) {
	honest := CountersignHeader{
		Assertion: AssertionApprove,
		Ref:       "bundle#fragments/a",
		Form:      AttestFragmentRaw,
	}
	forged := CountersignHeader{
		Assertion: AssertionApprove,
		Ref:       refWithForgedTail,
		Form:      AttestNone,
	}

	honestFrame, err := frameIfValid(honest, honestPayload)
	require.NoError(t, err, "the honest header must stay acceptable")

	// A ref carrying a newline is not a ref. Refusing it is defence in depth
	// behind ingest-time stripping (remote.NormalizeRef): the frame is the thing
	// being signed, so no future path that bypasses ingest may reach it.
	forgedFrame, err := frameIfValid(forged, nil)
	require.Error(t, err,
		"a header whose ref carries a newline must be refused: its ref forges the rest of the frame, "+
			"framing to the same bytes as the honest tuple (%q)", honestFrame)
	assert.Empty(t, forgedFrame, "a refused header must not yield framed bytes")
}

// TestCountersignFrame_IsInjectiveOverAcceptedHeaders walks the tuples the
// closed vocabulary admits and asserts no two of them frame alike — including
// the pairs the ref/form/payload axes are specifically meant to keep apart
// (same bytes at the same ref in two forms; a ref-reject vs a content-reject).
func TestCountersignFrame_IsInjectiveOverAcceptedHeaders(t *testing.T) {
	payloads := [][]byte{nil, []byte("x"), honestPayload, []byte("ref: elsewhere\n")}
	refs := []string{"", "bundle#fragments/a", "bundle#fragments/ab", "other#fragments/a", refWithForgedTail}
	assertions := []Assertion{AssertionApprove, AssertionReject}
	forms := append(AttestationForms(), AttestNone)

	seen := map[string]CountersignHeader{}
	for _, a := range assertions {
		for _, ref := range refs {
			for _, form := range forms {
				for _, p := range payloads {
					h := CountersignHeader{Assertion: a, Ref: ref, Form: form}
					frame, err := frameIfValid(h, p)
					if err != nil {
						continue // outside the accepted set; nothing to collide
					}
					prior, dup := seen[frame]
					require.False(t, dup,
						"frame collision: %+v and %+v (payload %q) produce identical signed bytes",
						prior, h, p)
					seen[frame] = h
				}
			}
		}
	}
	require.NotEmpty(t, seen)
}

func TestCountersignHeader_Validate_RejectsControlCharsInRef(t *testing.T) {
	for _, tc := range []struct {
		name string
		ref  string
	}{
		{"lf", "bundle#fragments/a\nform: fragment/raw\n"},
		{"cr", "bundle#fragments/a\rform: fragment/raw\r"},
		{"trailing lf", "bundle#fragments/a\n"},
		{"leading lf", "\nbundle#fragments/a"},
		{"tab", "bundle#fragments/a\tb"},
		{"esc", "bundle#fragments/a\x1b[2K"},
		{"nul", "bundle#fragments/a\x00"},
		{"del", "bundle#fragments/a\x7f"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := CountersignHeader{Assertion: AssertionApprove, Ref: tc.ref, Form: AttestFragmentRaw}
			assert.Error(t, h.Validate())
		})
	}
}

func TestCountersignHeader_Validate_AcceptsOrdinaryRefs(t *testing.T) {
	for _, ref := range []string{
		"my-tools#fragments/go-testing",
		"https://github.com/owner/repo@bundles/core#mcp/postgres",
		"ctxloom:local@bundles/dev#commands/review",
		"ctxloom:local|my-tools#fragments/go-testing",
		"", // a content-reject binds no ref
	} {
		h := CountersignHeader{Assertion: AssertionApprove, Ref: ref, Form: AttestFragmentRaw}
		assert.NoError(t, h.Validate(), "ref %q", ref)
	}
}
