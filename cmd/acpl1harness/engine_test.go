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
