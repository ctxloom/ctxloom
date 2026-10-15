package interaction

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
)

// mailHome is a Home no coordinator ever answers: the mail handler only asks
// it what waits, and nothing does.
func mailHome(t *testing.T) *runner.Home {
	t.Helper()
	home, err := runner.NewHome(context.Background(), runner.HomeConfig{
		URL: "http://127.0.0.1:1/mcp", Token: "unused", Harness: "test", Version: "test", Harp: "h",
	})
	require.NoError(t, err)
	t.Cleanup(func() { home.Close(0, "") })
	return home
}

// postMail sends body straight to the mail handler and returns its status
// and answer.
func postMail(t *testing.T, home *runner.Home, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	mailHandler(home).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, runner.MailPath, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

// sizedSteal is a well-formed request of exactly n bytes whose op the handler
// does not know. The padding sits INSIDE the object, so a body cut short is
// no longer JSON: which refusal answers says how much of it was read.
func sizedSteal(t *testing.T, n int) string {
	t.Helper()
	const head, tail = `{"op":"steal"`, `}`
	require.Greater(t, n, len(head)+len(tail))
	return head + strings.Repeat(" ", n-len(head)-len(tail)) + tail
}

// TestMailHandler_ABodyOfExactlyTheBoundIsReadWhole: maxMailRequest is the
// largest request the handler takes, and it takes all of it — the request
// reaches the op dispatch (refused there for its op), never the size refusal
// and never a decode of a truncated body.
func TestMailHandler_ABodyOfExactlyTheBoundIsReadWhole(t *testing.T) {
	code, body := postMail(t, mailHome(t), sizedSteal(t, maxMailRequest))
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, body, `unknown mail-peek op "steal"`)
}

// TestMailHandler_ABodyPastTheBoundIsRefusedForItsSize: one byte over the
// bound is refused as oversized even though it would decode cleanly.
func TestMailHandler_ABodyPastTheBoundIsRefusedForItsSize(t *testing.T) {
	code, body := postMail(t, mailHome(t), sizedSteal(t, maxMailRequest+1))
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, body, "unreadable or oversized mail-peek request")
	assert.NotContains(t, body, "unknown mail-peek op")
}

// TestMailHandler_AFetchMustNameAPositiveCap: a fetch of max_chars 0 names no
// room to fill and is refused; 1 is the smallest cap it takes.
func TestMailHandler_AFetchMustNameAPositiveCap(t *testing.T) {
	home := mailHome(t)
	code, body := postMail(t, home, `{"op":"fetch","max_chars":0}`)
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, body, "max_chars")

	code, body = postMail(t, home, `{"op":"fetch","max_chars":1}`)
	assert.Equal(t, http.StatusOK, code, body)
}

// TestEndpointFs_NilIsTheOSFilesystemAndASetOneIsKept: an endpoint that
// names no filesystem serves over the OS's; one that names one serves over
// exactly that one.
func TestEndpointFs_NilIsTheOSFilesystemAndASetOneIsKept(t *testing.T) {
	assert.IsType(t, &afero.OsFs{}, Endpoint{}.fs())
	mem := afero.NewMemMapFs()
	assert.Same(t, mem, Endpoint{Fs: mem}.fs())
}
