// Package signing implements the ctxloom signature envelope: an sshsig
// sign/verify wrapper (sign.go) and the payload framing that defines exactly
// which bytes get signed (payload.go). See
// docs/signature-envelope.spec.md for the normative design; this file exists
// to make deviating from it require touching a comment that says so.
//
// There are two payload shapes, because there are two things being asserted
// about two different byte sequences:
//
//  1. A PUBLISHER signature covers the raw bundle FILE bytes, unframed,
//     verbatim, verified before any YAML parse. There is no framing function
//     for this shape — Sign/Verify are simply called with the file bytes
//     directly. See spec §3.1.
//
//  2. A COUNTERSIGNATURE (a human approval or rejection) covers the exposed
//     ITEM bytes — the same bytes ComputeContentHash/EffectiveContentHash in
//     package bundles already hash — wrapped in a small fixed-shape ASCII
//     header that binds {contract, assertion, ref, form, len}. See
//     spec §3.2, and CountersignPayload below.
package signing

import (
	"bytes"
	"fmt"
	"strconv"
)

// CountersignContract is the fixed contract-version string that opens every
// countersign payload. Bumping it invalidates every existing approval and
// rejection — a deliberate, announced act (spec §12), never a silent drift.
//
// /2 replaced the separate `kind:` and `form:` header lines with the single
// composite AttestationForm: the role an approval attests is now carried by the
// form value itself. Every /1 record therefore stops verifying and its item
// returns to pending. Those records are still READ — the display index makes a
// superseded approval surface as an UPDATE ("a human approved something here
// once, and it no longer covers these bytes") rather than as a first-time item
// (see countersign.Store.LatestApprove) — but they are never re-keyed onto the
// new vocabulary: a stale record is reported, never honoured.
const CountersignContract = "ctxloom-countersign/2"

// ExecPreimageContract is the contract-version string carried as the FIRST
// field of the canonical JSON preimage of an EXEC item — an MCP server or a
// hook (bundles.BundleMCP/BundleHook.ContentPayload, which are the single
// preimage builders for those kinds).
//
// It exists because the exec preimage is the one place the spec's "we never
// canonicalize" rule cannot be honored (spec §3.3.2): an MCP server has no raw
// bytes, only structured fields, so its preimage IS a canonicalization — an
// existing, already-shipped one. The hazard that creates is specific and
// stated: adding a single field to BundleMCP changes the preimage, which
// silently invalidates every approval of every MCP server in every user's
// store, sending them all back to pending with no version signal and no
// announcement. That is fail-closed and therefore safe, but it is a nasty
// surprise.
//
// Carrying the version INSIDE the signed bytes is what converts that surprise
// into an announcement: any change to the exec field set REQUIRES bumping this
// string, which makes the mass re-review a deliberate act (spec §3.3.2, §12).
// Third parties depend on this string; it is a public contract, not an
// implementation detail.
//
// Note the version is not defensive against an attacker — a forged preimage
// gains nothing by naming a version. It is defensive against US: it makes an
// accidental, unannounced field addition impossible to ship quietly.
const ExecPreimageContract = "ctxloom-exec/1"

// Assertion is what a countersignature claims about the bytes it covers.
type Assertion string

const (
	// AssertionApprove: "I reviewed these exact bytes and allow them to
	// reach my agent."
	AssertionApprove Assertion = "approve"
	// AssertionReject: "I refuse these exact bytes / this ref, permanently."
	AssertionReject Assertion = "reject"
)

// Form is the LAYOUT form: which materialization of an item's content a byte
// sequence is, in the terms the FILE LAYOUT uses. Mirrors bundles.ContentForm's
// string values (FormRaw = "raw", FormDistilled = "distilled") plus FormNone,
// the absent form (a ref-reject binds no content at all).
//
// It is deliberately NOT what a countersignature binds. A layout form names a
// materialization and nothing else — the same "raw" describes a fragment's
// authored text and an MCP server's canonical JSON — so it cannot discriminate
// ROLE, and a preimage keyed on it would let byte-identical items of different
// kinds share one approval. AttestationForm carries the role; the two axes are
// separate types precisely so neither can stand in for the other. The layout
// axis is the one that reaches filenames (the suffix is ".distilled.md", which
// could never carry "fragment/distilled").
//
// Items with exactly one materialization (mcp, hook, skill) carry the BASE
// layout form, FormRaw: their single component has an unsuffixed filename.
type Form string

