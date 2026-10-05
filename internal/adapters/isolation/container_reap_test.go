package isolation

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/pidalive"
)

// reapFakeRuntime is a Runtime stub for FindOrphanedContainers: Enumerate
// returns a fixed candidate list (no `docker ps` involved) and everything
// else falls back to fakeRuntime — the "fake Runtime, no docker" seam the
// finder is designed around. err, when set, is Enumerate's failure.
type reapFakeRuntime struct {
	fakeRuntime
	infos []ContainerInfo
	err   error
}

func (r reapFakeRuntime) Enumerate(context.Context, string) ([]ContainerInfo, error) {
	return r.infos, r.err
}

// RemoveArgs mirrors ociRuntime.RemoveArgs' real `rm -f <name>`, so a finder
// that DID try to remove would be seen doing it through probeExec rather
// than issuing an empty argv.
func (reapFakeRuntime) RemoveArgs(name string) []string { return []string{"rm", "-f", name} }

// stubReapProbeExec replaces the package's probeExec seam with one that
// records every call (binary + args, as one joined slice) and reports
// success, restoring the original on cleanup. Any removal this package makes
// goes out through probeExec, so an empty record is the proof that
// FindOrphanedContainers touched nothing. Named distinctly from
// sharedfs_test.go's own stubProbeExec (a different signature, scoped to the
// shared-fs marker probe) to avoid colliding in this shared test package.
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
			labelOwnerPID:   strconv.Itoa(pid),
			labelCreatedAt:  createdAt,
			labelOwnerPIDNS: ownerPIDNamespace(),
		},
	}
}

func dockerWith(infos ...ContainerInfo) reapFakeRuntime {
	return reapFakeRuntime{
		fakeRuntime: fakeRuntime{name: "docker", binary: "docker", available: true},
		infos:       infos,
	}
}

// TestFindOrphanedContainers_ForeignPIDNamespaceIsNeverJudged: under
// docker-outside-of-docker several ctxloom processes in different pid
// namespaces share one daemon, and a pid read in ANOTHER namespace names
// nothing here — a dead-looking pid there may be a live owner. A container
// whose owner-pidns is not ours, or absent, is never reported orphaned, even
// when its pid is dead in this namespace.
func TestFindOrphanedContainers_ForeignPIDNamespaceIsNeverJudged(t *testing.T) {
	foreign := labeledInfo("ctxloom-iso-agent-foreign", deadPid, oldEnough())
	foreign.Labels[labelOwnerPIDNS] = "pid:[4026532999]"
	absent := labeledInfo("ctxloom-iso-agent-absent", deadPid, oldEnough())
	delete(absent.Labels, labelOwnerPIDNS)
	for _, info := range []ContainerInfo{foreign, absent} {
		calls := stubReapProbeExec(t)
		found, err := FindOrphanedContainers(context.Background(), dockerWith(info))
		require.NoError(t, err)
		assert.Empty(t, found, info.Name)
		assert.Empty(t, *calls, "%s must never be touched", info.Name)
		c := classifyContainer(time.Now(), info)
		assert.Equal(t, pidalive.State(0), c.OwnerState, "%s: its pid is never probed here", info.Name)
	}
}

// TestFindOrphanedContainers_DeadOwnerIsReportedNotRemoved is the positive
// case: a ctxloom-iso- container, past the grace window, whose owner-pid
// label names a CONFIRMED dead process is reported as orphaned — and nothing
// is removed. The finder only answers the question; whoever acts on the
// answer owns that decision.
func TestFindOrphanedContainers_DeadOwnerIsReportedNotRemoved(t *testing.T) {
	calls := stubReapProbeExec(t)

	found, err := FindOrphanedContainers(context.Background(), dockerWith(labeledInfo("ctxloom-iso-agent-abc", deadPid, oldEnough())))

	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, "ctxloom-iso-agent-abc", found[0].Name)
	assert.Equal(t, deadPid, found[0].OwnerPID)
	assert.Equal(t, ContainerOrphaned, found[0].Verdict)
	assert.Empty(t, *calls, "finding an orphan must never remove it")
}

// TestFindOrphanedContainers_LiveOwnerIsNotReported: an otherwise-identical
// candidate whose owner-pid names THIS live test process is not an orphan.
func TestFindOrphanedContainers_LiveOwnerIsNotReported(t *testing.T) {
	calls := stubReapProbeExec(t)

	found, err := FindOrphanedContainers(context.Background(), dockerWith(labeledInfo("ctxloom-iso-agent-abc", os.Getpid(), oldEnough())))

	require.NoError(t, err)
	assert.Empty(t, found)
	assert.Empty(t, *calls)
}

