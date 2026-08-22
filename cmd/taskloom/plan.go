package main

import (
	"fmt"
	"io"
	"sort"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/cliemit"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/plans"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/projectid"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
	"github.com/spf13/cobra"
)

// taskloom owns plans alongside tasks: `plan list` enumerates the session plan
// documents under ~/.ctxloom/sessions/<harp>/persist/*.plan.md (via the shared plans
// package, so the location/frontmatter logic isn't duplicated), and `plan show`
// prints one plan's content. The Plan view in the ctxloom VS Code extension
// reads these.

var planListGlobal bool

var planCmd = &cobra.Command{
	Use:   "plan",
	Short: "Browse session plans (~/.ctxloom/sessions/<harp>/persist/*.plan.md)",
}

var planListCmd = &cobra.Command{
	Use:   "list",
	Short: "List session plans",
	Long: `List session plans (~/.ctxloom/sessions/<harp>/persist/*.plan.md).

By default a listing is scoped to the CURRENT project, resolved exactly the
way ` + "`taskloom list`" + ` resolves it (--project, else CTXLOOM_PROJECT_ID,
else cwd) and joined to plans through the session index: each plan lives in a
session directory, and ~/.ctxloom/sessions/index.yaml records which project
directory that session ran in. Pass --global to list every project's plans.

A plan whose session has no index entry — an ephemeral or worktree session, a
pruned entry, a hand-written plan file — cannot be attributed to any project.
Those are ALWAYS listed, in scoped listings too, with "-" in the project
column. A plan is never hidden merely because we could not work out whose it
is; only plans that positively belong to a DIFFERENT project are excluded.

When no project can be resolved at all — not inside a git repo, no
CTXLOOM_ROOT override, and no prior history at this exact path — the listing
falls back to --global on its own, with a notice on stderr saying why.`,
	RunE: runPlanList,
}

var planShowCmd = &cobra.Command{
	Use:   "show <path>",
	Short: "Print a plan's content",
	Args:  cobra.ExactArgs(1),
	RunE:  runPlanShow,
}

// planListOptions is the resolved input to `plan list`, separated from cobra so
// the body can be driven directly in tests.
type planListOptions struct {
	Global bool
	Format clifmt.Format
}

// runPlanList is planListCmd's RunE: it resolves the task context and format
// off cobra, then hands the real work to runPlanListCmd.
func runPlanList(cmd *cobra.Command, args []string) error {
	tc, err := taskContext()
	if err != nil {
		return err
	}
	format, err := cliemit.Resolve(cmd)
	if err != nil {
		return err
	}
	return runPlanListCmd(cmd.OutOrStdout(), cmd.ErrOrStderr(), tc, planListOptions{
		Global: planListGlobal,
		Format: format,
	})
}

// runPlanShow is planShowCmd's RunE.
func runPlanShow(cmd *cobra.Command, args []string) error {
	content, err := plans.Show(args[0])
	if err != nil {
		return err
	}
	_, err = cmd.OutOrStdout().Write([]byte(content))
	return err
}

// runPlanListCmd is planListCmd's body. out/errw are separate so stdout stays
// parseable while stderr carries the scope diagnostics, matching `taskloom
// list`.
//
// Scope resolution goes through the SAME resolveListScope the task listings
// use — a second, parallel scoping mechanism beside it would be the defect,
// not the fix — and the project directory it scopes to is resolved by
// planScopeDir.
func runPlanListCmd(out, errw io.Writer, tc operations.TaskContext, opts planListOptions) error {
	scope, err := resolveListScope(opts.Global, tc.ProjectID, tc.WorkDir, tc.WorkDirIsBoundary)
	if err != nil {
		return err
	}

	projectDir := ""
	if !scope.Global {
		dir, notice, err := planScopeDir(tc)
		if err != nil {
			return err
		}
		if dir == "" {
			// A pinned project-id that no longer resolves to a path is not a
			// reason to list nothing: widen and say why.
			scope = listScope{Global: true, Notice: notice}
		} else {
			projectDir = dir
		}
	}

	if scope.Notice != "" {
		clidiag.Fwarn(errw, progName, "%s", scope.Notice)
	}

	var list []plans.Plan
	if scope.Global {
		all, err := plans.ListHome()
		if err != nil {
			return err
		}
		if list, err = plans.AttributeAll(all); err != nil {
			return err
		}
	} else {
		matched, unattributed, err := plans.ListHomeScoped(projectDir)
		if err != nil {
			return err
		}
		// The unattributed set is merged in, not appended as a tail block, so
		// the listing keeps one stable session-then-name order regardless of
		// how much of the session index survives.
		list = append(matched, unattributed...)
		sortPlans(list)
	}

	return renderOrEmit(out, list, opts.Format)
}

