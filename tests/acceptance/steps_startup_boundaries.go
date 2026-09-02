//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// The two arms of the startup gate's verdict, for scenarios about WHOSE
// findings reach it.
//
// Both assert the EXIT STATUS first and the diagnostic second, and the exit
// status is the load-bearing half: `mcp serve` is a stdio server, so a run that
// starts cleanly still ends nonzero when the client closes stdin ("server is
// closing: EOF"). "Failed" and "refused to launch" are therefore NOT the same
// outcome here, and a scenario that only asserted nonzero would read a normal,
// fully-started server as a refusal — passing whether or not the gate ever
// fired. strictness.ExitCodeFatalFindings is the status that separates them,
// cited by symbol so it cannot drift from the value the binary actually uses.
const startupAbortBanner = "aborting startup"

// bundleFindingRemedy is the fix line the bundle finding must carry. Asserted
// because a refusal a user cannot act on is the failure mode the refuse-by-
// default posture exists to avoid: nothing the user typed mentions a lockfile.
const bundleFindingRemedy = "ctxloom deps pull"

// bundleFindingLabel is how the startup gate tags a ClassBundle finding in its
// abort report. Derived from the class rather than spelled out, so renaming the
// class breaks this loudly instead of leaving a string that quietly matches
// nothing.
var bundleFindingLabel = "[" + string(strictness.ClassBundle) + "]"

// The reaper fixture's coordinates. The harp is a plain name — nothing has to
// resolve it, because the sweep classifies by what is ON DISK (a
// "ctxloom-wt-" directory under some harp's ephemeral dir) and by its owner
// marker, never by consulting the session index.
const (
	orphanHarp = "crashed-run-harp"
	orphanName = "orphan"
)

// orphanReapReport is what operations.SweepOrphanedWorktrees prints once it
// has actually removed something. The COUNT is part of it deliberately: a
// bare "reaped" would also match a sweep that removed some other number of
// trees, and this fixture plants exactly one.
//
// It is the secondary assertion on both arms. The primary one is the
// directory itself — this line is the tool's REPORT of what it did, and a
// report is precisely what a silent no-op still gets right.
const orphanReapReport = "reaped 1 orphaned per-agent worktree(s)"

