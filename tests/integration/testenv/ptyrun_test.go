package testenv

import (
	"strings"
	"testing"
	"time"
)

func containsText(sub string) func(string) bool {
	return func(s string) bool { return strings.Contains(s, sub) }
}

// TestPtyCaptureAwaitWakesOnTheWriteThatSatisfiesIt: a wait carries no
// deadline of its own (expired is nil) and still returns, because the write
// that makes cond true is the event that wakes it.
func TestPtyCaptureAwaitWakesOnTheWriteThatSatisfiesIt(t *testing.T) {
	c := &ptyCapture{}
	type result struct {
		out string
		ok  bool
	}
	done := make(chan result, 1)
	go func() {
		out, ok := c.await(nil, containsText("ready"))
		done <- result{out, ok}
	}()

	_, _ = c.Write([]byte("not yet "))
	_, _ = c.Write([]byte("ready"))

	r := <-done
	if !r.ok || !strings.Contains(r.out, "not yet ready") {
		t.Fatalf("await = (%q, %v), want the satisfying output and true", r.out, r.ok)
	}
}

// TestPtyCaptureAwaitReportsWhatArrivedWhenExpired: an expired wait reports
// false with everything captured, so the failure can show what it waited on.
func TestPtyCaptureAwaitReportsWhatArrivedWhenExpired(t *testing.T) {
	c := &ptyCapture{}
	_, _ = c.Write([]byte("partial screen"))
	expired := make(chan time.Time)
	close(expired)

	out, ok := c.await(expired, containsText("never written"))
	if ok || out != "partial screen" {
		t.Fatalf("await = (%q, %v), want (%q, false)", out, ok, "partial screen")
	}
}
