package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// runStructuredREPL drives a structured, multi-turn conversation over the
// backend's StructuredChat capability (the Chat RPC → claude-code stream-json) —
// messages in, normalized turn events out, no pty. It composes nothing else: the
// Chat stream is self-contained (no transcript bootstrap), so there is no harp/
// session-id wait. Stdin lines are read as messages (one line = one message);
// the agent's turns render to stdout as text or, with --format json, NDJSON —
// the contract a GUI frontend consumes (stdin = messages, stdout = NDJSON turns).
//
// EOF on stdin (Ctrl-D) closes input so the agent completes its last turn; an
// interrupt cancels the whole exchange.
//
// mcpServers is the managed MCP set composed from the run's ManagedConfig
// (agent.ManagedConfig.ChatMCPServers): the Chat RPC never runs Setup, so the
// servers Setup would write to the engine's settings file ride the session
// instead.
func runStructuredREPL(ctx context.Context, client pb.Client, req *pb.RunStart, mcpServers []agent.ChatMCPServer, format string, stdin io.Reader, stdout io.Writer) error {
	opts := req.GetOptions()
	return runChatSession(ctx, client, agent.ChatRequest{
		WorkDir:     opts.GetWorkDir(),
		Model:       opts.GetModel(),
		Env:         opts.GetEnv(),
		Permissions: agent.WireMode(opts.GetPermissionMode()),
		MCPServers:  mcpServers,
	}, chatTurns{Stdin: stdin}, format, stdout)
}

// chatTurns names where one conversation's turns come from, so a caller says
// it once instead of threading three loose strings through the driver.
//
// Lead is ctxloom's assembled context. It is not a turn: it rides the FIRST
// turn as its lead block — the same delivery `ctxloom acp serve` gives an
// editor's first prompt — so the engine reads the opening question against
// the context it was assembled for. Sending it as a turn of its own would ask
// the engine to answer a pile of fragments.
type chatTurns struct {
	Lead string
	// Opening is a turn the caller already has (a prompt on the command
	// line). Empty means the conversation opens with whatever is read first.
	Opening string
	// Stdin is the turn source read after Opening, one line per turn, until
	// EOF ends the conversation. nil = the Opening turn is the whole
	// conversation.
	Stdin io.Reader
}

// runChatSession opens the engine's structured chat and drives turns through
// it until the conversation ends. It is the shared body of every multi-turn
// CLI surface — `run --structured` and `acp run`'s session form — so the
// half-close/teardown ordering below is reasoned about once.
func runChatSession(ctx context.Context, client pb.Client, req agent.ChatRequest, turns chatTurns, format string, stdout io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	in, events, errs, err := client.Chat(ctx, req)
	if err != nil {
		return fmt.Errorf("start chat: %w", err)
	}

	// Render turn events until the stream closes.
	renderDone := make(chan struct{})
	go func() {
		defer close(renderDone)
		if rerr := renderChatEvents(stdout, format, events); rerr != nil && ctx.Err() == nil {
			clidiag.Warn("ctxloom", "chat render failed: %v", rerr)
			cancel()
		}
	}()

	// Read stdin lines → messages in its own goroutine, closing `in` to
	// half-close the chat at EOF so the agent finishes its last turn. Reading
	// concurrently (and making the loop ctx-aware) lets a stream that ends
	// before stdin EOF — a backend crash or clean close — unblock us instead of
	// hanging forever on an open stdin pipe with the stream error unread.
	scanDone := make(chan error, 1)
	go func() {
		scanDone <- pumpTurns(ctx, turns, in)
		close(in)
	}()

	var (
		scanErr         error
		reportStreamErr bool
	)
	select {
	case scanErr = <-scanDone:
		// stdin closed first (EOF/read error): wait for the agent's last turn.
		<-renderDone
		reportStreamErr = ctx.Err() == nil
	case <-renderDone:
		// The stream ended first (clean close or backend crash). Don't wait on
		// stdin — a blocking read on an open pipe can't be interrupted — so
		// cancel (which also unblocks any pending message send) and move on. A
		// genuine stream end here (we hadn't already cancelled) should surface.
		reportStreamErr = ctx.Err() == nil
		cancel()
	}

	// errs carries the stream's terminal error (buffered, then closed). Report a
	// real stream failure, but stay quiet when we (render error) or an interrupt
	// cancelled — there the error is just the teardown we asked for.
	if e := <-errs; e != nil && reportStreamErr {
		clidiag.Warn("ctxloom", "chat stream ended: %v", e)
		if scanErr == nil {
			scanErr = e
		}
	}
	return scanErr
}

