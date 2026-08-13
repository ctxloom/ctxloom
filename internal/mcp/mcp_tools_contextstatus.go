package mcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/contextmetrics"
)

// contextStatusInput is context_status's argument surface: how much history to
// return, and nothing else. The session is never a parameter — the tool
// answers for the CALLER's own session, resolved from the identity every
// host-relay tool already carries (credential-derived on the coordinator's
// HTTP surface, env-derived on stdio), so a child asking "how full am I"
// cannot accidentally be told about its parent.
type contextStatusInput struct {
	Trend int `json:"trend,omitempty" jsonschema:"How many of the most recent samples to return as a trend, oldest first (default 10, maximum 100)"`
}

// contextStatusResult is what an agent gets back.
//
// Available is the field that carries the honesty. Latest is a POINTER and
// Trend omits itself when empty, so a session with no recorded samples returns
// no percentage at all rather than a zero one — the distinction the whole tool
// exists to preserve, since a 0 in this position reads as "plenty of room"
// when it means "nobody measured".
type contextStatusResult struct {
	Available bool                    `json:"available"`
	Message   string                  `json:"message"`
	Harp      string                  `json:"harp,omitempty"`
	Latest    *contextmetrics.Sample  `json:"latest,omitempty"`
	Trend     []contextmetrics.Sample `json:"trend,omitempty"`
}

const (
	// defaultContextTrend is how many samples a caller gets without asking.
	// Enough to see a direction, few enough that reading it costs less
	// context than the pressure it is being consulted about.
	defaultContextTrend = 10
	// maxContextTrend bounds the answer. A series can run to thousands of
	// samples over a long session, and a tool consulted BECAUSE context is
	// scarce must never be able to spend a large amount of it.
	maxContextTrend = 100
)

// noContextSamplesMsg is returned, verbatim, whenever the series is empty. It
// names the likely cause because the failure is otherwise invisible: capture
// happens in the statusline callback, which an engine without a
// command-backed statusline (or a session whose harness was never configured
// to run `ctxloom hook hud`) simply never invokes.
const noContextSamplesMsg = "no samples yet (statusline integration not active for this session?) — context occupancy is UNKNOWN for this session, which is not the same as low"

// noSessionIdentityMsg is the other honest empty answer: without a harp there
// is no session whose series could be read, so the tool reports that rather
// than reading someone else's.
const noSessionIdentityMsg = "no ctxloom session identity for this caller — context occupancy is UNKNOWN, which is not the same as low"

// contextStatusDesc is registered on BOTH the stdio server and the runner's
// host relay, so the two surfaces cannot describe the tool two ways.
const contextStatusDesc = "Measure how full this session's context window actually is, instead of estimating it. Returns the most recent recorded sample (percent used, tokens in the window, window size) plus a short trend of earlier samples so the DIRECTION is visible, not just the level. Call this the moment a conversation starts to feel long — before winding down, compacting, splitting work off to a subagent, or telling the user you are running low: this turns that hunch into a number. Samples are captured by ctxloom's statusline integration as the session runs. When no samples exist the tool says so explicitly and returns NO percentage — an absent measurement, never a zero one, because a reported 0% would be indistinguishable from an empty context."

// registerContextStatusTool wires context_status on the stdio server and
// returns the names it registered, matching the convention the cell-local
// registrar follows so the runner's route-classification check reads the
// registration itself rather than a second hand-written copy of it.
func (s *ctxServer) registerContextStatusTool(server *mcp.Server) []string {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "context_status",
			Description: contextStatusDesc,
		},
		s.handleContextStatus)
	return []string{"context_status"}
}

// handleContextStatus reads the caller's own context-occupancy series.
//
// Read-only and cheap by construction: one file read, no distillation, no
// backend, no subprocess — it is meant to be affordable to an agent that is
// already short of room, so it must never become the kind of tool you hesitate
// to call.
func (s *ctxServer) handleContextStatus(_ context.Context, _ *mcp.CallToolRequest, in contextStatusInput) (*mcp.CallToolResult, *contextStatusResult, error) {
	harp := s.self.Harp
	if harp == "" {
		return nil, &contextStatusResult{Available: false, Message: noSessionIdentityMsg}, nil
	}

	n := in.Trend
	if n <= 0 {
		n = defaultContextTrend
	}
	if n > maxContextTrend {
		n = maxContextTrend
	}

	samples, err := contextmetrics.Tail(harp, n)
	if err != nil {
		// A series that cannot be READ is a different fact from a series that
		// does not exist, and the caller is entitled to tell them apart:
		// contextmetrics.Read already treats absence as an empty result, so
		// anything reaching here is a real I/O failure and is reported as one.
		return nil, nil, fmt.Errorf("context_status: read metrics for %s: %w", harp, err)
	}
	if len(samples) == 0 {
		return nil, &contextStatusResult{Available: false, Message: noContextSamplesMsg, Harp: harp}, nil
	}

	latest := samples[len(samples)-1]
	return nil, &contextStatusResult{
		Available: true,
		Harp:      harp,
		Message: fmt.Sprintf("context %.0f%% used (%d of %d tokens) as of %s; %d sample(s) in trend",
			latest.ContextPct, latest.TokensUsed, latest.Window,
			latest.TS.Format("2006-01-02 15:04:05Z07:00"), len(samples)),
		Latest: &latest,
		Trend:  samples,
	}, nil
}
