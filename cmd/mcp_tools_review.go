package cmd

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// Review-flow tools. These are the only mutators that can clear
// bundleReviewState.pending. Each one ends by calling
// applyHooksIfNotPending so deferred hooks resume exactly when the last
// pending entry leaves.

type acknowledgeBundleReviewInput struct {
	// No fields — approving the whole review is the only operation; the
	// shape exists so the SDK generates a valid input schema.
}

type acknowledgeBundleReviewResult struct {
	Status  string `json:"status"`
	Merged  int    `json:"merged"`
	Message string `json:"message"`
}

type declineBundleInput struct {
	Name string `json:"name,omitempty" jsonschema:"Optional bundle name to decline (e.g. 'remote/bundle'). Omit to decline ALL pending changes; the active lockfile stays as-is and any new bundles are not installed."`
}

type declineBundleResult struct {
	Status    string   `json:"status"`
	Declined  []string `json:"declined"`
	Remaining int      `json:"remaining"`
	Message   string   `json:"message"`
}

type showBundleVerbatimInput struct {
	Name string `json:"name" jsonschema:"Bundle name (e.g. 'remote/bundle') to print. Read from the *pending* lockfile so you see the new SHA before approving."`
}

type showBundleVerbatimResult struct {
	Name    string `json:"name"`
	SHA     string `json:"sha"`
	Remote  string `json:"remote"`
	Content string `json:"content"`
}

type trustRemoteInput struct {
	Name  string `json:"name" jsonschema:"Remote name (as registered in remotes.yaml) to mark as trusted."`
	Trust bool   `json:"trust" jsonschema:"true to trust (auto-approve future bundle changes from this remote and approve any currently pending), false to untrust."`
}

type trustRemoteResult struct {
	Name      string   `json:"name"`
	Trust     bool     `json:"trust"`
	Approved  []string `json:"approved,omitempty"`
	Remaining int      `json:"remaining"`
	Message   string   `json:"message"`
}

func (s *ctxServer) registerReviewTools(server *mcp.Server) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "acknowledge_bundle_review",
			Description: "Approve every bundle change currently pending review. Merges lock.pending.yaml into lock.yaml and unblocks the gated tools.",
		},
		s.handleAcknowledgeBundleReview)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "decline_bundle",
			Description: "Decline pending bundle changes. With no name, drops ALL pending entries (modified bundles continue reading at the active SHA; new bundles are not installed). With a name, drops only that one.",
		},
		s.handleDeclineBundle)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "show_bundle_verbatim",
			Description: "Return the raw YAML of a pending bundle at the new SHA, so the user can review before approving. Does not mutate review state.",
		},
		s.handleShowBundleVerbatim)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "trust_remote",
			Description: "Mark a remote as trusted (trust=true) or untrusted (trust=false). When trusting, every currently pending bundle from that remote is auto-approved and the registry is persisted.",
		},
		s.handleTrustRemote)
}

func (s *ctxServer) handleAcknowledgeBundleReview(ctx context.Context, _ *mcp.CallToolRequest, _ acknowledgeBundleReviewInput) (*mcp.CallToolResult, *acknowledgeBundleReviewResult, error) {
	pending := s.review.snapshot()
	if pending.IsEmpty() {
		return nil, &acknowledgeBundleReviewResult{Status: "no_pending", Message: "No bundle review pending."}, nil
	}
	merged, err := operations.MergePendingLockfileCount(s.cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("merge pending lockfile: %w", err)
	}
	s.review.clear()
	s.applyHooksIfNotPending(ctx)
	return nil, &acknowledgeBundleReviewResult{
		Status:  "approved",
		Merged:  merged,
		Message: fmt.Sprintf("Approved %d bundle change(s). Hooks applied; gated tools are available.", merged),
	}, nil
}

