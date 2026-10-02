package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

func hookEnv(url, token string) func(string) string {
	env := sessions.EncodeHookReach(sessions.Endpoint{URL: url, Credential: token})
	return func(k string) string { return env[k] }
}

// TestRunHookPermission_ForwardsTheAskAndWritesTheDecision: the hook hands
// the engine's payload, verbatim, to the runner's approval hook under the
// session's bearer, naming its event, and writes the runner's answer — the
// engine's native decision — to stdout untouched.
func TestRunHookPermission_ForwardsTheAskAndWritesTheDecision(t *testing.T) {
	var gotAuth, gotEvent, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotEvent = r.URL.Query().Get(runner.HookEventParam)
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"hookSpecificOutput":{"decision":{"behavior":"allow"}}}`))
	}))
	t.Cleanup(srv.Close)

	var out bytes.Buffer
	err := runHookPermission(context.Background(), "PermissionRequest", strings.NewReader(`{"tool_name":"Bash"}`), &out, hookEnv(srv.URL+runner.HookPath, "tok"), srv.Client())
	require.NoError(t, err)
	assert.Equal(t, "Bearer tok", gotAuth)
	assert.Equal(t, "PermissionRequest", gotEvent)
	assert.Equal(t, `{"tool_name":"Bash"}`, gotBody)
	assert.Equal(t, `{"hookSpecificOutput":{"decision":{"behavior":"allow"}}}`, out.String())
}

// TestRunHookPermission_AnyFailureDecidesNothing: no endpoint in the
// environment, an unreachable runner, or a refusal from it all leave stdout
// EMPTY — no decision, which an engine nobody sits at denies — and
// the reason is returned for the diagnostic channel.
func TestRunHookPermission_AnyFailureDecidesNothing(t *testing.T) {
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no host anchor", http.StatusBadRequest)
	}))
	t.Cleanup(refusing.Close)
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()

	for name, env := range map[string]func(string) string{
		"no endpoint":     func(string) string { return "" },
		"unreachable":     hookEnv(deadURL+runner.HookPath, "tok"),
		"refused by host": hookEnv(refusing.URL+runner.HookPath, "tok"),
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			err := runHookPermission(context.Background(), "PermissionRequest", strings.NewReader(`{}`), &out, env, http.DefaultClient)
			require.Error(t, err)
			assert.Empty(t, out.String(), "a failed hook writes no decision")
		})
	}
}