// renderChatEvents renders the chat turn stream. json mode emits NDJSON (one
// event per line — the structured-frontend contract); text mode pretty-prints
// entries, a status line per completion (context gauge + cost + timing), and a
// one-line session header.
func renderChatEvents(out io.Writer, format string, events <-chan agent.ChatEvent) error {
	switch format {
	case formatJSON:
		enc := json.NewEncoder(out)
		for ev := range events {
			if err := enc.Encode(chatEventToJSON(ev)); err != nil {
				return err
			}
		}
		return nil
	case "", formatText:
		w := iox.NewErrWriter(out)
		for ev := range events {
			renderChatEventText(w, ev)
		}
		return w.Err()
	default:
		return unknownFormatError(format)
	}
}

func renderChatEventText(w *iox.ErrWriter, ev agent.ChatEvent) {
	switch {
	case ev.Entry != nil:
		e := ev.Entry
		switch e.Type {
		case agent.EntryTypeToolUse:
			w.Printf("  → %s\n", e.ToolName)
		case agent.EntryTypeToolResult:
			marker := "✓"
			if e.IsError {
				marker = "✗"
			}
			w.Printf("  %s %s\n", marker, e.ToolName)
		default:
			w.Printf("%s: %s\n", e.Type, e.Content)
		}
	case ev.Complete != nil:
		c := ev.Complete
		w.Printf("── context %s/%s · $%.4f · %dms\n",
			humanCount(c.InputTokens), humanCount(c.ContextWindow), c.CostUSD, c.DurationMs)
	case ev.Session != nil:
		s := ev.Session
		var mcp []string
		for _, m := range s.MCPServers {
			mcp = append(mcp, fmt.Sprintf("%s(%s)", m.Name, m.Status))
		}
		line := s.Model
		if len(mcp) > 0 {
			line += " · mcp: " + strings.Join(mcp, ", ")
		}
		w.Printf("[%s]\n", line)
	}
}

// rfc3339OrEmpty formats a turn's timestamp for the NDJSON wire, returning ""
// for the zero time so the field is omitted (the frontend then falls back to its
// own clock). The backend stamps chat entries at receipt; transcript entries
// carry their own time.
func rfc3339OrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// humanCount renders a token count compactly (e.g. 23933 → "23.9k", 1000000 → "1.0M").
func humanCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// --- NDJSON output DTOs (the structured-frontend contract; camelCase fields) ---

type chatEntryJSON struct {
	Type       string          `json:"type"`
	Timestamp  string          `json:"timestamp,omitempty"` // RFC3339; the frontend's protocol resolves it (or now())
	Content    string          `json:"content,omitempty"`
	ToolName   string          `json:"toolName,omitempty"`
	ToolInput  json.RawMessage `json:"toolInput,omitempty"`
	ToolOutput string          `json:"toolOutput,omitempty"`
	IsError    bool            `json:"isError,omitempty"`
	// Sidechain marks an entry belonging to an engine's own in-harness
	// subagent rather than the session's main thread (agent.SessionEntry.
	// Sidechain). A frontend that does not attribute subagent-interior events
	// renders a subagent's whole conversation as if the user had had it.
	Sidechain bool `json:"sidechain,omitempty"`

	// --- IR2 (2026-07) richness. Mirrors agent.SessionEntry field for field;
	// all optional, a zero value meaning "the producing backend had none".
	// These were silently absent from this channel until the mirror-parity
	// gate (internal/cli/arch_test.go) was pointed at it.
	ToolCallID    string                 `json:"toolCallId,omitempty"`
	ToolKind      string                 `json:"toolKind,omitempty"`
	ToolLocations []chatToolLocationJSON `json:"toolLocations,omitempty"`
	ToolContent   []chatToolContentJSON  `json:"toolContent,omitempty"`
	ContentBlocks []chatContentBlockJSON `json:"contentBlocks,omitempty"`
	SystemKind    string                 `json:"systemKind,omitempty"`
	Plan          []chatPlanEntryJSON    `json:"plan,omitempty"`
}

// chatToolLocationJSON mirrors agent.ToolLocation.
type chatToolLocationJSON struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
}

// chatContentBlockJSON mirrors agent.ContentBlock.
type chatContentBlockJSON struct {
	Kind string          `json:"kind"`
	Text string          `json:"text,omitempty"`
	Raw  json.RawMessage `json:"raw,omitempty"`
}

