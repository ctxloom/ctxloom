package agent

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent/present"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// A FAKE engine composer. It stands in for a real engine deliberately: the
// seam's contract is that an engine declares its own presentations in its own
// package, so a test that reached for claude's declaration would be testing
// claude rather than the seam — and would go red whenever claude changed.
//
// The three names are the PERSISTED USER VOCABULARY. Users type them and
// agent-binding files on disk hold them, so they are spelled here exactly as
// they are spelled on disk.
const (
	fakeEngine            = "fakeengine"
	fakeUnsafeFileName    = "unsafe-file"
	fakeSystemPromptName  = "system-prompt"
	fakeHookName          = "hook"
	fakeProjectRoot       = "/proj"
	fakeRelocatedHomeRoot = "/elsewhere/home"
)

// Each presenter builds a DISTINGUISHABLE presentation, which is what lets a
// test say which one resolution actually chose rather than merely that it
// returned something.
func fakeUnsafeFilePresenter(s present.Start) present.Presentation {
	return s.WithProjectRootFile(present.ProjectRootFile{Rel: "FAKE.md"}).Build()
}

func fakeSystemPromptPresenter(s present.Start) present.Presentation {
	return s.WithEnvDirFile(
		present.EnvDirFile{EnvVar: "FAKE_HOME", HomeDefault: ".fake", Rel: "context.md"},
		fakeRelocatedHomeRoot,
	).WithFlag(present.FlagValue{Flag: "--append-system-prompt-file"}).Build()
}

func fakeHookPresenter(s present.Start) present.Presentation {
	return s.WithProjectRootFile(present.ProjectRootFile{Rel: ".fake/hook.json"}).Build()
}

// fakeContextPresentations is the declaration under test: a default plus two
// alternatives, exactly the shape a real engine will write in S4-S7.
func fakeContextPresentations() Presentations {
	return Presents(fakeEngine, SurfaceContext, fakeUnsafeFileName, fakeUnsafeFilePresenter).
		Or(fakeSystemPromptName, fakeSystemPromptPresenter).
		Or(fakeHookName, fakeHookPresenter)
}

func fakeStart() present.Start { return present.New(fakeProjectRoot) }

// arm installs a strictness mode for one test and guarantees the process-wide
// state is restored. Findings, the FailOnce dedup set and the checkpoint
// generation are all process-global, so a test that skipped the Reset could
// pass on a finding a PREVIOUS test recorded.
func arm(t *testing.T, degraded bool) strictness.Mark {
	t.Helper()
	prev := strictness.Degraded()
	strictness.Reset()
	strictness.SetDegraded(degraded)
	t.Cleanup(func() {
		strictness.SetDegraded(prev)
		strictness.Reset()
	})
	return strictness.Checkpoint()
}

func TestPresentations_DeclaredName_BuildsThatPresentationWithoutFinding(t *testing.T) {
	mark := arm(t, false)
	d := fakeContextPresentations()

	for _, tc := range []struct {
		name string
		want present.Presentation
	}{
		{fakeUnsafeFileName, fakeUnsafeFilePresenter(fakeStart())},
		{fakeSystemPromptName, fakeSystemPromptPresenter(fakeStart())},
		{fakeHookName, fakeHookPresenter(fakeStart())},
	} {
		got := d.Resolve(tc.name, fakeStart())
		if got.HostPath != tc.want.HostPath {
			t.Errorf("Resolve(%q) host path = %q, want %q", tc.name, got.HostPath, tc.want.HostPath)
		}
		if got.EnginePath != tc.want.EnginePath {
			t.Errorf("Resolve(%q) engine path = %q, want %q", tc.name, got.EnginePath, tc.want.EnginePath)
		}
		if strings.Join(got.Args, " ") != strings.Join(tc.want.Args, " ") {
			t.Errorf("Resolve(%q) args = %v, want %v", tc.name, got.Args, tc.want.Args)
		}
	}

	// A name the engine declares is not a fault, so nothing may be recorded —
	// otherwise every ordinary launch would poison the startup gate.
	if found := strictness.Since(mark); len(found) != 0 {
		t.Errorf("resolving declared names recorded %d finding(s), want 0: %+v", len(found), found)
	}
	if err := strictness.FindingsError(mark); err != nil {
		t.Errorf("resolving declared names refused the launch: %v", err)
	}
}

