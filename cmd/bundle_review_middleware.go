package cmd

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// allowedDuringReview lists every tool that may still be invoked while a
// bundle review is pending. Anything not on this list is hard-blocked at
// the middleware (docs/bundle-review-plan.md Phase 4.1). The shape of
// this list is the load-bearing security invariant — keep it minimal.
//
// Categorization rule: ALLOW tools that don't return bundle bodies,
// don't run bundle-shipped code, don't replay prior session content,
// and can't re-trigger sync. BLOCK everything else.
var allowedDuringReview = map[string]struct{}{
	// Review machinery itself.
	"acknowledge_bundle_review": {},
	"decline_bundle":            {},
	"show_bundle_verbatim":      {},
	"trust_remote":              {},

	// Pure metadata listings — no bundle bodies returned.
	"list_remotes":      {},
	"list_profiles":     {},
	"list_prompts":      {},
	"list_fragments":    {},
	"list_mcp_servers":  {},
	"list_sessions":     {},

	// Metadata get/browse — same rule as above.
	"get_profile":            {},
	"browse_session_history": {},
	"get_previous_session":   {},

	// Local-only bundle mutations (Phase 4: bundles stay as tools because
	// authoring is frequent and integrates with the review gate).
	"create_bundle": {},
	"update_bundle": {},
	"delete_bundle": {},

	// Session compaction (reduces only, doesn't expose bundle bytes).
	"compact_session": {},

	// Phase 4 Lever B removals: add_remote, remove_remote, update_remote,
	// discover_remotes, search_remotes, pull_remote, push_bundle,
	// add_mcp_server, remove_mcp_server, set_mcp_auto_register,
	// create_profile, update_profile, delete_profile, create_fragment,
	// delete_fragment — all moved to CLI; no allowlist entries needed.
	//
	// Phase 4 Lever A removals (deferred from this commit; tools currently
	// still registered): list_*, get_*, browse_* — to be migrated to
	// MCP resources alongside the existing ctxloom://help, ctxloom://
	// sessions/recent, ctxloom://tasks/summary. While they're still tools,
	// they need allowlist entries below.

	// Tasks — file-local state, no bundle content involvement.
	"task_list":       {},
	"task_add":        {},
	"task_set_status": {},
}

// installReviewMiddleware wires the gate-and-prepend behavior into the SDK
// server. Runs for every incoming method; only tools/call is gated.
//
// Two responsibilities, both bound to s.review:
//
//  1. Gate: when s.review.hasPending(), reject any tools/call whose name
//     isn't in allowedDuringReview by returning a CallToolResult that
//     renders the review template + a "blocked" line. The inner handler
//     never runs — no partial data leaks past the gate.
//
//  2. Prepend: for allowed calls during a pending review, append the
//     rendered template to the result's Content so the protocol stays
//     visible on every tool turn until acknowledged.
func (s *ctxServer) installReviewMiddleware(server *mcp.Server) {
	server.AddReceivingMiddleware(s.reviewMiddleware())
}

// reviewMiddleware returns the middleware function. Kept as a separate
// method (rather than inlined into install) so tests can wrap a fake
// handler with the same gate logic.
func (s *ctxServer) reviewMiddleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, req)
			}
			pending := s.review.snapshot()
			if pending.IsEmpty() {
				return next(ctx, method, req)
			}

			toolName := extractToolName(req)

			if _, ok := allowedDuringReview[toolName]; !ok {
				return blockedToolResult(toolName, pending), nil
			}

			result, err := next(ctx, method, req)
			if err != nil {
				return result, err
			}
			// Prepend the rendered template to the result content so the
			// protocol stays visible. Only TextContent gets the prepend;
			// embedded resources, images, etc. pass through unchanged.
			if ctr, ok := result.(*mcp.CallToolResult); ok && ctr != nil {
				ctr.Content = append(
					[]mcp.Content{&mcp.TextContent{Text: renderReviewTemplate(pending)}},
					ctr.Content...,
				)
			}
			return result, nil
		}
	}
}

// extractToolName pulls the tool name out of a tools/call request. The SDK
// uses either CallToolParams or CallToolParamsRaw depending on whether the
// arguments have been decoded yet — both expose Name as a field.
func extractToolName(req mcp.Request) string {
	switch p := req.GetParams().(type) {
	case *mcp.CallToolParams:
		return p.Name
	case *mcp.CallToolParamsRaw:
		return p.Name
	default:
		return ""
	}
}

// blockedToolResult builds the response returned for a gated tool call.
// Content is the rendered review template + a one-line suffix naming the
// blocked tool. IsError = true so clients render it as a failure rather
// than as a normal result, but the message itself is the user-facing
// instruction (echo the template, then pick a response).
func blockedToolResult(toolName string, pending *operations.BundleChangeSet) *mcp.CallToolResult {
	body := renderReviewTemplate(pending)
	body += fmt.Sprintf("\n(tool %q is blocked until the bundle review is acknowledged)", toolName)
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: body}},
		IsError: true,
	}
}
