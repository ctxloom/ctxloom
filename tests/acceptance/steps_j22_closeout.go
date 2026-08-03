//go:build acceptance

// J22: "the close-out" (j22_closeout.feature) — FLOWS-UNIFIED.md's U11.
//
// THIS JOURNEY SPECIFIES A SURFACE THAT DOES NOT EXIST. Almost every scenario
// here is red, and that is the deliverable: `session worktrees`,
// `session purge`, `session distill --skill/--to-bundle`, and doctor's five
// new checks are the C10 set FLOWS-UNIFIED proposes, and this file is their
// acceptance specification. A red scenario naming a real gap is worth more
// than a green one that dodged the assertion.
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
// j22DeadPid exceeds even a kernel.pid_max=4194304 configuration by a wide
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

	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

const (
	// j22DeadPid names no live process on any Linux configuration — see the
	// file doc. Copied deliberately rather than imported: the isolation
	// package's constant is unexported, and an acceptance fixture that
	// silently changed meaning when that package was refactored would be
	// worse than a duplicated literal with this comment attached.
	j22DeadPid = 999999999

	// j22SessionsRel is the harp store, relative to the isolated HOME.
	j22SessionsRel = ".ctxloom/sessions"

	// Markers. Each names a CONTENT CLASS from FLOWS-UNIFIED §5.4, so a purge
	// assertion says which class survived rather than which file did.
	j22BulkMarker     = "J22-MACHINE-WRITTEN-BULK"
	j22EssenceMarker  = "J22-DERIVED-ESSENCE"
	j22AuthoredMarker = "J22-HUMAN-AUTHORED-PLAN"
	j22WIPMarker      = "J22-UNCOMMITTED-WIP"

	// j22LessonsBundle is where the lessons leg lands its fragments.
	j22LessonsBundle = "team-lessons"
	// j22LessonsSkill is the distill skill the lessons leg selects — a ref in
	// the "<bundle>#skills/<name>" grammar FLOWS-UNIFIED §5.3 specifies.
	j22LessonsSkill = "house-style#skills/lessons"
)

// j22Harp records one seeded harp directory and what was planted in it.
type j22Harp struct {
	name      string
	dir       string // absolute
	essence   bool
	authored  bool
	worktrees []string // absolute scratch-worktree dirs under this harp
}

// j22State is this journey's fixture state.
type j22State struct {
	harps   map[string]*j22Harp
	order   []string // seeding order, so the index renders deterministically
	foreign map[string]string

	signer    *testenv.TestSigner
	stopAgent func() error
	ready     bool

	probes []j21ProbeResult
}

func j22Of(w *World) *j22State {
	if w.j22 == nil {
		w.j22 = &j22State{harps: map[string]*j22Harp{}, foreign: map[string]string{}}
	}
	return w.j22
}

// j22Git runs a git command in dir under the harness's isolated environment.
func j22Git(w *World, dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = w.env.Command(nil, "version").Env // the isolated env every helper trusts
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s in %s: %s: %w", strings.Join(args, " "), dir, out, err)
	}
	return string(out), nil
}

// j22Setup is the Background: a real git repo with a commit to branch
// worktrees from, a configured project, and Carol's signing key live, since
// the lessons leg must SIGN what it writes.
func j22Setup(w *World) error {
	st := j22Of(w)
	if st.ready {
		return nil
	}
	if err := ensureProjectWithEngine(w, "claude-code", "claude-code"); err != nil {
		return err
	}
	// `git worktree add -b` needs a valid HEAD to branch from.
	if err := w.env.WriteFile("README.md", "# j22 close-out fixture\n"); err != nil {
		return err
	}
	if err := w.env.GitCommit("initial commit"); err != nil {
		return err
	}

	signer, err := testenv.GenerateTestSigner()
	if err != nil {
		return fmt.Errorf("generate the close-out signing key: %w", err)
	}
	st.signer = signer
	if err := w.env.WriteFile("closeout.pub", signer.AuthorizedKey("alice@acme.example")); err != nil {
		return err
	}
	sock, stop, err := testenv.StartSSHAgent(w.env.Root, testenv.SSHAgentIdentity{Signer: signer, Comment: "alice@acme.example"})
	if err != nil {
		return fmt.Errorf("start hermetic ssh-agent: %w", err)
	}
	st.stopAgent = stop
	w.env.SetEnv("SSH_AUTH_SOCK", sock)
	if err := w.env.GitConfigLocal("user.signingkey", filepath.Join(w.env.ProjectDir, "closeout.pub")); err != nil {
		return err
	}

	st.ready = true
	return nil
}

