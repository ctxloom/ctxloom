package transcript

import (
	"reflect"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport/parity"
)

// ---------------------------------------------------------------------------
// U144-F10 — the class gate behind U144-F01 and U144-F02.
//
// record.go:9-12 claims the on-disk payload structs mirror agent.ChatEvent's
// variants "field-for-field". Nothing enforced that claim, and it had already
// silently failed TWICE (ChatSessionInfo.Resumable, PermissionRequest.
// ToolCallID) before anyone noticed. Each new agent field must today be
// edited in four places (agent type → payload struct → to-payload converter →
// from-payload converter); this gate turns "someone forgot one of the four"
// from a silent on-disk data loss into a build failure.
//
// The three halves and what each is for are documented on the shared engine,
// internal/testsupport/parity — which the `--format json` NDJSON DTOs
// (internal/cli/chat_json_parity_test.go) now ride too, after the SAME class
// of drop was found on that third mirror.
// ---------------------------------------------------------------------------

// parityPairs is the transcript's set of agent-type ↔ on-disk-payload mirrors.
func parityPairs() []parity.Pair {
	return []parity.Pair{
		{
			Name:       "SessionEntry↔EntryPayload",
			AgentType:  agent.SessionEntry{},
			MirrorType: EntryPayload{},
			Exempt: map[string]string{
				"Timestamp": "hoisted to the envelope's Record.TS",
			},
			Convert: func(v reflect.Value) any {
				e := v.Addr().Interface().(*agent.SessionEntry)
				return entryPayload(e)
			},
		},
		{
			Name:       "ChatSessionInfo↔SessionPayload",
			AgentType:  agent.ChatSessionInfo{},
			MirrorType: SessionPayload{},
			Exempt: map[string]string{
				"SessionID": "hoisted to the envelope's Record.SessionID",
			},
			Convert: func(v reflect.Value) any {
				s := v.Addr().Interface().(*agent.ChatSessionInfo)
				return sessionPayload(s)
			},
		},
		{
			Name:       "TurnMeta↔CompletePayload",
			AgentType:  agent.TurnMeta{},
			MirrorType: CompletePayload{},
			Exempt:     map[string]string{},
			Convert: func(v reflect.Value) any {
				m := v.Addr().Interface().(*agent.TurnMeta)
				return completePayload(m)
			},
		},
		{
			Name:       "PermissionRequest↔PermissionPayload",
			AgentType:  agent.PermissionRequest{},
			MirrorType: PermissionPayload{},
			Exempt:     map[string]string{},
			Convert: func(v reflect.Value) any {
				p := v.Addr().Interface().(*agent.PermissionRequest)
				return permissionPayload(p)
			},
		},
	}
}

func TestPayloadMirrorsAgentTypeFieldCount(t *testing.T) {
	parity.CheckFieldCount(t, parityPairs())
}

func TestPayloadConverterCopiesEveryAgentField(t *testing.T) {
	parity.CheckConverterCopies(t, parityPairs())
}

// TestPayloadBoolFieldsLandDistinctly closes the blind spot the count-based
// half left: SessionEntry has TWO bools (IsError, Sidechain), and a converter
// writing one of them into both payload slots satisfies a count of trues.
func TestPayloadBoolFieldsLandDistinctly(t *testing.T) {
	parity.CheckBoolIsolation(t, parityPairs())
}
