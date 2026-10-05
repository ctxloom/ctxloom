package isolation

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/pidalive"
)

// labelOwnerPID is the container label key stamping the pid of the ctxloom
// process that started the container (ownerLabelArgs, read at `docker/podman
// run` time) — the identity classifyContainer checks via pidalive.Probe
// to decide whether a still-RUNNING container has been orphaned.
const labelOwnerPID = "ctxloom.owner-pid"

// labelOwnerPIDNS is the container label key stamping the pid namespace
// labelOwnerPID was read in (ownerPIDNamespace). A pid names a process only
// within its namespace: under docker-outside-of-docker, ctxloom processes in
// different pid namespaces share one daemon, and one of them probing another's
// pid in its OWN namespace would read a live owner as dead and kill its
// runner. So only containers stamped with its own namespace are judged.
const labelOwnerPIDNS = "ctxloom.owner-pidns"

// labelCreatedAt is the container label key stamping the RFC3339 UTC launch
// time (ownerLabelArgs) — containerReapGraceWindow reads this so a container
// that has not been running long enough to trust is never judged an orphan.
const labelCreatedAt = "ctxloom.created-at"

// containerReapGraceWindow is how long a container must have been running
// before FindOrphanedContainers will even consider its owner-liveness
// verdict. A container just past `docker run` can be visible to `ps` (and
// therefore to Enumerate) before its owning ctxloom process has finished the
// rest of its own startup — a probe run in that instant would see a pid
// whose parent shell/exec chain has not fully settled. The grace window is
// generous next to that startup cost and cheap to pay: nothing on a hot path
// asks.
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
		"--label", labelOwnerPIDNS + "=" + ownerPIDNamespace(),
	}
}

// initArgs asks the container runtime to put a real init at PID 1.
//
// Without it the image entrypoint's `exec "$@"` REPLACES the entrypoint, so the
// engine itself becomes PID 1 — and PID 1 must reap orphans. `ctxloom runner`
// does not, so a delegated runner reparented to it becomes a permanent zombie.
// That is not merely untidy: a zombie keeps its process-table entry, so
// pidalive.Probe (which classifies with signal 0) reads it as ALIVE, its stale
// discovery marker is never reaped, and the next `ctxloom mcp` in that cell
// REFUSES TO START rather than risk becoming a rogue second coordinator.
// Measured: 13 zombies accumulated in one agent session, and it cost two
// acceptance scenarios that are green wherever PID 1 is a real init.
//
// Rendered once here rather than at each runtime's RunArgs, because a rule
// hand-copied into two run-arg heads is a rule that drifts — this package has
// already watched the `-timeout 30m` gate rule diverge across three copies.
//
// NOTE the flag is NOT one binary: docker's --init runs docker-init (tini),
// podman's runs catatonit, which must be present on the host or the run fails.
// Owning a pinned init in our own image is the settled follow-up; until then
// this is the runtime's init, not ours.
func initArgs() []string { return []string{"--init"} }

// ContainerReapVerdict is classifyContainer's judgement of one candidate,
// mirroring WorktreeVerdict's vocabulary (see worktree_reap.go).
type ContainerReapVerdict string

const (
	// ContainerOrphaned: the container's owner is confirmed dead.
	ContainerOrphaned ContainerReapVerdict = "orphaned"
	// ContainerSkipped: not judged an orphan — no ctxloom-iso- name prefix,
	// no/unparsable owner-pid or created-at label, an owner-pidns other than
	// this process's own (or none), still inside the grace window, or the
	// owner is alive or its liveness could not be confirmed. Every one of
	// these is "not an orphan, on doubt" — see classifyContainer's doc.
	ContainerSkipped ContainerReapVerdict = "skipped"
)

// ContainerCandidate is one container Enumerate reported and what
// classifyContainer decided about it.
type ContainerCandidate struct {
	Name       string
	OwnerPID   int
	OwnerState pidalive.State // meaningless (zero value) when OwnerPID is 0
	Verdict    ContainerReapVerdict
	Reason     string
}

// FindOrphanedContainers lists every RUNNING ctxloom-iso-* container rt can
// see (via Enumerate) whose owning ctxloom process is CONFIRMED dead, and
// touches none of them.
//
// A dead owner does not by itself make a container garbage: a runner whose
// coordinator died waits out its owner-loss window (runner.Home.OwnerLost)
// for a restarted coordinator to re-adopt it, and exits on its own — --rm
// then removes the container — when none does. Within that window a healthy
// runner and a wedged one look identical here; one still listed after it is
// wedged. That is why this only answers the question and leaves acting on it
// to its caller.
//
// Every candidate is classified by classifyContainer, which says "not an
// orphan" on ANY doubt. An Enumerate failure is returned: a runtime that
// could not list its containers has not shown it holds none.
func FindOrphanedContainers(ctx context.Context, rt Runtime) ([]ContainerCandidate, error) {
	if rt == nil {
		return nil, nil
	}
	infos, err := rt.Enumerate(ctx, containerNamePrefix)
	if err != nil {
		return nil, fmt.Errorf("list %s containers: %w", rt.Name(), err)
	}
	now := time.Now()
	var out []ContainerCandidate
	for _, info := range infos {
		if c := classifyContainer(now, info); c.Verdict == ContainerOrphaned {
			out = append(out, c)
		}
	}
	return out, nil
}

// ownerPIDOf reads the owner pid a container's labels carry. why is non-empty
// when that pid cannot be judged from this process: absent, unparsable, or
// read in another pid namespace (pid is still returned for that last one, for
// the report).
func ownerPIDOf(labels map[string]string) (pid int, why string) {
	pidRaw, ok := labels[labelOwnerPID]
	if !ok {
		return 0, "no owner-pid label — the owner cannot be proven dead"
	}
	pid, err := strconv.Atoi(pidRaw)
	if err != nil || pid <= 0 {
		return 0, "owner-pid label is unparsable"
	}
	if ns, ok := labels[labelOwnerPIDNS]; !ok || ns != ownerPIDNamespace() {
		return pid, "owner-pid was read in another pid namespace (or names none) — it cannot be judged from here"
	}
	return pid, ""
}

// classifyContainer decides whether one ContainerInfo is an orphan, applying
// every safety rule in one place and touching nothing:
//
//   - its name must carry the containerNamePrefix ("ctxloom-iso-") this
//     package's own containerName mints — anything else is never even
//     considered, regardless of what labels it happens to carry;
//   - it must carry a present, parsable, positive owner-pid label — absent
//     or unparsable is treated exactly like "cannot prove the owner dead",
//     never as "no owner, an orphan";
//   - it must carry an owner-pidns label equal to this process's own pid
//     namespace — a pid from another namespace names nothing here;
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

	pid, why := ownerPIDOf(info.Labels)
	c.OwnerPID = pid
	if why != "" {
		c.Verdict = ContainerSkipped
		c.Reason = why
		return c
	}

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
	// the same as a live owner — see pidalive.State.MaybeAlive's doc: removing
	// an orphan is destructive and irreversible, so an unsure verdict must
	// skip exactly like a confirmed-live owner, never read as an orphan.
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

	c.Verdict = ContainerOrphaned
	c.Reason = fmt.Sprintf("owner process %d is confirmed dead", pid)
	return c
}
