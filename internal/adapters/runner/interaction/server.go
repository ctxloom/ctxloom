package interaction

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/mcpschema"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/plans"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/version"
)

// LocalSurface is the cell-local half of the endpoint's surface — the
// context tools and the ctxloom:// resources — registered on the server and
// answering for the names it returns, which the routing table must classify
// cell-local. The runner serves it off the Loadout's Package and Index; the
// plugin-hosted owner arm's socket endpoint (adapters/mcp, until the plugin
// arm dies) serves it off the process config.
type LocalSurface interface {
	Register(server *mcp.Server) (toolNames []string)
}

// NewServer assembles one session's MCP surface per the routing table
// (mcpschema.Routes): the local surface, the host-relayed tools as
// HostRequest frames on the runner's reach-back Home, and the proto-canonical
// coordination and artifact-fetch tools. Completeness is a STARTUP
// invariant: every tool registered here must be classified, and every
// classified tool must be served by exactly the route the table names — a
// mismatch errors the runner up front, never a silent fallthrough.
//
// harp names the session (the server's Title and the identity every
// coordination frame speaks as); cwd is the cell's working directory — the
// cell-path boundary agent_report and agent_fetch_artifact confine
// themselves to (resolveCellPath). leaf withholds the coordinator-only tools
// (mcpschema.CoordinatorOnlyTools): a one-shot run, or one at the
// delegation-depth cap, holding an agent_recv inbox plus a roster would
// infer it has children and stall waiting on notifications that never
// arrive. It is the launch identity's Leaf, decided by the coordinator that
// minted it — the runner holds no config to compute it from. wake serves the
// session relay's subscription to WakeURI.
func NewServer(rep report.Reporter, home *runner.Home, harp, cwd string, leaf bool, local LocalSurface, wake *WakeSignal) (*mcp.Server, error) {
	opts := &mcp.ServerOptions{Instructions: operations.SessionInstructions(harp)}
	wake.options(opts)
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "ctxloom",
		Title:   harp,
		Version: version.Version,
	}, opts)
	wake.serve(server)

	routes := mcpschema.Routes()
	registered := map[string]bool{}

	if err := claimRoutes(routes, registered, mcpschema.RouteCellLocal, local.Register(server)...); err != nil {
		return nil, err
	}

	if err := claimRoutes(routes, registered, mcpschema.RouteHostRelay, registerHostRelays(server, home)...); err != nil {
		return nil, err
	}

	if err := registerGeneratedTools(rep, server, home, harp, cwd, leaf, routes, registered); err != nil {
		return nil, err
	}

	// Exhaustiveness: nothing classified may be missing from this surface.
	for name := range routes {
		if !registered[name] {
			return nil, fmt.Errorf("runner MCP: classified tool %q is not served by any route — fix the registration or mcpschema.Routes", name)
		}
	}
	return server, nil
}

// claimRoutes asserts that every name a registrar just served is classified as
// want, and marks it served for the exhaustiveness check. The names come FROM
// the registrars (which return what they registered) rather than from a second
// hand-written list beside them — a list that could only ever drift from the
// registrations it was meant to describe.
func claimRoutes(routes map[string]mcpschema.Route, registered map[string]bool, want mcpschema.Route, names ...string) error {
	for _, name := range names {
		if routes[name] != want {
			return fmt.Errorf("runner MCP: tool %q is served on the %s route but classified otherwise — fix mcpschema.Routes", name, routeName(want))
		}
		registered[name] = true
	}
	return nil
}

// routeName renders a Route for a startup-failure message. mcpschema.Route is
// an int enum with no String method of its own, and a bare integer in the one
// error a runner dies on tells the reader nothing.
func routeName(r mcpschema.Route) string {
	switch r {
	case mcpschema.RouteCoordination:
		return "coordination"
	case mcpschema.RouteCellLocal:
		return "cell-local"
	case mcpschema.RouteHostRelay:
		return "host-relay"
	case mcpschema.RouteArtifactFetch:
		return "artifact-fetch"
	default:
		return fmt.Sprintf("route(%d)", int(r))
	}
}

