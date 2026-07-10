package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/mcpschema"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// RUNNER-TERMINATED MCP (agentcoord B1.6): the runner (`ctxloom llm serve`)
// serves the whole ctxloom MCP surface over streamable HTTP on a
// container-LOCAL unix socket, created BEFORE the harness spawns. The
// harness's stdio `ctxloom mcp` shim forwards here (CTXLOOM_MCP_SOCKET); the
// runner routes each tool per the three-way table (mcpschema.Routes):
//
//   - coordination tools → typed plane-2 frames on the RunChannel (the
//     proto-canonical generated surface; the runner holds the one
//     credential and is the one egress — the tool path never crosses the
//     container boundary);
//   - cell-local content tools → served locally (the data was delivered
//     into the cell; same binary, same handlers);
//   - host-resident tools → CustomRequest{ctxloom/<tool>} relay to the
//     coordinator-side handlers (4MiB watched there).

// runnerMCP is one runner's MCP endpoint: the unix listener + server.
type runnerMCP struct {
	socketPath string
	httpSrv    *http.Server
	cleanup    func()
}

// serveRunnerMCP builds the runner's MCP server, binds the unix socket, and
// starts serving. It returns only with the socket LISTENING — the ordering
// invariant: the runner controls the harness spawn, and the socket exists
// before it (assert, don't race).
func serveRunnerMCP(cfg *config.Config, harp string, home *coord.Home) (*runnerMCP, error) {
	server, err := newRunnerMCPServer(cfg, harp, home)
	if err != nil {
		return nil, err
	}
	path, cleanup, err := runnerSocketPath()
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("runner MCP socket %s: %w", path, err)
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", consumeAfterWrite(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)))
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	return &runnerMCP{socketPath: path, httpSrv: srv, cleanup: cleanup}, nil
}

// close shuts the endpoint down and removes its socket dir.
func (r *runnerMCP) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = r.httpSrv.Shutdown(ctx)
	r.cleanup()
}

// runnerSocketPath picks the runner MCP socket location on CONTAINER-LOCAL
// (or host user-private) filesystem — NEVER inside the host-mounted plugin
// dir: a bind-mounted unix socket is exactly the VirtioFS trap the design
// avoids. Preference order: /run/ctxloom/local (the agent-image convention;
// writable only in-container), $XDG_RUNTIME_DIR, then a private temp dir.
// Paths are kept short for the sun_path limit.
func runnerSocketPath() (string, func(), error) {
	const sunPathHeadroom = 100
	candidate := func(dir string) (string, bool) {
		if dir == "" {
			return "", false
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", false
		}
		p := filepath.Join(dir, fmt.Sprintf("mcp-%d.sock", os.Getpid()))
		if len(p) > sunPathHeadroom {
			return "", false
		}
		_ = os.Remove(p) // a stale same-pid socket from a recycled pid
		return p, true
	}
	if p, ok := candidate("/run/ctxloom/local"); ok {
		return p, func() { _ = os.Remove(p) }, nil
	}
	if p, ok := candidate(filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "ctxloom")); ok {
		return p, func() { _ = os.Remove(p) }, nil
	}
	dir, err := os.MkdirTemp("", "ctxloom-mcp-")
	if err != nil {
		return "", nil, fmt.Errorf("runner MCP socket dir: %w", err)
	}
	p := filepath.Join(dir, "mcp.sock")
	if len(p) > sunPathHeadroom {
		_ = os.RemoveAll(dir)
		return "", nil, fmt.Errorf("runner MCP socket path %q exceeds the portable sun_path limit", p)
	}
	return p, func() { _ = os.RemoveAll(dir) }, nil
}

