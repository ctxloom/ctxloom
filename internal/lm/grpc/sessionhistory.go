package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
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

// int32Clamped narrows a Go int onto one of this wire's int32 fields without
// wrapping. Every such field is a count, an index, a line number or a timeout,
// and each is read downstream as a non-negative quantity; a bare int32(v) of an
// out-of-range value silently WRAPS, turning a large value into a large
// negative one. Saturating keeps it monotone and in range instead.
func int32Clamped(v int) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		return math.MinInt32
	}
	return int32(v)
}

// EntryToProto converts one normalized turn to its proto form. Shared by
// sessionToProto (whole-transcript reassembly), WatchSession (per-entry
// streaming), and the operations feed resolver (live-tap normalization) so
// all encode entries identically.
func EntryToProto(e agent.SessionEntry) *SessionEntry {
	return &SessionEntry{
		Type:          string(e.Type),
		Content:       e.Content,
		ToolName:      e.ToolName,
		ToolInput:     e.ToolInput,
		ToolOutput:    e.ToolOutput,
		IsError:       e.IsError,
		TimestampUnix: timeToUnix(e.Timestamp),
		Sidechain:     e.Sidechain,
		ToolCallId:    e.ToolCallID,
		ToolKind:      e.ToolKind,
		ToolLocations: locationsToProto(e.ToolLocations),
		ToolContent:   toolContentToProto(e.ToolContent),
		ContentBlocks: contentBlocksToProto(e.ContentBlocks),
		SystemKind:    string(e.SystemKind),
		Plan:          planEntriesToProto(e.Plan),
	}
}

func locationsToProto(locs []agent.ToolLocation) []*ToolLocation {
	if len(locs) == 0 {
		return nil
	}
	out := make([]*ToolLocation, 0, len(locs))
	for _, l := range locs {
		out = append(out, &ToolLocation{Path: l.Path, Line: int32Clamped(l.Line)})
	}
	return out
}

func locationsFromProto(locs []*ToolLocation) []agent.ToolLocation {
	if len(locs) == 0 {
		return nil
	}
	out := make([]agent.ToolLocation, 0, len(locs))
	for _, l := range locs {
		out = append(out, agent.ToolLocation{Path: l.GetPath(), Line: int(l.GetLine())})
	}
	return out
}

func contentBlocksToProto(blocks []agent.ContentBlock) []*ContentBlock {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]*ContentBlock, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, &ContentBlock{Kind: b.Kind, Text: b.Text, Raw: b.Raw})
	}
	return out
}

func contentBlocksFromProto(blocks []*ContentBlock) []agent.ContentBlock {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]agent.ContentBlock, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, agent.ContentBlock{Kind: b.GetKind(), Text: b.GetText(), Raw: b.GetRaw()})
	}
	return out
}

func toolContentToProto(content []agent.ToolContentBlock) []*ToolContentBlock {
	if len(content) == 0 {
		return nil
	}
	out := make([]*ToolContentBlock, 0, len(content))
	for _, c := range content {
		out = append(out, &ToolContentBlock{
			Kind: c.Kind, Text: c.Text,
			DiffPath: c.DiffPath, DiffOldText: c.DiffOldText, DiffNewText: c.DiffNewText,
			TerminalId: c.TerminalID, Raw: c.Raw,
		})
	}
	return out
}

func toolContentFromProto(content []*ToolContentBlock) []agent.ToolContentBlock {
	if len(content) == 0 {
		return nil
	}
	out := make([]agent.ToolContentBlock, 0, len(content))
	for _, c := range content {
		out = append(out, agent.ToolContentBlock{
			Kind: c.GetKind(), Text: c.GetText(),
			DiffPath: c.GetDiffPath(), DiffOldText: c.GetDiffOldText(), DiffNewText: c.GetDiffNewText(),
			TerminalID: c.GetTerminalId(), Raw: c.GetRaw(),
		})
	}
	return out
}

func planEntriesToProto(entries []agent.PlanEntry) []*PlanEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]*PlanEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, &PlanEntry{Content: e.Content, Priority: e.Priority, Status: e.Status})
	}
	return out
}

func planEntriesFromProto(entries []*PlanEntry) []agent.PlanEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]agent.PlanEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, agent.PlanEntry{Content: e.GetContent(), Priority: e.GetPriority(), Status: e.GetStatus()})
	}
	return out
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
		out.Entries = append(out.Entries, EntryToProto(e))
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
		s.Entries = append(s.Entries, entryFromProto(e))
	}
	return s
}

// entryFromProto converts one proto SessionEntry to its normalized form. Shared
// by sessionFromProto and the Chat client (chat.go) so both decode identically.
func entryFromProto(e *SessionEntry) agent.SessionEntry {
	var ti json.RawMessage
	if in := e.GetToolInput(); len(in) > 0 {
		ti = json.RawMessage(in)
	}
	return agent.SessionEntry{
		Timestamp:     unixToTime(e.GetTimestampUnix()),
		Type:          agent.SessionEntryType(e.GetType()),
		Content:       e.GetContent(),
		ToolName:      e.GetToolName(),
		ToolInput:     ti,
		ToolOutput:    e.GetToolOutput(),
		IsError:       e.GetIsError(),
		Sidechain:     e.GetSidechain(),
		ToolCallID:    e.GetToolCallId(),
		ToolKind:      e.GetToolKind(),
		ToolLocations: locationsFromProto(e.GetToolLocations()),
		ToolContent:   toolContentFromProto(e.GetToolContent()),
		ContentBlocks: contentBlocksFromProto(e.GetContentBlocks()),
		SystemKind:    agent.SessionSystemKind(e.GetSystemKind()),
		Plan:          planEntriesFromProto(e.GetPlan()),
	}
}

func sessionMetaToProto(m agent.SessionMeta) *SessionMeta {
	return &SessionMeta{
		Id:         m.ID,
		StartUnix:  timeToUnix(m.StartTime),
		EndUnix:    timeToUnix(m.EndTime),
		EntryCount: int32Clamped(m.EntryCount),
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
	// "No such session" (a nil session with no error) cannot cross this wire as
	// itself: a nil message is serialized as an EMPTY one, so the host would
	// decode a non-nil, zero-entry agent.Session — indistinguishable from a
	// session that genuinely exists and has produced no turns yet. Report the
	// absence instead, so the two answers stay apart.
	if sess == nil {
		return nil, fmt.Errorf("backend %s has no session %q", s.Impl.Name(), req.GetSessionId())
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

// GetSession and ListSessions are promoted from LLMRunner's embedded
// *GRPCClient (U059-F08) — no forwarder needed.
