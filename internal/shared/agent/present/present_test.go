package present

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// --- advice: Host and Containerize -----------------------------------------

// TestOnHost_EveryRootIsItsOwnEngineSide pins the identity advice: with no
// containerization, the engine sees exactly what the host wrote, and nothing
// is mounted — there is no runtime work to do at all.
func TestOnHost_EveryRootIsItsOwnEngineSide(t *testing.T) {
	m := OnHost(Paths{
		ProjectRoot: Root{Host: "/host/project"},
		EngineHome:  Root{Host: "/host/home"},
	})
	if got := m.Paths().ProjectRoot; got.Engine != got.Host {
		t.Fatalf("ProjectRoot: Engine %q must equal Host %q", got.Engine, got.Host)
	}
	if got := m.Paths().EngineHome; got.Engine != got.Host {
		t.Fatalf("EngineHome: Engine %q must equal Host %q", got.Engine, got.Host)
	}
	if len(m.Mounts()) != 0 {
		t.Fatalf("host transport must record no mounts: %v", m.Mounts())
	}
}

// TestOnHost_UnresolvedRootStaysZero: a root this run never resolved (Host
// == "") must not be given a fabricated engine side.
func TestOnHost_UnresolvedRootStaysZero(t *testing.T) {
	m := OnHost(Paths{ProjectRoot: Root{Host: "/host/project"}})
	if got := m.Paths().Scratch; got != (Root{}) {
		t.Fatalf("an unresolved root must stay the zero value, got %+v", got)
	}
}

// TestContainerize_ConfiguredTarget_RewritesEngineAndMounts: a root with a
// configured target has its Engine side moved there, and the mount that
// makes the move true is recorded — the container CHOSE the target; nothing
// was asked of an engine to discover whether it could.
func TestContainerize_ConfiguredTarget_RewritesEngineAndMounts(t *testing.T) {
	m := Containerize{EngineHome: "/container/home"}.Apply(Paths{
		EngineHome: Root{Host: "/host/cfg0"},
	})
	got := m.Paths().EngineHome
	if got.Engine != "/container/home" {
		t.Fatalf("Engine = %q, want the configured target", got.Engine)
	}
	if got.Host != "/host/cfg0" {
		t.Fatalf("Host must be left alone: %q", got.Host)
	}
	if len(m.Mounts()) != 1 || m.Mounts()[0] != (Mount{HostDir: "/host/cfg0", TargetDir: "/container/home"}) {
		t.Fatalf("mounts: %v", m.Mounts())
	}
}

// TestContainerize_NoConfiguredTarget_MountsAtTheSamePath: with no configured
// target, the engine will look exactly where it always looks — the fixed
// path convention it was never given a variable to override. The only mount
// that leaves it reachable is one AT that same path, not merely a skipped
// mount: without it the container has no view of the bytes at all.
func TestContainerize_NoConfiguredTarget_MountsAtTheSamePath(t *testing.T) {
	m := Containerize{}.Apply(Paths{ProjectRoot: Root{Host: "/host/project"}})
	got := m.Paths().ProjectRoot
	if got.Engine != got.Host {
		t.Fatalf("no lever: Engine must equal Host, got %q vs %q", got.Engine, got.Host)
	}
	if len(m.Mounts()) != 1 || m.Mounts()[0] != (Mount{HostDir: "/host/project", TargetDir: "/host/project"}) {
		t.Fatalf("mounts: %v", m.Mounts())
	}
}

// TestContainerize_UnresolvedRoot_ContributesNoMount: a root this run never
// resolved has nothing to mount — mounting "" would be a container binding
// nothing meaningful, not a harmless no-op.
func TestContainerize_UnresolvedRoot_ContributesNoMount(t *testing.T) {
	m := Containerize{Scratch: "/container/scratch"}.Apply(Paths{
		ProjectRoot: Root{Host: "/host/project"},
	})
	for _, mnt := range m.Mounts() {
		if mnt.HostDir == "" {
			t.Fatalf("an unresolved root must not appear in Mounts: %v", m.Mounts())
		}
	}
	if got := m.Paths().Scratch; got != (Root{}) {
		t.Fatalf("Scratch must stay the zero value: %+v", got)
	}
}

// --- rooting and announcing --------------------------------------------

