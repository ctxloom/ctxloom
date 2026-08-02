package remote

import (
	"bytes"
	"net/http"
	"os"
	"testing"

	"github.com/google/go-github/v60/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// The 401-retry notice was written to os.Stderr with a raw
// fmt.Fprintf while everything else in this package that has something to warn
// about goes through clidiag (lockfile.go, pull.go, normalize.go).
//
// The difference is not house style. clidiag owns two properties this line
// needs and could not have. Its sink is redirectable, because os.Stderr is NOT
// always safe to write to -- under `ctxloom run` stderr IS the terminal the
// harness paints its TUI on, and a session that owns the terminal redirects
// the sink for its lifetime; an unconditional write lands mid-frame. And it
// owns the structured wire shape, so under --format json the line is a
// WarningEnvelope rather than loose text in a JSON stream.
func TestGitHubFetcher_TokenRetryWarningGoesThroughClidiag(t *testing.T) {
	fetcher := &GitHubFetcher{fallback: newMockGitHubClient()}
	resp401 := &github.Response{Response: &http.Response{StatusCode: http.StatusUnauthorized}}

	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	defer restore()

	require.True(t, fetcher.shouldRetry401(resp401, nil))

	assert.Contains(t, sink.String(), "retrying without authentication",
		"the notice must reach the redirected sink, not the raw terminal")
	assert.Contains(t, sink.String(), "ctxloom: warning:",
		"and wear the family's diagnostic shape")
}

// The three CTXLOOM_DEBUG_HTTP traces in loggingTransport are deliberately NOT
// moved: they are traces, not warnings, and clidiag speaks only "warning". The
// row reads them as the same job as the notice above; they are not. What they
// DO share is the raw os.Stderr write, so this pins what they are -- quiet
// unless the switch is on -- and leaves the sink question on the record rather
// than half-answering it.
func TestLoggingTransport_QuietUnlessDebugSwitchIsOn(t *testing.T) {
	t.Setenv("CTXLOOM_DEBUG_HTTP", "")

	stderr := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w
	defer func() { os.Stderr = stderr }()

	transport := &loggingTransport{base: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusInternalServerError, Body: http.NoBody}, nil
	})}
	req, err := http.NewRequest(http.MethodGet, "https://example.test/x", nil)
	require.NoError(t, err)
	_, err = transport.RoundTrip(req)
	require.NoError(t, err)

	require.NoError(t, w.Close())
	os.Stderr = stderr
	var captured bytes.Buffer
	_, err = captured.ReadFrom(r)
	require.NoError(t, err)
	assert.Empty(t, captured.String(), "a 500 must stay silent while the debug switch is off")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
