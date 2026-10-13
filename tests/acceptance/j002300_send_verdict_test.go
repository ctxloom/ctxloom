package acceptance

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/spool"
)

const (
	sendHarp   = "child-harp"
	sendMarker = "J002300-SEND-MARKER"
)

// autoReport is the runner's automatic turn report as the coordinator's spool
// holds it: result-kind, marked in its structured companion.
func autoReport(body string) *spool.Message {
	return &spool.Message{FromHarp: sendHarp, Kind: coord.KindResult, Body: body,
		Structured: map[string]any{"auto_report": true}}
}

// childSend is a message the child chose to send with agent_send: unmarked,
// whatever kind the model picked.
func childSend(kind, body string) *spool.Message {
	return &spool.Message{FromHarp: sendHarp, Kind: kind, Body: body}
}

// The agent_send cell passes ONLY on a body the child sent itself. The
// automatic turn report carries whatever the model said, so a model that
// merely SAYS the marker puts it in the coordinator's mailbox without ever
// calling agent_send — that is the false green this verdict exists to refuse.
func TestJ002300ScanSends_OnlyAnUnmarkedBodyCounts(t *testing.T) {
	cases := []struct {
		name     string
		mail     []*spool.Message
		hit      string
		autoOnly bool
	}{
		{"the child's agent_send carries it", []*spool.Message{childSend(coord.KindMessage, sendMarker)}, sendMarker, false},
		{"a send's kind is the model's choice and does not matter", []*spool.Message{childSend(coord.KindResult, "it is "+sendMarker)}, "it is " + sendMarker, false},
		{"only the automatic turn report carries it", []*spool.Message{autoReport("the marker is " + sendMarker)}, "", true},
		{"a send found after a report still counts", []*spool.Message{autoReport(sendMarker), childSend(coord.KindMessage, sendMarker)}, sendMarker, false},
		{"another child's send does not count", []*spool.Message{{FromHarp: "sibling", Kind: coord.KindMessage, Body: sendMarker}}, "", false},
		{"a send without the marker does not count", []*spool.Message{childSend(coord.KindMessage, "something else")}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := j002300ScanSends(c.mail, sendHarp, sendMarker)
			assert.Equal(t, c.hit, got.hit)
			assert.Equal(t, c.autoOnly, got.hit == "" && got.autoReport != "", "whether the marker arrived only in the automatic report")
		})
	}
}

// An error-kind message from the child (its launch failure) is reported, so
// the step can fail at once with the child's own words.
func TestJ002300ScanSends_ReportsTheChildsFailure(t *testing.T) {
	got := j002300ScanSends([]*spool.Message{childSend(coord.KindError, "launch failed")}, sendHarp, sendMarker)
	assert.Equal(t, []string{"launch failed"}, got.failed)
	assert.Equal(t, []string{"launch failed"}, got.seen)
}
