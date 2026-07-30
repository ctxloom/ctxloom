package cli

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// The `type` field is the NDJSON contract's discriminator: the VSCode frontend
// switches on it, so these three strings are a published wire contract and not
// an internal label. Two independent producers emit them — the go-plugin
// structured path and the owner-owned container path — so the values are pinned
// here for both, and both producers name the same constants so the compiler
// links what a frontend already assumes is linked.
func TestChatEventJSON_DiscriminatorValues(t *testing.T) {
	assert.Equal(t, "entry", chatEventTypeEntry)
	assert.Equal(t, "complete", chatEventTypeComplete)
	assert.Equal(t, "session", chatEventTypeSession)

	assert.Equal(t, chatEventTypeEntry,
		chatEventToJSON(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant}}).Type)
	assert.Equal(t, chatEventTypeComplete,
		chatEventToJSON(agent.ChatEvent{Complete: &agent.TurnMeta{}}).Type)
	assert.Equal(t, chatEventTypeSession,
		chatEventToJSON(agent.ChatEvent{Session: &agent.ChatSessionInfo{}}).Type)
	assert.Empty(t, chatEventToJSON(agent.ChatEvent{}).Type,
		"an event with no payload carries no discriminator to switch on")
}

// The owner-owned container path is the second producer of the same contract:
// its json mode must emit the identical discriminator for an assistant delta,
// or a frontend consuming both feeds sees two vocabularies for one event.
func TestRenderOwnedRunEvents_EmitsTheSameEntryDiscriminator(t *testing.T) {
	ch := make(chan *agentcoordpb.AgentEvent, 4)
	ch <- ownedFinalStart("m1")
	ch <- ownedDelta("m1", "hello")
	ch <- ownedTurnIdle()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var out bytes.Buffer
	idle := make(chan string, 4)
	done := make(chan error, 1)
	go func() {
		_, err := renderOwnedRunEvents(ctx, &out, formatJSON, ownedTestRunID, ch, idle, true)
		done <- err
	}()

	select {
	case <-idle:
	case <-time.After(10 * time.Second):
		t.Fatal("no turn boundary observed")
	}
	cancel()
	<-done

	require.NotEmpty(t, out.String())
	assert.Contains(t, out.String(), `"type":"`+chatEventTypeEntry+`"`)
}
