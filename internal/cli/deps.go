package cli

import (
	"context"
	"fmt"
	"io"
	"path"
	"sort"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/gitutil"
)

// `deps` is the LOCAL CLOSURE: what this project has installed, at which
// commit, and everything that moves it. `remote` is the REGISTERED SOURCE:
// which repositories exist to draw from.
//
// The split is a split of QUESTIONS. "Where does content come from" is
// answered by a file of URLs and never touches the network. "What do I have"
// is answered by the lockfile, is a property of this checkout, and is what
// every install, upgrade and freeze acts on. Under one noun those read as the
// same subject, and a verb phrased "<source-noun> pull" reads as "pull the
// source" — which is not what installing a closure does. `deps pull` takes no
// remote argument at all: it acts on everything this project's profiles name,
// across every remote at once.
//
// Bare `ctxloom deps` lists the closure: the most-typed spelling answers the
// most common question, and reading the lockfile touches nothing.
var depsCmd = groupNodeDefault(&cobra.Command{
	Use:   "deps",
	Short: "Manage this project's installed dependency closure",
	Long: `Install, inspect and advance the remote content this project depends on.

A profile names a remote bundle; the closure is every bundle that follows from
those names. The lockfile pins each one to a resolved commit, so two checkouts
of the same project install the same bytes.

  ctxloom deps list                      What is installed, at which commit
  ctxloom deps pull                      Make the installation match upstream
  ctxloom deps check                     Report what a newer commit is available for
  ctxloom deps upgrade                   Advance pins to the newest commit each constraint allows
  ctxloom deps hold <name>               Freeze one dependency at its locked commit
  ctxloom deps unhold <name>             Let it move again

Where content comes FROM is the other noun: 'ctxloom remote --help'.

Pulling a dependency does not expose it to your assistant. Content from an
untrusted source is withheld until you accept it with 'ctxloom review'.`,
}, "list")

// installedDep is one row of the closure listing: the four facts that answer
// "what have I got" without a network call.
type installedDep struct {
	// Name is the bundle's short name — what a profile writes and what hold
	// takes as an argument.
	Name string `json:"name" yaml:"name" toml:"name"`
	// Ref is the canonical reference the lockfile keys on.
	Ref string `json:"ref" yaml:"ref" toml:"ref"`
	// SHA is the commit this dependency is installed at.
	SHA string `json:"sha" yaml:"sha" toml:"sha"`
	// Constraint is the version range or branch the manifest asked for; empty
	// means the default branch.
	Constraint string `json:"constraint,omitempty" yaml:"constraint,omitempty" toml:"constraint,omitempty"`
	// Held reports a hold freezing this entry against 'deps upgrade'.
	Held bool `json:"held" yaml:"held" toml:"held"`
	// Origin is the registered remote this came from, or "" when no registered
	// remote matches its URL.
	Origin string `json:"origin,omitempty" yaml:"origin,omitempty" toml:"origin,omitempty"`
	// URL is the repository the content is pulled from — the fallback identity
	// when Origin is empty.
	URL string `json:"url,omitempty" yaml:"url,omitempty" toml:"url,omitempty"`
}

// depsListing is the emitted shape of `deps list`.
type depsListing struct {
	Deps  []installedDep `json:"deps" yaml:"deps" toml:"deps"`
	Count int            `json:"count" yaml:"count" toml:"count"`
}

var depsListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List the installed dependency closure",
	Long: `Read the lockfile and report what this project has installed: each bundle, the
commit it sits on, the constraint it was resolved from, whether a hold freezes
it, and which registered remote it came from.

OFFLINE, and deliberately. This is the one question you can still ask with no
network, an expired credential, or a remote that has been deleted — it reads
the lockfile and nothing else, so it answers when nothing else can.

What it therefore does NOT say is whether anything newer exists upstream; that
needs the network and is 'ctxloom deps check'.`,
	Args: cobra.NoArgs,
	RunE: runDepsList,
}

func runDepsList(cmd *cobra.Command, _ []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return err
	}

	listing, err := loadDepsListing(cmd.Context(), cfg)
	if err != nil {
		return err
	}

	return emit(cmd, listing, func() error {
		return renderDepsList(cmd.OutOrStdout(), listing)
	})
}

// loadDepsListing reads the active lockfile and labels each entry with the
// registered remote it came from.
//
// A registry that will not load is NOT fatal: the origin column is a
// convenience over an identity the lockfile already carries in full, and
// refusing the whole listing because remotes.yaml is unreadable would take
// away the one answer that still works when everything else is broken. The
// rows fall back to their URL.
func loadDepsListing(ctx context.Context, cfg *config.Config) (*depsListing, error) {
	lockfile, err := remote.NewLockfileManager(projectAppDir(cfg)).Load()
	if err != nil {
		return nil, err
	}

	origins := registeredOriginsByIdentity(ctx, cfg)

	listing := &depsListing{}
	for _, e := range lockfile.AllEntries() {
		dep := installedDep{
			Ref:        e.Ref,
			SHA:        e.Entry.SHA,
			Constraint: e.Entry.RequestedVersion,
			Held:       e.Entry.Held,
			URL:        e.Entry.URL,
		}
		if parsed, perr := remote.ParseReference(e.Ref); perr == nil {
			dep.Name = path.Base(parsed.Path)
			if dep.URL == "" {
				dep.URL = parsed.URL
			}
		}
		if dep.Name == "" {
			dep.Name = e.Ref
		}
		dep.Origin = origins[remote.NormalizeURL(dep.URL)]
		listing.Deps = append(listing.Deps, dep)
	}

	// Sorted by name so two runs over the same lockfile print the same thing;
	// AllEntries walks a map, whose order is deliberately randomized.
	sort.Slice(listing.Deps, func(i, j int) bool {
		if listing.Deps[i].Name != listing.Deps[j].Name {
			return listing.Deps[i].Name < listing.Deps[j].Name
		}
		return listing.Deps[i].Ref < listing.Deps[j].Ref
	})
	listing.Count = len(listing.Deps)
	return listing, nil
}

