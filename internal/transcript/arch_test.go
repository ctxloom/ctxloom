//go:build arch

package transcript

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport/parity"
)

// ---------------------------------------------------------------------------
// This is the class gate behind two real defects found by hand: an agent
// field silently dropped on its way to disk, twice.
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
// (internal/cli/arch_test.go) now ride too, after the SAME class
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

func TestArch_TranscriptPayload_MirrorsAgentFieldCount(t *testing.T) {
	parity.CheckFieldCount(t, parityPairs())
}

func TestArch_TranscriptPayload_ConverterCopiesEveryAgentField(t *testing.T) {
	parity.CheckConverterCopies(t, parityPairs())
}

// TestArch_TranscriptPayload_BoolFieldsLandDistinctly closes the blind spot the count-based
// half left: SessionEntry has TWO bools (IsError, Sidechain), and a converter
// writing one of them into both payload slots satisfies a count of trues.
func TestArch_TranscriptPayload_BoolFieldsLandDistinctly(t *testing.T) {
	parity.CheckBoolIsolation(t, parityPairs())
}

// ---------------------------------------------------------------------------
// Half 4 — THE READ SIDE.
//
// The three halves above all gate the agent → payload direction. They are
// blind, by construction, to the OTHER converter in this package's four-place
// edit: a payload → agent reader that forgets a field leaves the bytes on disk
// perfectly correct and simply never hands them back. Every write-side
// assertion still passes; the data is lost anyway, at read time, for every
// consumer of the canonical transcript (memory distillation, session-load
// replay, `session list`, the resume picker).
//
// This is the same defect class that produced two real drops on the
// write side, and it has exactly one honest assertion: fill an agent value
// totally by reflection, write it, read it back through the REAL bytes, and
// require the whole struct. A named per-field check covers what its author
// thought of; a field added tomorrow is covered here without editing this file.
//
// SCOPE — and why it is one pair and not four. This package writes four
// payloads but reads back exactly ONE. entriesFromRecord (history.go) is the
// only payload → agent converter in the repo: KindSession/KindComplete/
// KindPermission lines are envelope metadata that ParseTranscriptFile reads
// for their `ts` alone and never reconstitutes into agent.ChatSessionInfo /
// agent.TurnMeta / agent.PermissionRequest. Those three are write-only today,
// so there is no read side to gate — gating a converter that does not exist
// would be a gate satisfiable by the wrong evidence. When a reader is added
// for any of them, it gets a pair here.
// ---------------------------------------------------------------------------

// roundTripPairs is the transcript's set of agent-type ↔ on-disk-payload
// projections that have BOTH converters.
func roundTripPairs() []parity.RoundTripPair {
	return []parity.RoundTripPair{
		{
			Name:      "SessionEntry→EntryPayload→SessionEntry",
			AgentType: agent.SessionEntry{},
			// No exemptions. Timestamp is exempt on the WRITE-side pairs above
			// because it lives in the envelope rather than the payload, but the
			// envelope is part of the round trip, so it must come back too —
			// this half is strictly stronger there.
			Exempt: map[string]string{},
			RoundTrip: func(v reflect.Value) any {
				t := parityT
				e := v.Addr().Interface().(*agent.SessionEntry)

				// Through the REAL on-disk bytes, not struct-to-struct: a
				// writer and reader that disagree about a json tag round-trip
				// perfectly in memory and lose the field on disk.
				line, err := json.Marshal(Record{
					V:         SchemaVersion,
					Harp:      "parity-harp",
					SessionID: "parity-session",
					Engine:    "parity",
					Seq:       0,
					TS:        e.Timestamp,
					Kind:      KindEntry,
					Entry:     entryPayload(e),
				})
				if err != nil {
					t.Fatalf("marshal record: %v", err)
				}
				var rec Record
				if err := json.Unmarshal(line, &rec); err != nil {
					t.Fatalf("unmarshal record: %v", err)
				}

				got := entriesFromRecord(rec)
				if len(got) != 1 {
					t.Fatalf("entriesFromRecord returned %d entries for a KindEntry record, want 1 — a dropped entry is total loss, not field loss (line: %s)", len(got), line)
				}
				return got[0]
			},
		},
	}
}

// parityT is set by TestArch_TranscriptPayload_ReadSideRoundTripsEveryField for the duration of
// its run so the RoundTrip closure can fail loudly. RoundTrip's signature is
// deliberately *T-free (the engine drives it), and a closure that swallowed a
// marshal error would be the very silent-no-op this package exists to prevent.
var parityT *testing.T

// TestArch_TranscriptPayload_ReadSideRoundTripsEveryField is half 4: the payload → agent
// direction, which no other test in this repo covers.
func TestArch_TranscriptPayload_ReadSideRoundTripsEveryField(t *testing.T) {
	parityT = t
	t.Cleanup(func() { parityT = nil })
	parity.CheckRoundTrip(t, roundTripPairs())
}
