package importer

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

func TestFlushComplete_NilPendingIsNoOp(t *testing.T) {
	var pending *agent.TurnMeta
	var called bool
	record := func(agent.ChatEvent) error { called = true; return nil }

	require.NoError(t, FlushComplete(&pending, record))
	assert.False(t, called, "a nil pending must never invoke record")
}

func TestFlushComplete_RecordsAndClears(t *testing.T) {
	pending := &agent.TurnMeta{InputTokens: 42}
	var got agent.ChatEvent
	record := func(ev agent.ChatEvent) error { got = ev; return nil }

	require.NoError(t, FlushComplete(&pending, record))
	assert.Nil(t, pending, "pending must be cleared after a successful flush")
	require.NotNil(t, got.Complete)
	assert.Equal(t, 42, got.Complete.InputTokens)
}

func TestFlushComplete_PropagatesRecordError(t *testing.T) {
	pending := &agent.TurnMeta{}
	record := func(agent.ChatEvent) error { return errors.New("boom") }

	err := FlushComplete(&pending, record)
	require.Error(t, err)
	assert.Equal(t, "boom", err.Error())
}
