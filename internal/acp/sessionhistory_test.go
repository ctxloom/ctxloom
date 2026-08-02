package acp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestACP_NoSessionHistory pins that the acp backend carried a placeholder
// SessionHistory whose ListSessions returned (nil, nil) while its three
// siblings on the same type returned "acp session history not yet supported".
// A caller therefore got a clean EMPTY session list and could not tell
// "this backend cannot answer" from "this workspace genuinely has none" — the
// list surface silently reported the absence of an answer as an answer.
//
// Every other engine now declares the absence by passing nil for
// SessionHistory, which grpc/sessionhistory.go and operations.HistoryForBackend
// already turn into "backend %s has no session history". acp does the same.
func TestACP_NoSessionHistory(t *testing.T) {
	assert.Nil(t, NewACP().History(),
		"acp must declare it has no session history rather than answer an empty list")
}
