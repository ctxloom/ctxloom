//go:build acceptance

package acceptance

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The setup-response verdict: the codeword alone is not enough, because the
// prompt that asks for it carries the guidance naming it. An engine that
// echoes its prompt (the mock: "mock chat: <prompt>") contains the codeword
// without having read anything — the false green this verdict refuses.
func TestJ000300SetupReplyConfirms(t *testing.T) {
	const cw = "J000300-LIVE-COMPANY-CODEWORD"
	require.NoError(t, j000300SetupReplyConfirms(cw+"\n", cw), "a real reply naming only the codeword")
	require.NoError(t, j000300SetupReplyConfirms("</think>"+cw, cw), "a stray reasoning tag around the codeword still confirms")

	err := j000300SetupReplyConfirms("mock chat: Between the markers below...\n<setup-guidance>\n...replying with the codeword "+cw+".\n</setup-guidance>", cw)
	require.Error(t, err, "an echo of the prompt carries the codeword without confirming it")
	assert.Contains(t, err.Error(), "echo")

	require.Error(t, j000300SetupReplyConfirms("I could not find a codeword.", cw))
}
