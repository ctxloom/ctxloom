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
//     ITEM bytes — whatever that kind's ContentPayload builder in package
//     bundles produces, which EffectiveContentHash hashes — wrapped in a small
//     fixed-shape ASCII header that binds {contract, assertion, ref, form,
//     len}. See spec §3.2, and CountersignPayload below.
//
// None of those item payloads is bare bytes: every kind's preimage opens with
// its own contract version — the exec and skill canonicalizations
// (ExecPreimageContract, SkillPreimageContract; built in package bundles) and
// the fragment and command framings (FragmentPreimage, CommandPreimage below).
// Each carries the version INSIDE the signed bytes for the reason stated on
// ExecPreimageContract, and the contracts are pairwise distinct and
// prefix-free so no two kinds can ever produce identical preimage bytes.
package signing

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
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

// FragmentPreimageContract is the contract-version string that opens every
// fragment preimage (FragmentPreimage below). It exists for the same reason
// ExecPreimageContract does: a fragment's countersigned bytes are no longer the
// bare authored text but a framing over EVERYTHING the agent is shown of the
// fragment — its selection premise and its body — and any change to that
// presented field set must bump this string so the mass re-review it causes is
// announced rather than discovered.
//
// The premise is inside the signed bytes because it decides whether the body is
// ever seen. Signing the body while leaving the premise unsigned defends against
// injection and not against SUPPRESSION: rewrite a guardrail's premise to a
// condition that never holds, leave its body byte-identical, and every existing
// approval keeps verifying while the guardrail silently never enters context.
// The invariant is that the signed preimage is exactly what is presented, in
// both directions — presented-but-unsigned is that attack, and
// signed-but-never-presented is a spurious invalidation that trains reviewers
// to re-approve reflexively.
const FragmentPreimageContract = "ctxloom-fragment/1"

// FragmentPreimage builds the exact byte sequence a fragment countersignature
// is taken over — the item payload that CountersignPayload then frames:
//
//	"ctxloom-fragment/1\n"
//	"premise-len: " <decimal byte length of premise> "\n"
//	"content-len: " <decimal byte length of content> "\n"
//	"\n"
//	<premise> "\n"
//	<content>
//
// This is a FRAMING, not a canonicalization, in exactly the sense
// CountersignPayload is: a fixed LF-delimited ASCII preamble with a closed field
// set and declared lengths, so no byte inside either field can move the
// boundary between them and two distinct (premise, content) pairs never frame
// to the same bytes (TestFragmentPreimage_IsInjectiveOverTheSplit). The
// single LF between premise and content carries no information — the lengths
// already fix the split — and is there so a reviewer reading the preimage as
// text sees the premise on its own line.
//
// Every fragment is framed this way, premised or not: an empty premise is
// declared with length 0 rather than falling back to bare bytes, because a
// second shape for the common case would be a second definition of "the bytes
// of this fragment", and two definitions is the bug. It takes the two fields
// rather than a fragment because this package depends on nothing else in the
// tree; bundles.FragmentSurface is the model that supplies them and the only
// production caller.
func FragmentPreimage(premise string, content []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString(FragmentPreimageContract)
	buf.WriteByte('\n')
	buf.WriteString("premise-len: ")
	buf.WriteString(strconv.Itoa(len(premise)))
	buf.WriteByte('\n')
	buf.WriteString("content-len: ")
	buf.WriteString(strconv.Itoa(len(content)))
	buf.WriteByte('\n')
	buf.WriteByte('\n')
	buf.WriteString(premise)
	buf.WriteByte('\n')
	buf.Write(content)
	return buf.Bytes()
}

// CommandPreimageContract is the contract-version string that opens every
// command preimage (CommandPreimage below). A command's countersigned bytes
// are a framing over EVERYTHING the agent is shown of the command: its
// description, its per-engine export config — the slash command's help text,
// argument hint, tool grant, model and enablement, which the engine writes
// into the command file's frontmatter — and its body. Signing the body alone
// left the help text rewritable under a verifying approval, and left the tool
// grant, which is a CAPABILITY, reaching the agent unsigned. Any change to the
// presented field set must bump this string so the mass re-review it causes is
// announced rather than discovered.
const CommandPreimageContract = "ctxloom-command/1"

// CommandPreimage builds the exact byte sequence a command countersignature is
// taken over — the item payload that CountersignPayload then frames:
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
// It is a FRAMING in exactly FragmentPreimage's sense: a fixed LF-delimited
// ASCII preamble with a closed field set and declared lengths, so no byte
// inside any field can move a boundary and two distinct triples never frame to
// the same bytes (TestCommandPreimage_IsInjectiveOverTheSplit). The description
// and the content are carried verbatim. The exports field is the one
// structured part of the surface — per-engine settings with a list-valued
// tool grant, and no raw bytes of their own — so it arrives here already
// canonicalized by its builder (bundles.CommandSurface.ExportsPayload), under
// the same rule that lets the exec preimage be a canonicalization at all.
//
// Every command is framed this way, described or not: empty fields are
// declared with length 0 rather than falling back to bare bytes, because a
// second shape for the common case would be a second definition of "the bytes
// of this command". This package depends on nothing else in the tree, so it
// takes the three fields rather than a command; bundles.CommandSurface is the
// model that supplies them and the only production caller.
func CommandPreimage(description string, exports, content []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString(CommandPreimageContract)
	buf.WriteByte('\n')
	buf.WriteString("description-len: ")
	buf.WriteString(strconv.Itoa(len(description)))
	buf.WriteByte('\n')
	buf.WriteString("exports-len: ")
	buf.WriteString(strconv.Itoa(len(exports)))
	buf.WriteByte('\n')
	buf.WriteString("content-len: ")
	buf.WriteString(strconv.Itoa(len(content)))
	buf.WriteByte('\n')
	buf.WriteByte('\n')
	buf.WriteString(description)
	buf.WriteByte('\n')
	buf.Write(exports)
	buf.WriteByte('\n')
	buf.Write(content)
	return buf.Bytes()
}

