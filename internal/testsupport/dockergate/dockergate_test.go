package dockergate

import (
	"fmt"
	"strings"
	"testing"
)

// fakeTB is a minimal testing.TB double: embedding the (nil) interface
// satisfies its unexported method, and only the methods RequireRuntime/
// SkipCapability actually call (Helper, Fatalf, Skipf) are overridden.
type fakeTB struct {
	testing.TB
	fatalCalled bool
	skipCalled  bool
	msg         string
}

func (f *fakeTB) Helper() {}
func (f *fakeTB) Fatalf(format string, args ...any) {
	f.fatalCalled = true
	f.msg = fmt.Sprintf(format, args...)
}
func (f *fakeTB) Skipf(format string, args ...any) {
	f.skipCalled = true
	f.msg = fmt.Sprintf(format, args...)
}

// TestRequireRuntime_UnreachableAndRequired_Fails and its sibling below pin
// both branches directly (the package that guarantees "a skip is
// never silent" previously had zero tests proving its own two branches) —
// available is orthogonal to `required`, this test manipulates `required`
// directly rather than the real CTXLOOM_REQUIRE_DOCKER env var, since
// `required` is captured once at package init and re-reading the env var
// later has no effect.
func TestRequireRuntime_UnreachableAndRequired_Fails(t *testing.T) {
	old := required
	required = true
	t.Cleanup(func() { required = old })

	tb := &fakeTB{}
	RequireRuntime(tb, false, "the widget test")

	if !tb.fatalCalled {
		t.Fatal("expected Fatalf to be called when unreachable and required")
	}
	if tb.skipCalled {
		t.Fatal("Skipf must not be called when required")
	}
	if !strings.Contains(tb.msg, EnvRequireDocker+"=1 demands one") || !strings.Contains(tb.msg, "the widget test") {
		t.Fatalf("Fatalf message missing expected content: %q", tb.msg)
	}
}

func TestRequireRuntime_UnreachableAndNotRequired_Skips(t *testing.T) {
	old := required
	required = false
	t.Cleanup(func() { required = old })

	tb := &fakeTB{}
	RequireRuntime(tb, false, "the widget test")

	if !tb.skipCalled {
		t.Fatal("expected Skipf to be called when unreachable and not required")
	}
	if tb.fatalCalled {
		t.Fatal("Fatalf must not be called when not required")
	}
	if !strings.Contains(tb.msg, "the widget test") || !strings.Contains(tb.msg, EnvRequireDocker) {
		t.Fatalf("Skipf message missing expected content: %q", tb.msg)
	}
}

// TestRequireRuntime_Available_NeitherFailsNorSkips is the pass-through path,
// regardless of `required`.
func TestRequireRuntime_Available_NeitherFailsNorSkips(t *testing.T) {
	old := required
	required = true
	t.Cleanup(func() { required = old })

	tb := &fakeTB{}
	RequireRuntime(tb, true, "the widget test")

	if tb.fatalCalled || tb.skipCalled {
		t.Fatal("expected neither Fatalf nor Skipf when available")
	}
}

// TestSkipCapability_AlwaysSkips_NeverPromoted pins the doc's own claim:
// SkipCapability is NEVER promoted to a failure, regardless of `required`.
func TestSkipCapability_AlwaysSkips_NeverPromoted(t *testing.T) {
	old := required
	required = true
	t.Cleanup(func() { required = old })

	tb := &fakeTB{}
	SkipCapability(tb, "rootless daemon")

	if !tb.skipCalled {
		t.Fatal("expected Skipf to be called")
	}
	if tb.fatalCalled {
		t.Fatal("SkipCapability must never call Fatalf, even when required")
	}
	if !strings.Contains(tb.msg, "rootless daemon") {
		t.Fatalf("Skipf message missing the reason: %q", tb.msg)
	}
}
