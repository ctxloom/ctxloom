//go:build acceptance

// P14, NATIVE HISTORY (capability_native_history.feature): the godog half.
// What the cells prove and why is in probe_p14_native_history.go.
//
// The container-writes cell runs through ctxloom on the isolation probe's
// own Given/When steps (one paid turn in a rootless container) and judges the
// host-side native history watchScratch captured during the run, which the
// container reaches through its engine home's link. The
// symlinked-projects cell runs the vendor binary directly, as P12 does, so a
// red names claude alone.
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

	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

// p14RunTimeout bounds the symlinked-projects cell's one claude turn.
const p14RunTimeout = 90 * time.Second

// p14State is the symlinked-projects cell's fixture and captured run.
type p14State struct {
	cell       probeCellID
	claudePath string
	cred       credentialMapping // never printed
	repo       string
	cfg, home  string
	run        probeRun
}

func registerP14NativeHistorySteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^claude's conversation history landed under its config home's projects directory$`, func(c context.Context) error {
		p := probeStateOf(worldFrom(c))
		if p.Result == nil {
			return fmt.Errorf("%s: no result recorded — the When step never ran", p14Family)
		}
		err := p14JudgeContainerWrites(p.Result.Scratch.NativeHome, p.Result.Scratch.NativeTree)
		fmt.Printf("EVIDENCE %s %s: exit=%d nativeHome=%s tree=%v verdict=%v\n",
			p14Family, p14ContainerWrites, p.Result.ExitCode, p.Result.Scratch.NativeHome, p.Result.Scratch.NativeTree, err)
		if err == nil && p.Result.ExitCode != 0 {
			return fmt.Errorf("%s %s: run exited %d; output:\n%s", p14Family, p14ContainerWrites, p.Result.ExitCode, p.Result.Output)
		}
		return err
	})

	ctx.Step(`^the native-history probe targets "([^"]*)" under runtime "([^"]*)" and workspace "([^"]*)" with projects symlinked$`,
		func(c context.Context, engine, runtime, workspace string) error {
			w := worldFrom(c)
			if runtime != "host" || workspace != "none" {
				return fmt.Errorf("%s: axes %s/%s — the symlink cell runs the vendor binary directly, so neither ctxloom isolation axis is in play", p14Family, runtime, workspace)
			}
			p := &p14State{cell: probeCellID{Probe: probeP14, Engine: engine, Runtime: runtime, Workspace: workspace, Variant: p14SymlinkedProjects}}
			w.p14 = p
			a, cred, err := probeDirectCellGate(w, p14Family, p.cell)
			if err != nil {
				return err
			}
			p.cred = cred
			if p.claudePath, err = exec.LookPath(a.binary); err != nil {
				return err
			}
			root := filepath.Join(w.env.Root, "p14")
			p.repo, p.home = filepath.Join(root, "repo"), filepath.Join(root, "home")
			p.cfg = filepath.Join(root, "sessions", "h", "home", claude.HomeLeaf)
			native := filepath.Join(p.cfg, p14NativeLink)
			for _, d := range []string{p.repo, p.home, p.cfg, native} {
				if err := os.MkdirAll(d, 0o700); err != nil {
					return err
				}
			}
			if err := os.Symlink(p14NativeLink, filepath.Join(p.cfg, claude.TranscriptsDirName)); err != nil {
				return err
			}
			if out, err := taskstest.GitCmd(filepath.Dir(p.repo), nil, "init", "-q", p.repo).CombinedOutput(); err != nil {
				return fmt.Errorf("%s: git init %s: %w: %s", p14Family, p.repo, err, out)
			}
			return nil
		})

	ctx.Step(`^it runs one native-history turn$`, func(c context.Context) error {
		p := worldFrom(c).p14
		if p == nil {
			return fmt.Errorf("%s: the Given step never ran", p14Family)
		}
		runCtx, cancel := context.WithTimeout(c, p14RunTimeout)
		defer cancel()
		cmd := exec.CommandContext(runCtx, p.claudePath, "-p", "Reply with the single word ok.", "--model", liveClaudeModel)
		cmd.Dir = p.repo
		cmd.Env = liveVendorEnv(p.cred, p.home, p.cfg)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		p.run = probeRun{Stdout: stdout.String(), Stderr: stderr.String(), Err: err}
		var exitErr *exec.ExitError
		switch {
		case errors.As(err, &exitErr):
			p.run.ExitCode = exitErr.ExitCode()
		case err != nil:
			p.run.ExitCode = -1
		}
		fmt.Printf("EVIDENCE %s %s: exit=%d timedOut=%t stdout=%dB stderr=%dB\n",
			p14Family, p.cell, p.run.ExitCode, errors.Is(runCtx.Err(), context.DeadlineExceeded), len(p.run.Stdout), len(p.run.Stderr))
		return nil
	})

	ctx.Step(`^claude wrote its history through the symlink and left the link in place$`, func(c context.Context) error {
		p := worldFrom(c).p14
		if p == nil {
			return fmt.Errorf("%s: the Given step never ran", p14Family)
		}
		if p.run.ExitCode != 0 {
			return fmt.Errorf("%s %s: the turn exited %d — the credential or the engine is the suspect, not the link; stderr:\n%s", p14Family, p.cell, p.run.ExitCode, p.run.Stderr)
		}
		return p14JudgeSymlink(p.cfg)
	})
}
