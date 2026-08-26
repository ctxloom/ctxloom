package tasks

import (
	"fmt"
	"strings"
)

// StatusClassSigil is the RESERVED prefix that marks a status CLASS rather
// than a status name. It is reserved in both directions: a filter value
// starting with it is expanded as a class and never matched literally, and a
// status NAME starting with it is refused on write (ValidateStatusName), so a
// user-defined status can never collide with a class if user-defined statuses
// ever land.
const StatusClassSigil = "@"

// The status classes a filter may name in place of literal statuses. They are
// sugar over the already-repeatable status filter: a class MIXES freely with
// literal values in one filter, and expands to plain status names before any
// store ever sees it.
const (
	// StatusClassOpen is every NON-terminal status — the work that is still
	// live, whatever the taxonomy currently calls it. Spelling that set by
	// hand ("To Do", "In Progress", "Deferred") is three literals a caller
	// must keep in sync with the taxonomy by hand, which is why nobody
	// relied on it and sessions hand-folded the raw append-only log instead.
	StatusClassOpen = StatusClassSigil + "open"
	// StatusClassTerminal is every terminal (completed) status.
	StatusClassTerminal = StatusClassSigil + "terminal"
)

// statusClass is one @-class: the name a caller spells, and the predicate
// that decides which statuses it stands for.
//
// The predicate reads a StatusInfo's BITS, never a status NAME. That is the
// whole point: the class is computed against whatever Statuses() publishes at
// the moment it is expanded, so a status added to the taxonomy is picked up
// by the right class with no edit here. A slice of names would be a second
// copy of the taxonomy to hand-sync — exactly the failure this feature exists
// to remove from its callers.
type statusClass struct {
	name    string
	selects func(StatusInfo) bool
}

var statusClasses = []statusClass{
	{name: StatusClassOpen, selects: func(s StatusInfo) bool { return !s.Terminal }},
	{name: StatusClassTerminal, selects: func(s StatusInfo) bool { return s.Terminal }},
}

// StatusClasses returns the spellings a status filter accepts in place of a
// literal status, in declaration order — so a frontend can list them (help
// text, a picker, an error message) without restating them.
func StatusClasses() []string {
	out := make([]string, len(statusClasses))
	for i, c := range statusClasses {
		out[i] = c.name
	}
	return out
}

// ExpandStatusClasses rewrites a status filter's values, replacing every
// @-class with the statuses it currently stands for and passing every other
// value through untouched. Order is preserved (a class expands in taxonomy
// order at the position it was given) and repeats are collapsed, so mixing a
// class with a literal it already covers filters on that status once.
//
// nil/empty is returned unchanged: an empty status filter means "no status
// filter", and manufacturing a slice for it would flip a caller's default
// active-only view into an explicit one.
//
// Two things fail LOUD rather than quietly narrowing a listing to nothing:
// an unrecognized @-value (a typo like @done must not be mistaken for a
// literal status nobody has), and a class that currently selects NO status
// at all (which would expand to an empty filter and read as "no filter",
// i.e. silently the opposite of what was asked).
func ExpandStatusClasses(values []string) ([]string, error) {
	if len(values) == 0 {
		return values, nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	add := func(status string) {
		if _, dup := seen[status]; dup {
			return
		}
		seen[status] = struct{}{}
		out = append(out, status)
	}
	for _, v := range values {
		if !strings.HasPrefix(v, StatusClassSigil) {
			add(v)
			continue
		}
		class, ok := lookupStatusClass(v)
		if !ok {
			return nil, fmt.Errorf("unknown status class %q (known classes: %s; %q is reserved as a status-class prefix, so it is never matched as a literal status name)",
				v, strings.Join(StatusClasses(), ", "), StatusClassSigil)
		}
		matched := 0
		for _, s := range Statuses() {
			if !class.selects(s) {
				continue
			}
			matched++
			add(s.Name)
		}
		if matched == 0 {
			return nil, fmt.Errorf("status class %q currently matches no status in the taxonomy (see `taskloom statuses`) — refusing to filter on nothing", v)
		}
	}
	return out, nil
}

func lookupStatusClass(name string) (statusClass, bool) {
	for _, c := range statusClasses {
		if c.name == name {
			return c, true
		}
	}
	return statusClass{}, false
}

// ValidateStatusName refuses a status name that would collide with the
// reserved class sigil. Every write path runs it, so a status beginning with
// "@" can never reach the log and later be indistinguishable from a class in
// a filter. Checked at the write seam rather than at the filter, because by
// filter time the damage — an unfilterable status sitting in the store — is
// already done.
func ValidateStatusName(status string) error {
	if strings.HasPrefix(status, StatusClassSigil) {
		return fmt.Errorf("status name %q is not allowed: %q is reserved as the status-class prefix (%s)",
			status, StatusClassSigil, strings.Join(StatusClasses(), ", "))
	}
	return nil
}
