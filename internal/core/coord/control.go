package coord

import (
	"context"
	"errors"
	"fmt"
)

// Coordinator→agent CONTROL (steer, question, summarize, pause, resume): the
// guard chain every verb runs, and the steer verb itself. The instruction
// planes live in spoolcontrol.go — a steer, a question or a summarize is a
// durable file in the target's own in/ spool, and pause/resume are
// RunnerChannel requests.

// ControlInitiator names who asked for a control action: the audit identity,
// the privilege discriminator, and the mirror-notice policy key.
//
// Kind is a CLOSED ENUM, not a bool. This value decides whether the caller may
// control a run the coordinator holds, so an unrecognised initiator must fail
// closed rather than fall into whichever branch a bool happened to select.
type ControlInitiator struct {
	Kind ControlInitiatorKind
	// Harp MUST be set iff Kind == AGENT, and MUST be empty otherwise. The
	// human has no harp; an agent that does not name itself cannot be
	// ownership-checked.
	Harp string
}

// Validate enforces the Kind/Harp pairing, so the invalid combinations a bool
// made representable cannot be constructed silently.
func (i ControlInitiator) Validate() error {
	switch i.Kind {
	case InitiatorHuman:
		if i.Harp != "" {
			return fmt.Errorf("control initiator: HUMAN carries no harp, but %q was set", i.Harp)
		}
		return nil
	case InitiatorAgent:
		if i.Harp == "" {
			return errors.New("control initiator: AGENT must name the initiating harp (it is the ownership check's whole input)")
		}
		return nil
	case InitiatorUnspecified:
		return errors.New("control initiator: UNSPECIFIED is not an initiator; a control action must say who asked for it")
	default:
		return fmt.Errorf("control initiator: unrecognised kind %q — refused rather than defaulted, "+
			"because an initiator this build does not know must not inherit another's privileges", string(i.Kind))
	}
}

// auditName is the identity a control action is journaled under.
func (i ControlInitiator) auditName() string {
	if i.Kind == InitiatorAgent {
		return i.Harp
	}
	return UserSender
}

// SteerOutcome reports how a steer landed.
type SteerOutcome struct {
	// Delivery is the Delivery* mode the write observed: whether the target
	// was woken into a new turn, will see it at its next boundary, or will
	// be resumed for it.
	Delivery string
	// MessageID is the WITHDRAW HANDLE: a steer is a durable file, and this
	// is the id WithdrawSteer takes to retract it before the target reads it.
	MessageID string
}

// ErrCapabilityUnavailable marks every refusal whose CAUSE is that the target
// run does not (or no longer does) advertise a capability the request needs.
//
// It is typed because §5.6's fallback selection has to key on it: "this run
// cannot do steer" routes to the mailbox, while "the runner answered an error"
// or "the request timed out" must not. Matching on a gRPC code alone cannot
// draw that line — FAILED_PRECONDITION is answered for several reasons — and
// matching on prose is not a contract.
var ErrCapabilityUnavailable = errors.New("the target run does not advertise a capability this request requires")

// capUnavailable is a capability refusal: errors.Is-able against
// ErrCapabilityUnavailable (so an in-process caller with a fallback routes on
// the cause, not on the code) with the prose naming the gap and the
// advertisement as its whole message (so the wire adapter's status carries it
// verbatim under FAILED_PRECONDITION — StatusFromErr's table).
func capUnavailable(format string, a ...any) error {
	return Refusal(ErrCapabilityUnavailable, format, a...)
}

// ErrControlRefused marks every ownership refusal a control verb makes: the
// initiator is not allowed to control THIS target (a self-target, a run that
// is not the initiator's child, an initiator kind this build does not
// recognise). Typed so a transport can answer PERMISSION_DENIED on the cause
// rather than on the prose, and so a caller can tell "not yours" from "does
// not exist" (ErrNotInjectable) without reading the message.
var ErrControlRefused = errors.New("control: the initiator may not control this target")

