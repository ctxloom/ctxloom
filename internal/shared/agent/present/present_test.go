package present

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestLever_ContainerPICKSTheTargetAndTellsTheEngine: a composition whose
// location is named through the environment HAS a lever, so the container may
// put the root wherever it likes and say so.
func TestLever_ContainerPICKSTheTargetAndTellsTheEngine(t *testing.T) {
	const home, target = "/scratch/cfg0", "/container/home"
	got := New("/host/project").
		WithEnvDirFile(EnvDirFile{EnvVar: "CODEX_HOME", HomeDefault: ".codex", Rel: "config.toml"}, home).
		WithContainerMount(ContainerMount{TargetDir: target}).
		WithFlag(FlagValue{Flag: "--config"}).
		Build()

	if want := filepath.FromSlash(target + "/config.toml"); got.EnginePath != want {
		t.Fatalf("engine path: got %q want %q", got.EnginePath, want)
	}
	if got.Env["CODEX_HOME"] != target {
		t.Fatalf("the engine must be TOLD the new home, got %v", got.Env)
	}
	if len(got.Mounts) != 1 || got.Mounts[0].HostDir != filepath.FromSlash(home) {
		t.Fatalf("the HOME is what must be mounted: %v", got.Mounts)
	}
	if got.Mounts[0].TargetDir != target {
		t.Fatalf("a lever means the requested target is honoured: %v", got.Mounts)
	}
	if got.Args[1] != got.EnginePath {
		t.Fatalf("argv must name the ENGINE path: %v", got.Args)
	}
	if got.HostPath != filepath.FromSlash(home+"/config.toml") {
		t.Fatalf("host path records where the bytes were written: %q", got.HostPath)
	}
}

// TestNoLever_ContainerMustMountAtTheEnginesOwnPath: with no environment
// variable and no flag, the engine will look exactly where it always looks.
// The requested target is therefore not merely ignored — honouring it would
// mount the bytes somewhere the engine never examines, and the run would report
// success having read nothing.
func TestNoLever_ContainerMustMountAtTheEnginesOwnPath(t *testing.T) {
	const fixed = "/host/project/.claude"
	got := New("/host/project").
		WithProjectRootFile(ProjectRootFile{Rel: ".claude/settings.json"}).
		WithContainerMount(ContainerMount{TargetDir: "/workspace/.claude"}).
		WithFlag(FlagValue{Flag: "--settings"}).
		Build()

	if len(got.Mounts) != 1 {
		t.Fatalf("mounts: %v", got.Mounts)
	}
	if got.Mounts[0].HostDir != filepath.FromSlash(fixed) {
		t.Fatalf("mount must cover the directory the bytes were written to: %q", got.Mounts[0].HostDir)
	}
	if got.Mounts[0].TargetDir != filepath.FromSlash(fixed) {
		t.Fatalf("no lever, so the root must be mounted AT itself, got %q", got.Mounts[0].TargetDir)
	}
	if got.EnginePath != got.HostPath {
		t.Fatalf("nothing can tell this engine a new path, so it must be unchanged: %q vs %q",
			got.EnginePath, got.HostPath)
	}
	if got.Args[1] != got.EnginePath {
		t.Fatalf("argv must name the engine path: %v", got.Args)
	}
}

// TestHostComposition_ArgvNamesTheHostPath: with no remapping layer the engine
// path IS the host path, and the same WithFlag does the right thing without
// knowing whether a container was involved.
func TestHostComposition_ArgvNamesTheHostPath(t *testing.T) {
	got := New("/host/project").
		WithProjectRootFile(ProjectRootFile{Rel: ".claude/settings.json"}).
		WithFlag(FlagValue{Flag: "--settings"}).
		Build()

	if got.EnginePath != got.HostPath {
		t.Fatalf("unmapped composition must leave engine path == host path: %q vs %q", got.EnginePath, got.HostPath)
	}
	if got.Args[1] != got.HostPath {
		t.Fatalf("args: %v", got.Args)
	}
	if len(got.Mounts) != 0 {
		t.Fatalf("a host composition must contribute no mounts: %v", got.Mounts)
	}
}

// TestConventionalComposition_NeedsNoFlag: a well-known-path surface completes
// without anything naming it and contributes no argv at all.
func TestConventionalComposition_NeedsNoFlag(t *testing.T) {
	got := New("/host/project").
		WithProjectRootFile(ProjectRootFile{Rel: "CLAUDE.md"}).
		Build()

	if len(got.Args) != 0 {
		t.Fatalf("a conventional-path surface must contribute no argv: %v", got.Args)
	}
	if got.EnginePath == "" {
		t.Fatal("engine path must still resolve so a later layer could name it")
	}
}

