package antigravity

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// Compile-time assertion that Antigravity offers the optional StructuredChat
// capability.
var _ agent.StructuredChat = (*Antigravity)(nil)

// chatWaitDelay bounds how long a turn's subprocess wait lingers on a
// surviving descendant's held-open stdout/stderr pipe after the process
// itself has exited or been cancelled — see runChatTurn's cmd.WaitDelay.
const chatWaitDelay = 3 * time.Second

// Chat implements structured chat as a BESPOKE PROSE driver over `agy -p` — the
// first non-ACP StructuredChat implementation in the codebase. agy has neither a
// native ACP subcommand (kiro/opencode's path) nor a first-party ACP adapter
// (codex-acp/claude-code-acp's path), and it MUST NOT adopt a third-party one:
// agy's OWN JSON conversation API (`agy agentapi new-conversation/send-message`)
// looked strictly better on paper (structured, ids in-band) but every subcommand
// hard-requires ANTIGRAVITY_LS_ADDRESS — a running Antigravity language-server
// this CLI ships no supported way to start. Driving it would mean bootstrapping
// and reverse-engineering a private `exa.language_server_pb` gRPC protocol, the
// exact private-internals coupling [[mimic-cli-native-surfaces]] forbids. See
// the agy-backend-permissions plan (§2.3) for the fuller record.
//
// agy's structured chat is genuinely WEAKER than every other backend's:
//   - prose in, prose out — no JSON, no streaming, no tool_use/tool_result
//     events (agy's own tool calls never leave the subprocess).
//   - TurnMeta token/cost/context fields always stay zero ("unknown" — prose
//     mode exposes none of it, so this driver never fabricates a number).
//   - ChatMessage.Permission answers are INERT: agy -p never forwards a
//     permission request, so req.ForwardPermissions cannot be honored. The
//     permission posture is decided ONCE at launch via buildArgs' switch
//     (backend.go) and never revisited mid-turn.
//   - LIVE FINDING (2026-07-15, authenticated agy 1.1.2): --mode plan itself is
//     NOT enforced headlessly (see the long comment on buildArgs) — this
//     applies here too, since every turn below spawns `agy -p` exactly like
//     Execute's oneshot path. A caller must not treat agy chat under
//     PermissionPlan as a genuine sandbox.
//
// Every turn spawns a FRESH `agy -p` subprocess directly via os/exec (mirroring
// internal/acp's spawnTransport, NOT BaseBackend.RunNonInteractive): the Chat
// gRPC path never calls Setup (only the Run/Execute RPC does — see
// internal/lm/grpc/chat.go), so BaseBackend.workDir is never populated when
// Chat runs. req.WorkDir is the only reliable cwd, exactly as internal/acp's
// driver uses req.WorkDir rather than any backend-held state.
func (b *Antigravity) Chat(ctx context.Context, req agent.ChatRequest, in <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
	defer close(out)

	send := func(ev agent.ChatEvent) bool {
		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}

	c := &chatTurns{b: b, ctx: ctx, req: req, send: send, conversationID: req.ResumeSessionID}

	if !send(agent.ChatEvent{Session: &agent.ChatSessionInfo{
		SessionID:      c.conversationID,
		Model:          req.Model,
		PermissionMode: req.Permissions.String(),
		MCPServers:     advisoryMCPStatus(req.MCPServers),
	}}) {
		return ctx.Err()
	}

	return c.run(in)
}

// chatTurns is one Chat call's turn state: the turn in flight (if any), the
// messages that arrived while it ran, and the conversation id every later turn
// resumes. The loop below is split across its methods so each arm stays legible
// on its own; the state is shared by all of them, which is why it lives here
// instead of being threaded through parameters.
type chatTurns struct {
	b    *Antigravity
	ctx  context.Context
	req  agent.ChatRequest
	send func(agent.ChatEvent) bool

	conversationID  string
	turnDone        chan chatTurnResult // non-nil while a turn is in flight
	cancelTurn      context.CancelFunc
	cancelRequested bool
	queued          []string // text messages that arrived mid-turn, in order
}

// chatTurnResult is one finished `agy -p` turn: its prose reply, when it
// started, and why it ended if it failed.
type chatTurnResult struct {
	text  string
	start time.Time
	err   error
}

// run is Chat's event loop: keep a turn moving while there is one to run, then
// service whichever of cancellation, a finished turn, or new input arrives
// first. It returns nil once input is closed and nothing is left to drain.
func (c *chatTurns) run(in <-chan agent.ChatMessage) error {
	inChan := in // nil'd once the caller closes input
	for {
		if !c.nextTurn(inChan != nil) {
			return nil
		}

		select {
		case <-c.ctx.Done():
			c.cancelInFlight()
			return c.ctx.Err()

		case res := <-c.turnDone:
			if err := c.completeTurn(res); err != nil {
				return err
			}

		case msg, ok := <-inChan:
			if !ok {
				// Input closed: finish the in-flight/queued turns, then return
				// (nextTurn reports done once both drain).
				inChan = nil
				continue
			}
			c.handleMessage(msg)
		}
	}
}

