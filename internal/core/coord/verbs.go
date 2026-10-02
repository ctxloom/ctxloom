package coord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/launch"
)

// Verbs is the coordination verb set — the ONE place a verb's request is
// validated. Every transport (the RunChannel handlers in adapters/coordgrpc,
// the in-process caller) decodes into these request types and calls the
// verb; a request that is wrong is refused HERE, by Validate, and nowhere
// else, so a caller learns the same refusal whichever way it arrived. The
// wire proto and the MCP tool schema are projections of these request types
// (verbs_schema_test.go holds them to that).
type Verbs interface {
	Spawn(ctx context.Context, caller Identity, req SpawnRequest) (SpawnResult, error)
	Send(ctx context.Context, caller Identity, req SendRequest) (SendResult, error)
	Stop(ctx context.Context, caller Identity, req StopRequest) (StopResult, error)
	StopChildren(ctx context.Context, caller Identity, reason string) ([]StoppedChild, error)
	Roster(caller Identity) []RosterEntry
	Report(ctx context.Context, caller Identity, req ReportRequest) error
	FetchArtifact(ctx context.Context, caller Identity, req FetchRequest) (Artifact, error)
	Control(ctx context.Context, by ControlInitiator, req ControlRequest) (ControlResult, error)
	Host(ctx context.Context, caller Identity, req HostRequest) (HostResult, error)
}

// ErrInvalidRequest is the class every Validate refusal wraps: the request
// is malformed at the verb, before any state is read or written.
var ErrInvalidRequest = errors.New("invalid request")

// SpawnRequest is agent_run: launch Agent as the caller's child with Prompt
// as its first turn. Workspace and DirtyTree are the per-call axis
// overrides; empty defers to the project's configured default.
type SpawnRequest struct {
	Agent     string `json:"agent"`
	Prompt    string `json:"prompt"`
	Workspace string `json:"workspace,omitempty"`
	DirtyTree string `json:"dirty_tree_handler,omitempty"`
}

// Validate is the ONLY validation site for a spawn: the agent name and the
// prompt are required, and both per-call vocabularies are parsed here — a
// typo would otherwise ride inward to the launch goroutine and be reported
// as a child that died, to a caller who could no longer see which argument
// was wrong. dirty_tree_handler's default member auto-commits the user's
// working tree, so a misspelling that fell through to a default would write
// to the repository past both the caller's and the project's explicit
// choice.
func (r SpawnRequest) Validate() error {
	if err := requireNonEmpty("agent_run", "agent", r.Agent); err != nil {
		return err
	}
	if err := requireNonEmpty("agent_run", "prompt", r.Prompt); err != nil {
		return err
	}
	if _, err := launch.ParseWorkspaceAxis(r.Workspace); err != nil {
		return fmt.Errorf("%w: agent_run: workspace: %v", ErrInvalidRequest, err)
	}
	if _, err := launch.ParseDirtyTreeHandler(r.DirtyTree); err != nil {
		return fmt.Errorf("%w: agent_run: dirty_tree_handler: %v", ErrInvalidRequest, err)
	}
	return nil
}

// axes converts the validated overrides to their launch types.
func (r SpawnRequest) axes() (launch.WorkspaceAxis, launch.DirtyTreeHandler) {
	w, _ := launch.ParseWorkspaceAxis(r.Workspace)
	d, _ := launch.ParseDirtyTreeHandler(r.DirtyTree)
	return w, d
}

// SpawnResult names the child — its harp and this incarnation's run id —
// and how it was launched: the engine and profiles it resolved to, the
// runtime axis, whether it queued behind the execution cap, and the
// degraded findings the resolve accepted. Disposition is the prose form.
type SpawnResult struct {
	Harp        string
	RunID       string
	Engine      string
	Profiles    []string
	Runtime     launch.RuntimeAxis
	Queued      bool
	Degraded    []string
	Disposition string
}

// SendRequest is agent_send: a message from the caller to To (a child harp,
// or ParentAddress from a child), of a sender-settable Kind, with an
// optional JSON-object companion and an optional reply correlation.
type SendRequest struct {
	To         string          `json:"to"`
	Kind       string          `json:"kind"`
	Body       string          `json:"body"`
	Structured json.RawMessage `json:"structured,omitempty"`
	InReplyTo  string          `json:"in_reply_to,omitempty"`
}

// Validate is the ONLY validation site for a send: a recipient is required,
// the body must be non-empty (a structured companion alone is payload too),
// and the kind must be one a sender may set — unless the send answers an ask
// (InReplyTo), whose kind the reply's authority supplies. The body's LENGTH is
// not a request error: boundBody bounds it where every route converges.
func (r SendRequest) Validate() error {
	if err := requireNonEmpty("agent_send", "to", r.To); err != nil {
		return err
	}
	if strings.TrimSpace(r.Body) == "" && len(r.Structured) == 0 {
		return fmt.Errorf("%w: agent_send: body is required (a structured companion alone is payload too)", ErrInvalidRequest)
	}
	if r.InReplyTo == "" {
		if err := SenderMailKind(r.Kind); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
		}
	}
	return nil
}

