package bundles

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// A LINK GROUP is one delivery unit inside a bundle. Items that share a
// `ctxloom:link_id=<name>` tag deliver together or not at all: when any MCP
// member of the group did not reach the run's granted set, every other member
// is withheld, so a fragment or skill is never delivered beside a tool it
// depends on that is not there.
//
// The link is a VALUE in the tag field every item already carries — the
// selection surface the host evaluates and the agent never sees — rather than
// a new field, so it widens no preimage and invalidates no approval. The cost
// of that shape is that a link is two edits that must agree, which is why
// checkLinks refuses a one-sided link at parse.
//
// The group key is the PAIR (bundle, id): LinkGroups is a method on one
// Bundle, so two bundles that both say `tooling` form two groups and one
// author's withheld server can never withhold another author's prose.

// linkTagKey is the tag namespace a link id rides under. ParseLinkTag is the
// only reader of this spelling.
const linkTagKey = "ctxloom:link_id"

// ParseLinkTag reports the link id a tag carries, and whether the tag is a
// link tag at all. An empty id is not a link.
func ParseLinkTag(tag string) (string, bool) {
	key, id, found := strings.Cut(tag, "=")
	if !found || key != linkTagKey || id == "" {
		return "", false
	}
	return id, true
}

// LinkIDs returns the link ids among tags, deduplicated, in first-seen order.
// nil when there are none, which is the common case.
func LinkIDs(tags []string) []string {
	var ids []string
	for _, tag := range tags {
		id, ok := ParseLinkTag(tag)
		if ok && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// LinkMember names one item of a link group by kind and bare name.
type LinkMember struct {
	Kind trust.ItemKind
	Name string
}

func (m LinkMember) String() string { return string(m.Kind) + "/" + m.Name }

// LinkGroup is one delivery unit: every member delivers, or none does.
type LinkGroup struct {
	ID      string
	Members []LinkMember
}

// MCPMembers returns the names of the group's MCP-server members — the
// members whose absence from a run withholds the rest — in name order.
func (g LinkGroup) MCPMembers() []string {
	var names []string
	for _, m := range g.Members {
		if m.Kind == trust.KindMCP {
			names = append(names, m.Name)
		}
	}
	return names
}

// LinkGroups returns the bundle's link groups keyed by id, each member list in
// (kind, name) order. Membership is computed from each item's EFFECTIVE tags —
// bundle tags merged onto the item's, the same merge the reader applies to
// the tags it emits — so a bundle-level link tag binds every item and the
// group seen here is the group the pipeline sees on a delivered item.
//
// The members are the four deliverable item kinds. Hooks and profiles carry
// no tags and are not part of a link.
func (b *Bundle) LinkGroups() map[string]LinkGroup {
	groups := make(map[string]LinkGroup)
	join := func(kind trust.ItemKind, name string, itemTags []string) {
		for _, id := range LinkIDs(slices.Concat(b.Tags, itemTags)) {
			g := groups[id]
			g.ID = id
			g.Members = append(g.Members, LinkMember{Kind: kind, Name: name})
			groups[id] = g
		}
	}
	for name, f := range b.Fragments {
		join(trust.KindFragment, name, f.Tags)
	}
	for name, c := range b.Commands {
		join(trust.KindPrompt, name, c.Tags)
	}
	for name, s := range b.Skills {
		join(trust.KindSkill, name, s.Tags)
	}
	for name, m := range b.MCP {
		join(trust.KindMCP, name, m.Tags)
	}
	for id, g := range groups {
		sort.Slice(g.Members, func(i, j int) bool {
			if g.Members[i].Kind != g.Members[j].Kind {
				return g.Members[i].Kind < g.Members[j].Kind
			}
			return g.Members[i].Name < g.Members[j].Name
		})
		groups[id] = g
	}
	return groups
}

// checkLinks is the load-time finding that makes a two-sided tag acceptable:
// a link id carried by exactly one item is refused, naming the id and the
// lone member, because a dangling link is a typo on the other side and left
// alone it would silently deliver that item beside the tool it lacks. Every
// dangling id is named in the one error — a misspelled pair dangles on both
// sides, and the author should see both.
func (b *Bundle) checkLinks() error {
	groups := b.LinkGroups()
	ids := make([]string, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var dangling []string
	for _, id := range ids {
		if g := groups[id]; len(g.Members) == 1 {
			dangling = append(dangling, fmt.Sprintf("%s=%s only on %s", linkTagKey, id, g.Members[0]))
		}
	}
	if len(dangling) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s; a link binds two or more items in one bundle, so the other side is missing or misspelled",
		errs.ErrDanglingLink, strings.Join(dangling, ", "))
}

// LinkGrant answers the one question a link group asks of a run: did the MCP
// server `server`, AS SHIPPED BY the bundle `read`, reach the run's granted
// set? It is handed the read rather than a bare name so a same-named server
// from another bundle cannot answer for this one. config answers it from
// ResolveBundleMCPServers; a surface that is not a run says LinksUnchecked.
type LinkGrant interface {
	Granted(read BundleRead, server string) bool
}

// LinkGrantFunc adapts a plain function to LinkGrant.
type LinkGrantFunc func(read BundleRead, server string) bool

func (f LinkGrantFunc) Granted(read BundleRead, server string) bool { return f(read, server) }

// LinksUnchecked is the management/listing statement, spelled as a value: link
// groups are not consulted and every linked item resolves. It is for surfaces
// that answer "what exists" or an explicit by-name ask, never for assembling a
// run. A pipeline built with a nil grant withholds every linked item instead
// (see Pipeline.linkWithholds).
func LinksUnchecked() LinkGrant {
	return LinkGrantFunc(func(BundleRead, string) bool { return true })
}
