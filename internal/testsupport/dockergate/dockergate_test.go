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

// --- named-runtime gating (runtimes.go) -------------------------------------

// TestNamedRuntime_Unreachable_NotDeclared_Skips is the developer-laptop case
// AND the asymmetric-CI case: no host is both rootful and rootless, so a matrix
// cell for the runtime this host lacks must skip rather than fail.
func TestNamedRuntime_Unreachable_NotDeclared_Skips(t *testing.T) {
	old := requiredRuntimes
	requiredRuntimes = nil
	t.Cleanup(func() { requiredRuntimes = old })

	d, msg := NamedRuntimeDecision("podman", false, "the container cell")
	if d != Skip {
		t.Fatalf("decision = %v, want Skip", d)
	}
	if !strings.Contains(msg, "podman") || !strings.Contains(msg, "the container cell") {
		t.Fatalf("skip message names neither the runtime nor the test: %q", msg)
	}
	if !strings.Contains(msg, EnvRequireRuntimes) {
		t.Fatalf("skip message does not name the variable that would promote it: %q", msg)
	}
}

// TestNamedRuntime_Unreachable_Declared_Fails is the whole point of the second
// variable: a lane that says it covers podman and does not is red.
func TestNamedRuntime_Unreachable_Declared_Fails(t *testing.T) {
	old := requiredRuntimes
	requiredRuntimes = []string{"podman"}
	t.Cleanup(func() { requiredRuntimes = old })

	d, msg := NamedRuntimeDecision("podman", false, "the container cell")
	if d != Fail {
		t.Fatalf("decision = %v, want Fail", d)
	}
	if !strings.Contains(msg, "podman") || !strings.Contains(msg, "ran NOTHING") {
		t.Fatalf("failure message does not state what was not covered: %q", msg)
	}
}

// TestNamedRuntime_RequireDockerDoesNotPromote pins the boundary between the
// two variables. CTXLOOM_REQUIRE_DOCKER=1 asserts SOME runtime is reachable; if
// it also promoted named misses, every matrix would be permanently red on every
// host, because no host is both rootful and rootless.
func TestNamedRuntime_RequireDockerDoesNotPromote(t *testing.T) {
	oldReq, oldRT := required, requiredRuntimes
	required, requiredRuntimes = true, nil
	t.Cleanup(func() { required, requiredRuntimes = oldReq, oldRT })

	if d, _ := NamedRuntimeDecision("docker-rootful", false, "the container cell"); d != Skip {
		t.Fatalf("decision = %v, want Skip: %s=1 must not promote a NAMED runtime miss", d, EnvRequireDocker)
	}
}

// TestNamedRuntime_DeclaredAndReachable_Proceeds is the covered case.
func TestNamedRuntime_DeclaredAndReachable_Proceeds(t *testing.T) {
	old := requiredRuntimes
	requiredRuntimes = []string{"podman"}
	t.Cleanup(func() { requiredRuntimes = old })

	if d, _ := NamedRuntimeDecision("podman", true, "the container cell"); d != Proceed {
		t.Fatalf("decision = %v, want Proceed", d)
	}
}

// TestValidateRequiredRuntimes_RejectsTypo: an unreachable-by-typo name matches
// nothing, so every cell skips and the lane reports green having covered none of
// what it declared. That must be loud at the gate, not silent in the result.
func TestValidateRequiredRuntimes_RejectsTypo(t *testing.T) {
	old := requiredRuntimes
	requiredRuntimes = []string{"podmn"}
	t.Cleanup(func() { requiredRuntimes = old })

	err := ValidateRequiredRuntimes([]string{"docker-rootful", "docker-rootless", "podman"})
	if err == nil {
		t.Fatal("expected an error for an unprobeable runtime name")
	}
	if !strings.Contains(err.Error(), "podmn") || !strings.Contains(err.Error(), "podman") {
		t.Fatalf("error names neither the typo nor the vocabulary: %v", err)
	}
}

func TestValidateRequiredRuntimes_AcceptsKnown(t *testing.T) {
	old := requiredRuntimes
	requiredRuntimes = []string{"docker-rootless", "podman"}
	t.Cleanup(func() { requiredRuntimes = old })

	if err := ValidateRequiredRuntimes([]string{"docker-rootful", "docker-rootless", "podman"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseRuntimeList_TrimsAndSorts(t *testing.T) {
	got := parseRuntimeList(" podman , docker-rootful ,, ")
	want := []string{"docker-rootful", "podman"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if len(parseRuntimeList("")) != 0 {
		t.Fatal("an unset variable must declare nothing")
	}
}

// TestRuntimeDecision_MatchesRequireRuntime keeps the extracted policy and the
// testing.TB wrapper from drifting: RequireRuntime IS RuntimeDecision + Apply,
// and the godog steps that call RuntimeDecision directly must get the same rule.
func TestRuntimeDecision_MatchesRequireRuntime(t *testing.T) {
	oldReq := required
	t.Cleanup(func() { required = oldReq })

	for _, tc := range []struct {
		required  bool
		available bool
		want      Decision
	}{
		{required: false, available: true, want: Proceed},
		{required: true, available: true, want: Proceed},
		{required: false, available: false, want: Skip},
		{required: true, available: false, want: Fail},
	} {
		required = tc.required
		d, _ := RuntimeDecision(tc.available, "the widget test")
		if d != tc.want {
			t.Fatalf("required=%v available=%v: decision = %v, want %v", tc.required, tc.available, d, tc.want)
		}
		tb := &fakeTB{}
		RequireRuntime(tb, tc.available, "the widget test")
		gotApplied := Proceed
		switch {
		case tb.fatalCalled:
			gotApplied = Fail
		case tb.skipCalled:
			gotApplied = Skip
		}
		if gotApplied != tc.want {
			t.Fatalf("required=%v available=%v: RequireRuntime applied %v, want %v", tc.required, tc.available, gotApplied, tc.want)
		}
	}
}
