//go:build acceptance

// P16, STRICT MCP CONNECTORS (capability_strict_mcp_connectors.feature): the
// godog half of the rung. What the cell proves and why is in
// probe_p16_strict_mcp_connectors.go; this file gates the cell, lays out the
// fixture and spends its two turns.
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

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"

	"github.com/cucumber/godog"
)

// p16RunTimeout bounds each of the cell's two one-word turns.
const p16RunTimeout = 90 * time.Second

// p16State is the cell's fixture and its two captured turns.
type p16State struct {
	engine, runtime, workspace string

	claudePath string
	cred       credentialMapping // never printed
	dir        string
	repo       string // cwd: a fresh git repo
	cfg, home  string // throwaway CLAUDE_CONFIG_DIR and HOME

	outcome p16Outcome
}

func p16Of(w *World) *p16State {
	if w.p16 == nil {
		w.p16 = &p16State{}
	}
	return w.p16
}

func (p *p16State) cell() probeCellID {
	return probeCellID{Probe: probeP16, Engine: p.engine, Runtime: p.runtime, Workspace: p.workspace}
}

func registerP16StrictMCPConnectorsSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^the strict-mcp-connectors probe targets "([^"]*)" under runtime "([^"]*)" and workspace "([^"]*)"$`,
		func(c context.Context, engine, runtime, workspace string) error {
			w := worldFrom(c)
			p := p16Of(w)
			if runtime != "host" || workspace != "none" {
				return fmt.Errorf("%s: axes %s/%s — this rung is host/none only: it runs the vendor binary directly", p16Family, runtime, workspace)
			}
			p.engine, p.runtime, p.workspace = engine, runtime, workspace
			a, cred, err := probeDirectCellGate(w, p16Family, p.cell())
			if err != nil {
				return err
			}
			p.cred = cred
			if p.claudePath, err = exec.LookPath(a.binary); err != nil {
				return err
			}
			p.dir = filepath.Join(w.env.Root, "p16")
			p.repo, p.cfg, p.home = filepath.Join(p.dir, "repo"), filepath.Join(p.dir, "cfg"), filepath.Join(p.dir, "home")
			for _, d := range []string{p.repo, p.cfg, p.home} {
				if err := os.MkdirAll(d, 0o700); err != nil {
					return err
				}
			}
			if out, err := taskstest.GitCmd(filepath.Dir(p.repo), nil, "init", "-q", p.repo).CombinedOutput(); err != nil {
				return fmt.Errorf("%s: git init %s: %w: %s", p16Family, p.repo, err, out)
			}
			return nil
		})

	ctx.Step(`^it starts one turn without and one turn with --strict-mcp-config$`, func(c context.Context) error {
		p := p16Of(worldFrom(c))
		if p.dir == "" {
			return errP12NotRun
		}
		p.outcome = p16Outcome{Cell: p.cell(), Control: p16RunArm(c, p, false), Strict: p16RunArm(c, p, true)}
		fmt.Printf("EVIDENCE %s %s: %s\n", p16Family, p.cell(), p.outcome.summary())
		return nil
	})

	ctx.Step(`^the claude\.ai connectors load only without the flag$`, func(c context.Context) error {
		p := p16Of(worldFrom(c))
		if p.dir == "" {
			return errP12NotRun
		}
		return p16Assert(p.outcome)
	})
}

// p16RunArm spends one turn.
func p16RunArm(c context.Context, p *p16State, strict bool) p16Arm {
	runCtx, cancel := context.WithTimeout(c, p16RunTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, p.claudePath, p16Args(strict)...)
	cmd.Dir = p.repo
	cmd.Env = liveVendorEnv(p.cred, p.home, p.cfg)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return p16Arm{Started: true, TimedOut: errors.Is(runCtx.Err(), context.DeadlineExceeded),
		Run: probeRun{Stdout: stdout.String(), Stderr: stderr.String(), Err: err, ExitCode: probeExitCode(err)}}
}
