package coord

import (
	"errors"
	"fmt"
	"strings"
)

// The mailbox `kind` vocabulary. It is CLOSED and split in two: kinds a SENDER
// may set on agent_send, and kinds only the coordinator itself constructs.
//
// The split is a security boundary, not a naming convention. A reserved kind is
// one a receiving model is expected to act on BECAUSE the coordinator authored
// it — approval_request is relayed to a human as a trust decision — so a sender
// able to set it phishes that decision. The kind also renders into the
// provenance header of a delivered turn (frameCoordinatorDelivery), which is
// why nothing outside this set may reach the frame.
const (
	// KindMessage is the plain sender-to-sender message kind.
	KindMessage = "message"
	// KindResult carries a sender's own findings/verdict, and is also the
	// automatic turn-boundary bridge's kind (children.go's bridgeTurnResult).
	KindResult = "result"
	// KindError reports a failure — a sender's own, or a coordinator-synthesized
	// launch/resume failure.
	KindError = "error"
	// KindQuestion asks the recipient something and expects an answer back.
	KindQuestion = "question"

	// KindApprovalRequest is COORDINATOR-RESERVED: the escalation ladder's
	// relayed ApprovalRequest projection (approval.go), which the recipient
	// answers as a trust decision on a child's behalf.
	KindApprovalRequest = "approval_request"
)

// senderMailKinds is the vocabulary agent_send documents and accepts.
var senderMailKinds = []string{KindMessage, KindResult, KindError, KindQuestion}

// reservedMailKinds are constructed by the coordinator only. Every one of them
// asks the recipient to trust its provenance, so accepting one from a sender
// would let the sender borrow the coordinator's authority.
var reservedMailKinds = []string{KindApprovalRequest, KindUserInjected, KindExited}

// ErrSenderMailKind rejects a sender-supplied mail kind outside the
// sender-allowed vocabulary. Typed so the plane-2 ingress answers
// INVALID_ARGUMENT (statusFromErr) rather than an opaque internal error.
var ErrSenderMailKind = errors.New("agent_send: unusable message kind")

// SenderMailKind validates one sender-supplied mail kind. An absent kind stays
// legal (it is optional, and an unkinded message claims no authority); anything
// present must be a name from the sender-allowed vocabulary, which the refusal
// enumerates so the sender can correct itself without guessing.
//
// This is the string-level form of the closed vocabulary; the typed enum whose
// decode performs the same rejection replaces it, at which point this function
// has no callers left.
func SenderMailKind(kind string) error {
	if kind == "" {
		return nil
	}
	for _, ok := range senderMailKinds {
		if kind == ok {
			return nil
		}
	}
	vocab := strings.Join(senderMailKinds, " | ")
	for _, reserved := range reservedMailKinds {
		if kind == reserved {
			return fmt.Errorf("%w: %q is reserved for the coordinator and cannot be set by a sender "+
				"(a sender-set %q would let a message borrow the coordinator's authority); use one of: %s, or omit kind",
				ErrSenderMailKind, kind, kind, vocab)
		}
	}
	return fmt.Errorf("%w: %q is not a message kind; use one of: %s, or omit kind",
		ErrSenderMailKind, kind, vocab)
}

// MailKinds returns every kind a mailbox message can carry, in a stable
// order: the UNKINDED value first, then the sender-allowed vocabulary, then
// the coordinator-reserved one.
//
// It exists so a table in another file can be checked for exhaustiveness
// against the authority instead of against a second hand-kept list — the same
// reason spool.Dirs() exists. A kind added to senderMailKinds or
// reservedMailKinds and to nothing else must make the mapping test RED, not
// quietly travel unmapped.
//
// The empty string is a member, not an omission: an agent_send with no kind is
// legal (SenderMailKind accepts it) and control.go's Inject queues one on
// every human injection, so "" is the single most common kind on the wire.
func MailKinds() []string {
	out := make([]string, 0, 1+len(senderMailKinds)+len(reservedMailKinds))
	out = append(out, KindUnset)
	out = append(out, senderMailKinds...)
	return append(out, reservedMailKinds...)
}