// registerHostRelays adds the host-resident tools as HostRequest relays and
// returns the names it registered. Each carries the typed input and the
// description operations declares as the tool's contract, so this endpoint
// and the stdio server cannot describe one tool two ways.
func registerHostRelays(server *mcp.Server, home *runner.Home) []string {
	return []string{
		addHostRelay[operations.CompactSessionInput](server, home, "compact_session", operations.CompactSessionDesc),
		addHostRelay[operations.LoadSessionInput](server, home, "load_session", operations.LoadSessionDesc),
		addHostRelay[operations.RecoverSessionInput](server, home, "recover_session", operations.RecoverSessionDesc),
		addHostRelay[operations.GetPreviousSessionInput](server, home, "get_previous_session", operations.GetPreviousSessionDesc),
		addHostRelay[operations.ListSessionsInput](server, home, "list_sessions", operations.ListSessionsDesc),
		addHostRelay[operations.EvaluateTriggersInput](server, home, "evaluate_triggers", operations.EvaluateTriggersDesc),
		addHostRelay[operations.ContextStatusInput](server, home, "context_status", operations.ContextStatusDesc),
	}
}

// addHostRelay registers one host-resident tool and returns its name, so the
// name is written once per tool rather than once in the registration and again
// in a classification list.
func addHostRelay[In any](server *mcp.Server, home *runner.Home, name, desc string) string {
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: desc}, relayTyped[In](home, name))
	return name
}

// registerGeneratedTools adds the proto-canonical tools: coordination frames
// AND artifact-fetch both draw their schemas from mcpschema.Tools() and differ
// only in which handler builder serves them (Binding.Route).
func registerGeneratedTools(rep report.Reporter, server *mcp.Server, home *runner.Home, harp, cwd string, leaf bool, routes map[string]mcpschema.Route, registered map[string]bool) error {
	tools, err := mcpschema.Tools()
	if err != nil {
		return err
	}
	for _, spec := range tools {
		// The routing table decides where this tool's arguments go, and
		// mcpschema.Route's zero value is RouteCoordination — so a bare
		// lookup cannot tell a MISSING classification from a deliberate
		// coordination one. Asked explicitly, before the leaf gate: a tool a
		// leaf withholds is still part of the surface this runner vouches
		// for.
		route, classified := routes[spec.Name]
		if !classified {
			return fmt.Errorf("runner MCP: generated tool %q is not classified in mcpschema.Routes — classify it or drop its binding", spec.Name)
		}
		// Trust-boundary gate: a LEAF session must not receive the
		// coordinator-only tools (mcpschema.CoordinatorOnlyTools) — a leaf
		// holding an agent_recv inbox plus a
		// roster infers it has children and stalls waiting for notifications
		// that never arrive. Still marked registered (deliberately withheld),
		// or the caller's exhaustiveness check fails runner startup;
		// agent_send/agent_recv/agent_report (parent reporting) are untouched
		// by this gate.
		if leaf && mcpschema.CoordinatorOnlyTools()[spec.Name] {
			registered[spec.Name] = true
			continue
		}
		h, herr := generatedToolHandler(rep, home, harp, cwd, route, spec.Name, leaf)
		if herr != nil {
			return herr
		}
		tool := &mcp.Tool{
			Name:        spec.Name,
			Description: spec.Description,
			InputSchema: json.RawMessage(spec.InputSchema),
		}
		if len(spec.OutputSchema) > 0 {
			tool.OutputSchema = json.RawMessage(spec.OutputSchema)
		}
		server.AddTool(tool, h)
		registered[spec.Name] = true
	}
	return nil
}

// generatedToolHandler picks the handler builder one generated tool's route
// names. An unclassified tool is a startup error, never a silent fallthrough.
func generatedToolHandler(rep report.Reporter, home *runner.Home, harp, cwd string, route mcpschema.Route, name string, leaf bool) (mcp.ToolHandler, error) {
	switch route {
	case mcpschema.RouteCoordination:
		return coordinationHandler(rep, home, harp, cwd, name, leaf)
	case mcpschema.RouteArtifactFetch:
		return artifactFetchHandler(home, cwd, name)
	default:
		return nil, fmt.Errorf("runner MCP: generated tool %q is not classified as coordination or artifact-fetch — fix mcpschema.Routes", name)
	}
}

