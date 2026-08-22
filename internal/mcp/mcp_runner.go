package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
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

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/mcpschema"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/plans"
	"github.com/ctxloom/ctxloom/internal/version"
)

// RUNNER-TERMINATED MCP (agentcoord B1.6): the runner (`ctxloom llm serve`)
// serves the whole ctxloom MCP surface over streamable HTTP on a
// container-LOCAL unix socket, created BEFORE the harness spawns. The
// harness's stdio `ctxloom mcp` shim forwards here (CTXLOOM_MCP_SOCKET); the
// runner routes each tool per the routing table (mcpschema.Routes):
//
//   - coordination tools → typed plane-2 frames on the RunChannel (the
//     proto-canonical generated surface; the runner holds the one
//     credential and is the one egress — the tool path never crosses the
//     container boundary);
//   - cell-local content tools → served locally (the data was delivered
//     into the cell; same binary, same handlers);
//   - host-resident tools → CustomRequest{ctxloom/<tool>} relay to the
//     coordinator-side handlers (4MiB watched there);
//   - artifact-fetch tools (E1d) → served locally by calling
//     ArtifactTransferService directly on the runner's own credentialed
//     connection (coord.Home.DownloadArtifact) — schema-derived like the
//     coordination tools, but never a typed RunChannel frame; see
//     mcpschema.RouteArtifactFetch.

// RunnerMCP is one runner's MCP endpoint: the unix listener + server.
type RunnerMCP struct {
	SocketPath string
	httpSrv    *http.Server
	cleanup    func()
}

// ServeRunnerMCP builds the runner's MCP server, binds the unix socket, and
// starts serving. It returns only with the socket LISTENING — the ordering
// invariant: the runner controls the harness spawn, and the socket exists
// before it (assert, don't race).
//
// HOST-CONTROLLED DISCOVERY (fix/host-controlled-mcp-discovery): env-var
// delivery of the socket path rides a VENDOR-CONTROLLED channel (the ACP
// mcpServers.env array) that at least one real adapter drops on the floor
// (codex-acp: honors name/command/args, discards env). Alongside the socket,
// this ALSO publishes a discovery marker at a well-known location keyed the
// same way a shim with no env can rediscover it — see writeDiscoveryMarker
// and probeWellKnownRunner (mcp_discovery.go). Best-effort: a marker failure
// degrades to env-only discovery, same fault tolerance as the socket bind
// itself never blocking the runner.
//
// cellWorkDir is the runner's coord.EnvCellWorkDir reading (empty when
// unset): the prepared workspace dir the harness's engine process actually
// runs in, which can differ from THIS process's own os.Getwd() for a
// workspace:worktree run (fix/host-discovery-anchor — the runner is spawned
// with no cmd.Dir and inherits the coordinator's cwd, while the harness is
// launched with cmd.Dir=the per-agent worktree). The marker key must agree
// with the shim's cwd-derived key, so cellWorkDir wins over os.Getwd() when
// present; falls back to os.Getwd() when empty (workspace:none/container, or
// any caller that never threads it) — behaviour-identical to before this
// var existed.
func ServeRunnerMCP(cfg *config.Config, harp string, home *coord.Home, leaf bool, cellWorkDir string) (*RunnerMCP, error) {
	// cellWorkDir must reach the SAME place on both uses — the
	// discovery marker key below AND the tool surface's own cell-path
	// boundary (newRunnerMCPServer's ctxServer identity + resolveCellPath
	// roots). Before this fix only the marker got it; newRunnerMCPServer
	// took its own independent os.Getwd() — the coordinator's cwd, not the
	// cell work dir the harness actually runs in (coord/identity.go's own
	// doc names this mismatch) — so a workspace:worktree run's
	// agent_report/agent_fetch_artifact resolved against the PARENT
	// project root instead of the per-agent worktree resolveCellPath
	// exists to confine them to.
	server, err := newRunnerMCPServer(cfg, harp, home, leaf, cellWorkDir)
	if err != nil {
		return nil, err
	}
	path, dir, kind, cleanupSocket, err := runnerSocketPath()
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		cleanupSocket()
		return nil, fmt.Errorf("runner MCP socket %s: %w", path, err)
	}
	// habitable-cape / graceful-egomaniac unit 4: reap this dir's stale
	// markers (confirmed-dead owner pid) BEFORE publishing our own — cheap,
	// best-effort, and this is what clears the 12,334-marker backlog the
	// finding measured on the very first runner launch after this fix, and
	// keeps it from reaccumulating on every launch after that.
	if n := reapStaleDiscoveryMarkers(dir); n > 0 {
		fmt.Fprintf(os.Stderr, "ctxloom: runner MCP: reaped %d stale discovery marker(s) in %s\n", n, dir)
	}
	cwd := resolveCellWorkDir(cellWorkDir)
	cleanupMarker, merr := writeDiscoveryMarker(dir, kind, cwd, runnerDiscoveryMarker{
		Socket: path,
		Pid:    os.Getpid(),
		Harp:   harp,
		Stamp:  version.Version,
	})
	if merr != nil {
		clidiag.Warn("ctxloom", "runner MCP discovery marker: %v (the shim will still find this runner via %s)", merr, coord.EnvMCPSocket)
		cleanupMarker = func() {}
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	srv := &http.Server{Handler: mux}
	go serveRunnerHTTP(srv, ln, path)
	cleanup := func() {
		cleanupMarker()
		cleanupSocket()
	}
	return &RunnerMCP{SocketPath: path, httpSrv: srv, cleanup: cleanup}, nil
}

