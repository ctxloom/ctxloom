package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"

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

	fakeProjectRoot = "/proj"
	fakeEngineHome  = "/elsewhere/home"

	fakeSystemPromptRel  = "context.md"
	fakeSystemPromptFlag = "--append-system-prompt-file"
)

// Each presenter builds a DISTINGUISHABLE presentation, which is what lets a
// test say which one resolution actually chose rather than merely that it
// returned something.
//
// Each also takes its root FROM THE ADVISED START rather than closing over
// one. That is the behaviour under test as much as it is a convenience: a
// presenter that captured a root at declaration time is exactly what handing
// over a present.Start exists to make unnecessary.
func fakeUnsafeFilePresenter(s present.Start) present.Presentation {
	return s.UnderProjectRoot("FAKE.md").Build()
}

// fakeSystemPromptPresenter is the RELOCATED-HOME case, and the reason the
// seam takes more than a project root: it roots on the engine home rather than
// on the project, which a start pre-rooted at the project root cannot express.
//
// It does NOT containerize. Where a containerized engine sees this file was
// already decided before this presenter ran — present.Start already carries
// the advised roots — so this presenter neither knows nor asks whether it
// did.
func fakeSystemPromptPresenter(s present.Start) present.Presentation {
	return s.UnderEngineHome(fakeSystemPromptRel).
		AnnounceEnv("FAKE_HOME").
		AnnounceFlag(fakeSystemPromptFlag).
		Build()
}

func fakeHookPresenter(s present.Start) present.Presentation {
	return s.UnderProjectRoot(".fake/hook.json").Build()
}

// fakeApproach lifts a presenter into an Approach whose Deliver writes
// nothing: only the PRESENTATION is under test here. constructed counts how
// many times the Construct ran, so a test can prove enumeration built nothing.
type fakeApproach struct {
	present func(present.Start) present.Presentation
}

func (f fakeApproach) Present(s present.Start) present.Presentation { return f.present(s) }
func (fakeApproach) Deliver(present.Start) (Delivered, error)       { return nil, nil }

var constructed int

func fakeConstruct(presenter func(present.Start) present.Presentation) Construct {
	return func(SurfaceInputs, afero.Fs) Approach {
		constructed++
		return fakeApproach{present: presenter}
	}
}

// resolve constructs the named approach and presents it against start —
// the two halves of what a single Resolve used to do, now separable because
// construction takes content and presentation takes roots.
func resolve(t *testing.T, d Presentations, name string, start present.Start) present.Presentation {
	t.Helper()
	a, ok := d.Construct(name, SurfaceInputs{}, nil)
	if !ok {
		t.Fatalf("Construct(%q) reported the name undeclared", name)
	}
	return a.Present(start)
}

// fakeContextPresentations is the declaration under test: a default plus two
// alternatives, exactly the shape a real engine will write in S4-S7.
//
// It is a package-level literal in all but syntax — it closes over NOTHING and
// takes no environment, which is what keeps Names and Default answerable
// before anything is resolved.
func fakeContextPresentations() Presentations {
	return Presents(fakeEngine, SurfaceContext, fakeUnsafeFileName, fakeConstruct(fakeUnsafeFilePresenter)).
		Or(fakeSystemPromptName, fakeConstruct(fakeSystemPromptPresenter)).
		Or(fakeHookName, fakeConstruct(fakeHookPresenter))
}