// relayTyped forwards one host-resident tool over the RunChannel as
// CustomRequest{ctxloom/<tool>} with the SAME typed input the stdio server
// registers (identical advertised schema), handing the coordinator's Struct
// result back as the tool's structured content.
func relayTyped[In any](home *runner.Home, name string) mcp.ToolHandlerFor[In, map[string]any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, map[string]any, error) {
		raw, err := json.Marshal(in)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: encode arguments: %w", name, err)
		}
		args := &structpb.Struct{}
		if err := protojson.Unmarshal(raw, args); err != nil {
			return nil, nil, fmt.Errorf("%s: encode arguments: %w", name, err)
		}
		// A distillation is minutes of honest work; the harness hands us a
		// deadline-free ctx, so without this the request would inherit the
		// coordination-frame default and fail mid-flight on every long
		// session while the host carried on distilling behind it.
		if budget := mcpschema.RelayBudget(name); budget > 0 {
			if _, has := ctx.Deadline(); !has {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, budget)
				defer cancel()
			}
		}
		resp, err := home.Request(ctx, &agentcoordpb.AgentRequest{
			Kind: &agentcoordpb.AgentRequest_Host{Host: &agentcoordpb.HostRequest{Tool: name, Args: args}},
		})
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", name, err)
		}
		if st := resp.GetStatus(); st.GetCode() != int32(codes.OK) {
			return nil, nil, errors.New(st.GetMessage())
		}
		rawOut, err := protojson.Marshal(resp.GetHost().GetBody())
		if err != nil {
			return nil, nil, fmt.Errorf("%s: decode result: %w", name, err)
		}
		var structured map[string]any
		if err := json.Unmarshal(rawOut, &structured); err != nil {
			return nil, nil, fmt.Errorf("%s: decode result: %w", name, err)
		}
		return nil, structured, nil
	}
}

// coordinationHandler builds the handler for one generated coordination
// tool: protojson-decode the args into the bound contract message (both
// snake_case and camelCase accepted), run the plane-2 exchange (or the
// runner-local recv/report), and project the result back with proto names.
func coordinationHandler(rep report.Reporter, home *runner.Home, harp, cwd, name string, leaf bool) (mcp.ToolHandler, error) {
	switch name {
	case mcpschema.ToolAgentRecv:
		return RecvHandler(rep, home, leaf), nil
	case mcpschema.ToolAgentReport:
		return reportHandler(rep, home, harp, cwd), nil
	}
	if h, ok := requestToolHandler(home, name); ok {
		return h, nil
	}
	if h, ok := controlVerbHandler(home, name); ok {
		return h, nil
	}
	return nil, fmt.Errorf("runner MCP: no handler for generated tool %q — extend coordinationHandler alongside the binding table", name)
}

// requestToolHandler is the handler for a plane-2 request tool (agent_run,
// agent_send, agent_stop, roster); false for any other tool.
func requestToolHandler(home *runner.Home, name string) (mcp.ToolHandler, bool) {
	switch name {
	case mcpschema.ToolAgentRun:
		return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var m agentcoordpb.SpawnAgentRequest
			if err := unmarshalArgs(req, &m); err != nil {
				return nil, fmt.Errorf("agent_run: %w", err)
			}
			resp, err := home.Request(ctx, &agentcoordpb.AgentRequest{Kind: &agentcoordpb.AgentRequest_SpawnAgent{SpawnAgent: &m}})
			if err != nil {
				return nil, fmt.Errorf("agent_run: %w", err)
			}
			return coordinationResult(resp, resp.GetSpawnAgent())
		}, true
	case mcpschema.ToolAgentSend:
		return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var m agentcoordpb.PeerSendRequest
			if err := unmarshalArgs(req, &m); err != nil {
				return nil, fmt.Errorf("agent_send: %w", err)
			}
			resp, err := home.Request(ctx, &agentcoordpb.AgentRequest{Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &m}})
			if err != nil {
				return nil, fmt.Errorf("agent_send: %w", err)
			}
			return coordinationResult(resp, resp.GetPeerSend())
		}, true
	case mcpschema.ToolAgentStop:
		return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var m agentcoordpb.StopRun
			if err := unmarshalArgs(req, &m); err != nil {
				return nil, fmt.Errorf("agent_stop: %w", err)
			}
			resp, err := home.Request(ctx, &agentcoordpb.AgentRequest{Kind: &agentcoordpb.AgentRequest_StopRun{StopRun: &m}})
			if err != nil {
				return nil, fmt.Errorf("agent_stop: %w", err)
			}
			return coordinationResult(resp, resp.GetStopRun())
		}, true
	case mcpschema.ToolRoster:
		return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var m agentcoordpb.ListRunsRequest
			if err := unmarshalArgs(req, &m); err != nil {
				return nil, fmt.Errorf("roster: %w", err)
			}
			resp, err := home.Request(ctx, &agentcoordpb.AgentRequest{Kind: &agentcoordpb.AgentRequest_ListRuns{ListRuns: &m}})
			if err != nil {
				return nil, fmt.Errorf("roster: %w", err)
			}
			return coordinationResult(resp, resp.GetListRuns())
		}, true
	default:
		return nil, false
	}
}

