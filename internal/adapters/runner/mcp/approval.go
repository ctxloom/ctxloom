package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// permissionHostDesc is the permission host's description. The engine is
// pointed at the tool; the model is told plainly that it decides nothing.
const permissionHostDesc = "ctxloom's permission host for this session's engine. It holds a permission request open while the approval hook carries the human's decision to the engine, and it always answers deny. It grants nothing: calling it yourself is refused."

// errNoApprovalRoute is the permission host's answer in a run that routes no
// approvals: nobody is asked, so nothing is held.
var errNoApprovalRoute = errors.New("permission_host: this run routes no approvals to a human")

// maxHookPayload bounds an approval hook's body: an ask carries one tool
// call's input, at most a plan's markdown.
const maxHookPayload = 8 << 20

// registerApprovalHost adds the permission host and returns its name. It is
// served against the run's approval route (runner.Home.ApprovalHost), bound
// when the run's approver is the human.
func registerApprovalHost(server *mcp.Server, home *runner.Home) []string {
	server.AddTool(&mcp.Tool{
		Name:        engine.PermissionHostTool,
		Description: permissionHostDesc,
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		route := home.ApprovalHost()
		if route == nil {
			return nil, errNoApprovalRoute
		}
		out, err := route.Host(ctx, req.Params.Arguments)
		if err != nil {
			return nil, fmt.Errorf("permission_host: %w", err)
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: out}}}, nil
	})
	return []string{engine.PermissionHostTool}
}

// hookHandler serves the approval hook's POST: the engine's native payload
// for the event the query names, answered with the engine's native
// decision. Anything else is an error status, which the hook command turns
// into no decision at all — and the held permission host then denies.
func hookHandler(home *runner.Home) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "the approval hook POSTs", http.StatusMethodNotAllowed)
			return
		}
		route := home.ApprovalHost()
		if route == nil {
			http.Error(w, errNoApprovalRoute.Error(), http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxHookPayload+1))
		if err != nil {
			http.Error(w, fmt.Sprintf("read the hook payload: %v", err), http.StatusBadRequest)
			return
		}
		if len(body) > maxHookPayload {
			http.Error(w, "the hook payload is larger than an ask can be", http.StatusRequestEntityTooLarge)
			return
		}
		out, err := route.Hook(r.Context(), r.URL.Query().Get(runner.HookEventParam), body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	})
}
