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
// third thing: no spelling maps to it, so a decode refuses it (coordgrpc's
// SendRequestFromWire).
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

// LegacyKindName is one enum value's spelling in the mailbox's string
// vocabulary: the enum name with its MESSAGE_KIND_ prefix stripped and
// lowercased. MESSAGE_KIND_APPROVAL_REQUEST -> "approval_request".
//
// The enum is the SINGLE vocabulary; the mailbox, the spool frontmatter and
// the journal spell its members as these strings, and coord (which imports
// this package, not the other way round) builds its sender-allowed and
// reserved lists from LegacySenderKindNames/LegacyReservedKindNames rather
// than keeping literals of its own. So the relationship is mechanical instead
// of remembered: every value's spelling is DERIVED here, and the inverse
// (MessageKindForLegacyName) is derived from the same function, so the two
// directions cannot disagree.
//
// UNSPECIFIED has no spelling: the string vocabulary expresses "unkinded" as
// the EMPTY STRING, which the enum deliberately does not represent (see
// MESSAGE_KIND_MESSAGE's comment). It returns "".
//
// The name comes from the generated MessageKind_name map, not k.String():
// String() reads the file descriptor, which the generated init() builds AFTER
// package-level vars — and legacyNameToKind below is one.
func LegacyKindName(k MessageKind) string {
	name, ok := MessageKind_name[int32(k)]
	if !ok || k == MessageKind_MESSAGE_KIND_UNSPECIFIED {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(name, "MESSAGE_KIND_"))
}

// legacyNameToKind inverts LegacyKindName over every declared value, built
// once. "" maps to UNSPECIFIED by construction (LegacyKindName's own answer for
// it), so the unkinded mailbox Message projects onto the wire's zero value.
var legacyNameToKind = func() map[string]MessageKind {
	inv := make(map[string]MessageKind, len(MessageKind_name))
	for v := range MessageKind_name {
		k := MessageKind(v)
		inv[LegacyKindName(k)] = k
	}
	return inv
}()

// MessageKindForLegacyName resolves a mailbox spelling back onto the enum —
// the projection a mailbox Message makes when it becomes a PeerMessage. It is
// the inverse of LegacyKindName, not a second string→kind conversion: the
// table is derived from that function. A spelling outside it is an ERROR,
// never UNSPECIFIED, so a kind nobody mapped cannot ride the wire as "unset".
func MessageKindForLegacyName(name string) (MessageKind, error) {
	k, ok := legacyNameToKind[name]
	if !ok {
		return MessageKind_MESSAGE_KIND_UNSPECIFIED,
			fmt.Errorf("mail kind %q is not a spelling of any message kind this build knows; the vocabulary is %s", name, allKindNames())
	}
	return k, nil
}

// LegacySenderKindNames is the sender-allowed vocabulary in the mailbox
// spelling, in enum-declaration order — the order every refusal enumerates
// the legal values in, and the source coord's own list is built from.
func LegacySenderKindNames() []string {
	return legacyNamesWhere(func(k MessageKind) bool { return k.IsSenderAllowed() })
}

// LegacyReservedKindNames is the coordinator-reserved vocabulary in the
// mailbox spelling, in enum-declaration order — what coord refuses from a
// sender as reserved, and what only the coordinator constructs.
func LegacyReservedKindNames() []string {
	return legacyNamesWhere(func(k MessageKind) bool { return k.IsCoordinatorReserved() })
}

// legacyNamesWhere lists the spellings of every declared value that satisfies
// keep, in enum-declaration (numeric) order. Declaration order rather than
// alphabetical because the documented vocabulary reads in that order and the
// refusal text quotes it.
func legacyNamesWhere(keep func(MessageKind) bool) []string {
	values := make([]int32, 0, len(MessageKind_name))
	for v := range MessageKind_name {
		if keep(MessageKind(v)) {
			values = append(values, v)
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	names := make([]string, 0, len(values))
	for _, v := range values {
		names = append(names, LegacyKindName(MessageKind(v)))
	}
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
