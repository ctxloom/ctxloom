package kiro

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/transcript/vendorreader"
)

// captureRecorder is a transcript.Recorder that keeps what it is handed, so
// two spellings of the same conversion can be compared event for event
// without going anywhere near a file.
type captureRecorder struct{ evs []agent.ChatEvent }

func (c *captureRecorder) Record(ev agent.ChatEvent) error {
	c.evs = append(c.evs, ev)
	return nil
}
func (c *captureRecorder) Close() error { return nil }

// handRolledTail is Adapter.Convert's own tail as it stood before it was
// collapsed onto vendorreader.ConvertJSONLLines — the "before" side of this
// package's duplicate-collapse parity check, kept verbatim so the comparison
// stays meaningful after the production copy is gone.
func handRolledTail(ctx context.Context, rec *captureRecorder, doc *conversationDoc, conversationID string) error {
	if info := sessionInfo(conversationID, doc); info != nil {
		if err := rec.Record(agent.ChatEvent{Session: info}); err != nil {
			return fmt.Errorf("kiro: record session: %w", err)
		}
	}

	c := &converter{record: vendorreader.RecordFunc(rec, "kiro")}
	for _, turnRaw := range doc.History {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := c.handleTurn(turnRaw, doc.ModelInfo); err != nil {
			return err
		}
	}
	if len(doc.History) > 0 && c.entries == 0 {
		return fmt.Errorf("kiro: conversation %s has %d history turn(s) but converted ZERO transcript entries — the vendor format this build parses no longer matches the document", conversationID, len(doc.History))
	}
	return nil
}

// sharedDriverTail is the same conversion expressed through the shared
// two-pass driver every other adapter runs.
func sharedDriverTail(ctx context.Context, rec *captureRecorder, doc *conversationDoc, conversationID string) error {
	c := &converter{record: vendorreader.RecordFunc(rec, "kiro")}
	return vendorreader.ConvertJSONLLines(ctx, rec, rawTurns(doc), "kiro", sessionInfo(conversationID, doc),
		func(turn []byte) error { return c.handleTurn(turn, doc.ModelInfo) },
		func() error { return c.checkFloor(conversationID, len(doc.History)) },
	)
}

// fixtureDoc decodes the committed real-shaped conversation row's own value
// blob into the document Convert's tail actually operates on.
func fixtureDoc(t *testing.T) *conversationDoc {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "conversation-fixture.json"))
	require.NoError(t, err)
	var row fixtureRow
	require.NoError(t, json.Unmarshal(raw, &row))
	var doc conversationDoc
	require.NoError(t, json.Unmarshal(row.Value, &doc))
	return &doc
}

// TestConvertTail_ParityWithSharedJSONLDriver compares BOTH spellings of
// Convert's tail — the hand-rolled one and vendorreader.ConvertJSONLLines — over
// the same decoded document, before the hand-rolled one is removed. Any
// divergence between them is the defect the duplication was hiding.
//
// Every arm the tail can take is covered, because parity on the happy path
// alone would not reach the branches that could differ: a real conversation, a
// document with no history turns, one whose every turn is unrecognized (the
// zero-entries floor), and one carrying no session metadata at all (the
// no-Session-event arm). A cancelled context is checked separately, since it
// is the one arm whose answer depends on WHEN the check runs.
func TestConvertTail_ParityWithSharedJSONLDriver(t *testing.T) {
	real := fixtureDoc(t)
	cases := []struct {
		name string
		doc  *conversationDoc
	}{
		{"real conversation", real},
		{"no history turns", decodeDoc(t, `{"conversation_id":"c1","model_info":{"model_id":"m","context_window_tokens":7},"history":[]}`)},
		{"every turn unrecognized", decodeDoc(t, `{"conversation_id":"c1","model_info":{"model_id":"m","context_window_tokens":7},"history":[{"user":{"content":{"Unmodeled":{}}},"assistant":{"Unmodeled":{}}}]}`)},
		{"no session metadata", decodeDoc(t, `{"history":[]}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hand, shared captureRecorder
			handErr := handRolledTail(t.Context(), &hand, tc.doc, "c1")
			sharedErr := sharedDriverTail(t.Context(), &shared, tc.doc, "c1")

			if handErr == nil {
				require.NoError(t, sharedErr)
			} else {
				require.EqualError(t, sharedErr, handErr.Error())
			}
			require.Equal(t, hand.evs, shared.evs)
		})
	}

	t.Run("cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var hand, shared captureRecorder
		handErr := handRolledTail(ctx, &hand, real, "c1")
		sharedErr := sharedDriverTail(ctx, &shared, real, "c1")
		require.ErrorIs(t, handErr, context.Canceled)
		require.ErrorIs(t, sharedErr, context.Canceled)
		require.Equal(t, hand.evs, shared.evs)
	})
}

func decodeDoc(t *testing.T, s string) *conversationDoc {
	t.Helper()
	var doc conversationDoc
	require.NoError(t, json.Unmarshal([]byte(s), &doc))
	return &doc
}
