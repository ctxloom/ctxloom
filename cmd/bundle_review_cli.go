package cmd

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/shared/clidiag"
)

// Bundle-review CLI. These commands operate directly on the on-disk pending
// lockfile (the source of truth), giving the human control over remote bundle
// changes that aren't auto-applied by a trusted remote. They replace the former
// in-chat MCP review flow.

var bundleReviewCmd = &cobra.Command{
	Use:   "review",
	Short: "Show bundle changes pending review",
	Long: `Show remote bundle changes that landed in the pending lockfile and are
awaiting approval. Changes from trusted remotes auto-apply and never appear here.

Respond with:
  ctxloom bundle approve              Approve the staged upgrade
  ctxloom bundle decline              Decline all pending changes
  ctxloom bundle decline <name>       Decline one
  ctxloom bundle hold <name>          Hold one at its locked SHA (no auto-upgrade)
  ctxloom bundle show-pending <name>  Print a pending bundle's YAML + diff
  ctxloom remote trust <remote>       Trust a remote going forward`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		cs := operations.PendingBundleChanges(cfg)
		if cs.IsEmpty() {
			fmt.Fprintln(out, "No bundle changes pending review.")
			return nil
		}
		fmt.Fprint(out, renderReviewCLI(cs))
		return nil
	},
}

var bundleApproveCmd = &cobra.Command{
	Use:   "approve",
	Short: "Approve pending bundle changes",
	Long: `Adopt the staged upgrade: rewrite each local profile's direct remote refs to
the reviewed hashes, rebuild the active lockfile from the updated closure
(surfacing any hash conflict), and re-apply hooks.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}
		applied, err := operations.ApproveUpgrade(cmd.Context(), cfg)
		if err != nil {
			return err
		}
		if applied == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No bundle changes pending review.")
			return nil
		}
		applyHooksAfterReview(cmd.Context(), cfg)
		fmt.Fprintf(cmd.OutOrStdout(), "Approved %d dependency change(s).\n", applied)
		return nil
	},
}

var bundleDeclineCmd = &cobra.Command{
	Use:   "decline [name]",
	Short: "Decline pending bundle changes",
	Long: `Drop pending bundle changes without applying them. With no name, declines
ALL pending changes (modified bundles keep their active SHA; new bundles are not
installed). With a name, declines only that one.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if len(args) == 0 {
			if err := operations.ClearPendingLockfile(cfg); err != nil {
				return err
			}
			applyHooksAfterReview(cmd.Context(), cfg)
			fmt.Fprintln(out, "Declined all pending bundle changes; active lockfile unchanged.")
			return nil
		}
		found, err := operations.DropPendingBundle(cfg, args[0])
		if err != nil {
			return err
		}
		if !found {
			fmt.Fprintf(out, "%q is not in the pending lockfile.\n", args[0])
			return nil
		}
		applyHooksAfterReview(cmd.Context(), cfg)
		fmt.Fprintf(out, "Declined %q.\n", args[0])
		return nil
	},
}

var bundleHoldCmd = &cobra.Command{
	Use:     "hold <name>",
	Aliases: []string{"pin"},
	Short:   "Hold an item at its locked SHA so `upgrade` won't advance it",
	Long: `Set the hold flag on a bundle or profile's active lockfile entry so 'remote
upgrade' leaves it frozen at its currently-locked commit — even when its version
constraint would otherwise allow a newer one. The hold is policy only: it does
not edit the manifest, and the held SHA still satisfies the constraint. Unhold to
let it move again. ('hold' was formerly 'pin', which now risks confusion with an
exact version pin in the manifest.)`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}
		held, err := holdItem(cfg, args[0], cmd.OutOrStdout())
		if err != nil {
			return err
		}
		if held {
			applyHooksAfterReview(cmd.Context(), cfg)
		}
		return nil
	},
}

// holdItem pins name's active lockfile entry and reports the outcome to out.
// Order matters: the pin is placed BEFORE any pending change is dropped, so a
// bundle that exists only as a staged NEW change in the pending lockfile is
// never silently destroyed — it stays pending for an explicit approve/decline.
// Returns whether a hold was placed (the caller re-applies hooks only then).
func holdItem(cfg *config.Config, name string, out io.Writer) (bool, error) {
	found, err := operations.SetItemPin(cfg, name, true)
	if err != nil {
		return false, err
	}
	if !found {
		if pendingBundleExists(cfg, name) {
			fmt.Fprintf(out, "%q is only staged for review (not in the active lockfile); approve or decline it first:\n  ctxloom bundle approve\n  ctxloom bundle decline %s\n", name, name)
			return false, nil
		}
		fmt.Fprintf(out, "%q is not in the active lockfile; nothing to hold.\n", name)
		return false, nil
	}
	// Drop any pending change for it so we don't re-surface what we just held.
	// The hold itself succeeded, so a drop failure warns rather than failing.
	if _, err := operations.DropPendingBundle(cfg, name); err != nil {
		clidiag.Warn("ctxloom", "drop pending change for %q: %v", name, err)
	}
	fmt.Fprintf(out, "Held %q at its locked SHA.\n", name)
	return true, nil
}