// nextTurn starts the next queued turn when none is in flight. It reports false
// only when nothing is in flight, nothing is queued, and no more input is
// coming — the loop's one non-error exit.
func (c *chatTurns) nextTurn(inputOpen bool) bool {
	if c.turnDone != nil {
		return true
	}
	if len(c.queued) == 0 {
		return inputOpen
	}
	next := c.queued[0]
	c.queued = c.queued[1:]
	c.startTurn(next)
	return true
}

// startTurn spawns one turn against the current conversation id, under its own
// cancellable context so a CancelTurn ends the turn rather than the Chat.
func (c *chatTurns) startTurn(text string) {
	turnCtx, cancel := context.WithCancel(c.ctx)
	c.cancelTurn = cancel
	done := make(chan chatTurnResult, 1)
	c.turnDone = done
	start := time.Now()
	go func() {
		reply, err := c.b.runChatTurn(turnCtx, c.req, text, c.conversationID)
		done <- chatTurnResult{text: reply, start: start, err: err}
	}()
}

// cancelInFlight cancels the turn in flight, if there is one.
func (c *chatTurns) cancelInFlight() {
	if c.cancelTurn != nil {
		c.cancelTurn()
	}
}

// completeTurn services a finished turn: emit the reply, refresh the
// conversation id, close the turn out. nil continues the loop; an error is
// terminal for the whole Chat.
func (c *chatTurns) completeTurn(res chatTurnResult) error {
	c.turnDone = nil
	c.cancelTurn = nil
	wasCancelled := c.cancelRequested
	c.cancelRequested = false

	if res.err != nil {
		return c.failedTurn(res.err, wasCancelled)
	}

	if !c.send(agent.ChatEvent{Entry: &agent.SessionEntry{
		Type:      agent.EntryTypeAssistant,
		Content:   res.text,
		Timestamp: res.start,
	}}) {
		return c.ctx.Err()
	}

	if err := c.refreshConversationID(); err != nil {
		return err
	}

	if !c.send(agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn", Model: c.req.Model}}) {
		return c.ctx.Err()
	}
	return nil
}

// failedTurn resolves a turn that ended in error. A turn the caller cancelled
// completes with StopReason "cancelled" and the conversation carries on (nil);
// anything else ends the Chat with that error.
func (c *chatTurns) failedTurn(err error, wasCancelled bool) error {
	if c.ctx.Err() != nil {
		return c.ctx.Err()
	}
	if !wasCancelled {
		return err
	}
	if !c.send(agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "cancelled", Model: c.req.Model}}) {
		return c.ctx.Err()
	}
	return nil
}

// refreshConversationID resolves (or refreshes) the conversation id via agy's
// own workspace->conversation map (conversationmap.go's agyConversationMap) —
// agy -p never prints it. A CHANGED id is emitted as a follow-up Session event,
// so a coordinator can journal SessionID for a later ResumeSessionID resume. An
// unreadable cache costs only the continuation, not the turn, so it is reported
// and the loop carries on.
//
// R3 (TOCTOU): that cache file is last-writer-wins under concurrent agy runs
// sharing a workDir; per-agent cwd isolation (worktree/container) is what
// actually protects this, not a lock here.
func (c *chatTurns) refreshConversationID() error {
	id, ok, err := c.b.resolveChatConversationID(c.req.WorkDir)
	if err != nil {
		agent.Warn("antigravity: %v — this turn's conversation id is unknown, so the next turn starts a fresh conversation instead of resuming", err)
	}
	if !ok || id == c.conversationID {
		return nil
	}
	c.conversationID = id
	if !c.send(agent.ChatEvent{Session: &agent.ChatSessionInfo{
		SessionID:      c.conversationID,
		Model:          c.req.Model,
		PermissionMode: c.req.Permissions.String(),
	}}) {
		return c.ctx.Err()
	}
	return nil
}

// handleMessage services one inbound message: a cancel request kills the turn in
// flight, a permission answer is inert, and anything else queues as a turn's
// text.
func (c *chatTurns) handleMessage(msg agent.ChatMessage) {
	switch {
	case msg.CancelTurn:
		if c.cancelTurn != nil {
			c.cancelRequested = true
			c.cancelTurn()
		}
	case msg.Permission != nil:
		// Inert (see Chat's doc comment): agy -p never asks, so there is nothing
		// to answer — the launch-time flags decided the posture for every turn.
	default:
		c.queued = append(c.queued, msg.Text)
	}
}

