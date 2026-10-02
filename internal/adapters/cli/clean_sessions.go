package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/errwriter"
)

// The session half of `ctxloom clean`: the age-bounded reap of every
// session's disposable members. WHAT is taken is the reaper's policy
// (sessions.ReapPolicy — the table's Ephemeral rows, and persist/ besides
// under --include-persist); this file only resolves the bound the human
// stated and renders the report.

var (
	cleanOlderThan      string
	cleanIncludePersist bool
)

// sessionReapPolicy resolves the reap's policy from the flags: the bound is
// --older-than when given (one invocation), else the configured
// session_reap_age, else config.DefaultSessionReapAge; the scope is
// paths.Persist only under --include-persist.
//
// A value that does not parse is REFUSED, naming where it came from, never
// resolved to the default: a typo in the home config would otherwise reap on
// an age nobody chose. The one tolerated fault is a config that cannot be
// LOADED at all — clean exists to work on a broken project (see
// TestClean_ReachesTheCacheThroughAnUnreadableConfig) — and there the reap
// proceeds on the built-in default and says so, formatting the value it
// proceeds with from the constant it actually used.
func sessionReapPolicy(now time.Time) (sessions.ReapPolicy, error) {
	p := sessions.ReapPolicy{Apply: cleanYes}
	if cleanIncludePersist {
		p.Scope = paths.Persist
	}
	cutoff, err := reclaimCutoff(cleanOlderThan, now)
	p.Cutoff = cutoff
	return p, err
}

// reclaimCutoff resolves the reclaim bound: --older-than when given, else
// the configured session_reap_age, else config.DefaultSessionReapAge (see
// sessionReapPolicy). Shared by `clean` and `session sweep`, whose reclaim
// is one rule.
func reclaimCutoff(olderThan string, now time.Time) (time.Time, error) {
	if olderThan != "" {
		return parseAgeBound("--older-than", olderThan, now)
	}
	age := config.DefaultSessionReapAge
	if cfg, err := GetConfig(); err != nil {
		clidiag.Warn("ctxloom", "config could not be loaded (%v); the session reap proceeds on the built-in session_reap_age %s", err, age)
	} else {
		age = cfg.SessionReapAge()
	}
	return parseAgeBound("session_reap_age", age, now)
}

// renderSessionReclaim prints the aged-session plan.
//
// Every candidate is listed, including the skipped ones, because "why did it
// free nothing" is the question a caller actually has — and a session left
// alone for holding someone's uncommitted work is precisely the thing that
// must not be silent. Sessions newer than the bound are the one exception:
// they are counted on a single line, because the reap runs on every clean
// and a line per session in use would bury the rest.
func renderSessionReclaim(out *errwriter.Writer, rep sessions.Report) error {
	members := memberList(rep.Members)
	if len(rep.Candidates) == 0 {
		out.Printf("No session %s is older than %s", members, rep.Cutoff.Format(time.RFC3339))
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
		verb, humanBytes(rep.Bytes), members, rep.Cutoff.Format(time.RFC3339))
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
		out.Printf("\n  %s/ is referenced data and is left alone; --include-persist reclaims it from the same sessions.\n", paths.PersistDirName)
	}
	out.Printf("\n")
	return out.Err()
}

// memberList renders the members a reap takes for a human, e.g. "home/,
// ephemeral/" — the rels the report carries, each as a directory.
func memberList(rels []string) string {
	dirs := make([]string, len(rels))
	for i, rel := range rels {
		dirs[i] = rel + "/"
	}
	return strings.Join(dirs, ", ")
}

// parseAgeBound turns a stated age into the instant that bounds it: an offset
// back from now ("30d", "12w", "720h" — Go's own duration units plus d and w,
// which it lacks and which are the units a person actually reaches for), or a
// calendar date ("2026-01-01"). source names where the text came from
// (the flag, or the config key) so a refusal points at the thing to fix.
//
// It NEVER returns a zero time with a nil error: a bound that parsed to zero
// would reach sessions.Reap as "no bound stated" and be refused there, but
// arriving at that refusal through a silently-misparsed value is a worse
// story than failing here with the text the caller typed.
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
	cleanCmd.Flags().StringVar(&cleanOlderThan, "older-than", "",
		"reap the disposable members of sessions last active before this age (30d, 12w, 720h) or date (2026-01-01), overriding the configured session_reap_age for this invocation")
	cleanCmd.Flags().BoolVar(&cleanIncludePersist, "include-persist", false,
		"also reap persist/ — transcripts, plans, artifacts — from the aged sessions; referenced data, so never taken without this")
}