// controlVerbHandler is the handler for a control-verb tool (steer, ask,
// summarize, pause, resume); false for any other tool.
func controlVerbHandler(home *runner.Home, name string) (mcp.ToolHandler, bool) {
	switch name {
	case mcpschema.ToolAgentSteer:
		return controlToolHandler(home, name,
			func(m *agentcoordpb.ControlSteer) *agentcoordpb.ControlRun {
				return &agentcoordpb.ControlRun{Verb: &agentcoordpb.ControlRun_Steer{Steer: m}}
			},
			func(r *agentcoordpb.ControlRunResult) proto.Message { return r.GetSteer() }), true
	case mcpschema.ToolAgentAsk:
		return controlToolHandler(home, name,
			func(m *agentcoordpb.ControlQuestion) *agentcoordpb.ControlRun {
				return &agentcoordpb.ControlRun{Verb: &agentcoordpb.ControlRun_Question{Question: m}}
			},
			func(r *agentcoordpb.ControlRunResult) proto.Message { return r.GetQuestion() }), true
	case mcpschema.ToolAgentSummarize:
		return controlToolHandler(home, name,
			func(m *agentcoordpb.ControlSummarize) *agentcoordpb.ControlRun {
				return &agentcoordpb.ControlRun{Verb: &agentcoordpb.ControlRun_Summarize{Summarize: m}}
			},
			func(r *agentcoordpb.ControlRunResult) proto.Message { return r.GetSummarize() }), true
	case mcpschema.ToolAgentPause:
		return controlToolHandler(home, name,
			func(m *agentcoordpb.ControlPause) *agentcoordpb.ControlRun {
				return &agentcoordpb.ControlRun{Verb: &agentcoordpb.ControlRun_Pause{Pause: m}}
			},
			func(r *agentcoordpb.ControlRunResult) proto.Message { return r.GetPause() }), true
	case mcpschema.ToolAgentResume:
		return controlToolHandler(home, name,
			func(m *agentcoordpb.ControlResume) *agentcoordpb.ControlRun {
				return &agentcoordpb.ControlRun{Verb: &agentcoordpb.ControlRun_Resume{Resume: m}}
			},
			func(r *agentcoordpb.ControlRunResult) proto.Message { return r.GetResume() }), true
	default:
		return nil, false
	}
}

// controlToolHandler builds the handler for one control tool. The arguments
// decode into the verb's OWN message (the tool's generated input schema),
// ride the wire as that arm of ControlRun, and the matching arm of
// ControlRunResult is the structured result — one exchange shape for all
// five verbs, so a sixth cannot be served by a copy that drifts.
//
// arm wraps the decoded message as its ControlRun arm; pick selects the
// answering arm (a nil pick is an empty structured result, which
// coordinationResult already handles).
func controlToolHandler[M any, PM interface {
	*M
	proto.Message
}](home *runner.Home, name string, arm func(PM) *agentcoordpb.ControlRun, pick func(*agentcoordpb.ControlRunResult) proto.Message) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		m := PM(new(M))
		if err := unmarshalArgs(req, m); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if budget := controlWireBudget(name); budget > 0 {
			if _, has := ctx.Deadline(); !has {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, budget)
				defer cancel()
			}
		}
		resp, err := home.Request(ctx, &agentcoordpb.AgentRequest{Kind: &agentcoordpb.AgentRequest_ControlRun{ControlRun: arm(m)}})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		return coordinationResult(resp, pick(resp.GetControlRun()))
	}
}

// controlWireBudget is how long one control tool's plane-2 request may take
// when the harness hands the handler a deadline-free ctx, or zero to keep
// Home.Request's default. Only the two ASKS outrun it: they block for the
// child's cooperative answer, and the coordinator's own verdict on an
// unanswered one must arrive before the wire gives up (coord.AskWireBudget).
// Steer, pause and resume are mechanical and keep the default so a wedged
// runner fails fast.
func controlWireBudget(name string) time.Duration {
	switch name {
	case mcpschema.ToolAgentAsk, mcpschema.ToolAgentSummarize:
		return coord.AskWireBudget
	default:
		return 0
	}
}

// unmarshalArgs decodes tool arguments into the bound proto message.
// protojson accepts both proto (snake_case) and camelCase field names —
// projection rule (a)'s runtime half.
func unmarshalArgs(req *mcp.CallToolRequest, m proto.Message) error {
	if len(req.Params.Arguments) == 0 {
		return nil
	}
	if err := protojson.Unmarshal(req.Params.Arguments, m); err != nil {
		return fmt.Errorf("arguments do not match the tool schema: %w", err)
	}
	return nil
}