// registeredOriginsByIdentity maps each registered remote's normalized URL to
// its name, so a lockfile entry can be labelled with the remote a human knows
// it by. Keyed on the normalized identity because a remote registered as
// git@host:o/r.git and a lockfile entry recorded as https://host/o/r are one
// repository.
func registeredOriginsByIdentity(ctx context.Context, cfg *config.Config) map[string]string {
	origins := map[string]string{}
	listed, err := operations.ListRemotes(ctx, cfg, operations.ListRemotesRequest{})
	if err != nil {
		return origins
	}
	for _, r := range listed.Remotes {
		if id := remote.NormalizeURL(r.URL); id != "" {
			origins[id] = r.Name
		}
	}
	return origins
}

// renderDepsList writes the human listing: the empty-closure guidance, or one
// row per installed dependency. Split out of the RunE so the rendering can be
// driven from a value, without a project or a config load.
func renderDepsList(out io.Writer, listing *depsListing) error {
	if len(listing.Deps) == 0 {
		fmt.Fprintln(out, "No dependencies installed.")
		fmt.Fprintln(out, "A profile that names a remote bundle gets one; run 'ctxloom deps pull' to install it.")
		return nil
	}

	fmt.Fprintln(out, "Installed dependencies:")
	for _, d := range listing.Deps {
		fmt.Fprintf(out, "  %-20s %-10s %s%s\n",
			d.Name, gitutil.ShortSHA(d.SHA), depOrigin(d), depMarks(d))
	}
	return nil
}

// depOrigin names where a dependency came from: the registered remote when one
// matches, and otherwise the repository URL itself. Never blank — a blank
// origin column reads as "this is local", which is the one thing it cannot be.
func depOrigin(d installedDep) string {
	if d.Origin != "" {
		return d.Origin
	}
	if d.URL != "" {
		return d.URL
	}
	return d.Ref
}

// depMarks renders the row's trailing state. A hold is the one piece of local
// policy on an entry, and it changes what `deps upgrade` will do to it, so it
// has to be visible in the listing that answers "what have I got".
func depMarks(d installedDep) string {
	marks := ""
	if d.Constraint != "" {
		marks += "  (" + d.Constraint + ")"
	}
	if d.Held {
		marks += "  [held]"
	}
	return marks
}

func init() {
	rootCmd.AddCommand(depsCmd)

	depsCmd.AddCommand(depsListCmd)
}

// depsHoldCmd and depsUnholdCmd freeze and release one lockfile entry.
//
// A hold is LOCAL POLICY over the closure, which is why it lives here rather
// than on `bundle`: it edits the lockfile, not the bundle, and it changes what
// `deps upgrade` is allowed to do. It is deliberately not called "pin" —
// a manifest pin is an exact version the AUTHOR wrote, a hold is a freeze the
// INSTALLER applied, and the held SHA still satisfies whatever the constraint
// asked for.
var depsHoldCmd = &cobra.Command{
	Use:   "hold <name>",
	Short: "Freeze a dependency at its locked commit so `upgrade` cannot advance it",
	Long: `Set the hold flag on a bundle's active lockfile entry so 'ctxloom deps upgrade'
leaves it frozen at its currently-locked commit — even when its version
constraint would otherwise allow a newer one. The hold is policy only: it does
not edit the manifest, and the held commit still satisfies the constraint.
'ctxloom deps unhold' lets it move again.

A hold survives a 'deps pull' too, including a forced one: forcing a pull
re-resolves the reference exactly as an upgrade would, and a freeze that only
held against one of them would not be a freeze.`,
	Args: cobra.ExactArgs(1),
	RunE: runDepsHold,
}

var depsUnholdCmd = &cobra.Command{
	Use:   "unhold <name>",
	Short: "Release a hold so `upgrade` can advance the dependency again",
	Long: `Clear the hold flag on a bundle's active lockfile entry. The next
'ctxloom deps upgrade' may advance it to the newest commit its version
constraint allows.`,
	Args: cobra.ExactArgs(1),
	RunE: runDepsUnhold,
}

func runDepsHold(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return err
	}
	return holdItem(cfg, args[0], cmd.OutOrStdout(), cmd.ErrOrStderr())
}

func runDepsUnhold(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return err
	}
	found, err := operations.SetItemPin(cfg, args[0], false)
	if err != nil {
		return err
	}
	// See reportNothingToHold: a local bundle has nothing to release and
	// succeeds; a name that resolves to nothing is refused.
	if !found {
		return reportNothingToHold(cfg, args[0], "unhold", cmd.ErrOrStderr())
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Released hold on %q; the next 'ctxloom deps upgrade' may advance it.\n", args[0])
	return nil
}

func init() {
	depsCmd.AddCommand(depsHoldCmd)
	depsCmd.AddCommand(depsUnholdCmd)
}
