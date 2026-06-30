package state

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/ltk/engine"
)

// Without a delay, the first denial arms and an immediate repeat is honored.
func TestConfirmByRepeatNoDelay(t *testing.T) {
	fs := afero.NewMemMapFs()
	denied := engine.Response{Allow: false, Reason: "use just test"}
	t0 := time.Unix(1_000_000, 0)

	first, ok := ConfirmByRepeat(fs, denied, "go test", "s.json", t0, 0, 30*time.Second)
	if ok || first.Allow || !strings.Contains(first.Reason, "again") {
		t.Fatalf("first denial should arm and hint, got ok=%v allow=%v reason=%q", ok, first.Allow, first.Reason)
	}
	second, ok := ConfirmByRepeat(fs, denied, "go test", "s.json", t0.Add(time.Second), 0, 30*time.Second)
	if !ok || !second.Allow {
		t.Fatalf("an immediate repeat with no delay should be allowed, got ok=%v allow=%v", ok, second.Allow)
	}
}

// With a delay, a repeat inside the delay is refused with a sharper rebuke and
// does not reset the timer; only a repeat after the delay (within the window) is
// honored. The clock is injected, so the test does not sleep.
func TestConfirmByRepeatDelayBand(t *testing.T) {
	fs := afero.NewMemMapFs()
	denied := engine.Response{Allow: false, Reason: "use just test"}
	t0 := time.Unix(1_000_000, 0)
	const delay, window = 10 * time.Second, 30 * time.Second

	first, ok := ConfirmByRepeat(fs, denied, "go test", "s.json", t0, delay, window)
	if ok || first.Allow || !strings.Contains(first.Reason, "wait at least 10s") {
		t.Fatalf("first denial should announce the delay, got ok=%v allow=%v reason=%q", ok, first.Allow, first.Reason)
	}

	// Repeat 2s in (still inside the 10s delay): refused, rebuked, timer intact.
	early, ok := ConfirmByRepeat(fs, denied, "go test", "s.json", t0.Add(2*time.Second), delay, window)
	if ok || early.Allow || !strings.Contains(early.Reason, "almost immediately") {
		t.Fatalf("a repeat inside the delay should be rebuked, got ok=%v allow=%v reason=%q", ok, early.Allow, early.Reason)
	}

	// Repeat at 15s (delay elapsed, window open): honored — proving the early
	// repeat did not push the band out.
	after, ok := ConfirmByRepeat(fs, denied, "go test", "s.json", t0.Add(15*time.Second), delay, window)
	if !ok || !after.Allow {
		t.Fatalf("a repeat after the delay should be allowed, got ok=%v allow=%v", ok, after.Allow)
	}
}
