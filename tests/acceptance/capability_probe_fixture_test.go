package acceptance

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/config"
)

// TestProbeConfigYAML_EveryProbeCarriesTheRuntimeAxisOntoTheBinding is the
// assertion this wiring owes, and it guards a SILENT failure.
//
// A probe that records the axes but never writes them runs on the HOST while
// its cell id, its tags and its evidence line all say "container". Exit 0,
// green, measuring the wrong thing — matrixConfigYAML's own doc names it: "an
// axis a cell asked for that quietly runs on the host instead". No ordinary
// gate can see it, because the cell passes.
//
// So this pins the ONE line that carries the containerization axis, for every
// probe that builds a binding. A builder that stops emitting it goes RED here
// rather than going green on the host.
func TestProbeConfigYAML_EveryProbeCarriesTheRuntimeAxisOntoTheBinding(t *testing.T) {
	a := liveAgent{config: fmt.Sprintf("version: %d\nllm:\n  configs:\n    claude:\n      type: claude-code\n      model: claude-haiku-4-5-20251001\n", config.CurrentConfigVersion)}

	// UNTAGGED builders only. P2's and P3's live in //go:build acceptance files,
	// so they are covered by the sibling test in
	// capability_probe_axis_wiring_acceptance_test.go — split rather than tagging
	// this whole file, because these two must keep running under plain
	// `just test` (the reason probe_p4_plan_sentinel_test.go states for staying
	// untagged: a guard that only fires where a real engine and a credential
	// happen to exist is not much of a guard).
	builders := probeRuntimeBuilders(a)

	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			for _, rt := range []string{"container-rootless", "container-rootful"} {
				assert.Contains(t, build(rt), "    runtime: "+rt+"\n",
					"%s must write the requested runtime onto the agent binding; without it the cell runs on the HOST and reports itself containerized", name)
			}
			// The host axis is the schema default. Writing it would put a key in
			// the fixture that every existing host row was measured without.
			assert.NotContains(t, build("host"), "runtime:",
				"%s must NOT write a runtime line for the host axis", name)
			assert.NotContains(t, build(""), "runtime:",
				"%s must NOT write a runtime line for an unset axis", name)
		})
	}
}

// TestRuntimeBindingLine_PassesAnUnknownValueThroughToTheSchema pins the
// deliberate choice this helper made when it replaced three copies.
//
// Two of those copies disagreed: one allow-listed the two container values, so
// an unrecognised runtime wrote NOTHING and the cell silently ran on the host.
// Passing the value through instead sends it to config's schema, which refuses
// it loudly. A future ownership mode is then a schema edit, not a silent drop.
func TestRuntimeBindingLine_PassesAnUnknownValueThroughToTheSchema(t *testing.T) {
	assert.Equal(t, "", runtimeBindingLine("host"), "host is the schema default")
	assert.Equal(t, "", runtimeBindingLine(""), "unset writes nothing")
	assert.Equal(t, "    runtime: container-rootless\n", runtimeBindingLine("container-rootless"))
	assert.Equal(t, "    runtime: container-rootful\n", runtimeBindingLine("container-rootful"))
	assert.Equal(t, "    runtime: container-someday\n", runtimeBindingLine("container-someday"),
		"an unrecognised mode must reach the schema and be REFUSED there, never be dropped here into a silent host run")
}

// probeRuntimeBuilders is the untagged half of the builder set. The acceptance-
// tagged file adds P2 and P3 to it, so ONE assertion body covers every probe
// that writes a binding, on whichever build the caller is running.
func probeRuntimeBuilders(a liveAgent) map[string]func(string) string {
	return map[string]func(string) string{
		"p0/p1 matrixConfigYAML": func(rt string) string { return matrixConfigYAML(a, "claude", rt) },
		"p4 p4ConfigYAML":        func(rt string) string { return p4ConfigYAML(a, "claude", p4Plan, rt) },
	}
}

// probeCellRunDir decides WHERE the evidence is read from, and every way it can
// be wrong produces a confident false finding rather than an error: read the
// wrong directory and the verdict reports "it never ran" about a cell that ran
// perfectly. All four arms are pinned.
func TestProbeCellRunDir_NoneRunsTheProjectItself(t *testing.T) {
	got, err := probeCellRunDir("probe", "/tmp/proj", "none")
	if err != nil {
		t.Fatalf("workspace=none must resolve without consulting git at all: %v", err)
	}
	if got != "/tmp/proj" {
		t.Errorf("probeCellRunDir(none) = %q, want the project dir %q", got, "/tmp/proj")
	}
}

func TestProbeCellRunDir_WorktreeResolvesThePerAgentCheckout(t *testing.T) {
	proj := t.TempDir()
	gitInitForProbe(t, proj)
	wt := filepath.Join(t.TempDir(), "agent-checkout")
	runGitForProbe(t, proj, "worktree", "add", "-b", "probe-cell", wt)

	got, err := probeCellRunDir("probe", proj, "worktree")
	if err != nil {
		t.Fatalf("a workspace=worktree cell must resolve its per-agent checkout: %v", err)
	}
	// EvalSymlinks: git reports the resolved path, and a temp dir is a symlink
	// on some platforms — comparing the two unresolved would fail for a reason
	// that has nothing to do with the behaviour under test.
	wantResolved, _ := filepath.EvalSymlinks(wt)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Errorf("probeCellRunDir(worktree) = %q, want the per-agent checkout %q — reading the project's copy instead finds no evidence and reports that the engine never ran", got, wt)
	}
}

func TestProbeCellRunDir_RefusesWhenNoCheckoutWasCreated(t *testing.T) {
	proj := t.TempDir()
	gitInitForProbe(t, proj)

	if _, err := probeCellRunDir("probe", proj, "worktree"); err == nil {
		t.Fatal("ZERO worktrees after a workspace=worktree run must be REFUSED, never smoothed into 'then use the project dir': that substitution lets a cell which never got its checkout pass on the host fixture's evidence, which is the axis silently not being tested")
	}
}

func TestProbeCellRunDir_RefusesWhenAnotherCellLeakedItsCheckout(t *testing.T) {
	proj := t.TempDir()
	gitInitForProbe(t, proj)
	runGitForProbe(t, proj, "worktree", "add", "-b", "cell-one", filepath.Join(t.TempDir(), "one"))
	runGitForProbe(t, proj, "worktree", "add", "-b", "cell-two", filepath.Join(t.TempDir(), "two"))

	if _, err := probeCellRunDir("probe", proj, "worktree"); err == nil {
		t.Fatal("TWO worktrees must be refused: this cell cannot tell whose evidence it is about to read, and picking either one is how a cell passes on a previous cell's stamp")
	}
}

func gitInitForProbe(t *testing.T, dir string) {
	t.Helper()
	runGitForProbe(t, dir, "init")
	runGitForProbe(t, dir, "config", "user.email", "probe@example.test")
	runGitForProbe(t, dir, "config", "user.name", "probe")
	if err := os.WriteFile(filepath.Join(dir, "seed"), []byte("seed\n"), 0o600); err != nil {
		t.Fatalf("seeding the probe repo: %v", err)
	}
	runGitForProbe(t, dir, "add", "seed")
	runGitForProbe(t, dir, "commit", "-m", "seed")
}

func runGitForProbe(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
	}
}