// coordinationResult projects a plane-2 response onto the MCP result: a
// non-OK status is the tool error (its message names the conflict), the
// result message becomes structured content (proto names), and the status
// message rides as human-readable text content.
func coordinationResult(resp *agentcoordpb.CoordinatorResponse, result proto.Message) (*mcp.CallToolResult, error) {
	st := resp.GetStatus()
	if st.GetCode() != int32(codes.OK) {
		return nil, errors.New(st.GetMessage())
	}
	out := &mcp.CallToolResult{}
	if msg := st.GetMessage(); msg != "" {
		out.Content = []mcp.Content{&mcp.TextContent{Text: msg}}
	}
	if result != nil && !protoIsNil(result) {
		raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(result)
		if err != nil {
			return nil, fmt.Errorf("encode result: %w", err)
		}
		var structured any
		if err := json.Unmarshal(raw, &structured); err != nil {
			return nil, fmt.Errorf("encode result: %w", err)
		}
		out.StructuredContent = structured
	}
	return out, nil
}

// protoIsNil guards typed-nil proto results inside the oneof accessors.
func protoIsNil(m proto.Message) bool {
	return m == nil || !m.ProtoReflect().IsValid()
}

// RecvHandler is the runner-LOCAL agent_recv: park against the Home's
// notice buffer. Returned messages stay tentative at the coordinator until
// the NEXT recv (cursor-ack) or a clean runner shutdown acknowledges them —
// the go-sdk streamable server runs tool handlers on session-scoped
// contexts and holds POST streams open, so there is no per-response write
// hook to ack on; a crash before the ack re-delivers (at-least-once).
// leaf selects the timeout verdict (see recvOutcome): a child gets an error
// telling it to finish, a coordinator a successful empty receive.
func RecvHandler(rep report.Reporter, home *runner.Home, leaf bool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in struct {
			Wait int `json:"wait"`
		}
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
				return nil, fmt.Errorf("agent_recv: %w", err)
			}
		}
		wait := mcpschema.ClampRecvWait(in.Wait)
		msgs, err := home.Recv(ctx, wait)
		if err != nil {
			// Role, not transport, picks the verdict shape; recvOutcome
			// holds the leaf/coordinator asymmetry and the reason it must
			// stay.
			disposition, failure := RecvOutcome(err, wait, leaf)
			if failure != nil {
				return nil, failure
			}
			return &mcp.CallToolResult{StructuredContent: map[string]any{
				"messages":    []any{},
				"disposition": disposition,
			}}, nil
		}
		// home.Recv already committed msgs as RETURNED (the
		// cursor-ack fires on the NEXT Recv) before this loop even starts —
		// a message that fails to marshal/decode here is gone for good, not
		// redelivered. The old code silently `continue`d, so an all-fail
		// batch answered {"messages": []}, a successful call with zero
		// payload, structurally the same consume-then-decode shape as the
		// confirmed approval-reply defect. Never silently dropped now: a
		// failure is logged AND named in the result, so the caller can tell
		// "genuinely nothing waiting" from "N messages existed and were
		// lost to a decode failure".
		items := make([]any, 0, len(msgs))
		var dropped []string
		for _, m := range msgs {
			raw, merr := protojson.MarshalOptions{UseProtoNames: true}.Marshal(m)
			if merr != nil {
				dropped = append(dropped, m.GetMessageId())
				rep.Warnf("agent_recv: message %s: marshal: %v (already acked as returned — dropped, not redelivered)", m.GetMessageId(), merr)
				continue
			}
			var v any
			if uerr := json.Unmarshal(raw, &v); uerr != nil {
				dropped = append(dropped, m.GetMessageId())
				rep.Warnf("agent_recv: message %s: decode: %v (already acked as returned — dropped, not redelivered)", m.GetMessageId(), uerr)
				continue
			}
			items = append(items, v)
		}
		result := map[string]any{"messages": items}
		if len(dropped) > 0 {
			result["dropped_message_ids"] = dropped
		}
		return &mcp.CallToolResult{StructuredContent: result}, nil
	}
}

