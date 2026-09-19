package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/projectroot"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

var (
	cleanYes            bool
	cleanOlderThan      string
	cleanIncludePersist bool
)

// `ctxloom clean` obeys the same rule the session destroyers do: ABSENCE OF
// --yes MEANS REPORT ONLY, on a TTY or not, and the report says so out loud
// (see session_purge_cmd.go's header). There is deliberately no --dry-run — a
// second spelling of a contract this project already has would only invite the
// two to disagree.
var cleanCmd = &cobra.Command{
	Use:   "clean",
	Short: "Remove this project's regenerable cache, keeping everything a clone cannot restore",
	Long: `Removes .ctxloom/cache — the pulled bundle copies, the git clone cache,
the assembled context files and the refused-advance record. Every one of them
is rebuilt by a command this report names, so the only cost is the time to
re-run it.

Nothing else is touched by default. Your authored content and profiles are
committed and a clone has them. Your approvals and application records are
LOCAL-ONLY — nothing rebuilds them, so clean never takes them, and neither
does --yes. lock.yaml survives too: it is rebuildable but committed, so
deleting it would dirty your tree rather than free anything.

Every session also leaves a directory under ~/.ctxloom/sessions/<harp>/,
and clean reaps the DISPOSABLE part of it by age: ephemeral/, the scratch
store (agent worktrees, rendered overlays) whose loss costs nothing. A
session is aged when nothing anywhere in it has been modified for longer
than the bound — 30d unless your ~/.ctxloom/config.yaml sets
session_reap_age, and --older-than overrides either for one invocation:

  ctxloom clean --older-than 30d          an offset: 30d, 12w, 720h
  ctxloom clean --older-than 2026-01-01   or a date

persist/ — transcripts, plans, session artifacts — is REFERENCED DATA:
task rows and design records cite paths in it, so no age takes it. Pass
--include-persist to reclaim it too, from the same aged sessions. The
session's own record (its sidecar, essence and rotation segments) is never
taken: the directory stays, and the session still lists and resolves.

To exempt one session from every sweep, place an empty file named 'keep'
at the top of its directory.

A session is reclaimed only when its liveness lock proves its owner has
ended. A running session, or one whose liveness cannot be established at
all, is reported and left alone. A session holding a scratch worktree with
uncommitted work is reported and left alone too — that work exists nowhere
else.

A session that predates the liveness lock has no lock file, so its owner can
never be proven dead — and this sweep, which ranges over every session at
once, will NEVER reclaim it. That is deliberate, not a gap: nothing but the
lock can tell such a session apart from one still running, and a bulk sweep
is the wrong place to gamble a live session's scratch on a guess. Clearing
one is a per-session decision a human makes by naming it:
'ctxloom session transcript purge <harp> --even-if-live' (and the artifacts
counterpart) destroy the machine-written bulk of the one session you name.

Without --yes this only reports; nothing on disk changes.

clean is not uninstall. What it takes comes back on your next run, because
that is what regenerable means. To strip ctxloom's integration with this
project — its hooks, statusline, MCP registration and generated command
files — use 'ctxloom manage uninstall'.

Neither command makes ctxloom stay gone: running ctxloom in this project
again re-delivers every surface, because nothing records that you removed
them.`,
	Args: cobra.NoArgs,
	RunE: runClean,
}

func runClean(cmd *cobra.Command, _ []string) error {
	// The project root comes from projectroot, NOT from config.
	//
	// The cache is what you reach for when the project is broken, so clean
	// must not need the project to be loadable in order to find it. Config
	// resolution is fault-tolerant today and would very likely have answered
	// too — but "very likely" is the wrong dependency for the one command
	// whose job is to work on a project that does not. projectroot answers
	// from CTXLOOM_ROOT or the git boundary and parses nothing.
	appDir := filepath.Join(projectroot.WorkDir(), paths.AppDirName)
	home, err := os.UserHomeDir()
	if err != nil {
		// Only RootHome entries need it, and no cache entry is one today; an
		// unresolvable home must not stop a project-scoped clean.
		home = ""
	}

	res, err := operations.CleanCache(appDir, home, cleanYes)
	if err != nil {
		return err
	}
	rep := cleanReport{CleanResult: res}

	cutoff, err := sessionReapCutoff(time.Now())
	if err != nil {
		return err
	}
	scope := operations.ReclaimEphemeral
	if cleanIncludePersist {
		scope = operations.ReclaimEphemeralAndPersist
	}
	rep.Sessions, err = operations.ReclaimAgedSessions(cmd.Context(), nil, cutoff, scope, cleanYes)
	if err != nil {
		return err
	}

	if err := emit(cmd, rep, func() error {
		return renderCleanPlan(cmd.OutOrStdout(), rep)
	}); err != nil {
		return err
	}
	if !cleanYes {
		return reportPlanOnly(cmd, "ctxloom clean --yes")
	}
	return nil
}

// sessionReapCutoff resolves the instant that bounds the session sweep:
// --older-than when given (one invocation), else the configured
// session_reap_age, else config.DefaultSessionReapAge.
//
// A value that does not parse is REFUSED, naming where it came from, never
// resolved to the default: a typo in the home config would otherwise reap on
// an age nobody chose. The one tolerated fault is a config that cannot be
// LOADED at all — clean exists to work on a broken project (see
// TestClean_ReachesTheCacheThroughAnUnreadableConfig) — and there the sweep
// proceeds on the built-in default and says so, formatting the value it
// proceeds with from the constant it actually used.
func sessionReapCutoff(now time.Time) (time.Time, error) {
	if cleanOlderThan != "" {
		return parseAgeBound("--older-than", cleanOlderThan, now)
	}
	age := config.DefaultSessionReapAge
	if cfg, err := GetConfig(); err != nil {
		clidiag.Warn("ctxloom", "config could not be loaded (%v); the session sweep proceeds on the built-in session_reap_age %s", err, age)
	} else {
		age = cfg.SessionReapAge()
	}
	return parseAgeBound("session_reap_age", age, now)
}