func registerStartupBoundarySteps(ctx *godog.ScenarioContext) {
	// The reaper fixture: exactly what a crashed run leaves behind — a real
	// linked worktree under a harp's ephemeral dir whose recorded owner pid is
	// confirmed dead, and whose tree is genuinely clean. Both halves are
	// load-bearing, because the reaper spares anything it cannot prove safe:
	// a live/unprovable owner is SKIPPED and a dirty tree is SPARED, and
	// either would leave the directory standing for reasons that have nothing
	// to do with --dry-run. seedScratchWorktree refuses to plant this outside
	// the scenario's own isolated root — see its doc for why a reaper fixture
	// gets a guard no other fixture needs.
	ctx.Step(`^a crashed run left a clean orphaned per-agent worktree$`, func(c context.Context) error {
		w := worldFrom(c)
		// `git worktree add -b` needs a valid HEAD to branch from, and the
		// project fixture initializes a repo without committing.
		if err := w.env.WriteFile("README.md", "# startup boundaries fixture\n"); err != nil {
			return err
		}
		if err := w.env.GitCommit("initial commit"); err != nil {
			return err
		}
		wtDir := scratchWorktreeDir(w, orphanHarp, orphanName)
		if err := seedScratchWorktree(w, wtDir, "wt-"+orphanName, deadOwnerPid); err != nil {
			return err
		}
		// Prove the fixture is what the reaper is looking for before any
		// scenario asserts on its fate: the checkout and its owner marker
		// both have to be there, or both arms below assert about nothing.
		if _, err := os.Stat(wtDir); err != nil {
			return fmt.Errorf("the seeded orphan checkout is not on disk: %w", err)
		}
		if _, err := os.Stat(wtDir + scratchWorktreeOwnerSuffix); err != nil {
			return fmt.Errorf("the seeded orphan has no owner marker, so the reaper would SKIP it for want of proof rather than because of any flag: %w", err)
		}
		w.orphanWorktree = wtDir
		return nil
	})

	// The negative arm. Note what it does NOT do: it never asserts the
	// directory is absent, because absence is this suite's cheapest false
	// green. It asserts the directory SURVIVED, which a run that reaped
	// cannot satisfy — and the control step below is what proves this same
	// fixture was reapable all along.
	ctx.Step(`^the orphaned per-agent worktree is still on disk$`, func(c context.Context) error {
		w := worldFrom(c)
		wtDir, err := orphanWorktreePath(w)
		if err != nil {
			return err
		}
		if _, err := os.Stat(wtDir); err != nil {
			return fmt.Errorf("the orphaned worktree at %s is gone — the startup reaper RAN and destroyed it: %w\noutput:\n%s", wtDir, err, w.env.LastOutput())
		}
		if strings.Contains(w.env.LastOutput(), orphanReapReport) {
			return fmt.Errorf("the run reported %q, so the worktree reaper executed. output:\n%s", orphanReapReport, w.env.LastOutput())
		}
		return nil
	})

	// THE CONTROL, and the only reason the step above means anything. Same
	// fixture, same binary, same isolated home — only the flag is gone. The
	// DIRECTORY's disappearance is the assertion; the report line is
	// corroboration, since a sweep that printed its tally while removing
	// nothing is exactly the silent no-op this suite exists to catch.
	ctx.Step(`^the orphaned per-agent worktree has been reaped$`, func(c context.Context) error {
		w := worldFrom(c)
		wtDir, err := orphanWorktreePath(w)
		if err != nil {
			return err
		}
		if _, err := os.Stat(wtDir); err == nil {
			return fmt.Errorf("the orphaned worktree at %s is still on disk — a clean, provably-dead-owner orphan must be reaped by a real start, so the dry-run assertion above proves nothing. output:\n%s", wtDir, w.env.LastOutput())
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("cannot tell whether %s was removed: %w", wtDir, err)
		}
		if !strings.Contains(w.env.LastOutput(), orphanReapReport) {
			return fmt.Errorf("the worktree is gone but the run never reported %q; something other than the startup reaper removed it. output:\n%s", orphanReapReport, w.env.LastOutput())
		}
		return nil
	})

	ctx.Step(`^the startup aborts on a fatal bundle finding$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		if code := w.env.LastExitCode(); code != strictness.ExitCodeFatalFindings {
			return fmt.Errorf("startup exited %d, want %d (a fatal-findings refusal); a lockfile that cannot be parsed must abort the launch, not warn and continue. output:\n%s",
				code, strictness.ExitCodeFatalFindings, out)
		}
		for _, want := range []string{startupAbortBanner, bundleFindingLabel, bundleFindingRemedy} {
			if !strings.Contains(out, want) {
				return fmt.Errorf("the abort report is missing %q — it must name that it refused, the class that refused, and the remedy. output:\n%s", want, out)
			}
		}
		return nil
	})

	// The negative arm. It is deliberately NOT "the command succeeded": see the
	// exit-status note above.
	ctx.Step(`^the startup does not abort on a fatal bundle finding$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		if code := w.env.LastExitCode(); code == strictness.ExitCodeFatalFindings {
			return fmt.Errorf("startup exited %d — it refused to launch on a fatal finding, but nothing in scope for this run pins any bundle. output:\n%s",
				strictness.ExitCodeFatalFindings, out)
		}
		if strings.Contains(out, bundleFindingLabel) {
			return fmt.Errorf("the run reported a %s finding, so a bundle closure WAS resolved for it. output:\n%s", bundleFindingLabel, out)
		}
		return nil
	})
}

// orphanWorktreePath returns the seeded orphan's path, refusing when no
// fixture was seeded. Without this an empty path resolves to the project
// root's parent and every "is it gone?" question gets a confident, meaningless
// answer — and "gone" is the answer the reaper assertion WANTS, which is how
// absence-satisfies-absence passes for years.
func orphanWorktreePath(w *World) (string, error) {
	if w.orphanWorktree == "" {
		return "", fmt.Errorf("no orphaned worktree was seeded — the %q step must run first, or these assertions are about a directory that never existed", "a crashed run left a clean orphaned per-agent worktree")
	}
	if !filepath.IsAbs(w.orphanWorktree) {
		return "", fmt.Errorf("the seeded orphan path %q is not absolute", w.orphanWorktree)
	}
	return w.orphanWorktree, nil
}
