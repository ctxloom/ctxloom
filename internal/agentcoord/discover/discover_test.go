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

	eps := List()
	require.Len(t, eps, 1)
	assert.Equal(t, "http://127.0.0.1:54321/mcp", eps[0].URL)
	assert.Equal(t, "tok-a", eps[0].Cred)
}

func TestList_SkipsIncompleteOrMalformedFiles(t *testing.T) {
	home := testsupport.Isolate(t)
	writeEndpoint(t, home, "proj-no-cred", `{"loopback_port":1}`, time.Now())
	writeEndpoint(t, home, "proj-no-port", `{"consumer_cred":"tok"}`, time.Now())
	writeEndpoint(t, home, "proj-garbage", `not json`, time.Now())

	assert.Empty(t, List(), "a coordinator with no minted consumer credential or unparsable file is skipped, not erred")
}

func TestList_MostRecentlyActiveFirst(t *testing.T) {
	home := testsupport.Isolate(t)
	older := time.Now().Add(-1 * time.Hour)
	newer := time.Now()
	writeEndpoint(t, home, "proj-old", `{"loopback_port":1,"consumer_cred":"old"}`, older)
	writeEndpoint(t, home, "proj-new", `{"loopback_port":2,"consumer_cred":"new"}`, newer)

	eps := List()
	require.Len(t, eps, 2)
	assert.Equal(t, "new", eps[0].Cred, "the most recently active coordinator's endpoint must sort first")
	assert.Equal(t, "old", eps[1].Cred)
}

func TestList_NoCoordDirYieldsEmpty(t *testing.T) {
	testsupport.Isolate(t)
	assert.Empty(t, List())
}