// fakeStart is an advised composition, host-transported (uncontainerized).
// The two roots are DIFFERENT values on purpose — equal ones would let a
// presenter that read the wrong field pass.
func fakeStart() present.Start {
	return present.New(present.OnHost(present.Paths{
		ProjectRoot: present.Root{Host: fakeProjectRoot},
		EngineHome:  present.Root{Host: fakeEngineHome},
	}))
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
		{fakeUnsafeFileName, fakeUnsafeFilePresenter(fakeStart())},
		{fakeSystemPromptName, fakeSystemPromptPresenter(fakeStart())},
		{fakeHookName, fakeHookPresenter(fakeStart())},
	} {
		got := resolve(t, d, tc.name, fakeStart())
		if got.HostPath != tc.want.HostPath {
			t.Errorf("Construct(%q).Present host path = %q, want %q", tc.name, got.HostPath, tc.want.HostPath)
		}
		if got.EnginePath != tc.want.EnginePath {
			t.Errorf("Construct(%q).Present engine path = %q, want %q", tc.name, got.EnginePath, tc.want.EnginePath)
		}
		if strings.Join(got.Args, " ") != strings.Join(tc.want.Args, " ") {
			t.Errorf("Construct(%q).Present args = %v, want %v", tc.name, got.Args, tc.want.Args)
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
// it UNSWAPPED. Two roots in one struct is exactly what makes handing over the
// wrong one possible, and the mistake is silent: both are plausible absolute
// paths, so the wrong one produces a well-formed argv naming a file that is not
// there.
//
// Asserted against LITERAL paths rather than against a second call to the same
// presenter: deriving the expectation from the presenter would agree with
// itself no matter which field the presenter read, which is the shape that
// passes while proving nothing.
func TestPresentations_Resolve_DeliversEachRootToThePresenterThatBuildsFromIt(t *testing.T) {
	arm(t, false)
	d := fakeContextPresentations()

	// The PROJECT ROOT reaches the presenter that roots on the project.
	if got, want := resolve(t, d, fakeUnsafeFileName, fakeStart()).HostPath,
		filepath.Join(fakeProjectRoot, "FAKE.md"); got != want {
		t.Errorf("project-rooted host path = %q, want %q", got, want)
	}

	// The ENGINE HOME reaches the presenter that roots on the relocated home —
	// NOT the project root, which is the confusion two roots make possible.
	got := resolve(t, d, fakeSystemPromptName, fakeStart())
	want := filepath.Join(fakeEngineHome, fakeSystemPromptRel)
	if got.HostPath != want {
		t.Errorf("relocated-home host path = %q, want %q (EngineHome)", got.HostPath, want)
	}
	if strings.HasPrefix(got.HostPath, fakeProjectRoot) {
		t.Errorf("relocated-home surface was rooted at the PROJECT root: %q", got.HostPath)
	}

	// The engine home is also what the engine is TOLD, via its own variable.
	if got.Env["FAKE_HOME"] != fakeEngineHome {
		t.Errorf("engine home variable = %q, want %q", got.Env["FAKE_HOME"], fakeEngineHome)
	}

	// Nothing here mounts, so the engine sees the file where it was written and
	// argv says so. Remapping is the chain's job and no input asks for it.
	if got.EnginePath != got.HostPath {
		t.Errorf("unmapped composition: engine path %q must equal host path %q",
			got.EnginePath, got.HostPath)
	}
	if wantArgs := fakeSystemPromptFlag + " " + want; strings.Join(got.Args, " ") != wantArgs {
		t.Errorf("argv = %q, want %q", strings.Join(got.Args, " "), wantArgs)
	}
}

// An unknown name is NOT constructible, and Construct says so with a plain
// false rather than a finding or a fallback: the CALLER names the failure
// (Build errors loudly; a config loader raises its own ClassConfig finding).
// Nothing is constructed and nothing is recorded — a lookup that silently
// substituted the default would deliver a presentation the caller did not ask
// for, which is the substitution this seam exists to refuse.
func TestPresentations_UnknownName_IsNotConstructible(t *testing.T) {
	mark := arm(t, false)
	d := fakeContextPresentations()
	before := constructed

	a, ok := d.Construct("no-such-delivery", SurfaceInputs{}, nil)

	if ok {
		t.Fatal("Construct reported an undeclared name as constructible")
	}
	if a != nil {
		t.Errorf("Construct handed back an approach for an undeclared name: %#v", a)
	}
	if constructed != before {
		t.Errorf("an undeclared name ran a constructor (%d → %d): the default was substituted", before, constructed)
	}
	if found := strictness.Since(mark); len(found) != 0 {
		t.Errorf("Construct recorded %d finding(s), want 0 — the caller owns the failure: %+v", len(found), found)
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

	// Every declared name must construct and present: that is what makes
	// Names() a promise rather than an advertisement.
	for _, name := range got {
		if p := resolve(t, d, name, fakeStart()); p.HostPath == "" {
			t.Errorf("Names() lists %q but resolving it built nothing", name)
		}
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
	before := constructed

	if got := len(d.Names()); got != 3 {
		t.Errorf("Names() returned %d entries without an environment, want 3", got)
	}
	if d.Default() != fakeUnsafeFileName {
		t.Errorf("Default() = %q, want %q", d.Default(), fakeUnsafeFileName)
	}

	// Enumerating is not constructing: it must not construct anything, and so
	// must not be able to record a fault.
	if constructed != before {
		t.Errorf("enumerating ran %d constructor(s); Names/Default must read the declaration only", constructed-before)
	}
	if found := strictness.Since(mark); len(found) != 0 {
		t.Errorf("enumerating recorded %d finding(s), want 0: %+v", len(found), found)
	}
}

// Or must not mutate the receiver through a shared map. Declarations are built
// once and read by whatever launches; an alias that could gain deliveries
// would let one engine's declaration alter another's.
func TestPresentations_Or_DoesNotMutateTheReceiver(t *testing.T) {
	base := Presents(fakeEngine, SurfaceContext, fakeUnsafeFileName, fakeConstruct(fakeUnsafeFilePresenter))
	extended := base.Or(fakeHookName, fakeConstruct(fakeHookPresenter))

	if got := base.Names(); len(got) != 1 {
		t.Errorf("Or mutated the receiver: base Names() = %v, want just %q", got, fakeUnsafeFileName)
	}
	if got := extended.Names(); len(got) != 2 {
		t.Errorf("extended Names() = %v, want 2 entries", got)
	}
}
