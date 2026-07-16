package cli

// H1b — the "one door" equivalence test (ACP conformance harness, second
// layer). ctxloom has exactly TWO frontends that drive an engine
// conversation: `run --structured` (runStructuredREPL, this file's sibling
// run_structured.go) and `ctxloom acp` (internal/acpagent, opened via
// internal/operations.OpenEngineSession). Both are documented to reach the
// engine ONLY through pb.Client.Chat — the one door where value injection
// (transcript tee, context assembly, trust gate, harp mint) lives so no
// frontend can forget it (see internal/operations/engine_session.go's package
// doc and internal/lm/grpc/chat.go's GRPCClient.Chat, "edgy-ivory"/"tough-cloud
// S2" comments).
//
// This file drives the IDENTICAL scripted conversation through both
// frontends against a deterministic, hermetic backend (doorBackend — no real
// engine, no credentials, no network) and asserts the resulting
// agent.ChatEvent sequences and canonical transcripts are identical modulo
// ts/harp/seq. If they ever diverge, a frontend has grown its own door
// around the shared substrate — a real architectural regression, not a test
// to be tuned into passing.
//
// Both paths are driven over REAL production wire code:
//   - runStructuredREPL is called directly (unexported, same package).
//   - The ACP side runs the real acpagent.Serve (the exact entrypoint
//     `ctxloom acp` calls) over an in-process io.Pipe transport — the same
//     in-process discipline internal/acpagent/server_test.go's startServer
//     already uses — driven by a minimal hand-written ACP client using the
//     production jsonrpc codec (deliberately NOT shared with
//     internal/acpagent/loopback_test.go's own driver: this codebase's
//     established discipline is to duplicate small test drivers rather than
//     couple them — see that file's doc comment for the same argument, and it
//     could not be imported here anyway: internal/cli already imports
//     internal/acpagent, so the reverse import would cycle).
//   - Both paths reach the backend through a REAL GRPCServer/GRPCClient pair
//     (production types, unmodified) wired over an in-memory gRPC transport
//     (bufconn) — the identical technique internal/lm/grpc/
//     server_realtransport_test.go already uses to exercise the real wire
//     path without a subprocess. Only the OUTERMOST layer is swapped (no
//     go-plugin subprocess spawn, no unix socket), mirroring L1's own
//     documented discipline (loopback_test.go) of substituting only the
//     engine, never the protocol code under test.
//
// What this test does NOT cover (by design, stated honestly rather than
// silently assumed): context assembly, the MCP trust gate, and harp minting
// (OpenEngineSession's own value-injection logic) are bypassed here — this
// ChatOpener wires pb.Client.Chat directly, the same way
// internal/acpagent/server_test.go's fakes do, because OpenEngineSession
// itself is out of scope for this slice (owned by ISO2, actively being
// rewritten). This test pins the ONE DOOR'S output equivalence, not
// OpenEngineSession's internals.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	api "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/ctxloom/ctxloom/internal/acp/jsonrpc"
	"github.com/ctxloom/ctxloom/internal/acpagent"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

// doorMessages is the deterministic scripted conversation driven through
// BOTH doors. Deliberately its own independent script (not shared with
// cmd/acpl1harness/engine.go's sentinel vocabulary) — same "duplicate, don't
// couple" discipline loopback_test.go documents for its own sentinels.
var doorMessages = []string{"first door turn", "second door turn"}

// doorBackend is a deterministic, hermetic agent.Backend + agent.StructuredChat:
// no real engine, no credentials, no network. It emits one session event,
// then for every inbound turn one assistant entry echoing the text plus one
// completion — enough real substance (multiple turns, real content, real
// accounting numbers) to make an identical-sequence comparison meaningful.
type doorBackend struct{}