// cleanReport is `clean`'s payload: the cache plan plus the aged-session
// plan. The sweep always runs (the age is defaulted), so Sessions is always
// present.
type cleanReport struct {
	operations.CleanResult
	Sessions operations.SessionReclaimResult `json:"sessions"`
}

func renderCleanPlan(w io.Writer, rep cleanReport) error {
	out := iox.NewErrWriter(w)
	res := rep.CleanResult
	present := 0
	for _, t := range res.Targets {
		if t.Present {
			present++
		}
	}
	if present == 0 {
		out.Printf("Nothing to clean: this project's cache is already absent.\n")
		return renderSessionReclaim(out, rep.Sessions)
	}

	verb := "would remove"
	if res.Applied {
		verb = "removed"
	}
	out.Printf("ctxloom %s %s of regenerable cache:\n\n", verb, humanBytes(res.Bytes))
	for _, t := range res.Targets {
		if !t.Present {
			continue
		}
		// The rebuild command is printed per path, not once in a footer: they
		// differ, and a caller who cleans is owed the specific command that
		// undoes what they just did to THAT path.
		out.Printf("  %-42s %10s   rebuild: %s\n", t.Rel, humanBytes(t.Bytes), t.Rebuild)
	}
	out.Printf("\n")
	return renderSessionReclaim(out, rep.Sessions)
}

// renderSessionReclaim prints the aged-session plan.
//
// Every candidate is listed, including the skipped ones, because "why did it
// free nothing" is the question a caller actually has — and a session left
// alone for holding someone's uncommitted work is precisely the thing that
// must not be silent. Sessions newer than the bound are the one exception:
// they are counted on a single line, because the sweep now runs on every
// clean and a line per session in use would bury the rest.
func renderSessionReclaim(out *iox.ErrWriter, rep operations.SessionReclaimResult) error {
	if len(rep.Candidates) == 0 {
		out.Printf("No session %s is older than %s", rep.Scope, rep.Cutoff.Format(time.RFC3339))
		if rep.Newer > 0 {
			out.Printf(" (%d sessions were active since then)", rep.Newer)
		}
		out.Printf(".\n")
		return out.Err()
	}

	verb := "would reclaim"
	if rep.Applied {
		verb = "reclaimed"
	}
	out.Printf("ctxloom %s %s of session %s last active before %s:\n\n",
		verb, humanBytes(rep.Bytes), rep.Scope, rep.Cutoff.Format(time.RFC3339))
	for _, c := range rep.Candidates {
		out.Printf("  %-28s %10s   %s\n", c.Harp, humanBytes(c.Bytes), c.Verdict)
		if c.Reason != "" {
			out.Printf("  %-28s              %s\n", "", c.Reason)
		}
	}
	if rep.Newer > 0 {
		out.Printf("\n  %d sessions were active since the bound and were not considered.\n", rep.Newer)
	}
	if !cleanIncludePersist {
		out.Printf("\n  persist/ is referenced data and is left alone; --include-persist reclaims it from the same sessions.\n")
	}
	out.Printf("\n")
	return out.Err()
}

// parseAgeBound turns a stated age into the instant that bounds it: an offset
// back from now ("30d", "12w", "720h" — Go's own duration units plus d and w,
// which it lacks and which are the units a person actually reaches for), or a
// calendar date ("2026-01-01"). source names where the text came from
// (the flag, or the config key) so a refusal points at the thing to fix.
//
// It NEVER returns a zero time with a nil error: a bound that parsed to zero
// would reach operations.ReclaimAgedSessions as "no bound stated" and be
// refused there, but arriving at that refusal through a silently-misparsed
// value is a worse story than failing here with the text the caller typed.
func parseAgeBound(source, raw string, now time.Time) (time.Time, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}, fmt.Errorf("%s needs an age: an offset like 30d, or a date like 2026-01-01", source)
	}
	if d, err := time.ParseDuration(expandDurationUnits(s)); err == nil {
		if d <= 0 {
			return time.Time{}, fmt.Errorf("%s %q is not a positive age", source, raw)
		}
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("%s %q is neither an offset (30d, 12w, 720h) nor a date (2026-01-01)", source, raw)
}

// expandDurationUnits rewrites the day and week suffixes time.ParseDuration
// does not know into hours. Only a bare <number><unit> is rewritten; anything
// else is handed through untouched to fail in ParseDuration with its own
// message.
func expandDurationUnits(s string) string {
	for suffix, hours := range map[string]int{"d": 24, "w": 168} {
		if !strings.HasSuffix(s, suffix) {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSuffix(s, suffix))
		if err != nil {
			continue
		}
		return strconv.Itoa(n*hours) + "h"
	}
	return s
}

func init() {
	cleanCmd.Flags().BoolVar(&cleanYes, "yes", false, "apply exactly the plan this reports")
	cleanCmd.Flags().StringVar(&cleanOlderThan, "older-than", "",
		"reclaim the disposable store of sessions last active before this age (30d, 12w, 720h) or date (2026-01-01), overriding the configured session_reap_age for this invocation")
	cleanCmd.Flags().BoolVar(&cleanIncludePersist, "include-persist", false,
		"also reclaim persist/ (transcripts, plans, artifacts) from the aged sessions; referenced data, so never taken without this")
	rootCmd.AddCommand(cleanCmd)
}

// humanBytes renders a size for a human reading a removal plan. Written here
// rather than taken as a dependency: it is eight lines, and the alternative
// puts a module in go.mod for one call site.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
