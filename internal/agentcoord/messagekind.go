package agentcoord

// MessageKind ingress policy (plane-2 §4.B).
//
// The enum itself closes the vocabulary; this file draws the line WITHIN it
// between what a sender may name and what only the coordinator may mint. That
// split is policy over one type, not two types: a mailbox message has one kind
// space, and `agent_send` is simply not allowed to name all of it.
//
// Why this is not a call-site check. `structured["kind"]` used to be a free
// string, so every producer and every consumer had to agree on the vocabulary
// by convention — and one of them didn't: a child could send
// kind="approval_request" and the coordinator's delivery framing interpolated
// it verbatim, minting what read to the receiving model as a genuine approval
// prompt. Making it an enum moves rejection to DECODE and rendering to a NAME
// from a closed set. What remains is the reserved/sender-allowed split, and it
// belongs here, next to the enum, so a new value cannot be added without
// landing in one of the two lists below.

import (
	"fmt"
	"sort"
	"strings"
)

// senderAllowedKinds is what `agent_send` accepts from a sender.
//
// Adding a value to MessageKind and NOT listing it here makes it
// coordinator-reserved — the safe default, and the reason this is an allow-list
// rather than a deny-list.
//
// It must agree, MEMBER FOR MEMBER, with the string-level vocabulary
// `coord.SenderMailKind` enforces at the peerSend chokepoint (plane-2 Stage 0's
// standalone form of this check). Two vocabularies that disagree are worse than
// either alone: the typed guard and the string guard would each let through what
// the other refuses, at different ingresses. LegacySenderKindNames below is the
// executable link — a test cross-checks it against that vocabulary, so the pair
// cannot drift silently while both look correct in isolation.
var senderAllowedKinds = map[MessageKind]bool{
	MessageKind_MESSAGE_KIND_MESSAGE:  true,
	MessageKind_MESSAGE_KIND_RESULT:   true,
	MessageKind_MESSAGE_KIND_ERROR:    true,
	MessageKind_MESSAGE_KIND_QUESTION: true,
}

// IsSenderAllowed reports whether a sender may name this kind on `agent_send`.
// MESSAGE_KIND_UNSPECIFIED is never sender-allowed: "unset" is not a kind.
func (k MessageKind) IsSenderAllowed() bool { return senderAllowedKinds[k] }

// IsCoordinatorReserved reports whether only the coordinator may mint this
// kind. UNSPECIFIED is neither reserved nor allowed — it is INVALID, which is a
// third thing (see ValidateMessageKind).
func (k MessageKind) IsCoordinatorReserved() bool {
	return k != MessageKind_MESSAGE_KIND_UNSPECIFIED && !k.IsSenderAllowed() && k.recognised()
}

// recognised reports whether the value is one this build's enum declares.
//
// proto3 enums are OPEN on the wire: an unrecognised number survives decoding
// as itself rather than failing, so "the field decoded" is NOT the same as "the
// value is in the vocabulary". Every ingress check therefore has to ask this
// question explicitly — the whole point being that an unrecognised kind is
// REFUSED, never quietly treated as the zero value and passed to a switch whose
// default branch decides what happens next.
func (k MessageKind) recognised() bool {
	_, ok := MessageKind_name[int32(k)]
	return ok
}

// ValidateMessageKind is the INGRESS GUARD for a kind that arrived from a
// sender (`agent_send`, over either the runner's typed plane-2 frame or the
// stdio tool surface).
//
// It refuses three distinct things, and says which:
//
//   - an unrecognised number — the open-enum case, a sender on a newer or
//     hand-rolled build, or a probe;
//   - MESSAGE_KIND_UNSPECIFIED — a message that never named its kind. Refused
//     rather than defaulted: the zero value must not be a way to arrive
//     unclassified;
//   - a coordinator-reserved value — the forgery case, and the reason any of
//     this exists.
//
// The error names the accepted vocabulary, because a refusal an agent cannot
// act on just becomes a retry loop.
func ValidateMessageKind(k MessageKind) error {
	switch {
	case !k.recognised():
		return fmt.Errorf("agent_send: kind %d is not a message kind this build knows; use one of %s",
			int32(k), SenderAllowedKindNames())
	case k == MessageKind_MESSAGE_KIND_UNSPECIFIED:
		return fmt.Errorf("agent_send: kind is required — name one of %s", SenderAllowedKindNames())
	case k.IsCoordinatorReserved():
		return fmt.Errorf("agent_send: kind %s is the coordinator's own and cannot be sent; use one of %s",
			k, SenderAllowedKindNames())
	}
	return nil
}