const (
	FormRaw       Form = "raw"
	FormDistilled Form = "distilled"
	FormNone      Form = ""
)

// AttestationForm is the ATTESTATION form: the closed composite vocabulary a
// countersignature binds, naming both the item's ROLE and (for the two
// distillable kinds) which materialization was reviewed.
//
// It is composite because an approval attests bytes IN A ROLE, and role is not
// recoverable from the bytes. Fragment and command payloads are bare content
// bytes with no tag or length prefix, and exec/skill payloads are deterministic
// JSON with every field always emitted — so a bundle can ship a FRAGMENT whose
// body is literally an MCP server's preimage JSON alongside the matching MCP
// server, by byte EQUALITY rather than by any collision search. With role
// folded into the signed form value, the reviewer's approval of that fragment
// keys as "fragment/raw" and can never satisfy the MCP server's "exec/mcp" gate
// — which matters because the dangerous rendering (an executable shown as "what
// it runs") is exactly the step skipped once an item is already approved.
//
// It is a CLOSED enum, not a free string, for two reasons. What VERIFIES must
// not depend on which plugins are loaded: an open vocabulary would make the
// preimage a function of the running binary's registry. And a closed set of
// typed constants is unforgeable and attribute-eligible by construction rather
// than by remembering to escape.
//
// Routing and rendering never read this value — they come from the surface-type
// registry — so a new registry name with no attestation form is INERT: it can
// be neither approved nor exposed through the gate, and extension therefore
// adds no security surface.
type AttestationForm string

const (
	AttestFragmentRaw       AttestationForm = "fragment/raw"
	AttestFragmentDistilled AttestationForm = "fragment/distilled"
	AttestCommandRaw        AttestationForm = "command/raw"
	AttestCommandDistilled  AttestationForm = "command/distilled"
	AttestExecMCP           AttestationForm = "exec/mcp"
	AttestExecHook          AttestationForm = "exec/hook"
	AttestSkill             AttestationForm = "skill"
	// AttestNone is the absent attestation form: a ref-reject binds no
	// content, so it binds no role either.
	AttestNone AttestationForm = ""
)

// AttestationForms returns every content-bearing attestation form, in a fixed
// order. AttestNone is excluded: it names the absence of a bound payload, not a
// role anything can be approved in.
//
// This is the enumeration the exhaustiveness gate walks — a new value that is
// added to the vocabulary but reachable from no (kind, layout) pair, or a pair
// that maps to no value, fails that test rather than surfacing as a silent
// pending item at runtime.
func AttestationForms() []AttestationForm {
	return []AttestationForm{
		AttestFragmentRaw, AttestFragmentDistilled,
		AttestCommandRaw, AttestCommandDistilled,
		AttestExecMCP, AttestExecHook, AttestSkill,
	}
}

// Valid reports whether f is a member of the closed vocabulary. The switch is
// exhaustive over the constants above, which is what makes "closed enum" a
// property of the code rather than a claim in a comment: a value that reaches
// the framing without appearing here is refused (CountersignHeader.Validate).
func (f AttestationForm) Valid() bool {
	switch f {
	case AttestFragmentRaw, AttestFragmentDistilled,
		AttestCommandRaw, AttestCommandDistilled,
		AttestExecMCP, AttestExecHook, AttestSkill, AttestNone:
		return true
	default:
		return false
	}
}

// CountersignHeader is the closed field set bound to a countersignature
// (approve/reject) payload. Every field is drawn from a closed vocabulary
// except Ref, which is a ctxloom item-ref string; that plus the `len` field
// making the payload boundary unambiguous is what keeps this a fixed framing
// rather than a canonicalization (spec §3.2).
//
// There is no Kind field. The role an approval attests lives in Form, whose
// composite vocabulary carries it — one field, one closed vocabulary, and no
// way to write a header whose kind and form disagree.
type CountersignHeader struct {
	Assertion Assertion
	Ref       string          // canonical item ref, or "" for a content-reject (§5.3)
	Form      AttestationForm // AttestNone for a ref-reject, whose payload is also empty
}

