package discover

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

func writeEndpoint(t *testing.T, home, projectKey, body string, at time.Time) {
	t.Helper()
	dir := filepath.Join(home, ".ctxloom", "coord", projectKey)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	path := filepath.Join(dir, "endpoint.json")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	require.NoError(t, os.Chtimes(path, at, at))
}

func TestList_ParsesEndpointFiles(t *testing.T) {
	home := testsupport.Isolate(t)
	writeEndpoint(t, home, "proj-a", `{"loopback_port":54321,"consumer_cred":"tok-a"}`, time.Now())

	eps, skipped := List()
	require.Len(t, eps, 1)
	assert.Equal(t, "http://127.0.0.1:54321/mcp", eps[0].URL)
	assert.Equal(t, "tok-a", eps[0].Cred)
	assert.Empty(t, skipped)
}

// TestList_SkipsIncompleteOrMalformedFiles (U025-F02): the documented,
// common "not minted yet" case (missing cred or port, but VALID JSON) stays
// a SILENT skip — not reported. Malformed JSON is a genuinely different
// failure mode (I/O/decode, not "healthy but early") and must now be
// REPORTED via skipped, not collapsed into the same silent nothing.
func TestList_SkipsIncompleteOrMalformedFiles(t *testing.T) {
	home := testsupport.Isolate(t)
	writeEndpoint(t, home, "proj-no-cred", `{"loopback_port":1}`, time.Now())
	writeEndpoint(t, home, "proj-no-port", `{"consumer_cred":"tok"}`, time.Now())
	writeEndpoint(t, home, "proj-garbage", `not json`, time.Now())

	eps, skipped := List()
	assert.Empty(t, eps, "a coordinator with no minted consumer credential or unparsable file is skipped, not erred")
	require.Len(t, skipped, 1, "the malformed-JSON candidate must be REPORTED (the two valid-but-incomplete "+
		"candidates must NOT — that is the documented common case, kept silent on purpose)")
	assert.Contains(t, skipped[0].Error(), "proj-garbage")
}

// TestList_ReportsUnreadableFile (U025-F02): a file that exists but cannot
// be read (permission denied) must be distinguishable from "no file exists
// at all" — the whole point of the finding (sessionfeed.go's consumer used
// to assert the latter unconditionally).
func TestList_ReportsUnreadableFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: file permissions are not enforced")
	}
	home := testsupport.Isolate(t)
	writeEndpoint(t, home, "proj-locked", `{"loopback_port":1,"consumer_cred":"tok"}`, time.Now())
	path := filepath.Join(home, ".ctxloom", "coord", "proj-locked", "endpoint.json")
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	eps, skipped := List()
	assert.Empty(t, eps)
	require.Len(t, skipped, 1, "an unreadable endpoint.json must be reported, not silently indistinguishable "+
		"from no candidate existing")
	assert.Contains(t, skipped[0].Error(), "proj-locked")
}

func TestList_MostRecentlyActiveFirst(t *testing.T) {
	home := testsupport.Isolate(t)
	older := time.Now().Add(-1 * time.Hour)
	newer := time.Now()
	writeEndpoint(t, home, "proj-old", `{"loopback_port":1,"consumer_cred":"old"}`, older)
	writeEndpoint(t, home, "proj-new", `{"loopback_port":2,"consumer_cred":"new"}`, newer)

	eps, skipped := List()
	require.Len(t, eps, 2)
	assert.Equal(t, "new", eps[0].Cred, "the most recently active coordinator's endpoint must sort first")
	assert.Equal(t, "old", eps[1].Cred)
	assert.Empty(t, skipped)
}

func TestList_NoCoordDirYieldsEmpty(t *testing.T) {
	testsupport.Isolate(t)
	eps, skipped := List()
	assert.Empty(t, eps)
	assert.Empty(t, skipped, "no coord dir at all is not an error — nothing to report")
}
