//go:build acceptance

// P13, UNTRUSTED REPO HOOKS (capability_untrusted_repo_hooks.feature): the
// godog half of the rung. What the cells prove and why ctxloom depends on it is
// in probe_p13_untrusted_repo_hooks.go, with the verdicts; this file gates the
// cell, lays out the fixture, spends the one turn and hands the observation
// over.
package acceptance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// p13RunTimeout bounds the one claude turn: the fixture's turn is one allowed
// echo, and the bound is P12's.
const p13RunTimeout = p12RunTimeout

// p13State is one cell's fixture and captured run.
type p13State struct {
	engine, runtime, workspace string
	variant                    p13Variant

	claudePath string
	cred       credentialMapping // the launch credential (vendorCredential); never printed
	dir        string            // the cell's root: the markers live here, outside the repo
	repo       string            // cwd: a git repo with a COMMITTED .claude/settings.json, skill and agent
	cfg, home  string            // throwaway CLAUDE_CONFIG_DIR and HOME; cfg holds a .claude.json trusting the repo only in the trusted control
	launch     p13Launch         // the ctxloom-launch cell's engine-composed argv and stdin; zero in the vendor cells

	run      probeRun
	started  bool
	timedOut bool
}

func p13Of(w *World) *p13State {
	if w.p13 == nil {
		w.p13 = &p13State{}
	}
	return w.p13
}

func (p *p13State) cell() probeCellID {
	return probeCellID{Probe: probeP13, Engine: p.engine, Runtime: p.runtime, Workspace: p.workspace, Variant: string(p.variant)}
}

func registerP13UntrustedRepoHooksSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^the untrusted-repo-hooks probe targets "([^"]*)" under runtime "([^"]*)" and workspace "([^"]*)" in the "([^"]*)" posture$`,
		func(c context.Context, engine, runtime, workspace, variant string) error {
			w := worldFrom(c)
			p := p13Of(w)
			if runtime != "host" || workspace != "none" {
				return fmt.Errorf("%s: axes %s/%s — this rung is host/none only: the claim is about the vendor binary, which the cell runs directly, so neither ctxloom isolation axis is in play", p13Family, runtime, workspace)
			}
			switch p13Variant(variant) {
			case p13Fires, p13Suppresses, p13TrustedFrontmatter, p13CtxloomUntrusted:
			default:
				return fmt.Errorf("%s: unknown variant %q (want %q, %q, %q or %q)", p13Family, variant, p13Fires, p13Suppresses, p13TrustedFrontmatter, p13CtxloomUntrusted)
			}
			p.engine, p.runtime, p.workspace, p.variant = engine, runtime, workspace, p13Variant(variant)

			// The cell runs claude itself in a throwaway HOME and config dir,
			// so it takes what claude accepts, captured at launch, and never
			// the real home's login.
			a, cred, err := probeDirectCellGate(w, p13Family, p.cell())
			if err != nil {
				return err
			}
			p.cred = cred
			if p.claudePath, err = exec.LookPath(a.binary); err != nil {
				return err
			}
			return p13Fixture(w, p)
		})

	ctx.Step(`^it asks the engine to run echo hi in one turn$`, func(c context.Context) error {
		p := p13Of(worldFrom(c))
		if p.dir == "" {
			return errP13NotRun
		}
		runCtx, cancel := context.WithTimeout(c, p13RunTimeout)
		defer cancel()
		args := p13Args(p.variant)
		if p.variant == p13CtxloomUntrusted {
			args = p.launch.Args
		}
		cmd := exec.CommandContext(runCtx, p.claudePath, args...)
		if p.launch.Stdin != nil {
			cmd.Stdin = bytes.NewReader(p.launch.Stdin)
		}
		cmd.Dir = p.repo
		cmd.Env = liveVendorEnv(p.cred, p.home, p.cfg)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr

		p.started = true
		err := cmd.Run()
		p.timedOut = errors.Is(runCtx.Err(), context.DeadlineExceeded)
		p.run = probeRun{Stdout: stdout.String(), Stderr: stderr.String(), Err: err}
		var exitErr *exec.ExitError
		switch {
		case errors.As(err, &exitErr):
			p.run.ExitCode = exitErr.ExitCode()
		case err != nil:
			p.run.ExitCode = -1
		}
		fmt.Printf("EVIDENCE %s %s: exit=%d timedOut=%t stdout=%dB stderr=%dB\n",
			p13Family, p.cell(), p.run.ExitCode, p.timedOut, len(p.run.Stdout), len(p.run.Stderr))
		return nil
	})

	ctx.Step(`^the repo's committed hooks behave as the "([^"]*)" posture requires$`, func(c context.Context, variant string) error {
		p := p13Of(worldFrom(c))
		if p.dir == "" {
			return errP13NotRun
		}
		if p13Variant(variant) != p.variant {
			return fmt.Errorf("%s %s: the Then step asks for %q but the cell runs %q", p13Family, p.cell(), variant, p.variant)
		}
		o := p13Outcome{Cell: p.cell(), Variant: p.variant, Started: p.started, TimedOut: p.timedOut, Run: p.run, Fired: map[string]bool{}, Frontmatter: map[string]bool{}}
		for _, family := range []struct {
			markers  map[string]string
			observed map[string]bool
		}{{p13Markers, o.Fired}, {p13FrontmatterMarkers, o.Frontmatter}} {
			for key, marker := range family.markers {
				switch _, err := os.Stat(filepath.Join(p.dir, marker)); {
				case err == nil:
					family.observed[key] = true
				case !errors.Is(err, fs.ErrNotExist):
					o.MarkerErr = err
				}
			}
		}
		fmt.Printf("EVIDENCE %s %s: fired=%v frontmatter=%v\n", p13Family, p.cell(), o.Fired, o.Frontmatter)
		return p13Assert(o)
	})
}

