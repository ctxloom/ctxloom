//go:build acceptance

// J9: "the isolation seam's two axes actually land where they say they will"
// (j9_isolation.feature). The workspace axis (none|worktree) is proven via
// the mock backend's now-honest req.WorkDir record (internal/lm/backends/
// mock.go — the mock's own OS-level cwd never moves, since isolation never
// os.Chdir's the plugin subprocess itself; only req.WorkDir, the value
// isolation.Prepare actually resolved, does). The runtime axis's fail-loud
// contract (a requested container that can't launch aborts unless
// --degraded) needs no such record — it is proven by exit code + stderr.
//
// See the feature file's own "WHAT THIS JOURNEY CAN AND CANNOT SEE" note for
// why this journey stops at workspace-boundary distinctness/non-escape and
// does not attempt per-engine config-home variable isolation (that needs a
// real registered-engine fixture — claude-code/codex/kiro — out of hermetic
// scope here) or a real container launch (needs a live daemon + built image,
// deliberately deferred behind @container elsewhere, not in this file).
package acceptance

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/gitignore"
	"github.com/ctxloom/ctxloom/internal/testsupport/containercell"
)

// j9Record is one "Alice runs the mock agent under workspace ..." step's
// observed outcome: the record file's cwd= (the plugin subprocess's own,
// isolation-invariant OS cwd — kept for diagnostics only) and workdir= (the
// value isolation.Prepare actually resolved and threaded through
// RunOptions.WorkDir — the honest workspace-boundary signal).
type j9Record struct {
	Workspace string
	Prompt    string
	Cwd       string
	WorkDir   string
}

// j9State is J9's fixture state: the scratch dir holding one record file per
// run (a fresh path per invocation — the A.3 "sequential" approach: no mock/
// harness concurrency change needed, each run mints its own harp and its own
// scratch), and every run's observed record in order.
type j9State struct {
	recordDir string
	records   []j9Record
	// lastContainerRecPath is the mock-container run's own record file:
	// distinct from records above, which only the workspace-axis "mock agent"
	// steps append to. Lets "the run runs on the host" check WHERE the
	// degraded run actually executed, not merely that it exited 0.
	lastContainerRecPath string
}

func j9Of(w *World) *j9State {
	if w.j9 == nil {
		w.j9 = &j9State{recordDir: filepath.Join(w.env.Root, "j9-records")}
	}
	return w.j9
}

// j9ConfigYAML renders the PROJECT half of config.yaml for J9's fixture: one
// "fast" mock LLM label and two agent bindings — "mock" (host runtime, the
// default) for the workspace-axis scenarios, and "mock-container" (runtime:
// container) for the runtime-axis degrade scenario. Static (no per-run
// record path), so it is written once; j9HomeConfigYAML carries the part
// that changes per run (mirrors j6RenderConfig's direct config.yaml
// authoring rather than the MockLM helper, which does not preserve an
// "agents:" section).
//
// llm.configs.fast.env used to live HERE, in the project file, alongside
// type. It moved to the HOME half (j9HomeConfigYAML): llm.configs.*.env is
// ScopeMachine (internal/config/layerscope) — credential passthrough, where
// a committed project-file value is a leaked secret — so a project-declared
// env block no longer survives a real Load at all. Splitting the "fast"
// label across layers like this is legal (unlike agents.*, llm.configs.*
// has no atomic-replace merge rule), so the project's own `type: mock` and
// home's `env` deep-merge into one usable entry.
func j9ConfigYAML() string {
	return `version: 4
llm:
  configs:
    fast:
      type: mock
  defaults:
    primary: fast
    fast: fast
agents:
  mock:
    engine: fast
    profiles: []
  mock-container:
    engine: fast
    profiles: []
    runtime: container
`
}

