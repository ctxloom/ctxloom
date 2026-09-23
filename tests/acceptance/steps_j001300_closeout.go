//go:build acceptance

// J001300: "the close-out" (j001300_closeout.feature) — FLOWS-UNIFIED.md's U11.
//
// Every scenario drives a shipped verb: `ctxloom doctor`, `session worktrees`
// and its `purge` leaf, `session purge`, and `session sweep` — the
// deterministic sweep that ties the leaves together.
//
// `session distill` takes NO --skill and NO --to-bundle. That leg was
// specified here and rejected: extraction-into-a-bundle is not a close-out
// concern, so do not re-add steps for it.
//
// WHY THE FIXTURES ARE THIS DETAILED. A close-out flow is defined almost
// entirely by what it REFUSES to do, and a refusal cannot be tested against a
// fixture that has nothing to refuse. So these steps build the real debris:
// genuine `git worktree add` checkouts under a harp's own ephemeral dir with
// real sibling `.owner.pid` markers (the exact layout
// isolation.findEphemeralWorktrees scans and isolation.ReapOrphanedWorktrees
// reasons about), foreign long-lived worktrees outside the sessions root,
// uncommitted WIP, harp directories carrying machine-written bulk beside
// human-authored plan files. Every "spared", "skipped" and "preserved"
// assertion then reads a real file that a wrong implementation would really
// have destroyed.
//
// THE SAFETY SEMANTICS BEING SPECIFIED are not invented here — they are
// isolation.ReapOrphanedWorktrees's, which already treats "can't prove who
// owned this" identically to "still owned by someone alive: never touch it",
// spares uncommitted or unknowable WIP in place, and removes only genuinely
// clean trees. FLOWS-UNIFIED §5.2's proposed leaf drives that SAME logic on
// demand rather than reimplementing it, so these scenarios assert the reaper's
// established outcome taxonomy (reaped / spared / skipped) through a new
// surface.
//
// LIVENESS IS PER-SESSION, and the fixtures are shaped by that: a harp's
// scratch worktrees all share their owning session's verdict, so each
// owner-state gets its OWN session. A dead session has a lock file nothing
// holds; a live one has a lock this very test process holds for the length of
// the scenario; an unprovable one has no lock file at all. Clean-vs-dirty
// still varies freely WITHIN a session, because that is a property of the
// checkout.
package acceptance

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

const (
	// Markers. Each names a CONTENT CLASS from FLOWS-UNIFIED §5.4, so a purge
	// assertion says which class survived rather than which file did.
	j001300BulkMarker     = "J001300-MACHINE-WRITTEN-BULK"
	j001300EssenceMarker  = "J001300-DERIVED-ESSENCE"
	j001300AuthoredMarker = "J001300-HUMAN-AUTHORED-PLAN"
	j001300WIPMarker      = "J001300-UNCOMMITTED-WIP"
)

// j001300Harp records one seeded harp directory and what was planted in it.
type j001300Harp struct {
	name      string
	dir       string // absolute
	essence   bool
	authored  bool
	origin    string   // the sidecar's origin; "" is a human's session
	worktrees []string // absolute scratch-worktree dirs under this harp
}

// j001300State is this journey's fixture state.
type j001300State struct {
	harps   map[string]*j001300Harp
	order   []string // seeding order, so the index renders deterministically
	foreign map[string]string

	ready bool
}

func j001300Of(w *World) *j001300State {
	if w.j001300 == nil {
		w.j001300 = &j001300State{harps: map[string]*j001300Harp{}, foreign: map[string]string{}}
	}
	return w.j001300
}

// j001300Setup is the Background: a real git repo with a commit to branch
// worktrees from, and a configured project.
func j001300Setup(w *World) error {
	st := j001300Of(w)
	if st.ready {
		return nil
	}
	if err := ensureProjectWithEngine(w, "claude-code", "claude-code"); err != nil {
		return err
	}
	// `git worktree add -b` needs a valid HEAD to branch from.
	if err := w.env.WriteFile("README.md", "# j001300 close-out fixture\n"); err != nil {
		return err
	}
	if err := w.env.GitCommit("initial commit"); err != nil {
		return err
	}

	st.ready = true
	return nil
}

