package importer

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

// SetSessionID latches id if it is non-empty and no SessionID has been set
// yet.
func (b *SessionInfoBuilder) SetSessionID(id string) {
	if id != "" && b.info.SessionID == "" {
		b.info.SessionID = id
		b.found = true
	}
}

// SetModel latches model if it is non-empty and no Model has been set yet.
func (b *SessionInfoBuilder) SetModel(model string) {
	if model != "" && b.info.Model == "" {
		b.info.Model = model
		b.found = true
	}
}

// SetPermissionMode latches mode if it is non-empty and no PermissionMode
// has been set yet.
func (b *SessionInfoBuilder) SetPermissionMode(mode string) {
	if mode != "" && b.info.PermissionMode == "" {
		b.info.PermissionMode = mode
		b.found = true
	}
}

// SetContextWindow latches window if it is non-zero and no ContextWindow has
// been set yet.
func (b *SessionInfoBuilder) SetContextWindow(window int) {
	if window != 0 && b.info.ContextWindow == 0 {
		b.info.ContextWindow = window
		b.found = true
	}
}

// Build returns the accumulated info, or nil if nothing was ever latched.
func (b *SessionInfoBuilder) Build() *agent.ChatSessionInfo {
	if !b.found {
		return nil
	}
	info := b.info
	return &info
}