// TestRelocatedHome_ContributesTheEnvVar: the relocated-home mechanism carries
// its env var into the presentation — how a host run points an engine at a
// per-run scratch.
//
// It also pins the unmapped case for THIS rooting variant: with nothing
// remapping the composition, the engine sees the file exactly where the host
// wrote it. That is what lets a caller hand over ONE home and let the chain
// decide where the engine sees it — a caller that had to supply a second,
// already-mapped home could contradict the layer that does the mapping.
// TestHostComposition_ArgvNamesTheHostPath pins the same claim for
// WithProjectRootFile; deleting the assignment in EITHER rooting layer must go
// red, so neither may rely on the other's coverage.
func TestRelocatedHome_ContributesTheEnvVar(t *testing.T) {
	got := New("/host/project").
		WithEnvDirFile(EnvDirFile{EnvVar: "CLAUDE_CONFIG_DIR", HomeDefault: ".claude", Rel: "settings.json"}, "/scratch/cfg0").
		Build()

	if got.Env["CLAUDE_CONFIG_DIR"] != "/scratch/cfg0" {
		t.Fatalf("env: %v", got.Env)
	}
	if got.HostPath != filepath.FromSlash("/scratch/cfg0/settings.json") {
		t.Fatalf("host path: %q", got.HostPath)
	}
	if got.EnginePath != filepath.FromSlash("/scratch/cfg0/settings.json") {
		t.Fatalf("unmapped relocated home must let the engine see the host path, got %q want %q",
			got.EnginePath, got.HostPath)
	}
	if len(got.Mounts) != 0 {
		t.Fatalf("nothing remapped this composition, so it must carry no mounts: %v", got.Mounts)
	}
}

