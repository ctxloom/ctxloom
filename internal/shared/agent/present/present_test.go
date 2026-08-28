package present

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestContainerComposition_ArgvNamesTheENGINEPath is the whole point of the
// composition, as an assertion. Handing an engine a HOST path that nothing
// mounted makes it read nothing while the run reports success.
//
// Here the container layer is what PRODUCES the engine path, so argv cannot
// name anything the mount did not cover.
func TestContainerComposition_ArgvNamesTheENGINEPath(t *testing.T) {
	got := New("/host/project").
		WithProjectRootFile(ProjectRootFile{Rel: ".claude/settings.json"}).
		WithContainerMount("/workspace/.claude").
		WithFlag(FlagValue{Flag: "--settings"}).
		Build()

	if got.HostPath != filepath.FromSlash("/host/project/.claude/settings.json") {
		t.Fatalf("host path: %q", got.HostPath)
	}
	want := filepath.FromSlash("/workspace/.claude/settings.json")
	if got.EnginePath != want {
		t.Fatalf("engine path: got %q want %q", got.EnginePath, want)
	}
	if len(got.Args) != 2 || got.Args[0] != "--settings" {
		t.Fatalf("args: %v", got.Args)
	}
	if got.Args[1] != want {
		t.Fatalf("argv must name the ENGINE path, not the host path: got %q", got.Args[1])
	}
	if got.Args[1] == got.HostPath {
		t.Fatalf("argv named the HOST path, which nothing mounted: %q", got.Args[1])
	}
	if len(got.Mounts) != 1 || got.Mounts[0].TargetDir != "/workspace/.claude" {
		t.Fatalf("mounts: %v", got.Mounts)
	}
	if got.Mounts[0].HostDir != filepath.FromSlash("/host/project/.claude") {
		t.Fatalf("mount must cover the directory the bytes were written to: %q", got.Mounts[0].HostDir)
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
			`WithFlag(FlagValue{Flag: "--settings"}).WithContainerMount("/w")`,
		"double_mount": `_ = New("/p").WithProjectRootFile(ProjectRootFile{Rel: "a"}).` +
			`WithContainerMount("/w").WithContainerMount("/w2")`,
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
				`WithContainerMount("/w").WithFlag(FlagValue{Flag: "--settings"}).Build()`)
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