// controlTarget runs guards 1–4 shared by every control verb and returns the
// target's run record. The ORDER is load-bearing and the switch is exhaustive
// on purpose — see each guard's comment.
func (c *Coordinator) controlTarget(by ControlInitiator, harp string) (*RunRecord, error) {
	if err := by.Validate(); err != nil {
		return nil, err
	}
	// Guard 1 — target exists.
	var rec *RunRecord
	c.runs.View(func() {
		if r := c.runsF.currentRun(harp); r != nil {
			cp := *r
			rec = &cp
		}
	})
	if rec == nil {
		return nil, fmt.Errorf("control: %q is not a child of this coordinator: %w", harp, ErrNotInjectable)
	}
	// Guard 2 — self-target refused. In the owner-run topology the owned run
	// reuses the coordinating session's own harp as BOTH its own harp and its
	// journaled parent harp (StartOwnedRun, owner_run.go) — a self-loop by
	// construction, independent of depth (the owned run is depth 0, same as
	// the session owner it IS, not a child of it). Without this fence a
	// session would pass its own ownership check via that self-loop and could
	// steer, pause or question ITSELF — a loop with no floor.
	if harp == by.Harp {
		return nil, fmt.Errorf("%w: %q cannot control itself", ErrControlRefused, harp)
	}
	// Guard 3 — ownership. Exhaustive, with no default-allow arm: an initiator
	// this build does not recognise must be REFUSED, not quietly handed the
	// narrower branch's privileges. A future initiator whose policy has not
	// been designed would otherwise inherit child-control by default.
	switch by.Kind {
	case InitiatorHuman:
		// The human may control any run this coordinator holds.
	case InitiatorAgent:
		if rec.ParentHarp != by.Harp {
			return nil, fmt.Errorf("%w: %q is not the parent of %q; a coordinating agent controls only its own children", ErrControlRefused, by.Harp, harp)
		}
	default:
		return nil, fmt.Errorf("%w: initiator kind %q is refused", ErrControlRefused, string(by.Kind))
	}
	return rec, nil
}

// ControlSteer delivers an instruction into a running target: a durable
// `steer` file in the target's own in/ spool (steerViaSpool), which survives
// a relaunch, is visible in in/ while unread, and can be withdrawn
// (WithdrawSteer) until the target takes it.
//
// Sequence, fixed: guards → the write → mirror.
func (c *Coordinator) ControlSteer(ctx context.Context, by ControlInitiator, harp, text string) (SteerOutcome, error) {
	rec, err := c.controlTarget(by, harp)
	if err != nil {
		return SteerOutcome{}, err
	}
	c.audit("agent_steer", by.auditName(), map[string]string{"harp": harp})

	sender := by.auditName()
	outcome, err := c.steerViaSpool(sender, harp, text)
	if err != nil {
		return SteerOutcome{}, err
	}
	// Mirror notice (decision O3): a human injection always tells the
	// target's parent, so a coordinator's picture of its child never diverges
	// without a trace. An AGENT initiator gets no mirror — the parent IS the
	// initiator.
	if by.Kind == InitiatorHuman {
		if _, merr := c.queueMail(harp, rec.ParentHarp, KindUserInjected, injectDigest(text)); merr != nil {
			c.rep.Warnf("steer %s: mirror notice: %v", harp, merr)
		}
	}
	return outcome, nil
}

// steerAsMail is the body of the durable steer: the delivery question ("does
// this target need waking, and did the send complete a waiting receive") is
// ordinary mail's, asked once, and the KIND renders into the delivered
// turn's provenance header so the agent sees an instruction rather than an
// anonymous message.
func (c *Coordinator) steerAsMail(sender, harp, kind, text string) (msgID string, outcome SteerOutcome, err error) {
	msgID = newMessageID()
	observed, err := c.deliverMailID(msgID, sender, harp, kind, text, nil, "")
	if err != nil {
		return "", SteerOutcome{}, err
	}
	mode, _ := deliveryDisposition(observed)
	return msgID, SteerOutcome{Delivery: mode}, nil
}

// controlDisposition words an accepted control request's answer, per verb:
// the text a wire caller reads beside the typed result.
func controlDisposition(req ControlRequest, out ControlResult) string {
	switch req.Verb {
	case ControlVerbSteer:
		return fmt.Sprintf("steered %s (%s)", req.Harp, out.Delivery)
	case ControlVerbQuestion:
		if out.Answer != nil {
			return fmt.Sprintf("%s answered", out.Answer.From)
		}
	case ControlVerbSummarize:
		if out.Answer != nil {
			return fmt.Sprintf("%s summarized", out.Answer.From)
		}
	case ControlVerbPause:
		if out.Changed {
			return fmt.Sprintf("paused %s: its current turn finishes, nothing new is handed to it until agent_resume", req.Harp)
		}
		return fmt.Sprintf("%s was already paused", req.Harp)
	case ControlVerbResume:
		if out.Changed {
			return fmt.Sprintf("resumed %s: turns held at its gate are handed to it in arrival order", req.Harp)
		}
		return fmt.Sprintf("%s was not paused", req.Harp)
	}
	return ""
}
