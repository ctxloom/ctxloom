// Package agebound turns an age a person states on a command line or in a
// config file into the instant that bounds it. It is shared by every command
// that takes an --older-than style bound — ctxloom's clean and session sweep,
// taskloom's prune — so the one grammar a user learns is accepted everywhere.
package agebound

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Parse turns a stated age into the instant that bounds it: an offset back
// from now ("30d", "12w", "720h" — Go's own duration units plus d and w,
// which it lacks and which are the units a person actually reaches for), or a
// calendar date ("2026-01-01"). source names where the text came from (the
// flag, or the config key) so a refusal points at the thing to fix.
//
// It NEVER returns a zero time with a nil error: a bound that parsed to zero
// would reach a caller as "no bound stated" and be refused there, but
// arriving at that refusal through a silently-misparsed value is a worse
// story than failing here with the text the caller typed.
func Parse(source, raw string, now time.Time) (time.Time, error) {
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
