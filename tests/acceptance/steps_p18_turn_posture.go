//go:build acceptance

// P18, TURN POSTURE (capability_turn_posture.feature): the godog half of the
// rung. What the cells prove and why is in probe_p18_turn_posture.go; this
// file gates the cell, lays out the fixture and spends the two turns.
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
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"

	"github.com/cucumber/godog"
)

// p18RunTimeout bounds each of the cell's two turns.
const p18RunTimeout = 120 * time.Second

// p18State is the cell's fixture and its captured turns.
type p18State struct {
	engine, runtime, workspace string
	variant                    p18Variant

	claudePath string
	cred       credentialMapping // never printed
	dir        string            // the cell's root: the hook and its asks live here, outside the repo
	repo       string            // cwd: a fresh git repo, where the turns write
	cfg, home  string            // throwaway CLAUDE_CONFIG_DIR (the session home) and HOME

	outcome p18Outcome
}

func p18Of(w *World) *p18State {
	if w.p18 == nil {
		w.p18 = &p18State{}
	}
	return w.p18
}

func (p *p18State) cell() probeCellID {
	return probeCellID{Probe: probeP18, Engine: p.engine, Runtime: p.runtime, Workspace: p.workspace, Variant: string(p.variant)}
}

func registerP18TurnPostureSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^the turn-posture probe targets "([^"]*)" under runtime "([^"]*)" and workspace "([^"]*)" for variant "([^"]*)"$`,
		func(c context.Context, engine, runtime, workspace, variant string) error {
			w := worldFrom(c)
			p := p18Of(w)
			if runtime != "host" || workspace != "none" {
				return fmt.Errorf("%s: axes %s/%s — this rung is host/none only: it runs the vendor binary directly", p18Family, runtime, workspace)
			}
			if !slices.Contains(p18Variants, p18Variant(variant)) {
				return fmt.Errorf("%s: unknown variant %q (want one of %v) — a cell no verdict arm judges would cost two turns and be scored by nothing", p18Family, variant, p18Variants)
			}
			p.engine, p.runtime, p.workspace, p.variant = engine, runtime, workspace, p18Variant(variant)
			a, cred, err := probeDirectCellGate(w, p18Family, p.cell())
			if err != nil {
				return err
			}
			p.cred = cred
			if p.claudePath, err = exec.LookPath(a.binary); err != nil {
				return err
			}
			return p18Fixture(w, p)
		})

	ctx.Step(`^it runs two turns of one session, the second resumed under its own settings$`, func(c context.Context) error {
		p := p18Of(worldFrom(c))
		if p.dir == "" {
			return errP12NotRun
		}
		o := p18Outcome{Cell: p.cell(), Variant: p.variant}
		session := ""
		for i, spec := range p.variant.turns(p.repo) {
			t, err := p18RunTurn(c, p, i+1, spec, session)
			if err != nil {
				return err
			}
			o.Turns[i] = t
			s, _ := p12Decode(t.Run.Stdout)
			if session = s.SessionID; session == "" {
				break // the verdict names the missing init frame; a turn 2 could resume nothing
			}
		}
		p.outcome = o
		fmt.Printf("EVIDENCE %s %s: %s\n", p18Family, p.cell(), o.summary())
		return nil
	})

	ctx.Step(`^each turn starts in, and behaves as, the mode its settings named$`, func(c context.Context) error {
		p := p18Of(worldFrom(c))
		if p.dir == "" {
			return errP12NotRun
		}
		return p18Assert(p.outcome)
	})
}

// p18RunTurn spends one turn and observes what it left: the asks its hook
// captured (moved aside to asks.turn<n>, so the next turn's are its own), the
// targets on disk and the plans in the config home.
func p18RunTurn(c context.Context, p *p18State, n int, spec p18TurnSpec, resume string) (p18TurnRun, error) {
	runCtx, cancel := context.WithTimeout(c, p18RunTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, p.claudePath, p18Args(spec, resume)...)
	cmd.Dir = p.repo
	cmd.Env = liveVendorEnv(p.cred, p.home, p.cfg)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	t := p18TurnRun{Started: true, TimedOut: errors.Is(runCtx.Err(), context.DeadlineExceeded),
		Run: probeRun{Stdout: stdout.String(), Stderr: stderr.String(), Err: err, ExitCode: probeExitCode(err)}, Landed: map[string]bool{}}

	asks := filepath.Join(p.dir, p18AsksDir)
	entries, err := os.ReadDir(asks)
	if err != nil {
		return t, err
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(asks, e.Name()))
		if err != nil {
			return t, err
		}
		t.Asks = append(t.Asks, b)
	}
	if err := os.Rename(asks, fmt.Sprintf("%s.turn%d", asks, n)); err != nil {
		return t, err
	}
	if err := os.Mkdir(asks, 0o700); err != nil {
		return t, err
	}
	for _, target := range []p18Target{p18First, p18Second} {
		b, err := os.ReadFile(filepath.Join(p.repo, target.Name))
		switch {
		case err == nil:
			t.Landed[target.Name] = strings.TrimSpace(string(b)) == target.Content
		case !errors.Is(err, fs.ErrNotExist):
			return t, err
		}
	}
	plans, err := filepath.Glob(filepath.Join(p.cfg, p18PlansDir, "*.md"))
	t.Plans = len(plans)
	return t, err
}

// p18Fixture lays the cell out under the scenario's temp root:
// <dir>/{cfg,home,repo,asks} and <dir>/hook.sh, with the hook registered in
// cfg — the config home's user settings, where ctxloom puts its hooks.
func p18Fixture(w *World, p *p18State) error {
	p.dir = filepath.Join(w.env.Root, "p18")
	p.repo, p.cfg, p.home = filepath.Join(p.dir, "repo"), filepath.Join(p.dir, "cfg"), filepath.Join(p.dir, "home")
	for _, d := range []string{p.repo, p.cfg, p.home, filepath.Join(p.dir, p18AsksDir)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	if out, err := taskstest.GitCmd(filepath.Dir(p.repo), nil, "init", "-q", p.repo).CombinedOutput(); err != nil {
		return fmt.Errorf("%s: git init %s: %w: %s", p18Family, p.repo, err, out)
	}
	answer, err := p.variant.hookAnswer()
	if err != nil {
		return err
	}
	hook := filepath.Join(p.dir, p18HookName)
	if err := os.WriteFile(hook, []byte(p18HookScript(p.dir, answer)), 0o700); err != nil {
		return err
	}
	settings, err := p18HomeSettingsJSON(hook)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(p.cfg, p17HomeSettingsName), settings, 0o600)
}