// SkillPreimageContract is the contract-version string carried as the FIRST
// field of the canonical JSON preimage of a skill package
// (bundles.BundleSkill.ContentPayload, the single preimage builder for that
// kind). A skill has no raw bytes — it is a file tree named by a manifest,
// plus per-engine export settings — so its preimage is a canonicalization for
// the same reason the exec preimage is, and it is versioned for the same
// reason: any change to the field set must bump this string. It is a
// separate contract from ExecPreimageContract because it names a different
// shape; a skill borrowing the exec string would let a change to one field
// set go unannounced under the other's version.
const SkillPreimageContract = "ctxloom-skill/1"

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
// It is composite because an approval attests bytes IN A ROLE, and role must be
// in the record, not inferred from the bytes. Every kind's preimage now opens
// with its own contract string, so a text item can no longer be byte-identical
// to an executable's preimage by construction; the role in the form is the
// second, independent defence — the store keys on it, and a reviewer's
// approval of a command keys as "command/raw" and can never satisfy an MCP
// server's "exec/mcp" gate whatever the bytes. That matters because the
// dangerous rendering (an executable shown as "what it runs") is exactly the
// step skipped once an item is already approved.
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
	if i := strings.IndexFunc(h.Ref, isControl); i != -1 {
		return fmt.Errorf("countersign header: ref %q contains a control character at byte %d — "+
			"a ctxloom ref carries none, and one here forges the rest of the frame", h.Ref, i)
	}
	return nil
}

// isControl reports whether r is a C0 control character or DEL — the class
// CountersignPayload's framing cannot survive inside Ref.
//
// This is what makes the framing's "no user-controlled structure beyond Ref"
// claim TRUE rather than assumed. The preamble is LF-delimited and `ref:` is
// emitted BEFORE `form:` and `len:`, so an LF inside Ref closes the ref line
// early and the remainder of the ref is read back as those later fields: the
// header {approve, "bundle#fragments/a", fragment/raw} over a 15-byte payload
// and the header {approve, "bundle#fragments/a\nform: fragment/raw\nlen: 15\n",
// AttestNone} over an empty payload frame to the SAME bytes. One signature then
// verifies for both tuples, and countersign.indexHash files both at one path.
// `len` cannot disambiguate: it is emitted after `ref` and never parsed back.
//
// Refusing — rather than silently stripping, which is what the INGEST
// normaliser (remote.NormalizeRef) does at the boundary — is deliberate here:
// these bytes are the preimage, and quietly rewriting a preimage changes what a
// signature covers without anyone being told. By the time a ref reaches this
// function it has already had its one chance to be normalised; arriving dirty
// means it bypassed ingest, which is a fault to report, not to repair.
//
// The two layers share no code on purpose. This package deliberately depends on
// nothing else in the tree, and a defence in depth whose second layer imports
// its first is one layer.
func isControl(r rune) bool {
	return r < 0x20 || r == 0x7f
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
// user-controlled structure beyond Ref — and `len` makes the payload boundary
// unambiguous regardless of what payloadBytes itself contains (see
// TestCountersignPayload_HeaderIsNotAffectedByPayloadContent).
//
// Ref is the one field the caller supplies as free text, and the framing is
// injective ONLY because a ref may not carry a control character:
// CountersignHeader.Validate enforces that, and every write and lookup path
// goes through Validate first. Without it a ref containing an LF closes the
// `ref:` line early and forges the `form:` and `len:` lines emitted after it,
// so two distinct tuples frame to identical bytes. Do not reorder the fields to
// put `len:` before `ref:` and call the problem solved either — the invariant
// is that Ref contains no framing characters, and that is where it is kept.
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

// CountersignPreimage frames a header the way the three shape wrappers below
// do, dispatching on the shape the header already names. It is the seam a
// header-carrying caller uses instead of calling CountersignPayload directly:
// countersign.Store threads a CountersignHeader through its write, index-hash
// and candidate-lookup paths, so it cannot pass (ref, form, payload) to a
// wrapper the way an assertion-specific caller would — and calling the raw
// framing function was what left the wrappers with zero production callers,
// their traps unarmed on the only paths that actually sign anything.
//
// The dispatch is a pure re-expression: for every header it routes, the bytes
// are identical to CountersignPayload(h, payloadBytes) — see
// TestCountersignPreimage_MatchesCountersignPayload, which is the whole
// contract. The ref-reject arm is guarded on an empty payload precisely because
// RefRejectCountersignPayload has no payload parameter: routing a
// payload-carrying header there would drop the bytes and silently reframe. Any
// shape the wrappers do not name (a reject that binds both a ref and a form)
// falls through to the raw framing rather than being reshaped to fit.
func CountersignPreimage(h CountersignHeader, payloadBytes []byte) []byte {
	switch {
	case h.Assertion == AssertionApprove:
		return ApproveCountersignPayload(h.Ref, h.Form, payloadBytes)
	case h.Assertion == AssertionReject && h.Ref == "":
		return ContentRejectCountersignPayload(h.Form, payloadBytes)
	case h.Assertion == AssertionReject && h.Form == AttestNone && len(payloadBytes) == 0:
		return RefRejectCountersignPayload(h.Ref)
	default:
		return CountersignPayload(h, payloadBytes)
	}
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
