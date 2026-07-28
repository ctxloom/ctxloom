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

	first, ok, err := ConfirmByRepeat(fs, denied, "go test", "s.json", t0, 0, 30*time.Second)
	if err != nil {
		t.Fatalf("unexpected Save error: %v", err)
	}
	if ok || first.Allow || !strings.Contains(first.Reason, "again") {
		t.Fatalf("first denial should arm and hint, got ok=%v allow=%v reason=%q", ok, first.Allow, first.Reason)
	}
	second, ok, err := ConfirmByRepeat(fs, denied, "go test", "s.json", t0.Add(time.Second), 0, 30*time.Second)
	if err != nil {
		t.Fatalf("unexpected Save error: %v", err)
	}
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

	first, ok, err := ConfirmByRepeat(fs, denied, "go test", "s.json", t0, delay, window)
	if err != nil {
		t.Fatalf("unexpected Save error: %v", err)
	}
	if ok || first.Allow || !strings.Contains(first.Reason, "wait at least 10s") {
		t.Fatalf("first denial should announce the delay, got ok=%v allow=%v reason=%q", ok, first.Allow, first.Reason)
	}

	// Repeat 2s in (still inside the 10s delay): refused, rebuked, timer intact.
	early, ok, err := ConfirmByRepeat(fs, denied, "go test", "s.json", t0.Add(2*time.Second), delay, window)
	if err != nil {
		t.Fatalf("unexpected Save error: %v", err)
	}
	if ok || early.Allow || !strings.Contains(early.Reason, "almost immediately") {
		t.Fatalf("a repeat inside the delay should be rebuked, got ok=%v allow=%v reason=%q", ok, early.Allow, early.Reason)
	}

	// Repeat at 15s (delay elapsed, window open): honored — proving the early
	// repeat did not push the band out.
	after, ok, err := ConfirmByRepeat(fs, denied, "go test", "s.json", t0.Add(15*time.Second), delay, window)
	if err != nil {
		t.Fatalf("unexpected Save error: %v", err)
	}
	if !ok || !after.Allow {
		t.Fatalf("a repeat after the delay should be allowed, got ok=%v allow=%v", ok, after.Allow)
	}
}

// TestConfirmByRepeatReportsSaveError pins U076-F01: a Store.Save failure
// (here, an afero.Fs wrapped read-only so every write fails) must be
// reported to the caller, not silently discarded. Before the fix this arming
// call's error vanished entirely (`_ = st.Save(now)`), so a persistence
// failure converted "arm the override" into a no-op with no signal — the
// agent's next identical repeat would find nothing armed and be denied
// again, forever, while the message shown promised the opposite.
func TestConfirmByRepeatReportsSaveError(t *testing.T) {
	fs := afero.NewReadOnlyFs(afero.NewMemMapFs())
	denied := engine.Response{Allow: false, Reason: "use just test"}
	_, _, err := ConfirmByRepeat(fs, denied, "go test", "s.json", time.Unix(1_000_000, 0), 0, 30*time.Second)
	if err == nil {
		t.Fatal("a Save failure on a read-only store must be reported, not swallowed")
	}
}

// TestConfirmByRepeatTooEarlyDoesNotWrite pins U076-F07: an over-eager repeat
// (still inside the delay) must not persist changed state -- nothing armed or
// cleared changes on this path, so it must attempt no write. Proven the hard
// way: arm on a writable fs, snapshot the resulting bytes onto a SEPARATE
// read-only fs, then repeat too early against that read-only copy. If the
// too-early branch still called Store.Save (as it did before this fix), this
// would fail with a permission error, exactly like
// TestConfirmByRepeatReportsSaveError's arm-branch case above -- the only
// difference here is that nothing changed, so there is nothing to persist.
func TestConfirmByRepeatTooEarlyDoesNotWrite(t *testing.T) {
	const delay, window = 10 * time.Second, 30 * time.Second
	t0 := time.Unix(1_000_000, 0)
	denied := engine.Response{Allow: false, Reason: "use just test"}

	writable := afero.NewMemMapFs()
	if _, _, err := ConfirmByRepeat(writable, denied, "go test", "s.json", t0, delay, window); err != nil {
		t.Fatalf("arming on a writable fs: %v", err)
	}
	armed, err := afero.ReadFile(writable, "s.json")
	if err != nil {
		t.Fatalf("read back armed state: %v", err)
	}

	frozen := afero.NewMemMapFs()
	if err := afero.WriteFile(frozen, "s.json", armed, 0o644); err != nil {
		t.Fatalf("seed frozen fs: %v", err)
	}
	readOnly := afero.NewReadOnlyFs(frozen)

	_, ok, err := ConfirmByRepeat(readOnly, denied, "go test", "s.json", t0.Add(2*time.Second), delay, window)
	if ok {
		t.Fatal("a too-early repeat must not be honored")
	}
	if err != nil {
		t.Fatalf("a too-early repeat changes nothing and must not attempt a write, got: %v", err)
	}
}
