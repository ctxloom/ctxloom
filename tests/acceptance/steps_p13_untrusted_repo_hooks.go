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

	"github.com/cucumber/godog"
)

// p13RunTimeout bounds the one claude turn: the fixture's turn is one allowed
// echo, and the bound is P12's.
const p13RunTimeout = p12RunTimeout

// p13State is one cell's fixture and captured run.
type p13State struct {
	engine, runtime, workspace string
	variant                    p13Variant

	claudePath string
	cred       credentialMapping // the launch credential (liveCredential); never printed
	dir        string            // the cell's root: the markers live here, outside the repo
	repo       string            // cwd: a git repo with a COMMITTED .claude/settings.json
	cfg, home  string            // throwaway CLAUDE_CONFIG_DIR and HOME; cfg holds no .claude.json, so the repo is untrusted

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
			case p13Fires, p13Suppresses:
			default:
				return fmt.Errorf("%s: unknown variant %q (want %q or %q)", p13Family, variant, p13Fires, p13Suppresses)
			}
			p.engine, p.runtime, p.workspace, p.variant = engine, runtime, workspace, p13Variant(variant)

			a, _, err := probeCellGate(c, w, p13Family, p.cell())
			if err != nil {
				return err
			}
			// Untrusted means a CLAUDE_CONFIG_DIR with no projects entry, so the
			// config dir must be throwaway — and a throwaway config dir cannot
			// use the subscription login. Only a token captured at launch can
			// authenticate this cell.
			var ok bool
			if p.cred, ok = liveCredential(a); !ok {
				return probeCellSkip(p13Family, p.cell(), fmt.Sprintf(
					"this cell runs claude in a throwaway config dir and HOME, so it needs a token captured at launch (one of %v); none was", a.apiKeyEnvs))
			}
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
		cmd := exec.CommandContext(runCtx, p.claudePath, p13Args(p.variant)...)
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
		o := p13Outcome{Cell: p.cell(), Variant: p.variant, Started: p.started, TimedOut: p.timedOut, Run: p.run, Fired: map[string]bool{}}
		for event, marker := range p13Markers {
			switch _, err := os.Stat(filepath.Join(p.dir, marker)); {
			case err == nil:
				o.Fired[event] = true
			case !errors.Is(err, fs.ErrNotExist):
				o.MarkerErr = err
			}
		}
		fmt.Printf("EVIDENCE %s %s: fired=%v\n", p13Family, p.cell(), o.Fired)
		return p13Assert(o)
	})
}

// errP13NotRun marks an outcome whose fixture never reached the run.
var errP13NotRun = errors.New(p13Family + ": the cell's fixture was not prepared")

// p13Fixture lays the cell out under the scenario's own temp root, which the
// harness removes: <dir>/{cfg,home,repo}, with the repo's .claude/settings.json
// COMMITTED — a repo that ships hooks, not a working-tree edit — and the
// markers its hooks write landing in <dir>, outside the repo. cfg is left
// empty: no .claude.json, so no projects entry, so the repo is untrusted.
func p13Fixture(w *World, p *p13State) error {
	p.dir = filepath.Join(w.env.Root, "p13-"+string(p.variant))
	p.repo, p.cfg, p.home = filepath.Join(p.dir, "repo"), filepath.Join(p.dir, "cfg"), filepath.Join(p.dir, "home")
	for _, d := range []string{filepath.Join(p.repo, filepath.Dir(p13RepoSettingsPath)), p.cfg, p.home} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	settings, err := p13RepoSettingsJSON(p.dir)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(p.repo, p13RepoSettingsPath), settings, 0o600); err != nil {
		return err
	}
	git := func(args ...string) error {
		cmd := exec.Command("git", append([]string{"-C", p.repo,
			"-c", "user.name=p13", "-c", "user.email=p13@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s: git %v: %w: %s", p13Family, args, err, out)
		}
		return nil
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", p13RepoSettingsPath},
		{"commit", "-q", "--no-verify", "-m", "p13 fixture: a repo that ships hooks"},
	} {
		if err := git(args...); err != nil {
			return err
		}
	}
	return nil
}
