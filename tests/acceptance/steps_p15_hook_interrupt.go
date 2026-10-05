//go:build acceptance

// P15, HOOK INTERRUPT (capability_hook_interrupt.feature): the godog half of
// the rung. What the cell proves and why is in probe_p15_hook_interrupt.go,
// with the verdict; this file gates the cell, lays out the fixture, spends the
// one turn, interrupts it and reads the hook's fate.
package acceptance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/shared/procsig"
)

// p15PollEvery is how often the cell looks for the hook's pid file and, after
// claude exits, for the hook leaving /proc. Both are another process's state
// with no channel to wait on.
const p15PollEvery = 100 * time.Millisecond

// p15State is the cell's fixture and observation.
type p15State struct {
	engine, runtime, workspace string

	claudePath string
	cred       credentialMapping // never printed
	dir        string            // the cell's root: hook, settings, pid files, marker
	repo       string            // cwd: a fresh git repo
	cfg, home  string            // throwaway CLAUDE_CONFIG_DIR and HOME

	outcome p15Outcome
}

func p15Of(w *World) *p15State {
	if w.p15 == nil {
		w.p15 = &p15State{}
	}
	return w.p15
}

func (p *p15State) cell() probeCellID {
	return probeCellID{Probe: probeP15, Engine: p.engine, Runtime: p.runtime, Workspace: p.workspace}
}

func registerP15HookInterruptSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^the hook-interrupt probe targets "([^"]*)" under runtime "([^"]*)" and workspace "([^"]*)"$`,
		func(c context.Context, engine, runtime, workspace string) error {
			w := worldFrom(c)
			p := p15Of(w)
			if runtime != "host" || workspace != "none" {
				return fmt.Errorf("%s: axes %s/%s — this rung is host/none only: it runs the vendor binary directly", p15Family, runtime, workspace)
			}
			p.engine, p.runtime, p.workspace = engine, runtime, workspace
			a, cred, err := probeDirectCellGate(w, p15Family, p.cell())
			if err != nil {
				return err
			}
			p.cred = cred
			if p.claudePath, err = exec.LookPath(a.binary); err != nil {
				return err
			}
			return p15Fixture(w, p)
		})

	ctx.Step(`^it interrupts the engine while its PermissionRequest hook is blocked$`, func(c context.Context) error {
		p := p15Of(worldFrom(c))
		if p.dir == "" {
			return errP12NotRun
		}
		p.outcome = p15Run(p)
		fmt.Printf("EVIDENCE %s %s: %s\n", p15Family, p.cell(), p.outcome.summary())
		return nil
	})

	ctx.Step(`^the blocked hook dies with the engine$`, func(c context.Context) error {
		p := p15Of(worldFrom(c))
		if p.dir == "" {
			return errP12NotRun
		}
		return p15Assert(p.outcome)
	})
}

// p15Run spends the turn: start claude as the driver does, wait for the hook
// to block, interrupt, give claude the driver's grace, then read the hook.
// Whatever it observed, it leaves no fixture process running.
func p15Run(p *p15State) p15Outcome {
	o := p15Outcome{Cell: p.cell()}
	msg, err := p15UserMessage(p12Prompt(filepath.Join(p.dir, p12ProofName)))
	if err != nil {
		o.Run.Err = err
		return o
	}
	cmd := exec.Command(p.claudePath, p15Args(filepath.Join(p.dir, p12SettingsName))...)
	cmd.Dir = p.repo
	cmd.Env = liveVendorEnv(p.cred, p.home, p.cfg)
	cmd.SysProcAttr = procsig.SpawnAttr()
	cmd.Stdin = bytes.NewReader(msg)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		o.Run.Err = err
		return o
	}
	o.Started = true
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var waitErr error
	exited := false
	o.HookPID, o.HookStartErr, waitErr, exited = p15AwaitHook(filepath.Join(p.dir, p15HookPIDName), done)
	if !exited {
		interrupted := time.Now()
		if err := procsig.Interrupt(cmd.Process); err != nil {
			o.HookStartErr = errors.Join(o.HookStartErr, err)
		}
		select {
		case waitErr = <-done:
			o.ExitedOnInterrupt = o.HookStartErr == nil
			o.ExitAfter = time.Since(interrupted)
		case <-time.After(p15Grace):
			_ = cmd.Process.Kill()
			waitErr = <-done
			o.ExitAfter = p15Grace
		}
	}
	o.Run = probeRun{Stdout: stdout.String(), Stderr: stderr.String(), Err: waitErr}
	var exitErr *exec.ExitError
	switch {
	case errors.As(waitErr, &exitErr):
		o.Run.ExitCode = exitErr.ExitCode()
	case waitErr != nil:
		o.Run.ExitCode = -1
	}

	if o.HookPID != 0 {
		o.HookAlive, o.HookAliveErr = p15AwaitDeath(o.HookPID)
	}
	sleepPID, _ := p15ReadPID(filepath.Join(p.dir, p15SleepPIDName))
	if sleepPID != 0 {
		o.SleepAlive, _ = p15ProcessAlive(sleepPID)
	}
	_, markerErr := os.Stat(filepath.Join(p.dir, p12MarkerName))
	o.MarkerExists = markerErr == nil

	// Measured; now leave nothing behind.
	for _, pid := range []int{o.HookPID, sleepPID} {
		if alive, _ := p15ProcessAlive(pid); pid != 0 && alive {
			if proc, err := os.FindProcess(pid); err == nil {
				_ = proc.Kill()
			}
		}
	}
	return o
}

// p15AwaitHook waits for the hook's pid file, or for claude to exit first.
func p15AwaitHook(pidPath string, done <-chan error) (pid int, startErr, waitErr error, exited bool) {
	deadline := time.After(p15HookStartWait)
	tick := time.NewTicker(p15PollEvery)
	defer tick.Stop()
	for {
		select {
		case waitErr = <-done:
			return 0, errP15HookNeverBlocked, waitErr, true
		case <-deadline:
			return 0, errP15HookNeverBlocked, nil, false
		case <-tick.C:
			if pid, err := p15ReadPID(pidPath); err == nil {
				return pid, nil, nil, false
			}
		}
	}
}

// p15AwaitDeath gives the hook p15ReapWait to leave /proc and reports whether
// it is still alive after that.
func p15AwaitDeath(pid int) (bool, error) {
	deadline := time.Now().Add(p15ReapWait)
	for {
		alive, err := p15ProcessAlive(pid)
		if err != nil || !alive || time.Now().After(deadline) {
			return alive, err
		}
		time.Sleep(p15PollEvery)
	}
}

// p15Fixture lays the cell out under the scenario's temp root, as P12's does:
// <dir>/{cfg,home,repo}, with the hook and its settings outside the repo.
func p15Fixture(w *World, p *p15State) error {
	p.dir = filepath.Join(w.env.Root, "p15")
	p.repo, p.cfg, p.home = filepath.Join(p.dir, "repo"), filepath.Join(p.dir, "cfg"), filepath.Join(p.dir, "home")
	for _, d := range []string{p.repo, p.cfg, p.home} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	if out, err := exec.Command("git", "init", "-q", p.repo).CombinedOutput(); err != nil {
		return fmt.Errorf("%s: git init %s: %w: %s", p15Family, p.repo, err, out)
	}
	hookPath := filepath.Join(p.dir, p12HookName)
	script, err := p15HookScript(p.dir)
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