// j001300SeedHarp plants one harp directory carrying every content class §5.4
// inventories, so a purge scenario can assert per-class outcomes:
//
//	transcript.jsonl        machine-written bulk  — purgeable
//	persist/…               machine-written bulk  — purgeable
//	essence.md              derived               — preserved by default
//	<harp>.plan.md          HUMAN-AUTHORED        — never silently destroyed
//
// The authored file is written at the harp dir's TOP LEVEL on purpose: that is
// exactly where the plan-stamping convention puts it, and exactly the
// unclassified middle boundary B13 names — neither persist/ (mounted into
// containers) nor ephemeral/ (rightly excluded).
func j001300SeedHarp(w *World, harp string, essence, authored bool) error {
	st := j001300Of(w)
	dir := harpDirIn(w, harp)
	for _, sub := range []string{"persist/transcripts", "ephemeral"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(sub)), 0o755); err != nil {
			return fmt.Errorf("create %s/%s: %w", harp, sub, err)
		}
	}
	// Machine-written bulk, deliberately large enough that "bytes freed" is a
	// meaningful number rather than a rounding artifact.
	bulk := strings.Repeat(`{"role":"assistant","content":"`+j001300BulkMarker+`"}`+"\n", 200)
	if err := os.WriteFile(filepath.Join(dir, "transcript.jsonl"), []byte(bulk), 0o644); err != nil {
		return fmt.Errorf("write %s transcript: %w", harp, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "persist", "transcripts", "turns.jsonl"), []byte(bulk), 0o644); err != nil {
		return fmt.Errorf("write %s persisted transcript: %w", harp, err)
	}
	if essence {
		body := fmt.Sprintf("---\nharp_name: %s\ndistilled_at: 2026-01-01T00:00:00Z\n---\n\n%s for %s.\n", harp, j001300EssenceMarker, harp)
		if err := os.WriteFile(filepath.Join(dir, "essence.md"), []byte(body), 0o644); err != nil {
			return fmt.Errorf("write %s essence: %w", harp, err)
		}
	}
	if authored {
		body := fmt.Sprintf("# %s design notes\n\n%s\n\nDecisions this session reached that exist nowhere else.\n", harp, j001300AuthoredMarker)
		if err := os.WriteFile(filepath.Join(dir, harp+".plan.md"), []byte(body), 0o644); err != nil {
			return fmt.Errorf("write %s authored plan: %w", harp, err)
		}
	}

	if _, seen := st.harps[harp]; !seen {
		st.order = append(st.order, harp)
	}
	st.harps[harp] = &j001300Harp{name: harp, dir: dir, essence: essence, authored: authored}
	return j001300WriteIndex(w)
}

// j001300WriteIndex (re)records EVERY seeded harp's session: each harp's
// directory carries its own sidecar, so an addition never disturbs an
// earlier harp, and re-recording all of them keeps a close-out journey's
// inherently multi-harp world whole after a step rewrites one.
func j001300WriteIndex(w *World) error {
	st := j001300Of(w)
	for _, name := range st.order {
		if err := seedSessionSidecar(w, name, sessionSeed{
			SessionID:      "seeded-" + name,
			Backend:        "claude-code",
			StartedAt:      "2026-01-01T00:00:00Z",
			EndedAt:        "2026-01-02T00:00:00Z",
			TranscriptPath: filepath.Join(harpDirIn(w, name), "transcript.jsonl"),
			Origin:         st.harps[name].origin,
		}); err != nil {
			return err
		}
	}
	return nil
}