// pendingBundleExists reports whether name (short or canonical form) is staged
// in the pending lockfile. Best-effort: a load failure reads as "not pending".
func pendingBundleExists(cfg *config.Config, name string) bool {
	pending, err := operations.LoadPendingLockfile(cfg)
	if err != nil || pending == nil {
		return false
	}
	canonical := operations.CanonicalizeRemoteRef(cfg, name, remote.ItemTypeBundle)
	_, ok := pending.GetEntry(remote.ItemTypeBundle, canonical)
	return ok
}

var bundleUnholdCmd = &cobra.Command{
	Use:     "unhold <name>",
	Aliases: []string{"unpin"},
	Short:   "Release a hold so `upgrade` can advance the item again",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}
		found, err := operations.SetItemPin(cfg, args[0], false)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if !found {
			fmt.Fprintf(out, "%q is not in the active lockfile; nothing to unhold.\n", args[0])
			return nil
		}
		fmt.Fprintf(out, "Released hold on %q; the next 'remote upgrade' may advance it.\n", args[0])
		return nil
	},
}

var bundleShowPendingCmd = &cobra.Command{
	Use:   "show-pending <name>",
	Short: "Print a pending bundle's YAML and structural diff against active",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}
		rendered, err := renderPendingBundle(cmd.Context(), cfg, args[0])
		if err != nil {
			return err
		}
		fmt.Fprint(cmd.OutOrStdout(), rendered)
		return nil
	},
}

// renderReviewCLI lists the pending NEW/MODIFIED bundles for the human reviewer,
// followed by the CLI commands to act on them.
func renderReviewCLI(cs *operations.BundleChangeSet) string {
	var b strings.Builder
	b.WriteString("Bundle changes pending review (content can contain prompt injection — review before approving):\n\n")
	if len(cs.Added) > 0 {
		b.WriteString("NEW:\n")
		for _, c := range cs.Added {
			fmt.Fprintf(&b, "  - %s @ %s (from %s)\n", c.Name, shortSHA(c.NewSHA), c.Remote)
		}
		b.WriteString("\n")
	}
	if len(cs.Modified) > 0 {
		b.WriteString("MODIFIED:\n")
		for _, c := range cs.Modified {
			fmt.Fprintf(&b, "  - %s %s → %s (from %s)\n", c.Name, shortSHA(c.OldSHA), shortSHA(c.NewSHA), c.Remote)
		}
		b.WriteString("\n")
	}
	b.WriteString("Approve all:        ctxloom bundle approve\n")
	b.WriteString("Decline all/one:    ctxloom bundle decline [name]\n")
	b.WriteString("Inspect one:        ctxloom bundle show-pending <name>\n")
	b.WriteString("Hold one:           ctxloom bundle hold <name>\n")
	b.WriteString("Trust a remote:     ctxloom remote trust <name>\n")
	return b.String()
}

