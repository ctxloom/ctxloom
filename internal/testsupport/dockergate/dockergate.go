// Package dockergate is the single decision point for what a docker-gated
// test does when its container runtime is missing.
//
// Every `-tags docker_integration` test self-skips when the daemon is
// unreachable. That is right on a developer laptop without docker and WRONG
// on a CI runner: a runner that loses its socket then passes the
// docker-integration step having executed zero container tests, reporting
// green for a suite that ran nothing. It is the same silent-no-op failure
// family these tests exist to catch, sitting inside the gate meant to catch
// it — and it was load-bearing, .github/workflows/ci.yml explicitly relied on
// the self-skip to keep the step "safe to run".
//
// So reachability becomes an assertion where it can be one: with
// CTXLOOM_REQUIRE_DOCKER=1 (CI sets it, nobody else does) an unreachable
// runtime is a FAILURE via RequireRuntime. Locally, unset, the skip stays and
// a developer without docker is not blocked.
//
// Routing every skip through here is the point: a bare t.Skip in a
// docker-gated test is invisible reachability policy that this env var cannot
// see. `just test-docker-integration` runs _check-docker-skip-gate
// (build/gates.justfile), which fails the build on any t.Skip/t.Skipf inside a
// docker_integration-tagged file, so a future test cannot add an unguarded
// one.
//
// The package deliberately does NOT probe for docker itself: callers pass the
// availability bool. internal/lm/isolation owns the probe
// (isolation.Docker{}.Available()) and its own tests are in `package
// isolation`, so a probing dockergate would import isolation and close a
// cycle. A bool keeps one gate usable from all four packages.
//
// WHICH runtime, not merely SOME runtime, is the second axis — see runtimes.go.
// CTXLOOM_REQUIRE_DOCKER cannot express "this lane covers podman", so a lane
// meant to cover podman on a host that has only docker would skip GREEN: the
// aspiration is in the lane's name and nowhere a gate can read it.
// CTXLOOM_REQUIRE_RUNTIMES names them, and RequireNamedRuntime enforces it.
package dockergate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// EnvRequireDocker, set to "1", promotes "container runtime unreachable" from
// a skip to a failure. Set it in CI only.
const EnvRequireDocker = "CTXLOOM_REQUIRE_DOCKER"

// required is captured at package init, on purpose. testsupport.Isolate
// clears every CTXLOOM_* variable in testsupport.EnvKeys (this one included,
// so the enforcement test there stays honest), and a test that isolates
// before it gates would otherwise silently demote itself back to skipping —
// the exact failure this package exists to remove.
var required = os.Getenv(EnvRequireDocker) == "1"

// RuntimeDecision is RequireRuntime's policy WITHOUT a testing.TB: it returns
// what the gate decided and the message that decision carries. It exists
// because the acceptance suite is godog, not `go test` — a Gherkin step has no
// *testing.T to Skipf on, and reimplementing the CTXLOOM_REQUIRE_DOCKER rule
// there would be a second, drifting copy of exactly the policy this package
// exists to centralise (the two-copies-of-one-recipe hole in
// build/gates.justfile is the same mistake in the neighbouring file).
//
// RequireRuntime is this function plus Apply. Both callers therefore share one
// rule and one wording.
func RuntimeDecision(available bool, what string) (Decision, string) {
	if available {
		return Proceed, ""
	}
	if required {
		return Fail, fmt.Sprintf("no container runtime is reachable, but %s=1 demands one: %s ran NOTHING. "+
			"A skip here would report green for a suite that executed zero container tests; "+
			"fix the runner's docker socket, or unset %s to go back to skipping.",
			EnvRequireDocker, what, EnvRequireDocker)
	}
	return Skip, fmt.Sprintf("docker unavailable; skipping %s (set %s=1 to make this a failure instead)",
		what, EnvRequireDocker)
}

// RequireRuntime gates a test on container-runtime REACHABILITY. available is
// the caller's probe (isolation.Docker{}.Available()); what names the test in
// the resulting message, e.g. "the container-progress integration test".
//
// Unreachable + CTXLOOM_REQUIRE_DOCKER=1 fails the test. Unreachable without
// it skips, naming the env var so the reader knows the stricter mode exists.
//
// It answers "is SOME runtime reachable", never "is podman reachable" — see
// RequireNamedRuntime for that.
func RequireRuntime(t testing.TB, available bool, what string) {
	t.Helper()
	d, msg := RuntimeDecision(available, what)
	Apply(t, d, msg)
}

// Apply turns a Decision into the testing.TB call it names. Fatalf before
// Skipf is deliberate and load-bearing: a testing.TB that merely RECORDS a
// failure instead of stopping the goroutine (this package's own fakeTB, found
// by dockergate_test.go) must not fall through to a "skipped" report for what
// CTXLOOM_REQUIRE_DOCKER=1 demands be a hard failure.
func Apply(t testing.TB, d Decision, msg string) {
	t.Helper()
	switch d {
	case Fail:
		t.Fatalf("%s", msg)
	case Skip:
		t.Skipf("%s", msg)
	case Proceed:
	}
}

// SkipCapability skips for an environment CAPABILITY that a legitimate runner
// may lack — a rootless daemon, engine credentials, git on PATH — as opposed
// to runtime reachability. These are NEVER promoted to failures:
// CTXLOOM_REQUIRE_DOCKER asserts that docker is REACHABLE, not that the host
// is rootless or carries a subscription. GitHub-hosted runners are rootful,
// so promoting those would turn CI permanently red for a condition CI cannot
// satisfy.
//
// It exists so that every skip in a docker-gated test is still a call into
// this package, which is what lets _check-docker-skip-gate ban bare t.Skip
// outright rather than maintaining an allowlist of "fine" ones.
func SkipCapability(t testing.TB, reason string) {
	t.Helper()
	t.Skipf("%s — environment capability, not runtime reachability, so %s=1 does not promote it",
		reason, EnvRequireDocker)
}

// DockerIsRootless reports whether the local docker daemon runs rootless.
//
// It exists because a docker-gated test that bind-mounts a host directory has
// to know: a ROOTFUL daemon runs the container as real root, so anything it
// writes lands root-owned on the host (unremovable debris) and anything it
// reads out of a 0700 directory owned by the test user is refused. Passing
// `--user $(id -u):$(id -g)` fixes both, and is exactly wrong under a rootless
// daemon, which already maps container root onto the invoking user.
//
// An unreachable or unanswering daemon reads as ROOTFUL — the conservative
// direction, since the extra --user is harmless where it is unnecessary while
// its absence leaks root-owned files. This is deliberately NOT part of the
// skip/fail decision above: reachability is that decision's job, and a test
// calling this has already passed it.
//
// It lives here rather than in each test file because two docker-gated
// packages had already grown their own copy, and a probe of the daemon's
// posture that disagrees between test files is how one of them silently starts
// mounting as the wrong user.
func DockerIsRootless() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.SecurityOptions}}").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "rootless")
}