// j001300AddScratchWorktree seeds one scratch worktree (seedScratchWorktree,
// which owns the layout and the ownerPid convention) inside an already-seeded
// harp, recording it against that harp and optionally planting the uncommitted
// work a reaper must refuse to destroy.
func j001300AddScratchWorktree(w *World, harp, name string, dirty bool) (string, error) {
	st := j001300Of(w)
	h, ok := st.harps[harp]
	if !ok {
		return "", fmt.Errorf("harp %q has not been seeded", harp)
	}
	wtDir := scratchWorktreeDir(w, harp, name)
	if err := seedScratchWorktree(w, wtDir, "wt-"+harp+"-"+name); err != nil {
		return "", err
	}
	if dirty {
		// Uncommitted work that no reaper may ever destroy.
		if err := os.WriteFile(filepath.Join(wtDir, "in-flight.go"), []byte("// "+j001300WIPMarker+"\n"), 0o644); err != nil {
			return "", fmt.Errorf("plant WIP in %s: %w", wtDir, err)
		}
	}
	h.worktrees = append(h.worktrees, wtDir)
	return wtDir, nil
}

// j001300AddForeignWorktree creates a long-lived worktree OUTSIDE the sessions
// root — the ~/workspace/worktrees/<project>--<branch> population. It is
// invisible to the candidate finder by construction, and ctxloom may only ever
// report on it.
func j001300AddForeignWorktree(w *World, branch string, dirty bool) (string, error) {
	st := j001300Of(w)
	dir := filepath.Join(w.env.HomeDir, "workspace", "worktrees", "proj--"+branch)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", fmt.Errorf("create foreign worktrees parent: %w", err)
	}
	if _, err := isolatedGit(w, w.env.ProjectDir, "worktree", "add", "-q", "-b", branch, dir); err != nil {
		return "", err
	}
	if dirty {
		if err := os.WriteFile(filepath.Join(dir, "uncommitted.txt"), []byte(j001300WIPMarker+"\n"), 0o644); err != nil {
			return "", fmt.Errorf("plant WIP in foreign tree: %w", err)
		}
	}
	st.foreign[branch] = dir
	return dir, nil
}

// j001300Answered reports whether out — the stream the caller chose — names
// every want, failing with it whole and the exit code so a red scenario
// documents what the product said instead. A REPORT (doctor's checks, a
// listing, a purge plan) is read from stdout alone: a stderr line quoting the
// same path or harp would otherwise stand in for the report that never
// rendered. A REFUSAL is read from the combined stream, which is where
// reportRefusal writes it.
func j001300Answered(w *World, out, what string, wants ...string) error {
	var missing []string
	for _, want := range wants {
		if !strings.Contains(out, want) {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s did not report %v (exit %d); the whole output was:\n%s",
			what, missing, w.env.LastExitCode(), out)
	}
	return nil
}

// j001300DirExists is a plain on-disk existence check on an absolute path — the
// only honest way to assert a reap happened or did not.
func j001300DirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// j001300RanRealSurface fails unless the last invocation actually reached a
// command that EXISTS.
//
// This guard is the difference between a specification and a lie. Several
// scenarios in this journey assert that something was NOT destroyed, NOT
// listed, or NOT reported as success — and every one of those assertions is
// trivially satisfied by a CLI that rejected the invocation before doing
// anything at all. A misspelled verb or a retired flag destroys nothing, of
// course, and every safety assertion in this file passes green against it. A
// green scenario that dodged its own assertion is worth less than a red one,
// because it reports coverage where there is none.
//
// So every negative assertion runs through here first, and a missing surface
// is reported as a missing surface rather than as a passing safety property.
func j001300RanRealSurface(w *World) error {
	out := w.env.LastOutput()
	for _, tell := range []string{"unknown command", "unknown flag", "unknown shorthand flag"} {
		if strings.Contains(out, tell) {
			return fmt.Errorf("this scenario's safety assertion cannot mean anything yet: the invocation never reached a real "+
				"command (%s). It is RED because the surface does not exist, NOT green because the surface behaved safely. "+
				"ctxloom said (exit %d):\n%s", tell, w.env.LastExitCode(), out)
		}
	}
	return nil
}

