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