// reportHandler is agent_report: file the Summary (and auto-stamped plan
// manifests + any explicitly published files) as plane-1 events, returning
// once the coordinator's Ack covers them (durably journaled). E1c upgrade:
// bytes are UPLOADED via ArtifactTransferService BEFORE the manifest fact is
// filed — the ArtifactProduced fact carries upload_id (+ sha256), path stays
// a label, never the transfer mechanism (manifests can no longer dangle).
func reportHandler(rep report.Reporter, home *runner.Home, harp, cwd string) mcp.ToolHandler {
	stamper := &artifactStamper{harp: harp}
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var summary agentcoordpb.Summary
		if err := unmarshalArgs(req, &summary); err != nil {
			return nil, fmt.Errorf("agent_report: %w", err)
		}
		if err := validateReportSummary(&summary); err != nil {
			return nil, err
		}
		artifacts, stampFailures := stampPlans(ctx, rep, home, stamper)
		declared, err := publishDeclared(ctx, home, stamper, cwd, summary.GetPublishPaths())
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, declared...)
		for _, a := range artifacts {
			summary.ArtifactIds = append(summary.ArtifactIds, a.GetArtifactId())
		}
		if err := home.Report(ctx, &summary, artifacts); err != nil {
			return nil, fmt.Errorf("agent_report: %w", err)
		}
		return reportResult(artifacts, stampFailures), nil
	}
}

// validateReportSummary requires a report body and a scope.
func validateReportSummary(summary *agentcoordpb.Summary) error {
	if summary.GetText() == "" {
		return errors.New("agent_report: text is required (the report body)")
	}
	if summary.GetScope() == agentcoordpb.Summary_SCOPE_UNSPECIFIED {
		return errors.New("agent_report: scope is required (SCOPE_PROGRESS | SCOPE_CHECKPOINT | SCOPE_FINAL | SCOPE_STEP)")
	}
	return nil
}

// stampPlans auto-stamps the session dir's *.plan.md files, best-effort per
// file, returning what was published and everything it could not deliver.
// A failure here must not block the report — these files are not something
// the agent asked for THIS call — but it must be said: a report journaled
// with an empty artifact list otherwise reads as one that simply had no
// plans to stamp.
func stampPlans(ctx context.Context, rep report.Reporter, home *runner.Home, stamper *artifactStamper) ([]*agentcoordpb.ArtifactProduced, []string) {
	var artifacts []*agentcoordpb.ArtifactProduced
	var stampFailures []string
	cands, cerr := stamper.planCandidates()
	if cerr != nil {
		rep.Warnf("agent_report: plan discovery: %v", cerr)
		stampFailures = append(stampFailures, fmt.Sprintf("plan discovery: %v", cerr))
	}
	for _, cand := range cands {
		a, perr := stamper.publish(ctx, home, cand)
		if perr != nil {
			rep.Warnf("agent_report: plan stamp %s: %v", cand.absPath, perr)
			stampFailures = append(stampFailures, fmt.Sprintf("%s: %v", cand.absPath, perr))
			continue
		}
		if a != nil {
			artifacts = append(artifacts, a)
		}
	}
	return artifacts, stampFailures
}

// publishDeclared publishes the files the agent DECLARED in publish_paths.
// Unlike plan auto-discovery, a failure here is FAIL LOUD — the agent
// explicitly asked to publish a specific file; silently dropping it would be
// a correctness bug, not a degrade-gracefully case.
func publishDeclared(ctx context.Context, home *runner.Home, stamper *artifactStamper, cwd string, paths []string) ([]*agentcoordpb.ArtifactProduced, error) {
	var artifacts []*agentcoordpb.ArtifactProduced
	for _, rel := range paths {
		abs, perr := resolveCellPath(cwd, rel)
		if perr != nil {
			return nil, fmt.Errorf("agent_report: publish_paths %q: %w", rel, perr)
		}
		cand := artifactCandidate{
			artifactID: "file/" + rel,
			name:       filepath.Base(rel),
			mediaType:  mimeByExt(rel),
			kind:       agentcoordpb.ArtifactKind_ARTIFACT_KIND_OTHER,
			absPath:    abs,
		}
		a, perr := stamper.publish(ctx, home, cand)
		if perr != nil {
			return nil, fmt.Errorf("agent_report: publish_paths %q: %w", rel, perr)
		}
		if a != nil {
			artifacts = append(artifacts, a)
		}
	}
	return artifacts, nil
}

// reportResult projects agent_report's outcome onto the MCP result.
//
// journaled/artifact_ids are the tool's ADVERTISED structured shape (the
// generated output schema in mcpschema) and stay exactly that. Plan-stamping
// failures ride as TEXT content instead: they are per-call diagnostics rather
// than part of the proto-canonical result, and text content is the same channel
// coordinationResult already uses for a human-readable status. The agent
// therefore learns that N plans went unstamped without the advertised schema
// and the delivered payload drifting apart.
func reportResult(artifacts []*agentcoordpb.ArtifactProduced, stampFailures []string) *mcp.CallToolResult {
	ids := make([]any, 0, len(artifacts))
	for _, a := range artifacts {
		ids = append(ids, a.GetArtifactId())
	}
	out := &mcp.CallToolResult{StructuredContent: map[string]any{
		"journaled":    true,
		"artifact_ids": ids,
	}}
	if len(stampFailures) > 0 {
		out.Content = []mcp.Content{&mcp.TextContent{
			Text: fmt.Sprintf("report journaled, but %d plan artifact(s) were NOT stamped and are absent from artifact_ids:\n  %s",
				len(stampFailures), strings.Join(stampFailures, "\n  ")),
		}}
	}
	return out
}

