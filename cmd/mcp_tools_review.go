package cmd

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/remote"
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
	// OldSHA is non-empty when the bundle exists in the active lockfile
	// (the change is Modified, not Added). Diff summarizes the structural
	// delta between OldSHA and SHA — which fragments/prompts/mcp/hooks
	// were added, removed, or modified. Empty when OldSHA is empty (new
	// bundle: full Content is the only thing to review).
	OldSHA string `json:"old_sha,omitempty"`
	Diff   string `json:"diff,omitempty"`
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

type pinBundleInput struct {
	Name string `json:"name" jsonschema:"Bundle name (e.g. 'remote/bundle') to freeze at its active SHA. Future syncs still fetch new SHAs into pending, but the review template no longer surfaces them until you unpin."`
}

type pinBundleResult struct {
	Name    string `json:"name"`
	Pinned  bool   `json:"pinned"`
	SHA     string `json:"sha,omitempty"`
	Message string `json:"message"`
}

type unpinBundleInput struct {
	Name string `json:"name" jsonschema:"Bundle name (e.g. 'remote/bundle') to unpin. After unpinning, the next review will surface any accumulated SHA change for this bundle."`
}

type unpinBundleResult struct {
	Name    string `json:"name"`
	Pinned  bool   `json:"pinned"`
	Message string `json:"message"`
}

type approveRemotePendingInput struct {
	Remote string `json:"remote" jsonschema:"Remote name (as registered in remotes.yaml). Every pending bundle whose source remote matches is promoted into the active lockfile in one turn. Unlike trust_remote, this does NOT mark the remote as trusted for future syncs."`
}

type approveRemotePendingResult struct {
	Remote    string   `json:"remote"`
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

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "pin_bundle",
			Description: "Freeze a bundle at its current active SHA so future review prompts stop surfacing it. The lockfile entry's Pinned flag is set; SHA itself is unchanged. New SHAs still land in lock.pending.yaml for inspection, but the review template hides them. Reversible via unpin_bundle. Idempotent.",
		},
		s.handlePinBundle)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "unpin_bundle",
			Description: "Reverse a previous pin_bundle. After unpinning, any accumulated SHA change for this bundle will surface in the next review. Idempotent.",
		},
		s.handleUnpinBundle)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "approve_remote_pending",
			Description: "Approve every pending bundle change from one remote in a single turn. Smaller-scope than trust_remote: this only acts on what's currently pending and does NOT persist any trust setting — future syncs from this remote still surface in review.",
		},
		s.handleApproveRemotePending)
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
	newData, err := reader.ReadBundleBytes(ctx, in.Name)
	if err != nil {
		return nil, nil, err
	}
	entry, _ := reader.LockEntryFor(in.Name)
	remoteName, _, _ := strings.Cut(in.Name, "/")

	result := &showBundleVerbatimResult{
		Name:    in.Name,
		SHA:     entry.SHA,
		Remote:  remoteName,
		Content: string(newData),
	}

	// If the bundle is also in the active lockfile, compute a structural
	// diff between the two parsed bundle YAMLs. Failure to load the old
	// side is non-fatal — we just skip the diff and return raw Content.
	active, err := operations.LoadActiveLockfile(s.cfg)
	if err == nil && active != nil {
		if oldEntry, ok := active.GetEntry(remote.ItemTypeBundle, in.Name); ok && oldEntry.SHA != "" {
			oldReader := operations.NewBundleReaderForLockfile(s.cfg, active)
			if oldReader != nil {
				if oldData, err := oldReader.ReadBundleBytes(ctx, in.Name); err == nil {
					result.OldSHA = oldEntry.SHA
					result.Diff = diffBundleYAMLs(oldData, newData)
				}
			}
		}
	}

	return nil, result, nil
}

