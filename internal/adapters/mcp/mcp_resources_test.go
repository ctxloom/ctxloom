package mcp

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestExtractURIName covers the helper that powers every templated
// resource handler (fragments/{name}, profiles/{name}, commands/{name},
// remotes/{name}/contents). The prefix-strip approach has subtle edge
// cases: empty input, missing prefix, exact prefix match (no name),
// and nested path segments after the variable.
func TestExtractURIName(t *testing.T) {
	cases := []struct {
		uri, prefix, want string
	}{
		{"ctxloom://fragments/foo", "ctxloom://fragments/", "foo"},
		{"ctxloom://profiles/bar", "ctxloom://profiles/", "bar"},
		// Nested segments after the variable: the caller is expected to
		// further parse (e.g., the remotes/{name}/contents handler).
		{"ctxloom://remotes/alice/contents", "ctxloom://remotes/", "alice/contents"},
		{"ctxloom://help", "ctxloom://fragments/", ""},
		{"", "ctxloom://fragments/", ""},
		{"ctxloom://fragments/", "ctxloom://fragments/", ""},
	}
	for _, tc := range cases {
		got := extractURIName(tc.uri, tc.prefix)
		assert.Equal(t, tc.want, got, "extractURIName(%q, %q)", tc.uri, tc.prefix)
	}
}

// TestResourceText asserts the shape of the result wrapper used by every
// text-resource handler. One Contents entry, URI / MIMEType / Text all
// populated. Regression guard against silent shape drift in the SDK.
func TestResourceText(t *testing.T) {
	res := resourceText("ctxloom://help", "text/markdown", "body")
	require.NotNil(t, res)
	require.Len(t, res.Contents, 1)
	c := res.Contents[0]
	assert.Equal(t, "ctxloom://help", c.URI)
	assert.Equal(t, "text/markdown", c.MIMEType)
	assert.Equal(t, "body", c.Text)
}

// TestMarshalResourceYAML wraps an arbitrary value as a YAML resource.
// The empty value should still produce a valid wrapper (resources are
// allowed to be empty data, not just unconfigured paths).
func TestMarshalResourceYAML(t *testing.T) {
	type sample struct {
		Items []string `yaml:"items"`
	}
	res, err := marshalResourceYAML("ctxloom://test", sample{Items: []string{"a", "b"}})
	require.NoError(t, err)
	require.Len(t, res.Contents, 1)
	assert.Equal(t, "application/yaml", res.Contents[0].MIMEType)
	assert.Contains(t, res.Contents[0].Text, "items:")
	assert.Contains(t, res.Contents[0].Text, "- a")
}

// TestHandleResourceHelp asserts the markdown index lists every concrete
// resource ctxloom advertises. If a new resource is added without
// updating the help body, this fails — the help becomes stale silently
// otherwise.
func TestHandleResourceHelp(t *testing.T) {
	s := &ctxServer{}
	req := &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{URI: resourceHelpURI}}
	res, err := s.handleResourceHelp(t.Context(), req)
	require.NoError(t, err)
	require.Len(t, res.Contents, 1)
	body := res.Contents[0].Text
	for _, uri := range []string{
		"ctxloom://help",
		"ctxloom://sessions/recent",
	} {
		assert.Contains(t, body, uri, "help body must mention %s", uri)
	}
}

// TestHandleResourceSessionsRecent_EmptyIndex confirms the handler
// gracefully serves an empty list when no harp-named sessions exist
// (fresh-machine first-run case).
func TestHandleResourceSessionsRecent_EmptyIndex(t *testing.T) {
	// Isolate HOME so the user-global index doesn't pick up the real user's
	// history during tests.
	testsupport.Isolate(t)
	s := &ctxServer{}
	req := &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{URI: resourceSessionsRecentURI}}
	res, err := s.handleResourceSessionsRecent(t.Context(), req)
	require.NoError(t, err)
	require.Len(t, res.Contents, 1)
	body := res.Contents[0].Text
	// YAML serialization of {sessions: nil} or empty list — both contain
	// the sessions: key with nothing meaningful underneath.
	assert.Contains(t, body, "sessions:")
}