// errP13NotRun marks an outcome whose fixture never reached the run.
var errP13NotRun = errors.New(p13Family + ": the cell's fixture was not prepared")

// p13Fixture lays the cell out under the scenario's own temp root, which the
// harness removes: <dir>/{cfg,home,repo}, with the repo's .claude/settings.json,
// skill and agent COMMITTED — a repo that ships them, not a working-tree edit —
// and the markers they write landing in <dir>, outside the repo. cfg is empty
// (no .claude.json, so no projects entry, so the repo is untrusted) except in
// the trusted control, whose .claude.json trusts the repo, and in the
// ctxloom-launch cell, where claude's own instance-config writer generates it
// for the verdict it took.
func p13Fixture(w *World, p *p13State) error {
	p.dir = filepath.Join(w.env.Root, "p13-"+string(p.variant))
	p.repo, p.cfg, p.home = filepath.Join(p.dir, "repo"), filepath.Join(p.dir, "cfg"), filepath.Join(p.dir, "home")
	for _, d := range []string{p.cfg, p.home} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	files := map[string]func() ([]byte, error){
		filepath.Join(p.repo, p13RepoSettingsPath): func() ([]byte, error) { return p13RepoSettingsJSON(p.dir) },
		filepath.Join(p.repo, p13RepoSkillPath):    func() ([]byte, error) { return p13SkillMD(p.dir) },
		filepath.Join(p.repo, p13RepoAgentPath):    func() ([]byte, error) { return p13AgentMD(p.dir) },
	}
	if p.variant == p13TrustedFrontmatter {
		files[filepath.Join(p.cfg, ".claude.json")] = func() ([]byte, error) { return p13TrustJSON(p.repo) }
	}
	for path, render := range files {
		if err := p13WriteFile(path, render); err != nil {
			return err
		}
	}
	git := func(args ...string) error {
		cmd := taskstest.GitCmd(p.repo, nil, append([]string{"-c", "user.name=p13", "-c", "user.email=p13@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s: git %v: %w: %s", p13Family, args, err, out)
		}
		return nil
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "--all"},
		{"commit", "-q", "--no-verify", "-m", "p13 fixture: a repo that ships hooks, a skill and an agent"},
	} {
		if err := git(args...); err != nil {
			return err
		}
	}
	if p.variant != p13CtxloomUntrusted {
		return nil
	}
	return p13ComposeLaunch(p)
}

// p13ComposeLaunch composes the ctxloom-launch cell after the commit, since
// the verdict walks the repo's .git. A verdict that is not untrusted means
// the human home trusts the fixture, and the turn would measure the trusted
// control instead — refused, not spent.
func p13ComposeLaunch(p *p13State) error {
	var err error
	if p.launch, err = p13CtxloomLaunch(p.home, p.cfg, p.repo); err != nil {
		return err
	}
	if p.launch.Verdict != engine.TrustUntrusted {
		return fmt.Errorf("%s %s: claude's verdict on the fixture repo is %d, not untrusted; the cell would not measure an untrusted repo", p13Family, p.cell(), p.launch.Verdict)
	}
	return nil
}

// p13WriteFile renders one fixture file and writes it, parents and all.
func p13WriteFile(path string, render func() ([]byte, error)) error {
	body, err := render()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o600)
}
