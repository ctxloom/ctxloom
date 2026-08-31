package isolation

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/pidalive"
)

// labelOwnerPID is the container label key stamping the pid of the ctxloom
// process that started the container (ownerLabelArgs, read at `docker/podman
// run` time) — the identity ReapOrphanedContainers checks via pidalive.Probe
// to decide whether a still-RUNNING container has been orphaned.
const labelOwnerPID = "ctxloom.owner-pid"

// labelCreatedAt is the container label key stamping the RFC3339 UTC launch
// time (ownerLabelArgs) — containerReapGraceWindow reads this so a container
// that has not been running long enough to trust is never reaped.
const labelCreatedAt = "ctxloom.created-at"

// containerReapGraceWindow is how long a container must have been running
// before ReapOrphanedContainers will even consider its owner-liveness
// verdict. A container just past `docker run` can be visible to `ps` (and
// therefore to Enumerate) before its owning ctxloom process has finished the
// rest of its own startup — a probe run in that instant would see a pid
// whose parent shell/exec chain has not fully settled. The grace window is
// generous next to that startup cost and cheap to pay: this sweep runs once
// per `ctxloom run`/`ctxloom mcp` launch, not on a hot path.
const containerReapGraceWindow = 60 * time.Second

// ownerLabelArgs renders the `--label` flags every Docker/Podman RunArgs
// stamps onto its container's HEAD (see Docker.RunArgs, Podman.RunArgs) — the
// only mechanism this fix uses to record ownership; no sidecar pid file, so a
// container's ownership is recoverable purely from `docker/podman ps
// --filter label=...` with no host-side state to go stale or get orphaned
// itself. Freshly rendered on every call: os.Getpid() is THIS process (the
// one about to own the container) and time.Now() is the actual launch
// instant, so a caller must never memoize this across separate RunArgs
// calls.
func ownerLabelArgs() []string {
	return []string{
		"--label", fmt.Sprintf("%s=%d", labelOwnerPID, os.Getpid()),
		"--label", fmt.Sprintf("%s=%s", labelCreatedAt, time.Now().UTC().Format(time.RFC3339)),
	}
}

// ContainerReapVerdict is one candidate's outcome, mirroring WorktreeVerdict's
// vocabulary (see worktree_reap.go) for the container sweep.
type ContainerReapVerdict string

const (
	// ContainerReaped: the container's owner was confirmed dead and it was
	// removed.
	ContainerReaped ContainerReapVerdict = "reaped"
	// ContainerSkipped: left running — no ctxloom-iso- name prefix, no/
	// unparsable owner-pid or created-at label, still inside the grace
	// window, the owner is alive or its liveness could not be confirmed, or
	// the remove itself failed. Every one of these is "never touched, on
	// doubt" — see classifyContainer's doc.
	ContainerSkipped ContainerReapVerdict = "skipped"
)

// ContainerCandidate is one container Enumerate reported and what the reaper
// decided about it.
type ContainerCandidate struct {
	Name       string
	OwnerPID   int
	OwnerState pidalive.State // meaningless (zero value) when OwnerPID is 0
	Verdict    ContainerReapVerdict
	Reason     string
}

// ContainerReapResult tallies one ReapOrphanedContainers sweep, for a
// one-line boot-transcript summary — report only when something was actually
// removed, so the all-clear path stays silent (mirrors WorktreeReapResult).
type ContainerReapResult struct {
	Reaped  int
	Skipped int
}

