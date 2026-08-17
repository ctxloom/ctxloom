//go:build arch

package cli

import (
	"reflect"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport/parity"
)

// ---------------------------------------------------------------------------
// The THIRD mirror.
//
// chatEventToJSON (run_structured.go) is a hand-written projection of
// agent.ChatEvent onto the NDJSON DTOs that `ctxloom run --format json` emits —
// "the contract a GUI frontend consumes", and the channel the ctxloom VSCode
// frontend actually reads. It is the same shape of code as the canonical
// transcript's payload converters, and it had the same defect, worse: the
// entry variant dropped EIGHT agent fields (Sidechain, ToolCallID, ToolKind,
// ToolLocations, ToolContent, ContentBlocks, SystemKind, Plan) and the session
// variant dropped two (SessionID, Resumable). No finding row named it; it was
// found by later remediation work, not by the review corpus.
//
// The gate is the same engine the transcript pair rides
// (internal/testsupport/parity), extended to these pairs rather than rebuilt —
// the machinery lived in a _test.go file and so was not importable, which is
// the only reason it moved. Its three halves are documented there.
// ---------------------------------------------------------------------------

func chatJSONParityPairs() []parity.Pair {
	return []parity.Pair{
		{
			Name:       "SessionEntry↔chatEntryJSON",
			AgentType:  agent.SessionEntry{},
			MirrorType: chatEntryJSON{},
			Exempt:     map[string]string{},
			Convert: func(v reflect.Value) any {
				e := v.Addr().Interface().(*agent.SessionEntry)
				return chatEventToJSON(agent.ChatEvent{Entry: e}).Entry
			},
		},
		{
			Name:       "ChatSessionInfo↔chatSessionJSON",
			AgentType:  agent.ChatSessionInfo{},
			MirrorType: chatSessionJSON{},
			Exempt:     map[string]string{},
			Convert: func(v reflect.Value) any {
				s := v.Addr().Interface().(*agent.ChatSessionInfo)
				return chatEventToJSON(agent.ChatEvent{Session: s}).Session
			},
		},
		{
			Name:       "TurnMeta↔chatCompleteJSON",
			AgentType:  agent.TurnMeta{},
			MirrorType: chatCompleteJSON{},
			Exempt:     map[string]string{},
			Convert: func(v reflect.Value) any {
				m := v.Addr().Interface().(*agent.TurnMeta)
				return chatEventToJSON(agent.ChatEvent{Complete: m}).Complete
			},
		},
	}
}

func TestArch_ChatJSON_MirrorsAgentFieldCount(t *testing.T) {
	parity.CheckFieldCount(t, chatJSONParityPairs())
}

func TestArch_ChatJSON_ConverterCopiesEveryAgentField(t *testing.T) {
	parity.CheckConverterCopies(t, chatJSONParityPairs())
}

// TestArch_ChatJSON_BoolFieldsLandDistinctly closes the count-based half's blind
// spot on the frontend-facing channel: SessionEntry carries two bools
// (IsError, Sidechain) and dropping one while duplicating the other keeps the
// count of `true` leaves right.
func TestArch_ChatJSON_BoolFieldsLandDistinctly(t *testing.T) {
	parity.CheckBoolIsolation(t, chatJSONParityPairs())
}