// TestConventionalComposition_NeedsNoAnnouncement: a well-known-path surface
// completes without anything naming it and contributes no argv or env at
// all — the engine finds it by its own fixed rule.
func TestConventionalComposition_NeedsNoAnnouncement(t *testing.T) {
	got := New(OnHost(Paths{ProjectRoot: Root{Host: "/host/project"}})).
		UnderProjectRoot("CLAUDE.md").
		Build()

	if len(got.Args) != 0 {
		t.Fatalf("a conventional-path surface must contribute no argv: %v", got.Args)
	}
	if len(got.Env) != 0 {
		t.Fatalf("a conventional-path surface must contribute no env: %v", got.Env)
	}
	if got.EnginePath == "" {
		t.Fatal("engine path must still resolve so a later layer could name it")
	}
}

// TestHostComposition_ArgvNamesTheHostPath: with no containerization the
// engine path IS the host path, and AnnounceFlag does the right thing without
// knowing that.
func TestHostComposition_ArgvNamesTheHostPath(t *testing.T) {
	got := New(OnHost(Paths{ProjectRoot: Root{Host: "/host/project"}})).
		UnderProjectRoot(".claude/settings.json").
		AnnounceFlag("--settings").
		Build()

	if got.EnginePath != got.HostPath {
		t.Fatalf("unmapped composition must leave engine path == host path: %q vs %q", got.EnginePath, got.HostPath)
	}
	if got.Args[1] != got.HostPath {
		t.Fatalf("args: %v", got.Args)
	}
}

// TestRelocatedHome_AnnounceEnvNamesTheRoot pins the relocated-home
// mechanism: AnnounceEnv names the DIRECTORY the composition is rooted
// under, not the specific file, and does so from the resolved Engine side —
// unmapped here, so it is the host value.
func TestRelocatedHome_AnnounceEnvNamesTheRoot(t *testing.T) {
	got := New(OnHost(Paths{EngineHome: Root{Host: "/scratch/cfg0"}})).
		UnderEngineHome("settings.json").
		AnnounceEnv("CLAUDE_CONFIG_DIR").
		Build()

	if got.Env["CLAUDE_CONFIG_DIR"] != "/scratch/cfg0" {
		t.Fatalf("env: %v", got.Env)
	}
	if got.HostPath != filepath.FromSlash("/scratch/cfg0/settings.json") {
		t.Fatalf("host path: %q", got.HostPath)
	}
	if got.EnginePath != got.HostPath {
		t.Fatalf("unmapped relocated home must let the engine see the host path, got %q want %q",
			got.EnginePath, got.HostPath)
	}
}

// TestNestedRel_EnginePathKeepsItsPositionBeneathTheMountTarget: with a
// nested rel, the file's position relative to the root must survive the
// remap — the root moves, the file's place within it does not.
func TestNestedRel_EnginePathKeepsItsPositionBeneathTheMountTarget(t *testing.T) {
	m := Containerize{EngineHome: "/container/home"}.Apply(Paths{
		EngineHome: Root{Host: "/scratch/cfg0"},
	})
	got := New(m).UnderEngineHome("sub/config.toml").AnnounceEnv("CODEX_HOME").Build()

	if got.EnginePath != "/container/home/sub/config.toml" {
		t.Fatalf("engine path must keep its position beneath the home: got %q", got.EnginePath)
	}
	if got.Env["CODEX_HOME"] != "/container/home" {
		t.Fatalf("the home the engine is told must be the mount target: %v", got.Env)
	}
	if got.HostPath != filepath.FromSlash("/scratch/cfg0/sub/config.toml") {
		t.Fatalf("host path must stay where the bytes were written: %q", got.HostPath)
	}
}

// TestEnginePath_UsesForwardSlashesRegardlessOfHostSeparator is the bug fix:
// EnginePath is a container-reachable path once it genuinely differs from
// Host, and a container is Linux regardless of the host OS this process runs
// on. filepath.Join would have carried the host's separator into a path the
// engine can never open; this pins the '/'-joined result directly, with no
// filepath.FromSlash laundering the assertion the way the prior version's
// tests did.
func TestEnginePath_UsesForwardSlashesRegardlessOfHostSeparator(t *testing.T) {
	m := Containerize{Scratch: "/container/scratch"}.Apply(Paths{
		Scratch: Root{Host: filepath.Join("host", "scratch", "run1")},
	})
	got := New(m).UnderScratch("sub/deep/file.txt").Build()

	if got.EnginePath != "/container/scratch/sub/deep/file.txt" {
		t.Fatalf("engine path must be forward-slash-joined: got %q", got.EnginePath)
	}
}

