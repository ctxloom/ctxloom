package dockergate

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// Decision is what a gate decided: run the test, skip it, or fail it. It is
// the policy separated from its application, so a caller with no *testing.T —
// a godog step, a table-driven matrix that wants to count what it covered —
// gets the SAME rule as `go test` rather than a second copy of it.
type Decision int

const (
	// Proceed: the gate's precondition holds; run the test.
	Proceed Decision = iota
	// Skip: the precondition does not hold and nothing declared that it must.
	Skip
	// Fail: the precondition does not hold and a runner DECLARED that it does.
	Fail
)

func (d Decision) String() string {
	switch d {
	case Proceed:
		return "proceed"
	case Skip:
		return "skip"
	case Fail:
		return "fail"
	default:
		return fmt.Sprintf("Decision(%d)", int(d))
	}
}

// EnvRequireRuntimes names the container runtimes a lane CLAIMS TO COVER, as a
// comma-separated list (e.g. "podman" or "docker-rootful,podman"). Every named
// runtime that is not reachable becomes a FAILURE.
//
// It is the missing half of EnvRequireDocker. That variable asserts that SOME
// runtime is reachable — which is the right question for a test whose subject
// is "containers work at all", and the WRONG one for a lane whose entire
// purpose is a specific runtime. A CI job called "podman" on a host with only
// docker satisfies CTXLOOM_REQUIRE_DOCKER=1 completely and covers podman not at
// all: it skips green, and the claim "this lane covers podman" lives only in
// the job's name, where no gate can read it. Naming it here moves that claim
// somewhere enforceable.
//
// The two variables are INDEPENDENT and compose. CTXLOOM_REQUIRE_DOCKER=1 alone
// does not promote a named-runtime miss, because "a runtime is reachable" is
// simply not a statement about which one; and CTXLOOM_REQUIRE_RUNTIMES=podman
// alone is enough to make an absent podman red without also demanding docker.
const EnvRequireRuntimes = "CTXLOOM_REQUIRE_RUNTIMES"

// requiredRuntimes is captured at package init for the same reason `required`
// is: testsupport.Isolate clears every CTXLOOM_* variable, so a test that
// isolates before it gates would otherwise silently demote itself back to
// skipping — the exact failure this package exists to remove.
var requiredRuntimes = parseRuntimeList(os.Getenv(EnvRequireRuntimes))

func parseRuntimeList(v string) []string {
	var out []string
	for _, f := range strings.Split(v, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

// RequiredRuntimes returns the runtimes this run's lane declared it covers, in
// sorted order. Empty means "no lane declared anything", which is a developer
// laptop and CI-before-anyone-set-it alike.
func RequiredRuntimes() []string {
	out := make([]string, len(requiredRuntimes))
	copy(out, requiredRuntimes)
	return out
}

// RuntimeRequired reports whether name appears in CTXLOOM_REQUIRE_RUNTIMES.
func RuntimeRequired(name string) bool {
	for _, r := range requiredRuntimes {
		if r == name {
			return true
		}
	}
	return false
}

// ValidateRequiredRuntimes rejects a declared runtime this build does not know
// how to probe. A typo is the failure mode this exists for: with
// CTXLOOM_REQUIRE_RUNTIMES=podmn nothing matches, every real runtime falls back
// to "not declared", every cell skips, and the lane reports green having proven
// exactly nothing — reintroducing the aspirational-coverage hole one layer up
// from where it was closed. known is the caller's probe vocabulary
// (containercell.Names()).
func ValidateRequiredRuntimes(known []string) error {
	var unknown []string
	for _, r := range requiredRuntimes {
		found := false
		for _, k := range known {
			if k == r {
				found = true
				break
			}
		}
		if !found {
			unknown = append(unknown, r)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	return fmt.Errorf("%s names %d runtime(s) this build cannot probe: %s. Known runtimes: %s. "+
		"An unknown name matches nothing, so every cell would skip and the lane would report green "+
		"having covered none of what it declared",
		EnvRequireRuntimes, len(unknown), strings.Join(unknown, ", "), strings.Join(known, ", "))
}

// NamedRuntimeDecision gates on a SPECIFIC runtime being reachable. name is a
// probe vocabulary term ("docker-rootful", "docker-rootless", "podman");
// available is the caller's probe for that one runtime; what names the test.
//
// Unreachable + the name declared in CTXLOOM_REQUIRE_RUNTIMES -> Fail: the lane
// said it covers this runtime and it does not.
//
// Unreachable + not declared -> Skip, naming the runtime that was not exercised
// and what did not run. Deliberately NOT promoted by CTXLOOM_REQUIRE_DOCKER=1:
// that variable means "a runtime is reachable", and promoting a named miss with
// it would make every matrix permanently red on every host, since no single
// host is both rootful and rootless and most have no podman. That is the same
// reason SkipCapability is never promoted — and this is why the two rules stay
// distinct rather than one being folded into the other: SkipCapability's
// condition can never be DECLARED (a host is rootless or it is not, and no
// runner can choose), whereas which runtimes a lane covers is exactly the kind
// of claim a runner makes and should be held to.
func NamedRuntimeDecision(name string, available bool, what string) (Decision, string) {
	if available {
		return Proceed, ""
	}
	if RuntimeRequired(name) {
		return Fail, fmt.Sprintf("runtime %q is NOT reachable, but %s declares this lane covers it (%s=%s): %s ran NOTHING for %s. "+
			"A skip here is how a lane comes to be named for a runtime it never exercised; "+
			"install/fix %s on this runner, or drop it from %s",
			name, EnvRequireRuntimes, EnvRequireRuntimes, strings.Join(requiredRuntimes, ","), what, name, name, EnvRequireRuntimes)
	}
	return Skip, fmt.Sprintf("runtime %q is not reachable on this host, so %s did NOT run under %s "+
		"(no other runtime substitutes for it; set %s=%s on a runner that has it to make this a failure instead)",
		name, what, name, EnvRequireRuntimes, name)
}

// RequireNamedRuntime applies NamedRuntimeDecision to a testing.TB.
func RequireNamedRuntime(t testing.TB, name string, available bool, what string) {
	t.Helper()
	d, msg := NamedRuntimeDecision(name, available, what)
	Apply(t, d, msg)
}