// j22HarpDir returns the absolute path of a harp's directory in the isolated
// home — ~/.ctxloom/sessions/<harp>, the layout paths.HarpDir builds.
func j22HarpDir(w *World, harp string) string {
	return filepath.Join(w.env.HomeDir, filepath.FromSlash(j22SessionsRel), harp)
}

// j22SeedHarp plants one harp directory carrying every content class §5.4
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
func j22SeedHarp(w *World, harp string, essence, authored bool) error {
	st := j22Of(w)
	dir := j22HarpDir(w, harp)
	for _, sub := range []string{"persist/transcripts", "ephemeral"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(sub)), 0o755); err != nil {
			return fmt.Errorf("create %s/%s: %w", harp, sub, err)
		}
	}
	// Machine-written bulk, deliberately large enough that "bytes freed" is a
	// meaningful number rather than a rounding artifact.
	bulk := strings.Repeat(`{"role":"assistant","content":"`+j22BulkMarker+`"}`+"\n", 200)
	if err := os.WriteFile(filepath.Join(dir, "transcript.jsonl"), []byte(bulk), 0o644); err != nil {
		return fmt.Errorf("write %s transcript: %w", harp, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "persist", "transcripts", "turns.jsonl"), []byte(bulk), 0o644); err != nil {
		return fmt.Errorf("write %s persisted transcript: %w", harp, err)
	}
	if essence {
		body := fmt.Sprintf("---\nharp_name: %s\ndistilled_at: 2026-01-01T00:00:00Z\n---\n\n%s for %s.\n", harp, j22EssenceMarker, harp)
		if err := os.WriteFile(filepath.Join(dir, "essence.md"), []byte(body), 0o644); err != nil {
			return fmt.Errorf("write %s essence: %w", harp, err)
		}
	}
	if authored {
		body := fmt.Sprintf("# %s design notes\n\n%s\n\nDecisions this session reached that exist nowhere else.\n", harp, j22AuthoredMarker)
		if err := os.WriteFile(filepath.Join(dir, harp+".plan.md"), []byte(body), 0o644); err != nil {
			return fmt.Errorf("write %s authored plan: %w", harp, err)
		}
	}

	if _, seen := st.harps[harp]; !seen {
		st.order = append(st.order, harp)
	}
	st.harps[harp] = &j22Harp{name: harp, dir: dir, essence: essence, authored: authored}
	return j22WriteIndex(w)
}

// j22WriteIndex rewrites the session index over EVERY seeded harp. The shared
// "a recorded session" fixture step writes a single-entry index, which would
// silently drop earlier harps; a close-out journey is inherently multi-harp,
// so it owns its own accumulating writer.
func j22WriteIndex(w *World) error {
	st := j22Of(w)
	var b strings.Builder
	b.WriteString("sessions:\n")
	for _, name := range st.order {
		fmt.Fprintf(&b, "  - harp_name: %s\n", name)
		fmt.Fprintf(&b, "    session_id: seeded-%s\n", name)
		b.WriteString("    backend: claude-code\n")
		fmt.Fprintf(&b, "    project_dir: %s\n", w.env.ProjectDir)
		b.WriteString("    started_at: 2026-01-01T00:00:00Z\n")
		b.WriteString("    ended_at: 2026-01-02T00:00:00Z\n")
		fmt.Fprintf(&b, "    transcript_path: %s\n", filepath.Join(j22HarpDir(w, name), "transcript.jsonl"))
		fmt.Fprintf(&b, "    summary: seeded close-out session %s\n", name)
	}
	return w.env.WriteHomeFile(j22SessionsRel+"/index.yaml", b.String())
}

