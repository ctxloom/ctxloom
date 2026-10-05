package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// runApprovals runs `session approvals` in format, returning stdout, stderr
// and the command's error.
func runApprovals(t *testing.T, format string) (string, string, error) {
	t.Helper()
	cmd, out := formatCmd(format)
	cmd.SetContext(context.Background())
	var errOut bytes.Buffer
	cmd.SetErr(&errOut)
	err := runSessionApprovals(cmd, nil)
	return out.String(), errOut.String(), err
}

func pendingFixture(now time.Time) *agentcoordpb.PendingApprovalsResult {
	return &agentcoordpb.PendingApprovalsResult{
		ProjectDir: "/proj",
		Pending: []*agentcoordpb.PendingApprovalsResult_Pending{{
			Harp: "child-a", Agent: "worker",
			Lineage: []string{"root", "child-a"}, Summary: "Bash: make",
			Since: timestamppb.New(now.Add(-2 * time.Minute)), Deadline: timestamppb.New(now.Add(13 * time.Minute)),
		}},
	}
}

// TestSessionApprovals_ListsFromALiveCoordinator: a request parked in a
// coordinator another process runs is listed here, over its
// ConsumerService, in the structured form every list command emits.
func TestSessionApprovals_ListsFromALiveCoordinator(t *testing.T) {
	home := testsupport.Isolate(t)
	f := newFakeConsumerServer()
	f.approvals = pendingFixture(time.Now())
	startFakeCoordinator(t, home, f)

	out, _, err := runApprovals(t, formatJSON)
	require.NoError(t, err)
	var got approvalsListResult
	require.NoError(t, json.Unmarshal([]byte(out), &got), out)
	assert.Equal(t, 1, got.Coordinators)
	require.Len(t, got.Approvals, 1)
	a := got.Approvals[0]
	assert.Equal(t, "/proj", a.Project)
	assert.Equal(t, "child-a", a.Harp)
	assert.Equal(t, "worker", a.Agent)
	assert.Equal(t, "Bash: make", a.Summary)
	assert.Equal(t, []string{"root", "child-a"}, a.Lineage)
	assert.InDelta(t, 120, a.AgeSeconds, 5)
	assert.InDelta(t, 13*60, a.LeftSeconds, 5)
}

// TestSessionApprovals_Text: the human form names who asked, through whom,
// what for and how long is left — and no PROJECT column for one coordinator.
func TestSessionApprovals_Text(t *testing.T) {
	home := testsupport.Isolate(t)
	f := newFakeConsumerServer()
	f.approvals = pendingFixture(time.Now())
	startFakeCoordinator(t, home, f)

	out, _, err := runApprovals(t, formatText)
	require.NoError(t, err)
	for _, want := range []string{"LEFT", "AGE", "ASKER", "LINEAGE", "SUMMARY", "child-a (worker)", "root→child-a", "Bash: make"} {
		assert.Contains(t, out, want)
	}
	assert.NotContains(t, out, "PROJECT")
}

// TestSessionApprovals_Empty: a live coordinator with nothing parked is a
// clean "none", and an empty list — not null — in structured form.
func TestSessionApprovals_Empty(t *testing.T) {
	home := testsupport.Isolate(t)
	startFakeCoordinator(t, home, newFakeConsumerServer())

	out, _, err := runApprovals(t, formatText)
	require.NoError(t, err)
	assert.Contains(t, out, noPendingApprovals)

	out, _, err = runApprovals(t, formatJSON)
	require.NoError(t, err)
	assert.JSONEq(t, `{"approvals":[],"coordinators":1}`, out)
}

// TestSessionApprovals_NoCoordinator: nothing running is the ordinary state
// between runs — a plain message and exit 0, nothing pending.
func TestSessionApprovals_NoCoordinator(t *testing.T) {
	testsupport.Isolate(t)
	out, errOut, err := runApprovals(t, formatText)
	require.NoError(t, err)
	assert.Contains(t, out, noCoordinatorRunning)
	where, err := paths.HomeCoordDir()
	require.NoError(t, err)
	assert.Contains(t, out, where, "it says where it looked")
	assert.Empty(t, errOut)
}