// newRunnerMCPServer assembles the runner's tool surface per the routing
// table. Completeness is a STARTUP invariant: every tool registered here
// must be classified, and every classified tool must be served by exactly
// the route the table names — a mismatch errors the runner up front, never
// a silent fallthrough.
func newRunnerMCPServer(cfg *config.Config, harp string, home *coord.Home) (*mcp.Server, error) {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "ctxloom",
		Version: Version,
	}, &mcp.ServerOptions{Instructions: sessionInstructions(harp)})

	routes := mcpschema.Routes()
	registered := map[string]bool{}

	// Cell-local content tools + resources: the per-runner ctxServer over
	// the cell-delivered config. Identity is the runner's own harp.
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	s := &ctxServer{cfg: cfg, self: coord.Identity{Harp: harp, Project: cwd}}
	s.registerContextTools(server)
	s.registerResources(server)
	for _, name := range []string{"assemble_context", "search_content", "search_library"} {
		if routes[name] != mcpschema.RouteCellLocal {
			return nil, fmt.Errorf("runner MCP: tool %q registered cell-local but classified otherwise — fix mcpschema.Routes", name)
		}
		registered[name] = true
	}

	// Host-resident tools: relay as CustomRequest{ctxloom/<tool>}, with the
	// SAME typed inputs (and so schemas) the stdio server registers. The
	// descriptions mirror the stdio registrations (mcp_tools_memory.go);
	// TestRunnerServer_HostRelayDescriptionsMatchStdio pins the parity.
	mcp.AddTool(server, &mcp.Tool{Name: "compact_session", Description: relayCompactSessionDesc},
		relayTyped[compactSessionInput](home, "compact_session"))
	mcp.AddTool(server, &mcp.Tool{Name: "load_session", Description: relayLoadSessionDesc},
		relayTyped[loadSessionInput](home, "load_session"))
	mcp.AddTool(server, &mcp.Tool{Name: "recover_session", Description: relayRecoverSessionDesc},
		relayTyped[recoverSessionInput](home, "recover_session"))
	mcp.AddTool(server, &mcp.Tool{Name: "get_previous_session", Description: relayGetPreviousSessionDesc},
		relayTyped[getPreviousSessionInput](home, "get_previous_session"))
	for _, name := range []string{"compact_session", "load_session", "recover_session", "get_previous_session"} {
		if routes[name] != mcpschema.RouteHostRelay {
			return nil, fmt.Errorf("runner MCP: tool %q registered as host-relay but classified otherwise — fix mcpschema.Routes", name)
		}
		registered[name] = true
	}

	// Coordination tools: the proto-canonical generated surface.
	tools, err := mcpschema.Tools()
	if err != nil {
		return nil, err
	}
	for _, spec := range tools {
		if routes[spec.Name] != mcpschema.RouteCoordination {
			return nil, fmt.Errorf("runner MCP: generated tool %q is not classified as coordination — fix mcpschema.Routes", spec.Name)
		}
		h, err := coordinationHandler(home, harp, spec.Name)
		if err != nil {
			return nil, err
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

	// Exhaustiveness: nothing classified may be missing from this surface.
	for name := range routes {
		if !registered[name] {
			return nil, fmt.Errorf("runner MCP: classified tool %q is not served by any route — fix the registration or mcpschema.Routes", name)
		}
	}
	return server, nil
}

// Host-relay tool descriptions — kept byte-identical to the stdio
// registrations in mcp_tools_memory.go (pinned by the parity test; the
// stdio file stays untouched so its registration group stays uniform).
const (
	relayCompactSessionDesc     = "Compact current or specified session log into a distilled summary. Use this to compress a session log when context is running low."
	relayLoadSessionDesc        = "Distill and load context from a session. Accepts either session_id (backend UUID) or harp_name (human-readable). For names, see ctxloom://sessions/recent."
	relayRecoverSessionDesc     = "Recover context from the current session after /clear. Resolves the most recent session transcript for this working directory and distills it (no session id needed; pass one to target a specific session)."
	relayGetPreviousSessionDesc = "Distill and load an EARLIER session's content — the most recent session BEFORE the active one for this working directory, resolved via the session registry (cross-agent aware; falls back to the second-most-recent transcript). For inspecting a prior session. NOT the post-/clear path: /clear keeps the SAME session alive, so to recover context wiped by /clear use recover_session instead."
)

// relayTyped forwards one host-resident tool over the RunChannel as
// CustomRequest{ctxloom/<tool>} with the SAME typed input the stdio server
// registers (identical advertised schema), handing the coordinator's Struct
// result back as the tool's structured content.
func relayTyped[In any](home *coord.Home, name string) mcp.ToolHandlerFor[In, map[string]any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, map[string]any, error) {
		raw, err := json.Marshal(in)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: encode arguments: %w", name, err)
		}
		args := &structpb.Struct{}
		if err := protojson.Unmarshal(raw, args); err != nil {
			return nil, nil, fmt.Errorf("%s: encode arguments: %w", name, err)
		}
		resp, err := home.Request(ctx, &agentcoordpb.AgentRequest{
			Kind: &agentcoordpb.AgentRequest_Custom{Custom: &agentcoordpb.CustomRequest{
				Name:  coord.CustomToolPrefix + name,
				Value: args,
			}},
		})
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", name, err)
		}
		if st := resp.GetStatus(); st.GetCode() != int32(codes.OK) {
			return nil, nil, errors.New(st.GetMessage())
		}
		rawOut, err := protojson.Marshal(resp.GetCustom())
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
func coordinationHandler(home *coord.Home, harp, name string) (mcp.ToolHandler, error) {
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
		}, nil
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
		}, nil
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
		}, nil
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
		}, nil
	case mcpschema.ToolAgentRecv:
		return recvHandler(home), nil
	case mcpschema.ToolAgentReport:
		return reportHandler(home, harp), nil
	default:
		return nil, fmt.Errorf("runner MCP: no handler for generated tool %q — extend coordinationHandler alongside the binding table", name)
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
	if result != nil && !reflectIsNil(result) {
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

// reflectIsNil guards typed-nil proto results inside the oneof accessors.
func reflectIsNil(m proto.Message) bool {
	return m == nil || !m.ProtoReflect().IsValid()
}

// recvHandler is the runner-LOCAL agent_recv: park against the Home's
// notice buffer; the consumption fact is emitted AFTER the response reaches
// the shim (consumeAfterWrite middleware) so a crash in between re-delivers.
func recvHandler(home *coord.Home) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in struct {
			Wait int `json:"wait"`
		}
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
				return nil, fmt.Errorf("agent_recv: %w", err)
			}
		}
		wait := time.Duration(in.Wait) * time.Second
		if wait <= 0 {
			wait = defaultRecvWait
		}
		if wait > maxRecvWait {
			wait = maxRecvWait
		}
		msgs, consume, err := home.Recv(ctx, wait)
		if err != nil {
			if errors.Is(err, coord.ErrRecvTimeout) {
				return nil, fmt.Errorf("%w (waited %s)", coord.ErrRecvTimeout, wait)
			}
			return nil, err
		}
		items := make([]any, 0, len(msgs))
		for _, m := range msgs {
			raw, merr := protojson.MarshalOptions{UseProtoNames: true}.Marshal(m)
			if merr != nil {
				continue
			}
			var v any
			if json.Unmarshal(raw, &v) == nil {
				items = append(items, v)
			}
		}
		scheduleConsume(ctx, consume)
		return &mcp.CallToolResult{StructuredContent: map[string]any{"messages": items}}, nil
	}
}

