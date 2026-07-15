package opencode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// This file carries the launch capabilities the embedded agent.LaunchBackend
// requires: commands (a no-op — opencode reads .claude/skills/ natively) and
// the session-history reader.
//
// The session reader is driven entirely by opencode's OWN documented commands
// — `opencode session list --format json` (listing, with a per-session
// `directory` that keys it to a project) and `opencode export <id>` (the full
// transcript) — NOT by hand-parsing the private on-disk store under
// ~/.local/share/opencode/. That storage layout is undocumented and moved to a
// SQLite blob (opencode.db) in 1.18.x; the kiro reader was CONFIRMED BROKEN by
// exactly that kind of guess-the-private-layout coupling, so this reader refuses
// to depend on it. Verified against opencode 1.18.1.

// opencodeCommands is a no-op ContentCommands. opencode reads .claude/skills/
// natively, so ctxloom writes no skill format of its own here.
type opencodeCommands struct{}

func (s *opencodeCommands) RegisterFromContent(workDir string, cmds []agent.CommandExport) error {
	return nil
}

// opencodeSessionHistory implements agent.SessionHistory over opencode's CLI.
// It shells out to the same `opencode` binary the backend runs, so a configured
// binary_path (this host's opencode is not on PATH) is honored via the backend
// back-reference. run is the exec seam tests inject to replace the real binary
// with captured fixture bytes.
type opencodeSessionHistory struct {
	backend *Opencode
	// run executes `opencode <args...>` and returns stdout. Nil in production
	// (falls back to execCLI); set by WithOpencodeSessionRunner in tests.
	run func(args ...string) ([]byte, error)
}

// OpencodeSessionOption configures opencodeSessionHistory.
type OpencodeSessionOption func(*opencodeSessionHistory)

// WithOpencodeSessionRunner replaces the opencode-binary exec with a fake for
// testing. The fake receives the CLI args (e.g. "session","list","--format",
// "json" or "export",<id>) and returns the stdout the real command would.
func WithOpencodeSessionRunner(run func(args ...string) ([]byte, error)) OpencodeSessionOption {
	return func(h *opencodeSessionHistory) { h.run = run }
}

// newOpencodeSessionHistory builds the reader bound to its backend (for the
// resolved binary path).
func newOpencodeSessionHistory(b *Opencode, opts ...OpencodeSessionOption) *opencodeSessionHistory {
	h := &opencodeSessionHistory{backend: b}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// execCLI runs the configured opencode binary with args in dir (dir=="" inherits
// the caller's cwd) and returns stdout. dir is load-bearing for `session list`:
// opencode scopes the listing to the PROJECT resolved from its cwd (git root, or
// "global"), so the reader must run it in the session's workDir to see that
// project's sessions at all — run elsewhere it silently lists a different
// project. opencode prints diagnostic noise (e.g. "Exporting session: <id>") to
// STDERR, so only stdout is returned; stderr is folded into the error on failure.
func (h *opencodeSessionHistory) execCLI(dir string, args ...string) ([]byte, error) {
	if h.run != nil {
		return h.run(args...)
	}
	bin := "opencode"
	if h.backend != nil && h.backend.BinaryPath != "" {
		bin = h.backend.BinaryPath
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("opencode %v failed: %w: %s", args, err, bytes.TrimSpace(stderr.Bytes()))
	}
	return stdout.Bytes(), nil
}

// opencodeListEntry is one element of `opencode session list --format json`.
// `directory` is the session's cwd — the project key. `created`/`updated` are
// unix milliseconds.
type opencodeListEntry struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Created   int64  `json:"created"`
	Updated   int64  `json:"updated"`
	Directory string `json:"directory"`
	ProjectID string `json:"projectId"`
}

// GetCurrentSession returns the most recent session transcript for workDir.
func (h *opencodeSessionHistory) GetCurrentSession(workDir string) (*agent.Session, error) {
	sessions, err := h.ListSessions(workDir)
	return agent.MostRecentSession(sessions, err, func(m agent.SessionMeta) (*agent.Session, error) {
		return h.GetSession(workDir, m.ID)
	})
}

// ListSessions returns opencode sessions whose `directory` matches workDir,
// most-recent-first (by last-updated). An empty workDir lists every session. A
// project with no sessions returns an empty slice and nil error (honest
// absence); a failed `session list` invocation errors loudly rather than
// masquerading as "no sessions".
func (h *opencodeSessionHistory) ListSessions(workDir string) ([]agent.SessionMeta, error) {
	out, err := h.execCLI(workDir, "session", "list", "--format", "json")
	if err != nil {
		return nil, err
	}
	// A project with zero sessions makes opencode emit EMPTY stdout (not "[]"),
	// exit 0 — that is honest absence, not corruption, so it returns an empty
	// list. Only NON-empty, unparseable output is a loud error (the reader must
	// never silently swallow a real failure).
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return nil, nil
	}
	var entries []opencodeListEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("parse opencode session list: %w", err)
	}

	absWork := ""
	if workDir != "" {
		if absWork, err = filepath.Abs(workDir); err != nil {
			return nil, err
		}
	}

	var sessions []agent.SessionMeta
	for _, e := range entries {
		if absWork != "" && e.Directory != absWork {
			continue
		}
		sessions = append(sessions, agent.SessionMeta{
			ID:        e.ID,
			StartTime: msToTime(e.Created),
			EndTime:   msToTime(e.Updated),
		})
	}
	// Order by last-updated desc: "most recent session for this project" is the
	// one most recently touched (a resumed old session outranks an untouched
	// newer one), so we sort on the updated time, not created.
	sort.SliceStable(sessions, func(i, j int) bool {
		return sessions[i].EndTime.After(sessions[j].EndTime)
	})
	return sessions, nil
}

