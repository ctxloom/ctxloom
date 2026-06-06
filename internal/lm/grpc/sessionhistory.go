package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ctxloom/ctxloom/internal/agent"
	"github.com/ctxloom/ctxloom/internal/projectroot"
)

// This file carries the session-history transport: the backend (plugin) is
// authoritative for locating, reassembling, and translating its own native
// transcripts, and serves the normalized result over GetSession/ListSessions.
// ctxloom (and any other consumer — including a different agent during a
// cross-agent handoff) retrieves transcripts through here rather than parsing a
// backend's files directly. Converters mirror agent.Session/SessionEntry/
// SessionMeta; times cross as unix seconds (proto has no time.Time).

func timeToUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func unixToTime(u int64) time.Time {
	if u == 0 {
		return time.Time{}
	}
	return time.Unix(u, 0).UTC()
}

func sessionToProto(s *agent.Session) *SessionData {
	if s == nil {
		return nil
	}
	out := &SessionData{
		Id:        s.ID,
		StartUnix: timeToUnix(s.StartTime),
		EndUnix:   timeToUnix(s.EndTime),
		Entries:   make([]*SessionEntry, 0, len(s.Entries)),
	}
	for _, e := range s.Entries {
		out.Entries = append(out.Entries, &SessionEntry{
			Type:          string(e.Type),
			Content:       e.Content,
			ToolName:      e.ToolName,
			ToolInput:     e.ToolInput,
			ToolOutput:    e.ToolOutput,
			IsError:       e.IsError,
			TimestampUnix: timeToUnix(e.Timestamp),
		})
	}
	return out
}

func sessionFromProto(p *SessionData) *agent.Session {
	if p == nil {
		return nil
	}
	s := &agent.Session{
		ID:        p.GetId(),
		StartTime: unixToTime(p.GetStartUnix()),
		EndTime:   unixToTime(p.GetEndUnix()),
		Entries:   make([]agent.SessionEntry, 0, len(p.GetEntries())),
	}
	for _, e := range p.GetEntries() {
		var ti json.RawMessage
		if in := e.GetToolInput(); len(in) > 0 {
			ti = json.RawMessage(in)
		}
		s.Entries = append(s.Entries, agent.SessionEntry{
			Timestamp:  unixToTime(e.GetTimestampUnix()),
			Type:       agent.SessionEntryType(e.GetType()),
			Content:    e.GetContent(),
			ToolName:   e.GetToolName(),
			ToolInput:  ti,
			ToolOutput: e.GetToolOutput(),
			IsError:    e.GetIsError(),
		})
	}
	return s
}

func sessionMetaToProto(m agent.SessionMeta) *SessionMeta {
	return &SessionMeta{
		Id:         m.ID,
		StartUnix:  timeToUnix(m.StartTime),
		EndUnix:    timeToUnix(m.EndTime),
		EntryCount: int32(m.EntryCount),
		Path:       m.Path,
	}
}

func sessionMetaFromProto(p *SessionMeta) agent.SessionMeta {
	if p == nil {
		return agent.SessionMeta{}
	}
	return agent.SessionMeta{
		ID:         p.GetId(),
		StartTime:  unixToTime(p.GetStartUnix()),
		EndTime:    unixToTime(p.GetEndUnix()),
		EntryCount: int(p.GetEntryCount()),
		Path:       p.GetPath(),
	}
}

// --- server (plugin) handlers ---

// GetSession resolves a session id via the backend's own SessionHistory — which
// derives the agent-specific transcript path — and returns the normalized
// session. The agent server is self-situated: it resolves its own workspace
// (projectroot.WorkDir: CTXLOOM_ROOT → git root → cwd) rather than taking a host
// path over the wire, so the contract holds for a remote agent.
func (s *GRPCServer) GetSession(ctx context.Context, req *GetSessionRequest) (*SessionData, error) {
	hist := s.Impl.History()
	if hist == nil {
		return nil, fmt.Errorf("backend %s has no session history", s.Impl.Name())
	}
	sess, err := hist.GetSession(projectroot.WorkDir(), req.GetSessionId())
	if err != nil {
		return nil, err
	}
	return sessionToProto(sess), nil
}

// ListSessions returns the backend's raw transcript-store metadata for its own
// (self-resolved) workspace.
func (s *GRPCServer) ListSessions(ctx context.Context, req *ListSessionsRequest) (*SessionList, error) {
	hist := s.Impl.History()
	if hist == nil {
		return nil, fmt.Errorf("backend %s has no session history", s.Impl.Name())
	}
	metas, err := hist.ListSessions(projectroot.WorkDir())
	if err != nil {
		return nil, err
	}
	out := &SessionList{Sessions: make([]*SessionMeta, 0, len(metas))}
	for _, m := range metas {
		out.Sessions = append(out.Sessions, sessionMetaToProto(m))
	}
	return out, nil
}

// --- client (host) methods ---

// GetSession asks the plugin to materialize the transcript for sessionID and
// returns the normalized session. No workspace is passed — the agent is
// self-situated.
func (c *GRPCClient) GetSession(ctx context.Context, sessionID string) (*agent.Session, error) {
	resp, err := c.client.GetSession(ctx, &GetSessionRequest{SessionId: sessionID})
	if err != nil {
		return nil, err
	}
	return sessionFromProto(resp), nil
}

// ListSessions returns the plugin's transcript-store metadata for its own
// workspace.
func (c *GRPCClient) ListSessions(ctx context.Context) ([]agent.SessionMeta, error) {
	resp, err := c.client.ListSessions(ctx, &ListSessionsRequest{})
	if err != nil {
		return nil, err
	}
	out := make([]agent.SessionMeta, 0, len(resp.GetSessions()))
	for _, m := range resp.GetSessions() {
		out = append(out, sessionMetaFromProto(m))
	}
	return out, nil
}

// GetSession delegates to the underlying gRPC client.
func (p *LLMRunner) GetSession(ctx context.Context, sessionID string) (*agent.Session, error) {
	return p.grpc.GetSession(ctx, sessionID)
}

// ListSessions delegates to the underlying gRPC client.
func (p *LLMRunner) ListSessions(ctx context.Context) ([]agent.SessionMeta, error) {
	return p.grpc.ListSessions(ctx)
}
