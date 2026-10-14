package interaction_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
)

// TestServe_TheMailPeekIsGuardedAndAnswersBothOps: the mail-peek hook's POST
// rides the session endpoint's listener behind the same bearer as the MCP
// surface and the approval hook. A fetch with nothing waiting answers an empty
// peek; a confirm of a lease the runner never issued delivers nothing; an op
// it does not know is refused rather than guessed at.
func TestServe_TheMailPeekIsGuardedAndAnswersBothOps(t *testing.T) {
	lo := loadoutAt(freePort(t))
	serveWithHome(t, lo, deadHome(t))
	u, err := url.Parse(lo.MCP.URL)
	require.NoError(t, err)
	u.Path = runner.MailPath
	target := u.String()
	bearer := map[string]string{"Authorization": "Bearer bearer-token"}

	code, _ := postHook(t, target, nil, `{"op":"fetch","max_chars":7500}`)
	assert.Equal(t, http.StatusUnauthorized, code, "no bearer, no mail")

	code, body := postHook(t, target, bearer, `{"op":"fetch","max_chars":7500}`)
	require.Equal(t, http.StatusOK, code, body)
	var peek runner.MailPeek
	require.NoError(t, json.Unmarshal([]byte(body), &peek))
	assert.Empty(t, peek.Context)
	assert.Empty(t, peek.IDs)

	code, body = postHook(t, target, bearer, `{"op":"confirm","lease":"never-issued","ids":["m-1"]}`)
	require.Equal(t, http.StatusOK, code, body)
	var confirmed runner.MailConfirmed
	require.NoError(t, json.Unmarshal([]byte(body), &confirmed))
	assert.Zero(t, confirmed.Confirmed)

	code, _ = postHook(t, target, bearer, `{"op":"steal"}`)
	assert.Equal(t, http.StatusBadRequest, code)
}
