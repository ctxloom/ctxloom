package vendorreader

import (
	"fmt"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// TWO FAILURE LEVELS LIVE IN THIS PACKAGE AND MUST NOT BE COLLAPSED INTO ONE.
// They look similar — both are "this transcript did not fully read" — and they
// call for opposite behaviour.
//
//  1. AN UNKNOWN VERSION REFUSES OUTRIGHT. That is this file. No adapter, no
//     read, nothing written. A version matching no adapter refuses rather than
//     picking the nearest; a session carrying no recorded version refuses
//     rather than assuming the newest; a version command that failed or could
//     not be parsed refuses naming what could not be determined. There is
//     never a fallback to a default adapter, because a default adapter is a
//     guess wearing a version number — and what a guessed parser produces is a
//     transcript that LOOKS fine, is not, and goes straight into a model's
//     context.
//
//  2. A MALFORMED LINE WITHIN A KNOWN VERSION DEGRADES TO PARTIAL. That is
//     VendorAdapter.Convert's own contract (see adapter.go): a bad line is
//     skipped, never fatal, because an adapter that aborted on the first bad
//     byte would turn one corrupt line into an entirely lost session. Convert
//     errors only on structural failure — src unreadable, ctx cancelled, or
//     rec.Record itself failing, since a live disk-write failure means the
//     sink is no longer trustworthy and is not safe to skip.
//
// Collapse them one way and one bad line loses a whole session; collapse them
// the other and ctxloom half-reads a format it was never validated against and
// says nothing. Both directions are wrong, and the difference is which side of
// the ADAPTER SELECTION the failure happens on: before it, refuse; after it,
// degrade.

// VersionRange is the span of engine CLI versions ONE whole-file adapter was
// validated against — min inclusive, max exclusive, the ordinary convention
// for "everything in this release line".
//
// A range rather than an exact version because vendors ship often and an
// adapter per patch release would be unmaintainable — but a declared range is
// a CLAIM about versions actually validated, and the thing that makes it
// evidence rather than hope is .github/engine-versions.env, the tested-version
// lock CI keeps honest. Every range ctxloom carries must contain that engine's
// pin (see TestVendorReaderRanges_ContainThePinnedTestedVersion), so a range
// cannot quietly widen past what anyone has run.
//
// MaxExclusive empty means unbounded above. Use it sparingly: an unbounded
// range asserts compatibility with versions that do not exist yet, which is
// precisely the guess this mechanism exists to prevent.
type VersionRange struct {
	MinInclusive string
	MaxExclusive string
}

func (r VersionRange) String() string {
	if r.MaxExclusive == "" {
		return ">= " + r.MinInclusive
	}
	return r.MinInclusive + " – " + r.MaxExclusive + " (exclusive)"
}

// Contains reports whether version falls in the range. An unparseable version
// is an error, never a false: "I could not tell" and "no" lead to the same
// refusal here but not the same MESSAGE, and a user debugging a refusal needs
// to know which one happened.
func (r VersionRange) Contains(version string) (bool, error) {
	v, err := semver.NewVersion(strings.TrimSpace(version))
	if err != nil {
		return false, fmt.Errorf("%q is not a usable version: %w", version, err)
	}
	if r.MinInclusive != "" {
		min, err := semver.NewVersion(r.MinInclusive)
		if err != nil {
			return false, fmt.Errorf("adapter range lower bound %q is not a version: %w", r.MinInclusive, err)
		}
		if v.LessThan(min) {
			return false, nil
		}
	}
	if r.MaxExclusive != "" {
		max, err := semver.NewVersion(r.MaxExclusive)
		if err != nil {
			return false, fmt.Errorf("adapter range upper bound %q is not a version: %w", r.MaxExclusive, err)
		}
		if !v.LessThan(max) {
			return false, nil
		}
	}
	return true, nil
}

// VersionedAdapter pairs one WHOLE-FILE adapter with the engine version range
// it handles.
//
// Whole-file is deliberate for 0.7 (task wrought-spearman): several complete
// adapters per engine, selected by version, rather than adapters decomposed
// into shared subcomponents — that is task asleep-hatchback, deferred to
// 0.8.0. You cannot cut the roles well until you have watched several versions
// vary, and each per-version adapter shipped here is a datapoint telling
// 0.8.0 where the format actually moves.
type VersionedAdapter struct {
	// Adapter is the parser itself. Its Convert contract is unchanged.
	Adapter VendorAdapter
	// Versions is the engine version span this adapter handles.
	Versions VersionRange
	// ValidatedVersion is the exact version inside Versions that ctxloom has
	// actually been run against — the value in .github/engine-versions.env.
	// It is what turns Versions from an assertion into a citation, and what a
	// refusal message can point at when a user asks why their version is not
	// covered.
	ValidatedVersion string
}

// NoRecordedVersionError reports that the session carries no engine version at
// all, so no adapter can be chosen for it.
//
// This is the ordinary shape of a session that PREDATES version recording, and
// of one whose engine could not be probed at start. It refuses rather than
// falling back to the newest adapter: the newest adapter is the one most
// likely to be wrong about an old session, and being wrong here produces a
// readable-looking transcript nobody can tell is fabricated.
type NoRecordedVersionError struct {
	Engine string
	Harp   string
}

func (e *NoRecordedVersionError) Error() string {
	where := "this session"
	if e.Harp != "" {
		where = e.Harp
	}
	return fmt.Sprintf("%s records no %s version, so ctxloom cannot tell which transcript format wrote it "+
		"(sessions started before ctxloom recorded engine versions, or whose engine could not be asked, carry none) — "+
		"refusing to read rather than guessing at a format", where, e.Engine)
}

// UnsupportedVersionError reports that the session's engine version is known
// but matches no adapter ctxloom carries — or is not a version at all.
//
// Known lists the ranges that WERE available, because a user meeting this
// refusal needs to see the gap: their version against what ctxloom validated
// is the whole diagnosis, and it is also the bug report that gets an adapter
// written.
type UnsupportedVersionError struct {
	Engine  string
	Version string
	Known   []VersionRange
	Err     error
}

func (e *UnsupportedVersionError) Error() string {
	known := "none at all"
	if len(e.Known) > 0 {
		parts := make([]string, 0, len(e.Known))
		for _, r := range e.Known {
			parts = append(parts, r.String())
		}
		known = strings.Join(parts, ", ")
	}
	msg := fmt.Sprintf("no %s transcript reader is validated for version %q (ctxloom carries: %s) — "+
		"refusing to read rather than parsing a format it has never been checked against",
		e.Engine, e.Version, known)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *UnsupportedVersionError) Unwrap() error { return e.Err }

// SelectAdapter picks the adapter validated for the engine version this
// session was RECORDED as running (sessions.Entry.EngineVersion), and returns
// a refusal — never a fallback — when there isn't one.
//
// harp is used only to name the session in a refusal; it may be empty.
//
// The first matching candidate wins. Overlapping ranges are therefore resolved
// by declaration order rather than by "closest" or "newest", which is the
// honest behaviour: two adapters both claiming a version is a mistake in the
// declarations, and silently preferring one by some derived score would hide
// it. Keep the ranges disjoint.
func SelectAdapter(engine, recordedVersion, harp string, candidates []VersionedAdapter) (VendorAdapter, error) {
	if strings.TrimSpace(recordedVersion) == "" {
		return nil, &NoRecordedVersionError{Engine: engine, Harp: harp}
	}

	known := make([]VersionRange, 0, len(candidates))
	for _, c := range candidates {
		known = append(known, c.Versions)
	}

	for _, c := range candidates {
		ok, err := c.Versions.Contains(recordedVersion)
		if err != nil {
			// The recorded value is not a version at all. Every candidate
			// would fail identically, so report it once, here.
			return nil, &UnsupportedVersionError{Engine: engine, Version: recordedVersion, Known: known, Err: err}
		}
		if ok {
			return c.Adapter, nil
		}
	}
	return nil, &UnsupportedVersionError{Engine: engine, Version: recordedVersion, Known: known}
}
