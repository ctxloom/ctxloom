package cmd

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// applyHooksInput's RegenerateContext is a pointer so the caller can
// distinguish "use default (true)" from explicit false. The bare bool
// would conflate "not provided" with "false".
type applyHooksInput struct {
	Backend           string `json:"backend,omitempty" jsonschema:"Which backend to apply hooks for (one of: claude-code, gemini, all; default: all)"`
	RegenerateContext *bool  `json:"regenerate_context,omitempty" jsonschema:"Also regenerate the context file (default: true)"`
}

// syncDependenciesInput uses pointer-bool for Lock and ApplyHooks for
// the same reason: their legacy defaults are true, not the zero value.
type syncDependenciesInput struct {
	Profiles   []string `json:"profiles,omitempty" jsonschema:"Specific profiles to sync (default: all profiles)"`
	Force      bool     `json:"force,omitempty" jsonschema:"Re-pull even if already installed (default: false)"`
	Lock       *bool    `json:"lock,omitempty" jsonschema:"Update lockfile after sync (default: true)"`
	ApplyHooks *bool    `json:"apply_hooks,omitempty" jsonschema:"Apply hooks after sync (default: true)"`
}

func (s *ctxServer) registerHooksTools(server *mcp.Server) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "apply_hooks",
			Description: "Apply/reapply ctxloom hooks to backend configuration files (.claude/settings.json, .gemini/settings.json)",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in applyHooksInput) (*mcp.CallToolResult, *operations.ApplyHooksResult, error) {
			regenerate := true
			if in.RegenerateContext != nil {
				regenerate = *in.RegenerateContext
			}
			result, err := operations.ApplyHooks(ctx, s.cfg, operations.ApplyHooksRequest{
				Backend:           in.Backend,
				RegenerateContext: regenerate,
			})
			return nil, result, err
		})
}

func (s *ctxServer) registerSyncTools(server *mcp.Server) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "sync_dependencies",
			Description: "Sync remote bundles and profiles referenced in config. Automatically fetches missing dependencies, updates lockfile, and applies hooks.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in syncDependenciesInput) (*mcp.CallToolResult, *operations.SyncDependenciesResult, error) {
			lock := true
			if in.Lock != nil {
				lock = *in.Lock
			}
			wantHooks := true
			if in.ApplyHooks != nil {
				wantHooks = *in.ApplyHooks
			}

			// SECURITY: never let SyncDependencies blind-apply hooks. Bundle-
			// shipped hooks are arbitrary code and must clear the same
			// bundle-review gate the startup path enforces — otherwise a
			// runtime sync that pulls a new/untrusted bundle would run its
			// hooks before the user ever sees the review template. We sync
			// with ApplyHooks=false, route any freshly-pulled changes through
			// the review state, and apply hooks ourselves only once nothing is
			// pending (mirrors mcp_server.go startup()).
			result, err := operations.SyncDependencies(ctx, s.cfg, operations.SyncDependenciesRequest{
				Profiles:   in.Profiles,
				Force:      in.Force,
				Lock:       lock,
				ApplyHooks: false,
			})
			if err != nil {
				return nil, result, err
			}

			s.handleSyncChanges(ctx, result.Changes)
			if wantHooks {
				s.applyHooksIfNotPending(ctx)
			}

			// If the sync surfaced changes needing review, return the review
			// template directly: the review middleware only prepends it when a
			// review was already pending at call entry, which it was not here.
			if pending := s.review.snapshot(); !pending.IsEmpty() {
				body := renderReviewTemplate(pending) + "\n" + result.Message
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: body}},
				}, nil, nil
			}
			return nil, result, nil
		})
}