// runChatTurn spawns one `agy -p <text>` turn against req.WorkDir and returns
// its captured prose reply. conversationID, when non-empty, resumes that agy
// conversation (`--conversation <id> --continue`); empty starts a fresh one.
func (b *Antigravity) runChatTurn(ctx context.Context, req agent.ChatRequest, text, conversationID string) (string, error) {
	args := b.chatArgs(req, text, conversationID)
	// Derive --print-timeout from ctx's own deadline when the caller set one,
	// so agy's wait matches ctx rather than racing its own 5m default against
	// a shorter caller deadline (whichever fires first would otherwise win
	// with a less informative error). No deadline -> no flag -> agy's
	// documented 5m default applies.
	if deadline, ok := ctx.Deadline(); ok {
		if d := time.Until(deadline); d > 0 {
			args = append(args, "--print-timeout", d.Round(time.Second).String())
		}
	}

	binary := b.BinaryPath
	if binary == "" {
		binary = "agy"
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = req.WorkDir
	cmd.Env = b.BuildEnv(req.Env)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Held-fd hang guard, same shape and reasoning as
	// internal/lm/backends/launcher.go's nonInteractiveWaitDelay (not reused
	// directly: that package imports this one, so importing back would cycle).
	// A CancelTurn (or the whole Chat's ctx ending) SIGKILLs agy's direct
	// process, but a surviving descendant it forked can keep holding the
	// stdout/stderr pipes open — without WaitDelay, Wait() blocks until that
	// descendant's OWN exit, which can be arbitrarily later than the
	// cancellation the caller asked for.
	cmd.WaitDelay = chatWaitDelay

	err := cmd.Run()
	if ctx.Err() != nil {
		// Cancelled (either this turn's CancelTurn, or the whole Chat's ctx) —
		// the caller distinguishes the two via its own ctx check; report the
		// cancellation plainly rather than agy's SIGKILL exit status.
		return "", ctx.Err()
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("agy -p failed: %s", msg)
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

// chatArgs builds one turn's argv: the shared permission-posture switch
// (backend.go's buildArgs, so Chat and Execute can never map a tier
// differently), the resume flags when a conversation id is already known, and
// the print-timeout derived from ctx's deadline (falling back to agy's own 5m
// default when the call carries none).
func (b *Antigravity) chatArgs(req agent.ChatRequest, text, conversationID string) []string {
	execReq := &agent.ExecuteRequest{
		Mode:        agent.ModeOneshot,
		Model:       req.Model,
		Permissions: req.Permissions,
	}
	args := b.buildArgs(execReq, req.Model) // model + permission flags only (no prompt: Prompt is nil)

	if conversationID != "" {
		args = append(args, "--conversation", conversationID, "--continue")
	}

	args = append(args, "-p", text)
	return args
}

// advisoryMCPStatus reports the caller-supplied MCP servers as advisory-only
// status: agy has no per-invocation MCP flag (Setup already wrote
// .agents/mcp_config.json, which agy reads from cwd on its own), so
// req.MCPServers cannot be injected per-turn the way an ACP session/new call
// can. Surfacing them here at least lets a client show what was ASKED for,
// even though this driver cannot enforce it turn-by-turn.
func advisoryMCPStatus(servers []agent.ChatMCPServer) []agent.MCPStatus {
	if len(servers) == 0 {
		return nil
	}
	out := make([]agent.MCPStatus, len(servers))
	for i, s := range servers {
		out[i] = agent.MCPStatus{Name: s.Name, Status: "advisory (agy has no per-invocation MCP flag; see .agents/mcp_config.json)"}
	}
	return out
}

// resolveChatConversationID looks up workDir's current agy conversation id via
// agy's own workspace->conversation map (conversationmap.go's agyConversationMap
// — a live continuation lookup, NOT the deleted transcript scraper; see
// backend.go's convMap doc). ok is false when workDir is empty, the cache
// file doesn't exist yet, or workDir has no entry.
//
// A cache file that exists but cannot be read or parsed is returned as an error,
// NOT folded into ok=false: "no entry yet" is routine, whereas an unreadable
// cache means every later turn silently loses its continuation, which the caller
// must be able to report.
func (b *Antigravity) resolveChatConversationID(workDir string) (id string, ok bool, err error) {
	if workDir == "" {
		return "", false, nil
	}
	m, err := b.convMap.read()
	if err != nil {
		return "", false, err
	}
	if m == nil {
		return "", false, nil
	}
	id, ok = m[filepath.Clean(workDir)]
	return id, ok, nil
}
