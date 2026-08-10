//go:build acceptance

// J001300: "the close-out" (j001300_closeout.feature) — FLOWS-UNIFIED.md's U11.
//
// ONE SCENARIO HERE STILL SPECIFIES A SURFACE THAT DOES NOT EXIST — the
// `cleanup` routine — and that is the deliverable rather than a defect. A red
// scenario naming a real gap is worth more than a green one that dodged the
// assertion. Everything else drives shipped verbs: `ctxloom doctor`,
// `session worktrees` and its `purge` leaf, and `session purge`.
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
// THE PID CONVENTION mirrors internal/lm/isolation/worktree_reap_test.go's:
// j001300DeadPid exceeds even a kernel.pid_max=4194304 configuration by a wide
// margin, so "this owner is confirmed dead" is deterministic with no
// fork/kill/race. A LIVE owner is this test process's own pid, which is
// trivially alive for exactly as long as the scenario runs.
package acceptance

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cucumber/godog"
)

const (
	// j001300DeadPid names no live process on any Linux configuration — see the
	// file doc. Copied deliberately rather than imported: the isolation
	// package's constant is unexported, and an acceptance fixture that
	// silently changed meaning when that package was refactored would be
	// worse than a duplicated literal with this comment attached.
	j001300DeadPid = 999999999

	// j001300SessionsRel is the harp store, relative to the isolated HOME.
	j001300SessionsRel = ".ctxloom/sessions"

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
	worktrees []string // absolute scratch-worktree dirs under this harp
}

// j001300State is this journey's fixture state.
type j001300State struct {
	harps   map[string]*j001300Harp
	order   []string // seeding order, so the index renders deterministically
	foreign map[string]string

	ready bool

	probes []j001900ProbeResult
}

func j001300Of(w *World) *j001300State {
	if w.j001300 == nil {
		w.j001300 = &j001300State{harps: map[string]*j001300Harp{}, foreign: map[string]string{}}
	}
	return w.j001300
}

// j001300Git runs a git command in dir under the harness's isolated environment.
func j001300Git(w *World, dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = w.env.Command(nil, "version").Env // the isolated env every helper trusts
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s in %s: %s: %w", strings.Join(args, " "), dir, out, err)
	}
	return string(out), nil
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

