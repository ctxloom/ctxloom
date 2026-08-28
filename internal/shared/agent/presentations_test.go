package agent

import (
	"path/filepath"
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
	fakeEngine           = "fakeengine"
	fakeUnsafeFileName   = "unsafe-file"
	fakeSystemPromptName = "system-prompt"
	fakeHookName         = "hook"

	fakeProjectRoot      = "/proj"
	fakeEngineHomeHost   = "/elsewhere/home"
	fakeEngineHomeTarget = "/in-container/home"

	fakeSystemPromptRel  = "context.md"
	fakeSystemPromptFlag = "--append-system-prompt-file"
)

// Each presenter builds a DISTINGUISHABLE presentation, which is what lets a
// test say which one resolution actually chose rather than merely that it
// returned something.
//
// Each also takes its root FROM THE INPUTS rather than closing over one. That
// is the behaviour under test as much as it is a convenience: a presenter that
// captured a root at declaration time is exactly what PresentInputs exists to
// make unnecessary.
func fakeUnsafeFilePresenter(in PresentInputs) present.Presentation {
	return present.New(in.ProjectRoot).
		WithProjectRootFile(present.ProjectRootFile{Rel: "FAKE.md"}).
		Build()
}

// fakeSystemPromptPresenter is the RELOCATED-HOME case, and the reason the
// seam takes more than a project root. It roots on the engine home the host
// materialized, remaps it to where the engine actually sees it, and names the
// remapped path on argv — so it consumes both home roots and would be
// unwritable against a start pre-rooted at the project root.
func fakeSystemPromptPresenter(in PresentInputs) present.Presentation {
	return present.New(in.ProjectRoot).
		WithEnvDirFile(
			present.EnvDirFile{EnvVar: "FAKE_HOME", HomeDefault: ".fake", Rel: fakeSystemPromptRel},
			in.EngineHomeHost,
		).
		WithContainerMount(in.EngineHomeTarget).
		WithFlag(present.FlagValue{Flag: fakeSystemPromptFlag}).
		Build()
}

func fakeHookPresenter(in PresentInputs) present.Presentation {
	return present.New(in.ProjectRoot).
		WithProjectRootFile(present.ProjectRootFile{Rel: ".fake/hook.json"}).
		Build()
}

// fakeContextPresentations is the declaration under test: a default plus two
// alternatives, exactly the shape a real engine will write in S4-S7.
//
// It is a package-level literal in all but syntax — it closes over NOTHING and
// takes no environment, which is what keeps Names and Default answerable
// before anything is resolved.
func fakeContextPresentations() Presentations {
	return Presents(fakeEngine, SurfaceContext, fakeUnsafeFileName, fakeUnsafeFilePresenter).
		Or(fakeSystemPromptName, fakeSystemPromptPresenter).
		Or(fakeHookName, fakeHookPresenter)
}

