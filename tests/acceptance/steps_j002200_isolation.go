//go:build acceptance

// J002200: "the isolation seam's two axes actually land where they say they will"
// (j002200_isolation.feature). The workspace axis (none|worktree) is proven via
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
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport/containercell"
)

// j002200Record is one "Alice runs the mock agent under workspace ..." step's
// observed outcome: the record file's cwd= (the plugin subprocess's own,
// isolation-invariant OS cwd — kept for diagnostics only) and workdir= (the
// value isolation.Prepare actually resolved and threaded through
// RunOptions.WorkDir — the honest workspace-boundary signal).
type j002200Record struct {
	Workspace string
	Prompt    string
	Cwd       string
	WorkDir   string
}

// j002200State is J002200's fixture state: the scratch dir holding one record file per
// run (a fresh path per invocation — the A.3 "sequential" approach: no mock/
// harness concurrency change needed, each run mints its own harp and its own
// scratch), and every run's observed record in order.
type j002200State struct {
	recordDir string
	records   []j002200Record
	// lastContainerRecPath is the mock-container run's own record file:
	// distinct from records above, which only the workspace-axis "mock agent"
	// steps append to. Lets "the run runs on the host" check WHERE the
	// degraded run actually executed, not merely that it exited 0.
	lastContainerRecPath string
}

func j002200Of(w *World) *j002200State {
	if w.j002200 == nil {
		w.j002200 = &j002200State{recordDir: filepath.Join(w.env.Root, "j002200-records")}
	}
	return w.j002200
}

