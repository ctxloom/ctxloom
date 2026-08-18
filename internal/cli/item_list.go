package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// The `fragment list` / `command list` read path: the normalized listing row
// every frontend consumes, its remote/bundle classification, and the human
// renderer.

// itemRow is the normalized listing shape shared by the fragment and prompt
// listings: a name, its merged tags, the bundle it came from (the grouping
// key), and the fully-qualified ref (bundle#fragments/name or
// bundle#commands/name) that `show` and assemble accept. It flattens the
// operations FragmentEntry / CommandEntry projections so the grouping/printing
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
		// name/source are bundle-authored (a fragment/command key from the
		// bundle's own YAML) and reach this listing row without having
		// passed through remote.NormalizeRef — the same display-surface gap
		// review.go's classify() had (see its comment). Strip (not
		// NormalizeRef) so a malicious name cannot repaint `fragment/command
		// list` output; this is a listing, not an ingest boundary, so it does
		// not own the loud warning.
		cleanName, _ := remote.StripRefControlChars(name)
		cleanSource, _ := remote.StripRefControlChars(source)
		return itemRow{
			Name:        cleanName,
			Tags:        tags,
			Bundle:      cleanSource,
			Ref:         remote.NormalizeRef(source + "#" + itemRefPrefix(itemType) + name),
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
	case ItemTypeCommand:
		res, err := operations.ListCommands(ctx, cfg, operations.ListCommandsRequest{SortBy: "source"})
		if err != nil {
			return nil, err
		}
		rows := make([]itemRow, 0, len(res.Commands))
		for _, p := range res.Commands {
			rows = append(rows, row(p.Name, p.Tags, p.Source))
		}
		return rows, nil
	}
	// Unreachable while ItemType has exactly two constants — but "success with
	// zero payload" is the wrong shape to leave behind for the third one: a
	// listing that cannot tell what it was asked to list must
	// say so, not return an empty list that reads as "there are none".
	return nil, fmt.Errorf("cannot list items: unrecognized item type %q", itemType)
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
// printItemInfos writes rows grouped by bundle to w. Takes an explicit
// writer (not bare fmt.Printf to the real os.Stdout) so it honors
// cmd.OutOrStdout() like every other renderer in this flow — a caller that
// redirects a command's output (a test, the VSCode companion capturing a
// subprocess, `ctxloom ... > file`) used to see nothing from this path even
// though the command itself reported success.
func printItemInfos(w io.Writer, rows []itemRow, itemType ItemType) {
	fmt.Fprintf(w, "%ss (%d):\n\n", titleCase(string(itemType)), len(rows))
	currentBundle := ""
	for _, r := range rows {
		if r.Bundle != currentBundle {
			if currentBundle != "" {
				fmt.Fprintln(w)
			}
			fmt.Fprintf(w, "  %s:\n", r.Bundle)
			currentBundle = r.Bundle
		}
		fmt.Fprintf(w, "    - %s", r.Name)
		if len(r.Tags) > 0 {
			fmt.Fprintf(w, " [%s]", strings.Join(r.Tags, ", "))
		}
		fmt.Fprintln(w)
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
	// A --bundle that names nothing is a typo, not an empty result.
	// The enumeration is only consulted when the filter matched nothing, and a
	// failure to enumerate leaves the listing alone rather than inventing a
	// verdict.
	if len(filtered) == 0 && bundleFilter != "" {
		if infos, lerr := operations.ListBundles(cmd.Context(), cfg); lerr == nil {
			known := make([]string, 0, len(infos))
			for _, b := range infos {
				known = append(known, b.Name)
			}
			if err := checkBundleFilter(bundleFilter, known); err != nil {
				return err
			}
		}
	}
	// Stamp effective trust only for the machine (json) surface: it materializes
	// and hashes each item, so the cheaper ref-only human listing stays unchanged.
	if outputFormatOf(cmd) == formatJSON {
		stampItemTrust(cfg, itemType, filtered)
	}
	return emit(cmd, filtered, func() error {
		// Tested on the FILTERED slice, not the unfiltered one: the
		// old `len(rows) == 0` was false whenever any item existed anywhere, so
		// a filter that matched nothing fell through to "Fragments (0):".
		if len(filtered) == 0 {
			out := cmd.OutOrStdout()
			if bundleFilter != "" {
				fmt.Fprintf(out, "No %ss in bundle %q (%d %ss exist in other bundles).\n", itemType, bundleFilter, len(rows), itemType)
				return nil
			}
			fmt.Fprintf(out, "No %ss found.\n", itemType)
			fmt.Fprintln(out, "Add remote bundles to a profile (ctxloom profile create/modify), then ctxloom deps pull")
			return nil
		}
		printItemInfos(cmd.OutOrStdout(), filtered, itemType)
		return nil
	})
}
