package cli

import (
	"errors"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/agebound"
	"github.com/ctxloom/ctxloom/internal/shared/errwriter"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// The session half of `ctxloom clean`: the age-bounded reap of every
// session's disposable members. WHAT is taken is the reaper's policy
// (sessions.ReapPolicy — the table's Ephemeral rows, and its top-level
// machine rows besides under --include-persist); this file only resolves the bound the human
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
	if err != nil {
		return sessions.ReapPolicy{}, err
	}
	p.Cutoff = cutoff
	if p.Scope == paths.Persist {
		p.Retention, err = resolveTranscriptRetention(now)
	}
	return p, err
}

// resolveTranscriptRetention is transcriptRetention, as every destroyer of
// transcripts calls it: a var so a test about something else (the lock, the
// populations) can release retention rather than age every fixture.
var resolveTranscriptRetention = transcriptRetention

// transcriptRetention resolves the rule every destroyer of transcripts asks
// (sessions.Retention): a session's transcript is kept until the session
// ended more than session_transcript_retention ago, else
// config.DefaultSessionTranscriptRetention. A value that does not parse is
// REFUSED, and a refused config refuses; a config that cannot be loaded at
// all proceeds on the built-in default and says so — the default keeps
// transcripts, so it is never the unsafe direction.
func transcriptRetention(now time.Time) (sessions.Retention, error) {
	age := config.DefaultSessionTranscriptRetention
	cfg, err := GetConfig()
	switch {
	case errors.As(err, new(configRefusedError)):
		return sessions.Retention{}, err
	case err != nil:
		clidiag.Warn("ctxloom", "config could not be loaded (%v); transcripts are retained for the built-in session_transcript_retention %s", err, age)
	default:
		age = cfg.SessionTranscriptRetention()
	}
	cutoff, err := agebound.Parse("session_transcript_retention", age, now)
	if err != nil {
		return sessions.Retention{}, err
	}
	return sessions.Retention{Cutoff: cutoff}, nil
}

// reclaimCutoff resolves the reclaim bound: --older-than when given, else
// the configured session_reap_age, else config.DefaultSessionReapAge (see
// sessionReapPolicy). Shared by `clean` and `session sweep`, whose reclaim
// is one rule.
func reclaimCutoff(olderThan string, now time.Time) (time.Time, error) {
	if olderThan != "" {
		return agebound.Parse("--older-than", olderThan, now)
	}
	age := config.DefaultSessionReapAge
	cfg, err := GetConfig()
	switch {
	case errors.As(err, new(configRefusedError)):
		// A config that loaded and was refused is not an unreadable one: the
		// reap must not proceed on the default age over a setting the user
		// did write.
		return time.Time{}, err
	case err != nil:
		clidiag.Warn("ctxloom", "config could not be loaded (%v); the session reap proceeds on the built-in session_reap_age %s", err, age)
	default:
		age = cfg.SessionReapAge()
	}
	return agebound.Parse("session_reap_age", age, now)
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
		out.Printf("\n  transcripts, native history and the other persistent members are left alone; --include-persist reclaims them from the same (compacted) sessions.\n")
	}
	out.Printf("\n")
	return out.Err()
}

// memberList renders the members a reap takes for a human, e.g. "home/,
// scratch/" — the rels the report carries, each as a directory.
func memberList(rels []string) string {
	dirs := make([]string, len(rels))
	for i, rel := range rels {
		dirs[i] = rel + "/"
	}
	return strings.Join(dirs, ", ")
}

func init() {
	cleanCmd.Flags().StringVar(&cleanOlderThan, "older-than", "",
		"reap the disposable members of sessions last active before this age (30d, 12w, 720h) or date (2026-01-01), overriding the configured session_reap_age for this invocation")
	cleanCmd.Flags().BoolVar(&cleanIncludePersist, "include-persist", false,
		"also reap the persistent machine members — transcripts, native history, spool, package store, logs — from the aged sessions whose transcripts session_transcript_retention releases (ended long enough ago, not marked keep); never taken without this")
}
