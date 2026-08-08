package vendorreader

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// latchCase drives one setter through the shared rule and reads back the field
// it owns, so every setter is exercised by the SAME table rather than by four
// bespoke assertions that could each drift.
type latchCase struct {
	name  string
	zero  func(*SessionInfoBuilder)       // set with the field's zero value
	first func(*SessionInfoBuilder)       // set with a first non-zero value
	later func(*SessionInfoBuilder)       // set again with a DIFFERENT value
	read  func(agent.ChatSessionInfo) any // the field this setter owns
	want  any                             // the value that must survive
}

func latchCases() []latchCase {
	return []latchCase{
		{
			name:  "SetSessionID",
			zero:  func(b *SessionInfoBuilder) { b.SetSessionID("") },
			first: func(b *SessionInfoBuilder) { b.SetSessionID("first") },
			later: func(b *SessionInfoBuilder) { b.SetSessionID("second") },
			read:  func(i agent.ChatSessionInfo) any { return i.SessionID },
			want:  "first",
		},
		{
			name:  "SetModel",
			zero:  func(b *SessionInfoBuilder) { b.SetModel("") },
			first: func(b *SessionInfoBuilder) { b.SetModel("first-model") },
			later: func(b *SessionInfoBuilder) { b.SetModel("second-model") },
			read:  func(i agent.ChatSessionInfo) any { return i.Model },
			want:  "first-model",
		},
		{
			name:  "SetPermissionMode",
			zero:  func(b *SessionInfoBuilder) { b.SetPermissionMode("") },
			first: func(b *SessionInfoBuilder) { b.SetPermissionMode("on-request") },
			later: func(b *SessionInfoBuilder) { b.SetPermissionMode("never") },
			read:  func(i agent.ChatSessionInfo) any { return i.PermissionMode },
			want:  "on-request",
		},
		{
			name:  "SetContextWindow",
			zero:  func(b *SessionInfoBuilder) { b.SetContextWindow(0) },
			first: func(b *SessionInfoBuilder) { b.SetContextWindow(1000) },
			later: func(b *SessionInfoBuilder) { b.SetContextWindow(2000) },
			read:  func(i agent.ChatSessionInfo) any { return i.ContextWindow },
			want:  1000,
		},
	}
}

// TestSessionInfoBuilder_EverySetterObeysTheSameLatchRule is the
// characterization pin for a collapse of four duplicated setters onto one
// shared rule — "take the first non-zero value, and only then record
// that something was found" — differing only in the field and its zero test,
// and the collapse onto a single generic helper is behaviour-preserving BY
// CONSTRUCTION, so no test can be red for it (a new canonical symbol cannot
// be tested before it compiles). This pins the
// shared rule at the PUBLIC seam above the duplication instead, unchanged by
// the collapse and red only if one setter's behaviour ever diverges from the
// others'.
func TestSessionInfoBuilder_EverySetterObeysTheSameLatchRule(t *testing.T) {
	for _, tc := range latchCases() {
		t.Run(tc.name, func(t *testing.T) {
			// A zero value never latches and never marks anything found.
			var zeroOnly SessionInfoBuilder
			tc.zero(&zeroOnly)
			assert.Nil(t, zeroOnly.Build(), "a zero value must not count as found")

			// The first non-zero value wins and a later one never displaces it.
			var b SessionInfoBuilder
			tc.first(&b)
			tc.later(&b)
			info := b.Build()
			require.NotNil(t, info, "one non-zero value is enough to make the builder found")
			assert.Equal(t, tc.want, tc.read(*info), "first non-zero value wins")

			// A zero value arriving AFTER the latch is also inert.
			tc.zero(&b)
			after := b.Build()
			require.NotNil(t, after)
			assert.Equal(t, tc.want, tc.read(*after), "a later zero value must not clear the latch")
		})
	}
}

// TestSessionInfoBuilder_SettersTouchOnlyTheirOwnField pins the other half of
// the rule a shared helper could plausibly break: each setter writes exactly
// one field and leaves the other three at their zero values.
func TestSessionInfoBuilder_SettersTouchOnlyTheirOwnField(t *testing.T) {
	for _, tc := range latchCases() {
		t.Run(tc.name, func(t *testing.T) {
			var b SessionInfoBuilder
			tc.first(&b)
			info := b.Build()
			require.NotNil(t, info)

			for _, other := range latchCases() {
				if other.name == tc.name {
					continue
				}
				assert.Zero(t, other.read(*info),
					"%s must not disturb the field %s owns", tc.name, other.name)
			}
		})
	}
}