func registerJ001300Steps(ctx *godog.ScenarioContext) {
	// --- Background ---------------------------------------------------------

	ctx.Step(`^the feature shipped on Friday and Alice is closing the workstream out$`, func(c context.Context) error {
		return j001300Setup(worldFrom(c))
	})

	// --- Debris fixtures ----------------------------------------------------

	// A FINISHED session: seeded, then proven ended by a FREE lock file. That
	// is the only state that permits reclaiming anything of it.
	ctx.Step(`^a finished session "([^"]*)" whose work is already distilled$`, func(c context.Context, harp string) error {
		w := worldFrom(c)
		if err := j001300SeedHarp(w, harp, true, false); err != nil {
			return err
		}
		return seedDeadSession(w, harp)
	})

	ctx.Step(`^a finished session "([^"]*)" that was never distilled$`, func(c context.Context, harp string) error {
		w := worldFrom(c)
		if err := j001300SeedHarp(w, harp, false, false); err != nil {
			return err
		}
		return seedDeadSession(w, harp)
	})

	ctx.Step(`^a finished session "([^"]*)" carrying design notes nobody filed$`, func(c context.Context, harp string) error {
		w := worldFrom(c)
		if err := j001300SeedHarp(w, harp, true, true); err != nil {
			return err
		}
		return seedDeadSession(w, harp)
	})

	// A session that is STILL RUNNING: this test process holds its lock, so
	// every worktree under it must be left strictly alone.
	ctx.Step(`^a session "([^"]*)" that is still running$`, func(c context.Context, harp string) error {
		w := worldFrom(c)
		if err := j001300SeedHarp(w, harp, false, false); err != nil {
			return err
		}
		return seedLiveSession(w, harp)
	})

	// A session with NO lock file at all — from before the lock existed, or
	// one whose Hold never succeeded. "Cannot prove dead" must be treated
	// identically to "alive", never as permission.
	ctx.Step(`^a session "([^"]*)" nothing can prove the liveness of$`, func(c context.Context, harp string) error {
		return j001300SeedHarp(worldFrom(c), harp, false, false)
	})

	ctx.Step(`^session "([^"]*)" left a clean scratch worktree$`, func(c context.Context, harp string) error {
		_, err := j001300AddScratchWorktree(worldFrom(c), harp, "clean", false)
		return err
	})

	ctx.Step(`^session "([^"]*)" left a scratch worktree holding uncommitted work$`, func(c context.Context, harp string) error {
		_, err := j001300AddScratchWorktree(worldFrom(c), harp, "wip", true)
		return err
	})

	ctx.Step(`^session "([^"]*)" left a scratch worktree of its own$`, func(c context.Context, harp string) error {
		_, err := j001300AddScratchWorktree(worldFrom(c), harp, "own", false)
		return err
	})

	ctx.Step(`^a long-lived worktree "([^"]*)" of her own, outside the sessions root, with unmerged work$`, func(c context.Context, branch string) error {
		w := worldFrom(c)
		dir, err := j001300AddForeignWorktree(w, branch, true)
		if err != nil {
			return err
		}
		// A real commit that is genuinely not on the integration branch, so
		// `git cherry` has something true to say about it.
		if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("unmerged work\n"), 0o644); err != nil {
			return err
		}
		if _, err := isolatedGit(w, dir, "add", "-A"); err != nil {
			return err
		}
		_, err = isolatedGit(w, dir, "commit", "-m", "unmerged feature work")
		return err
	})

	// --- Preconditions: doctor's new checks ---------------------------------

	ctx.Step(`^the project still carries the superseded blanket ctxloom ignore rule$`, func(c context.Context) error {
		w := worldFrom(c)
		// The exact line gitignore.isSupersededBlanket matches, and the exact
		// state ctxloom's own repo is in: a blanket rule under which
		// .ctxloom/content can never be committed at all.
		return w.env.WriteFile(".gitignore", ".ctxloom/*\n")
	})

	ctx.Step(`^the checks name the ignore rule and the command that retires it$`, func(c context.Context) error {
		w := worldFrom(c)
		return j001300Answered(w, w.env.LastStdout(), "`ctxloom doctor`", ".ctxloom", "manage gitignore install")
	})

	ctx.Step(`^the checks name the foreign worktree, that it is unmerged and dirty, and the exact commands to remove it$`, func(c context.Context) error {
		w := worldFrom(c)
		return j001300Answered(w, w.env.LastStdout(), "doctor's foreign-worktree report",
			"proj--stale-feature", "unmerged", "git worktree remove", "git branch -d")
	})

	ctx.Step(`^the checks warn that the design notes sit in the harp directory's unclassified top level$`, func(c context.Context) error {
		w := worldFrom(c)
		return j001300Answered(w, w.env.LastStdout(), "doctor's harp-durability check (B13)",
			".plan.md", "persist")
	})

	// --- session worktrees --------------------------------------------------

	ctx.Step(`^the report names each scratch worktree with its harp, its owner and its verdict$`, func(c context.Context) error {
		w := worldFrom(c)
		return j001300Answered(w, w.env.LastStdout(), "`ctxloom session worktrees`",
			"ctxloom-wt-clean", "ctxloom-wt-wip", fmt.Sprintf("%d", deadOwnerPid))
	})

	ctx.Step(`^only the clean, provably-orphaned worktree is gone from disk$`, func(c context.Context) error {
		w := worldFrom(c)
		st := j001300Of(w)
		var problems []string
		for _, h := range st.harps {
			for _, wt := range h.worktrees {
				base := filepath.Base(wt)
				gone := !j001300DirExists(wt)
				switch {
				case strings.HasSuffix(base, "-clean") && !gone:
					problems = append(problems, fmt.Sprintf("%s is a clean tree with a confirmed-dead owner and is STILL ON DISK — it was not reaped", wt))
				case !strings.HasSuffix(base, "-clean") && gone:
					problems = append(problems, fmt.Sprintf("%s was REMOVED, and nothing in this scenario made it safe to remove", wt))
				}
			}
		}
		if len(problems) > 0 {
			sort.Strings(problems)
			return fmt.Errorf("the reap did the wrong thing:\n  %s\nctxloom reported (exit %d):\n%s",
				strings.Join(problems, "\n  "), w.env.LastExitCode(), w.env.LastOutput())
		}
		return nil
	})

	ctx.Step(`^the uncommitted work is still there, spared in place$`, func(c context.Context) error {
		w := worldFrom(c)
		for _, h := range j001300Of(w).harps {
			for _, wt := range h.worktrees {
				if !strings.HasSuffix(filepath.Base(wt), "-wip") {
					continue
				}
				body, err := os.ReadFile(filepath.Join(wt, "in-flight.go"))
				if err != nil {
					return fmt.Errorf("the uncommitted work in %s is GONE — a reap destroyed unrecoverable WIP: %w", wt, err)
				}
				if !strings.Contains(string(body), j001300WIPMarker) {
					return fmt.Errorf("the uncommitted work in %s no longer carries its own bytes; it holds:\n%s", wt, body)
				}
				return nil
			}
		}
		return fmt.Errorf("no worktree holding uncommitted work was seeded, so this assertion measured nothing")
	})

	// The REASON, not merely the tally word: a report that prints "spared: 1"
	// and no why leaves the caller unable to act on it, and is exactly what a
	// wrong implementation that spared for the wrong reason also prints.
	ctx.Step(`^the report says why each spared worktree was left alone$`, func(c context.Context) error {
		w := worldFrom(c)
		return j001300Answered(w, w.env.LastStdout(), "the reap report", "spared", "uncommitted changes")
	})

	// The refusal arm: an invocation that could prove nothing safe must remove
	// NOTHING and must not report a clean sweep. Asserted on the population's
	// own bytes, because "the directory is still there" is what an invocation
	// that never ran also produces — hence the real-surface guard first.
	ctx.Step(`^no worktree of "([^"]*)" is removed, and ctxloom says it could prove nothing safe$`, func(c context.Context, harp string) error {
		w := worldFrom(c)
		if err := j001300RanRealSurface(w); err != nil {
			return err
		}
		h, ok := j001300Of(w).harps[harp]
		if !ok {
			return fmt.Errorf("harp %q was never seeded", harp)
		}
		if len(h.worktrees) == 0 {
			return fmt.Errorf("no worktree was seeded under %q, so this assertion measured nothing", harp)
		}
		for _, wt := range h.worktrees {
			if !j001300DirExists(wt) {
				return fmt.Errorf("%s was REMOVED. Its owning session could not be PROVEN ended, and "+
					"\"cannot determine\" is never permission to reclaim. ctxloom reported (exit %d):\n%s",
					wt, w.env.LastExitCode(), w.env.LastOutput())
			}
		}
		if w.env.LastExitCode() == 0 {
			return fmt.Errorf("the purge exited 0 having removed nothing. An action verb that changed nothing refuses, "+
				"so an unattended run cannot mistake it for one that cleaned up. Output:\n%s", w.env.LastOutput())
		}
		return j001300Answered(w, w.env.LastOutput(), "the refusal", "skipped")
	})

	ctx.Step(`^her own long-lived worktree is untouched and was never listed$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j001300RanRealSurface(w); err != nil {
			return err
		}
		st := j001300Of(w)
		for branch, dir := range st.foreign {
			if !j001300DirExists(dir) {
				return fmt.Errorf("the foreign worktree %s (%s) was REMOVED — ctxloom must never remove a worktree it did not create", branch, dir)
			}
			if strings.Contains(w.env.LastOutput(), dir) {
				return fmt.Errorf("`session worktrees` listed the foreign worktree %s; that population is doctor's to REPORT on, "+
					"and listing it under a verb that also reaps invites exactly the removal that is forbidden. Output:\n%s", dir, w.env.LastOutput())
			}
		}
		return nil
	})

	// --- session purge ------------------------------------------------------

	ctx.Step(`^the report lists what would be destroyed and what would be kept$`, func(c context.Context) error {
		w := worldFrom(c)
		return j001300Answered(w, w.env.LastStdout(), "`ctxloom session purge`", "transcript", "essence")
	})

	ctx.Step(`^every byte of every session is still on disk$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j001300RanRealSurface(w); err != nil {
			return err
		}
		st := j001300Of(w)
		for _, h := range st.harps {
			for _, rel := range []string{"transcript.jsonl", "persist/transcripts/turns.jsonl"} {
				p := filepath.Join(h.dir, filepath.FromSlash(rel))
				if _, err := os.Stat(p); err != nil {
					return fmt.Errorf("%s was destroyed by an invocation that only reported (exit %d). "+
						"Read-only default on every leaf is the confirmation line's first rule. Output:\n%s",
						p, w.env.LastExitCode(), w.env.LastOutput())
				}
			}
		}
		return nil
	})

	ctx.Step(`^the machine-written bulk of "([^"]*)" is gone$`, func(c context.Context, harp string) error {
		w := worldFrom(c)
		h, ok := j001300Of(w).harps[harp]
		if !ok {
			return fmt.Errorf("harp %q was never seeded", harp)
		}
		for _, rel := range []string{"transcript.jsonl", "persist/transcripts/turns.jsonl"} {
			p := filepath.Join(h.dir, filepath.FromSlash(rel))
			if _, err := os.Stat(p); err == nil {
				return fmt.Errorf("%s survived a purge that reported success (exit %d) — a purge that frees nothing while "+
					"reporting it destroyed something is the silent no-op wearing a different hat. Output:\n%s",
					p, w.env.LastExitCode(), w.env.LastOutput())
			}
		}
		return nil
	})

	// The sweep covers BOTH file populations, so the essence goes with the
	// bulk. The index entry is what does not: a purged session stays listed,
	// marked purged, because a session that vanishes from the index is
	// indistinguishable from one that never existed. Asserting the two halves
	// in one step keeps them from being read as alternatives — a run that
	// destroyed the essence AND unlisted the session must not pass.
	ctx.Step(`^its distilled essence goes with the bulk, and its index entry survives$`, func(c context.Context) error {
		w := worldFrom(c)
		st := j001300Of(w)
		for _, h := range st.harps {
			if !h.essence {
				continue
			}
			p := filepath.Join(h.dir, "essence.md")
			if _, err := os.Stat(p); err == nil {
				return fmt.Errorf("the distilled essence of %s survived a sweep that reported success (exit %d). "+
					"Emptying a session covers every population ctxloom wrote into it, and an essence left standing means the "+
					"caller believes the session is empty while its derived half is still on disk. Output:\n%s",
					h.name, w.env.LastExitCode(), w.env.LastOutput())
			}
		}
		// The record is now the session's OWN sidecar, not a shared index:
		// a purge stamps purged_at there and leaves the directory, so the
		// session stays listed marked purged rather than vanishing.
		for _, name := range st.order {
			sidecar := filepath.Join(harpDirIn(w, name), "session.yaml")
			body, err := os.ReadFile(sidecar)
			if err != nil {
				return fmt.Errorf("%s's session record is gone. A purged session must remain a listed session MARKED purged — "+
					"a session that vanishes is indistinguishable from one that never existed: %w", name, err)
			}
			if !strings.Contains(string(body), "purged_at") {
				return fmt.Errorf("%s's record survived but carries no purged_at, so the derived listing reads it as a live "+
					"session that lost its transcript and drops it as damage. Record:\n%s", name, body)
			}
		}
		return nil
	})

	ctx.Step(`^her unfiled design notes are still there, and were named in the report$`, func(c context.Context) error {
		w := worldFrom(c)
		for _, h := range j001300Of(w).harps {
			if !h.authored {
				continue
			}
			p := filepath.Join(h.dir, h.name+".plan.md")
			body, err := os.ReadFile(p)
			if err != nil {
				return fmt.Errorf("HUMAN-AUTHORED design notes at %s were DESTROYED: %w. Authored artifacts are never "+
					"negotiable — a cleanup that eats the only copy of a design nobody filed is worse than no cleanup at all", p, err)
			}
			if !strings.Contains(string(body), j001300AuthoredMarker) {
				return fmt.Errorf("%s no longer carries its own bytes; it holds:\n%s", p, body)
			}
			if !strings.Contains(w.env.LastStdout(), ".plan.md") {
				return fmt.Errorf("the authored notes survived but the purge never NAMED them. They are surfaced for the lessons "+
					"step or manual filing, not silently skipped — a file kept but never mentioned is a file nobody will ever file. Stdout:\n%s",
					w.env.LastStdout())
			}
			return nil
		}
		return fmt.Errorf("no harp carrying authored notes was seeded, so this assertion measured nothing")
	})

	// A refusal that does not name the route is only half a refusal: the caller
	// is told no and left with nowhere to go, which is how someone ends up
	// deleting a harp directory by hand. So this asserts the REMEDY as well as
	// the refusal.
	ctx.Step(`^ctxloom refuses, naming the session that was never distilled and the leaf that can destroy it$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j001300RanRealSurface(w); err != nil {
			return err
		}
		if w.env.LastExitCode() == 0 {
			return fmt.Errorf("the sweep proceeded against an undistilled session (exit 0). With no essence the transcript is the only "+
				"record of what happened, and destroying it must take a deliberate act on the leaf that owns it. Output:\n%s",
				w.env.LastOutput())
		}
		return j001300Answered(w, w.env.LastOutput(), "the refusal",
			"brisk-copper-moth", "never distilled", "session transcript purge", "--undistilled")
	})

	// The sweep covers the scratch worktrees, under the worktree population's
	// own safety rules — so a tree holding uncommitted work stays on disk AND
	// stays registered with git. Deregistering it while leaving the directory
	// would strand the work outside git's own view of the repository, which is
	// the quiet half of losing it.
	ctx.Step(`^the scratch worktree is still registered with git$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j001300RanRealSurface(w); err != nil {
			return err
		}
		st := j001300Of(w)
		out, err := isolatedGit(w, w.env.ProjectDir, "worktree", "list", "--porcelain")
		if err != nil {
			return err
		}
		for _, h := range st.harps {
			for _, wt := range h.worktrees {
				if !j001300DirExists(wt) {
					return fmt.Errorf("the sweep removed the scratch worktree %s, which holds uncommitted work. A sweep reaches the same "+
						"verdicts the worktree leaf reaches on its own, and that verdict is SPARED", wt)
				}
				if !strings.Contains(out, wt) {
					return fmt.Errorf("the sweep deregistered %s from git while leaving its files on disk. `git worktree list` says:\n%s", wt, out)
				}
			}
		}
		return nil
	})

	// --- The sweep ----------------------------------------------------------

	// An INTERNAL one-shot: the mint stamps origin "oneshot" on it, the one
	// fact that lets a sweep empty it without an essence.
	ctx.Step(`^an internal one-shot session "([^"]*)" that was never distilled$`, func(c context.Context, harp string) error {
		w := worldFrom(c)
		if err := j001300SeedHarp(w, harp, false, false); err != nil {
			return err
		}
		j001300Of(w).harps[harp].origin = "oneshot"
		if err := j001300WriteIndex(w); err != nil {
			return err
		}
		return seedDeadSession(w, harp)
	})

	// Age is a property of the fixture, not of how long the scenario ran:
	// every mtime under every seeded harp directory is set back. Symlinks are
	// skipped — the sweep's clock never reads them, and Chtimes would follow.
	ctx.Step(`^every session has been idle for (\d+) days$`, func(c context.Context, days int) error {
		w := worldFrom(c)
		old := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
		for _, h := range j001300Of(w).harps {
			err := filepath.WalkDir(h.dir, func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.Type()&fs.ModeSymlink != 0 {
					return err
				}
				return os.Chtimes(p, old, old)
			})
			if err != nil {
				return fmt.Errorf("backdate %s: %w", h.name, err)
			}
		}
		return nil
	})

	ctx.Step(`^the sweep report names "([^"]*)" and "([^"]*)"$`, func(c context.Context, a, b string) error {
		w := worldFrom(c)
		if err := j001300RanRealSurface(w); err != nil {
			return err
		}
		return j001300Answered(w, w.env.LastStdout(), "`ctxloom session sweep`", a, b, "planned")
	})

	// A skipped session is asserted on its own bytes AND on the report's
	// reason: a sweep that silently omitted it would leave the bytes too.
	ctx.Step(`^the sweep skipped "([^"]*)" and "([^"]*)" without touching either$`, func(c context.Context, running, unproven string) error {
		w := worldFrom(c)
		if err := j001300RanRealSurface(w); err != nil {
			return err
		}
		if w.env.LastExitCode() != 0 {
			return fmt.Errorf("the sweep exited %d; leaving what it cannot prove safe is not a failure. Output:\n%s", w.env.LastExitCode(), w.env.LastOutput())
		}
		for _, name := range []string{running, unproven} {
			if err := j001300Untouched(w, name); err != nil {
				return err
			}
		}
		return j001300Answered(w, w.env.LastStdout(), "the sweep report", "it is running",
			"its liveness cannot be proven", "ctxloom session purge "+unproven+" --even-if-live")
	})

	ctx.Step(`^the sweep kept the transcript of "([^"]*)" and named "([^"]*)"$`, func(c context.Context, harp, named string) error {
		w := worldFrom(c)
		if err := j001300BulkIntact(w, harp); err != nil {
			return err
		}
		return j001300Answered(w, w.env.LastStdout(), "the sweep report", harp, named)
	})
}

