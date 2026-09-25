//go:build !windows

package testenv

import (
	"testing"
	"time"
)

// A payload carrying no JSON-RPC request is handed over like a shell pipe:
// written, closed, done. Holding stdin open for mcpStdinGrace waiting on an
// answer that no such reader ever sends cost every hook-payload call the
// whole grace.
func TestRunWithStdin_ANonJSONRPCPayloadIsNotHeldForTheGrace(t *testing.T) {
	e := &TestEnvironment{AppBinary: "/bin/sh", ProjectDir: t.TempDir(), HomeDir: t.TempDir()}

	start := time.Now()
	if err := e.RunWithStdin(`{"hook_event_name":"Stop"}`+"\n", "-c", "cat"); err != nil {
		t.Fatalf("RunWithStdin: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= mcpStdinGrace/2 {
		t.Errorf("RunWithStdin took %s for a reader that finished at EOF: stdin was held for the grace (%s)", elapsed, mcpStdinGrace)
	}
	if got, want := e.LastOutput(), `{"hook_event_name":"Stop"}`+"\n"; got != want {
		t.Errorf("LastOutput() = %q, want %q: the payload must reach the reader intact", got, want)
	}
}