// serveRunnerHTTP runs the runner's MCP endpoint until it stops. http.Server's
// Serve ALWAYS returns an error; on a deliberate shutdown that error is
// http.ErrServerClosed, so any other value means the endpoint died while the
// runner carried on running and carried on advertising a socket (and a
// discovery marker) that nothing answers on. The harness's stdio shim then
// dials a live-looking path, and every ctxloom tool in that session fails with
// a transport error that names nothing about the real cause.
func serveRunnerHTTP(srv *http.Server, ln net.Listener, socketPath string) {
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		clidiag.Warn("ctxloom", "runner MCP endpoint at %s stopped serving: %v (ctxloom tools in this session will fail until the runner is restarted)", socketPath, err)
	}
}

// Close shuts the endpoint down and removes its socket dir.
func (r *RunnerMCP) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = r.httpSrv.Shutdown(ctx)
	r.cleanup()
}

// socketKind classifies WHICH tier runnerSocketPath landed on, so
// ServeRunnerMCP knows whether (and how) to publish a discovery marker
// alongside the socket — see mcp_discovery.go.
type socketKind int

const (
	// socketKindContainer is the /run/ctxloom/local convention: exactly ONE
	// runner per container, so its discovery marker can use a FIXED name —
	// no key negotiation needed between writer and reader.
	socketKindContainer socketKind = iota
	// socketKindHostRuntime is $XDG_RUNTIME_DIR/ctxloom: a host user-private
	// dir that MULTIPLE runners (multiple ctxloom sessions on one host, no
	// container isolation) can share, so its discovery marker is keyed by
	// the cell's workspace path to avoid collisions.
	socketKindHostRuntime
	// socketKindPrivateTemp is the last-resort per-process MkdirTemp dir:
	// unique to this runner by construction, so nothing else could ever
	// know where to look — no discovery marker is published there; the
	// env var remains the only path to this tier.
	socketKindPrivateTemp
)

// inContainerSocketDir is the agent-image convention: writable only
// in-container, never on a bare host (a normal user can't mkdir under
// /run), so trying it first costs nothing on a host run.
const inContainerSocketDir = "/run/ctxloom/local"

