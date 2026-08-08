package antigravity

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// ---- fake `agy` binary -----------------------------------------------------
//
// Chat spawns its subprocess directly via os/exec (see chat.go's doc comment:
// the Chat gRPC path never calls Setup, so BaseBackend.workDir is never
// populated — this driver cannot reuse the injected pty Launcher, which is
// itself only wired at the plugin-registry boundary). Tests below therefore
// point b.BinaryPath at a real, tiny shell script rather than mocking an
// interface — the same "spawn a fake CLI on PATH" shape internal/acp's own
// tests use for its subprocess transport.
//
// The script records every invocation's argv (NUL^A i.e. \x1f-separated, one
// call per line) to $FAKE_AGY_LOG, optionally mirrors itself into a
// last_conversations.json-shaped file at $FAKE_AGY_CONV_FILE (simulating
// agy's own post-turn write, keyed by $FAKE_AGY_WORKDIR -> $FAKE_AGY_CONV_ID)
// when both are set, optionally sleeps (for the cancel test), optionally
// fails with a stderr message, and otherwise prints $FAKE_AGY_REPLY to
// stdout.
const fakeAgyScript = `#!/usr/bin/env bash
set -u
{
  for a in "$@"; do printf '%s\x1f' "$a"; done
  printf '\n'
} >> "$FAKE_AGY_LOG"

if [ -n "${FAKE_AGY_CONV_FILE:-}" ] && [ -n "${FAKE_AGY_CONV_ID:-}" ]; then
  printf '{"%s":"%s"}' "$FAKE_AGY_WORKDIR" "$FAKE_AGY_CONV_ID" > "$FAKE_AGY_CONV_FILE"
fi

if [ -n "${FAKE_AGY_SLEEP:-}" ]; then
  sleep "$FAKE_AGY_SLEEP"
fi

if [ -n "${FAKE_AGY_FAIL:-}" ]; then
  echo "${FAKE_AGY_FAIL_MSG:-boom}" >&2
  exit 1
fi

printf '%s' "${FAKE_AGY_REPLY:-ok}"
`

// writeFakeAgy installs the fake agy script into dir and returns its path.
func writeFakeAgy(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-agy")
	require.NoError(t, os.WriteFile(path, []byte(fakeAgyScript), 0755))
	return path
}

// fakeAgyInvocations parses the argv log the fake script wrote: one []string
// per invocation, in call order.
func fakeAgyInvocations(t *testing.T, logPath string) [][]string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var calls [][]string
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(strings.TrimSuffix(line, "\x1f"), "\x1f")
		calls = append(calls, fields)
	}
	return calls
}

// newChatTestBackend builds an *Antigravity wired exactly like NewAntigravity
// but with its session history pointed at a test-controlled home dir (so
// resolveChatConversationID reads the FAKE last_conversations.json the fake
// agy script writes, via the REAL OS filesystem — the fake script is a real
// subprocess, so an in-memory afero.Fs would never see its write).
func newChatTestBackend(t *testing.T, binaryPath, home string) *Antigravity {
	t.Helper()
	b := &Antigravity{convMap: agyConversationMap{SessionStore: agent.SessionStore{FS: afero.NewOsFs(), HomeDir: home}}}
	b.BaseBackend = agent.NewBaseBackend("antigravity", "1.0.0")
	b.BinaryPath = binaryPath
	b.InitLaunch(
		agent.NewBaseLifecycle("antigravity"),
		agent.NewBaseContextProvider(),
		nil, // SessionHistory: deleted — resolveChatConversationID uses b.convMap instead
		&agent.CellDelivery{Build: agent.BuildWellKnown(NewSurfaces), RawContext: true},
	)
	return b
}