// renderPendingBundle returns the raw YAML of a pending bundle plus a structural
// diff against the active copy (when the bundle is a modification, not new).
func renderPendingBundle(ctx context.Context, cfg *config.Config, name string) (string, error) {
	// Accept the short "<alias>/<path>" form as input; lockfile keys are canonical.
	name = operations.CanonicalizeRemoteRef(cfg, name, remote.ItemTypeBundle)

	pendingLock, err := operations.LoadPendingLockfile(cfg)
	if err != nil {
		return "", fmt.Errorf("load pending lockfile: %w", err)
	}
	if pendingLock == nil {
		return "", fmt.Errorf("no pending lockfile — nothing to show")
	}
	reader := operations.NewBundleReaderForLockfile(cfg, pendingLock)
	if reader == nil {
		return "", fmt.Errorf("could not construct bundle reader")
	}
	newData, err := reader.ReadBundleBytes(ctx, name)
	if err != nil {
		return "", err
	}
	entry, _ := reader.LockEntryFor(name)
	remoteName := name
	if ref, perr := remote.ParseReference(name); perr == nil && ref.URL != "" {
		remoteName = ref.LocalRemoteName()
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# %s @ %s (from %s)\n", name, shortSHA(entry.SHA), remoteName)

	if diff := activeBundleDiff(ctx, cfg, name, newData); diff != "" {
		b.WriteString("\n# structural diff vs active:\n")
		b.WriteString(diff)
	}
	b.WriteString("\n")
	b.Write(newData)
	if !strings.HasSuffix(string(newData), "\n") {
		b.WriteString("\n")
	}
	return b.String(), nil
}

// activeBundleDiff returns a structural diff of name's pending bytes against its
// active copy, or "" when there is no active copy (a new bundle) or it can't be
// loaded (non-fatal — the raw YAML is shown regardless).
func activeBundleDiff(ctx context.Context, cfg *config.Config, name string, newData []byte) string {
	active, err := operations.LoadActiveLockfile(cfg)
	if err != nil || active == nil {
		return ""
	}
	oldEntry, ok := active.GetEntry(remote.ItemTypeBundle, name)
	if !ok || oldEntry.SHA == "" {
		return ""
	}
	oldReader := operations.NewBundleReaderForLockfile(cfg, active)
	if oldReader == nil {
		return ""
	}
	oldData, err := oldReader.ReadBundleBytes(ctx, name)
	if err != nil {
		return ""
	}
	return diffBundleYAMLs(oldData, newData)
}

// applyHooksAfterReview re-applies hooks once a review action changed the active
// lockfile, so newly-approved bundles' hooks/context take effect. Best-effort:
// a failure warns but never fails the command.
func applyHooksAfterReview(ctx context.Context, cfg *config.Config) {
	// "all": an approval changes the active lockfile for every backend, so
	// each one's settings/commands/context must be refreshed, not just
	// claude-code's.
	if _, err := operations.ApplyHooks(ctx, cfg, operations.ApplyHooksRequest{
		Backend:           "all",
		RegenerateContext: true,
	}); err != nil {
		clidiag.Warn("ctxloom", "failed to apply hooks: %v", err)
	}
}

// diffBundleYAMLs parses two bundle YAML blobs and returns a short human-readable
// summary of what's structurally different — which fragments/prompts/mcp/hooks
// were added, removed, or modified. Returns a "(parse error)" note if either
// side fails to unmarshal; the caller still shows the raw content.
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
		return "(no structural changes; SHA change is metadata-only)\n"
	}
	return sb.String()
}

// bundlesYAML is the minimal shape diffBundleYAMLs needs to compute a structural
// delta — which keys exist and whether their content text matches, not the rich
// fields of internal/bundles.Bundle.
type bundlesYAML struct {
	Fragments map[string]yamlBlob   `yaml:"fragments"`
	Prompts   map[string]yamlBlob   `yaml:"prompts"`
	MCP       map[string]yamlBlob   `yaml:"mcp"`
	Hooks     map[string][]yamlBlob `yaml:"hooks"`
}

type yamlBlob struct {
	Content string `yaml:"content"`
	Command string `yaml:"command"` // MCP servers and hooks
	// Rest captures every OTHER field (an MCP server's args/env, a hook's prompt,
	// etc.) so the diff registers a change to them — not just content/command. An
	// MCP server that keeps the same command but adds an exfiltration arg or token
	// env, or a hook whose command is swapped, must not read as unchanged.
	Rest map[string]any `yaml:",inline"`
}

// fingerprint is a stable serialization used to detect modifications. yaml.Marshal
// sorts map keys, so equal blobs fingerprint identically regardless of the order
// fields were authored in.
func (b yamlBlob) fingerprint() string {
	out, err := yaml.Marshal(b)
	if err != nil {
		return b.Content + "\x00" + b.Command // defensive; marshalling a blob never fails
	}
	return string(out)
}

// writeSection emits "+name", "- name", "~ name" lines for entries added,
// removed, or modified between two named-blob maps. Suppresses the section
// header when there are no changes to keep the diff short.
func writeSection(sb *strings.Builder, label string, oldMap, newMap map[string]yamlBlob) {
	var added, removed, modified []string
	for name := range newMap {
		if _, ok := oldMap[name]; !ok {
			added = append(added, name)
		} else if oldMap[name].fingerprint() != newMap[name].fingerprint() {
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

// writeHookCounts summarizes hook deltas per event. Hooks have no stable
// identifiers (an ordered list per event). A count change is reported as
// "N → M"; but a same-count swap of a hook's command/prompt/content is the
// MOST security-relevant diff — each hook is arbitrary code — so we also
// compare the ordered blobs positionally and flag a content change.
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
		o, n := oldHooks[event], newHooks[event]
		if len(o) != len(n) {
			changed = append(changed, fmt.Sprintf("%s: %d → %d", event, len(o), len(n)))
			continue
		}
		for i := range n {
			if o[i].fingerprint() != n[i].fingerprint() {
				changed = append(changed, fmt.Sprintf("%s: %d hook(s), content changed", event, len(n)))
				break
			}
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