// runnerSocketPath picks the runner MCP socket location on CONTAINER-LOCAL
// (or host user-private) filesystem — NEVER inside the host-mounted plugin
// dir: a bind-mounted unix socket is exactly the VirtioFS trap the design
// avoids. Preference order: /run/ctxloom/local (the agent-image convention;
// writable only in-container), $XDG_RUNTIME_DIR, then a private temp dir.
// Paths are kept short for the sun_path limit. Returns the socket path, the
// directory it lives in, and which tier was chosen (socketKind) — the
// directory+kind pair is what writeDiscoveryMarker needs to publish this
// socket at its well-known location.
func runnerSocketPath() (path string, dir string, kind socketKind, cleanup func(), err error) {
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
	if p, ok := candidate(inContainerSocketDir); ok {
		return p, inContainerSocketDir, socketKindContainer, func() { _ = os.Remove(p) }, nil
	}
	hostDir := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "ctxloom")
	if p, ok := candidate(hostDir); ok {
		return p, hostDir, socketKindHostRuntime, func() { _ = os.Remove(p) }, nil
	}
	tmpDir, mkErr := os.MkdirTemp("", "ctxloom-mcp-")
	if mkErr != nil {
		return "", "", socketKindPrivateTemp, nil, fmt.Errorf("runner MCP socket dir: %w", mkErr)
	}
	p := filepath.Join(tmpDir, "mcp.sock")
	if len(p) > sunPathHeadroom {
		_ = os.RemoveAll(tmpDir)
		return "", "", socketKindPrivateTemp, nil, fmt.Errorf("runner MCP socket path %q exceeds the portable sun_path limit", p)
	}
	return p, tmpDir, socketKindPrivateTemp, func() { _ = os.RemoveAll(tmpDir) }, nil
}

