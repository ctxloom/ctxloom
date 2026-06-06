package grpc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeHistory is a minimal agent.SessionHistory for handler tests: only
// GetSession/ListSessions carry behavior; the rest are unused stubs.
type fakeHistory struct {
	session    *agent.Session
	sessionErr error
	metas      []agent.SessionMeta
	metasErr   error
	gotWorkDir string
	gotID      string
}

func (h *fakeHistory) GetSession(workDir, id string) (*agent.Session, error) {
	h.gotWorkDir, h.gotID = workDir, id
	return h.session, h.sessionErr
}
func (h *fakeHistory) ListSessions(workDir string) ([]agent.SessionMeta, error) {
	h.gotWorkDir = workDir
	return h.metas, h.metasErr
}
func (h *fakeHistory) GetCurrentSession(string) (*agent.Session, error) { return nil, nil }
func (h *fakeHistory) GetSessionByPath(string) (*agent.Session, error)  { return nil, nil }
func (h *fakeHistory) TranscriptPathFromHook(_, _, _ string) string     { return "" }

func TestSessionRoundTrip(t *testing.T) {
	ts := time.Unix(1_700_000_000, 0).UTC()
	in := &agent.Session{
		ID:        "sess-1",
		StartTime: ts,
		EndTime:   ts.Add(time.Hour),
		Entries: []agent.SessionEntry{
			{Timestamp: ts, Type: agent.EntryTypeUser, Content: "hi"},
			{Timestamp: ts, Type: agent.EntryTypeToolUse, ToolName: "Bash", ToolInput: json.RawMessage(`{"cmd":"ls"}`)},
			{Timestamp: ts, Type: agent.EntryTypeToolResult, ToolOutput: "files", IsError: true},
		},
	}

	got := sessionFromProto(sessionToProto(in))
	require.NotNil(t, got)
	assert.Equal(t, in.ID, got.ID)
	assert.Equal(t, in.StartTime, got.StartTime)
	assert.Equal(t, in.EndTime, got.EndTime)
	require.Len(t, got.Entries, 3)
	assert.Equal(t, agent.EntryTypeUser, got.Entries[0].Type)
	assert.Equal(t, "hi", got.Entries[0].Content)
	assert.Equal(t, "Bash", got.Entries[1].ToolName)
	assert.JSONEq(t, `{"cmd":"ls"}`, string(got.Entries[1].ToolInput))
	assert.True(t, got.Entries[2].IsError)
	assert.Equal(t, "files", got.Entries[2].ToolOutput)
}

func TestSessionRoundTrip_ZeroTimesAndNil(t *testing.T) {
	assert.Nil(t, sessionToProto(nil))
	assert.Nil(t, sessionFromProto(nil))

	got := sessionFromProto(sessionToProto(&agent.Session{ID: "x"}))
	assert.True(t, got.StartTime.IsZero(), "zero time must round-trip as zero, not a 1970/epoch artifact")
	assert.True(t, got.EndTime.IsZero())
	assert.Empty(t, got.Entries)
}

func TestSessionMetaRoundTrip(t *testing.T) {
	ts := time.Unix(1_700_000_123, 0).UTC()
	in := agent.SessionMeta{ID: "m1", StartTime: ts, EndTime: ts, EntryCount: 7, Path: "/t/m1.jsonl"}
	got := sessionMetaFromProto(sessionMetaToProto(in))
	assert.Equal(t, in, got)
}

func TestGRPCServer_GetSession_DelegatesToHistory(t *testing.T) {
	hist := &fakeHistory{session: &agent.Session{ID: "sess-9", Entries: []agent.SessionEntry{{Type: agent.EntryTypeUser, Content: "x"}}}}
	srv := &GRPCServer{Impl: &fakeBackend{name: "gemini", history: hist}}

	out, err := srv.GetSession(context.Background(), &GetSessionRequest{SessionId: "sess-9"})
	require.NoError(t, err)
	assert.Equal(t, "sess-9", out.GetId())
	assert.Equal(t, "sess-9", hist.gotID, "the agent locates by id; no path or workdir crosses the wire")
	// The server self-resolves its workspace (no work_dir from the client).
	assert.NotEmpty(t, hist.gotWorkDir, "server must supply a self-resolved workspace to SessionHistory")
}

func TestGRPCServer_GetSession_NoHistory(t *testing.T) {
	srv := &GRPCServer{Impl: &fakeBackend{name: "codex"}} // History() == nil
	_, err := srv.GetSession(context.Background(), &GetSessionRequest{SessionId: "x"})
	require.Error(t, err)
}

func TestGRPCServer_ListSessions_DelegatesToHistory(t *testing.T) {
	hist := &fakeHistory{metas: []agent.SessionMeta{{ID: "a"}, {ID: "b"}}}
	srv := &GRPCServer{Impl: &fakeBackend{name: "gemini", history: hist}}

	out, err := srv.ListSessions(context.Background(), &ListSessionsRequest{})
	require.NoError(t, err)
	require.Len(t, out.GetSessions(), 2)
	assert.Equal(t, "a", out.GetSessions()[0].GetId())
}

func TestGRPCClient_GetSession_Delegates(t *testing.T) {
	fake := &fakeLLMClient{sessionResp: sessionToProto(&agent.Session{ID: "sess-7"})}
	c := &GRPCClient{client: fake}
	got, err := c.GetSession(context.Background(), "sess-7")
	require.NoError(t, err)
	assert.Equal(t, "sess-7", got.ID)
}

func TestGRPCClient_ListSessions_Delegates(t *testing.T) {
	fake := &fakeLLMClient{listResp: &SessionList{Sessions: []*SessionMeta{{Id: "a"}, {Id: "b"}}}}
	c := &GRPCClient{client: fake}
	got, err := c.ListSessions(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "a", got[0].ID)
}
