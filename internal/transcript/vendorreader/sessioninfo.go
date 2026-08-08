package vendorreader

import "github.com/ctxloom/ctxloom/internal/shared/agent"

// SessionInfoBuilder accumulates ChatSessionInfo fields discovered while
// scanning (codex, claude) or decoding (kiro) a vendor transcript for
// session-level metadata, latching each field onto its FIRST non-empty/
// non-zero value seen — mirroring transcript.Recorder.Record's own "latch
// onto the first KindSession line" discipline (recorder.go) — and tracking
// whether anything was ever found at all. A vendor transcript that carries
// no session metadata anywhere must yield a nil *ChatSessionInfo (no Session
// ChatEvent emitted), never an all-zero-value one that would misrepresent
// "nothing observed" as "genuinely empty on purpose."
//
// The zero value is ready to use.
type SessionInfoBuilder struct {
	info  agent.ChatSessionInfo
	found bool
}

// latch is the one rule every Set* method below applies: store v at dst and
// mark the builder found, but only when v is non-zero AND dst is still zero.
// Both halves matter and neither is redundant — the non-zero test is what
// keeps an absent vendor field from counting as observed metadata, and the
// still-zero test is what makes the latch first-wins rather than last-wins.
//
// A free function rather than a method because Go methods cannot take type
// parameters; comparable is exactly the constraint the rule needs, since it
// is written entirely in terms of a type's zero value.
func latch[T comparable](b *SessionInfoBuilder, dst *T, v T) {
	var zero T
	if v == zero || *dst != zero {
		return
	}
	*dst = v
	b.found = true
}

// SetSessionID latches id if it is non-empty and no SessionID has been set
// yet.
func (b *SessionInfoBuilder) SetSessionID(id string) { latch(b, &b.info.SessionID, id) }

// SetModel latches model if it is non-empty and no Model has been set yet.
func (b *SessionInfoBuilder) SetModel(model string) { latch(b, &b.info.Model, model) }

// SetPermissionMode latches mode if it is non-empty and no PermissionMode
// has been set yet.
func (b *SessionInfoBuilder) SetPermissionMode(mode string) { latch(b, &b.info.PermissionMode, mode) }

// SetContextWindow latches window if it is non-zero and no ContextWindow has
// been set yet.
func (b *SessionInfoBuilder) SetContextWindow(window int) { latch(b, &b.info.ContextWindow, window) }

// Build returns the accumulated info, or nil if nothing was ever latched.
func (b *SessionInfoBuilder) Build() *agent.ChatSessionInfo {
	if !b.found {
		return nil
	}
	info := b.info
	return &info
}
