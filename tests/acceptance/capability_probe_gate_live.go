//go:build acceptance

// Package acceptance: the live half of the shared cell gate — the two questions
// that cannot be answered without a World and a reachable container runtime,
// composed on top of the pure decisions in capability_probe_gate.go.
//
// Nothing here decides anything on its own. It orders the gates by COST (the
// cheapest refusal first, so a cell never discovers at container-probe time
// something the liveAgents table already knew), records what the gate saw into
// the cell's evidence sidecar, and hands every refusal to probeCellSkip so the
// line reads the same whichever gate produced it.
package acceptance

import (
	"context"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport/containercell"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// probeCellGate runs the gates in cost order and returns the engine's liveAgent
// row together with its liveAgents key (the llm label a cell's config selects).
// A refusal comes back as godog.ErrSkip with the reason already printed; a
// MALFORMED cell (an unknown axis, an engine no liveAgents row covers) comes
// back as a hard error, because that is a bug in the feature file rather than a
// fact about this box.
//
// The cell is passed whole rather than as loose axis strings: P4's arms differ
// only by variant, and four positional strings is exactly the shape in which a
// caller eventually swaps two of them.
//
// It also populates w.docStepMaterialized with the availability report, so the
// evidence sidecar of any cell — green, red or skipped — records what the gate
// saw about the engine at the moment it decided.
func probeCellGate(c context.Context, w *World, family string, cell probeCellID) (liveAgent, string, error) {
	a, key, err := probeCellResolve(family, cell)
	if err != nil {
		return liveAgent{}, "", err
	}

	report, skip := probeCellDecide(probeEngine(key, a, realHomeDir, resolveOptIn()))
	w.docStepMaterialized = report
	if skip != "" {
		return liveAgent{}, "", probeCellSkip(family, cell, skip)
	}

	if cell.Workspace == "worktree" {
		if path, reason := probeWorktreeAuthAvailable(cell.Engine); path == probeAuthNone {
			return liveAgent{}, "", probeCellSkip(family, cell,
				"worktree axis cannot authenticate this engine: "+reason)
		}
	}
	// probeCellResolve (called above, at the top of this function) already
	// parsed cell.Runtime via agent.ParseRuntimeAxis and rejected anything
	// that does not resolve — including the retired undifferentiated
	// "container" (task unwatched-discharge split it into container-rootless/
	// container-rootful; there is deliberately no "any container" value) —
	// so by the time control reaches here cell.Runtime is a KNOWN-GOOD axis
	// string. This asks the canonical predicate for "is it a container in
	// EITHER ownership mode", never a second vocabulary switch: a host cell
	// needs no further gating and falls through unchanged.
	if agent.IsContainerRuntimeAxis(agent.RuntimeAxis(cell.Runtime)) {
		// The credential mechanism is the SAME mount either way (see the
		// feature file's own header), so the auth check does not branch on
		// ownership; only runtime SELECTION does.
		if path, reason := probeContainerAuthAvailable(cell.Engine); path == probeAuthNone {
			return liveAgent{}, "", probeCellSkip(family, cell,
				"container axis cannot authenticate this engine: "+reason)
		}
		rt, decision, msg := probeContainerRuntimeForAxis(c, cell.Runtime, "the "+family+" container cell")
		switch decision {
		case dockergate.Fail:
			// A lane DECLARED it covers this axis (CTXLOOM_REQUIRE_RUNTIMES)
			// and does not — a hard error, never a skip, exactly like every
			// other named-runtime Fail in this suite (see
			// steps_j001400_bundle_distribution.go's j001400DeliverInContainer
			// for the same idiom).
			return liveAgent{}, "", fmt.Errorf("%s: %s", family, msg)
		case dockergate.Skip:
			return liveAgent{}, "", probeCellSkip(family, cell, msg)
		case dockergate.Proceed:
			_ = rt // the selected runtime; nothing here consumes it directly —
			// the actual container launch happens inside ctxloom itself, via
			// config.yaml's `runtime: container-rootless|container-rootful`
			// (capability_probe_fixture.go's matrixConfigYAML), not through
			// this cell object. This gate only answers "can SOMETHING serve
			// this axis at all", so a paid turn is never spent discovering
			// what containercell.Detect already knew.
		}
	}
	return a, key, nil
}

// probeContainerRuntimeForAxis resolves whether SOME reachable container
// runtime actually serves ONE ownership axis ("container-rootless" |
// "container-rootful"), reusing containercell's existing detection
// (Detect, Runtime.RootMapsToInvoker) and dockergate's existing
// declared-vs-reachable rule (NamedRuntimeDecision) — no new detector, and no
// second copy of the fail-vs-skip policy CTXLOOM_REQUIRE_RUNTIMES already
// owns (dockergate/runtimes.go).
//
// docker's two ownership modes are already split into two probe-vocabulary
// names (containercell.DockerRootful / DockerRootless, mutually exclusive by
// containercell's own probe), so the axis maps onto ONE of those names
// directly and the standard named-runtime decision applies unchanged.
//
// podman is named ONCE in the vocabulary (containercell.Podman) because its
// ownership is an INVOCATION property (podman vs sudo podman), not a second
// daemon — containercell's own package doc says so, and nothing here
// re-derives it. So whether THIS box's podman serves THIS axis is read from
// the probed Runtime.RootMapsToInvoker, and a podman that answered with the
// WRONG ownership is reported as a mismatch, never silently treated as
// "unreachable" and never silently accepted as a substitute — a same-vendor
// ownership swap is exactly the substitution the two container runtime values
// exist to catch (see dockergate's own doc on why "this lane covers podman"
// must not be satisfied by the wrong thing answering).
func probeContainerRuntimeForAxis(ctx context.Context, axis, what string) (containercell.Runtime, dockergate.Decision, string) {
	if err := dockergate.ValidateRequiredRuntimes(containercell.Names()); err != nil {
		return containercell.Runtime{}, dockergate.Fail, err.Error()
	}

	wantRootless := axis == "container-rootless"
	dockerName := containercell.DockerRootful
	if wantRootless {
		dockerName = containercell.DockerRootless
	}

	found := containercell.Detect(ctx)
	byName := make(map[string]containercell.Runtime, len(found))
	for _, r := range found {
		byName[r.Name] = r
	}
	docker := byName[dockerName]
	podman := byName[containercell.Podman]
	podmanServes := podman.Available && podman.RootMapsToInvoker == wantRootless

	if d, msg := dockergate.NamedRuntimeDecision(dockerName, docker.Available, what); d == dockergate.Proceed {
		return docker, dockergate.Proceed, ""
	} else if d == dockergate.Fail {
		// docker's own name already encodes ownership, so there is no "wrong
		// ownership" shape to report for it the way there is for podman below
		// — unreachable is the whole story.
		return containercell.Runtime{}, dockergate.Fail, msg
	}

	if dockergate.RuntimeRequired(containercell.Podman) {
		switch {
		case podmanServes:
			return podman, dockergate.Proceed, ""
		case podman.Available:
			return containercell.Runtime{}, dockergate.Fail, fmt.Sprintf(
				"%s declares this lane covers %s, but the reachable podman here answers %s (%s) — the %s axis "+
					"needs podman INVOKED so its OWN ownership is %s (podman vs sudo podman, not a second daemon); "+
					"a same-vendor ownership swap is exactly the substitution %s exists to catch",
				dockergate.EnvRequireRuntimes, containercell.Podman, ownershipWord(podman.RootMapsToInvoker), podman.Detail,
				axis, ownershipWord(wantRootless), dockergate.EnvRequireRuntimes)
		default:
			return containercell.Runtime{}, dockergate.Fail, fmt.Sprintf(
				"podman is NOT reachable, but %s declares this lane covers it: %s ran NOTHING for the %s axis",
				dockergate.EnvRequireRuntimes, what, axis)
		}
	}
	if podmanServes {
		return podman, dockergate.Proceed, ""
	}

	return containercell.Runtime{}, dockergate.Skip, fmt.Sprintf(
		"no container runtime with %s ownership reachable for %s (needs %s reachable, or podman invoked so it "+
			"reports %s ownership — no other runtime substitutes for one with the wrong ownership; set %s=%s or "+
			"%s on a runner that has it to make this a failure instead)\n%s",
		ownershipWord(wantRootless), what, dockerName, ownershipWord(wantRootless),
		dockergate.EnvRequireRuntimes, dockerName, containercell.Podman, containercell.Report(found))
}

// ownershipWord renders a probed Runtime.RootMapsToInvoker as the word this
// axis's own vocabulary uses, so a mismatch message reads in the same terms
// as containercell's package doc and the feature file's header.
func ownershipWord(rootMapsToInvoker bool) string {
	if rootMapsToInvoker {
		return "ROOTLESS"
	}
	return "ROOTFUL"
}
