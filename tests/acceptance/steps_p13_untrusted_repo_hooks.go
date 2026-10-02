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
	repo       string            // cwd: a git repo with a COMMITTED .claude/settings.json, skill and agent
	cfg, home  string            // throwaway CLAUDE_CONFIG_DIR and HOME; cfg holds a .claude.json trusting the repo only in the trusted control

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
			case p13Fires, p13Suppresses, p13TrustedFrontmatter:
			default:
				return fmt.Errorf("%s: unknown variant %q (want %q, %q or %q)", p13Family, variant, p13Fires, p13Suppresses, p13TrustedFrontmatter)
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
// the trusted control, whose .claude.json trusts the repo.
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
		cmd := exec.Command("git", append([]string{"-C", p.repo,
			"-c", "user.name=p13", "-c", "user.email=p13@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
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