func (doorBackend) Name() string                                     { return "h1b-door-backend" }
func (doorBackend) Version() string                                  { return "test" }
func (doorBackend) SupportedModes() []agent.ExecutionMode            { return nil }
func (doorBackend) History() agent.SessionHistory                    { return nil }
func (doorBackend) Setup(context.Context, *agent.SetupRequest) error { return nil }
func (doorBackend) Cleanup(context.Context) error                    { return nil }
func (doorBackend) Execute(context.Context, *agent.ExecuteRequest, io.Writer, io.Writer) (*agent.ExecuteResult, error) {
	return &agent.ExecuteResult{}, nil
}

func (doorBackend) Chat(ctx context.Context, req agent.ChatRequest, in <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
	defer close(out)
	select {
	case out <- agent.ChatEvent{Session: &agent.ChatSessionInfo{Model: req.Model}}:
	case <-ctx.Done():
		return ctx.Err()
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-in:
			if !ok {
				return nil
			}
			if msg.CancelTurn || msg.Permission != nil {
				continue // this scripted conversation never exercises cancel/permission
			}
			entry := agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "echo: " + msg.Text}}
			complete := agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn", InputTokens: len(msg.Text)}}
			select {
			case out <- entry:
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case out <- complete:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

var (
	_ agent.Backend        = doorBackend{}
	_ agent.StructuredChat = doorBackend{}
)

// newDoorClient wires a REAL GRPCServer/GRPCClient pair (production types,
// unmodified) over an in-memory gRPC transport (bufconn) driving doorBackend —
// see the file doc comment for why this is the real wire path, not a
// substitute for it.
func newDoorClient(t *testing.T) pb.Client {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	grpcServer := googlegrpc.NewServer()
	plug := &pb.LLMGRPCPlugin{Impl: doorBackend{}}
	require.NoError(t, plug.GRPCServer(nil, grpcServer))

	go func() { _ = grpcServer.Serve(lis) }()

	conn, err := googlegrpc.NewClient("passthrough:///doorbufnet",
		googlegrpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		googlegrpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)

	raw, err := plug.GRPCClient(context.Background(), nil, conn)
	require.NoError(t, err)
	// *pb.GRPCClient implements every Client method EXCEPT Kill (production
	// code only ever reaches it wrapped inside *pb.LLMRunner, which supplies
	// Kill via the go-plugin subprocess handle — see runnerFromConn in
	// client.go). There is no subprocess here, so doorClient below supplies
	// Kill itself (tearing down THIS bufconn conn/server) via embedding.
	grpcClient, ok := raw.(*pb.GRPCClient)
	require.True(t, ok, "LLMGRPCPlugin.GRPCClient must return a *pb.GRPCClient")

	t.Cleanup(func() {
		_ = conn.Close()
		grpcServer.GracefulStop()
	})
	return &doorClient{GRPCClient: grpcClient, conn: conn, server: grpcServer}
}

// doorClient adds the Kill method *pb.GRPCClient itself lacks (see
// newDoorClient) so the wrapped value satisfies pb.Client in full.
type doorClient struct {
	*pb.GRPCClient
	conn   *googlegrpc.ClientConn
	server *googlegrpc.Server
}

func (d *doorClient) Kill() {
	_ = d.conn.Close()
	d.server.GracefulStop()
}

// capturingClient wraps a real pb.Client and taps the RAW ChatEvent stream —
// exactly what crosses the one door — independent of how each frontend
// renders it. Forwarding is lossless: every event is captured AND forwarded
// unchanged, the same passthrough discipline internal/lm/grpc/chat_test.go
// already pins for the transcript tee itself.
type capturingClient struct {
	pb.Client
	mu     sync.Mutex
	events []agent.ChatEvent
}

func (c *capturingClient) Chat(ctx context.Context, req agent.ChatRequest) (chan<- agent.ChatMessage, <-chan agent.ChatEvent, <-chan error, error) {
	in, events, errs, err := c.Client.Chat(ctx, req)
	if err != nil {
		return in, events, errs, err
	}
	out := make(chan agent.ChatEvent)
	go func() {
		defer close(out)
		for ev := range events {
			c.mu.Lock()
			c.events = append(c.events, ev)
			c.mu.Unlock()
			out <- ev
		}
	}()
	return in, out, errs, nil
}

func (c *capturingClient) captured() []agent.ChatEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]agent.ChatEvent(nil), c.events...)
}

