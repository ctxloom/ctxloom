package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ctxloom/shared/agent"
	"github.com/ctxloom/shared/clidiag"
	"github.com/ctxloom/shared/iox"

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
func runStructuredREPL(ctx context.Context, client pb.Client, req *pb.RunStart, format string, stdin io.Reader, stdout io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	opts := req.GetOptions()
	in, events, errs, err := client.Chat(ctx, agent.ChatRequest{
		WorkDir:     opts.GetWorkDir(),
		Model:       opts.GetModel(),
		Env:         opts.GetEnv(),
		AutoApprove: opts.GetAutoApprove(),
	})
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
		scanDone <- readMessagesLoop(ctx, stdin, in)
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
	Model          string        `json:"model,omitempty"`
	PermissionMode string        `json:"permissionMode,omitempty"`
	ContextWindow  int           `json:"contextWindow,omitempty"`
	MCPServers     []chatMCPJSON `json:"mcpServers,omitempty"`
}

type chatEventJSON struct {
	Type     string            `json:"type"` // entry | complete | session
	Entry    *chatEntryJSON    `json:"entry,omitempty"`
	Complete *chatCompleteJSON `json:"complete,omitempty"`
	Session  *chatSessionJSON  `json:"session,omitempty"`
}

func chatEventToJSON(ev agent.ChatEvent) chatEventJSON {
	switch {
	case ev.Entry != nil:
		e := ev.Entry
		return chatEventJSON{Type: "entry", Entry: &chatEntryJSON{
			Type:       string(e.Type),
			Timestamp:  rfc3339OrEmpty(e.Timestamp),
			Content:    e.Content,
			ToolName:   e.ToolName,
			ToolInput:  e.ToolInput,
			ToolOutput: e.ToolOutput,
			IsError:    e.IsError,
		}}
	case ev.Complete != nil:
		c := ev.Complete
		return chatEventJSON{Type: "complete", Complete: &chatCompleteJSON{
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
		out := &chatSessionJSON{Model: s.Model, PermissionMode: s.PermissionMode, ContextWindow: s.ContextWindow}
		for _, m := range s.MCPServers {
			out.MCPServers = append(out.MCPServers, chatMCPJSON{Name: m.Name, Status: m.Status})
		}
		return chatEventJSON{Type: "session", Session: out}
	default:
		return chatEventJSON{}
	}
}

// readMessagesLoop reads in line by line and sends each decoded line as a chat
// message (one line = one message). Returns at EOF/read error, or when ctx is
// cancelled while a send is pending — without the ctx guard a send would block
// forever once a dead/cancelled chat stream stops draining out. Escapes within a
// line are decoded (see decodeMessageLine) so a single typed line can carry
// newlines, tabs, and quotes.
func readMessagesLoop(ctx context.Context, in io.Reader, out chan<- string) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		select {
		case out <- decodeMessageLine(scanner.Text()):
		case <-ctx.Done():
			return ctx.Err()
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