// TestMappingDoesNotWriteBackIntoTheCompositionItRead: a Rooted is copied by
// value but its Env and Args are not automatically, so a transform that
// assigned into the received map or appended in place could reach backwards
// and rewrite a BRANCH its own caller still holds. base already carries a
// non-nil Env and Args before branching — starting from nil would let a
// missing defensive copy hide, since writing into a freshly-allocated map or
// a nil-backed append can never alias anything.
func TestMappingDoesNotWriteBackIntoTheCompositionItRead(t *testing.T) {
	base := New(OnHost(Paths{EngineHome: Root{Host: "/scratch/cfg0"}})).
		UnderEngineHome("config.toml").
		AnnounceEnv("CODEX_HOME").
		AnnounceFlag("--config")

	branchA := base.AnnounceEnv("A").AnnounceFlag("--a").Build()
	branchB := base.AnnounceEnv("B").AnnounceFlag("--b").Build()

	if _, ok := branchA.Env["B"]; ok {
		t.Fatalf("branchA's env was rewritten by branchB's announcement: %v", branchA.Env)
	}
	if _, ok := branchB.Env["A"]; ok {
		t.Fatalf("branchB's env was rewritten by branchA's announcement: %v", branchB.Env)
	}
	if strings.Contains(strings.Join(branchA.Args, " "), "--b") {
		t.Fatalf("branchA's args were rewritten by branchB's announcement: %v", branchA.Args)
	}
	if strings.Contains(strings.Join(branchB.Args, " "), "--a") {
		t.Fatalf("branchB's args were rewritten by branchA's announcement: %v", branchB.Args)
	}
}

// --- invariant: nothing the engine reads names a host path -----------------

// TestContainerized_NothingTheEngineReadsNamesAHostPath is the invariant. The
// engine cannot see the host filesystem, so any host path surviving into what
// it reads is a path it will fail to open — or, worse, one it opens to find
// nothing. HostPath is deliberately exempt: it exists to name the host, and
// the runtime reads it, not the engine.
func TestContainerized_NothingTheEngineReadsNamesAHostPath(t *testing.T) {
	const home = "/scratch/cfg0"
	m := Containerize{EngineHome: "/container/home"}.Apply(Paths{EngineHome: Root{Host: home}})
	got := New(m).
		UnderEngineHome("sub/config.toml").
		AnnounceEnv("CODEX_HOME").
		AnnounceFlag("--config").
		Build()

	read := map[string]string{"EnginePath": got.EnginePath}
	for i, a := range got.Args {
		read["Args["+strconv.Itoa(i)+"]"] = a
	}
	for k, v := range got.Env {
		read["Env["+k+"]"] = v
	}
	if len(read) < 4 {
		t.Fatalf("nothing was checked, so this proves nothing: %v", read)
	}
	for where, v := range read {
		if strings.Contains(v, filepath.FromSlash(home)) {
			t.Errorf("%s still names a host path: %q", where, v)
		}
	}
}

// --- proof case 1: a flag-naming presenter CAN be containerized ------------

// TestProof_FlagNamingPresenter_CanBeContainerized is the clearest single
// proof the redesign works: under the OLD chain, mounting had to precede
// naming a flag (WithContainerMount lived on Rooted, WithFlag's result
// Flagged had no WithContainerMount), so a presenter that only ever named a
// flag — claude's --append-system-prompt-file, a scratch file named on argv
// and never on the environment — could not be containerized AT ALL: it had
// no channel a container could discover a lever through. Here containerizing
// is a property of Paths, decided and finished before this presenter is
// ever called, so the SAME presenter that runs uncontainerized runs
// unchanged here too.
func TestProof_FlagNamingPresenter_CanBeContainerized(t *testing.T) {
	appendSystemPromptPresenter := func(s Start) Presentation {
		return s.UnderScratch("system-prompt.md").
			AnnounceFlag("--append-system-prompt-file").
			Build()
	}

	mapped := Containerize{Scratch: "/container/scratch"}.Apply(Paths{
		Scratch: Root{Host: filepath.Join("host", "scratch", "run1")},
	})
	got := appendSystemPromptPresenter(New(mapped))

	if want := "/container/scratch/system-prompt.md"; got.EnginePath != want {
		t.Fatalf("engine path = %q, want %q", got.EnginePath, want)
	}
	if len(got.Args) != 2 || got.Args[0] != "--append-system-prompt-file" || got.Args[1] != got.EnginePath {
		t.Fatalf("argv must name the ENGINE path: %v", got.Args)
	}
	if got.Args[1] == got.HostPath {
		t.Fatalf("argv named the HOST path; the engine cannot open it: %q", got.Args[1])
	}
	if len(mapped.Mounts()) != 1 || mapped.Mounts()[0].TargetDir != "/container/scratch" {
		t.Fatalf("the scratch root must be mounted at the configured target: %v", mapped.Mounts())
	}
}