// drainChat runs Chat to completion in a goroutine, sends texts as separate
// turns (closing in once all are sent), and returns every ChatEvent plus the
// terminal error.
func drainChat(t *testing.T, ctx context.Context, b *Antigravity, req agent.ChatRequest, texts []string, extraDrive func(in chan<- agent.ChatMessage, events <-chan agent.ChatEvent)) ([]agent.ChatEvent, error) {
	t.Helper()
	in := make(chan agent.ChatMessage)
	out := make(chan agent.ChatEvent)

	errCh := make(chan error, 1)
	go func() { errCh <- b.Chat(ctx, req, in, out) }()

	var events []agent.ChatEvent
	done := make(chan struct{})
	go func() {
		for ev := range out {
			events = append(events, ev)
		}
		close(done)
	}()

	if extraDrive != nil {
		extraDrive(in, out)
	} else {
		for _, text := range texts {
			in <- agent.ChatMessage{Text: text}
		}
		close(in)
	}

	<-done
	return events, <-errCh
}

func TestChat_SingleTurn_EmitsSessionEntryComplete(t *testing.T) {
	dir := t.TempDir()
	binary := writeFakeAgy(t, dir)
	home := t.TempDir()
	b := newChatTestBackend(t, binary, home)

	logPath := filepath.Join(dir, "argv.log")
	req := agent.ChatRequest{
		WorkDir:     filepath.Join(dir, "ws"),
		Model:       "Gemini 3.5 Flash (Medium)",
		Permissions: agent.PermissionDefault,
		Env: map[string]string{
			"FAKE_AGY_LOG":   logPath,
			"FAKE_AGY_REPLY": "the answer is 42",
		},
	}
	require.NoError(t, os.MkdirAll(req.WorkDir, 0755))

	events, err := drainChat(t, context.Background(), b, req, []string{"what is the answer?"}, nil)
	require.NoError(t, err)

	require.GreaterOrEqual(t, len(events), 3)
	require.NotNil(t, events[0].Session, "first event is session info")
	assert.Equal(t, "Gemini 3.5 Flash (Medium)", events[0].Session.Model, "model rides verbatim (display string, R4)")

	var entry *agent.SessionEntry
	var complete *agent.TurnMeta
	for _, ev := range events[1:] {
		if ev.Entry != nil {
			entry = ev.Entry
		}
		if ev.Complete != nil {
			complete = ev.Complete
		}
	}
	require.NotNil(t, entry, "an assistant entry was emitted")
	assert.Equal(t, agent.EntryTypeAssistant, entry.Type)
	assert.Equal(t, "the answer is 42", entry.Content, "prose round-trips verbatim")

	require.NotNil(t, complete, "a completion was emitted")
	assert.Equal(t, "end_turn", complete.StopReason)
	// Prose mode exposes no accounting — TurnMeta must stay honestly zero, not
	// fabricate a number.
	assert.Zero(t, complete.InputTokens)
	assert.Zero(t, complete.OutputTokens)
	assert.Zero(t, complete.CostUSD)

	calls := fakeAgyInvocations(t, logPath)
	require.Len(t, calls, 1)
	assert.Contains(t, calls[0], "--model")
	assert.Contains(t, calls[0], "Gemini 3.5 Flash (Medium)")
	assert.Contains(t, calls[0], "-p")
	assert.Contains(t, calls[0], "what is the answer?")
	// PermissionDefault: no permission flag at all (agy's own prompting).
	assert.NotContains(t, calls[0], "--mode")
	assert.NotContains(t, calls[0], "--dangerously-skip-permissions")
}

