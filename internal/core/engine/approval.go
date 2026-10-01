package engine

import "encoding/json"

// AskKind classifies what an engine is asking a human to decide.
type AskKind int

const (
	// AskTool is a tool call the engine's rules left open.
	AskTool AskKind = iota
	// AskQuestion is the model asking the human one or more questions.
	AskQuestion
	// AskPlan is the model presenting a plan and asking to leave plan mode.
	AskPlan
)

// PermissionAsk is one engine request for a human decision, in ctxloom's
// neutral vocabulary: the engine's codec decodes its native payload into this
// shape, and nothing downstream of the codec (the coordinator's queue, a
// presenter) ever sees an engine wire format.
type PermissionAsk struct {
	Kind AskKind
	Tool string
	// Input is the tool call's input as canonical JSON.
	Input json.RawMessage
	// ToolUseID correlates the ask to the model's tool call; set whenever the
	// engine supplies one.
	ToolUseID string
	// Suggestions are engine-native session rules derived from the engine's
	// own suggestions — the choices an allow-for-session picker offers.
	Suggestions []string
	// SuggestsSetMode is a posture change the engine suggests alongside the
	// allow (e.g. accept edits for the rest of the session on an edit), in
	// the engine's own vocabulary.
	SuggestsSetMode Declared[string]
	Plan            *PlanProposal
	Questions       []Question
}

// PlanProposal is the plan an AskPlan presents.
type PlanProposal struct{ Markdown, Path string }

// Question is one question an AskQuestion presents.
type Question struct {
	Header, Text string
	Options      []QuestionOption
	MultiSelect  bool
}

// QuestionOption is one offered answer to a Question.
type QuestionOption struct{ Label, Description string }

// QuestionAnswer is the human's answer to one Question.
type QuestionAnswer struct {
	// Question is the question text verbatim: an engine may key answers by it.
	Question string
	// Labels are the chosen option labels verbatim; more than one only for a
	// MultiSelect question.
	Labels []string
	// Other is free text the human typed instead of, or beside, an option.
	Other string
}

// PermissionAnswer is the decision an ask receives, in the neutral
// vocabulary; the engine's codec encodes it into the native hook answer.
type PermissionAnswer struct {
	Allow bool
	// SessionRules are engine-native rules granted for the rest of the
	// engine's session; an encoder writes them to the session scope ONLY,
	// never to a settings file.
	SessionRules []string
	// SetMode is a posture change carried with an allow: one of the
	// engine's PermissionModel.Transitions from the session's posture.
	SetMode Declared[string]
	// Answers answer an AskQuestion.
	Answers []QuestionAnswer
	// Message is a deny's note, or a rejected plan's feedback.
	Message string
}

// HostCall is one call the engine made to ctxloom's permission host: the
// tool it asks about, the call's id and its input.
type HostCall struct {
	Tool, ToolUseID string
	Input           json.RawMessage
}

// ApprovalCodec is the engine's half of the approval route: neutral in,
// native out. Nothing outside the engine knows the native formats.
type ApprovalCodec interface {
	// DecodeAsk reads a native hook payload for the named hook event into
	// the neutral ask.
	DecodeAsk(event string, payload []byte) (PermissionAsk, error)
	// EncodeAnswer writes the decision for ask as the hook's native stdout.
	// It refuses an answer the engine must never be handed: a mode change
	// other than accept-edits or default.
	EncodeAnswer(event string, ask PermissionAsk, a PermissionAnswer) ([]byte, error)
	// HostCall reads the arguments of a call to the permission host.
	HostCall(args json.RawMessage) (HostCall, error)
	// HostDeny is the permission host's native deny result.
	HostDeny(message string) (string, error)
	// RepoSurfaces are the globs, relative to a repository root, of the
	// executable surfaces the engine loads from a repository.
	RepoSurfaces() []string
	// ValidateRule refuses a rule the engine's rule syntax does not accept.
	ValidateRule(rule string) error
}

// PermissionHostTool is the tool ctxloom's session endpoint serves as the
// engine's permission host: an engine whose approver is the human is pointed
// at it, and it holds each ask open — never deciding it — while the approval
// hook carries the human's decision back to the engine.
const PermissionHostTool = "permission_host"