// j002200ConfigYAML renders the PROJECT half of config.yaml for J002200's fixture: one
// "fast" mock LLM label and two agent bindings — "mock" (host runtime, the
// default) for the workspace-axis scenarios, and "mock-container" (runtime:
// container) for the runtime-axis degrade scenario. Static (no per-run
// record path), so it is written once; j002200HomeConfigYAML carries the part
// that changes per run (mirrors j002100RenderConfig's direct config.yaml
// authoring rather than the MockLM helper, which does not preserve an
// "agents:" section).
//
// llm.configs.fast.env used to live HERE, in the project file, alongside
// type. It moved to the HOME half (j002200HomeConfigYAML): llm.configs.*.env is
// ScopeMachine (internal/config/layerscope) — credential passthrough, where
// a committed project-file value is a leaked secret — so a project-declared
// env block no longer survives a real Load at all. Splitting the "fast"
// label across layers like this is legal (unlike agents.*, llm.configs.*
// has no atomic-replace merge rule), so the project's own `type: mock` and
// home's `env` deep-merge into one usable entry.
func j002200ConfigYAML() string {
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
    llm: fast
    profiles: []
  mock-container:
    llm: fast
    profiles: []
    runtime: container
`
}

// j002200HomeConfigYAML renders the HOME half of config.yaml for J002200's fixture:
// llm.configs.fast.env, pointed at recordFile. Rewritten before EVERY run
// with a fresh recordFile so sequential runs never clobber each other's
// evidence — see j002200ConfigYAML's doc for why this piece lives in HOME.
func j002200HomeConfigYAML(recordFile string) string {
	return fmt.Sprintf(`version: 4
llm:
  configs:
    fast:
      env:
        CTXLOOM_MOCK_RECORD_FILE: %q
`, recordFile)
}

// The two ClassIsolation findings a requested-but-unsatisfied container can
// produce. They come from DIFFERENT gates and the fail-loud row is about
// exactly one of them, so it reads them apart rather than matching whichever
// mentions containers.
const (
	// j002200RuntimeGateFinding is chainFor's runtime-unreachable finding
	// (internal/lm/isolation/isolation.go): no docker/podman resolves, so no
	// container policy is ever selected. This row's subject.
	j002200RuntimeGateFinding = "container requested but no container runtime is available"
	// j002200StartGateFinding is prepareChain's could-not-START finding: a
	// container policy WAS selected and then failed (image absent, shared-fs
	// probe, unresolvable auth). A different contract, asserted elsewhere.
	j002200StartGateFinding = "container isolation was requested but could not start"
)

// j002200RuntimeCLIs are the container-runtime binary names isolation's
// selector resolves through exec.LookPath (isolation.SelectRuntime's byName
// map / runtimeReachable). Masking means: none of these resolves.
var j002200RuntimeCLIs = []string{"docker", "podman"}

// j002200HostToolsNeeded are the host binaries a `ctxloom run` still has to be
// able to find once PATH has been rebuilt — kept as a short, explicit
// allowlist so the mask is a decision about what is REMOVED (the runtimes)
// rather than an accident of what happened to be inherited.
var j002200HostToolsNeeded = []string{"git"}

// j002200MaskContainerRuntime rebuilds PATH so no container runtime resolves
// for the rest of the scenario, reproducing the fail-loud row's precondition
// — a REQUESTED container that genuinely cannot launch — instead of stubbing
// it.
//
// It works because both sides read the same channel: the spawned `ctxloom`
// inherits this process's PATH (TestEnvironment.Run builds the child env from
// os.Environ()), and inside it isolation.SelectRuntime -> newDockerRuntime ->
// runtimeReachable calls exec.LookPath. No package var is substitutable across
// that subprocess boundary, so PATH is the seam.
//
// The mask is a scenario-private dir ALONE, holding symlinks to
// j002200HostToolsNeeded. Filtering the inherited PATH would not do: docker
// ships in /usr/bin on an ordinary Linux box, the same directory git ships in,
// so there is no directory to drop.
//
// It FAILS rather than proceeding when a runtime still resolves. Without that,
// a mask that quietly stopped working would hand the row back to the auth gate
// and it would still be green.
func j002200MaskContainerRuntime(w *World) error {
	binDir := filepath.Join(w.env.Root, "j002200-no-runtime-bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("create runtime-masked bin dir: %w", err)
	}
	for _, tool := range j002200HostToolsNeeded {
		src, err := exec.LookPath(tool)
		if err != nil {
			return fmt.Errorf("j002200 runtime mask needs %q on the host PATH: %w", tool, err)
		}
		link := filepath.Join(binDir, tool)
		if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("clear stale %s link: %w", tool, err)
		}
		if err := os.Symlink(src, link); err != nil {
			return fmt.Errorf("symlink %s into the runtime-masked bin dir: %w", tool, err)
		}
	}
	w.env.SetEnv("PATH", isoSanitizedPATH(binDir))
	for _, cli := range j002200RuntimeCLIs {
		if found, err := exec.LookPath(cli); err == nil {
			return fmt.Errorf("PATH mask failed: %q still resolves to %q, so this row would assert the fail-loud contract on a machine that can launch a container", cli, found)
		}
	}
	return nil
}

// j002200ParseRecord extracts the cwd= and workdir= lines the mock's
// recordMockInput (internal/lm/backends/mock.go) wrote to its record file.
func j002200ParseRecord(body string) (cwd, workDir string) {
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

func registerJ002200Steps(ctx *godog.ScenarioContext) {
	ctx.Step(`^Alice has a git-backed project with a mock agent$`, func(c context.Context) error {
		w := worldFrom(c)
		j002200 := j002200Of(w)
		if err := w.env.InitGitRepo(); err != nil {
			return err
		}
		if err := os.MkdirAll(j002200.recordDir, 0o755); err != nil {
			return fmt.Errorf("create record dir: %w", err)
		}
		if err := w.env.WriteFile(".ctxloom/config.yaml", j002200ConfigYAML()); err != nil {
			return err
		}
		// A throwaway initial record path — every run step below rewrites
		// the HOME half of config.yaml with its own fresh record file before
		// invoking `run`.
		if err := w.env.WriteHomeFile(".ctxloom/config.yaml", j002200HomeConfigYAML(filepath.Join(j002200.recordDir, "init.txt"))); err != nil {
			return err
		}
		return w.env.GitCommit("initial ctxloom config")
	})

	ctx.Step(`^Alice runs the mock agent under workspace "([^"]*)" with prompt "([^"]*)"$`, func(c context.Context, workspace, prompt string) error {
		w := worldFrom(c)
		j002200 := j002200Of(w)
		recPath := filepath.Join(j002200.recordDir, fmt.Sprintf("record-%d.txt", len(j002200.records)))
		if err := w.env.WriteHomeFile(".ctxloom/config.yaml", j002200HomeConfigYAML(recPath)); err != nil {
			return err
		}
		if err := runOK(w, "run", "--agent", "mock", "--workspace", workspace, "--one-shot", prompt); err != nil {
			return err
		}
		body, err := os.ReadFile(recPath)
		if err != nil {
			return fmt.Errorf("read mock record %q: %w", recPath, err)
		}
		cwd, workDir := j002200ParseRecord(string(body))
		j002200.records = append(j002200.records, j002200Record{Workspace: workspace, Prompt: prompt, Cwd: cwd, WorkDir: workDir})
		return nil
	})

	ctx.Step(`^the run's workdir reflects the "([^"]*)" workspace axis$`, func(c context.Context, workspace string) error {
		w := worldFrom(c)
		j002200 := j002200Of(w)
		if len(j002200.records) == 0 {
			return fmt.Errorf("no run recorded yet")
		}
		last := j002200.records[len(j002200.records)-1]
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
			return fmt.Errorf("j002200: unknown workspace axis %q", workspace)
		}
		return nil
	})

	ctx.Step(`^the two runs' workdirs are distinct and neither is under the project directory$`, func(c context.Context) error {
		w := worldFrom(c)
		j002200 := j002200Of(w)
		if len(j002200.records) < 2 {
			return fmt.Errorf("expected at least 2 runs, got %d", len(j002200.records))
		}
		a, b := j002200.records[len(j002200.records)-2], j002200.records[len(j002200.records)-1]
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
		j002200 := j002200Of(w)
		// THIS ROW ONLY MEANS ANYTHING WHERE THE REQUESTED CONTAINER CANNOT
		// LAUNCH — it asserts the fail-loud/degrade contract for exactly that
		// case, and where a container CAN start the correct behaviour is to
		// start it. So the row MANUFACTURES the cannot-launch condition instead
		// of waiting for a machine that happens to have it: PATH is rebuilt to
		// hold no container runtime, which is the real, unstubbed reason
		// isolation.SelectRuntime returns Host (it reaches docker/podman
		// through exec.LookPath, same as the harness does).
		//
		// Do not restore a "skip where a runtime is reachable" guard here. That
		// guard made the row unrunnable on every developer box and on CI, while
		// godog counted the all-skipped scenario as PASSED. Worse, the green it
		// replaced was itself false: the mock backend had no container-auth
		// profile, so resolveAuth failed closed and THAT produced the exit 3 —
		// the row asserted the runtime gate while riding the auth gate. Masking
		// PATH is what puts the assertion back on the gate it names.
		if err := j002200MaskContainerRuntime(w); err != nil {
			return err
		}
		// Fresh record path per invocation, same as the workspace-axis "mock
		// agent" step above -- the mock engine writes cwd=/workdir= to this
		// file regardless of whether it ends up running in a container or
		// (degraded) on the host, which is what lets "the run runs on the
		// host" actually check WHERE it ran rather than only that it exited 0.
		recPath := filepath.Join(j002200.recordDir, fmt.Sprintf("record-%d.txt", len(j002200.records)))
		if err := w.env.WriteHomeFile(".ctxloom/config.yaml", j002200HomeConfigYAML(recPath)); err != nil {
			return err
		}
		j002200.lastContainerRecPath = recPath
		args := []string{"run", "--agent", "mock-container", "--one-shot"}
		if flags != "" {
			args = append(args, strings.Fields(flags)...)
		}
		args = append(args, "isolation-check")
		_ = w.env.Run(args...) // exit status asserted by the Then step below
		return nil
	})

	// The POSITIVE container-launch row (unripe-juiciness). Unlike the fail-loud
	// row above — which means something only where a container CANNOT launch —
	// this one means something only where one CAN, so it self-skips loudly
	// otherwise. It is tagged @container: excluded from the default hermetic run,
	// gated by `just test-acceptance-container`, and it launches the mock in a
	// REAL container against a real daemon + built image.
	ctx.Step(`^Alice runs the container-bound agent in a real container$`, func(c context.Context) error {
		w := worldFrom(c)
		j002200 := j002200Of(w)
		rt, _, msg := containercell.Select(c, "j002200's container credential shared-identity row")
		if !rt.Available {
			fmt.Printf("SKIPPED (j002200 container shared-identity row): no container runtime reachable here (%s)\n", msg)
			return godog.ErrSkip
		}
		// The record MUST live inside the project workspace: a containerized
		// engine can only write where the container can see, and ctxloom mounts
		// the project at the SAME absolute path — so this host path IS the path
		// the container writes to, through a read-write bind mount. That shared
		// identity is the whole point (asserted by the Then step). A fresh name
		// keeps a stale prior record from reading as this run's evidence.
		recPath := filepath.Join(w.env.ProjectDir, "j002200-container-record.txt")
		if err := os.Remove(recPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("clear stale record %s: %w", recPath, err)
		}
		if err := w.env.WriteHomeFile(".ctxloom/config.yaml", j002200HomeConfigYAML(recPath)); err != nil {
			return err
		}
		j002200.lastContainerRecPath = recPath
		_ = w.env.Run("run", "--agent", "mock-container", "--one-shot", "credential-mount-check")
		if code := w.env.LastExitCode(); code != 0 {
			return fmt.Errorf("containerized mock run exited %d; output:\n%s", code, w.env.LastOutput())
		}
		return nil
	})

	ctx.Step(`^the engine's in-container write is the same file the host holds$`, func(c context.Context) error {
		w := worldFrom(c)
		j002200 := j002200Of(w)
		host, err := os.Hostname()
		if err != nil {
			return fmt.Errorf("read this host's name: %w", err)
		}
		if j002200.lastContainerRecPath == "" {
			return fmt.Errorf("no containerized run was recorded yet")
		}
		body, err := os.ReadFile(j002200.lastContainerRecPath)
		if err != nil {
			// The characteristic silent no-op guard: exit 0 with no payload. If
			// the record is not here, the engine's in-container write never
			// reached the host through the read-write bind mount — so it is NOT
			// the same file, which is exactly the safety property under test.
			return fmt.Errorf("no host-visible record at %s: the engine's in-container write never reached the host through the read-write bind mount (%w)", j002200.lastContainerRecPath, err)
		}
		rec := j002400ParseRecord(string(body))
		if rec.Hostname == "" {
			return fmt.Errorf("record carries no hostname line — cannot show it was written inside the container:\n%s", body)
		}
		if rec.Hostname == host {
			return fmt.Errorf("the engine reported this host's own name (%q); the run did not land in a container, so a host-path read proves nothing about a cross-boundary shared file", host)
		}
		// Written from INSIDE the container (its own UTS namespace → a different
		// hostname) and read here at the HOST path: one file, reached from both
		// sides through ctxloom's read-write bind mount, NOT a copy. That is
		// exactly the property claude's real-credential container mount now
		// relies on — a write on the container side IS the write on the host
		// side, so the container's single-use token refresh lands in the one
		// file the host holds and never desyncs it. The credential file itself
		// is not exercised here (the mock authenticates against nothing); its
		// mount SOURCE = real ~/.claude/.credentials.json, rw, refresh present is
		// pinned in auth_test.go, and a real claude refreshing in place is the
		// @live isolation probe. See this scenario's feature-file note.
		w.docStepMaterialized = fmt.Sprintf("in-container write (hostname=%s, this host=%s) read at host path %s — same file both sides via a read-write bind mount, not a copy", rec.Hostname, host, j002200.lastContainerRecPath)
		return nil
	})

	ctx.Step(`^the run aborts with an isolation finding$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		w.docStepMaterialized = fmt.Sprintf("exit=%d\n%s", w.env.LastExitCode(), strings.TrimSpace(out))
		if code := w.env.LastExitCode(); code != 3 {
			return fmt.Errorf("expected exit 3 (fatal isolation finding), got %d; output:\n%s", code, out)
		}
		// WHICH gate produced the abort is the whole point, so the abort is
		// read for the RUNTIME-unreachable wording specifically
		// (isolation.go's chainFor, the WantsContainer/Host branch), not for
		// the generic word "container".
		if !strings.Contains(out, j002200RuntimeGateFinding) {
			return fmt.Errorf("output does not name the RUNTIME gate (%q) — the abort came from somewhere else; output:\n%s", j002200RuntimeGateFinding, out)
		}
		// The sibling gate, deeper in prepareChain: a container policy that was
		// selected and then failed to START (image absent, shared-fs probe,
		// unresolvable auth). A generic substring check used to be satisfied by
		// THIS message on every box with docker installed, which is how the row
		// spent its life asserting the auth gate while claiming the runtime one.
		// Naming it as a NEGATIVE keeps that confusion from returning silently.
		if strings.Contains(out, j002200StartGateFinding) {
			return fmt.Errorf("the abort came from the container START gate (%q), not the runtime-unreachable gate this row asserts; output:\n%s", j002200StartGateFinding, out)
		}
		class := "[" + string(strictness.ClassIsolation) + "]"
		if !strings.Contains(out, class) {
			return fmt.Errorf("output does not classify the abort as %s; output:\n%s", class, out)
		}
		if !strings.Contains(out, "--degraded") {
			return fmt.Errorf("output does not carry the --degraded escape hatch; output:\n%s", out)
		}
		return nil
	})

	ctx.Step(`^the run runs on the host$`, func(c context.Context) error {
		w := worldFrom(c)
		j002200 := j002200Of(w)
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
		if j002200.lastContainerRecPath == "" {
			return fmt.Errorf("no container-bound run was recorded yet")
		}
		body, err := os.ReadFile(j002200.lastContainerRecPath)
		if err != nil {
			return fmt.Errorf("read mock record %q: %w", j002200.lastContainerRecPath, err)
		}
		_, workDir := j002200ParseRecord(string(body))
		w.docStepMaterialized += fmt.Sprintf("\nworkdir=%s", workDir)
		if workDir != w.env.ProjectDir {
			return fmt.Errorf("expected the degraded run's workdir to be the project dir %q (the host), got %q", w.env.ProjectDir, workDir)
		}
		return nil
	})
}