// j9HomeConfigYAML renders the HOME half of config.yaml for J9's fixture:
// llm.configs.fast.env, pointed at recordFile. Rewritten before EVERY run
// with a fresh recordFile so sequential runs never clobber each other's
// evidence — see j9ConfigYAML's doc for why this piece lives in HOME.
func j9HomeConfigYAML(recordFile string) string {
	return fmt.Sprintf(`version: 4
llm:
  configs:
    fast:
      env:
        CTXLOOM_MOCK_RECORD_FILE: %q
`, recordFile)
}

// j9ParseRecord extracts the cwd= and workdir= lines the mock's
// recordMockInput (internal/lm/backends/mock.go) wrote to its record file.
func j9ParseRecord(body string) (cwd, workDir string) {
	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(line, "cwd="):
			cwd = strings.TrimPrefix(line, "cwd=")
		case strings.HasPrefix(line, "workdir="):
			workDir = strings.TrimPrefix(line, "workdir=")
		}
	}
	return cwd, workDir
}

func registerJ9Steps(ctx *godog.ScenarioContext) {
	ctx.Step(`^Alice has a git-backed project with a mock agent$`, func(c context.Context) error {
		w := worldFrom(c)
		j9 := j9Of(w)
		if err := w.env.InitGitRepo(); err != nil {
			return err
		}
		if err := os.MkdirAll(j9.recordDir, 0o755); err != nil {
			return fmt.Errorf("create record dir: %w", err)
		}
		if err := w.env.WriteFile(".ctxloom/config.yaml", j9ConfigYAML()); err != nil {
			return err
		}
		// A throwaway initial record path — every run step below rewrites
		// the HOME half of config.yaml with its own fresh record file before
		// invoking `run`.
		if err := w.env.WriteHomeFile(".ctxloom/config.yaml", j9HomeConfigYAML(filepath.Join(j9.recordDir, "init.txt"))); err != nil {
			return err
		}
		return w.env.GitCommit("initial ctxloom config")
	})

	ctx.Step(`^Alice runs the mock agent under workspace "([^"]*)" with prompt "([^"]*)"$`, func(c context.Context, workspace, prompt string) error {
		w := worldFrom(c)
		j9 := j9Of(w)
		recPath := filepath.Join(j9.recordDir, fmt.Sprintf("record-%d.txt", len(j9.records)))
		if err := w.env.WriteHomeFile(".ctxloom/config.yaml", j9HomeConfigYAML(recPath)); err != nil {
			return err
		}
		if err := runOK(w, "run", "--agent", "mock", "--workspace", workspace, "--print", prompt); err != nil {
			return err
		}
		body, err := os.ReadFile(recPath)
		if err != nil {
			return fmt.Errorf("read mock record %q: %w", recPath, err)
		}
		cwd, workDir := j9ParseRecord(string(body))
		j9.records = append(j9.records, j9Record{Workspace: workspace, Prompt: prompt, Cwd: cwd, WorkDir: workDir})
		return nil
	})

	ctx.Step(`^the run's workdir reflects the "([^"]*)" workspace axis$`, func(c context.Context, workspace string) error {
		w := worldFrom(c)
		j9 := j9Of(w)
		if len(j9.records) == 0 {
			return fmt.Errorf("no run recorded yet")
		}
		last := j9.records[len(j9.records)-1]
		w.docStepMaterialized = fmt.Sprintf("workspace=%s -> workdir=%s (cwd=%s)", workspace, last.WorkDir, last.Cwd)
		switch workspace {
		case "none":
			if last.WorkDir != w.env.ProjectDir {
				return fmt.Errorf("workspace \"none\": workdir = %q, want the project dir %q", last.WorkDir, w.env.ProjectDir)
			}
		case "worktree":
			if last.WorkDir == "" {
				return fmt.Errorf("workspace \"worktree\": workdir was never recorded")
			}
			if last.WorkDir == w.env.ProjectDir || strings.HasPrefix(last.WorkDir, w.env.ProjectDir+string(filepath.Separator)) {
				return fmt.Errorf("workspace \"worktree\": workdir %q is the project dir (or under it) %q — not isolated", last.WorkDir, w.env.ProjectDir)
			}
		default:
			return fmt.Errorf("j9: unknown workspace axis %q", workspace)
		}
		return nil
	})

	ctx.Step(`^the two runs' workdirs are distinct and neither is under the project directory$`, func(c context.Context) error {
		w := worldFrom(c)
		j9 := j9Of(w)
		if len(j9.records) < 2 {
			return fmt.Errorf("expected at least 2 runs, got %d", len(j9.records))
		}
		a, b := j9.records[len(j9.records)-2], j9.records[len(j9.records)-1]
		w.docStepMaterialized = fmt.Sprintf("%s (%s) -> workdir=%s\n%s (%s) -> workdir=%s", a.Prompt, a.Workspace, a.WorkDir, b.Prompt, b.Workspace, b.WorkDir)
		if a.WorkDir == "" || b.WorkDir == "" {
			return fmt.Errorf("a run's workdir was never recorded: a=%q b=%q", a.WorkDir, b.WorkDir)
		}
		if a.WorkDir == b.WorkDir {
			return fmt.Errorf("both runs share the SAME workdir %q — not isolated", a.WorkDir)
		}
		proj := w.env.ProjectDir + string(filepath.Separator)
		if strings.HasPrefix(a.WorkDir, proj) || a.WorkDir == w.env.ProjectDir {
			return fmt.Errorf("run %q's workdir %q is under the project tree", a.Prompt, a.WorkDir)
		}
		if strings.HasPrefix(b.WorkDir, proj) || b.WorkDir == w.env.ProjectDir {
			return fmt.Errorf("run %q's workdir %q is under the project tree", b.Prompt, b.WorkDir)
		}
		if strings.HasPrefix(a.WorkDir, b.WorkDir+string(filepath.Separator)) || strings.HasPrefix(b.WorkDir, a.WorkDir+string(filepath.Separator)) {
			return fmt.Errorf("one run's workdir is nested inside the other's: %q vs %q", a.WorkDir, b.WorkDir)
		}
		return nil
	})

	ctx.Step(`^none of the per-agent worktree config artifacts appear in the project's git status$`, func(c context.Context) error {
		w := worldFrom(c)
		cmd := exec.Command("git", "status", "--porcelain")
		cmd.Dir = w.env.ProjectDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git status --porcelain: %w (%s)", err, out)
		}
		status := string(out)
		w.docStepMaterialized = fmt.Sprintf("git status --porcelain:\n%s", strings.TrimSpace(status))
		if status == "" {
			return nil
		}
		for _, pattern := range gitignore.WorktreeArtifactPatterns {
			bare := strings.TrimSuffix(pattern, "/")
			if strings.Contains(status, bare) {
				return fmt.Errorf("project git status unexpectedly carries the per-agent worktree artifact %q:\n%s", pattern, status)
			}
		}
		return nil
	})

	ctx.Step(`^the shared git exclude file carries the ctxloom per-agent worktree config block$`, func(c context.Context) error {
		w := worldFrom(c)
		path := filepath.Join(w.env.ProjectDir, ".git", "info", "exclude")
		body, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %q: %w", path, err)
		}
		content := string(body)
		w.docStepMaterialized = fmt.Sprintf("%s:\n%s", path, strings.TrimSpace(content))
		if !strings.Contains(content, gitignore.WorktreeComment) {
			return fmt.Errorf("%q does not carry the ctxloom worktree-config comment %q; content:\n%s", path, gitignore.WorktreeComment, content)
		}
		for _, pattern := range gitignore.WorktreeArtifactPatterns {
			if !strings.Contains(content, pattern) {
				return fmt.Errorf("%q does not carry the worktree-artifact pattern %q; content:\n%s", path, pattern, content)
			}
		}
		return nil
	})

	ctx.Step(`^Alice runs the container-bound agent with flags "([^"]*)"$`, func(c context.Context, flags string) error {
		w := worldFrom(c)
		j9 := j9Of(w)
		// THIS ROW ONLY MEANS ANYTHING ON A MACHINE WHERE THE CONTAINER CANNOT
		// LAUNCH. It asserts the fail-loud/degrade contract for a REQUESTED
		// container that cannot start; where one CAN start, the correct
		// behaviour is to start it, and asserting an abort would be asserting a
		// bug.
		//
		// It passed for years on machines that HAVE docker, and not because the
		// runtime was unreachable: the mock backend had no container-auth
		// profile, so resolveAuth failed closed and THAT produced the exit 3.
		// The moment mock gained a profile the run launched a real container and
		// this row went red — the scenario had been exercising the auth gate
		// while claiming to exercise the runtime gate. Skipping loudly here is
		// the honest reading; a green that depends on an unrelated failure is
		// worth less than a skip that says why.
		if rt, _, _ := containercell.Select(c, "j9's container fail-loud row"); rt.Available {
			fmt.Printf("SKIPPED (j9 container fail-loud row): a container runtime IS reachable here, so a requested container legitimately launches; this row asserts the CANNOT-launch contract\n")
			return godog.ErrSkip
		}
		// Fresh record path per invocation, same as the workspace-axis "mock
		// agent" step above -- the mock engine writes cwd=/workdir= to this
		// file regardless of whether it ends up running in a container or
		// (degraded) on the host, which is what lets "the run runs on the
		// host" actually check WHERE it ran rather than only that it exited 0.
		recPath := filepath.Join(j9.recordDir, fmt.Sprintf("record-%d.txt", len(j9.records)))
		if err := w.env.WriteHomeFile(".ctxloom/config.yaml", j9HomeConfigYAML(recPath)); err != nil {
			return err
		}
		j9.lastContainerRecPath = recPath
		args := []string{"run", "--agent", "mock-container", "--print"}
		if flags != "" {
			args = append(args, strings.Fields(flags)...)
		}
		args = append(args, "isolation-check")
		_ = w.env.Run(args...) // exit status asserted by the Then step below
		return nil
	})

	ctx.Step(`^the run aborts with an isolation finding$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		w.docStepMaterialized = fmt.Sprintf("exit=%d\n%s", w.env.LastExitCode(), strings.TrimSpace(out))
		if code := w.env.LastExitCode(); code != 3 {
			return fmt.Errorf("expected exit 3 (fatal isolation finding), got %d; output:\n%s", code, out)
		}
		if !strings.Contains(out, "container isolation") {
			return fmt.Errorf("output does not name the dropped container isolation boundary; output:\n%s", out)
		}
		if !strings.Contains(out, "--degraded") {
			return fmt.Errorf("output does not carry the --degraded escape hatch; output:\n%s", out)
		}
		return nil
	})

	ctx.Step(`^the run runs on the host$`, func(c context.Context) error {
		w := worldFrom(c)
		j9 := j9Of(w)
		out := w.env.LastOutput()
		w.docStepMaterialized = fmt.Sprintf("exit=%d\n%s", w.env.LastExitCode(), strings.TrimSpace(out))
		if code := w.env.LastExitCode(); code != 0 {
			return fmt.Errorf("expected exit 0 (degraded host run), got %d; output:\n%s", code, out)
		}
		// Exit 0 alone does not prove WHERE the run executed -- a
		// silently container-bound (or nowhere-run) "success" would pass this
		// check too, in a journey whose entire subject is where the run
		// landed. Read the mock's own recorded workdir=, the same observable
		// the sibling workspace-axis step already trusts (:150-154), and
		// require it to equal the project dir (the host boundary).
		if j9.lastContainerRecPath == "" {
			return fmt.Errorf("no container-bound run was recorded yet")
		}
		body, err := os.ReadFile(j9.lastContainerRecPath)
		if err != nil {
			return fmt.Errorf("read mock record %q: %w", j9.lastContainerRecPath, err)
		}
		_, workDir := j9ParseRecord(string(body))
		w.docStepMaterialized += fmt.Sprintf("\nworkdir=%s", workDir)
		if workDir != w.env.ProjectDir {
			return fmt.Errorf("expected the degraded run's workdir to be the project dir %q (the host), got %q", w.env.ProjectDir, workDir)
		}
		return nil
	})
}