// TestIllegalOrdersDoNotCOMPILE is the claim typestate exists to make: each
// case is a composition an enum-plus-dispatch design could express and fail on
// at RUNTIME, and here is not writable at all.
//
// THE SNIPPET COMPILES INSIDE A COPY OF THIS PACKAGE, deliberately. Compiling
// it standalone fails with "undefined: New" — a rejection for entirely the
// wrong reason, which would pass just as happily with every narrowing removed.
func TestIllegalOrdersDoNotCOMPILE(t *testing.T) {
	cases := map[string]string{
		"flag_before_any_root": `_ = New("/p").WithFlag(FlagValue{Flag: "--settings"})`,
		"mount_after_flag": `_ = New("/p").WithProjectRootFile(ProjectRootFile{Rel: "a"}).` +
			`WithFlag(FlagValue{Flag: "--settings"}).WithContainerMount(ContainerMount{TargetDir: "/w"})`,
		"double_mount": `_ = New("/p").WithProjectRootFile(ProjectRootFile{Rel: "a"}).` +
			`WithContainerMount(ContainerMount{TargetDir: "/w"}).WithContainerMount(ContainerMount{TargetDir: "/w2"})`,
		"re_root_an_already_rooted_composition": `_ = New("/p").WithProjectRootFile(ProjectRootFile{Rel: "a"}).` +
			`WithProjectRootFile(ProjectRootFile{Rel: "b"})`,
		"build_before_a_root_exists": `_ = New("/p").Build()`,
	}

	sources, err := filepath.Glob("*.go")
	if err != nil || len(sources) == 0 {
		t.Fatalf("no package sources to copy (%v) — this test would pass vacuously", err)
	}

	// CONTROL. Without it, every rejection below could be the harness failing
	// and the test would report success for the wrong reason.
	t.Run("control_a_legal_composition_COMPILES", func(t *testing.T) {
		out, cerr := compileInPackageCopy(t, sources,
			`_ = New("/p").WithProjectRootFile(ProjectRootFile{Rel: "a"}).`+
				`WithContainerMount(ContainerMount{TargetDir: "/w"}).WithFlag(FlagValue{Flag: "--settings"}).Build()`)
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

// TestNestedRel_TheHOMEIsMountedNotTheFilesDirectory: the root a container must
// mount is the one the previous advice CONTRIBUTED, not one re-derived from the
// file's own path. With a nested Rel the two differ, and mounting the file's
// directory would leave the engine's home pointing at a directory that was
// never mounted.
func TestNestedRel_TheHOMEIsMountedNotTheFilesDirectory(t *testing.T) {
	const home, target = "/scratch/cfg0", "/container/home"
	got := New("/host/project").
		WithEnvDirFile(EnvDirFile{EnvVar: "CODEX_HOME", HomeDefault: ".codex", Rel: "sub/config.toml"}, home).
		WithContainerMount(ContainerMount{TargetDir: target}).
		Build()

	if got.Mounts[0].HostDir != filepath.FromSlash(home) {
		t.Fatalf("the contributed HOME is the mount root, not the file's directory: %q", got.Mounts[0].HostDir)
	}
	if want := filepath.FromSlash(target + "/sub/config.toml"); got.EnginePath != want {
		t.Fatalf("engine path must keep its position beneath the home: got %q want %q", got.EnginePath, want)
	}
	if got.Env["CODEX_HOME"] != target {
		t.Fatalf("the home the engine is told must be the mount target: %v", got.Env)
	}
}

// TestContainerized_NothingTheEngineReadsNamesAHostPath is the invariant. The
// engine cannot see the host filesystem, so any host path surviving into what it
// reads is a path it will fail to open — or, worse, one it opens to find
// nothing. HostPath and Mounts[].HostDir are deliberately exempt: they exist to
// name the host, and the runtime reads them, not the engine.
func TestContainerized_NothingTheEngineReadsNamesAHostPath(t *testing.T) {
	const home, target = "/scratch/cfg0", "/container/home"
	got := New("/host/project").
		WithEnvDirFile(EnvDirFile{EnvVar: "CODEX_HOME", HomeDefault: ".codex", Rel: "sub/config.toml"}, home).
		WithContainerMount(ContainerMount{TargetDir: target}).
		WithFlag(FlagValue{Flag: "--config"}).
		Build()

	read := map[string]string{"EnginePath": got.EnginePath}
	for i, a := range got.Args {
		read["Args["+strconv.Itoa(i)+"]"] = a
	}
	for k, v := range got.Env {
		read["Env["+k+"]"] = v
	}
	if len(read) < 4 {
		t.Fatalf("nothing was swept, so this proves nothing: %v", read)
	}
	for where, v := range read {
		if strings.Contains(v, filepath.FromSlash(home)) {
			t.Errorf("%s still names a host path: %q", where, v)
		}
	}
}

// TestHostRoot_PrefersTheOUTERMOSTContributedHome: where several contributed
// values contain the file, the mount must cover them all, so the widest one wins.
func TestHostRoot_PrefersTheOUTERMOSTContributedHome(t *testing.T) {
	p := Presentation{
		HostPath: filepath.FromSlash("/scratch/cfg0/sub/config.toml"),
		Env: map[string]string{
			"INNER": filepath.FromSlash("/scratch/cfg0/sub"),
			"OUTER": filepath.FromSlash("/scratch/cfg0"),
		},
	}
	if got := hostRoot(p); got != filepath.FromSlash("/scratch/cfg0") {
		t.Fatalf("outermost containing home must win, got %q", got)
	}
}

// TestApplyEnv_RemapsEVERYValueBeneathTheRoot: the transform is defined over the
// environment it receives, not over the one variable this advice happens to know
// about. An advice added later contributes into the same map and must be carried
// across the boundary too.
func TestApplyEnv_RemapsEVERYValueBeneathTheRoot(t *testing.T) {
	in := Presentation{Env: map[string]string{
		"CODEX_HOME": filepath.FromSlash("/scratch/cfg0"),
		"OTHER":      filepath.FromSlash("/scratch/cfg0/tools/bin"),
		"UNRELATED":  filepath.FromSlash("/usr/bin"),
	}}
	got := ContainerMount{TargetDir: "/container/home", root: filepath.FromSlash("/scratch/cfg0")}.ApplyEnv(in)

	if got.Env["CODEX_HOME"] != "/container/home" {
		t.Errorf("CODEX_HOME: %q", got.Env["CODEX_HOME"])
	}
	if want := filepath.FromSlash("/container/home/tools/bin"); got.Env["OTHER"] != want {
		t.Errorf("a second value beneath the root must travel too: got %q want %q", got.Env["OTHER"], want)
	}
	if got.Env["UNRELATED"] != filepath.FromSlash("/usr/bin") {
		t.Errorf("a value outside the root must be left alone: %q", got.Env["UNRELATED"])
	}
}

// TestMappingDoesNotWriteBackIntoTheCompositionItRead: a Presentation is copied
// by value but its Env map is not, so a transform that assigned into the
// received map would reach backwards and rewrite the composition its own caller
// still holds.
func TestMappingDoesNotWriteBackIntoTheCompositionItRead(t *testing.T) {
	const home = "/scratch/cfg0"
	rooted := New("/host/project").
		WithEnvDirFile(EnvDirFile{EnvVar: "CODEX_HOME", HomeDefault: ".codex", Rel: "config.toml"}, home)

	_ = rooted.WithContainerMount(ContainerMount{TargetDir: "/container/home"}).Build()

	if got := rooted.Build().Env["CODEX_HOME"]; got != filepath.FromSlash(home) {
		t.Fatalf("the unmapped composition was rewritten behind its own back: %q", got)
	}
}

// TestTheLeverIsDECLAREDByTheInterface: containerization decides where to mount
// by asking whether the rooting advice implements EnvChannel. Both halves of
// that question are load-bearing, and only the positive half can be stated as a
// compile-time conformance assertion — Go cannot assert that a type does NOT
// implement an interface, so the negative half is checked here or nowhere.
func TestTheLeverIsDECLAREDByTheInterface(t *testing.T) {
	if _, ok := any(EnvDirFile{}).(EnvChannel); !ok {
		t.Error("EnvDirFile must implement EnvChannel: naming the location through a variable IS the lever")
	}
	if _, ok := any(ProjectRootFile{}).(EnvChannel); ok {
		t.Error("ProjectRootFile must NOT implement EnvChannel: the engine fixed this path, " +
			"and declaring a lever it does not have would relocate bytes to a directory it never reads")
	}
}
