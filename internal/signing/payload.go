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
//     header that binds {contract, assertion, kind, ref, form, len}. See
//     spec §3.2, and CountersignPayload below.
package signing

import (
	"bytes"
	"strconv"
)

// CountersignContract is the fixed contract-version string that opens every
// countersign payload. Bumping it invalidates every existing approval and
// rejection — a deliberate, announced act (spec §12), never a silent drift.
const CountersignContract = "ctxloom-countersign/1"

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

// ItemKind identifies which bundle content collection an item was drawn
// from. Matches the trust.ItemKind vocabulary; kept independent here so this
// package does not need to import the trust/operations packages.
type ItemKind string

const (
	KindFragments ItemKind = "fragments"
	// KindSkills is a LEGACY stored value: it is the historical item-kind for
	// what is now called a "command" (the skill->command rename, Part A of
	// the skill/command split). It is baked into persisted countersignature
	// payloads (see signingKindOf in internal/operations/countersign_records.go)
	// and MUST NOT be renamed or reused for anything else — doing so would
	// change the preimage bytes of every existing command approval/rejection
	// and silently invalidate them. The TRUE Agent Skill kind is
	// KindAgentSkills, below; it is a deliberately different string so the two
	// can never collide.
	KindSkills ItemKind = "skills"
	KindMCP    ItemKind = "mcp"
	KindHooks  ItemKind = "hooks"
	// KindAgentSkills is the Agent Skills item-kind (Part B2 of the
	// skill/command split): a true SKILL.md package (directory tree), never
	// the legacy KindSkills value above. Fresh stored value, no prior
	// signatures to protect.
	KindAgentSkills ItemKind = "agentskills"
)

// Form identifies which materialization of an item's content the payload
// bytes are. Mirrors bundles.ContentForm's string values (FormRaw =
// "raw", FormDistilled = "distilled") plus two values bundles.ContentForm
// has no use for: FormExec (mcp/hooks — items with exactly one form, whose
// preimage is a canonical JSON encoding, not raw/distilled text) and
// FormNone (the ref-reject payload, which binds no content form at all).
type Form string

const (
	FormRaw       Form = "raw"
	FormDistilled Form = "distilled"
	FormExec      Form = "exec"
	FormNone      Form = ""
)

// CountersignHeader is the closed field set bound to a countersignature
// (approve/reject) payload. Every field is drawn from a closed vocabulary
// except Ref, which is a ctxloom item-ref string; that plus the `len` field
// making the payload boundary unambiguous is what keeps this a fixed framing
// rather than a canonicalization (spec §3.2).
type CountersignHeader struct {
	Assertion Assertion
	Kind      ItemKind
	Ref       string // canonical item ref, or "" for a content-reject (§5.3)
	Form      Form   // "" for a ref-reject, whose payload is also empty
}

// CountersignPayload builds the exact byte sequence a countersignature signs:
//
//	"ctxloom-countersign/1\n"
//	"assertion: " <approve|reject> "\n"
//	"kind: " <fragments|skills|mcp|hooks|agentskills> "\n"
//	"ref: " <canonical item ref, or empty> "\n"
//	"form: " <raw|distilled|exec, or empty> "\n"
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
	buf.WriteString("kind: ")
	buf.WriteString(string(h.Kind))
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
func ApproveCountersignPayload(kind ItemKind, ref string, form Form, payloadBytes []byte) []byte {
	return CountersignPayload(CountersignHeader{
		Assertion: AssertionApprove,
		Kind:      kind,
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
func ContentRejectCountersignPayload(kind ItemKind, form Form, payloadBytes []byte) []byte {
	return CountersignPayload(CountersignHeader{
		Assertion: AssertionReject,
		Kind:      kind,
		Ref:       "",
		Form:      form,
	}, payloadBytes)
}

// RefRejectCountersignPayload builds the sticky ref-level block (spec §5.3,
// "ref-reject"): form is always FormNone and the payload is always empty —
// this blocks the ref regardless of what its content becomes. There are no
// form/payload parameters here to pass by mistake, for the same reason
// ContentRejectCountersignPayload has no ref parameter.
func RefRejectCountersignPayload(kind ItemKind, ref string) []byte {
	return CountersignPayload(CountersignHeader{
		Assertion: AssertionReject,
		Kind:      kind,
		Ref:       ref,
		Form:      FormNone,
	}, nil)
}