// chatToolContentJSON mirrors agent.ToolContentBlock.
type chatToolContentJSON struct {
	Kind        string          `json:"kind"`
	Text        string          `json:"text,omitempty"`
	DiffPath    string          `json:"diffPath,omitempty"`
	DiffOldText string          `json:"diffOldText,omitempty"`
	DiffNewText string          `json:"diffNewText,omitempty"`
	TerminalID  string          `json:"terminalId,omitempty"`
	Raw         json.RawMessage `json:"raw,omitempty"`
}

// chatPlanEntryJSON mirrors agent.PlanEntry.
type chatPlanEntryJSON struct {
	Content  string `json:"content"`
	Priority string `json:"priority,omitempty"`
	Status   string `json:"status,omitempty"`
}

type chatCompleteJSON struct {
	InputTokens         int     `json:"inputTokens"`
	OutputTokens        int     `json:"outputTokens"`
	CacheReadTokens     int     `json:"cacheReadTokens"`
	CacheCreationTokens int     `json:"cacheCreationTokens"`
	ContextWindow       int     `json:"contextWindow"`
	MaxOutputTokens     int     `json:"maxOutputTokens"`
	CostUSD             float64 `json:"costUsd"`
	Model               string  `json:"model,omitempty"`
	StopReason          string  `json:"stopReason,omitempty"`
	DurationMs          int     `json:"durationMs"`
	NumTurns            int     `json:"numTurns"`
}

type chatMCPJSON struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type chatSessionJSON struct {
	// SessionID is the harness-NATIVE session id (agent.ChatSessionInfo.
	// SessionID) — the resume handle. Without it a frontend cannot offer
	// "continue this conversation" at all.
	SessionID string `json:"sessionId,omitempty"`
	// Resumable is the LIVE half of the resume gate: the connected adapter's
	// own handshake advertising session/load (agent.ChatSessionInfo.
	// Resumable), as distinct from the static per-backend capability table.
	Resumable      bool          `json:"resumable,omitempty"`
	Model          string        `json:"model,omitempty"`
	PermissionMode string        `json:"permissionMode,omitempty"`
	ContextWindow  int           `json:"contextWindow,omitempty"`
	MCPServers     []chatMCPJSON `json:"mcpServers,omitempty"`
}

// chatEventType* are the NDJSON contract's discriminator values. The frontend
// switches on this field, so they are a published vocabulary — named here so
// every producer of the feed is linked to it by the compiler rather than by
// two files happening to spell the same word.
const (
	chatEventTypeEntry    = "entry"
	chatEventTypeComplete = "complete"
	chatEventTypeSession  = "session"
)

type chatEventJSON struct {
	Type     string            `json:"type"` // chatEventType*
	Entry    *chatEntryJSON    `json:"entry,omitempty"`
	Complete *chatCompleteJSON `json:"complete,omitempty"`
	Session  *chatSessionJSON  `json:"session,omitempty"`
}

func chatEventToJSON(ev agent.ChatEvent) chatEventJSON {
	switch {
	case ev.Entry != nil:
		e := ev.Entry
		return chatEventJSON{Type: chatEventTypeEntry, Entry: &chatEntryJSON{
			Type:          string(e.Type),
			Timestamp:     rfc3339OrEmpty(e.Timestamp),
			Content:       e.Content,
			ToolName:      e.ToolName,
			ToolInput:     e.ToolInput,
			ToolOutput:    e.ToolOutput,
			IsError:       e.IsError,
			Sidechain:     e.Sidechain,
			ToolCallID:    e.ToolCallID,
			ToolKind:      e.ToolKind,
			ToolLocations: chatToolLocationsJSON(e.ToolLocations),
			ToolContent:   chatToolContentsJSON(e.ToolContent),
			ContentBlocks: chatContentBlocksJSON(e.ContentBlocks),
			SystemKind:    string(e.SystemKind),
			Plan:          chatPlanEntriesJSON(e.Plan),
		}}
	case ev.Complete != nil:
		c := ev.Complete
		return chatEventJSON{Type: chatEventTypeComplete, Complete: &chatCompleteJSON{
			InputTokens:         c.InputTokens,
			OutputTokens:        c.OutputTokens,
			CacheReadTokens:     c.CacheReadTokens,
			CacheCreationTokens: c.CacheCreationTokens,
			ContextWindow:       c.ContextWindow,
			MaxOutputTokens:     c.MaxOutputTokens,
			CostUSD:             c.CostUSD,
			Model:               c.Model,
			StopReason:          c.StopReason,
			DurationMs:          c.DurationMs,
			NumTurns:            c.NumTurns,
		}}
	case ev.Session != nil:
		s := ev.Session
		out := &chatSessionJSON{
			SessionID:      s.SessionID,
			Resumable:      s.Resumable,
			Model:          s.Model,
			PermissionMode: s.PermissionMode,
			ContextWindow:  s.ContextWindow,
		}
		for _, m := range s.MCPServers {
			out.MCPServers = append(out.MCPServers, chatMCPJSON{Name: m.Name, Status: m.Status})
		}
		return chatEventJSON{Type: chatEventTypeSession, Session: out}
	default:
		return chatEventJSON{}
	}
}

