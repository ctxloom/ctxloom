//go:build parked_engines

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

// This file carries the session-history reader the embedded
// agent.LaunchBackend requires.
//
// The session reader is driven entirely by opencode's OWN documented commands
// — `opencode session list --format json` (listing, with a per-session
// `directory` that keys it to a project) and `opencode export <id>` (the full
// transcript) — NOT by hand-parsing the private on-disk store under
// ~/.local/share/opencode/. That storage layout is undocumented and moved to a
// SQLite blob (opencode.db) in 1.18.x; the kiro reader was CONFIRMED BROKEN by
// exactly that kind of guess-the-private-layout coupling, so this reader refuses
// to depend on it. Verified against opencode 1.18.1.

// opencodeSessionHistory implements agent.SessionHistory over opencode's CLI.
// It shells out to the same `opencode` binary the backend runs, so a configured
// binary_path (this host's opencode is not on PATH) is honored via the backend
// back-reference. run is the exec seam tests inject to replace the real binary
// with captured fixture bytes.
type opencodeSessionHistory struct {
	backend *Opencode
	// run executes `opencode <args...>` in dir and returns stdout. Nil in
	// production (falls back to execCLI); set by WithOpencodeSessionRunner in
	// tests. dir is threaded through so a test can assert the must-run-in-workDir
	// invariant execCLI's own doc comment calls load-bearing, instead of that
	// invariant being structurally untestable.
	run func(dir string, args ...string) ([]byte, error)
}

// OpencodeSessionOption configures opencodeSessionHistory.
type OpencodeSessionOption func(*opencodeSessionHistory)

// WithOpencodeSessionRunner replaces the opencode-binary exec with a fake for
// testing. The fake receives the working directory execCLI was called with,
// plus the CLI args (e.g. "session","list","--format","json" or
// "export",<id>), and returns the stdout the real command would.
func WithOpencodeSessionRunner(run func(dir string, args ...string) ([]byte, error)) OpencodeSessionOption {
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
		return h.run(dir, args...)
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
	// projectId is declared, never read by anything (including tests), and is
	// intentionally not modeled — json.Unmarshal ignores it.
}

// GetCurrentSession returns the most recent session transcript for workDir.
func (h *opencodeSessionHistory) GetCurrentSession(workDir string) (*agent.Session, error) {
	return agent.GetCurrentSessionViaGetSession(workDir, h.ListSessions, h.GetSession)
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
		// Resolve symlinks before comparing. filepath.Abs only
		// cleans the path, it does not resolve symlinks, so a symlinked
		// workDir (or a symlinked ancestor of it) never string-equalled
		// opencode's own (already-resolved) `directory` value and yielded
		// zero sessions with a nil error — indistinguishable from genuine
		// absence. resolveSymlinksBestEffort falls back to the unresolved
		// path when EvalSymlinks fails (e.g. a path that no longer exists),
		// preserving the exact-match behavior for that case.
		absWork = resolveSymlinksBestEffort(absWork)
	}

	var sessions []agent.SessionMeta
	for _, e := range entries {
		if absWork != "" && resolveSymlinksBestEffort(e.Directory) != absWork {
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
	return parseOpencodeExport(out, sessionID)
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
//
// wantID is the session the caller asked for. It is checked because json
// silently ignores unknown fields: a drifted export — a renamed top-level key, a
// re-nested messages array, a new part vocabulary — unmarshals CLEANLY into a
// zero-valued document, and this used to return `Entries: []` with a nil error,
// so /recover rendered an empty scrollback and reported success. Three
// shape-drift signatures are therefore hard failures:
//
//   - no info.id at all: this is not an export document
//   - an info.id that is not the one we asked for: this is the wrong document
//   - messages present but not one of them yielded an entry: the part
//     vocabulary drifted out from under opencodePartEntries
//
// A genuinely empty session (info.id present, zero messages) is NOT one of them
// and still parses: nothing was recorded, which is a real and reportable answer.
func parseOpencodeExport(data []byte, wantID string) (*agent.Session, error) {
	var exp opencodeExport
	if err := json.Unmarshal(data, &exp); err != nil {
		return nil, fmt.Errorf("parse opencode export: %w", err)
	}
	if exp.Info.ID == "" {
		return nil, fmt.Errorf("parse opencode export for %q: the document carries no info.id — `opencode export` output has drifted from the shape this reader knows; the transcript is NOT empty, it is unreadable", wantID)
	}
	if wantID != "" && exp.Info.ID != wantID {
		return nil, fmt.Errorf("parse opencode export: asked for session %q but the export carries %q", wantID, exp.Info.ID)
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
	if len(exp.Messages) > 0 && len(session.Entries) == 0 {
		return nil, fmt.Errorf("parse opencode export for %q: %d message(s) yielded no entries — every part type is unrecognised, so `opencode export`'s part vocabulary has drifted from this reader", exp.Info.ID, len(exp.Messages))
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

// resolveSymlinksBestEffort resolves p's symlinks for a robust directory
// comparison; if p cannot be resolved (does not exist, permission denied), it
// returns p unchanged rather than erroring — the comparison degrades to the
// old exact-match behavior for that one entry instead of failing the whole
// listing.
func resolveSymlinksBestEffort(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

// msToTime converts unix milliseconds to time.Time; 0 stays the zero time.
func msToTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
