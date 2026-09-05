//go:build arch

package buildpins

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Architectural gate for the "every justfile stamps a build the same way"
// contract.
//
// Three files compute the version stamp, and they must agree on HOW: the
// versionator template, the sed that inserts the 'T', the git-describe source
// of the -dirty marker, and the else branch that refuses to invent a
// placeholder. They are separate copies because just's `import` cannot share a
// variable across the two roots, so nothing but this gate holds them together —
// and a copy that silently drifts produces binaries that disagree about what
// they are called, which is precisely the signal this project verifies by.
//
// This is a class gate in this repo's sense — it fails when the copies stop
// agreeing, not when one particular past bug regresses — so it carries
// `//go:build arch` and the TestArch_ naming that `just test-arch` selects on.

// versionStampSite is one file computing the stamp, and whether it reads the
// stamp from the environment before computing one.
//
// wrapped is the deliberate asymmetry, NOT drift. The root justfile is the
// PRODUCER: it computes the stamp on the host and hands it to the devcontainer
// (`_run` passes -e CTXLOOM_VERSION_STAMP), so it has no env var to read. The
// other two are CONSUMERS and take the handed-in value when there is one,
// because a stamp recomputed inside the container is always "-dirty" — the
// container bind-mounts justfile.container over ./justfile, so its own git sees
// a modified tracked file. Collapsing the two shapes into one would reintroduce
// that bug.
type versionStampSite struct {
	path    string
	wrapped bool
}

// Listed rather than discovered: a discovery bug would silently shrink the set
// and leave this gate comparing one file with itself, green and worthless.
var versionStampSites = []versionStampSite{
	{path: "../../justfile", wrapped: false},
	{path: "../../justfile.container", wrapped: true},
	{path: "../../build/common.justfile", wrapped: true},
}

// versionAssignmentRE finds the stamp assignment at column 0. Only the
// right-hand side is captured; the wrapper and the backtick body are separated
// afterwards, because they are checked against different rules.
var versionAssignmentRE = regexp.MustCompile(`(?m)^version := (.*)$`)

const envWrapperPrefix = `env_var_or_default("CTXLOOM_VERSION_STAMP", `

// readVersionStampSite returns the site's backtick BODY and the wrapper text
// surrounding it.
func readVersionStampSite(t *testing.T, site versionStampSite) (body, prefix, suffix string) {
	t.Helper()

	raw, err := os.ReadFile(site.path)
	if err != nil {
		t.Fatalf("read %s: %v", site.path, err)
	}
	matches := versionAssignmentRE.FindAllStringSubmatch(string(raw), -1)
	if len(matches) != 1 {
		t.Fatalf("%s: found %d `version :=` assignments, want exactly 1 — either the stamp rule moved or versionAssignmentRE stopped matching, and this gate cannot compare what it cannot find", site.path, len(matches))
	}
	rhs := matches[0][1]

	first := strings.Index(rhs, "`")
	last := strings.LastIndex(rhs, "`")
	if first < 0 || last <= first {
		t.Fatalf("%s: the `version :=` right-hand side has no backtick body:\n\t%s", site.path, rhs)
	}
	return rhs[first+1 : last], rhs[:first], rhs[last+1:]
}

// TestArch_VersionStampRule_AgreesAcrossJustfiles fails when the three copies
// of the stamp computation stop computing the same thing.
//
// It compares the BACKTICK BODY ONLY, and that restriction is the whole point.
// The wrappers around those bodies differ ON PURPOSE (see versionStampSite),
// so a gate asserting the three LINES are identical would be red on the day it
// landed, and the obvious way to "fix" it — deleting the env_var_or_default on
// the two consumers — would destroy the host-computes-the-stamp design and
// stamp every containerised build "-dirty". Compare the bodies; leave the
// wrappers to TestArch_VersionStampRule_KeepsProducerConsumerAsymmetry.
func TestArch_VersionStampRule_AgreesAcrossJustfiles(t *testing.T) {
	if len(versionStampSites) < 2 {
		t.Fatalf("versionStampSites has %d entries — a comparison needs at least two, so this gate would pass vacuously", len(versionStampSites))
	}

	want, _, _ := readVersionStampSite(t, versionStampSites[0])

	// Anti-vacuous. If the extraction ever returns something that is not the
	// stamp rule, every site would return the same wrong thing and agree
	// perfectly. Pin the load-bearing pieces of the body so an empty or
	// truncated capture cannot be mistaken for agreement.
	for _, needle := range []string{"versionator output version", "BuildDateTimeCompact", "git describe --always --dirty"} {
		if !strings.Contains(want, needle) {
			t.Fatalf("%s: extracted stamp body does not contain %q, so the extraction is broken and every comparison below is meaningless. Got:\n\t%s", versionStampSites[0].path, needle, want)
		}
	}

	for _, site := range versionStampSites[1:] {
		got, _, _ := readVersionStampSite(t, site)
		if got == want {
			continue
		}
		t.Errorf("the version stamp rule in %s does not match %s.\n\n"+
			"NOTE: only the BACKTICK BODY is compared here. The wrappers around it are\n"+
			"EXPECTED to differ — the root justfile computes the stamp and hands it to the\n"+
			"container, while the other two read CTXLOOM_VERSION_STAMP first. Do not\n"+
			"\"fix\" this by making the wrappers match; make the bodies match.\n\n"+
			"%s\n\n  %s:\n    %s\n\n  %s:\n    %s",
			site.path, versionStampSites[0].path,
			describeFirstDifference(want, got),
			versionStampSites[0].path, want,
			site.path, got)
	}
}