// TestFindOrphanedContainers_AbsentOrUnparsableLabelIsNotReported covers both
// "no owner-pid label at all" and "owner-pid present but garbage" — ambiguity
// must never read as "no owner, an orphan".
func TestFindOrphanedContainers_AbsentOrUnparsableLabelIsNotReported(t *testing.T) {
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

			found, err := FindOrphanedContainers(context.Background(), dockerWith(ContainerInfo{Name: "ctxloom-iso-agent-abc", Labels: tc.labels}))

			require.NoError(t, err)
			assert.Empty(t, found)
			assert.Empty(t, *calls)
		})
	}
}

// TestFindOrphanedContainers_InsideGraceWindowIsNotReported: a confirmed-dead
// owner and a perfectly valid label pair is still not reported when
// created-at says the container is younger than containerReapGraceWindow —
// it may not be fully labelled/settled yet.
func TestFindOrphanedContainers_InsideGraceWindowIsNotReported(t *testing.T) {
	justCreated := time.Now().Add(-1 * time.Second).UTC().Format(time.RFC3339)

	found, err := FindOrphanedContainers(context.Background(), dockerWith(labeledInfo("ctxloom-iso-agent-abc", deadPid, justCreated)))

	require.NoError(t, err)
	assert.Empty(t, found)
}

// TestFindOrphanedContainers_NonPrefixNameIsNeverConsidered: a container that
// does not carry the ctxloom-iso- prefix is never reported, however perfectly
// it satisfies every other rule (dead owner, past the grace window, valid
// labels) — the name check is a hard boundary, not a tiebreak.
func TestFindOrphanedContainers_NonPrefixNameIsNeverConsidered(t *testing.T) {
	found, err := FindOrphanedContainers(context.Background(), dockerWith(labeledInfo("some-unrelated-container", deadPid, oldEnough())))

	require.NoError(t, err)
	assert.Empty(t, found)
}

// TestFindOrphanedContainers_EnumerateFailureIsAnError: a runtime that cannot
// list its containers has not shown there are none, so the failure reaches
// the caller instead of reading as an all-clear.
func TestFindOrphanedContainers_EnumerateFailureIsAnError(t *testing.T) {
	rt := dockerWith()
	rt.err = errReapEnumerateFixture

	found, err := FindOrphanedContainers(context.Background(), rt)

	require.ErrorIs(t, err, errReapEnumerateFixture)
	assert.Empty(t, found)
}

// errReapEnumerateFixture is the Enumerate failure the fake runtime reports.
var errReapEnumerateFixture = errors.New("daemon unreachable")

// TestFindOrphanedContainers_NilRuntimeFindsNothing: a caller with no
// available runtime gets no candidates and no error, not a panic.
func TestFindOrphanedContainers_NilRuntimeFindsNothing(t *testing.T) {
	found, err := FindOrphanedContainers(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, found)
}

// TestOwnerLabelArgs_StampsThisProcessAndARecentTimestamp pins ownerLabelArgs'
// contract directly: the pid label names THIS process (the one about to own
// the container) and the timestamp label parses as RFC3339 and is fresh —
// the two facts classifyContainer's safety rules depend on actually
// being true at `run` time, not just at read time.
func TestOwnerLabelArgs_StampsThisProcessAndARecentTimestamp(t *testing.T) {
	before := time.Now()
	args := ownerLabelArgs()
	after := time.Now()

	require.Len(t, args, 6)
	assert.Equal(t, "--label", args[0])
	assert.Equal(t, labelOwnerPID+"="+strconv.Itoa(os.Getpid()), args[1])
	assert.Equal(t, "--label", args[2])
	assert.Equal(t, []string{"--label", labelOwnerPIDNS + "=" + ownerPIDNamespace()}, args[4:6],
		"the pid is stamped with the namespace it is read in")

	createdRaw := args[3][len(labelCreatedAt)+1:]
	created, err := time.Parse(time.RFC3339, createdRaw)
	require.NoError(t, err)
	assert.False(t, created.Before(before.Add(-time.Second)), "created-at must not predate the call")
	assert.False(t, created.After(after.Add(time.Second)), "created-at must not postdate the call")
}