// artifactPublishSizeCap bounds one file the runner reads and uploads on
// agent_report's behalf (E1c: "runner-read, size-capped sanely") — checked
// locally BEFORE the round trip; matches the coordinator's own
// artifactUploadSizeCap (coord/artifacts.go), so a locally-oversized file
// never even attempts the transfer.
const artifactPublishSizeCap = 64 << 20

// artifactCandidate is one file the runner considers for publish, from
// either source (automatic plan-stamping or agent_report.publish_paths).
type artifactCandidate struct {
	artifactID string
	name       string
	mediaType  string
	kind       agentcoordpb.ArtifactKind
	absPath    string
}

// artifactStamper tracks content hashes already successfully uploaded,
// keyed by artifact_id, so unchanged content is never re-uploaded (E1a's
// idempotency rule applied at the source, ahead of the wire — the store's
// own content addressing dedupes again at the coordinator regardless). It
// performs the actual upload via ArtifactTransferService (E1c: the runner
// reads the file cell-locally and uploads it).
type artifactStamper struct {
	harp string
	mu   sync.Mutex
	seen map[string]string // artifact_id → hex sha256 last successfully uploaded
}

// planCandidates lists the session's *.plan.md files as publish candidates —
// unconditional on every report; publish decides per-file whether content
// actually changed. WHICH directories hold them is plans.SessionPlanPaths'
// decision, not this file's, so the stamper collects plans from exactly the
// place mcp.sessionInstructions told the agent to write them.
//
// Two "no candidates" outcomes are legitimate and return no error: a stamper
// with no harp (docgen, tests — there is no session to stamp for) and a
// session dir that does not exist yet (nothing has been authored). Anything
// else — an unresolvable harp dir, an unreadable one — is a FAULT, and
// returning it is the point: reporting nil for a directory that could not be
// read makes "this session authored no plans" and "every plan this session
// authored is unreachable" the same observation, and the report then answers
// journaled:true with an empty artifact list either way.
func (p *artifactStamper) planCandidates() ([]artifactCandidate, error) {
	if p.harp == "" {
		return nil, nil
	}
	found, problems := plans.SessionPlanPaths(p.harp)
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	var out []artifactCandidate
	for _, abs := range found {
		base := filepath.Base(abs)
		out = append(out, artifactCandidate{
			artifactID: "plan/" + strings.TrimSuffix(base, paths.PlanFileExt),
			name:       base,
			mediaType:  "text/markdown",
			kind:       agentcoordpb.ArtifactKind_ARTIFACT_KIND_IMPLEMENTATION_PLAN,
			absPath:    abs,
		})
	}
	return out, nil
}

// publish uploads one candidate IF its content changed since the last
// successful upload for its artifact_id (nil, nil on no change — the common
// case on every report after the first). seen is committed ONLY after a
// successful upload, so a failed attempt is retried on the next call rather
// than silently wedged as "already seen".
func (p *artifactStamper) publish(ctx context.Context, home *runner.Home, c artifactCandidate) (*agentcoordpb.ArtifactProduced, error) {
	raw, err := os.ReadFile(c.absPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", c.absPath, err)
	}
	if len(raw) > artifactPublishSizeCap {
		return nil, fmt.Errorf("%s is %d bytes, over the %d-byte publish cap", c.absPath, len(raw), artifactPublishSizeCap)
	}
	// A FLOOR, not just a cap. Only the maximum was ever checked,
	// so a 0-byte file uploaded, journaled, and returned a success receipt
	// with a content-addressed id — "published my plan, got an id back,
	// delivered nothing", this project's characteristic silent no-op. A file
	// holding only whitespace is the same delivery of nothing.
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("%s is empty (%d bytes, no non-whitespace content) — refusing to publish an artifact with nothing in it", c.absPath, len(raw))
	}
	sum := sha256.Sum256(raw)
	hexSum := hex.EncodeToString(sum[:])

	p.mu.Lock()
	if p.seen == nil {
		p.seen = map[string]string{}
	}
	unchanged := p.seen[c.artifactID] == hexSum
	p.mu.Unlock()
	if unchanged {
		return nil, nil
	}

	receipt, err := home.UploadArtifact(ctx, c.artifactID, c.name, c.mediaType, sum, int64(len(raw)), bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("upload %s: %w", c.artifactID, err)
	}

	p.mu.Lock()
	p.seen[c.artifactID] = hexSum
	p.mu.Unlock()

	return &agentcoordpb.ArtifactProduced{
		ArtifactId: c.artifactID,
		Kind:       c.kind,
		Name:       c.name,
		MediaType:  c.mediaType,
		SizeBytes:  uint64(len(raw)),
		Sha256:     sum[:],
		Content:    &agentcoordpb.ArtifactProduced_UploadId{UploadId: receipt.GetUploadId()},
		Labels:     map[string]string{"path": c.absPath},
	}, nil
}