// TestProof_PresenterDoesNotBranchOnContainerization runs the IDENTICAL
// presenter function through OnHost and through Containerize. Neither call
// site nor the presenter itself contains a conditional on "am I
// containerized" — the presenter yields Engine == Host in one case and the
// container's target in the other purely because Paths already differs
// before the presenter is invoked.
func TestProof_PresenterDoesNotBranchOnContainerization(t *testing.T) {
	presenter := func(s Start) Presentation {
		return s.UnderProjectRoot(".claude/settings.json").AnnounceFlag("--settings").Build()
	}

	host := presenter(New(OnHost(Paths{ProjectRoot: Root{Host: "/host/project"}})))
	if host.EnginePath != host.HostPath {
		t.Fatalf("uncontainerized: engine path must equal host path, got %q vs %q", host.EnginePath, host.HostPath)
	}

	contained := presenter(New(Containerize{ProjectRoot: "/workspace"}.Apply(Paths{
		ProjectRoot: Root{Host: "/host/project"},
	})))
	if contained.EnginePath != "/workspace/.claude/settings.json" {
		t.Fatalf("containerized: engine path = %q, want the mapped target", contained.EnginePath)
	}
	if contained.EnginePath == contained.HostPath {
		t.Fatalf("containerized composition must diverge from the host path")
	}
}

// --- proof case 2: served delivery composes as advice ----------------------

// TestProof_Served_ComposesAsAdvice: a served endpoint is a Root exactly like
// a file-backed one, so it travels through the SAME Containerize/OnHost
// advice with no served-specific case anywhere in that advice — Containerize
// has never heard of Served and does not need to. Proving this needs no
// escape hatch: the endpoint is read straight off the SAME Mapped a
// file-backed presenter would also read from.
func TestProof_Served_ComposesAsAdvice(t *testing.T) {
	unmapped := New(OnHost(Paths{Scratch: Root{Host: "/host/scratch/mcp.sock"}}))
	got := unmapped.Served(unmapped.Paths().Scratch, "MCP_ENDPOINT")
	if got.Env["MCP_ENDPOINT"] != "/host/scratch/mcp.sock" {
		t.Fatalf("uncontainerized endpoint: %v", got.Env)
	}
	if got.HostPath != "" {
		t.Fatalf("Served must contribute no HostPath: %q", got.HostPath)
	}

	mapped := New(Containerize{Scratch: "/container/scratch"}.Apply(Paths{
		Scratch: Root{Host: "/host/scratch/mcp.sock"},
	}))
	containerized := mapped.Served(mapped.Paths().Scratch, "MCP_ENDPOINT")
	if containerized.Env["MCP_ENDPOINT"] != "/container/scratch" {
		t.Fatalf("containerized endpoint must be the advised Engine side: %v", containerized.Env)
	}
}

// --- illegal orders ----------------------------------------------------