// reportHandler is agent_report: file the Summary (and auto-stamped plan
// manifests) as plane-1 events, returning once the coordinator's Ack covers
// them (durably journaled).
func reportHandler(home *coord.Home, harp string) mcp.ToolHandler {
	stamper := &planStamper{harp: harp}
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var summary agentcoordpb.Summary
		if err := unmarshalArgs(req, &summary); err != nil {
			return nil, fmt.Errorf("agent_report: %w", err)
		}
		if summary.GetText() == "" {
			return nil, errors.New("agent_report: text is required (the report body)")
		}
		if summary.GetScope() == agentcoordpb.Summary_SCOPE_UNSPECIFIED {
			return nil, errors.New("agent_report: scope is required (SCOPE_PROGRESS | SCOPE_CHECKPOINT | SCOPE_FINAL | SCOPE_STEP)")
		}
		// Plan-stamping is the hook: session-dir *.plan.md files become
		// artifact manifests (manifest-on-log; bytes stay in the session
		// dir) whenever their content changed since the last stamp.
		artifacts := stamper.changedManifests()
		for _, a := range artifacts {
			summary.ArtifactIds = append(summary.ArtifactIds, a.GetArtifactId())
		}
		if err := home.Report(ctx, &summary, artifacts); err != nil {
			return nil, fmt.Errorf("agent_report: %w", err)
		}
		ids := make([]any, 0, len(artifacts))
		for _, a := range artifacts {
			ids = append(ids, a.GetArtifactId())
		}
		return &mcp.CallToolResult{StructuredContent: map[string]any{
			"journaled":    true,
			"artifact_ids": ids,
		}}, nil
	}
}

