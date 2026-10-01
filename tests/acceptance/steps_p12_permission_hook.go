//go:build acceptance

// P12, THE NO-HOST PERMISSION HOOK (capability_permission_hook.feature): the
// godog half of the rung. What the cell proves and why it exists is in
// probe_p12_permission_hook.go, with the verdicts; this file gates the cell,
// lays out the fixture, spends the one turn and hands the observation over.
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
	"slices"
	"time"

	"github.com/cucumber/godog"
)

// p12RunTimeout bounds the one claude turn. Measured turns of this exact
// fixture took ~10s including the hook's own sleep; the bound is generous for
// a slow API without letting a hung engine hold a bounded live lane.
const p12RunTimeout = 90 * time.Second

// p12State is one cell's fixture and captured run.
type p12State struct {
	engine, runtime, workspace string
	decision                   p12Decision

	claudePath string
	cred       credentialMapping // the launch credential (liveCredential); never printed
	dir        string            // the cell's root: hook, settings, marker, proof
	repo       string            // cwd: a fresh git repo
	cfg, home  string            // throwaway CLAUDE_CONFIG_DIR and HOME

	run      probeRun
	started  bool
	timedOut bool
}

func p12Of(w *World) *p12State {
	if w.p12 == nil {
		w.p12 = &p12State{}
	}
	return w.p12
}

func (p *p12State) cell() probeCellID {
	return probeCellID{Probe: probeP12, Engine: p.engine, Runtime: p.runtime, Workspace: p.workspace, Variant: string(p.decision)}
}

func registerP12PermissionHookSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^the permission-hook probe targets "([^"]*)" under runtime "([^"]*)" and workspace "([^"]*)" with the hook answering "([^"]*)"$`,
		func(c context.Context, engine, runtime, workspace, decision string) error {
			w := worldFrom(c)
			p := p12Of(w)
			if runtime != "host" || workspace != "none" {
				return fmt.Errorf("%s: axes %s/%s — this rung is host/none only: the claim is about the vendor binary, which the cell runs directly, so neither ctxloom isolation axis is in play", p12Family, runtime, workspace)
			}
			if !slices.Contains(p12Decisions, p12Decision(decision)) {
				return fmt.Errorf("%s: unknown decision %q (want one of %v)", p12Family, decision, p12Decisions)
			}
			p.engine, p.runtime, p.workspace, p.decision = engine, runtime, workspace, p12Decision(decision)

			a, _, err := probeCellGate(c, w, p12Family, p.cell())
			if err != nil {
				return err
			}
			// The shared gate also passes an engine authenticated only by its
			// subscription login. This cell cannot use one: its CLAUDE_CONFIG_DIR
			// and HOME are throwaway, which is the isolation the cell requires,
			// so only a token captured at launch can authenticate it.
			var ok bool
			if p.cred, ok = liveCredential(a); !ok {
				return probeCellSkip(p12Family, p.cell(), fmt.Sprintf(
					"this cell runs claude in a throwaway config dir and HOME, so it needs a token captured at launch (one of %v); none was", a.apiKeyEnvs))
			}
			if p.claudePath, err = exec.LookPath(a.binary); err != nil {
				return err
			}
			return p12Fixture(w, p)
		})

	ctx.Step(`^it asks the engine to touch a file outside its working directory in one turn$`, func(c context.Context) error {
		p := p12Of(worldFrom(c))
		if p.dir == "" {
			return errP12NotRun
		}
		runCtx, cancel := context.WithTimeout(c, p12RunTimeout)
		defer cancel()
		args := append([]string{
			"-p", p12Prompt(filepath.Join(p.dir, p12ProofName)),
			"--settings", filepath.Join(p.dir, p12SettingsName),
			"--output-format", "stream-json", "--verbose",
			"--model", liveClaudeModel,
		}, p.decision.argv()...)
		cmd := exec.CommandContext(runCtx, p.claudePath, args...)
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
			p12Family, p.cell(), p.run.ExitCode, p.timedOut, len(p.run.Stdout), len(p.run.Stderr))
		return nil
	})

	ctx.Step(`^the gated call honours the hook's "([^"]*)" decision$`, func(c context.Context, decision string) error {
		p := p12Of(worldFrom(c))
		if p.dir == "" {
			return errP12NotRun
		}
		if p12Decision(decision) != p.decision {
			return fmt.Errorf("%s %s: the Then step asks for %q but the cell's hook answers %q", p12Family, p.cell(), decision, p.decision)
		}
		o := p12Outcome{Cell: p.cell(), Decision: p.decision, Started: p.started, TimedOut: p.timedOut, Run: p.run}
		o.HookInput, o.HookInputErr = os.ReadFile(filepath.Join(p.dir, p12HookInputName))
		if st, err := os.Stat(filepath.Join(p.dir, p12MarkerName)); err != nil {
			o.MarkerErr = err
		} else {
			o.MarkerAt = st.ModTime()
		}
		switch _, err := os.Stat(filepath.Join(p.dir, p12ProofName)); {
		case err == nil:
			o.ProofExists = true
		case !errors.Is(err, fs.ErrNotExist):
			o.ProofErr = err
		}
		return p12Assert(o)
	})
}

// p12Fixture lays the cell out under the scenario's own temp root, which the
// harness removes: <dir>/{cfg,home,repo} plus the hook and the settings file,
// both OUTSIDE the repo so the engine cannot read or edit them as project
// files. Nothing is written anywhere else.
func p12Fixture(w *World, p *p12State) error {
	p.dir = filepath.Join(w.env.Root, "p12-"+string(p.decision))
	p.repo, p.cfg, p.home = filepath.Join(p.dir, "repo"), filepath.Join(p.dir, "cfg"), filepath.Join(p.dir, "home")
	for _, d := range []string{p.repo, p.cfg, p.home} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	if out, err := exec.Command("git", "init", "-q", p.repo).CombinedOutput(); err != nil {
		return fmt.Errorf("%s: git init %s: %w: %s", p12Family, p.repo, err, out)
	}
	hookPath := filepath.Join(p.dir, p12HookName)
	script, err := p12HookScript(p.dir, p.decision)
	if err != nil {
		return err
	}
	if err := os.WriteFile(hookPath, []byte(script), 0o700); err != nil {
		return err
	}
	settings, err := p12SettingsJSON(hookPath)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(p.dir, p12SettingsName), settings, 0o600)
}
