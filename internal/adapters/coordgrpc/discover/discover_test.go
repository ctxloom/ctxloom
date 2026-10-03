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

// testRootHarp names the one coordinator root each fixture project holds.
const testRootHarp = "root-harp"

// endpointPath is a root's endpoint.json: coord/<project-key>/<root-harp>/.
func endpointPath(home, projectKey, rootHarp string) string {
	return filepath.Join(home, ".ctxloom", "coord", projectKey, rootHarp, "endpoint.json")
}

func writeEndpointAt(t *testing.T, path, body string, at time.Time) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	require.NoError(t, os.Chtimes(path, at, at))
}

func writeEndpoint(t *testing.T, home, projectKey, body string, at time.Time) {
	t.Helper()
	writeEndpointAt(t, endpointPath(home, projectKey, testRootHarp), body, at)
}

// TestList_EveryRootOfAProjectIsAnEndpoint: one project holds one root per
// independent tree, and each root's coordinator is its own endpoint. A file
// at the project level is no root's, so it is not one.
func TestList_EveryRootOfAProjectIsAnEndpoint(t *testing.T) {
	home := testsupport.Isolate(t)
	now := time.Now()
	writeEndpointAt(t, endpointPath(home, "proj", "tree-a"), `{"loopback_port":1001,"consumer_cred":"tok-a"}`, now)
	writeEndpointAt(t, endpointPath(home, "proj", "tree-b"), `{"loopback_port":1002,"consumer_cred":"tok-b"}`, now.Add(-time.Second))
	writeEndpointAt(t, filepath.Join(home, ".ctxloom", "coord", "proj", "endpoint.json"), `{"loopback_port":1003,"consumer_cred":"tok-c"}`, now.Add(time.Second))

	eps, skipped := List()
	assert.Empty(t, skipped)
	creds := make([]string, 0, len(eps))
	for _, ep := range eps {
		creds = append(creds, ep.Cred)
	}
	assert.Equal(t, []string{"tok-a", "tok-b"}, creds, "both trees list, most recent first; the project-level file does not")
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

// TestList_SkipsIncompleteOrMalformedFiles: the documented,
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

// TestList_ReportsUnreadableFile: a file that exists but cannot
// be read (permission denied) must be distinguishable from "no file exists
// at all" — the whole point of the finding (sessionfeed.go's consumer used
// to assert the latter unconditionally).
func TestList_ReportsUnreadableFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: file permissions are not enforced")
	}
	home := testsupport.Isolate(t)
	writeEndpoint(t, home, "proj-locked", `{"loopback_port":1,"consumer_cred":"tok"}`, time.Now())
	path := endpointPath(home, "proj-locked", testRootHarp)
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
