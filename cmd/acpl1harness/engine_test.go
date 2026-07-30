package main

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestOpenHarnessEngine_CloseWhileEngineParkedOnEmit pins U001-F01: tearing a
// session down must never race the scripting goroutine's send.
//
// The engine goroutine parks in emit's `select { case events <- ev:
// case <-ctx.Done(): }` as soon as the events buffer fills and nothing drains
// it. Closing that same channel from the teardown path wakes every parked
// sender with a "send on closed channel" PANIC — an unrecoverable crash of the
// L1 harness process, which the driving suite would report as an unrelated
// conformance failure. Close must therefore wait for the scripting goroutine
// to have exited before it closes the channel that goroutine sends on.
func TestOpenHarnessEngine_CloseWhileEngineParkedOnEmit(t *testing.T) {
	for range 20 {
		chat, err := openHarnessEngine(context.Background(), operations.OpenRequest{})
		if err != nil {
			t.Fatalf("openHarnessEngine: %v", err)
		}
		// Feed turns from a goroutine: `in` is small and the engine stops
		// reading it the moment it parks, so a synchronous producer would
		// block here instead.
		feed := make(chan struct{})
		go func() {
			defer close(feed)
			for range 24 {
				select {
				case chat.In <- agent.ChatMessage{Text: "fill"}:
				case <-time.After(2 * time.Second):
					return
				}
			}
		}()

		if !waitForFullEvents(chat.Events) {
			chat.Close()
			<-feed
			t.Fatal("events buffer never filled: the engine goroutine never parked in emit, so this test proves nothing")
		}
		chat.Close()
		<-feed
	}
}

// waitForFullEvents blocks until the events channel is at capacity — the state
// in which the scripting goroutine is provably parked inside emit's select.
func waitForFullEvents(events <-chan agent.ChatEvent) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(events) == cap(events) {
			// One more yield so the goroutine that filled the last slot has
			// re-entered emit and parked on the next send.
			runtime.Gosched()
			return true
		}
		runtime.Gosched()
	}
	return false
}

// TestOpenHarnessEngine_ConcurrentCloseIsIdempotent pins U001-F02: EngineChat's
// documented contract is "Close tears the engine conversation down
// (idempotent)". A select/default guard around close() is a non-atomic
// imitation of sync.Once — two callers can both observe the default arm and
// both close, panicking on "close of closed channel". Idempotence has to hold
// under concurrency, not merely under sequential repetition.
//
// Measured against the pre-sync.Once guard: this loop panics "close of closed
// channel" under the default GOMAXPROCS and is GREEN at GOMAXPROCS=1 — i.e. a
// genuine race, not a latent logic error. The harness's only consumer today
// (acpagent's closeAllSessions, a single goroutine calling Close once per
// session over a snapshot taken under lock) never exercises it, so this test is
// the only thing standing between the contract and a second caller appearing.
func TestOpenHarnessEngine_ConcurrentCloseIsIdempotent(t *testing.T) {
	for range 50 {
		chat, err := openHarnessEngine(context.Background(), operations.OpenRequest{})
		if err != nil {
			t.Fatalf("openHarnessEngine: %v", err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				chat.Close()
			}()
		}
		close(start)
		wg.Wait()
	}
}

// TestOpenHarnessEngine_ResumeReplaysFixedHistory characterizes the one
// OpenRequest field the harness does read (U001-F06): a resumed harp yields the
// fixed two-entry replay, a fresh session yields none and a generated harp.
func TestOpenHarnessEngine_ResumeReplaysFixedHistory(t *testing.T) {
	resumed, err := openHarnessEngine(context.Background(), operations.OpenRequest{ResumeHarp: "some-recorded-harp"})
	if err != nil {
		t.Fatalf("openHarnessEngine: %v", err)
	}
	defer resumed.Close()
	if resumed.Harp != "some-recorded-harp" {
		t.Errorf("resumed harp = %q, want the requested harp echoed back", resumed.Harp)
	}
	if len(resumed.Replay) != 2 || resumed.Replay[0].Content != replayUser || resumed.Replay[1].Content != replayAssistant {
		t.Errorf("resumed replay = %+v, want the fixed two-entry history", resumed.Replay)
	}

	fresh, err := openHarnessEngine(context.Background(), operations.OpenRequest{ResumeHarp: ""})
	if err != nil {
		t.Fatalf("openHarnessEngine: %v", err)
	}
	defer fresh.Close()
	if len(fresh.Replay) != 0 {
		t.Errorf("fresh replay = %+v, want none", fresh.Replay)
	}
	if fresh.Harp == "" {
		t.Error("fresh session must mint a connection-local harp")
	}
}

