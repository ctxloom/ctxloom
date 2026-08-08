package grpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/transcript/policy"
)

// stubSource is a SessionSource returning a fixed session, so these tests
// exercise the DECORATOR rather than any real reader.
type stubSource struct {
	sess  *agent.Session
	metas []agent.SessionMeta
	calls int
}

func (s *stubSource) GetSession(context.Context, string) (*agent.Session, error) {
	s.calls++
	return s.sess, nil
}
func (s *stubSource) CurrentSession(context.Context) (*agent.Session, error) {
	s.calls++
	return s.sess, nil
}
func (s *stubSource) ListSessions(context.Context) ([]agent.SessionMeta, error) {
	return s.metas, nil
}

func sessionWithContent(blocks ...agent.ToolContentBlock) *agent.Session {
	return &agent.Session{
		ID: "s1",
		Entries: []agent.SessionEntry{
			{Type: agent.EntryTypeUser, Content: "hello"},
			{Type: agent.EntryTypeToolResult, ToolOutput: "flattened", ToolContent: blocks},
		},
	}
}

// TestFilteredSource_FiltersOnRead is the wiring test for the READ side. Its
// predecessor lived on the write side and its absence there had already let a
// mutation survive silently, so it is kept pointed at whatever actually
// applies the policy.
func TestFilteredSource_FiltersOnRead(t *testing.T) {
	inner := &stubSource{sess: sessionWithContent(
		agent.ToolContentBlock{Kind: agent.KindContent, Text: "kept"},
		agent.ToolContentBlock{Kind: agent.KindProcessOutput, Raw: json.RawMessage(`{"stdout":"SENTINEL-STDOUT"}`)},
		agent.ToolContentBlock{Kind: agent.KindFileSnapshot, Raw: json.RawMessage(`{"originalFile":"SENTINEL-FILE"}`)},
	)}
	src := NewFilteredSource(inner, policy.Default())

	got, err := src.GetSession(context.Background(), "s1")
	require.NoError(t, err)
	require.Len(t, got.Entries, 2)

	blocks := got.Entries[1].ToolContent
	require.Len(t, blocks, 3,
		"withheld blocks are REPLACED by markers, never removed — a shorter list "+
			"would be indistinguishable from a tool returning less")
	assert.Equal(t, agent.KindContent, blocks[0].Kind)
	assert.Equal(t, agent.KindExcluded, blocks[1].Kind)
	assert.Equal(t, agent.KindExcluded, blocks[2].Kind)

	blob, err := json.Marshal(got)
	require.NoError(t, err)
	assert.NotContains(t, string(blob), "SENTINEL-STDOUT")
	assert.NotContains(t, string(blob), "SENTINEL-FILE")
	assert.Contains(t, string(blob), agent.KindProcessOutput,
		"the marker must name what was withheld, so absence is never silent")
}

// TestFilteredSource_LeavesStorageTotal is the property that justifies filtering
// on read rather than on write: the session the INNER source yielded — the
// on-disk truth — must be unchanged, so the policy stays reversible and a
// tier-2 source whose bytes exist nowhere else is never edited.
func TestFilteredSource_LeavesStorageTotal(t *testing.T) {
	raw := json.RawMessage(`{"stdout":"SENTINEL-STDOUT"}`)
	stored := sessionWithContent(
		agent.ToolContentBlock{Kind: agent.KindProcessOutput, Raw: raw},
	)
	inner := &stubSource{sess: stored}

	_, err := NewFilteredSource(inner, policy.Default()).GetSession(context.Background(), "s1")
	require.NoError(t, err)

	require.Len(t, stored.Entries[1].ToolContent, 1)
	assert.Equal(t, agent.KindProcessOutput, stored.Entries[1].ToolContent[0].Kind,
		"the source's own session must not be rewritten by a reader's view of it")
	assert.JSONEq(t, string(raw), string(stored.Entries[1].ToolContent[0].Raw),
		"the stored bytes must survive a filtered read intact")
}

func TestFilteredSource_CurrentSessionIsFilteredToo(t *testing.T) {
	inner := &stubSource{sess: sessionWithContent(
		agent.ToolContentBlock{Kind: agent.KindProcessOutput, Raw: json.RawMessage(`{"stdout":"SENTINEL"}`)},
	)}
	got, err := NewFilteredSource(inner, policy.Default()).CurrentSession(context.Background())
	require.NoError(t, err)
	require.Len(t, got.Entries[1].ToolContent, 1)
	assert.Equal(t, agent.KindExcluded, got.Entries[1].ToolContent[0].Kind,
		"CurrentSession must not be an unfiltered back door")
}

func TestFilteredSource_KeepsUnfilteredKinds(t *testing.T) {
	inner := &stubSource{sess: sessionWithContent(
		agent.ToolContentBlock{Kind: agent.KindToolCatalog, Raw: json.RawMessage(`{"t":"SENTINEL-CATALOG"}`)},
		agent.ToolContentBlock{Kind: agent.KindAgentResult, Raw: json.RawMessage(`{"a":"SENTINEL-AGENT"}`)},
	)}
	got, err := NewFilteredSource(inner, policy.Default()).GetSession(context.Background(), "s1")
	require.NoError(t, err)

	blob, err := json.Marshal(got)
	require.NoError(t, err)
	assert.Contains(t, string(blob), "SENTINEL-CATALOG")
	assert.Contains(t, string(blob), "SENTINEL-AGENT")
}