func (s *ctxServer) handleDeclineBundle(ctx context.Context, _ *mcp.CallToolRequest, in declineBundleInput) (*mcp.CallToolResult, *declineBundleResult, error) {
	pending := s.review.snapshot()
	if pending.IsEmpty() {
		return nil, &declineBundleResult{Status: "no_pending", Message: "No bundle review pending."}, nil
	}

	if in.Name == "" {
		declined := make([]string, 0, len(pending.All()))
		for _, c := range pending.All() {
			declined = append(declined, c.Name)
		}
		sort.Strings(declined)
		if err := operations.ClearPendingLockfile(s.cfg); err != nil {
			return nil, nil, fmt.Errorf("clear pending lockfile: %w", err)
		}
		s.review.clear()
		s.applyHooksIfNotPending(ctx)
		return nil, &declineBundleResult{
			Status:    "declined_all",
			Declined:  declined,
			Remaining: 0,
			Message:   fmt.Sprintf("Declined %d bundle change(s). Active lockfile unchanged; gated tools are available.", len(declined)),
		}, nil
	}

	// Single-bundle decline.
	found, err := operations.DropPendingBundle(s.cfg, in.Name)
	if err != nil {
		return nil, nil, fmt.Errorf("drop pending bundle: %w", err)
	}
	if !found {
		return nil, &declineBundleResult{
			Status:    "not_pending",
			Remaining: len(pending.All()),
			Message:   fmt.Sprintf("%q is not in the pending lockfile.", in.Name),
		}, nil
	}
	s.review.removeBundle(in.Name)
	s.applyHooksIfNotPending(ctx)
	remaining := 0
	if cs := s.review.snapshot(); cs != nil {
		remaining = len(cs.All())
	}
	return nil, &declineBundleResult{
		Status:    "declined",
		Declined:  []string{in.Name},
		Remaining: remaining,
		Message:   fmt.Sprintf("Declined %q. %d change(s) still pending.", in.Name, remaining),
	}, nil
}

func (s *ctxServer) handleShowBundleVerbatim(ctx context.Context, _ *mcp.CallToolRequest, in showBundleVerbatimInput) (*mcp.CallToolResult, *showBundleVerbatimResult, error) {
	if in.Name == "" {
		return nil, nil, fmt.Errorf("name is required")
	}
	pendingLock, err := operations.LoadPendingLockfile(s.cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("load pending lockfile: %w", err)
	}
	if pendingLock == nil {
		return nil, nil, fmt.Errorf("no pending lockfile — nothing to show")
	}
	reader := operations.NewBundleReaderForLockfile(s.cfg, pendingLock)
	if reader == nil {
		return nil, nil, fmt.Errorf("could not construct bundle reader")
	}
	data, err := reader.ReadBundleBytes(ctx, in.Name)
	if err != nil {
		return nil, nil, err
	}
	entry, _ := reader.LockEntryFor(in.Name)
	remoteName, _, _ := strings.Cut(in.Name, "/")
	return nil, &showBundleVerbatimResult{
		Name:    in.Name,
		SHA:     entry.SHA,
		Remote:  remoteName,
		Content: string(data),
	}, nil
}

func (s *ctxServer) handleTrustRemote(ctx context.Context, _ *mcp.CallToolRequest, in trustRemoteInput) (*mcp.CallToolResult, *trustRemoteResult, error) {
	if in.Name == "" {
		return nil, nil, fmt.Errorf("name is required")
	}
	registry, err := operations.GetRegistry(s.cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("load registry: %w", err)
	}
	if err := registry.SetTrustBundles(in.Name, in.Trust); err != nil {
		return nil, nil, err
	}

	if !in.Trust {
		return nil, &trustRemoteResult{
			Name:    in.Name,
			Trust:   false,
			Message: fmt.Sprintf("Remote %q is no longer trusted; future bundle changes from it will surface in review.", in.Name),
		}, nil
	}

	// Trusting: auto-approve every pending entry from this remote.
	removed := s.review.removeRemote(in.Name)
	names := make([]string, 0, len(removed))
	for _, c := range removed {
		names = append(names, c.Name)
	}
	if len(names) > 0 {
		if err := operations.PromotePendingBundles(s.cfg, names); err != nil {
			return nil, nil, fmt.Errorf("promote pending bundles for %s: %w", in.Name, err)
		}
	}
	s.applyHooksIfNotPending(ctx)

	remaining := 0
	if cs := s.review.snapshot(); cs != nil {
		remaining = len(cs.All())
	}
	sort.Strings(names)
	return nil, &trustRemoteResult{
		Name:      in.Name,
		Trust:     true,
		Approved:  names,
		Remaining: remaining,
		Message:   fmt.Sprintf("Remote %q trusted; auto-approved %d pending bundle(s); %d change(s) still pending.", in.Name, len(names), remaining),
	}, nil
}