// diffBundleYAMLs parses two bundle YAML blobs and returns a short
// human-readable summary of what's structurally different. Used by
// show_bundle_verbatim's Diff field so a reviewer can grok scope before
// reading raw content. Returns "(parse error)" if either side fails to
// unmarshal — the caller can still fall back to the raw Content.
func diffBundleYAMLs(oldData, newData []byte) string {
	var oldB, newB bundlesYAML
	if err := yaml.Unmarshal(oldData, &oldB); err != nil {
		return "(parse error on old bundle)"
	}
	if err := yaml.Unmarshal(newData, &newB); err != nil {
		return "(parse error on new bundle)"
	}
	var sb strings.Builder
	writeSection(&sb, "Fragments", oldB.Fragments, newB.Fragments)
	writeSection(&sb, "Prompts", oldB.Prompts, newB.Prompts)
	writeSection(&sb, "MCP servers", oldB.MCP, newB.MCP)
	writeHookCounts(&sb, oldB.Hooks, newB.Hooks)
	if sb.Len() == 0 {
		return "(no structural changes; SHA change is metadata-only)"
	}
	return sb.String()
}

// bundlesYAML is the minimal shape diffBundleYAMLs needs to compute a
// structural delta. It's intentionally not the full internal/bundles.Bundle
// type: the diff cares only about which keys exist and whether their
// content text matches, not the rich fields.
type bundlesYAML struct {
	Fragments map[string]yamlBlob       `yaml:"fragments"`
	Prompts   map[string]yamlBlob       `yaml:"prompts"`
	MCP       map[string]yamlBlob       `yaml:"mcp"`
	Hooks     map[string][]yamlBlob     `yaml:"hooks"`
}

type yamlBlob struct {
	Content string `yaml:"content"`
	Command string `yaml:"command"` // MCP servers and hooks
}

