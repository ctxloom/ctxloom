package coord

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// factAt is the ONLY place time enters a journal (facts.go): these tests pin
// what it must produce, so the marshal-and-stamp contract cannot drift while
// its construction is being reshaped.

func TestFactAt_CarriesKindTimeAndMarshalledPayload(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	got := factAt(factRunEnded, at, runEnded{RunID: "run-a", Cause: CauseChatClose, Detail: "d"})

	assert.Equal(t, factRunEnded, got.Kind)
	assert.True(t, got.At.Equal(at), "the fact carries the command-time timestamp verbatim: %s", got.At)

	var back runEnded
	require.NoError(t, json.Unmarshal(got.Data, &back))
	assert.Equal(t, runEnded{RunID: "run-a", Cause: CauseChatClose, Detail: "d"}, back)
}

// TestFactAt_RoundTripsThroughTheJournalLine: a fact is only useful if the
// journal line it becomes replays back into the same fact — the encoding is
// the durable contract, not an implementation detail.
func TestFactAt_RoundTripsThroughTheJournalLine(t *testing.T) {
	at := time.Unix(1_700_000_123, 0).UTC()
	fact := factAt(factItem, at, itemFact{RunID: "run-a", Seq: 7, Kind: "message_delta", Chars: 3})

	line, err := json.Marshal(fact)
	require.NoError(t, err)
	var back Fact
	require.NoError(t, json.Unmarshal(line, &back))

	assert.Equal(t, fact.Kind, back.Kind)
	assert.True(t, back.At.Equal(fact.At))
	assert.JSONEq(t, string(fact.Data), string(back.Data))
}

// TestFactAt_PanicsOnAnUnmarshallablePayload pins the fail-loud choice: a
// payload that cannot be marshalled is a programming error, and dropping the
// state silently would leave a journal that disagrees with the folds.
func TestFactAt_PanicsOnAnUnmarshallablePayload(t *testing.T) {
	assert.PanicsWithValue(t,
		"coord: marshal "+factRunEnded+" fact: json: unsupported type: chan int",
		func() { factAt(factRunEnded, time.Unix(1, 0), struct{ Ch chan int }{}) },
		"an unmarshallable payload must panic, naming the fact kind")
}
