//go:build acceptance

// J002600: "tasks aren't context" (j002600_worktree_task_store.feature).
// internal/projectroot.TaskStoreRoot and its wiring through
// internal/taskloom/workdir/workdir.go and internal/cli/taskstore_identity.go
// are already unit-tested at the resolution tier (taskstest.
// RealGitWorktreeFixture, TestTaskStoreWorkDir_*, workdir_test.go). This
// journey does not re-prove that resolution logic — it proves the
// end-to-end behavior those unit tests cannot see: that the REAL `taskloom`
// binary, run as a separate OS process from inside a REAL linked git
// worktree (a genuine `git worktree add`, not a fixture the resolver merely
// classifies), actually WRITES a filed task into the PRIMARY checkout's
// on-disk store, that the primary checkout can READ it back, that the
// redirect mints no SECOND store alongside the first, and that a worktree
// carrying its own .ctxloom is honored as a deliberately separate project.
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/shared/tasks"
)

// j002600State is J002600's fixture state: the linked worktrees created so far, keyed
// by the name they were given in the Gherkin (also their branch name and
// their directory's basename).
type j002600State struct {
	worktrees map[string]string // worktree name -> absolute directory
}

func j002600Of(w *World) *j002600State {
	if w.j002600 == nil {
		w.j002600 = &j002600State{worktrees: map[string]string{}}
	}
	return w.j002600
}

func registerJ002600Steps(ctx *godog.ScenarioContext) {
	ctx.Step(`^Alice has a git-backed taskloom project$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := w.env.InitGitRepo(); err != nil {
			return err
		}
		// `git worktree add -b` needs a valid HEAD to branch from.
		if err := w.env.WriteFile("README.md", "# j002600 fixture\n"); err != nil {
			return err
		}
		return w.env.GitCommit("initial commit")
	})

	ctx.Step(`^Alice files a task "([^"]*)" from the primary checkout$`, func(c context.Context, text string) error {
		w := worldFrom(c)
		_, stderr, err := w.env.RunTaskloom(nil, "add", text)
		if err != nil {
			return fmt.Errorf("file task %q from the primary checkout: %w: %s", text, err, stderr)
		}
		return nil
	})

	ctx.Step(`^Alice adds a linked git worktree named "([^"]*)"$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		j002600 := j002600Of(w)
		dir, err := w.env.AddGitWorktree(name)
		if err != nil {
			return fmt.Errorf("add linked worktree %q: %w", name, err)
		}
		j002600.worktrees[name] = dir
		return nil
	})

	ctx.Step(`^(?:Bob|Carol)'s worktree "([^"]*)" has its own \.ctxloom$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		j002600 := j002600Of(w)
		dir, ok := j002600.worktrees[name]
		if !ok {
			return fmt.Errorf("no linked worktree named %q registered", name)
		}
		// TaskStoreRoot's opt-out check (internal/projectroot/taskstore.go)
		// only cares whether <dir>/.ctxloom exists as a directory — this is
		// the same shape an explicit `ctxloom init` there would leave.
		if err := os.MkdirAll(filepath.Join(dir, ".ctxloom"), 0755); err != nil {
			return fmt.Errorf("create %s/.ctxloom: %w", dir, err)
		}
		return nil
	})

	ctx.Step(`^(?:Bob|Carol) files a task "([^"]*)" from worktree "([^"]*)"$`, func(c context.Context, text, name string) error {
		w := worldFrom(c)
		j002600 := j002600Of(w)
		dir, ok := j002600.worktrees[name]
		if !ok {
			return fmt.Errorf("no linked worktree named %q registered", name)
		}
		_, stderr, err := w.env.RunTaskloomIn(dir, nil, "add", text)
		if err != nil {
			return fmt.Errorf("file task %q from worktree %q: %w: %s", text, name, err, stderr)
		}
		return nil
	})

	ctx.Step(`^the primary checkout's taskloom lists a task "([^"]*)"$`, func(c context.Context, text string) error {
		found, all, err := j002600PrimaryListContains(worldFrom(c), text)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("task %q not found via the primary checkout's taskloom; got %v", text, all)
		}
		return nil
	})

	ctx.Step(`^the primary checkout's taskloom does not list a task "([^"]*)"$`, func(c context.Context, text string) error {
		found, all, err := j002600PrimaryListContains(worldFrom(c), text)
		if err != nil {
			return err
		}
		if found {
			return fmt.Errorf("task %q unexpectedly visible via the primary checkout's taskloom; got %v", text, all)
		}
		return nil
	})
}

// j002600PrimaryListContains runs `taskloom list --json` in the World's primary
// project checkout (e.ProjectDir — not any linked worktree) and reports
// whether a task with the given text is among the results.
func j002600PrimaryListContains(w *World, text string) (found bool, texts []string, err error) {
	out, stderr, err := w.env.RunTaskloom(nil, "list", "--json")
	if err != nil {
		return false, nil, fmt.Errorf("list from primary checkout: %w: %s", err, stderr)
	}
	var got []tasks.Task
	if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
		return false, nil, fmt.Errorf("list --json: not valid JSON: %s", out)
	}
	texts = make([]string, 0, len(got))
	for _, t := range got {
		texts = append(texts, t.Text)
		if t.Text == text {
			found = true
		}
	}
	return found, texts, nil
}