// The STRICT arm: an unknown name must REFUSE. Asserted on the gate's own
// verdict, not on message text.
func TestPresentations_UnknownName_Strict_RefusesAndFallsBackToDefault(t *testing.T) {
	mark := arm(t, false)
	d := fakeContextPresentations()

	got := d.Resolve("no-such-delivery", fakeStart())

	if err := strictness.FindingsError(mark); err == nil {
		t.Fatal("unknown delivery did not refuse the launch in strict mode; want a fatal finding")
	}

	found := strictness.Since(mark)
	if len(found) != 1 {
		t.Fatalf("recorded %d finding(s), want exactly 1: %+v", len(found), found)
	}
	f := found[0]

	// The CLASS decides fatality and degradability. A different class would
	// route this fault through a different gate.
	if f.Class != strictness.ClassConfig {
		t.Errorf("finding class = %q, want %q", f.Class, strictness.ClassConfig)
	}
	// Launching with a different presentation is not itself the harm, so this
	// fault must stay degradable. FailAlways here would break the promise that
	// --degraded always reaches a working LLM.
	if f.NonDegradable {
		t.Error("finding is NonDegradable; an unknown delivery name must yield to --degraded")
	}
	// The remedy is the whole user interface for this failure, and it is
	// useless unless it names what the user may actually type. Derived from
	// the declaration so it cannot be satisfied by a stale hardcoded list.
	if strings.TrimSpace(f.FixIt) == "" {
		t.Error("finding carries no remedy; the refusal must say how to fix it")
	}
	for _, name := range d.Names() {
		if !strings.Contains(f.FixIt, name) {
			t.Errorf("remedy %q does not name the known value %q", f.FixIt, name)
		}
	}

	assertIsDefault(t, d, got)
}

// The DEGRADED arm: the same bad input must WARN and let the launch proceed,
// landing on the engine's declared default.
func TestPresentations_UnknownName_Degraded_ContinuesOnEngineDefault(t *testing.T) {
	mark := arm(t, true)
	d := fakeContextPresentations()

	got := d.Resolve("no-such-delivery", fakeStart())

	// The promise: degraded mode always reaches a working LLM. A refusal here
	// would break it for every existing user.
	if err := strictness.FindingsError(mark); err != nil {
		t.Fatalf("degraded mode refused the launch over an unknown delivery: %v", err)
	}
	// Warned, not silent: the finding is still recorded, degraded only
	// suppresses its fatality.
	if found := strictness.Since(mark); len(found) != 1 {
		t.Errorf("degraded mode recorded %d finding(s), want exactly 1: %+v", len(found), found)
	}

	assertIsDefault(t, d, got)
}

// assertIsDefault pins WHICH presentation the fallback produced — the default's
// own bytes, not merely a non-empty result. Derived by invoking the declared
// default, so it follows the declaration rather than a literal.
func assertIsDefault(t *testing.T, d Presentations, got present.Presentation) {
	t.Helper()
	want := d.Resolve(d.Default(), fakeStart())
	if got.HostPath != want.HostPath {
		t.Errorf("fallback host path = %q, want the engine default %q (%q)",
			got.HostPath, want.HostPath, d.Default())
	}
	if got.EnginePath != want.EnginePath {
		t.Errorf("fallback engine path = %q, want the engine default %q", got.EnginePath, want.EnginePath)
	}
	if strings.Join(got.Args, " ") != strings.Join(want.Args, " ") {
		t.Errorf("fallback args = %v, want the engine default %v", got.Args, want.Args)
	}
}

// The vocabulary must be DERIVED from the registered presenters. A separately
// declared list could claim support the engine cannot construct — the exact
// disagreement the capability-list-plus-construction-map shape allowed.
func TestPresentations_Names_AreDerivedFromDeclaredPresenters(t *testing.T) {
	d := fakeContextPresentations()

	got := d.Names()
	want := []string{fakeHookName, fakeSystemPromptName, fakeUnsafeFileName} // sorted
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Names() = %v, want %v", got, want)
	}

	// Every declared name must resolve without a finding: that is what makes
	// Names() a promise rather than an advertisement.
	mark := arm(t, false)
	for _, name := range got {
		if p := d.Resolve(name, fakeStart()); p.HostPath == "" {
			t.Errorf("Names() lists %q but resolving it built nothing", name)
		}
	}
	if found := strictness.Since(mark); len(found) != 0 {
		t.Errorf("a name from Names() was rejected by Resolve: %+v", found)
	}

	if d.Default() != fakeUnsafeFileName {
		t.Errorf("Default() = %q, want %q", d.Default(), fakeUnsafeFileName)
	}
}

// Or must not mutate the receiver through a shared map. Declarations are built
// once and read by whatever launches; an alias that could gain deliveries
// would let one engine's declaration alter another's.
func TestPresentations_Or_DoesNotMutateTheReceiver(t *testing.T) {
	base := Presents(fakeEngine, SurfaceContext, fakeUnsafeFileName, fakeUnsafeFilePresenter)
	extended := base.Or(fakeHookName, fakeHookPresenter)

	if got := base.Names(); len(got) != 1 {
		t.Errorf("Or mutated the receiver: base Names() = %v, want just %q", got, fakeUnsafeFileName)
	}
	if got := extended.Names(); len(got) != 2 {
		t.Errorf("extended Names() = %v, want 2 entries", got)
	}
}