// mimeByExt guesses a publish_paths file's media type from its extension,
// falling back to a generic octet stream — never a bare "" (labels/schema
// consumers expect SOME value).
func mimeByExt(path string) string {
	if t := mime.TypeByExtension(filepath.Ext(path)); t != "" {
		return t
	}
	return "application/octet-stream"
}

// resolveCellPath resolves a caller-supplied path against root (the
// runner's cwd — the cell boundary) and rejects any result that escapes it
// (e.g. via ".."), matching the cell-local content tools' existing security
// boundary. Shared by agent_report's publish_paths (source) and
// agent_fetch_artifact's dest_path (destination).
func resolveCellPath(root, rel string) (string, error) {
	if rel == "" {
		return "", errors.New("path is required")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be relative to the working directory, not absolute", rel)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	absClean := filepath.Clean(filepath.Join(absRoot, rel))
	// The prefix a path under root must start with. Appending the separator
	// unconditionally breaks when root IS the filesystem root ("/"): absRoot
	// is already "/", so absRoot+separator would be "//", a prefix no real
	// path has — rejecting every relative path when the runner cwd is root.
	rootPrefix := absRoot
	if !strings.HasSuffix(rootPrefix, string(filepath.Separator)) {
		rootPrefix += string(filepath.Separator)
	}
	if absClean != absRoot && !strings.HasPrefix(absClean, rootPrefix) {
		return "", fmt.Errorf("path %q escapes the working directory", rel)
	}
	return absClean, nil
}

// artifactFetchHandler builds the handler for one generated
// RouteArtifactFetch tool — the mirror of coordinationHandler for the
// artifact-transfer route.
func artifactFetchHandler(home *runner.Home, cwd, name string) (mcp.ToolHandler, error) {
	switch name {
	case mcpschema.ToolAgentFetchArtifact:
		return fetchArtifactHandler(home, cwd), nil
	default:
		return nil, fmt.Errorf("runner MCP: no handler for generated tool %q — extend artifactFetchHandler alongside the binding table", name)
	}
}

// fetchArtifactHandler is agent_fetch_artifact (E1d): resolve dest_path
// cell-safely, then hand off to Home.DownloadArtifact, which streams the
// manifest header first, verifies the received content against it BEFORE
// placing the file, and hard-fails on a mismatch (E1e) — never a partial or
// corrupted file at dest_path.
func fetchArtifactHandler(home *runner.Home, cwd string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var m agentcoordpb.FetchArtifactRequest
		if err := unmarshalArgs(req, &m); err != nil {
			return nil, fmt.Errorf("agent_fetch_artifact: %w", err)
		}
		if m.GetAgentId() == "" {
			return nil, errors.New("agent_fetch_artifact: agent_id is required")
		}
		if m.GetArtifactId() == "" {
			return nil, errors.New("agent_fetch_artifact: artifact_id is required")
		}
		dest, err := resolveCellPath(cwd, m.GetDestPath())
		if err != nil {
			return nil, fmt.Errorf("agent_fetch_artifact: %w", err)
		}
		shaHex, size, err := home.DownloadArtifact(ctx, m.GetAgentId(), m.GetArtifactId(), dest)
		if err != nil {
			return nil, fmt.Errorf("agent_fetch_artifact: %w", err)
		}
		shaBytes, _ := hex.DecodeString(shaHex) // shaHex is our own hex.EncodeToString output
		resp := &agentcoordpb.CoordinatorResponse{Status: &rpcstatus.Status{
			Code:    int32(codes.OK),
			Message: fmt.Sprintf("wrote %s (%d bytes, sha256 %s)", dest, size, shaHex),
		}}
		return coordinationResult(resp, &agentcoordpb.FetchArtifactResult{
			Path:      dest,
			Sha256:    shaBytes,
			SizeBytes: uint64(size),
		})
	}
}