// Validate refuses a header no honest caller can produce: an assertion or a
// form outside its closed vocabulary. It is the enforcement point that makes
// the vocabularies closed in practice — the store calls it before writing (fail
// loud: a record signed under an unknown form could never be looked up again)
// and before looking up candidates (fail closed: an unrecognized form finds
// nothing rather than being framed and matched on its bytes alone).
func (h CountersignHeader) Validate() error {
	switch h.Assertion {
	case AssertionApprove, AssertionReject:
	default:
		return fmt.Errorf("countersign header: assertion %q is not %q or %q", h.Assertion, AssertionApprove, AssertionReject)
	}
	if !h.Form.Valid() {
		return fmt.Errorf("countersign header: attestation form %q is not in the closed vocabulary %v", h.Form, AttestationForms())
	}
	if h.Form == AttestNone && h.Ref == "" {
		return fmt.Errorf("countersign header: a payload-free record must bind a ref (a header binding neither a ref nor a form asserts nothing)")
	}
	return nil
}

// CountersignPayload builds the exact byte sequence a countersignature signs:
//
//	"ctxloom-countersign/2\n"
//	"assertion: " <approve|reject> "\n"
//	"ref: " <canonical item ref, or empty> "\n"
//	"form: " <an AttestationForm value, or empty> "\n"
//	"len: " <decimal length of payloadBytes> "\n"
//	"\n"
//	<payloadBytes>
//
// This is NOT a canonicalization. It is a fixed, length-prefixed,
// LF-delimited ASCII preamble with a closed field set, emitted and parsed by
// exactly this function on both signer and verifier, containing no
// user-controlled structure beyond Ref (drawn from an existing ref grammar)
// — and `len` makes the payload boundary unambiguous regardless of what
// payloadBytes itself contains (see
// TestCountersignPayload_HeaderIsNotAffectedByPayloadContent).
//
// Most callers should prefer ApproveCountersignPayload,
// ContentRejectCountersignPayload, or RefRejectCountersignPayload, which
// enforce the approve/reject asymmetry (spec §5.2/§5.3) at the API level.
// This function is exported for the (verification) case of re-deriving a
// payload from an already-known header, and for tests.
func CountersignPayload(h CountersignHeader, payloadBytes []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString(CountersignContract)
	buf.WriteByte('\n')
	buf.WriteString("assertion: ")
	buf.WriteString(string(h.Assertion))
	buf.WriteByte('\n')
	buf.WriteString("ref: ")
	buf.WriteString(h.Ref)
	buf.WriteByte('\n')
	buf.WriteString("form: ")
	buf.WriteString(string(h.Form))
	buf.WriteByte('\n')
	buf.WriteString("len: ")
	buf.WriteString(strconv.Itoa(len(payloadBytes)))
	buf.WriteByte('\n')
	buf.WriteByte('\n')
	buf.Write(payloadBytes)
	return buf.Bytes()
}

// ApproveCountersignPayload builds the ref-scoped approve payload (spec
// §5.2): the ref is bound deliberately, so an approval is of *this item at
// this ref in this form*. Moving an item to a new ref re-gates it to
// pending; approving a fragment's raw form does not approve its distilled
// form.
func ApproveCountersignPayload(ref string, form AttestationForm, payloadBytes []byte) []byte {
	return CountersignPayload(CountersignHeader{
		Assertion: AssertionApprove,
		Ref:       ref,
		Form:      form,
	}, payloadBytes)
}

// ContentRejectCountersignPayload builds the ref-omitted reject payload
// (spec §5.3, "content-reject"): the ref is deliberately absent from both
// this function's signature and the payload it produces, so a rejection of
// these bytes verifies wherever they appear — a renamed, moved, or
// republished-under-another-key identical copy is still rejected. There is
// no ref parameter to pass by mistake here; that is implementer trap #1
// (spec §14.1). Emit one of these per form the item currently has (raw and
// distilled), mirroring today's two-hash SetBlacklist denylist.
func ContentRejectCountersignPayload(form AttestationForm, payloadBytes []byte) []byte {
	return CountersignPayload(CountersignHeader{
		Assertion: AssertionReject,
		Ref:       "",
		Form:      form,
	}, payloadBytes)
}

// RefRejectCountersignPayload builds the sticky ref-level block (spec §5.3,
// "ref-reject"): form is always AttestNone and the payload is always empty —
// this blocks the ref regardless of what its content becomes. There are no
// form/payload parameters here to pass by mistake, for the same reason
// ContentRejectCountersignPayload has no ref parameter.
//
// A ref-reject needs no role either: the ref it blocks already embeds the kind
// directory, so blocking "…#mcp/postgres" cannot spill onto a fragment.
func RefRejectCountersignPayload(ref string) []byte {
	return CountersignPayload(CountersignHeader{
		Assertion: AssertionReject,
		Ref:       ref,
		Form:      AttestNone,
	}, nil)
}