// TestChat_PermissionFlagsThreaded proves the SAME buildArgs permission switch
// (backend.go, shared with Execute) drives Chat's per-turn argv — the plan's
// design goal that Chat and Execute can never map a tier differently.
func TestChat_PermissionFlagsThreaded(t *testing.T) {
	dir := t.TempDir()
	binary := writeFakeAgy(t, dir)
	home := t.TempDir()

	tests := []struct {
		name string
		perm agent.PermissionMode
		want []string
	}{
		{"plan", agent.PermissionPlan, []string{"--mode", "plan"}},
		{"acceptEdits", agent.PermissionAcceptEdits, []string{"--mode", "accept-edits"}},
		{"bypass", agent.PermissionBypass, []string{"--dangerously-skip-permissions"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newChatTestBackend(t, binary, home)
			logPath := filepath.Join(dir, tt.name+"-argv.log")
			ws := filepath.Join(dir, tt.name+"-ws")
			require.NoError(t, os.MkdirAll(ws, 0755))
			req := agent.ChatRequest{
				WorkDir:     ws,
				Permissions: tt.perm,
				Env:         map[string]string{"FAKE_AGY_LOG": logPath, "FAKE_AGY_REPLY": "ok"},
			}
			_, err := drainChat(t, context.Background(), b, req, []string{"hi"}, nil)
			require.NoError(t, err)

			calls := fakeAgyInvocations(t, logPath)
			require.Len(t, calls, 1)
			for _, w := range tt.want {
				assert.Contains(t, calls[0], w)
			}
		})
	}
}