// TestSessionApprovals_HelpNamesWhereItLooks: the help says where
// coordinators are discovered and that nothing here answers a request.
func TestSessionApprovals_HelpNamesWhereItLooks(t *testing.T) {
	assert.Contains(t, sessionApprovalsCmd.Long, "~/.ctxloom/coord/")
	assert.Contains(t, sessionApprovalsCmd.Long, "endpoint.json")
	assert.Contains(t, sessionApprovalsCmd.Long, "cannot answer")
}

// TestSessionApprovals_DeadEndpointIsNoCoordinator: an endpoint whose
// coordinator no longer listens is a coordinator that is not running, not
// a failure.
func TestSessionApprovals_DeadEndpointIsNoCoordinator(t *testing.T) {
	home := testsupport.Isolate(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	writeLiveEndpoint(t, home, port)

	out, errOut, err := runApprovals(t, formatText)
	require.NoError(t, err)
	assert.Contains(t, out, noCoordinatorRunning)
	assert.Empty(t, errOut)
}

// TestSessionApprovals_RefusalExitsNonZero: a coordinator that answered
// with an error is a real failure — named on stderr, a non-zero exit.
func TestSessionApprovals_RefusalExitsNonZero(t *testing.T) {
	home := testsupport.Isolate(t)
	f := newFakeConsumerServer()
	f.approvalsErr = status.Error(codes.PermissionDenied, "refused")
	startFakeCoordinator(t, home, f)

	_, errOut, err := runApprovals(t, formatText)
	var exit *ExitError
	require.True(t, errors.As(err, &exit), "want an ExitError, got %v", err)
	assert.Equal(t, 1, exit.Code)
	assert.Contains(t, errOut, "PermissionDenied")
	assert.Contains(t, errOut, fakeProjectDir, "a failing coordinator is named by its project")
}

// TestRenderApprovals_ProjectColumnWithSeveralCoordinators: rows from more
// than one coordinator say which project each came from.
func TestRenderApprovals_ProjectColumnWithSeveralCoordinators(t *testing.T) {
	res := approvalsListResult{Coordinators: 2, Approvals: []approvalRow{
		{Project: "/one", Harp: "a", Summary: "x", Lineage: []string{"r", "a"}, LeftSeconds: 61, AgeSeconds: 5},
		{Project: "/two", Harp: "b", Summary: "y", Lineage: []string{"r", "b"}, LeftSeconds: 3600, AgeSeconds: 7},
	}}
	var buf bytes.Buffer
	require.NoError(t, renderApprovals(&buf, res))
	out := buf.String()
	assert.Contains(t, out, "PROJECT")
	assert.Contains(t, out, "/one")
	assert.Contains(t, out, "/two")
	assert.Contains(t, out, "01:01")
	assert.Less(t, strings.Index(out, "/one"), strings.Index(out, "/two"))
}

// TestSessionApprovals_SummaryRawInJSONMarkedInText: the summary arrives raw;
// a program reading --format json gets the child's characters as they are,
// and a terminal gets them made visible — a bidi override can neither
// reorder the line a human reads nor turn into a marker a script parses.
func TestSessionApprovals_SummaryRawInJSONMarkedInText(t *testing.T) {
	home := testsupport.Isolate(t)
	f := newFakeConsumerServer()
	f.approvals = pendingFixture(time.Now())
	f.approvals.Pending[0].Summary = "Bash: ls \u202egnp.exe"
	f.approvals.Pending[0].Agent = "work\u202eer"
	startFakeCoordinator(t, home, f)

	out, _, err := runApprovals(t, formatJSON)
	require.NoError(t, err)
	var got approvalsListResult
	require.NoError(t, json.Unmarshal([]byte(out), &got), out)
	require.Len(t, got.Approvals, 1)
	assert.Equal(t, "Bash: ls \u202egnp.exe", got.Approvals[0].Summary, "JSON carries the raw text")
	assert.NotContains(t, out, "⟨U+202E⟩", "no marker reaches JSON")

	out, _, err = runApprovals(t, formatText)
	require.NoError(t, err)
	assert.Contains(t, out, "Bash: ls ⟨U+202E⟩gnp.exe")
	assert.Contains(t, out, "work⟨U+202E⟩er")
	assert.NotContains(t, out, "\u202e", "no bidi override reaches the terminal")
}