// doorEnv builds the ChatRequest env carrying the transcript-capture harp,
// omitted entirely when harp is empty (agent.SessionHarpEnv unset skips
// capture — see GRPCClient.Chat's openRecorder gate).
func doorEnv(harp string) map[string]string {
	if harp == "" {
		return nil
	}
	return map[string]string{agent.SessionHarpEnv: harp}
}

// runDoorViaStructuredREPL drives doorMessages through the REAL
// runStructuredREPL (run_structured.go:34) — the exact function `ctxloom run
// --structured` calls — and returns the raw ChatEvent sequence captured at
// the pb.Client.Chat tap. harp == "" skips canonical-transcript capture.
func runDoorViaStructuredREPL(t *testing.T, harp string) []agent.ChatEvent {
	t.Helper()
	cap := &capturingClient{Client: newDoorClient(t)}
	req := &pb.RunStart{Options: &pb.RunOptions{
		WorkDir: t.TempDir(),
		Model:   "door-model",
		Env:     doorEnv(harp),
	}}
	stdin := strings.NewReader(strings.Join(doorMessages, "\n") + "\n")
	var stdout bytes.Buffer
	err := runStructuredREPL(context.Background(), cap, req, nil, formatJSON, stdin, &stdout)
	require.NoError(t, err, "run --structured must complete cleanly over the door conversation")
	require.NotEmpty(t, stdout.Bytes(), "run --structured must actually render SOMETHING for the door conversation (silent no-op guard)")
	return cap.captured()
}

// doorACPHandler is the minimal jsonrpc.Handler for this test's ACP client
// connection: it only needs to observe notifications well enough to let the
// test proceed (no permission requests are ever forwarded by doorBackend, so
// any inbound request would be a genuine surprise).
type doorACPHandler struct {
	t *testing.T
}

func (h *doorACPHandler) HandleRequest(_ context.Context, method string, _ json.RawMessage, reply func(any, *jsonrpc.Error)) {
	h.t.Errorf("door ACP loopback: unexpected inbound request %q (this scripted conversation never triggers one)", method)
	reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "door test client: unsupported method " + method})
}

func (h *doorACPHandler) HandleNotification(context.Context, string, json.RawMessage) {
	// session/update notifications are not inspected here: the equivalence
	// comparison taps the raw ChatEvent stream (capturingClient) instead, at
	// the SAME layer the structured-REPL path is tapped, so both readings
	// are apples to apples regardless of how acpagent renders ACP frames.
}

