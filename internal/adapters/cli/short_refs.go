package cli

import (
	"context"
	"strings"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// shortRefProbe is the placeholder bundle path a remote's canonical prefix is
// derived with: the expansion of "<remote>/<probe>", less the probe.
const shortRefProbe = "p"

// shortRefs is the ONE way text output names a remote item: the label each
// ref is shown under, in the same order. A canonical ref inside a registered
// remote's bundles is shown as "<remote>/<bundle>[#...]" — what a user types,
// and what remote.CanonicalizeShortRef expands back to the identical ref,
// which is checked per ref rather than assumed. Two refs that would share a
// label are each shown canonically, so no label names something it does not
// resolve to; anything no remote names (a local bundle, an unregistered host)
// is shown as written. Structured output keeps canonical refs; only the text
// renderers call this.
func shortRefs(refs []string, remotes []operations.RemoteEntry) []string {
	urls := make(map[string]string, len(remotes))
	for _, r := range remotes {
		urls[r.Name] = r.URL
	}
	aliasURL := func(alias string) string { return urls[alias] }
	type prefix struct{ alias, canonical string }
	var prefixes []prefix
	for _, r := range remotes {
		probe := remote.CanonicalizeShortRef(r.Name+"/"+shortRefProbe, aliasURL, nil)
		if p, ok := strings.CutSuffix(probe, shortRefProbe); ok && p != r.Name+"/" {
			prefixes = append(prefixes, prefix{r.Name, p})
		}
	}

	labels := make([]string, len(refs))
	for i, ref := range refs {
		labels[i] = ref
		for _, p := range prefixes {
			rest, ok := strings.CutPrefix(ref, p.canonical)
			if !ok || rest == "" {
				continue
			}
			if short := p.alias + "/" + rest; remote.CanonicalizeShortRef(short, aliasURL, nil) == ref {
				labels[i] = short
				break
			}
		}
	}

	owners := make(map[string]map[string]bool, len(labels))
	for i, label := range labels {
		if owners[label] == nil {
			owners[label] = map[string]bool{}
		}
		owners[label][refs[i]] = true
	}
	for i, label := range labels {
		if len(owners[label]) > 1 {
			labels[i] = refs[i]
		}
	}
	return labels
}

// refLabeler is shortRefs over cfg's registered remotes, for a text renderer.
// A registry that cannot be read labels every ref as written: a listing must
// still list.
func refLabeler(ctx context.Context, cfg *config.Config) func([]string) []string {
	res, err := operations.ListRemotes(ctx, cfg, operations.ListRemotesRequest{})
	if err != nil {
		return func(refs []string) []string { return refs }
	}
	return func(refs []string) []string { return shortRefs(refs, res.Remotes) }
}