// newRunnerMCPServer assembles the runner's tool surface per the routing
// table. Completeness is a STARTUP invariant: every tool registered here
// must be classified, and every classified tool must be served by exactly
// the route the table names — a mismatch errors the runner up front, never
// a silent fallthrough.
//
// leaf is the trust-boundary gate's session-conditional axis (computed in
// attachRunnerMCP/runnerIsLeaf, llm_runner_common.go: this run's stamped
// delegation depth (EnvRunDepth) compared against the resolved
// delegation-depth cap, depth >= cap — the session owner is always depth 0
// and so is never leaf at the built-in cap — OR this run's stamped
// one-shot fact (EnvRunOneShot): a `driving: oneshot` run is ALWAYS a
// leaf, regardless of depth, since its engine tears down at every turn
// boundary and cannot hold a coordination relationship with a child):
// when true, the coordinator-only tools (mcpschema.CoordinatorOnlyTools)
// are deliberately withheld from THIS session's surface — see the
// registration loop below.
// resolveCellWorkDir is the DRY form of the "cellWorkDir wins over
// os.Getwd()" fallback rule: cellWorkDir is the prepared
// workspace dir the harness's engine process actually runs in, which can
// differ from THIS process's own os.Getwd() for a workspace:worktree run —
// the runner is spawned with no cmd.Dir and inherits the coordinator's cwd,
// while the harness is launched with cmd.Dir=the per-agent worktree. Falls
// back to os.Getwd() when cellWorkDir is empty (workspace:none/container, or
// a caller — mcp_docgen.go, tests — that has no real cell to anchor to).
func resolveCellWorkDir(cellWorkDir string) string {
	if cellWorkDir != "" {
		return cellWorkDir
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return "."
}

func newRunnerMCPServer(cfg *config.Config, harp string, home *coord.Home, leaf bool, cellWorkDir string) (*mcp.Server, error) {
	server := mcp.NewServer(&mcp.Implementation{
		Name: "ctxloom",
		// Title carries this runner's session harp across the initialize
		// handshake — graceful-egomaniac unit 2's identity check
		// (mcp_forward.go's forwardTargetIdentity/verifyForwardTarget). This
		// is an INTERNAL extension of the client<->runner handshake only:
		// the stdio server this package exposes to the real external MCP
		// client (buildForwardServer, mcp_forward.go) builds its own
		// separate Implementation with no Title, so nothing about the
		// wire protocol an editor sees changes.
		Title:   harp,
		Version: version.Version,
	}, &mcp.ServerOptions{Instructions: sessionInstructions(harp)})

	routes := mcpschema.Routes()
	registered := map[string]bool{}

	// Cell-local content tools + resources: the per-runner ctxServer over
	// the cell-delivered config. Identity is the runner's own harp. cwd is
	// ALSO the cell-path boundary root threaded into coordinationHandler/
	// artifactFetchHandler below (resolveCellPath) — this used to
	// be an independent os.Getwd() call, ignoring cellWorkDir entirely.
	cwd := resolveCellWorkDir(cellWorkDir)
	s := &ctxServer{cfg: cfg, self: coord.Identity{Harp: harp, Project: cwd}}
	s.registerResources(server)
	if err := claimRoutes(routes, registered, mcpschema.RouteCellLocal, s.registerContextTools(server)...); err != nil {
		return nil, err
	}

	if err := claimRoutes(routes, registered, mcpschema.RouteHostRelay, registerHostRelays(server, home)...); err != nil {
		return nil, err
	}

	if err := registerGeneratedTools(server, home, harp, cwd, leaf, routes, registered); err != nil {
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

// registerHostRelays adds the host-resident tools as CustomRequest relays and
// returns the names it registered. Each carries the SAME typed input (and so
// the same advertised schema) AND the same description constant the stdio
// server registers (mcp_tools_memory.go, mcp_tools_triggers.go), so the two
// surfaces cannot describe one tool two ways;
// TestRunnerServer_HostRelayDescriptionsMatchStdio pins that.
func registerHostRelays(server *mcp.Server, home *coord.Home) []string {
	return []string{
		addHostRelay[compactSessionInput](server, home, "compact_session", compactSessionDesc),
		addHostRelay[loadSessionInput](server, home, "load_session", loadSessionDesc),
		addHostRelay[recoverSessionInput](server, home, "recover_session", recoverSessionDesc),
		addHostRelay[getPreviousSessionInput](server, home, "get_previous_session", getPreviousSessionDesc),
		addHostRelay[listSessionsInput](server, home, "list_sessions", listSessionsDesc),
		addHostRelay[evaluateTriggersInput](server, home, "evaluate_triggers", evaluateTriggersDesc),
		addHostRelay[contextStatusInput](server, home, "context_status", contextStatusDesc),
	}
}

// addHostRelay registers one host-resident tool and returns its name, so the
// name is written once per tool rather than once in the registration and again
// in a classification list.
func addHostRelay[In any](server *mcp.Server, home *coord.Home, name, desc string) string {
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: desc}, relayTyped[In](home, name))
	return name
}

// registerGeneratedTools adds the proto-canonical tools: coordination frames
// AND artifact-fetch both draw their schemas from mcpschema.Tools() and differ
// only in which handler builder serves them (Binding.Route).
func registerGeneratedTools(server *mcp.Server, home *coord.Home, harp, cwd string, leaf bool, routes map[string]mcpschema.Route, registered map[string]bool) error {
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
		// coordinator-only tools (agent_run/roster/agent_stop/
		// agent_fetch_artifact) — a leaf holding an agent_recv inbox plus a
		// roster infers it has children and stalls waiting for notifications
		// that never arrive. Still marked registered (deliberately withheld),
		// or the caller's exhaustiveness check fails runner startup;
		// agent_send/agent_recv/agent_report (parent reporting) are untouched
		// by this gate.
		if leaf && mcpschema.CoordinatorOnlyTools()[spec.Name] {
			registered[spec.Name] = true
			continue
		}
		h, herr := generatedToolHandler(home, harp, cwd, route, spec.Name)
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
func generatedToolHandler(home *coord.Home, harp, cwd string, route mcpschema.Route, name string) (mcp.ToolHandler, error) {
	switch route {
	case mcpschema.RouteCoordination:
		return coordinationHandler(home, harp, cwd, name)
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
func coordinationHandler(home *coord.Home, harp, cwd, name string) (mcp.ToolHandler, error) {
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
		return reportHandler(home, harp, cwd), nil
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

// recvHandler is the runner-LOCAL agent_recv: park against the Home's
// notice buffer. Returned messages stay tentative at the coordinator until
// the NEXT recv (cursor-ack) or a clean runner shutdown acknowledges them —
// the go-sdk streamable server runs tool handlers on session-scoped
// contexts and holds POST streams open, so there is no per-response write
// hook to ack on; a crash before the ack re-delivers (at-least-once).
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
		wait := mcpschema.ClampRecvWait(in.Wait)
		msgs, err := home.Recv(ctx, wait)
		if err != nil {
			if errors.Is(err, coord.ErrRecvTimeout) {
				return nil, fmt.Errorf("%w (waited %s)", coord.ErrRecvTimeout, wait)
			}
			return nil, err
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
				clidiag.Warn("ctxloom", "agent_recv: message %s: marshal: %v (already acked as returned — dropped, not redelivered)", m.GetMessageId(), merr)
				continue
			}
			var v any
			if uerr := json.Unmarshal(raw, &v); uerr != nil {
				dropped = append(dropped, m.GetMessageId())
				clidiag.Warn("ctxloom", "agent_recv: message %s: decode: %v (already acked as returned — dropped, not redelivered)", m.GetMessageId(), uerr)
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
func reportHandler(home *coord.Home, harp, cwd string) mcp.ToolHandler {
	stamper := &artifactStamper{harp: harp}
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

		var artifacts []*agentcoordpb.ArtifactProduced
		// stampFailures collects everything plan auto-stamping could not
		// deliver, so the answer can say so. A failure here still must not
		// block the report — these files are not something the agent asked for
		// THIS call — but "did not block" was implemented as "did not mention",
		// and the caller then read journaled:true with an empty artifact list
		// as a report that simply had no plans to stamp.
		var stampFailures []string

		// Automatic plan-stamping: session-dir *.plan.md files, best-effort
		// per file exactly as before E1.
		cands, cerr := stamper.planCandidates()
		if cerr != nil {
			clidiag.Warn("ctxloom", "agent_report: plan discovery: %v", cerr)
			stampFailures = append(stampFailures, fmt.Sprintf("plan discovery: %v", cerr))
		}
		for _, cand := range cands {
			a, perr := stamper.publish(ctx, home, cand)
			if perr != nil {
				clidiag.Warn("ctxloom", "agent_report: plan stamp %s: %v", cand.absPath, perr)
				stampFailures = append(stampFailures, fmt.Sprintf("%s: %v", cand.absPath, perr))
				continue
			}
			if a != nil {
				artifacts = append(artifacts, a)
			}
		}

		// E1c generic publish case: agent-DECLARED files. Unlike plan
		// auto-discovery, a failure here is FAIL LOUD — the agent explicitly
		// asked to publish a specific file; silently dropping it would be a
		// correctness bug, not a degrade-gracefully case.
		for _, rel := range summary.GetPublishPaths() {
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

		for _, a := range artifacts {
			summary.ArtifactIds = append(summary.ArtifactIds, a.GetArtifactId())
		}
		if err := home.Report(ctx, &summary, artifacts); err != nil {
			return nil, fmt.Errorf("agent_report: %w", err)
		}
		return reportResult(artifacts, stampFailures), nil
	}
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
func (p *artifactStamper) publish(ctx context.Context, home *coord.Home, c artifactCandidate) (*agentcoordpb.ArtifactProduced, error) {
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
func artifactFetchHandler(home *coord.Home, cwd, name string) (mcp.ToolHandler, error) {
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
func fetchArtifactHandler(home *coord.Home, cwd string) mcp.ToolHandler {
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