// fakeInputs is a fully-remapped environment: the engine home is materialized
// at one path and seen by the engine at a DIFFERENT one. They differ on
// purpose — equal values would let a presenter that read the wrong field pass.
func fakeInputs() PresentInputs {
	return PresentInputs{
		ProjectRoot:      fakeProjectRoot,
		EngineHomeHost:   fakeEngineHomeHost,
		EngineHomeTarget: fakeEngineHomeTarget,
	}
}

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
		{fakeUnsafeFileName, fakeUnsafeFilePresenter(fakeInputs())},
		{fakeSystemPromptName, fakeSystemPromptPresenter(fakeInputs())},
		{fakeHookName, fakeHookPresenter(fakeInputs())},
	} {
		got := d.Resolve(tc.name, fakeInputs())
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

// Every declared root must reach the presenter that builds from it, and reach
// it UNSWAPPED.
//
// Asserted against LITERAL paths rather than against a second call to the same
// presenter: deriving the expectation from the presenter would agree with
// itself no matter which field the presenter read, which is the shape that
// passes while proving nothing.
func TestPresentations_Resolve_DeliversEachRootToThePresenterThatBuildsFromIt(t *testing.T) {
	arm(t, false)
	d := fakeContextPresentations()

	// The project root reaches a presenter that roots on it.
	if got, want := d.Resolve(fakeUnsafeFileName, fakeInputs()).HostPath,
		filepath.Join(fakeProjectRoot, "FAKE.md"); got != want {
		t.Errorf("project-rooted host path = %q, want %q", got, want)
	}

	got := d.Resolve(fakeSystemPromptName, fakeInputs())

	// The bytes are written where the HOST materialized the engine home.
	if want := filepath.Join(fakeEngineHomeHost, fakeSystemPromptRel); got.HostPath != want {
		t.Errorf("relocated-home host path = %q, want %q (EngineHomeHost)", got.HostPath, want)
	}

	// The engine sees them at the MOUNTED home — never at the host location.
	wantEngine := filepath.Join(fakeEngineHomeTarget, fakeSystemPromptRel)
	if got.EnginePath != wantEngine {
		t.Errorf("relocated-home engine path = %q, want %q (EngineHomeTarget)", got.EnginePath, wantEngine)
	}

	// argv must quote the ENGINE path. This is the assertion that goes red if
	// the target root is dropped and the host value is handed over in its
	// place: the launch would otherwise name a path that does not exist inside
	// the engine's namespace.
	if want := fakeSystemPromptFlag + " " + wantEngine; strings.Join(got.Args, " ") != want {
		t.Errorf("argv = %q, want %q", strings.Join(got.Args, " "), want)
	}

	// The mount carries BOTH sides, so neither root may stand in for the other.
	if len(got.Mounts) != 1 {
		t.Fatalf("mounts = %+v, want exactly 1", got.Mounts)
	}
	if m := got.Mounts[0]; m.HostDir != fakeEngineHomeHost || m.TargetDir != fakeEngineHomeTarget {
		t.Errorf("mount = %+v, want {HostDir:%q TargetDir:%q}",
			m, fakeEngineHomeHost, fakeEngineHomeTarget)
	}
}

// The unmapped-home rule: with nothing remapping the home, the engine sees it
// where the host put it. Resolve must supply that, so an empty target never
// reaches a presenter.
//
// Left unhandled this fails SILENTLY rather than loudly: an empty root leaves
// the joined path RELATIVE ("context.md"), not obviously broken, so it reaches
// argv looking well-formed and the engine resolves it against whatever working
// directory it has. Removing the rule is what this test's expectations are
// written against — the mutation that deletes it reports exactly that bare
// relative path.
func TestPresentations_Resolve_UnmappedEngineHome_TargetFallsBackToHost(t *testing.T) {
	arm(t, false)
	d := fakeContextPresentations()

	in := fakeInputs()
	in.EngineHomeTarget = "" // a host run: nothing remaps the home

	got := d.Resolve(fakeSystemPromptName, in)

	want := filepath.Join(fakeEngineHomeHost, fakeSystemPromptRel)
	if got.HostPath != want {
		t.Errorf("host path = %q, want %q", got.HostPath, want)
	}
	if got.EnginePath != want {
		t.Errorf("unmapped engine path = %q, want the host location %q", got.EnginePath, want)
	}
	if got.EnginePath != got.HostPath {
		t.Errorf("unmapped run: engine path %q and host path %q must agree",
			got.EnginePath, got.HostPath)
	}
	if wantArgs := fakeSystemPromptFlag + " " + want; strings.Join(got.Args, " ") != wantArgs {
		t.Errorf("argv = %q, want %q", strings.Join(got.Args, " "), wantArgs)
	}
	if len(got.Mounts) != 1 || got.Mounts[0].TargetDir != fakeEngineHomeHost {
		t.Errorf("mounts = %+v, want the target to be the host home %q", got.Mounts, fakeEngineHomeHost)
	}
}

// The STRICT arm: an unknown name must REFUSE. Asserted on the gate's own
// verdict, not on message text.
func TestPresentations_UnknownName_Strict_RefusesAndFallsBackToDefault(t *testing.T) {
	mark := arm(t, false)
	d := fakeContextPresentations()

	got := d.Resolve("no-such-delivery", fakeInputs())

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

	got := d.Resolve("no-such-delivery", fakeInputs())

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
	want := d.Resolve(d.Default(), fakeInputs())
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
		if p := d.Resolve(name, fakeInputs()); p.HostPath == "" {
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

// Names and Default must be answerable with NO environment at all — no roots,
// nothing constructed. Help text and shell completion call them before
// anything is resolved, and a declaration that needed placeholder values to
// enumerate itself would have to be built per-invocation instead of once.
func TestPresentations_NamesAndDefault_AreAnswerableWithoutAnyEnvironment(t *testing.T) {
	mark := arm(t, false)
	d := fakeContextPresentations()

	if got := len(d.Names()); got != 3 {
		t.Errorf("Names() returned %d entries without an environment, want 3", got)
	}
	if d.Default() != fakeUnsafeFileName {
		t.Errorf("Default() = %q, want %q", d.Default(), fakeUnsafeFileName)
	}

	// Enumerating is not resolving: it must not construct anything, and so must
	// not be able to record a fault.
	if found := strictness.Since(mark); len(found) != 0 {
		t.Errorf("enumerating recorded %d finding(s), want 0: %+v", len(found), found)
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