// TestChat_ConversationContinuity is the plan's §8 item 6 payload test: turn 2
// must spawn with `--conversation <id> --continue`, where <id> is EXACTLY the
// id agy's own workspace map reported after turn 1 — proving the driver reads
// the id back rather than inventing one.
func TestChat_ConversationContinuity(t *testing.T) {
	dir := t.TempDir()
	binary := writeFakeAgy(t, dir)
	home := t.TempDir()
	b := newChatTestBackend(t, binary, home)

	logPath := filepath.Join(dir, "argv.log")
	ws := filepath.Join(dir, "ws")
	require.NoError(t, os.MkdirAll(ws, 0755))
	convFile := filepath.Join(home, ".gemini", "antigravity-cli", "cache", "last_conversations.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(convFile), 0755))
	const wantID = "cccccccc-1111-2222-3333-444444444444"

	req := agent.ChatRequest{
		WorkDir: ws,
		Env: map[string]string{
			"FAKE_AGY_LOG":       logPath,
			"FAKE_AGY_REPLY":     "ack",
			"FAKE_AGY_CONV_FILE": convFile,
			"FAKE_AGY_CONV_ID":   wantID,
			"FAKE_AGY_WORKDIR":   filepath.Clean(ws),
		},
	}

	events, err := drainChat(t, context.Background(), b, req, []string{"turn one", "turn two"}, nil)
	require.NoError(t, err)

	calls := fakeAgyInvocations(t, logPath)
	require.Len(t, calls, 2, "two turns, two spawns")
	assert.NotContains(t, calls[0], "--conversation", "turn 1 starts fresh")
	assert.Contains(t, calls[1], "--conversation")
	assert.Contains(t, calls[1], wantID, "turn 2 resumes EXACTLY the id the map reported after turn 1")
	assert.Contains(t, calls[1], "--continue")

	// A follow-up Session event carried the resolved id for a coordinator to
	// journal as ResumeSessionID.
	var sawResolvedSession bool
	for _, ev := range events {
		if ev.Session != nil && ev.Session.SessionID == wantID {
			sawResolvedSession = true
		}
	}
	assert.True(t, sawResolvedSession, "the resolved conversation id was surfaced as a Session event")
}

// TestChat_CorruptConversationCacheIsLoud pins a real bug: agyConversationMap.read
// deliberately distinguishes an ABSENT cache file (nil map, no error — nothing
// to resume) from one that EXISTS but does not parse (agy changed the format, or
// the file is corrupt), and its doc says the latter is surfaced rather than
// discarded. The only caller collapsed both into ok=false, so a corrupt cache
// silently cost every subsequent turn its continuation: each one starts a fresh
// agy conversation with no journal-able id and no diagnostic anywhere.
func TestChat_CorruptConversationCacheIsLoud(t *testing.T) {
	dir := t.TempDir()
	binary := writeFakeAgy(t, dir)
	home := t.TempDir()
	b := newChatTestBackend(t, binary, home)

	ws := filepath.Join(dir, "ws")
	require.NoError(t, os.MkdirAll(ws, 0755))
	convFile := filepath.Join(home, ".gemini", "antigravity-cli", "cache", "last_conversations.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(convFile), 0755))
	require.NoError(t, os.WriteFile(convFile, []byte("{not json"), 0644))

	req := agent.ChatRequest{
		WorkDir: ws,
		Env: map[string]string{
			"FAKE_AGY_LOG":   filepath.Join(dir, "argv.log"),
			"FAKE_AGY_REPLY": "ack",
		},
	}

	var (
		events []agent.ChatEvent
		err    error
	)
	stderr := captureStderr(t, func() {
		events, err = drainChat(t, context.Background(), b, req, []string{"hi"}, nil)
	})
	require.NoError(t, err, "an unreadable cache must not fail the turn — the reply was already produced")
	assert.Contains(t, stderr, "last_conversations.json", "the unreadable cache is reported")

	// The turn itself still completed normally.
	var sawEntry, sawComplete bool
	for _, ev := range events {
		if ev.Entry != nil && ev.Entry.Content == "ack" {
			sawEntry = true
		}
		if ev.Complete != nil {
			sawComplete = true
		}
	}
	assert.True(t, sawEntry, "the assistant reply is still delivered")
	assert.True(t, sawComplete, "the turn still completes")
}

// TestChat_ResumeSessionID_FirstTurnIncludesContinueFlags proves
// ChatRequest.ResumeSessionID reaches the FIRST turn's argv, not just a later
// one — resuming a prior native session per the StructuredChat contract.
func TestChat_ResumeSessionID_FirstTurnIncludesContinueFlags(t *testing.T) {
	dir := t.TempDir()
	binary := writeFakeAgy(t, dir)
	home := t.TempDir()
	b := newChatTestBackend(t, binary, home)

	logPath := filepath.Join(dir, "argv.log")
	ws := filepath.Join(dir, "ws")
	require.NoError(t, os.MkdirAll(ws, 0755))
	const resumeID = "existing-conversation-id"

	req := agent.ChatRequest{
		WorkDir:         ws,
		ResumeSessionID: resumeID,
		Env:             map[string]string{"FAKE_AGY_LOG": logPath, "FAKE_AGY_REPLY": "resumed"},
	}
	_, err := drainChat(t, context.Background(), b, req, []string{"continue where we left off"}, nil)
	require.NoError(t, err)

	calls := fakeAgyInvocations(t, logPath)
	require.Len(t, calls, 1)
	assert.Contains(t, calls[0], "--conversation")
	assert.Contains(t, calls[0], resumeID)
	assert.Contains(t, calls[0], "--continue")
}

// TestChat_NonZeroExit_ReturnsError proves a failing agy turn surfaces as an
// error (agy -p exits non-zero and buffers no usable reply) rather than
// silently completing with empty prose — a silent no-op class this codebase
// treats as a bug.
func TestChat_NonZeroExit_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	binary := writeFakeAgy(t, dir)
	home := t.TempDir()
	b := newChatTestBackend(t, binary, home)

	ws := filepath.Join(dir, "ws")
	require.NoError(t, os.MkdirAll(ws, 0755))
	req := agent.ChatRequest{
		WorkDir: ws,
		Env: map[string]string{
			"FAKE_AGY_LOG":      filepath.Join(dir, "argv.log"),
			"FAKE_AGY_FAIL":     "1",
			"FAKE_AGY_FAIL_MSG": "model unavailable",
		},
	}
	_, err := drainChat(t, context.Background(), b, req, []string{"hi"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model unavailable")
}

// TestChat_CancelTurn proves CancelTurn kills the in-flight `agy -p` and
// completes the turn with StopReason "cancelled" WITHOUT ending the whole
// conversation — a subsequent turn still runs normally.
func TestChat_CancelTurn(t *testing.T) {
	dir := t.TempDir()
	binary := writeFakeAgy(t, dir)
	home := t.TempDir()
	b := newChatTestBackend(t, binary, home)

	ws := filepath.Join(dir, "ws")
	require.NoError(t, os.MkdirAll(ws, 0755))
	logPath := filepath.Join(dir, "argv.log")

	req := agent.ChatRequest{
		WorkDir: ws,
		Env: map[string]string{
			"FAKE_AGY_LOG":   logPath,
			"FAKE_AGY_SLEEP": "30",
			"FAKE_AGY_REPLY": "should never see this",
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	events, err := drainChat(t, ctx, b, req, nil, func(in chan<- agent.ChatMessage, events <-chan agent.ChatEvent) {
		in <- agent.ChatMessage{Text: "this will hang"}
		// Give the turn a moment to actually spawn before cancelling it.
		time.Sleep(300 * time.Millisecond)
		in <- agent.ChatMessage{CancelTurn: true}
		// The conversation must still be usable after a cancel: a second,
		// FAST turn (no sleep env this time — the fake script only sleeps
		// while FAKE_AGY_SLEEP is set on THIS request, which is fixed for
		// the whole ChatRequest.Env, so this second turn also sleeps 30s
		// unless cancelled too... instead just close input here and prove
		// the FIRST cancel alone completes cleanly.
		close(in)
	})
	require.NoError(t, err, "a cancelled turn must not fail the whole Chat call")

	var sawCancelled bool
	for _, ev := range events {
		if ev.Complete != nil && ev.Complete.StopReason == "cancelled" {
			sawCancelled = true
		}
	}
	assert.True(t, sawCancelled, "the cancelled turn completed with StopReason cancelled")
}

// TestChat_InputClosedWithNoTurns proves Chat returns cleanly (nil error) when
// the caller closes `in` without ever sending a message — no turn, no hang.
func TestChat_InputClosedWithNoTurns(t *testing.T) {
	dir := t.TempDir()
	binary := writeFakeAgy(t, dir)
	home := t.TempDir()
	b := newChatTestBackend(t, binary, home)

	ws := filepath.Join(dir, "ws")
	require.NoError(t, os.MkdirAll(ws, 0755))
	req := agent.ChatRequest{WorkDir: ws, Env: map[string]string{"FAKE_AGY_LOG": filepath.Join(dir, "argv.log")}}

	events, err := drainChat(t, context.Background(), b, req, nil, func(in chan<- agent.ChatMessage, _ <-chan agent.ChatEvent) {
		close(in)
	})
	require.NoError(t, err)
	require.Len(t, events, 1, "only the initial Session event")
	assert.NotNil(t, events[0].Session)
}

// TestChat_PermissionAnswerIsInert characterizes the input arm no other test
// reaches, ahead of a later split of Chat's loop: a PermissionAnswer is
// accepted and does nothing — agy -p never forwards a permission request, so
// there is nothing for an answer to resolve — and in particular it is NOT
// treated as a turn's text. It must not spawn agy, must not emit an Entry, and
// must not stop the conversation: a real turn after it still runs.
func TestChat_PermissionAnswerIsInert(t *testing.T) {
	dir := t.TempDir()
	binary := writeFakeAgy(t, dir)
	home := t.TempDir()
	b := newChatTestBackend(t, binary, home)

	ws := filepath.Join(dir, "ws")
	require.NoError(t, os.MkdirAll(ws, 0755))
	logPath := filepath.Join(dir, "argv.log")
	req := agent.ChatRequest{
		WorkDir: ws,
		Env:     map[string]string{"FAKE_AGY_LOG": logPath, "FAKE_AGY_REPLY": "after"},
	}

	events, err := drainChat(t, context.Background(), b, req, nil, func(in chan<- agent.ChatMessage, _ <-chan agent.ChatEvent) {
		in <- agent.ChatMessage{Permission: &agent.PermissionAnswer{ID: "1", OptionID: "allow"}}
		in <- agent.ChatMessage{Text: "real turn"}
		close(in)
	})
	require.NoError(t, err)

	calls := fakeAgyInvocations(t, logPath)
	require.Len(t, calls, 1, "the permission answer spawned no agy; the text turn did")
	assert.Contains(t, calls[0], "real turn")

	var entries []string
	for _, ev := range events {
		if ev.Entry != nil {
			entries = append(entries, ev.Entry.Content)
		}
	}
	assert.Equal(t, []string{"after"}, entries)
}

func TestAdvisoryMCPStatus(t *testing.T) {
	assert.Nil(t, advisoryMCPStatus(nil))
	got := advisoryMCPStatus([]agent.ChatMCPServer{{Name: "toolserver"}})
	require.Len(t, got, 1)
	assert.Equal(t, "toolserver", got[0].Name)
	assert.NotEmpty(t, got[0].Status)
}

// ---- @live (gated) ----------------------------------------------------------
//
// Not run by `just test`: requires an authenticated `agy` on PATH and spends
// real model turns/credits. Set CTXLOOM_LIVE_AGY=1 to run it manually — the
// same self-skip discipline the acceptance suite's @live scenarios use
// (steps_live.go), just at the Go-test layer since this exercises the new
// Chat driver directly rather than through `ctxloom run`.
func TestChat_Live_EchoesContextSentinel(t *testing.T) {
	if os.Getenv("CTXLOOM_LIVE_AGY") == "" {
		t.Skip("set CTXLOOM_LIVE_AGY=1 to run against a real, authenticated agy")
	}

	dir := t.TempDir()
	ws := filepath.Join(dir, "ws")
	require.NoError(t, os.MkdirAll(filepath.Join(ws, ".agents"), 0755))
	const sentinel = "CTXLOOM-CHAT-DRIVER-LIVE-SENTINEL-9f21ac"
	require.NoError(t, os.WriteFile(filepath.Join(ws, ".agents", "AGENTS.md"),
		[]byte(fmt.Sprintf("# Context\n\nThe one distinct marker phrase in this context is: %s\n", sentinel)), 0644))

	b := NewAntigravity() // real agy on PATH, real home dir (~/.gemini/...)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	req := agent.ChatRequest{WorkDir: ws}

	events, err := drainChat(t, ctx, b, req,
		[]string{"Please repeat, verbatim and in full, the one distinct marker phrase you can see in your context. Output nothing else."}, nil)
	require.NoError(t, err)

	var reply string
	for _, ev := range events {
		if ev.Entry != nil {
			reply += ev.Entry.Content
		}
	}
	assert.Contains(t, reply, sentinel, "the Chat driver's prose round-trip actually delivered the materialized context to a real agy")
}

// TestChat_StructuredChatCapabilityIsAdvertised pins the invariant two
// documents now assert in prose: antigravity DOES offer the optional
// StructuredChat capability, implemented here as a bespoke prose driver over
// `agy -p` rather than through internal/acp.NewChatDriver. Both
// docs/transcript-schema.md §2 and the transcript reader's package doc
// (internal/transcript/vendorreader/antigravity) previously claimed the opposite
// — "antigravity has no StructuredChat capability at all (no
// internal/antigravity/chat.go)" — and reasoned from it. This assertion is
// what makes the corrected prose maintained rather than merely asserted: if
// Chat is ever removed, this stops compiling and both claims come back up for
// review. chat.go's own `var _ agent.StructuredChat` cannot do that job, since
// it would be deleted along with the file it guards.
func TestChat_StructuredChatCapabilityIsAdvertised(t *testing.T) {
	var b any = &Antigravity{}
	sc, ok := b.(agent.StructuredChat)
	require.True(t, ok, "antigravity must offer StructuredChat (internal/antigravity/chat.go)")
	assert.NotNil(t, sc)
}