// SendResult is the message id and the delivery disposition the sender is
// told (deliveryDisposition's prose).
type SendResult struct {
	MessageID   string
	Disposition string
}

// StopRequest is agent_stop: one child by Harp, or — with no harp — every
// live child, for which a Reason is required so an accidental omission
// stops nothing. Grace is how long a stopped child's interrupted turn gets to
// end before it is killed — each child's, in the bulk shape; zero means
// DefaultStopGrace.
type StopRequest struct {
	Harp   string        `json:"harp,omitempty"`
	Reason string        `json:"reason,omitempty"`
	Grace  time.Duration `json:"grace,omitempty"`
}

// Validate refuses the bulk shape with no reason, and a negative grace.
func (r StopRequest) Validate() error {
	if r.Harp == "" && strings.TrimSpace(r.Reason) == "" {
		return fmt.Errorf("%w: %w", ErrInvalidRequest, ErrStopReasonRequired)
	}
	if r.Grace < 0 {
		return fmt.Errorf("%w: agent_stop: grace %s is negative", ErrInvalidRequest, r.Grace)
	}
	return nil
}

// StopResult is the disposition for one child, or the per-child outcomes of
// the bulk shape.
type StopResult struct {
	Disposition string
	Children    []StoppedChild
}

// ReportRequest is agent_report: a progress or final summary from a child.
type ReportRequest struct {
	Scope string `json:"scope"`
	Body  string `json:"body"`
}

// Validate requires a scope and a body.
func (r ReportRequest) Validate() error {
	if err := requireNonEmpty("agent_report", "scope", r.Scope); err != nil {
		return err
	}
	return requireNonEmpty("agent_report", "body", r.Body)
}

// FetchRequest is agent_fetch_artifact: an artifact one of the caller's
// children published, by the child's harp and the artifact id.
type FetchRequest struct {
	Harp       string `json:"harp"`
	ArtifactID string `json:"artifact_id"`
}

// Validate requires both halves of the address.
func (r FetchRequest) Validate() error {
	if err := requireNonEmpty("agent_fetch_artifact", "harp", r.Harp); err != nil {
		return err
	}
	return requireNonEmpty("agent_fetch_artifact", "artifact_id", r.ArtifactID)
}

// Artifact is a fetched artifact's manifest and bytes.
type Artifact struct {
	ID     string
	Digest string
	Bytes  []byte
}

// Control verbs.
const (
	ControlVerbSteer     = "steer"
	ControlVerbQuestion  = "question"
	ControlVerbSummarize = "summarize"
	ControlVerbPause     = "pause"
	ControlVerbResume    = "resume"
)

// controlVerbs is the closed vocabulary ControlRequest.Validate admits.
var controlVerbs = []string{ControlVerbSteer, ControlVerbQuestion, ControlVerbSummarize, ControlVerbPause, ControlVerbResume}

// ControlRequest is one control action on a target the initiator owns:
// Verb names the action, Harp the target, Body the instruction, question,
// focus or reason the verb takes (pause and resume take none). Interrupt, on
// a steer only, cuts the target's running turn short first.
type ControlRequest struct {
	Verb      string `json:"verb"`
	Harp      string `json:"harp"`
	Body      string `json:"body,omitempty"`
	Interrupt bool   `json:"interrupt,omitempty"`
}

// Validate requires a known verb, a target, and a body for the verbs that
// carry one.
func (r ControlRequest) Validate() error {
	known := false
	for _, v := range controlVerbs {
		if v == r.Verb {
			known = true
		}
	}
	if !known {
		return fmt.Errorf("%w: control: unknown verb %q (one of: %s)", ErrInvalidRequest, r.Verb, strings.Join(controlVerbs, " | "))
	}
	if err := requireNonEmpty(r.Verb, "harp", r.Harp); err != nil {
		return err
	}
	if r.Interrupt && r.Verb != ControlVerbSteer {
		return fmt.Errorf("%w: %s: interrupt is a steer's alone", ErrInvalidRequest, r.Verb)
	}
	switch r.Verb {
	case ControlVerbSteer, ControlVerbQuestion, ControlVerbSummarize:
		return requireNonEmpty(r.Verb, "body", r.Body)
	}
	return nil
}

// ControlResult is the verb's answer: the delivery mode and withdraw handle
// of a steer, the answer to a question or summarize, or whether a pause or
// resume changed anything.
type ControlResult struct {
	Verb      string
	Delivery  string
	MessageID string
	Answer    *AskAnswer
	Changed   bool
}

// requireNonEmpty is Validate's shared refusal for a required field.
func requireNonEmpty(verb, field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s: %s is required", ErrInvalidRequest, verb, field)
	}
	return nil
}