// TestArch_VersionStampRule_KeepsProducerConsumerAsymmetry fails when a site
// gains or loses its CTXLOOM_VERSION_STAMP wrapper.
//
// The asymmetry is a design decision with a measured reason behind it, and
// nothing else enforces it. Wrapping the root justfile would let an ambient env
// var override the one stamp that is supposed to be authoritative; unwrapping
// either consumer would make the container recompute a stamp its own bind-mount
// guarantees is "-dirty".
func TestArch_VersionStampRule_KeepsProducerConsumerAsymmetry(t *testing.T) {
	producers, consumers := 0, 0
	for _, site := range versionStampSites {
		_, prefix, suffix := readVersionStampSite(t, site)

		if site.wrapped {
			consumers++
			if prefix != envWrapperPrefix || suffix != ")" {
				t.Errorf("%s is a stamp CONSUMER and must take CTXLOOM_VERSION_STAMP when it is set, but its assignment is not wrapped in %s...).\n"+
					"Without the wrapper it recomputes the stamp inside the container, where the justfile.container bind-mount makes git report a modified tracked file and every stamp comes out \"-dirty\".\n"+
					"  got prefix: %q\n  got suffix: %q",
					site.path, envWrapperPrefix, prefix, suffix)
			}
			continue
		}

		producers++
		if prefix != "" || suffix != "" {
			t.Errorf("%s is the stamp PRODUCER: it computes the stamp on the host and hands it to the container, so its assignment must be a bare backtick with no env_var_or_default wrapper.\n"+
				"Wrapping it would let an ambient CTXLOOM_VERSION_STAMP override the one value that is supposed to be authoritative.\n"+
				"  got prefix: %q\n  got suffix: %q",
				site.path, prefix, suffix)
		}
	}

	// Both roles must actually be represented, or the asymmetry this gate
	// exists to protect is not present to be checked.
	if producers == 0 || consumers == 0 {
		t.Fatalf("expected both stamp roles to be present, got %d producer(s) and %d consumer(s) — versionStampSites no longer describes the design this gate protects", producers, consumers)
	}
}

// TestArch_VersionStampRule_InventsNoPlaceholderStamp fails when a stamp rule
// falls back to a made-up version instead of reporting that it has none.
//
// `else echo dev` was the original fallback. It is not a smaller version — it
// is a value version.ValidStamp refuses, so the build died later, somewhere
// else, with an error that never mentioned versionator. The else branch must
// say what is wrong and what to run, at the moment it happens.
func TestArch_VersionStampRule_InventsNoPlaceholderStamp(t *testing.T) {
	for _, site := range versionStampSites {
		body, _, _ := readVersionStampSite(t, site)

		if regexp.MustCompile(`else\s+echo\s+dev\s*;`).MatchString(body) {
			t.Errorf("%s falls back to the placeholder stamp `dev`. version.ValidStamp refuses it, so the build fails later and elsewhere with an error that does not name the cause. Report the problem here instead: say versionator is missing and what to run.", site.path)
		}

		// The else branch must direct a human at both remedies. Checked by
		// symbol/identifier rather than by prose so a reworded message stays
		// green while a message that stops naming the fix goes red.
		if !strings.Contains(body, ">&2") {
			t.Errorf("%s: the stamp rule writes nothing to stderr, so a build with no versionator gives the user no diagnosis at the point of failure.", site.path)
		}
		for _, remedy := range []string{"versionator", "CTXLOOM_VERSION_STAMP"} {
			if !strings.Contains(body, remedy) {
				t.Errorf("%s: the stamp rule's diagnostic never mentions %q, so it states a complaint without a remedy.", site.path, remedy)
			}
		}
	}
}

// describeFirstDifference locates where two stamp bodies diverge. A raw pair of
// 300-character shell one-liners is unreadable; the offset and the surrounding
// text are what make the failure actionable.
func describeFirstDifference(want, got string) string {
	n := len(want)
	if len(got) < n {
		n = len(got)
	}
	for i := 0; i < n; i++ {
		if want[i] != got[i] {
			lo := i - 30
			if lo < 0 {
				lo = 0
			}
			hiWant, hiGot := i+30, i+30
			if hiWant > len(want) {
				hiWant = len(want)
			}
			if hiGot > len(got) {
				hiGot = len(got)
			}
			return "first difference at byte " + strconv.Itoa(i) + ":\n    want ..." + want[lo:hiWant] + "...\n    got  ..." + got[lo:hiGot] + "..."
		}
	}
	if len(want) != len(got) {
		return "bodies share a common prefix but differ in length (" + strconv.Itoa(len(want)) + " vs " + strconv.Itoa(len(got)) + " bytes)"
	}
	return ""
}