// TestRunTurn_CancelSentinelsShareOneScript pins the behaviour the two
// cancel-awaiting sentinels have in common and the single point at which they
// diverge: both announce one assistant entry, then park until a CancelTurn
// arrives (ignoring any other traffic), then complete — sentinelCancelMe with
// "cancelled", sentinelRaceComplete with "end_turn" (the engine that races the
// cancel and finishes normally anyway, which the server must still resolve as
// cancelled).
//
// This is the pin above U001-F03's collapse of the two branches onto one
// helper: it is green before and after, and red if the collapse loses either
// the shared script or the deliberate divergence.
func TestRunTurn_CancelSentinelsShareOneScript(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		wantEntry string
		wantStop  string
	}{
		{"cancel", sentinelCancelMe, "waiting for cancel", "cancelled"},
		{"race", sentinelRaceComplete, "finishing before any cancel is read", "end_turn"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chat, err := openHarnessEngine(context.Background(), operations.OpenRequest{})
			if err != nil {
				t.Fatalf("openHarnessEngine: %v", err)
			}
			defer chat.Close()

			chat.In <- agent.ChatMessage{Text: tc.text}
			ev := recvEvent(t, chat.Events)
			if ev.Entry == nil || ev.Entry.Content != tc.wantEntry {
				t.Fatalf("first event = %+v, want an assistant entry %q", ev, tc.wantEntry)
			}

			// Traffic that is not a cancel must not complete the turn.
			chat.In <- agent.ChatMessage{Text: "not a cancel"}
			select {
			case unexpected := <-chat.Events:
				t.Fatalf("non-cancel traffic completed the turn: %+v", unexpected)
			case <-time.After(50 * time.Millisecond):
			}

			chat.In <- agent.ChatMessage{CancelTurn: true}
			ev = recvEvent(t, chat.Events)
			if ev.Complete == nil || ev.Complete.StopReason != tc.wantStop {
				t.Fatalf("completion = %+v, want stop reason %q", ev, tc.wantStop)
			}
		})
	}
}

