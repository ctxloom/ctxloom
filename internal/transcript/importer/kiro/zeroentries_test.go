package kiro

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

// A conversation whose every history turn parses as
// historyTurn but whose user/assistant content unions are BOTH unrecognized
// still emitted a Complete boundary for each turn (mapping.go's handleTurn
// unconditionally records one), so a fully drifted conversation "converted"
// to N all-zero accounting records and zero real content, with no floor
// check anywhere — Convert returned nil, and the caller reported success.
func TestConvert_FullyDriftedTurnsIsAnError(t *testing.T) {
	testsupport.Isolate(t)

	doc := map[string]any{
		"conversation_id": "conv-drifted",
		"history": []any{
			map[string]any{
				"user":             map[string]any{"content": map[string]any{"unrecognized_variant": true}},
				"assistant":        map[string]any{"unrecognized_variant": true},
				"request_metadata": map[string]any{},
			},
		},
	}
	raw, err := json.Marshal(doc)
	require.NoError(t, err)

	dbPath := insertFixtureRow(t, fixtureRow{
		Key:            "conv-drifted",
		ConversationID: "conv-drifted",
		Value:          raw,
		CreatedAt:      1,
		UpdatedAt:      1,
	})

	rec, rerr := transcript.NewRecorder(fixtureHarp, "kiro")
	require.NoError(t, rerr)
	defer func() { _ = rec.Close() }()

	err = Adapter{}.Convert(context.Background(), rec, mustLocator(t, dbPath, "conv-drifted"))
	assert.Error(t, err, "a conversation with real turns but zero recognizable content is a failed import, not a successful empty one")
}

// Discriminator: a conversation with NO history turns at all is legitimately
// nothing to import (an empty/just-started conversation) and stays a
// success.
func TestConvert_EmptyHistoryIsLegitimatelyEmpty(t *testing.T) {
	testsupport.Isolate(t)

	doc := map[string]any{
		"conversation_id": "conv-empty",
		"history":         []any{},
	}
	raw, err := json.Marshal(doc)
	require.NoError(t, err)

	dbPath := insertFixtureRow(t, fixtureRow{
		Key:            "conv-empty",
		ConversationID: "conv-empty",
		Value:          raw,
		CreatedAt:      1,
		UpdatedAt:      1,
	})

	rec, rerr := transcript.NewRecorder(fixtureHarp, "kiro")
	require.NoError(t, rerr)
	defer func() { _ = rec.Close() }()

	assert.NoError(t, Adapter{}.Convert(context.Background(), rec, mustLocator(t, dbPath, "conv-empty")),
		"an empty history is 'nothing to import', not a failure")
}
