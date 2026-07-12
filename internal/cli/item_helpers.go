package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/signing/agentkey"
)

// titleCase capitalizes the first letter of a string.
func titleCase(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// ItemType distinguishes between fragments and prompts.
type ItemType string

const (
	ItemTypeFragment ItemType = "fragment"
	ItemTypeSkill    ItemType = "skill"
)

// itemPrefix returns the prefix used in references (e.g., "fragments/" or "skills/").
func (t ItemType) prefix() string {
	return string(t) + "s/"
}

// parseItemRef parses a reference like "bundle#fragments/name" or "bundle#skills/name".
func parseItemRef(ref string, itemType ItemType) (bundleName, itemName string, err error) {
	hashIdx := strings.Index(ref, "#")
	if hashIdx == -1 {
		return "", "", fmt.Errorf("invalid reference format: expected bundle#%sname (got %q)", itemType.prefix(), ref)
	}

	bundleName = ref[:hashIdx]
	itemPath := ref[hashIdx+1:]

	prefix := itemType.prefix()
	if !strings.HasPrefix(itemPath, prefix) {
		return "", "", fmt.Errorf("invalid reference format: expected bundle#%sname (got %q)", prefix, ref)
	}

	itemName = strings.TrimPrefix(itemPath, prefix)
	if itemName == "" {
		return "", "", fmt.Errorf("invalid reference: missing %s name", itemType)
	}

	return bundleName, itemName, nil
}

// itemRow is the normalized listing shape shared by the fragment and prompt
// listings: a name, its merged tags, the bundle it came from (the grouping
// key), and the fully-qualified ref (bundle#fragments/name or
// bundle#skills/name) that `show` and assemble accept. It flattens the
// operations FragmentEntry / SkillEntry projections so the grouping/printing
// logic is type-agnostic. The json tags are snake_case (matching session list)
// and expose `ref` so frontends don't have to reconstruct it from name+bundle.
type itemRow struct {
	Name   string   `json:"name"`
	Tags   []string `json:"tags"`
	Bundle string   `json:"bundle"`
	Ref    string   `json:"ref"`
	// Remote, BundleLabel and SourceURL are derived from the bundle ref by the
	// backend so a client can group content by its owning remote and show a short
	// bundle label without parsing the `@bundles/` ref grammar itself. Remote is
	// the owning remote's NAME ("" for local/project content or an unregistered
	// source); BundleLabel is the segment after `@bundles/` (or the whole source
	// for a bare local bundle); SourceURL is the repo URL ("" for local).
	Remote      string `json:"remote"`
	BundleLabel string `json:"bundle_label"`
	SourceURL   string `json:"source_url,omitempty"`
	// Trusted, TrustSource, and State are the effective-trust stamp: whether
	// the decision function currently exposes this item, which step decided it
	// (rejected|local|builtin|trusted-signer|accepted|pending), and the three-state
	// review rendering (pending|accepted|rejected — exempt allows render
	// accepted, with TrustSource saying why). They are populated only for
	// --format json (see stampItemTrust); the human listing is unchanged. An
	// item whose content cannot be resolved/hashed is stamped conservatively
	// (trusted=false, pending) rather than failing the listing — the stamp is
	// read-only and never enforces here.
	Trusted     bool   `json:"trusted"`
	TrustSource string `json:"trust_source"`
	State       string `json:"state"`
}

// remoteURLMap maps each configured remote's URL to its name (best-effort: a
// registry failure yields an empty map, leaving items untagged rather than
// failing the listing). It is the authoritative source→remote lookup the client
// used to do by string-matching bundle prefixes against `remote list`.
func remoteURLMap(cfg *config.Config) map[string]string {
	m := map[string]string{}
	res, err := operations.ListRemotes(context.Background(), cfg, operations.ListRemotesRequest{})
	if err != nil {
		return m
	}
	for _, r := range res.Remotes {
		if r.URL != "" {
			m[r.URL] = r.Name
		}
	}
	return m
}

// classifySource splits a bundle source ref into the display fields a client
// needs — owning remote name, short bundle label, and source URL — parsing the
// canonical ref grammar here so the client never has to. A source that isn't a
// canonical ref (a bare local bundle name) is reported as local: no remote/URL,
// label = the source unchanged.
func classifySource(source string, remotes map[string]string) (remoteName, bundleLabel, sourceURL string) {
	bundleLabel = source
	ref, err := remote.ParseReference(source)
	if err != nil {
		return "", bundleLabel, ""
	}
	if ref.Path != "" {
		bundleLabel = ref.Path
	}
	if !ref.IsLocal {
		sourceURL = ref.URL
		remoteName = remotes[ref.URL]
	}
	return remoteName, bundleLabel, sourceURL
}

// listItemRows returns every item of the given type via the operations
// read-path, grouped by bundle (SortBy:"source", since an entry's Source is its
// bundle name).
func listItemRows(cfg *config.Config, itemType ItemType) ([]itemRow, error) {
	ctx := context.Background()
	remotes := remoteURLMap(cfg)
	row := func(name string, tags []string, source string) itemRow {
		remoteName, bundleLabel, sourceURL := classifySource(source, remotes)
		return itemRow{
			Name:        name,
			Tags:        tags,
			Bundle:      source,
			Ref:         source + "#" + itemType.prefix() + name,
			Remote:      remoteName,
			BundleLabel: bundleLabel,
			SourceURL:   sourceURL,
		}
	}
	switch itemType {
	case ItemTypeFragment:
		res, err := operations.ListFragments(ctx, cfg, operations.ListFragmentsRequest{SortBy: "source"})
		if err != nil {
			return nil, err
		}
		rows := make([]itemRow, 0, len(res.Fragments))
		for _, f := range res.Fragments {
			rows = append(rows, row(f.Name, f.Tags, f.Source))
		}
		return rows, nil
	case ItemTypeSkill:
		res, err := operations.ListSkills(ctx, cfg, operations.ListSkillsRequest{SortBy: "source"})
		if err != nil {
			return nil, err
		}
		rows := make([]itemRow, 0, len(res.Skills))
		for _, p := range res.Skills {
			rows = append(rows, row(p.Name, p.Tags, p.Source))
		}
		return rows, nil
	}
	return nil, nil
}

// filterByBundle keeps only the items belonging to bundleFilter (or all when
// the filter is empty).
func filterByBundle(rows []itemRow, bundleFilter string) []itemRow {
	if bundleFilter == "" {
		return rows
	}
	var filtered []itemRow
	for _, r := range rows {
		if r.Bundle == bundleFilter {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// printItemInfos prints items grouped by bundle, with tags.
func printItemInfos(rows []itemRow, itemType ItemType) {
	fmt.Printf("%ss (%d):\n\n", titleCase(string(itemType)), len(rows))
	currentBundle := ""
	for _, r := range rows {
		if r.Bundle != currentBundle {
			if currentBundle != "" {
				fmt.Println()
			}
			fmt.Printf("  %s:\n", r.Bundle)
			currentBundle = r.Bundle
		}
		fmt.Printf("    - %s", r.Name)
		if len(r.Tags) > 0 {
			fmt.Printf(" [%s]", strings.Join(r.Tags, ", "))
		}
		fmt.Println()
	}
}

// stampItemTrust annotates each row with its effective trust. It builds a
// single TrustStamper for the whole listing — trust store and remote registry
// read once, each bundle materialized+hashed once via the shared loader cache —
// so the content-keyed stamp does not re-fetch per item. Per-item failures are
// swallowed by the stamper (conservative trusted=false), never crashing the
// listing.
func stampItemTrust(cfg *config.Config, itemType ItemType, rows []itemRow) {
	stamper := operations.NewTrustStamper(cfg)
	for i := range rows {
		res := stamper.ForRef(rows[i].Ref)
		rows[i].Trusted = res.Trusted()
		rows[i].TrustSource = string(res.Source)
		rows[i].State = string(res.State())
	}
}

// listItems lists all items of the given type, optionally filtered by bundle.
func listItems(cmd *cobra.Command, itemType ItemType, bundleFilter string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	rows, err := listItemRows(cfg, itemType)
	if err != nil {
		return fmt.Errorf("failed to list %ss: %w", itemType, err)
	}

	filtered := filterByBundle(rows, bundleFilter)
	if filtered == nil {
		filtered = []itemRow{}
	}
	// Stamp effective trust only for the machine (json) surface: it materializes
	// and hashes each item, so the cheaper ref-only human listing stays unchanged.
	if outputFormatOf(cmd) == formatJSON {
		stampItemTrust(cfg, itemType, filtered)
	}
	return emit(cmd, filtered, func() error {
		if len(rows) == 0 {
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "No %ss found.\n", itemType)
			fmt.Fprintln(out, "Add remote bundles to a profile (ctxloom profile create/modify), then ctxloom remote pull")
			return nil
		}
		printItemInfos(filtered, itemType)
		return nil
	})
}

// loadBundleForItem resolves an item reference and loads its bundle, returning
// the bundle, the item name, and the loaded config (needed by distill).
func loadBundleForItem(ref string, itemType ItemType) (*bundles.Bundle, string, *config.Config, error) {
	bundleName, itemName, err := parseItemRef(ref, itemType)
	if err != nil {
		return nil, "", nil, err
	}

	cfg, err := GetConfig()
	if err != nil {
		return nil, "", nil, fmt.Errorf("failed to load config: %w", err)
	}

	bundle, err := operations.GetBundle(cfg, bundleName)
	if err != nil {
		return nil, "", nil, fmt.Errorf("load bundle %q: %w", bundleName, err)
	}
	return bundle, itemName, cfg, nil
}

// itemDisplayContent returns the content and distilled text for an item,
// erroring (with the available-names list) when it doesn't exist.
func itemDisplayContent(bundle *bundles.Bundle, itemName string, itemType ItemType) (content, distilled string, err error) {
	switch itemType {
	case ItemTypeFragment:
		frag, exists := bundle.Fragments[itemName]
		if !exists {
			return "", "", fmt.Errorf("fragment not found: %s\n\nAvailable fragments: %s",
				itemName, strings.Join(bundle.FragmentNames(), ", "))
		}
		return frag.Content, frag.Distilled, nil
	case ItemTypeSkill:
		prompt, exists := bundle.Skills[itemName]
		if !exists {
			return "", "", fmt.Errorf("skill not found: %s\n\nAvailable skills: %s",
				itemName, strings.Join(bundle.PromptNames(), ", "))
		}
		return prompt.Content, prompt.Distilled, nil
	}
	return "", "", nil
}

// showItem displays the content of a specific item. With interactive set AND an
// interactive terminal, it then offers the TR4 trust review/marking surface; in
// any non-interactive context (piped, redirected, scripted, or -i unset) it
// behaves exactly as before — the content is written to cmd.OutOrStdout (os.Stdout
// by default) with no trust UI, so piped output is byte-for-byte unchanged.
func showItem(cmd *cobra.Command, ref string, itemType ItemType, showDistilled, interactive bool) error {
	bundle, itemName, cfg, err := loadBundleForItem(ref, itemType)
	if err != nil {
		return err
	}

	content, distilled, err := itemDisplayContent(bundle, itemName, itemType)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if showDistilled && distilled != "" {
		content = distilled
		fmt.Fprintln(out, "# (distilled version)")
	}

	fmt.Fprintf(out, "%s\n\n", itemName)
	fmt.Fprint(out, content)
	if !strings.HasSuffix(content, "\n") {
		fmt.Fprintln(out)
	}

	// TTY-gated interactive trust review (-i). The content above is emitted
	// identically whether or not -i is set, and all trust UI goes to stderr, so
	// non-interactive/piped output never sees the trust surface. Viewing never
	// trusts — only an explicit t/b choice mutates.
	if interactive && isInteractiveTerminal() {
		return offerItemTrust(cmd, cfg, ref)
	}
	return nil
}

// createItem creates a new item in a bundle. Thin wrapper over the
// frontend-agnostic operations.AddItem core.
func createItem(bundleName, itemName string, itemType ItemType) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	_, err = operations.AddItem(context.Background(), cfg, operations.AddItemRequest{
		Bundle:  bundleName,
		Kind:    operations.ItemKind(itemType),
		Name:    itemName,
		Content: "# " + itemName + "\n\nAdd content here.",
	})
	if err != nil {
		if errors.Is(err, operations.ErrItemExists) {
			return fmt.Errorf("%s already exists: %s", itemType, itemName)
		}
		return err
	}

	fmt.Printf("Created %s %q in bundle %q\n", itemType, itemName, bundleName)
	fmt.Printf("Edit with: ctxloom %s edit %s#%s%s\n", itemType, bundleName, itemType.prefix(), itemName)
	return nil
}

// deleteItem removes an item from a bundle. Thin wrapper over
// operations.DeleteItem.
func deleteItem(ref string, itemType ItemType) error {
	bundleName, itemName, err := parseItemRef(ref, itemType)
	if err != nil {
		return err
	}

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	_, err = operations.DeleteItem(context.Background(), cfg, operations.DeleteItemRequest{
		Bundle: bundleName,
		Kind:   operations.ItemKind(itemType),
		Name:   itemName,
	})
	if err != nil {
		if errors.Is(err, operations.ErrItemNotFound) {
			return fmt.Errorf("%s not found: %s", itemType, itemName)
		}
		return err
	}

	fmt.Printf("Deleted %s %q from bundle %q\n", itemType, itemName, bundleName)
	return nil
}

// editItem opens an item in the editor, then writes the new content back through
// the operations core. The $EDITOR round-trip is the only CLI-specific concern;
// the load, guard, distillation, and save all live in the library.
func editItem(ref string, itemType ItemType) error {
	bundleName, itemName, err := parseItemRef(ref, itemType)
	if err != nil {
		return err
	}

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	kind := operations.ItemKind(itemType)

	cur, err := operations.GetItemContent(context.Background(), cfg, operations.GetItemRequest{
		Bundle: bundleName,
		Kind:   kind,
		Name:   itemName,
	})
	if err != nil {
		return err
	}

	newContent, err := editInEditor(cfg, cur.Content, itemName+".md")
	if err != nil {
		return fmt.Errorf("editor failed: %w", err)
	}
	if newContent == cur.Content {
		fmt.Println("No changes made.")
		return nil
	}

	res, err := operations.SetItemContent(context.Background(), cfg, operations.SetItemContentRequest{
		Bundle:    bundleName,
		Kind:      kind,
		Name:      itemName,
		Content:   newContent,
		Distiller: newLLMDistiller(cfg),
	})
	if err != nil {
		return err
	}

	fmt.Printf("Updated %s %q in bundle %q", itemType, itemName, bundleName)
	if res.Distilled {
		fmt.Print(" (re-distilled)")
	}
	fmt.Println()
	printPushReminder(bundleName)
	return nil
}

// distillSource returns the content and distill flags for an item, erroring
// when it doesn't exist.
// distillItem (re)distills an item via the operations core; the CLI only
// supplies the LLM-backed Distiller and renders the outcome.
func distillItem(ref string, itemType ItemType, force bool) error {
	bundleName, itemName, err := parseItemRef(ref, itemType)
	if err != nil {
		return err
	}
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	res, err := operations.DistillItem(context.Background(), cfg, operations.DistillItemRequest{
		Bundle:    bundleName,
		Kind:      operations.ItemKind(itemType),
		Name:      itemName,
		Force:     force,
		Distiller: newLLMDistiller(cfg),
	})
	if err != nil {
		if errors.Is(err, operations.ErrItemNotFound) {
			return fmt.Errorf("%s not found: %s", itemType, itemName)
		}
		return err
	}

	if res.Status == "skipped" {
		switch res.Reason {
		case "no_distill":
			fmt.Printf("%s %q is marked as no_distill\n", titleCase(string(itemType)), itemName)
		case "unchanged":
			fmt.Printf("%s %q is already distilled and unchanged\n", titleCase(string(itemType)), itemName)
		case "no_distiller":
			fmt.Printf("no LLM plugin configured; cannot distill %s %q\n", itemType, itemName)
		}
		return nil
	}

	fmt.Printf("Distilled %s (%s)\n", itemName, res.ModelID)
	return nil
}

// pushBundle publishes the named bundle to a remote. Each step is an operations
// call — resolve the bundle path, resolve the target remote (an explicit
// override or inferred from the bundle's location), resolve whether/how to
// sign, then publish — so the CLI re-implements none of the push logic and
// the same path is reachable by any frontend.
//
// sign/noSign are the --sign/--no-sign flags (spec §7A.3): signing rides the
// producing action the way `git commit -S` does. sign.default in config
// makes every push sign unless --no-sign overrides it for one invocation;
// an explicit --sign always signs. Key discovery, and any failure to find a
// key, happens BEFORE any network call — a signing failure must never
// degrade to a silent unsigned publish (spec §7A.4, normative).
func pushBundle(cmd *cobra.Command, bundleName, remoteOverride string, createPR bool, message string, sign, noSign bool) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	return pushBundleCfg(cmd, cfg, agentkey.NewDiscoverer(), nil, bundleName, remoteOverride, createPR, message, sign, noSign)
}

// pushBundleCfg is the testable body of pushBundle: cfg, discoverer, and mgr
// are all DI'd (a real config.Config over a temp project, a fake
// agentkey.Discoverer — mirroring internal/cli/sign.go's runSign — and an
// optional PublishManager backed by a mock Publisher) so the
// --sign/--no-sign/sign.default composition is exercisable without a real
// config.Load(), git binary, ssh-agent, or network call. mgr==nil uses
// PushBundle's own default (a real, network-backed manager) — production's
// path.
func pushBundleCfg(cmd *cobra.Command, cfg *config.Config, discoverer *agentkey.Discoverer, mgr *remote.PublishManager, bundleName, remoteOverride string, createPR bool, message string, sign, noSign bool) error {
	if sign && noSign {
		return fmt.Errorf("--sign and --no-sign are mutually exclusive")
	}

	bundle, err := operations.GetBundle(cfg, bundleName)
	if err != nil {
		return fmt.Errorf("load bundle %q: %w", bundleName, err)
	}

	remoteName, err := operations.ResolveBundleRemote(cfg, bundle.Path, remoteOverride)
	if err != nil {
		return err
	}

	req := operations.PushBundleRequest{
		Path:           bundle.Path,
		Remote:         remoteName,
		Message:        message,
		CreatePR:       createPR,
		PublishManager: mgr,
	}
	if sign || (cfg.ShouldSignByDefault() && !noSign) {
		discovered, err := discoverer.Discover(cmd.Context(), cfg.SignKey())
		if err != nil {
			return err
		}
		req.Signer = discovered.Signer
	}

	result, err := operations.PushBundle(cmd.Context(), cfg, req)
	if err != nil {
		return err
	}

	return emit(cmd, result, func() error { return printPushResult(cmd.OutOrStdout(), result) })
}

// printPushResult renders a push outcome for humans.
func printPushResult(w io.Writer, r *operations.PushBundleResult) error {
	if r.Status == "pr-created" {
		_, err := fmt.Fprintf(w, "Created pull request: %s\n", r.PRURL)
		return err
	}
	if _, err := fmt.Fprintf(w, "Pushed %s to %s\n", r.TargetPath, r.Remote); err != nil {
		return err
	}
	if r.CommitSHA != "" {
		if _, err := fmt.Fprintf(w, "Commit: %s\n", shortSHA(r.CommitSHA)); err != nil {
			return err
		}
	}
	if r.Signed {
		if _, err := fmt.Fprintln(w, "Signed: yes"); err != nil {
			return err
		}
	}
	return nil
}