// j001300Untouched asserts harp's machine-written bulk still carries its own
// bytes and every scratch worktree seeded under it is still on disk.
func j001300Untouched(w *World, name string) error {
	if err := j001300BulkIntact(w, name); err != nil {
		return err
	}
	for _, wt := range j001300Of(w).harps[name].worktrees {
		if !j001300DirExists(wt) {
			return fmt.Errorf("%s's scratch worktree %s was REMOVED by a sweep that should have left it. Output:\n%s", name, wt, w.env.LastOutput())
		}
	}
	return nil
}

// j001300BulkIntact asserts harp's machine-written bulk still carries its own
// bytes.
func j001300BulkIntact(w *World, name string) error {
	h, ok := j001300Of(w).harps[name]
	if !ok {
		return fmt.Errorf("harp %q was never seeded", name)
	}
	for _, rel := range []string{"transcript.jsonl", "persist/transcripts/turns.jsonl"} {
		body, err := os.ReadFile(filepath.Join(h.dir, filepath.FromSlash(rel)))
		if err != nil || !strings.Contains(string(body), j001300BulkMarker) {
			return fmt.Errorf("%s's %s was destroyed or rewritten by a sweep that should have left it whole (exit %d). Output:\n%s",
				name, rel, w.env.LastExitCode(), w.env.LastOutput())
		}
	}
	return nil
}