// KindUnset is the mailbox kind of a message whose sender set none.
const KindUnset = ""

// SpoolKindUnkinded is the FRONTMATTER spelling of KindUnset.
//
// The spool cannot spell it as the empty string: frontmatter kind is the
// routing discriminator, spool.Writer refuses a message without one, and
// spool.Parse refuses a file without one. It is equally not spelled "message":
// the two are observably different today — knownMailKind("") is false, so an
// unkinded delivery renders NO provenance header on the turn it becomes, while
// a "message" one does — and collapsing them here would make the tee's files
// disagree with the mailbox about what the recipient actually saw.
const SpoolKindUnkinded = "unkinded"

// mailKindToSpool maps the closed mailbox vocabulary onto the frontmatter
// kind vocabulary. Every named kind maps to ITSELF: the mailbox's names are
// already the vocabulary the design's frontmatter examples use, and renaming
// them in transit would buy nothing and cost every grep.
//
// A map rather than a switch for the same reason spoolDirToWire is one: the
// inverse is DERIVED from it below instead of hand-written, so the two
// directions cannot disagree.
var mailKindToSpool = map[string]string{
	KindUnset:           SpoolKindUnkinded,
	KindMessage:         KindMessage,
	KindResult:          KindResult,
	KindError:           KindError,
	KindQuestion:        KindQuestion,
	KindApprovalRequest: KindApprovalRequest,
	KindUserInjected:    KindUserInjected,
	KindExited:          KindExited,
}

// spoolKindToMail inverts mailKindToSpool, built once at init. A collision is
// a build-time panic rather than a runtime condition: two mailbox kinds
// sharing one frontmatter spelling would make the reverse mapping pick a
// winner, and the first symptom in production would be a message arriving
// under a kind its sender never set.
var spoolKindToMail = func() map[string]string {
	inv := make(map[string]string, len(mailKindToSpool))
	for mail, spoolKind := range mailKindToSpool {
		if prev, dup := inv[spoolKind]; dup {
			panic(fmt.Sprintf("coord: mail kind table maps %q and %q onto the same frontmatter kind %q", prev, mail, spoolKind))
		}
		inv[spoolKind] = mail
	}
	return inv
}()

// SpoolKindForMail projects a mailbox kind onto its frontmatter kind. A kind
// outside the closed vocabulary is an ERROR, never a pass-through: writing an
// unmapped kind verbatim would let a table gap reach disk as a message no
// reader routes, which is precisely the silent-drop the mapping exists to
// prevent.
func SpoolKindForMail(kind string) (string, error) {
	k, ok := mailKindToSpool[kind]
	if !ok {
		return "", fmt.Errorf("coord: mail kind %q has no frontmatter representation (the vocabulary is %s)", kind, strings.Join(MailKinds(), " | "))
	}
	return k, nil
}

// MailKindForSpool resolves a frontmatter kind back to its mailbox kind — the
// inverse SpoolKindForMail's round trip is asserted against, and what a reader
// of a teed file uses to recover what the mailbox held.
func MailKindForSpool(kind string) (string, error) {
	k, ok := spoolKindToMail[kind]
	if !ok {
		return "", fmt.Errorf("coord: frontmatter kind %q is not a mailbox kind this build knows", kind)
	}
	return k, nil
}

// knownMailKind reports whether kind is a name from the closed vocabulary —
// sender-allowed or coordinator-reserved. It gates what may render into a
// delivered turn's provenance header: a value from a closed set is unforgeable
// as header text, an arbitrary string is not.
func knownMailKind(kind string) bool {
	for _, ok := range senderMailKinds {
		if kind == ok {
			return true
		}
	}
	for _, ok := range reservedMailKinds {
		if kind == ok {
			return true
		}
	}
	return false
}