// ReapOrphanedContainers sweeps every RUNNING ctxloom-iso-* container rt can
// see (via Enumerate) and force-removes the ones whose owning ctxloom process
// is CONFIRMED dead.
//
// This is the fix for a bug: teardown of a container-isolated runner is
// `defer isolation.RunnerHandle.Kill` inside cli.runState.teardownAll, and a
// defer never survives SIGKILL, an OOM kill, or a closed terminal — so a
// killed ctxloom leaves its container running forever, with nothing else
// ever sweeping it. --rm already means a container that merely EXITED is
// gone by itself (see the RunArgs doc); orphans are therefore containers
// still RUNNING whose owner died, never exited ones, so this reaper has
// nothing to do with exit cleanup.
//
// Every candidate is skipped rather than reaped on ANY doubt — the same
// conservatism ReapOrphanedWorktrees applies, and for the same reason: a
// destructive, irreversible decision must never be authorised by ambiguity.
// See classifyContainer for the exact rules. Best-effort throughout: an
// Enumerate or per-container remove failure warns and moves on, never
// aborting the sweep or the caller's own startup.
func ReapOrphanedContainers(ctx context.Context, rt Runtime) ContainerReapResult {
	var result ContainerReapResult
	if rt == nil {
		return result
	}

	infos, err := rt.Enumerate(ctx, containerNamePrefix)
	if err != nil {
		// A startup sweep is fault-tolerant by contract (see doc above): warn
		// and report nothing rather than propagate.
		clidiag.Warn("ctxloom", "container reap (%s): %v", rt.Name(), err)
		return result
	}

	now := time.Now()
	for _, info := range infos {
		c := classifyContainer(now, info)
		if c.Verdict != ContainerReaped {
			result.Skipped++
			continue
		}

		// classifyContainer only ever proposes ContainerReaped for a
		// confirmed-dead owner past the grace window with a matching name —
		// the actual removal (and its own failure mode) happens here, kept
		// separate so classification stays a pure decision a unit test can
		// exercise without invoking probeExec/RemoveArgs at all.
		if _, rerr := probeExec(ctx, rt.Binary(), rt.RemoveArgs(info.Name)); rerr != nil && !removeReportsGone(rerr) {
			clidiag.Warn("ctxloom", "container reap: %s owner %d is dead but rm -f failed (leaving it in place): %v", info.Name, c.OwnerPID, rerr)
			result.Skipped++
			continue
		}
		result.Reaped++
	}
	return result
}

// classifyContainer decides whether one ContainerInfo is reapable, applying
// every safety rule in one place and touching nothing:
//
//   - its name must carry the containerNamePrefix ("ctxloom-iso-") this
//     package's own containerName mints — anything else is never even
//     considered, regardless of what labels it happens to carry;
//   - it must carry a present, parsable, positive owner-pid label — absent
//     or unparsable is treated exactly like "cannot prove the owner dead",
//     never as "no owner, safe to reap";
//   - it must carry a present, parsable created-at label at least
//     containerReapGraceWindow old — a container that cannot prove its own
//     age is left alone, and one still within the window is left alone even
//     with a fully valid pid, in case its owner's own startup has not yet
//     settled;
//   - pidalive.Probe(ownerPID).MaybeAlive() must be false — Dead only, never
//     a bare non-Alive check, so an Unsure verdict (this probe's honest "I
//     cannot tell") skips exactly like a confirmed-live owner.
func classifyContainer(now time.Time, info ContainerInfo) ContainerCandidate {
	c := ContainerCandidate{Name: info.Name}

	if !strings.HasPrefix(info.Name, containerNamePrefix) {
		c.Verdict = ContainerSkipped
		c.Reason = fmt.Sprintf("name does not carry the %q prefix", containerNamePrefix)
		return c
	}

	pidRaw, ok := info.Labels[labelOwnerPID]
	if !ok {
		c.Verdict = ContainerSkipped
		c.Reason = "no owner-pid label — the owner cannot be proven dead"
		return c
	}
	pid, err := strconv.Atoi(pidRaw)
	if err != nil || pid <= 0 {
		c.Verdict = ContainerSkipped
		c.Reason = "owner-pid label is unparsable"
		return c
	}
	c.OwnerPID = pid

	createdRaw, ok := info.Labels[labelCreatedAt]
	if !ok {
		c.Verdict = ContainerSkipped
		c.Reason = "no created-at label — cannot confirm it is past the grace window"
		return c
	}
	created, err := time.Parse(time.RFC3339, createdRaw)
	if err != nil {
		c.Verdict = ContainerSkipped
		c.Reason = "created-at label is unparsable"
		return c
	}
	if age := now.Sub(created); age < containerReapGraceWindow {
		c.Verdict = ContainerSkipped
		c.Reason = fmt.Sprintf("still inside the %s startup grace window (age %s)", containerReapGraceWindow, age)
		return c
	}

	// MaybeAlive (not a bare == Alive check) treats an unconfirmable probe
	// the same as a live owner — see pidalive.State.MaybeAlive's doc: reaping
	// is destructive and irreversible, so an unsure verdict must skip exactly
	// like a confirmed-live owner, never fall through toward removal.
	c.OwnerState = pidalive.Probe(pid)
	if c.OwnerState.MaybeAlive() {
		c.Verdict = ContainerSkipped
		if c.OwnerState == pidalive.Alive {
			c.Reason = fmt.Sprintf("owner process %d is alive", pid)
		} else {
			c.Reason = fmt.Sprintf("owner process %d's liveness could not be confirmed", pid)
		}
		return c
	}

	c.Verdict = ContainerReaped
	c.Reason = fmt.Sprintf("owner process %d is confirmed dead", pid)
	return c
}
