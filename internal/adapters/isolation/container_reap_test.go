package isolation

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reapFakeRuntime is a Runtime stub for ReapOrphanedContainers: Enumerate
// returns a fixed candidate list (no `docker ps` involved) and everything
// else falls back to fakeRuntime — this is exactly the "fake Runtime, no
// docker" seam the reaper is designed around.
type reapFakeRuntime struct {
	fakeRuntime
	infos []ContainerInfo
}

func (r reapFakeRuntime) Enumerate(context.Context, string) ([]ContainerInfo, error) {
	return r.infos, nil
}

// RemoveArgs mirrors ociRuntime.RemoveArgs' real `rm -f <name>` — fakeRuntime's
// own default is a noop nil, which would make TestReapOrphanedContainers_
// DeadOwnerIsReaped's removal-argv assertion vacuous.
func (reapFakeRuntime) RemoveArgs(name string) []string { return []string{"rm", "-f", name} }

// stubReapProbeExec replaces the package's probeExec seam with one that
// records every call (binary + args, as one joined slice) and reports
// success, restoring the original on cleanup. ReapOrphanedContainers'
// removal step calls probeExec directly (not through the fake Runtime), so
// this is the only way to observe — or prevent a false pass by NOT
// observing — a remove attempt. Named distinctly from sharedfs_test.go's own
// stubProbeExec (a different signature, scoped to the shared-fs marker
// probe) to avoid colliding in this shared test package.
func stubReapProbeExec(t *testing.T) *[][]string {
	t.Helper()
	orig := probeExec
	calls := &[][]string{}
	probeExec = func(_ context.Context, bin string, args []string) (string, error) {
		*calls = append(*calls, append([]string{bin}, args...))
		return "", nil
	}
	t.Cleanup(func() { probeExec = orig })
	return calls
}

// oldEnough is a created-at timestamp safely past containerReapGraceWindow.
func oldEnough() string {
	return time.Now().Add(-2 * containerReapGraceWindow).UTC().Format(time.RFC3339)
}

func labeledInfo(name string, pid int, createdAt string) ContainerInfo {
	return ContainerInfo{
		Name: name,
		Labels: map[string]string{
			labelOwnerPID:  strconv.Itoa(pid),
			labelCreatedAt: createdAt,
		},
	}
}

// TestReapOrphanedContainers_DeadOwnerIsReaped is the positive case: a
// ctxloom-iso- container, past the grace window, whose owner-pid label names
// a CONFIRMED dead process is reaped — and the removal actually goes out
// through rt.Binary()/rt.RemoveArgs via probeExec, not just tallied.
func TestReapOrphanedContainers_DeadOwnerIsReaped(t *testing.T) {
	calls := stubReapProbeExec(t)
	rt := reapFakeRuntime{
		fakeRuntime: fakeRuntime{name: "docker", binary: "docker", available: true},
		infos:       []ContainerInfo{labeledInfo("ctxloom-iso-agent-abc", deadPid, oldEnough())},
	}

	result := ReapOrphanedContainers(context.Background(), rt)

	assert.Equal(t, ContainerReapResult{Reaped: 1}, result)
	require.Len(t, *calls, 1, "the dead owner's container must actually be removed, not just tallied")
	assert.Equal(t, []string{"docker", "rm", "-f", "ctxloom-iso-agent-abc"}, (*calls)[0])
}

// TestReapOrphanedContainers_LiveOwnerIsNotReaped: an otherwise-identical
// candidate whose owner-pid names THIS live test process must be left alone
// — no tally as reaped, and critically no removal call at all.
func TestReapOrphanedContainers_LiveOwnerIsNotReaped(t *testing.T) {
	calls := stubReapProbeExec(t)
	rt := reapFakeRuntime{
		fakeRuntime: fakeRuntime{name: "docker", binary: "docker", available: true},
		infos:       []ContainerInfo{labeledInfo("ctxloom-iso-agent-abc", os.Getpid(), oldEnough())},
	}

	result := ReapOrphanedContainers(context.Background(), rt)

	assert.Equal(t, ContainerReapResult{Skipped: 1}, result)
	assert.Empty(t, *calls, "a live owner's container must never be removed")
}

