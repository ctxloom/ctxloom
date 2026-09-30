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
	// SuggestsSetMode is a mode change the engine suggests alongside the
	// allow (e.g. accept edits for the rest of the session on an edit).
	SuggestsSetMode Declared[PermissionMode]
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