// recvEvent takes the next scripted event or fails the test.
func recvEvent(t *testing.T, events <-chan agent.ChatEvent) agent.ChatEvent {
	t.Helper()
	select {
	case ev, ok := <-events:
		if !ok {
			t.Fatal("events channel closed before the expected event")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the next scripted event")
		return agent.ChatEvent{}
	}
}

// TestRunTurn_TornDownTurnNeverCompletesWithoutItsAnswer pins U001-F04: a turn
// must never report completion having silently dropped the answer that
// completion is about.
//
// emit returns false when the session died before the event could be delivered.
// Where that answer is DISCARDED, the following completion emit still runs, and
// emit's select is genuinely nondeterministic once the context is done and the
// buffer has room — so the answer can be dropped while the "end_turn" that
// follows it is delivered. That is this codebase's characteristic failure shape:
// a successful-looking completion carrying nothing. Checking emit's result and
// returning makes the pair atomic in the only sense that matters — no
// completion without its answer.
func TestRunTurn_TornDownTurnNeverCompletesWithoutItsAnswer(t *testing.T) {
	for i := range 500 {
		ctx, cancel := context.WithCancel(context.Background())
		chat, err := openHarnessEngine(ctx, operations.OpenRequest{})
		if err != nil {
			cancel()
			t.Fatalf("openHarnessEngine: %v", err)
		}

		chat.In <- agent.ChatMessage{Text: sentinelTerminal}
		if ev := recvEvent(t, chat.Events); ev.Terminal == nil {
			cancel()
			chat.Close()
			t.Fatalf("first event = %+v, want the brokered terminal request", ev)
		}

		// Race the editor's answer against session teardown, so the turn can
		// reach its answer emit with the context ALREADY dead and the events
		// buffer still roomy — the state in which emit's select is genuinely
		// nondeterministic and a discarded false lets the completion through
		// on its own.
		go func() {
			select {
			case chat.In <- agent.ChatMessage{Terminal: &agent.TerminalResponse{ID: terminalTestID, Result: []byte(`{"terminalId":"t1"}`)}}:
			case <-time.After(time.Second):
			}
		}()
		cancel()

		// Close waits for the scripting goroutine to exit and then closes
		// events, so draining to closure sees exactly what the turn emitted.
		chat.Close()
		sawAnswer, sawCompletion := drainToClose(chat.Events)
		if sawCompletion && !sawAnswer {
			t.Fatalf("iteration %d: turn reported completion with its answer silently dropped", i)
		}
	}
}

// drainToClose reads a closed-on-teardown events channel to exhaustion,
// reporting whether an answer entry and a completion were emitted.
func drainToClose(events <-chan agent.ChatEvent) (answer, completion bool) {
	for ev := range events {
		if ev.Entry != nil {
			answer = true
		}
		if ev.Complete != nil {
			completion = true
		}
	}
	return answer, completion
}

// TestOpenHarnessEngine_IgnoresEverythingButTheResumedHarp pins U001-F06's
// refutation: the L1 harness reads exactly ONE field of the OpenRequest and
// populates exactly the protocol-level fields of the EngineChat. That is the
// design this binary's package doc states — "it never touches ctxloom config,
// credentials, or a real LLM" — not an oversight, and honoring the rest would
// require the harness to fabricate ctxloom config it deliberately has none of.
//
// The surfaces left unset are not untested; each has a named in-process test in
// internal/acpagent, which is where a fake with no protocol boundary belongs:
//
//	Modes          TestServe_SessionModes
//	Commands       TestServe_AvailableCommandsUpdate (+ the commands_test.go set)
//	LLMs           TestServe_AdvertisesLLMs
//	WatchChildren  TestServe_ChildUpdatePush, ..._NilIsANoop
//	InitSummary    TestServe_Announcement*, incl. ..._AbsentWhenUnset, which pins
//	               that an unset summary emits NO frame — the exact arrangement
//	               this harness relies on
//	MCPServers     TestServe_McpServersPassThrough
//	FsUpstreamAddr internal/acpagent/fsupstream_test.go
//
// This test is red the moment the harness starts deriving session furniture
// from the request, which is what re-opens the question.
func TestOpenHarnessEngine_IgnoresEverythingButTheResumedHarp(t *testing.T) {
	full := operations.OpenRequest{
		Cwd:             t.TempDir(),
		Profile:         "some-profile",
		MCPServers:      []agent.ChatMCPServer{{Name: "srv", Command: "true"}},
		FsUpstreamAddr:  "/tmp/does-not-exist.sock",
		ForwardTerminal: true,
	}
	chat, err := openHarnessEngine(context.Background(), full)
	if err != nil {
		t.Fatalf("openHarnessEngine: %v", err)
	}
	defer chat.Close()

	if chat.Context != "" {
		t.Errorf("Context = %q, want none: this harness tests the protocol layer, not context assembly", chat.Context)
	}
	if chat.Modes != nil {
		t.Errorf("Modes = %+v, want nil", chat.Modes)
	}
	if chat.Commands != nil {
		t.Errorf("Commands = %+v, want nil", chat.Commands)
	}
	if chat.LLMs != nil {
		t.Errorf("LLMs = %+v, want nil", chat.LLMs)
	}
	if chat.AssembleMode != nil {
		t.Error("AssembleMode set: mode switching re-assembles ctxloom context, which this harness has none of")
	}
	if chat.WatchChildren != nil {
		t.Error("WatchChildren set: the harness hosts no coordinator")
	}
	if chat.InitSummary != "" {
		t.Errorf("InitSummary = %q, want empty (TestServe_AnnouncementAbsentWhenUnset pins that this emits no frame)", chat.InitSummary)
	}
	if len(chat.Replay) != 0 {
		t.Errorf("Replay = %+v, want none for a non-resumed request", chat.Replay)
	}
}