// planStamper tracks the session dir's *.plan.md content hashes and emits an
// ArtifactProduced manifest for each new/changed plan (revision assignment
// is the coordinator's — the producer sends 0).
type planStamper struct {
	harp string
	mu   sync.Mutex
	seen map[string]string // file name → hex sha256 last stamped
}

func (p *planStamper) changedManifests() []*agentcoordpb.ArtifactProduced {
	if p.harp == "" {
		return nil
	}
	dir, err := paths.HarpDir(p.harp)
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.seen == nil {
		p.seen = map[string]string{}
	}
	var out []*agentcoordpb.ArtifactProduced
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), paths.PlanFileExt) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			clidiag.Warn("ctxloom", "agent_report: plan stamp %s: %v", path, rerr)
			continue
		}
		sum := sha256.Sum256(raw)
		hexSum := fmt.Sprintf("%x", sum)
		if p.seen[e.Name()] == hexSum {
			continue
		}
		p.seen[e.Name()] = hexSum
		out = append(out, &agentcoordpb.ArtifactProduced{
			ArtifactId: "plan/" + strings.TrimSuffix(e.Name(), paths.PlanFileExt),
			Kind:       agentcoordpb.ArtifactKind_ARTIFACT_KIND_IMPLEMENTATION_PLAN,
			Name:       e.Name(),
			MediaType:  "text/markdown",
			SizeBytes:  uint64(len(raw)),
			Sha256:     sum[:],
			Labels:     map[string]string{"path": path},
		})
	}
	return out
}

// --- consume-after-write middleware -------------------------------------------

// consumeNoteKey carries the per-request consumption note.
type consumeNoteKey struct{}

type consumeNote struct {
	mu  sync.Mutex
	fns []func()
}

func (n *consumeNote) add(fn func()) {
	n.mu.Lock()
	n.fns = append(n.fns, fn)
	n.mu.Unlock()
}

func (n *consumeNote) take() []func() {
	n.mu.Lock()
	defer n.mu.Unlock()
	fns := n.fns
	n.fns = nil
	return fns
}

// consumeAfterWrite defers mail-consumption facts until AFTER the HTTP
// response carrying the messages was written to the shim — the playbook's
// tentative-delivery ordering. A crash before the write re-delivers; a crash
// after it at worst duplicates (deduped on message_id downstream).
func consumeAfterWrite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		note := &consumeNote{}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), consumeNoteKey{}, note)))
		for _, fn := range note.take() {
			fn()
		}
	})
}

// scheduleConsume runs the delivery's consumption fact after the response
// write when the transport carries the note (the HTTP middleware); on a
// transport that does not thread the request context (or in tests), it runs
// immediately — the at-least-once guarantee holds either way, only the
// crash-window direction differs.
func scheduleConsume(ctx context.Context, consume func()) {
	if consume == nil {
		return
	}
	if note, ok := ctx.Value(consumeNoteKey{}).(*consumeNote); ok {
		note.add(consume)
		return
	}
	consume()
}