// TestIllegalOrdersDoNotCOMPILE is the claim typestate exists to make: each
// case is a composition an enum-plus-dispatch design could express and fail
// on at RUNTIME, and here is not writable at all.
//
// THE SNIPPET COMPILES INSIDE A COPY OF THIS PACKAGE, deliberately. Compiling
// it standalone fails with "undefined: New" — a rejection for entirely the
// wrong reason, which would pass just as happily with every narrowing
// removed.
func TestIllegalOrdersDoNotCOMPILE(t *testing.T) {
	cases := map[string]string{
		"build_before_any_root": `_ = New(OnHost(Paths{})).Build()`,
		"announce_before_any_root": `_ = New(OnHost(Paths{})).` +
			`AnnounceFlag("--settings")`,
		"re_root_an_already_rooted_composition": `_ = New(OnHost(Paths{ProjectRoot: Root{Host: "/p"}})).` +
			`UnderProjectRoot("a").UnderProjectRoot("b")`,
		"served_result_cannot_be_further_composed": `_ = New(OnHost(Paths{Scratch: Root{Host: "/s"}})).` +
			`Served(Root{Host: "/s"}, "V").AnnounceFlag("--x")`,
	}

	sources, err := filepath.Glob("*.go")
	if err != nil || len(sources) == 0 {
		t.Fatalf("no package sources to copy (%v) — this test would pass vacuously", err)
	}

	// CONTROL. Without it, every rejection below could be the harness failing
	// and the test would report success for the wrong reason.
	t.Run("control_a_legal_composition_COMPILES", func(t *testing.T) {
		out, cerr := compileInPackageCopy(t, sources,
			`_ = New(OnHost(Paths{ProjectRoot: Root{Host: "/p"}})).`+
				`UnderProjectRoot("a").AnnounceEnv("V").AnnounceFlag("--settings").Build()`)
		if cerr != nil {
			t.Fatalf("harness rejects a LEGAL composition, so every rejection below proves nothing: %v\n%s", cerr, out)
		}
	})

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			out, cerr := compileInPackageCopy(t, sources, body)
			if cerr == nil {
				t.Fatalf("this composition COMPILED and must not: %s", body)
			}
			if !strings.Contains(out, "has no field or method") && !strings.Contains(out, "undefined") &&
				!strings.Contains(out, "not enough arguments") {
				t.Fatalf("rejected, but not by the type system as intended:\n%s", out)
			}
		})
	}
}

// TestNew_RejectsRawPaths_DoesNotCompile pins invariant 3 on its own: New's
// parameter type is Mapped, so passing a raw, un-advised Paths must not
// compile — there is no path from "resolved roots" to "a presenter can build
// from them" that skips advice. The control proves a legal composition
// (Paths pushed through OnHost first) compiles in the SAME harness, so the
// rejection below is the type system, not the harness rejecting everything.
func TestNew_RejectsRawPaths_DoesNotCompile(t *testing.T) {
	sources, err := filepath.Glob("*.go")
	if err != nil || len(sources) == 0 {
		t.Fatalf("no package sources to copy (%v) — this test would pass vacuously", err)
	}

	t.Run("control_New_of_advised_Paths_COMPILES", func(t *testing.T) {
		out, cerr := compileInPackageCopy(t, sources, `_ = New(OnHost(Paths{ProjectRoot: Root{Host: "/p"}}))`)
		if cerr != nil {
			t.Fatalf("harness rejects a LEGAL composition, so the rejection below proves nothing: %v\n%s", cerr, out)
		}
	})

	t.Run("New_of_raw_Paths_does_not_compile", func(t *testing.T) {
		out, cerr := compileInPackageCopy(t, sources, `_ = New(Paths{ProjectRoot: Root{Host: "/p"}})`)
		if cerr == nil {
			t.Fatal("New(Paths{...}) compiled; New must accept only Mapped")
		}
		if !strings.Contains(out, "cannot use") {
			t.Fatalf("rejected, but not by the type system as intended:\n%s", out)
		}
	})
}

func compileInPackageCopy(t *testing.T, sources []string, body string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	for _, src := range sources {
		if strings.HasSuffix(src, "_test.go") {
			continue
		}
		b, rerr := os.ReadFile(src)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if werr := os.WriteFile(filepath.Join(dir, filepath.Base(src)), b, 0o644); werr != nil {
			t.Fatal(werr)
		}
	}
	if werr := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module presenttmp\n\ngo 1.25\n"), 0o644); werr != nil {
		t.Fatal(werr)
	}
	snippet := "package present\n\nfunc snippet() {\n\t" + body + "\n}\n"
	if werr := os.WriteFile(filepath.Join(dir, "snippet.go"), []byte(snippet), 0o644); werr != nil {
		t.Fatal(werr)
	}
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	out, berr := cmd.CombinedOutput()
	return string(out), berr
}
