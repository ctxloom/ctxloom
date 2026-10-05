//go:build acceptance

// P17, INLINE SETTINGS (capability_inline_settings.feature): the godog half
// of the rung. What the cell proves and why is in probe_p17_inline_settings.go;
// this file gates the cell, lays out the fixture and spends the one turn.
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
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"

	"github.com/cucumber/godog"
)

// p17RunTimeout bounds the one turn: two echo calls.
const p17RunTimeout = 120 * time.Second

// p17State is the cell's fixture and captured run.
type p17State struct {
	engine, runtime, workspace string

	claudePath string
	cred       credentialMapping // never printed
	dir        string            // the cell's root: markers live here, outside the repo
	repo       string            // cwd: a fresh git repo
	cfg, home  string            // throwaway CLAUDE_CONFIG_DIR (the session home) and HOME

	outcome p17Outcome
}

func p17Of(w *World) *p17State {
	if w.p17 == nil {
		w.p17 = &p17State{}
	}
	return w.p17
}

func (p *p17State) cell() probeCellID {
	return probeCellID{Probe: probeP17, Engine: p.engine, Runtime: p.runtime, Workspace: p.workspace}
}

func registerP17InlineSettingsSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^the inline-settings probe targets "([^"]*)" under runtime "([^"]*)" and workspace "([^"]*)"$`,
		func(c context.Context, engine, runtime, workspace string) error {
			w := worldFrom(c)
			p := p17Of(w)
			if runtime != "host" || workspace != "none" {
				return fmt.Errorf("%s: axes %s/%s — this rung is host/none only: it runs the vendor binary directly", p17Family, runtime, workspace)
			}
			p.engine, p.runtime, p.workspace = engine, runtime, workspace
			a, cred, err := probeDirectCellGate(w, p17Family, p.cell())
			if err != nil {
				return err
			}
			p.cred = cred
			if p.claudePath, err = exec.LookPath(a.binary); err != nil {
				return err
			}
			return p17Fixture(w, p)
		})

	ctx.Step(`^it runs one turn with user settings in the config home and an inline JSON --settings$`, func(c context.Context) error {
		p := p17Of(worldFrom(c))
		if p.dir == "" {
			return errP12NotRun
		}
		runCtx, cancel := context.WithTimeout(c, p17RunTimeout)
		defer cancel()
		cmd := exec.CommandContext(runCtx, p.claudePath, p17Args()...)
		cmd.Dir = p.repo
		cmd.Env = liveVendorEnv(p.cred, p.home, p.cfg)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		o := p17Outcome{Cell: p.cell(), Started: true, TimedOut: errors.Is(runCtx.Err(), context.DeadlineExceeded),
			Run: probeRun{Stdout: stdout.String(), Stderr: stderr.String(), Err: err, ExitCode: probeExitCode(err)}, Fired: map[string]bool{}}
		for event, marker := range p13Markers {
			switch _, err := os.Stat(filepath.Join(p.dir, marker)); {
			case err == nil:
				o.Fired[event] = true
			case !errors.Is(err, fs.ErrNotExist):
				o.MarkerErr = err
			}
		}
		p.outcome = o
		fmt.Printf("EVIDENCE %s %s: exit=%d timedOut=%t fired=%v stdout=%dB stderr=%dB\n",
			p17Family, p.cell(), o.Run.ExitCode, o.TimedOut, o.Fired, len(o.Run.Stdout), len(o.Run.Stderr))
		return nil
	})

	ctx.Step(`^both settings sources apply to the turn$`, func(c context.Context) error {
		p := p17Of(worldFrom(c))
		if p.dir == "" {
			return errP12NotRun
		}
		return p17Assert(p.outcome)
	})
}

// p17Fixture lays the cell out under the scenario's temp root:
// <dir>/{cfg,home,repo}, with the user settings in cfg — the config home —
// and the markers in dir, outside the repo.
func p17Fixture(w *World, p *p17State) error {
	p.dir = filepath.Join(w.env.Root, "p17")
	p.repo, p.cfg, p.home = filepath.Join(p.dir, "repo"), filepath.Join(p.dir, "cfg"), filepath.Join(p.dir, "home")
	for _, d := range []string{p.repo, p.cfg, p.home} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	if out, err := taskstest.GitCmd(filepath.Dir(p.repo), nil, "init", "-q", p.repo).CombinedOutput(); err != nil {
		return fmt.Errorf("%s: git init %s: %w: %s", p17Family, p.repo, err, out)
	}
	settings, err := p17HomeSettingsJSON(p.dir)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(p.cfg, p17HomeSettingsName), settings, 0o600)
}