// planScopeDir resolves the project DIRECTORY a scoped plan listing filters
// on. The session index records project_dir, not project-id, so a directory is
// the join key: a pinned project-id is translated through the project registry
// first, and cwd is used otherwise.
//
// An empty dir return means "no directory could be resolved" and carries the
// notice explaining it; the caller widens to global rather than listing
// nothing.
func planScopeDir(tc operations.TaskContext) (dir, notice string, err error) {
	if tc.ProjectID != "" {
		pm, err := projectid.Open("")
		if err != nil {
			return "", "", fmt.Errorf("open project registry: %w", err)
		}
		e, err := pm.ResolveByID(tc.ProjectID)
		if err != nil {
			return "", "", err
		}
		if e != nil && e.Path != "" {
			return e.Path, "", nil
		}
		return "", fmt.Sprintf("project-id %q is not in the project registry, so its plans can't be identified; listing every project's plans (--global)", tc.ProjectID), nil
	}
	if tc.WorkDir == "" {
		return "", "no project directory could be resolved; listing every project's plans (--global)", nil
	}
	return tc.WorkDir, "", nil
}

// sortPlans restores the plans package's session-then-name ordering after two
// buckets have been merged.
func sortPlans(list []plans.Plan) {
	sort.Slice(list, func(i, j int) bool {
		if list[i].Session != list[j].Session {
			return list[i].Session < list[j].Session
		}
		return list[i].Name < list[j].Name
	})
}

// renderOrEmit writes the listing in the requested format: the bespoke human
// table for text, clifmt for everything else. It mirrors cliemit.Emit but takes
// an already-resolved format and an explicit writer, since the body is driven
// without cobra in tests.
func renderOrEmit(out io.Writer, list []plans.Plan, format clifmt.Format) error {
	if format == clifmt.FormatText || format == "" {
		return renderPlanTable(out, list)
	}
	return clifmt.Render(out, list, format)
}

// renderPlanTable writes the human-readable plan listing: one row per plan as
// "<session>\t<name>\t<title>\t<project>\t<path>", grouped naturally by the
// sorted session order. The project column is "-" for a plan that could not be
// attributed to any project — shown, never dropped.
//
// The path is last and it is the point: `plan show` accepts a PATH and nothing
// else, so a listing without it cannot be piped into it — a reader had to
// rebuild the path by hand from the documented layout. It is appended rather
// than inserted so the existing four columns keep their positions.
func renderPlanTable(out io.Writer, list []plans.Plan) error {
	w := iox.NewErrWriter(out)
	if len(list) == 0 {
		w.Printf("(no plans)\n")
		return w.Err()
	}
	for _, p := range list {
		project := p.ProjectDir
		if project == "" {
			project = "-"
		}
		w.Printf("%s\t%s\t%s\t%s\t%s\n", p.Session, p.Name, p.Title, project, p.Path)
	}
	return w.Err()
}

func init() {
	planListCmd.Flags().BoolVar(&planListGlobal, "global", false,
		"list every project's plans, not just the current project's")
	planCmd.AddCommand(planListCmd, planShowCmd)
	rootCmd.AddCommand(planCmd)
}