// runDoorViaACPLoopback drives doorMessages through a REAL acpagent.Serve
// (the exact entrypoint `ctxloom acp` calls) over an in-process io/Pipe
// transport, using a ChatOpener that opens the SAME kind of pb.Client.Chat
// conversation OpenEngineSession does — against doorBackend instead of a
// real engine + real config (see the file doc comment for exactly what this
// does and doesn't cover). harp == "" skips canonical-transcript capture.
func runDoorViaACPLoopback(t *testing.T, harp string) []agent.ChatEvent {
	t.Helper()
	var tap *capturingClient

	open := func(ctx context.Context, req acpagent.OpenRequest) (*acpagent.EngineChat, error) {
		tap = &capturingClient{Client: newDoorClient(t)}
		in, events, errs, err := tap.Chat(ctx, agent.ChatRequest{
			WorkDir:            req.Cwd,
			Model:              "door-model",
			Env:                doorEnv(harp),
			ForwardPermissions: true,
		})
		if err != nil {
			return nil, err
		}
		return &acpagent.EngineChat{
			In:     in,
			Events: events,
			Errs:   errs,
			Close:  func() {},
		}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()
	go func() { _ = acpagent.Serve(ctx, serverR, serverW, open) }()
	t.Cleanup(func() {
		_ = clientW.Close()
		_ = serverW.Close()
	})

	h := &doorACPHandler{t: t}
	conn := jsonrpc.NewConn(ctx, clientR, clientW, jsonrpc.CloserFunc(clientW.Close), h)
	t.Cleanup(func() { _ = conn.Close() })

	var initResp api.InitializeResponse
	require.NoError(t, conn.Call(ctx, api.AgentMethodInitialize,
		map[string]any{"protocolVersion": api.ProtocolVersionNumber, "clientCapabilities": map[string]any{}}, &initResp),
		"initialize")

	var newResp api.NewSessionResponse
	require.NoError(t, conn.Call(ctx, api.AgentMethodSessionNew,
		map[string]any{"cwd": t.TempDir(), "mcpServers": []any{}}, &newResp), "session/new")
	sid := newResp.SessionId
	require.NotEmpty(t, sid, "session/new must mint a real session id")

	for _, text := range doorMessages {
		var promptResp api.PromptResponse
		err := conn.Call(ctx, api.AgentMethodSessionPrompt, api.PromptRequest{
			SessionId: sid,
			Prompt:    []api.ContentBlock{api.TextBlock(text)},
		}, &promptResp)
		require.NoError(t, err, "session/prompt for %q", text)
		require.EqualValues(t, api.StopReasonEndTurn, promptResp.StopReason, "each door turn must end normally")
	}

	require.NotNil(t, tap, "the ChatOpener must have run")
	return tap.captured()
}

// TestH1bEquivalence_ChatEventSequence pins the one-door invariant at the
// ChatEvent level: run --structured and the ACP loopback, driving the
// IDENTICAL scripted conversation against the IDENTICAL hermetic backend,
// must produce IDENTICAL agent.ChatEvent sequences. A divergence here means
// one of the two frontends is not actually going through pb.Client.Chat —
// exactly the "frontend grew its own door" regression this test exists to
// catch.
func TestH1bEquivalence_ChatEventSequence(t *testing.T) {
	eventsA := runDoorViaStructuredREPL(t, "")
	eventsB := runDoorViaACPLoopback(t, "")

	require.NotEmpty(t, eventsA, "run --structured must have produced real ChatEvents (silent no-op guard)")
	require.Equal(t, len(eventsA), len(eventsB),
		"run --structured and the ACP loopback produced a DIFFERENT NUMBER of ChatEvents for the identical scripted conversation:\nA=%#v\nB=%#v", eventsA, eventsB)
	for i := range eventsA {
		assert.Equal(t, eventsA[i], eventsB[i], "ChatEvent[%d] diverged between run --structured and the ACP loopback", i)
	}
}

// TestH1bEquivalence_CanonicalTranscript pins the one-door invariant at the
// canonical-transcript level: the SAME scripted conversation, captured via
// GRPCClient.Chat's transcript tee (the "tough-cloud S2" seam), must produce
// the same recorded CONTENT regardless of which frontend drove the
// conversation — modulo harp/seq/ts (independently-recorded conversations
// legitimately differ there).
//
// FINDING (real, not a test bug — reported per the H1b brief rather than
// tuned away): a first draft of this test compared the two transcripts as
// one fully-ordered sequence and was FLAKY — record[0]'s Kind flipped between
// "session" and "entry" (a user turn) across repeated runs of the SAME
// frontend alone. Root cause, reconfirmed independently here: GRPCClient.Chat
// (internal/lm/grpc/chat.go) records a conversation's OWN user turns and the
// backend's own events via two SEPARATE, uncoordinated goroutines (the
// inbound tap vs. the outbound tee) sharing one Recorder/seq counter, with NO
// synchronization between them — already documented and accepted upstream
// (chat_test.go's TestGRPCClient_Chat_CapturesUserTurn: "Position is NOT
// asserted for the user entry relative to the Session record ... genuinely a
// race ... no observable effect on any reader"). What IS deterministic, and
// what this test actually pins: (a) the USER-turn subsequence, in the order
// the caller sent messages (a single inbound goroutine records them
// synchronously as they arrive), and (b) the ENGINE subsequence — session,
// then each turn's entry+complete — in the order the backend itself produced
// them (single outbound tee, single backend goroutine). Comparing the full
// racy interleave would either flake or require re-litigating an
// already-accepted design decision; comparing the two deterministic
// subsequences is the honest version of "identical transcripts" for a
// recorder whose OWN docs disclaim cross-stream ordering.
func TestH1bEquivalence_CanonicalTranscript(t *testing.T) {
	testsupport.Isolate(t)

	const harpA = "h1b-door-transcript-structured"
	const harpB = "h1b-door-transcript-acp-loopback"
	runDoorViaStructuredREPL(t, harpA)
	runDoorViaACPLoopback(t, harpB)

	recsA := readDoorTranscript(t, harpA)
	recsB := readDoorTranscript(t, harpB)
	require.NotEmpty(t, recsA, "run --structured must have written a real canonical transcript (silent no-op guard)")
	require.Equal(t, len(recsA), len(recsB),
		"canonical transcript RECORD COUNTS diverged between run --structured and the ACP loopback")

	userA, engineA := splitDoorRecords(recsA)
	userB, engineB := splitDoorRecords(recsB)

	require.Len(t, userA, len(doorMessages), "every door message must be recorded as a user turn")
	assertNormalizedRecordSequenceEqual(t, userA, userB, "user-turn")
	assertNormalizedRecordSequenceEqual(t, engineA, engineB, "engine")
}

// splitDoorRecords separates a transcript's USER-turn records (written by
// GRPCClient.Chat's inbound tap) from everything the BACKEND itself produced
// (session/entry/complete, written by the outbound tee) — see the test doc
// comment above for why these two subsequences, not the merged file order,
// are what this test compares.
func splitDoorRecords(recs []transcript.Record) (user, engine []transcript.Record) {
	for _, r := range recs {
		if r.Kind == transcript.KindEntry && r.Entry != nil && r.Entry.Type == "user" {
			user = append(user, r)
		} else {
			engine = append(engine, r)
		}
	}
	return user, engine
}

// assertNormalizedRecordSequenceEqual compares two record subsequences
// element-by-element (normalized for harp/seq/ts), failing loudly on any
// length or content mismatch.
func assertNormalizedRecordSequenceEqual(t *testing.T, a, b []transcript.Record, label string) {
	t.Helper()
	require.Equal(t, len(a), len(b), "%s record COUNT diverged between run --structured and the ACP loopback", label)
	for i := range a {
		assert.Equal(t, normalizeDoorRecord(a[i]), normalizeDoorRecord(b[i]),
			"%s record[%d] diverged between run --structured and the ACP loopback", label, i)
	}
}

// readDoorTranscript reads harp's canonical transcript.acp.jsonl into
// transcript.Record values, in file order.
func readDoorTranscript(t *testing.T, harp string) []transcript.Record {
	t.Helper()
	path, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var recs []transcript.Record
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var r transcript.Record
		require.NoError(t, json.Unmarshal([]byte(line), &r))
		recs = append(recs, r)
	}
	return recs
}

// normalizeDoorRecord zeroes the fields that legitimately differ between two
// independently-recorded conversations (harp/seq/ts — exactly the "modulo
// ts/harp/seq" the task specifies) so the comparison is on CONTENT.
func normalizeDoorRecord(r transcript.Record) transcript.Record {
	r.Harp = ""
	r.Seq = 0
	r.TS = time.Time{}
	return r
}