// j001300HarpDir returns the absolute path of a harp's directory in the isolated
// home — ~/.ctxloom/sessions/<harp>, the layout paths.HarpDir builds.
func j001300HarpDir(w *World, harp string) string {
	return filepath.Join(w.env.HomeDir, filepath.FromSlash(j001300SessionsRel), harp)
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
	dir := j001300HarpDir(w, harp)
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

// j001300WriteIndex rewrites the session index over EVERY seeded harp. The shared
// "a recorded session" fixture step writes a single-entry index, which would
// silently drop earlier harps; a close-out journey is inherently multi-harp,
// so it owns its own accumulating writer.
func j001300WriteIndex(w *World) error {
	st := j001300Of(w)
	var b strings.Builder
	b.WriteString("sessions:\n")
	for _, name := range st.order {
		fmt.Fprintf(&b, "  - harp_name: %s\n", name)
		fmt.Fprintf(&b, "    session_id: seeded-%s\n", name)
		b.WriteString("    backend: claude-code\n")
		fmt.Fprintf(&b, "    project_dir: %s\n", w.env.ProjectDir)
		b.WriteString("    started_at: 2026-01-01T00:00:00Z\n")
		b.WriteString("    ended_at: 2026-01-02T00:00:00Z\n")
		fmt.Fprintf(&b, "    transcript_path: %s\n", filepath.Join(j001300HarpDir(w, name), "transcript.jsonl"))
		fmt.Fprintf(&b, "    summary: seeded close-out session %s\n", name)
	}
	return w.env.WriteHomeFile(j001300SessionsRel+"/index.yaml", b.String())
}

// j001300AddScratchWorktree creates a REAL linked git worktree inside a harp's own
// ephemeral directory, named with the "ctxloom-wt-" prefix the candidate
// finder matches, plus the sibling ".owner.pid" marker the reaper reads.
// ownerPid of 0 means "write no marker at all" — the "can't prove who owned
// this" case, which the reaper must treat exactly like a live owner.
func j001300AddScratchWorktree(w *World, harp, name string, ownerPid int, dirty bool) (string, error) {
	st := j001300Of(w)
	h, ok := st.harps[harp]
	if !ok {
		return "", fmt.Errorf("harp %q has not been seeded", harp)
	}
	wtDir := filepath.Join(h.dir, "ephemeral", "ctxloom-wt-"+name)
	if _, err := j001300Git(w, w.env.ProjectDir, "worktree", "add", "-q", "-b", "wt-"+name, wtDir); err != nil {
		return "", err
	}
	if ownerPid != 0 {
		marker := wtDir + ".owner.pid"
		if err := os.WriteFile(marker, fmt.Appendf(nil, "%d\n", ownerPid), 0o600); err != nil {
			return "", fmt.Errorf("write owner marker for %s: %w", wtDir, err)
		}
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
	if _, err := j001300Git(w, w.env.ProjectDir, "worktree", "add", "-q", "-b", branch, dir); err != nil {
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

// j001300Probe records one candidate invocation of a surface that may not exist.
func j001300Probe(w *World, args ...string) {
	st := j001300Of(w)
	_ = w.env.Run(args...)
	st.probes = append(st.probes, j001900ProbeResult{
		args:   append([]string(nil), args...),
		exit:   w.env.LastExitCode(),
		output: w.env.LastOutput(),
	})
}

// j001300Answered reports whether the LAST run's output names every want, failing
// with the whole output and the exit code so a red scenario documents what the
// product said instead.
func j001300Answered(w *World, what string, wants ...string) error {
	out := w.env.LastOutput()
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

	ctx.Step(`^a finished session "([^"]*)" whose work is already distilled$`, func(c context.Context, harp string) error {
		return j001300SeedHarp(worldFrom(c), harp, true, false)
	})

	ctx.Step(`^a finished session "([^"]*)" that was never distilled$`, func(c context.Context, harp string) error {
		return j001300SeedHarp(worldFrom(c), harp, false, false)
	})

	ctx.Step(`^a finished session "([^"]*)" carrying design notes nobody filed$`, func(c context.Context, harp string) error {
		return j001300SeedHarp(worldFrom(c), harp, true, true)
	})

	ctx.Step(`^session "([^"]*)" left a clean scratch worktree whose owning process is dead$`, func(c context.Context, harp string) error {
		_, err := j001300AddScratchWorktree(worldFrom(c), harp, "clean", j001300DeadPid, false)
		return err
	})

	ctx.Step(`^session "([^"]*)" left a scratch worktree holding uncommitted work$`, func(c context.Context, harp string) error {
		_, err := j001300AddScratchWorktree(worldFrom(c), harp, "wip", j001300DeadPid, true)
		return err
	})

	ctx.Step(`^session "([^"]*)" left a scratch worktree nothing can prove the owner of$`, func(c context.Context, harp string) error {
		// No owner marker at all: "can't prove who owned this" must be treated
		// identically to "still owned by someone alive".
		_, err := j001300AddScratchWorktree(worldFrom(c), harp, "unknowable", 0, false)
		return err
	})

	ctx.Step(`^session "([^"]*)" left a scratch worktree whose owning process is still alive$`, func(c context.Context, harp string) error {
		_, err := j001300AddScratchWorktree(worldFrom(c), harp, "live", os.Getpid(), false)
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
		if _, err := j001300Git(w, dir, "add", "-A"); err != nil {
			return err
		}
		_, err = j001300Git(w, dir, "commit", "-m", "unmerged feature work")
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
		return j001300Answered(worldFrom(c), "`ctxloom doctor`", ".ctxloom", "manage gitignore install")
	})

	ctx.Step(`^the checks name the foreign worktree, that it is unmerged and dirty, and the exact commands to remove it$`, func(c context.Context) error {
		w := worldFrom(c)
		return j001300Answered(w, "doctor's foreign-worktree report",
			"proj--stale-feature", "unmerged", "git worktree remove", "git branch -d")
	})

	ctx.Step(`^the checks warn that the design notes sit in the harp directory's unclassified top level$`, func(c context.Context) error {
		w := worldFrom(c)
		return j001300Answered(w, "doctor's harp-durability check (B13)",
			".plan.md", "persist")
	})

	// --- session worktrees --------------------------------------------------

	ctx.Step(`^the report names each scratch worktree with its harp, its owner and its verdict$`, func(c context.Context) error {
		w := worldFrom(c)
		return j001300Answered(w, "`ctxloom session worktrees`",
			"ctxloom-wt-clean", "ctxloom-wt-wip", fmt.Sprintf("%d", j001300DeadPid))
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

	ctx.Step(`^the report says why each spared worktree was left alone$`, func(c context.Context) error {
		return j001300Answered(worldFrom(c), "the reap report", "spared", "skipped")
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
		return j001300Answered(worldFrom(c), "`ctxloom session purge`", "transcript", "essence")
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
		idx, err := w.env.ReadHomeFile(j001300SessionsRel + "/index.yaml")
		if err != nil {
			return fmt.Errorf("the session index was destroyed: %w", err)
		}
		for _, name := range st.order {
			if !strings.Contains(idx, name) {
				return fmt.Errorf("%s's index entry is gone. A purged session must remain in the index MARKED purged — "+
					"a session that vanishes from the index is indistinguishable from one that never existed. Index:\n%s", name, idx)
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
			if !strings.Contains(w.env.LastOutput(), ".plan.md") {
				return fmt.Errorf("the authored notes survived but the purge never NAMED them. They are surfaced for the lessons "+
					"step or manual filing, not silently skipped — a file kept but never mentioned is a file nobody will ever file. Output:\n%s",
					w.env.LastOutput())
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
		return j001300Answered(w, "the refusal",
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
		out, err := j001300Git(w, w.env.ProjectDir, "worktree", "list", "--porcelain")
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

	// --- The routine --------------------------------------------------------

	ctx.Step(`^ctxloom resolves a shipped, signed cleanup routine$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		if strings.Contains(out, "not found") || strings.Contains(out, "no such command") || strings.Contains(out, "unknown command") {
			return fmt.Errorf("no shipped `cleanup` command exists to run (exit %d). The one-thing-you-run affordance is recovered "+
				"through ctxloom's own mechanism — a first-party signed bundle command reached by `run -r` — precisely so that no "+
				"top-level cleanup verb has to be added. Output:\n%s", w.env.LastExitCode(), out)
		}
		return nil
	})
}
