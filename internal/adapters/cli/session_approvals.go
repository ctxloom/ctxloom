package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/displaysafe"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/errwriter"
)

// The command lives under `session`, not as `approvals list`: "approvals"
// names the recorded countersignatures of content trust, a different trust
// domain that already claims that noun.
var sessionApprovalsCmd = &cobra.Command{
	Use:   "approvals",
	Short: "List approvals waiting for the human, from any terminal (read-only)",
	Long: `Lists the requests children have parked for the root human's decision: who asked,
through which lineage, what for, how long it has waited and how long until it is
denied. This command cannot answer a request — answering happens only in the
terminal that started the run.

Every live coordinator on this host is asked: each one records its endpoint in
~/.ctxloom/coord/<project>/<root>/endpoint.json while it runs.`,
	Args: cobra.NoArgs,
	RunE: runSessionApprovals,
}

const (
	noPendingApprovals   = "no pending approvals"
	noCoordinatorRunning = "no coordinator is running"
	noCoordinatorAnswer  = "no coordinator answered"
)

// approvalsListResult is what `session approvals` emits: every request parked
// in every coordinator that answered, soonest deadline first.
type approvalsListResult struct {
	Approvals []approvalRow `json:"approvals"`
	// Coordinators is how many live coordinators answered.
	Coordinators int `json:"coordinators"`
}

// approvalRow is one parked request. Age and time left are computed from one
// clock read when the list is built.
type approvalRow struct {
	Project     string    `json:"project"`
	Harp        string    `json:"harp"`
	Agent       string    `json:"agent,omitempty"`
	Kind        string    `json:"kind"`
	Summary     string    `json:"summary"`
	Lineage     []string  `json:"lineage"`
	Since       time.Time `json:"since"`
	Deadline    time.Time `json:"deadline"`
	AgeSeconds  int64     `json:"age_seconds"`
	LeftSeconds int64     `json:"left_seconds"`
}

var approvalKindFromWire = map[agentcoordpb.ApprovalRequest_ApprovalKind]coord.ApprovalKind{
	agentcoordpb.ApprovalRequest_APPROVAL_KIND_TOOL:     coord.ApprovalTool,
	agentcoordpb.ApprovalRequest_APPROVAL_KIND_QUESTION: coord.ApprovalQuestion,
	agentcoordpb.ApprovalRequest_APPROVAL_KIND_PLAN:     coord.ApprovalPlan,
}

// runSessionApprovals asks every live coordinator on this host. A coordinator
// that is not there — none discovered, or one that exited after discovery
// found it (Unavailable) — has nothing pending, so that is a message naming
// where discovery looked, not a failure. The exit is non-zero only when a
// coordinator answered with an error (named by its project), or endpoint
// files could not be read and no coordinator answered.
func runSessionApprovals(cmd *cobra.Command, _ []string) error {
	coordinators, skipped := operations.DiscoverCoordinators()
	var answers []*agentcoordpb.PendingApprovalsResult
	var failures []error
	for _, c := range coordinators {
		res, err := operations.QueryPendingApprovals(cmd.Context(), c)
		switch {
		case err == nil:
			answers = append(answers, res)
		case status.Code(err) != codes.Unavailable:
			failures = append(failures, fmt.Errorf("%s: %w", c.ProjectDir, err))
		}
	}
	if len(answers) == 0 {
		failures = append(failures, skipped...)
	}
	res := buildApprovalsList(answers, time.Now())
	if err := emit(cmd, res, func() error {
		ew := errwriter.New(cmd.OutOrStdout())
		switch {
		case len(answers) == 0 && len(failures) > 0:
			ew.Println(noCoordinatorAnswer)
		case len(answers) == 0:
			ew.Println(noCoordinatorRunningIn())
		case len(res.Approvals) == 0:
			ew.Println(noPendingApprovals)
		default:
			return renderApprovals(cmd.OutOrStdout(), res)
		}
		return ew.Err()
	}); err != nil {
		return err
	}
	if len(failures) == 0 {
		return nil
	}
	ew := errwriter.New(cmd.ErrOrStderr())
	for _, err := range failures {
		ew.Println(err)
	}
	if err := ew.Err(); err != nil {
		return err
	}
	return &ExitError{Code: 1}
}

// noCoordinatorRunningIn says nothing is running, and where discovery
// looked. It is only reached when discovery reported nothing it could not
// read, so the state root resolved.
func noCoordinatorRunningIn() string {
	where, err := paths.HomeCoordDir()
	if err != nil {
		return noCoordinatorRunning
	}
	return noCoordinatorRunning + " (no live endpoint under " + where + ")"
}

// buildApprovalsList flattens the answers into rows, soonest deadline first.
func buildApprovalsList(answers []*agentcoordpb.PendingApprovalsResult, now time.Time) approvalsListResult {
	res := approvalsListResult{Approvals: []approvalRow{}, Coordinators: len(answers)}
	for _, a := range answers {
		for _, p := range a.GetPending() {
			since, deadline := p.GetSince().AsTime(), p.GetDeadline().AsTime()
			res.Approvals = append(res.Approvals, approvalRow{
				Project:     a.GetProjectDir(),
				Harp:        p.GetHarp(),
				Agent:       p.GetAgent(),
				Kind:        approvalKindFromWire[p.GetKind()].String(),
				Summary:     p.GetSummary(),
				Lineage:     p.GetLineage(),
				Since:       since,
				Deadline:    deadline,
				AgeSeconds:  int64(max(now.Sub(since), 0) / time.Second),
				LeftSeconds: int64(max(deadline.Sub(now), 0) / time.Second),
			})
		}
	}
	slices.SortStableFunc(res.Approvals, func(a, b approvalRow) int { return a.Deadline.Compare(b.Deadline) })
	return res
}

// renderApprovals is the text table; it names each row's project only when
// more than one coordinator answered. Every value is rendered through
// displaysafe.Text: the summary arrives raw (the child's own characters), and
// nothing a terminal would act on may reach it.
func renderApprovals(w io.Writer, res approvalsListResult) error {
	ew := errwriter.New(w)
	tw := tabwriter.NewWriter(ew, 0, 0, 2, ' ', 0)
	header := "LEFT\tAGE\tKIND\tASKER\tLINEAGE\tSUMMARY"
	if res.Coordinators > 1 {
		header = "PROJECT\t" + header
	}
	_, _ = fmt.Fprintln(tw, header)
	for _, r := range res.Approvals {
		asker := r.Harp
		if r.Agent != "" {
			asker += " (" + r.Agent + ")"
		}
		cells := []string{mmss(r.LeftSeconds), mmss(r.AgeSeconds), r.Kind, asker, strings.Join(r.Lineage, "→"), r.Summary}
		if res.Coordinators > 1 {
			cells = append([]string{r.Project}, cells...)
		}
		for i, c := range cells {
			cells[i] = displaysafe.Text(c, false)
		}
		_, _ = fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	return ew.Err()
}

// mmss is a count of seconds as the approval overlay's countdown shows one.
func mmss(secs int64) string {
	return fmt.Sprintf("%02d:%02d", secs/60, secs%60)
}