// j22AddScratchWorktree creates a REAL linked git worktree inside a harp's own
// ephemeral directory, named with the "ctxloom-wt-" prefix the candidate
// finder matches, plus the sibling ".owner.pid" marker the reaper reads.
// ownerPid of 0 means "write no marker at all" — the "can't prove who owned
// this" case, which the reaper must treat exactly like a live owner.
func j22AddScratchWorktree(w *World, harp, name string, ownerPid int, dirty bool) (string, error) {
	st := j22Of(w)
	h, ok := st.harps[harp]
	if !ok {
		return "", fmt.Errorf("harp %q has not been seeded", harp)
	}
	wtDir := filepath.Join(h.dir, "ephemeral", "ctxloom-wt-"+name)
	if _, err := j22Git(w, w.env.ProjectDir, "worktree", "add", "-q", "-b", "wt-"+name, wtDir); err != nil {
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
		if err := os.WriteFile(filepath.Join(wtDir, "in-flight.go"), []byte("// "+j22WIPMarker+"\n"), 0o644); err != nil {
			return "", fmt.Errorf("plant WIP in %s: %w", wtDir, err)
		}
	}
	h.worktrees = append(h.worktrees, wtDir)
	return wtDir, nil
}

// j22AddForeignWorktree creates a long-lived worktree OUTSIDE the sessions
// root — the ~/workspace/worktrees/<project>--<branch> population. It is
// invisible to the candidate finder by construction, and ctxloom may only ever
// report on it.
func j22AddForeignWorktree(w *World, branch string, dirty bool) (string, error) {
	st := j22Of(w)
	dir := filepath.Join(w.env.HomeDir, "workspace", "worktrees", "proj--"+branch)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", fmt.Errorf("create foreign worktrees parent: %w", err)
	}
	if _, err := j22Git(w, w.env.ProjectDir, "worktree", "add", "-q", "-b", branch, dir); err != nil {
		return "", err
	}
	if dirty {
		if err := os.WriteFile(filepath.Join(dir, "uncommitted.txt"), []byte(j22WIPMarker+"\n"), 0o644); err != nil {
			return "", fmt.Errorf("plant WIP in foreign tree: %w", err)
		}
	}
	st.foreign[branch] = dir
	return dir, nil
}

// j22Probe records one candidate invocation of a surface that may not exist.
func j22Probe(w *World, args ...string) {
	st := j22Of(w)
	_ = w.env.Run(args...)
	st.probes = append(st.probes, j21ProbeResult{
		args:   append([]string(nil), args...),
		exit:   w.env.LastExitCode(),
		output: w.env.LastOutput(),
	})
}