// TestReapOrphanedContainers_AbsentOrUnparsableLabelIsNotReaped covers both
// "no owner-pid label at all" and "owner-pid present but garbage" — CLAUDE.md's
// rule that ambiguity must never authorise a delete, so neither reads as "no
// owner, safe to reap".
func TestReapOrphanedContainers_AbsentOrUnparsableLabelIsNotReaped(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
	}{
		{
			name:   "no owner-pid label",
			labels: map[string]string{labelCreatedAt: oldEnough()},
		},
		{
			name: "unparsable owner-pid label",
			labels: map[string]string{
				labelOwnerPID:  "not-a-pid",
				labelCreatedAt: oldEnough(),
			},
		},
		{
			name: "zero owner-pid label",
			labels: map[string]string{
				labelOwnerPID:  "0",
				labelCreatedAt: oldEnough(),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := stubReapProbeExec(t)
			rt := reapFakeRuntime{
				fakeRuntime: fakeRuntime{name: "docker", binary: "docker", available: true},
				infos:       []ContainerInfo{{Name: "ctxloom-iso-agent-abc", Labels: tc.labels}},
			}

			result := ReapOrphanedContainers(context.Background(), rt)

			assert.Equal(t, ContainerReapResult{Skipped: 1}, result)
			assert.Empty(t, *calls)
		})
	}
}

// TestReapOrphanedContainers_InsideGraceWindowIsNotReaped: a confirmed-dead
// owner and a perfectly valid label pair is still spared when created-at says
// the container is younger than containerReapGraceWindow — it may not be
// fully labelled/settled yet.
func TestReapOrphanedContainers_InsideGraceWindowIsNotReaped(t *testing.T) {
	calls := stubReapProbeExec(t)
	justCreated := time.Now().Add(-1 * time.Second).UTC().Format(time.RFC3339)
	rt := reapFakeRuntime{
		fakeRuntime: fakeRuntime{name: "docker", binary: "docker", available: true},
		infos:       []ContainerInfo{labeledInfo("ctxloom-iso-agent-abc", deadPid, justCreated)},
	}

	result := ReapOrphanedContainers(context.Background(), rt)

	assert.Equal(t, ContainerReapResult{Skipped: 1}, result)
	assert.Empty(t, *calls)
}

// TestReapOrphanedContainers_NonPrefixNameIsNeverConsidered: a container that
// does not carry the ctxloom-iso- prefix is skipped regardless of how
// perfectly it satisfies every other rule (dead owner, past the grace
// window, valid labels) — the name check is a hard boundary, not a tiebreak.
func TestReapOrphanedContainers_NonPrefixNameIsNeverConsidered(t *testing.T) {
	calls := stubReapProbeExec(t)
	rt := reapFakeRuntime{
		fakeRuntime: fakeRuntime{name: "docker", binary: "docker", available: true},
		infos:       []ContainerInfo{labeledInfo("some-unrelated-container", deadPid, oldEnough())},
	}

	result := ReapOrphanedContainers(context.Background(), rt)

	assert.Equal(t, ContainerReapResult{Skipped: 1}, result)
	assert.Empty(t, *calls, "a non-ctxloom-iso- container must never be touched, even with a dead-owner label")
}

// TestReapOrphanedContainers_NilRuntimeIsANoop matches ReapOrphanedWorktrees'
// own fault tolerance: a caller with no available runtime gets a zero-value
// result, not a panic.
func TestReapOrphanedContainers_NilRuntimeIsANoop(t *testing.T) {
	assert.Equal(t, ContainerReapResult{}, ReapOrphanedContainers(context.Background(), nil))
}

// TestOwnerLabelArgs_StampsThisProcessAndARecentTimestamp pins ownerLabelArgs'
// contract directly: the pid label names THIS process (the one about to own
// the container) and the timestamp label parses as RFC3339 and is fresh —
// the two facts ReapOrphanedContainers' safety rules depend on actually
// being true at `run` time, not just at read time.
func TestOwnerLabelArgs_StampsThisProcessAndARecentTimestamp(t *testing.T) {
	before := time.Now()
	args := ownerLabelArgs()
	after := time.Now()

	require.Len(t, args, 4)
	assert.Equal(t, "--label", args[0])
	assert.Equal(t, labelOwnerPID+"="+strconv.Itoa(os.Getpid()), args[1])
	assert.Equal(t, "--label", args[2])

	createdRaw := args[3][len(labelCreatedAt)+1:]
	created, err := time.Parse(time.RFC3339, createdRaw)
	require.NoError(t, err)
	assert.False(t, created.Before(before.Add(-time.Second)), "created-at must not predate the call")
	assert.False(t, created.After(after.Add(time.Second)), "created-at must not postdate the call")
}