// GetSession returns a specific session by ID via `opencode export`.
func (h *opencodeSessionHistory) GetSession(workDir, sessionID string) (*agent.Session, error) {
	// `export` is keyed by session id and cwd-independent, so no dir is needed.
	out, err := h.execCLI("", "export", sessionID)
	if err != nil {
		return nil, err
	}
	return parseOpencodeExport(out)
}

// GetSessionByPath is unsupported: opencode does NOT persist a single per-session
// transcript file (its store is a SQLite DB read through `export`), so there is
// no path to parse. Sessions are materialized by ID via GetSession; the /recover
// scrollback path prefers the bound session id and only falls back to a path for
// file-backed backends. Failing loudly here beats returning a silent empty
// transcript.
func (h *opencodeSessionHistory) GetSessionByPath(path string) (*agent.Session, error) {
	return nil, fmt.Errorf("opencode has no file-backed transcript; read sessions by id via GetSession (path %q unsupported)", path)
}

// TranscriptPathFromHook returns empty: opencode has no per-session transcript
// file to bind, so recovery is keyed by session id, never a path.
func (h *opencodeSessionHistory) TranscriptPathFromHook(workDir, sessionID, transcriptPath string) string {
	return ""
}

// opencodeExport is the `opencode export <id>` document.
type opencodeExport struct {
	Info     opencodeExportInfo `json:"info"`
	Messages []opencodeMessage  `json:"messages"`
}

type opencodeExportInfo struct {
	ID        string `json:"id"`
	Directory string `json:"directory"`
	Time      struct {
		Created int64 `json:"created"`
		Updated int64 `json:"updated"`
	} `json:"time"`
}

// opencodeMessage is one message: an info envelope (role + times) and an ordered
// list of typed parts.
type opencodeMessage struct {
	Info struct {
		Role string `json:"role"` // "user" | "assistant"
		Time struct {
			Created int64 `json:"created"`
		} `json:"time"`
	} `json:"info"`
	Parts []opencodePart `json:"parts"`
}

// opencodePart is one part of a message. type is "text", "reasoning", "tool",
// "step-start", or "step-finish". Text carries prose; a tool part folds the
// call AND its result into one `state`.
type opencodePart struct {
	Type  string             `json:"type"`
	Text  string             `json:"text"`
	Tool  string             `json:"tool"`  // tool name (type == "tool")
	State *opencodeToolState `json:"state"` // tool call+result (type == "tool")
}

type opencodeToolState struct {
	Status string          `json:"status"` // "completed" | "error" | ...
	Input  json.RawMessage `json:"input"`  // tool arguments (JSON object)
	Output string          `json:"output"` // tool result text (completed)
	Error  string          `json:"error"`  // failure text (error)
}

// parseOpencodeExport converts an export document into the normalized Session.
// Unknown part types (step-start/step-finish) contribute nothing.
func parseOpencodeExport(data []byte) (*agent.Session, error) {
	var exp opencodeExport
	if err := json.Unmarshal(data, &exp); err != nil {
		return nil, fmt.Errorf("parse opencode export: %w", err)
	}
	session := &agent.Session{
		ID:        exp.Info.ID,
		StartTime: msToTime(exp.Info.Time.Created),
		EndTime:   msToTime(exp.Info.Time.Updated),
		Entries:   []agent.SessionEntry{},
	}
	for _, msg := range exp.Messages {
		ts := msToTime(msg.Info.Time.Created)
		for _, part := range msg.Parts {
			session.Entries = append(session.Entries, opencodePartEntries(msg.Info.Role, part, ts)...)
		}
	}
	return session, nil
}

// opencodePartEntries maps one part to zero or more normalized entries.
func opencodePartEntries(role string, part opencodePart, ts time.Time) []agent.SessionEntry {
	switch part.Type {
	case "text":
		if part.Text == "" {
			return nil
		}
		typ := agent.EntryTypeAssistant
		if role == "user" {
			typ = agent.EntryTypeUser
		}
		return []agent.SessionEntry{{Type: typ, Content: part.Text, Timestamp: ts}}
	case "reasoning":
		if part.Text == "" {
			return nil
		}
		return []agent.SessionEntry{{Type: agent.EntryTypeThinking, Content: part.Text, Timestamp: ts}}
	case "tool":
		return opencodeToolEntries(part, ts)
	default:
		return nil // step-start / step-finish / unknown
	}
}

// opencodeToolEntries expands a tool part into a tool_use entry (name + input)
// followed by its tool_result (output or error). opencode records both sides in
// one part's state, so both entries share the part's timestamp.
func opencodeToolEntries(part opencodePart, ts time.Time) []agent.SessionEntry {
	entries := []agent.SessionEntry{{
		Type:      agent.EntryTypeToolUse,
		ToolName:  part.Tool,
		Timestamp: ts,
	}}
	var output string
	isError := false
	if part.State != nil {
		entries[0].ToolInput = part.State.Input
		output = part.State.Output
		isError = part.State.Status == "error"
		if output == "" && part.State.Error != "" {
			output = part.State.Error
		}
	}
	entries = append(entries, agent.SessionEntry{
		Type:       agent.EntryTypeToolResult,
		ToolName:   part.Tool,
		ToolOutput: output,
		IsError:    isError,
		Timestamp:  ts,
	})
	return entries
}

// msToTime converts unix milliseconds to time.Time; 0 stays the zero time.
func msToTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