// writeSection emits "+name", "- name", "~ name" lines for entries
// added, removed, or modified between two named-blob maps. Suppresses
// the section header when there are no changes to keep the diff short.
func writeSection(sb *strings.Builder, label string, oldMap, newMap map[string]yamlBlob) {
	var added, removed, modified []string
	for name := range newMap {
		if _, ok := oldMap[name]; !ok {
			added = append(added, name)
		} else if oldMap[name] != newMap[name] {
			modified = append(modified, name)
		}
	}
	for name := range oldMap {
		if _, ok := newMap[name]; !ok {
			removed = append(removed, name)
		}
	}
	if len(added)+len(removed)+len(modified) == 0 {
		return
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(modified)
	fmt.Fprintf(sb, "%s:\n", label)
	for _, n := range added {
		fmt.Fprintf(sb, "  + %s\n", n)
	}
	for _, n := range removed {
		fmt.Fprintf(sb, "  - %s\n", n)
	}
	for _, n := range modified {
		fmt.Fprintf(sb, "  ~ %s\n", n)
	}
}

// writeHookCounts summarizes hook deltas per event. Hooks don't have
// stable identifiers — they're an ordered list per event — so we report
// counts rather than per-entry adds/removes. A hook count change is the
// most security-relevant kind of diff (each hook is arbitrary code).
func writeHookCounts(sb *strings.Builder, oldHooks, newHooks map[string][]yamlBlob) {
	events := make(map[string]struct{})
	for k := range oldHooks {
		events[k] = struct{}{}
	}
	for k := range newHooks {
		events[k] = struct{}{}
	}
	var changed []string
	for event := range events {
		if len(oldHooks[event]) != len(newHooks[event]) {
			changed = append(changed, fmt.Sprintf("%s: %d → %d", event, len(oldHooks[event]), len(newHooks[event])))
		}
	}
	if len(changed) == 0 {
		return
	}
	sort.Strings(changed)
	sb.WriteString("Hooks:\n")
	for _, line := range changed {
		fmt.Fprintf(sb, "  ~ %s\n", line)
	}
}

func (s *ctxServer) handlePinBundle(ctx context.Context, _ *mcp.CallToolRequest, in pinBundleInput) (*mcp.CallToolResult, *pinBundleResult, error) {
	if in.Name == "" {
		return nil, nil, fmt.Errorf("name is required")
	}
	// Drop from the pending set so the user isn't asked to review the
	// same change they just pinned away. Also drop from pending lockfile
	// for the same reason — a future sync may re-add it.
	s.review.removeBundle(in.Name)
	_, _ = operations.DropPendingBundle(s.cfg, in.Name)

	found, err := operations.SetBundlePin(s.cfg, in.Name, true)
	if err != nil {
		return nil, nil, err
	}
	if !found {
		return nil, &pinBundleResult{
			Name:    in.Name,
			Pinned:  false,
			Message: fmt.Sprintf("%q is not in the active lockfile; nothing to pin.", in.Name),
		}, nil
	}
	// Pin may have just cleared the last pending entry; resume deferred
	// hooks if so.
	s.applyHooksIfNotPending(ctx)

	sha := ""
	if active, err := operations.LoadActiveLockfile(s.cfg); err == nil && active != nil {
		if entry, ok := active.GetEntry(remote.ItemTypeBundle, in.Name); ok {
			sha = entry.SHA
		}
	}
	return nil, &pinBundleResult{
		Name:    in.Name,
		Pinned:  true,
		SHA:     sha,
		Message: fmt.Sprintf("Pinned %q at SHA %s. Future SHA changes will land in pending but the review template will hide them until you unpin.", in.Name, shortSHA(sha)),
	}, nil
}

func (s *ctxServer) handleUnpinBundle(_ context.Context, _ *mcp.CallToolRequest, in unpinBundleInput) (*mcp.CallToolResult, *unpinBundleResult, error) {
	if in.Name == "" {
		return nil, nil, fmt.Errorf("name is required")
	}
	found, err := operations.SetBundlePin(s.cfg, in.Name, false)
	if err != nil {
		return nil, nil, err
	}
	if !found {
		return nil, &unpinBundleResult{
			Name:    in.Name,
			Pinned:  false,
			Message: fmt.Sprintf("%q is not in the active lockfile; nothing to unpin.", in.Name),
		}, nil
	}
	return nil, &unpinBundleResult{
		Name:    in.Name,
		Pinned:  false,
		Message: fmt.Sprintf("Unpinned %q. Any accumulated SHA change will surface on the next review.", in.Name),
	}, nil
}

func (s *ctxServer) handleApproveRemotePending(ctx context.Context, _ *mcp.CallToolRequest, in approveRemotePendingInput) (*mcp.CallToolResult, *approveRemotePendingResult, error) {
	if in.Remote == "" {
		return nil, nil, fmt.Errorf("remote is required")
	}

	// Pull every pending entry whose source remote matches, in lock-step
	// with the in-memory review state.
	removed := s.review.removeRemote(in.Remote)
	names := make([]string, 0, len(removed))
	for _, c := range removed {
		names = append(names, c.Name)
	}
	if len(names) == 0 {
		return nil, &approveRemotePendingResult{
			Remote:  in.Remote,
			Message: fmt.Sprintf("No pending bundle changes from %q to approve.", in.Remote),
		}, nil
	}
	if err := operations.PromotePendingBundles(s.cfg, names); err != nil {
		return nil, nil, fmt.Errorf("promote pending bundles for %s: %w", in.Remote, err)
	}
	s.applyHooksIfNotPending(ctx)

	remaining := 0
	if cs := s.review.snapshot(); cs != nil {
		remaining = len(cs.All())
	}
	sort.Strings(names)
	return nil, &approveRemotePendingResult{
		Remote:    in.Remote,
		Approved:  names,
		Remaining: remaining,
		Message:   fmt.Sprintf("Approved %d pending bundle(s) from %q without trusting the remote; %d change(s) still pending.", len(names), in.Remote, remaining),
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
