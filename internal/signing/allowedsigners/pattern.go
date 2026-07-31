package allowedsigners

import (
	"fmt"
	"strings"
	"time"
)

// matchPatternList implements the pattern-list matching rules of
// ssh_config(5) PATTERNS, which ssh-keygen(1) ALLOWED SIGNERS explicitly
// defers to for both the principals field and the namespaces= option
// value: a comma-separated list of glob patterns, any of which may be
// negated with a leading '!'. A negated pattern that matches disqualifies
// the whole list immediately, regardless of any positive match elsewhere
// in the list (verified against real ssh-keygen principal-glob behavior;
// negation semantics are per the documented OpenSSH grammar). An empty or
// nil pattern list never matches anything.
func matchPatternList(patterns []string, s string) bool {
	matched := false
	for _, p := range patterns {
		neg := false
		if strings.HasPrefix(p, "!") {
			neg = true
			p = p[1:]
		}
		// U136-F18: this used to go through a one-line globMatch(string,string)
		// wrapper whose only work was these same two []byte conversions, with
		// exactly this one caller. Inlined.
		if globMatchBytes([]byte(p), []byte(s)) {
			if neg {
				return false
			}
			matched = true
		}
	}
	return matched
}

// globMatchBytes implements the restricted glob dialect used by SSH pattern
// matching: '*' matches zero or more characters, '?' matches exactly one
// character, every other character matches itself literally. There are no
// character classes ([...]) in this dialect.
//
// The algorithm is the single-backtrack-point one: only the MOST RECENT '*' is
// ever reconsidered, and it only ever gives up one more byte, so the whole
// match is O(len(pattern) x len(s)) in the worst case and linear in practice.
//
// It replaced a recursion that tried every split point for every '*'
// independently, which is exponential in the number of stars. The old
// consecutive-star collapse only merged ADJACENT stars, so it did nothing for
// *a*a*a*a*b — measured at over 130 seconds for eleven stars against
// forty-four characters. That matters here rather than being a curiosity:
// TrustedAs matches the trust root's principals pattern against an EXTERNALLY
// claimed identity (a git committer email, a loadout envelope's advisory
// signer field), so both operands are shaped by someone other than the person
// asking the question.
func globMatchBytes(pattern, s []byte) bool {
	p, i := 0, 0
	// starP is the pattern index of the most recent '*'; starI is the subject
	// index it currently resumes from. Reconsidering only this one star is
	// what makes the search linear: an earlier star never needs revisiting,
	// because anything it could have absorbed, a later star can absorb too.
	starP, starI := -1, 0
	for i < len(s) {
		if p < len(pattern) && pattern[p] == '*' {
			starP, starI = p, i
			p++
			continue
		}
		if p < len(pattern) && (pattern[p] == '?' || pattern[p] == s[i]) {
			p++
			i++
			continue
		}
		if starP < 0 {
			return false // no star to give ground; this subject cannot match
		}
		starI++
		p, i = starP+1, starI
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// splitPatternList splits an unquoted option value (e.g. the content of
// namespaces="a,b") into its comma-separated patterns. An empty value
// yields an empty, non-nil slice — deliberately distinct from a nil slice,
// which callers use to mean "the option was absent entirely" (see
// Entry.Namespaces doc). Verified against real ssh-keygen: namespaces=""
// parses without error and then matches no namespace.
func splitPatternList(v string) []string {
	if v == "" {
		return []string{}
	}
	return strings.Split(v, ",")
}

// parseTimestamp parses a valid-after=/valid-before= timestamp in the
// formats ssh-keygen(1) documents: YYYYMMDD[Z] or YYYYMMDDHHMM[SS][Z].
// Dates/times are interpreted in the local system time zone unless
// suffixed with 'Z', which forces UTC — matching ssh-keygen's documented
// behavior exactly (verified: -Overify-time with and without a trailing Z
// against a real allowed_signers file with quoted valid-after/valid-before
// values).
func parseTimestamp(s string) (time.Time, error) {
	utc := strings.HasSuffix(s, "Z")
	body := s
	loc := time.Local
	if utc {
		body = s[:len(s)-1]
		loc = time.UTC
	}
	var layout string
	switch len(body) {
	case 8:
		layout = "20060102"
	case 12:
		layout = "200601021504"
	case 14:
		layout = "20060102150405"
	default:
		return time.Time{}, fmt.Errorf("timestamp %q: expected YYYYMMDD[Z] or YYYYMMDDHHMM[SS][Z]", s)
	}
	t, err := time.ParseInLocation(layout, body, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("timestamp %q: %w", s, err)
	}
	return t, nil
}
