package containercell

import (
	"context"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// Select answers "which runtime should a SINGLE-runtime caller use, and what
// should it do if there is none" — the acceptance suite's question. A caller
// that wants the whole matrix (the docker-gated cell test) iterates Detect and
// gates each row with dockergate.RequireNamedRuntime instead.
//
// The order of the checks is the policy:
//
//  1. A name in CTXLOOM_REQUIRE_RUNTIMES this build cannot probe is a Fail
//     before anything else runs. A typo covers nothing while looking like it
//     covers something, which is the exact hole the variable exists to close.
//  2. A DECLARED runtime that is not reachable is a Fail, even if another
//     runtime is. "This lane covers podman" is not satisfied by docker being
//     present; substituting one for the other is how a lane comes to be named
//     for a runtime it never exercised.
//  3. Otherwise prefer a declared runtime, then the vocabulary order.
//  4. With none available at all, fall back to the reachability gate —
//     CTXLOOM_REQUIRE_DOCKER=1 makes it a Fail, unset it is a Skip.
//
// what names the caller in every message ("J20's container delivery rows").
func Select(ctx context.Context, what string) (Runtime, dockergate.Decision, string) {
	if err := dockergate.ValidateRequiredRuntimes(Names()); err != nil {
		return Runtime{}, dockergate.Fail, err.Error()
	}
	found := Detect(ctx)
	byName := make(map[string]Runtime, len(found))
	for _, r := range found {
		byName[r.Name] = r
	}

	declared := dockergate.RequiredRuntimes()
	for _, name := range declared {
		r := byName[name]
		if d, msg := dockergate.NamedRuntimeDecision(name, r.Available, what); d != dockergate.Proceed {
			return Runtime{}, d, msg
		}
	}
	for _, name := range declared {
		if r := byName[name]; r.Available {
			return r, dockergate.Proceed, ""
		}
	}
	for _, name := range Names() {
		if r := byName[name]; r.Available {
			return r, dockergate.Proceed, ""
		}
	}

	d, msg := dockergate.RuntimeDecision(false, what)
	return Runtime{}, d, msg + "\n" + Report(found)
}

// Report renders what each runtime probe found, one line each. It is printed on
// every container-cell run, passing or skipping, for the same reason the
// acceptance suite prints its live-engine report: a run that covered nothing
// must SAY so, rather than being indistinguishable from one that covered
// everything.
func Report(runtimes []Runtime) string {
	var b strings.Builder
	b.WriteString("container cell runtimes:\n")
	for _, r := range runtimes {
		mark := "unavailable"
		if r.Available {
			mark = "AVAILABLE"
		}
		declared := ""
		if dockergate.RuntimeRequired(r.Name) {
			declared = " [declared by " + dockergate.EnvRequireRuntimes + "]"
		}
		fmt.Fprintf(&b, "  %-16s %-11s %s%s\n", r.Name, mark, r.Detail, declared)
	}
	return strings.TrimRight(b.String(), "\n")
}