// j22Answered reports whether the LAST run's output names every want, failing
// with the whole output and the exit code so a red scenario documents what the
// product said instead.
func j22Answered(w *World, what string, wants ...string) error {
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

// j22DirExists is a plain on-disk existence check on an absolute path — the
// only honest way to assert a reap happened or did not.
func j22DirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// j22RanRealSurface fails unless the last invocation actually reached a
// command that EXISTS.
//
// This guard is the difference between a specification and a lie. Several
// scenarios in this journey assert that something was NOT destroyed, NOT
// listed, or NOT reported as success — and every one of those assertions is
// trivially satisfied by a CLI that rejected the invocation before doing
// anything at all. Measured while writing this file: three scenarios passed
// green purely because `session purge` and `session worktrees --reap` do not
// exist, so of course they destroyed nothing. A green scenario that dodged its
// own assertion is worth less than a red one, because it reports coverage
// where there is none.
//
// So every negative assertion runs through here first, and a missing surface
// is reported as a missing surface rather than as a passing safety property.
func j22RanRealSurface(w *World) error {
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

func registerJ22Steps(ctx *godog.ScenarioContext) {
	ctx.After(func(c context.Context, sc *godog.Scenario, err error) (context.Context, error) {
		w := worldFrom(c)
		if w == nil || w.j22 == nil {
			return c, nil
		}
		if w.j22.stopAgent != nil {
			stop := w.j22.stopAgent
			w.j22.stopAgent = nil
			if serr := stop(); serr != nil {
				return c, fmt.Errorf("stop hermetic ssh-agent: %w", serr)
			}
		}
		return c, nil
	})

	// --- Background ---------------------------------------------------------

	ctx.Step(`^the feature shipped on Friday and Alice is closing the workstream out$`, func(c context.Context) error {
		return j22Setup(worldFrom(c))
	})

	// --- Debris fixtures ----------------------------------------------------

	ctx.Step(`^a finished session "([^"]*)" whose work is already distilled$`, func(c context.Context, harp string) error {
		return j22SeedHarp(worldFrom(c), harp, true, false)
	})

	ctx.Step(`^a finished session "([^"]*)" that was never distilled$`, func(c context.Context, harp string) error {
		return j22SeedHarp(worldFrom(c), harp, false, false)
	})

	ctx.Step(`^a finished session "([^"]*)" carrying design notes nobody filed$`, func(c context.Context, harp string) error {
		return j22SeedHarp(worldFrom(c), harp, true, true)
	})

	ctx.Step(`^session "([^"]*)" left a clean scratch worktree whose owning process is dead$`, func(c context.Context, harp string) error {
		_, err := j22AddScratchWorktree(worldFrom(c), harp, "clean", j22DeadPid, false)
		return err
	})

	ctx.Step(`^session "([^"]*)" left a scratch worktree holding uncommitted work$`, func(c context.Context, harp string) error {
		_, err := j22AddScratchWorktree(worldFrom(c), harp, "wip", j22DeadPid, true)
		return err
	})

	ctx.Step(`^session "([^"]*)" left a scratch worktree nothing can prove the owner of$`, func(c context.Context, harp string) error {
		// No owner marker at all: "can't prove who owned this" must be treated
		// identically to "still owned by someone alive".
		_, err := j22AddScratchWorktree(worldFrom(c), harp, "unknowable", 0, false)
		return err
	})

	ctx.Step(`^session "([^"]*)" left a scratch worktree whose owning process is still alive$`, func(c context.Context, harp string) error {
		_, err := j22AddScratchWorktree(worldFrom(c), harp, "live", os.Getpid(), false)
		return err
	})

	ctx.Step(`^a long-lived worktree "([^"]*)" of her own, outside the sessions root, with unmerged work$`, func(c context.Context, branch string) error {
		w := worldFrom(c)
		dir, err := j22AddForeignWorktree(w, branch, true)
		if err != nil {
			return err
		}
		// A real commit that is genuinely not on the integration branch, so
		// `git cherry` has something true to say about it.
		if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("unmerged work\n"), 0o644); err != nil {
			return err
		}
		if _, err := j22Git(w, dir, "add", "-A"); err != nil {
			return err
		}
		_, err = j22Git(w, dir, "commit", "-m", "unmerged feature work")
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
		return j22Answered(worldFrom(c), "`ctxloom doctor`", ".ctxloom", "manage gitignore install")
	})

	ctx.Step(`^the checks name the foreign worktree, that it is unmerged and dirty, and the exact commands to remove it$`, func(c context.Context) error {
		w := worldFrom(c)
		return j22Answered(w, "doctor's foreign-worktree report",
			"proj--stale-feature", "unmerged", "git worktree remove", "git branch -d")
	})

	ctx.Step(`^the checks warn that the design notes sit in the harp directory's unclassified top level$`, func(c context.Context) error {
		w := worldFrom(c)
		return j22Answered(w, "doctor's harp-durability check (B13)",
			".plan.md", "persist")
	})

	// --- session worktrees --------------------------------------------------

	ctx.Step(`^the report names each scratch worktree with its harp, its owner and its verdict$`, func(c context.Context) error {
		w := worldFrom(c)
		return j22Answered(w, "`ctxloom session worktrees`",
			"ctxloom-wt-clean", "ctxloom-wt-wip", fmt.Sprintf("%d", j22DeadPid))
	})

	ctx.Step(`^only the clean, provably-orphaned worktree is gone from disk$`, func(c context.Context) error {
		w := worldFrom(c)
		st := j22Of(w)
		var problems []string
		for _, h := range st.harps {
			for _, wt := range h.worktrees {
				base := filepath.Base(wt)
				gone := !j22DirExists(wt)
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
		for _, h := range j22Of(w).harps {
			for _, wt := range h.worktrees {
				if !strings.HasSuffix(filepath.Base(wt), "-wip") {
					continue
				}
				body, err := os.ReadFile(filepath.Join(wt, "in-flight.go"))
				if err != nil {
					return fmt.Errorf("the uncommitted work in %s is GONE — a reap destroyed unrecoverable WIP: %w", wt, err)
				}
				if !strings.Contains(string(body), j22WIPMarker) {
					return fmt.Errorf("the uncommitted work in %s no longer carries its own bytes; it holds:\n%s", wt, body)
				}
				return nil
			}
		}
		return fmt.Errorf("no worktree holding uncommitted work was seeded, so this assertion measured nothing")
	})

	ctx.Step(`^the report says why each spared worktree was left alone$`, func(c context.Context) error {
		return j22Answered(worldFrom(c), "the reap report", "spared", "skipped")
	})

	ctx.Step(`^her own long-lived worktree is untouched and was never listed$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j22RanRealSurface(w); err != nil {
			return err
		}
		st := j22Of(w)
		for branch, dir := range st.foreign {
			if !j22DirExists(dir) {
				return fmt.Errorf("the foreign worktree %s (%s) was REMOVED — ctxloom must never remove a worktree it did not create", branch, dir)
			}
			if strings.Contains(w.env.LastOutput(), dir) {
				return fmt.Errorf("`session worktrees` listed the foreign worktree %s; that population is doctor's to REPORT on, "+
					"and listing it under a verb that also reaps invites exactly the removal that is forbidden. Output:\n%s", dir, w.env.LastOutput())
			}
		}
		return nil
	})

	// --- Lessons: session distill --skill --to-bundle ------------------------

	ctx.Step(`^Alice's house lessons-extraction skill is trusted content she can select$`, func(c context.Context) error {
		w := worldFrom(c)
		// A directory-form bundle: skills require one (single-file bundles
		// refuse with a re-init message), and the lessons skill is a package.
		dir := ".ctxloom/content/bundles/house-style"
		if err := w.env.WriteFile(dir+"/bundle.yaml", "version: \"1.0.0\"\ndescription: house authoring style\n"); err != nil {
			return err
		}
		return w.env.WriteFile(dir+"/skills/lessons/SKILL.md",
			"---\nname: lessons\ndescription: extract decisions, traps and invariants as candidate fragments\n---\n\n"+
				"Read the session and emit, for each decision reached, one candidate fragment stating the decision and why.\n")
	})

	ctx.Step(`^the lessons land as fragments in the "([^"]*)" bundle$`, func(c context.Context, bundle string) error {
		w := worldFrom(c)
		rel := ".ctxloom/content/bundles/" + bundle + ".yaml"
		body, err := w.env.ReadFile(rel)
		if err != nil {
			// Directory form is equally acceptable — check before failing.
			if dirBody, derr := w.env.ReadFile(".ctxloom/content/bundles/" + bundle + "/bundle.yaml"); derr == nil {
				body = dirBody
			} else {
				return fmt.Errorf("no %q bundle was written at all (exit %d). ctxloom reported:\n%s\n(read error: %v)",
					bundle, w.env.LastExitCode(), w.env.LastOutput(), err)
			}
		}
		if !strings.Contains(body, "fragments:") {
			return fmt.Errorf("the %q bundle exists but carries no fragments section — a lessons run that writes a bundle and no lessons "+
				"is this codebase's characteristic bug. It holds:\n%s", bundle, body)
		}
		return nil
	})

	ctx.Step(`^that bundle is signed in the same breath$`, func(c context.Context) error {
		w := worldFrom(c)
		for _, rel := range []string{
			".ctxloom/content/bundles/" + j22LessonsBundle + ".yaml.sig",
			".ctxloom/content/bundles/" + j22LessonsBundle + "/bundle.yaml.sig",
		} {
			if body, err := w.env.ReadFile(rel); err == nil {
				if strings.TrimSpace(body) == "" {
					return fmt.Errorf("%s exists but is EMPTY — a signature file carrying nothing", rel)
				}
				return nil
			}
		}
		return fmt.Errorf("the lessons bundle was written UNSIGNED. An edited-unsigned bundle is silently withheld "+
			"(boundary B2, and J21's own red scenario), so a lessons flow that ends unsigned manufactures the exact defect "+
			"it exists to close. ctxloom reported (exit %d):\n%s", w.env.LastExitCode(), w.env.LastOutput())
	})

	ctx.Step(`^ctxloom refuses rather than falling back to its built-in prompt$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j22RanRealSurface(w); err != nil {
			return err
		}
		if w.env.LastExitCode() == 0 {
			return fmt.Errorf("ctxloom exited 0 with a withheld distill skill selected. cli.loadDistillPrompt swallows EVERY "+
				"GetCommand error including ErrCommandWithheld and silently falls back to the embedded prompt, so a trust decision "+
				"silently changes which prompt runs — and the user is never told their chosen skill was not the one used. Output:\n%s",
				w.env.LastOutput())
		}
		return j22Answered(w, "the refusal", "withheld", "review")
	})

	ctx.Step(`^nothing was written into the "([^"]*)" bundle$`, func(c context.Context, bundle string) error {
		w := worldFrom(c)
		if err := j22RanRealSurface(w); err != nil {
			return err
		}
		for _, rel := range []string{
			".ctxloom/content/bundles/" + bundle + ".yaml",
			".ctxloom/content/bundles/" + bundle + "/bundle.yaml",
		} {
			if body, err := w.env.ReadFile(rel); err == nil && strings.Contains(body, "fragments:") {
				return fmt.Errorf("%s carries fragments after a run that should have written nothing; it holds:\n%s", rel, body)
			}
		}
		return nil
	})

	ctx.Step(`^ctxloom reports that it extracted nothing, and does not call that success$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j22RanRealSurface(w); err != nil {
			return err
		}
		if w.env.LastExitCode() == 0 {
			return fmt.Errorf("ctxloom exited 0 having written zero fragments. 'wrote 0 fragments' must be exit-nonzero, never success — "+
				"the standing silent-no-op rule, and the failure mode this whole suite exists to catch. Output:\n%s", w.env.LastOutput())
		}
		return nil
	})

	// --- session purge ------------------------------------------------------

	ctx.Step(`^the report lists what would be destroyed and what would be kept$`, func(c context.Context) error {
		return j22Answered(worldFrom(c), "`ctxloom session purge`", "transcript", "essence")
	})

	ctx.Step(`^every byte of every session is still on disk$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j22RanRealSurface(w); err != nil {
			return err
		}
		st := j22Of(w)
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
		h, ok := j22Of(w).harps[harp]
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

	ctx.Step(`^its distilled essence and its index entry survive$`, func(c context.Context) error {
		w := worldFrom(c)
		st := j22Of(w)
		for _, h := range st.harps {
			if !h.essence {
				continue
			}
			body, err := os.ReadFile(filepath.Join(h.dir, "essence.md"))
			if err != nil {
				return fmt.Errorf("the distilled essence of %s was destroyed: %w. Essence is DERIVED and cheap to keep — "+
					"purging it destroys the only durable record of what the session concluded", h.name, err)
			}
			if !strings.Contains(string(body), j22EssenceMarker) {
				return fmt.Errorf("%s's essence no longer carries its own bytes; it holds:\n%s", h.name, body)
			}
		}
		idx, err := w.env.ReadHomeFile(j22SessionsRel + "/index.yaml")
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
		for _, h := range j22Of(w).harps {
			if !h.authored {
				continue
			}
			p := filepath.Join(h.dir, h.name+".plan.md")
			body, err := os.ReadFile(p)
			if err != nil {
				return fmt.Errorf("HUMAN-AUTHORED design notes at %s were DESTROYED: %w. Authored artifacts are never "+
					"negotiable — a cleanup that eats the only copy of a design nobody filed is worse than no cleanup at all", p, err)
			}
			if !strings.Contains(string(body), j22AuthoredMarker) {
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

	ctx.Step(`^ctxloom refuses, naming the session that was never distilled$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j22RanRealSurface(w); err != nil {
			return err
		}
		if w.env.LastExitCode() == 0 {
			return fmt.Errorf("`--everything` proceeded against an undistilled session (exit 0). Destroying the only record of a "+
				"never-summarized session must take maximal friction — its own flag, on top of the typed confirmation. Output:\n%s",
				w.env.LastOutput())
		}
		return j22Answered(w, "the refusal", "undistilled")
	})

	ctx.Step(`^the scratch worktrees are still registered with git, untouched by the purge$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j22RanRealSurface(w); err != nil {
			return err
		}
		st := j22Of(w)
		out, err := j22Git(w, w.env.ProjectDir, "worktree", "list", "--porcelain")
		if err != nil {
			return err
		}
		for _, h := range st.harps {
			for _, wt := range h.worktrees {
				if !j22DirExists(wt) {
					return fmt.Errorf("the purge removed the scratch worktree %s. A purge NEVER runs git operations — reaping is "+
						"`session worktrees --reap`'s job, under the safety semantics a purge does not implement", wt)
				}
				if !strings.Contains(out, wt) {
					return fmt.Errorf("the purge deregistered %s from git. `git worktree list` says:\n%s", wt, out)
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