// ParseMessageKind resolves an enum NAME to its value, refusing anything
// outside the vocabulary.
//
// It exists for the surfaces that still carry the kind as text — the stdio MCP
// tool's JSON arguments, and CLI/journal round-trips — so that exactly one
// place turns a string into a MessageKind, with exactly one error message. A
// second string→kind conversion somewhere else is how the old free-string
// vocabulary drifted in the first place.
func ParseMessageKind(name string) (MessageKind, error) {
	v, ok := MessageKind_value[name]
	if !ok {
		return MessageKind_MESSAGE_KIND_UNSPECIFIED,
			fmt.Errorf("unknown message kind %q; the vocabulary is %s", name, allKindNames())
	}
	return MessageKind(v), nil
}

// SenderAllowedKindNames lists the sender-allowed value names, sorted, for use
// in error messages.
func SenderAllowedKindNames() string {
	names := make([]string, 0, len(senderAllowedKinds))
	for k := range senderAllowedKinds {
		names = append(names, k.String())
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// LegacyKindName is one enum value's spelling in the RETIRED free-string
// vocabulary: the enum name with its MESSAGE_KIND_ prefix stripped and
// lowercased. MESSAGE_KIND_APPROVAL_REQUEST -> "approval_request".
//
// It exists so the two guards cannot drift by SPELLING. The string-level
// chokepoint guard (`coord.SenderMailKind`) and this typed enum have to agree
// while both exist, and they live in packages that cannot import each other
// (coord imports this one). So the relationship is made mechanical instead of
// remembered: every enum value's legacy spelling is DERIVED, and a test pins the
// derivation against the four names the string vocabulary actually uses.
//
// What this does NOT prove: that the string guard's MEMBERSHIP matches. That
// check belongs at the merge that brings both into one tree, and it has one
// obvious form — repoint coord's `senderMailKinds` at LegacySenderKindNames()
// and delete its literal. Until then, this is the shared definition to converge
// on rather than a second one to maintain.
//
// UNSPECIFIED has no legacy spelling: the free-string vocabulary expressed
// "unkinded" as the EMPTY STRING, which the enum deliberately does not
// represent (see MESSAGE_KIND_MESSAGE's comment). It returns "".
func LegacyKindName(k MessageKind) string {
	if k == MessageKind_MESSAGE_KIND_UNSPECIFIED || !k.recognised() {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(k.String(), "MESSAGE_KIND_"))
}

// LegacySenderKindNames is the sender-allowed vocabulary in the retired
// free-string spelling, sorted — the exact set `agent_send` used to accept as
// text, derived from the enum so there is one source for it.
func LegacySenderKindNames() []string {
	names := make([]string, 0, len(senderAllowedKinds))
	for k := range senderAllowedKinds {
		names = append(names, LegacyKindName(k))
	}
	sort.Strings(names)
	return names
}

// allKindNames lists every declared value name except UNSPECIFIED, sorted.
func allKindNames() string {
	names := make([]string, 0, len(MessageKind_name))
	for v, name := range MessageKind_name {
		if MessageKind(v) == MessageKind_MESSAGE_KIND_UNSPECIFIED {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// ValidateControlInitiatorKind refuses an unset or unrecognised control
// initiator (§4.E).
//
// This value is the PRIVILEGE discriminator a control verb reads to decide
// whether the caller may control a given run, so it fails CLOSED: an
// unrecognised initiator must not inherit the narrower branch's privileges by
// falling through a switch. It is never a request payload field — the
// coordinator derives it from the authenticated caller — so a failure here is a
// coordinator bug, not a hostile sender, and should read like one.
func ValidateControlInitiatorKind(k ControlInitiatorKind) error {
	switch k {
	case ControlInitiatorKind_CONTROL_INITIATOR_KIND_HUMAN,
		ControlInitiatorKind_CONTROL_INITIATOR_KIND_AGENT:
		return nil
	case ControlInitiatorKind_CONTROL_INITIATOR_KIND_UNSPECIFIED:
		return fmt.Errorf("control initiator is unset: it is derived from the authenticated caller " +
			"(CONTROL_INITIATOR_KIND_HUMAN or CONTROL_INITIATOR_KIND_AGENT), never defaulted")
	default:
		return fmt.Errorf("control initiator %d is not one this build knows", int32(k))
	}
}
