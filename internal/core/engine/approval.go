package engine

import (
	"encoding/json"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// PermissionAsk is one engine request for a human decision about a tool call
// the engine's rules left open, in ctxloom's neutral vocabulary: the engine's
// codec decodes its native payload into this shape, and nothing downstream
// of the codec (the coordinator's queue, a presenter) ever sees an engine
// wire format. A model's questions and plans are not asks: the child ends
// its turn holding them, and its parent answers between turns.
type PermissionAsk struct {
	Tool string
	// Input is the tool call's input as canonical JSON.
	Input json.RawMessage
	// ToolUseID is the model's tool call the ask is about. No codec reads
	// it: the runner's approval route stamps it from the call the ask
	// matched in the turn's ledger.
	ToolUseID string
	// Suggestions are engine-native session rules derived from the engine's
	// own suggestions — the choices an allow-for-session picker offers.
	Suggestions []string
	// SuggestsSetMode is a posture change the engine suggests alongside the
	// allow (e.g. accept edits for the rest of the session on an edit), in
	// the engine's own vocabulary.
	SuggestsSetMode Declared[string]
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
	// Message is a deny's note.
	Message string
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
	// RepoSurfaces are the globs, relative to a repository root, of the
	// executable surfaces the engine loads from a repository.
	RepoSurfaces() []string
	// ValidateRule refuses a rule the engine's rule syntax does not accept.
	ValidateRule(rule string) error
	// Covers reports whether granting rule allows ask's call. It is never
	// wider than the engine's own matching: a rule whose reach the codec
	// cannot judge exactly covers nothing, and the human is asked instead.
	Covers(rule string, ask PermissionAsk) bool
	// Hooks are the hooks that carry the engine's asks to the approval
	// route, in the engine's own events and matchers, for a run whose
	// approval timeout is timeout.
	Hooks(timeout time.Duration) wire.UnifiedHooks
}