func chatToolLocationsJSON(locs []agent.ToolLocation) []chatToolLocationJSON {
	if len(locs) == 0 {
		return nil
	}
	out := make([]chatToolLocationJSON, 0, len(locs))
	for _, l := range locs {
		out = append(out, chatToolLocationJSON{Path: l.Path, Line: l.Line})
	}
	return out
}

func chatToolContentsJSON(blocks []agent.ToolContentBlock) []chatToolContentJSON {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]chatToolContentJSON, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, chatToolContentJSON{
			Kind:        b.Kind,
			Text:        b.Text,
			DiffPath:    b.DiffPath,
			DiffOldText: b.DiffOldText,
			DiffNewText: b.DiffNewText,
			TerminalID:  b.TerminalID,
			Raw:         b.Raw,
		})
	}
	return out
}

func chatContentBlocksJSON(blocks []agent.ContentBlock) []chatContentBlockJSON {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]chatContentBlockJSON, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, chatContentBlockJSON{Kind: b.Kind, Text: b.Text, Raw: b.Raw})
	}
	return out
}

func chatPlanEntriesJSON(entries []agent.PlanEntry) []chatPlanEntryJSON {
	if len(entries) == 0 {
		return nil
	}
	out := make([]chatPlanEntryJSON, 0, len(entries))
	for _, e := range entries {
		out = append(out, chatPlanEntryJSON{Content: e.Content, Priority: e.Priority, Status: e.Status})
	}
	return out
}

// pumpTurns sends the conversation's turns: the caller's opening turn, if it
// has one, then each line of turns.Stdin as one more (one line = one turn).
// The lead context rides whichever of those goes first. Returns at EOF/read
// error, or when ctx is cancelled while a send is pending — without the ctx
// guard a send would block forever once a dead/cancelled chat stream stops
// draining out. Escapes within a line are decoded (see decodeMessageLine) so a
// single typed line can carry newlines, tabs, and quotes.
func pumpTurns(ctx context.Context, turns chatTurns, out chan<- agent.ChatMessage) error {
	lead := turns.Lead
	send := func(text string) error {
		// JoinLeadBlocks drops empties, so a turn with no lead (every turn
		// after the first) is sent exactly as typed. Clearing lead here is
		// what makes the context ride ONE turn.
		text = operations.JoinLeadBlocks(lead, text)
		lead = ""
		select {
		case out <- agent.ChatMessage{Text: text}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if strings.TrimSpace(turns.Opening) != "" {
		if err := send(turns.Opening); err != nil {
			return err
		}
	}
	if turns.Stdin == nil {
		return nil
	}

	scanner := bufio.NewScanner(turns.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		// A blank line is legitimately nothing to say — drop it here rather
		// than handing the driver an empty prompt (which refuses it, and
		// rightly: a zero-byte turn is the house silent-no-op).
		text := decodeMessageLine(scanner.Text())
		if strings.TrimSpace(text) == "" {
			continue
		}
		if err := send(text); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// decodeMessageLine interprets backslash escapes in a typed REPL line so a single
// line can carry control characters and explicit quotes: \n→newline, \t→tab,
// \r→carriage return, \\→backslash, and \" / \' → a literal quote. An
// unrecognized escape is left verbatim (the backslash is preserved) so content
// like a Windows path or a regex isn't silently mangled. Quote characters that
// are not escaped pass through as ordinary literal content.
func decodeMessageLine(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i == len(s)-1 {
			b.WriteByte(c)
			continue
		}
		switch s[i+1] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '\\':
			b.WriteByte('\\')
		case '"':
			b.WriteByte('"')
		case '\'':
			b.WriteByte('\'')
		default:
			b.WriteByte('\\')
			continue
		}
		i++
	}
	return b.String()
}
