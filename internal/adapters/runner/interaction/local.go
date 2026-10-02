package interaction

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/shared/textblocks"
)

// The ctxloom:// resource URIs this endpoint serves. The catalog resources
// enumerate the Loadout's Index; the single-item templates read the item's
// body off the Loadout's Package.
const (
	resourceHelpURI      = "ctxloom://help"
	resourceFragmentsURI = operations.FragmentsResourceURI
	resourceCommandsURI  = "ctxloom://commands"
	resourceSkillsURI    = "ctxloom://skills"
)

// loadoutSurface is the runner's LocalSurface: the cell-local tools and the
// ctxloom:// resources over the Loadout the launch carried — the decoded
// Package and the catalog Index. The runner holds no config, so nothing here
// opens one: a fragment the package did not carry, or a profile or tag ask,
// is refused by name rather than resolved against a catalog the runner
// cannot see.
type loadoutSurface struct{ lo delivery.Loadout }

// Register implements LocalSurface.
func (s loadoutSurface) Register(server *mcp.Server) []string {
	s.registerResources(server)
	return s.registerContextTools(server)
}

// assembleContextInput's `Fragments` field is named `bundles` over the wire:
// the qualified refs the premise catalog (ctxloom://fragments) hands back.
type assembleContextInput struct {
	Profile   string   `json:"profile,omitempty" jsonschema:"Profile name to use"`
	Fragments []string `json:"bundles,omitempty" jsonschema:"Additional fragment names to include"`
	Tags      []string `json:"tags,omitempty" jsonschema:"Include all fragments with these tags"`
}

type searchContentInput struct {
	Query     string   `json:"query" jsonschema:"Search text (matches name, description, tags)"`
	Types     []string `json:"types,omitempty" jsonschema:"Content types to search (any of: fragment, command, skill, profile, mcp_server; default: all)"`
	Tags      []string `json:"tags,omitempty" jsonschema:"Filter by tags (fragments only)"`
	SortBy    string   `json:"sort_by,omitempty" jsonschema:"Sort field (one of: name, type, relevance; default: relevance)"`
	SortOrder string   `json:"sort_order,omitempty" jsonschema:"Sort order (one of: asc, desc; default: asc)"`
	Limit     int      `json:"limit,omitempty" jsonschema:"Maximum results to return (default: 50)"`
}

type searchRemotesInput struct {
	Query    string `json:"query" jsonschema:"Search text. Plain words match name/description/tags; use tag:NAME to match a tag (e.g. tag:golang, tag:docker)"`
	ItemType string `json:"item_type,omitempty" jsonschema:"Item type to search (currently only: bundle; profiles ship inside bundles)"`
}

// assembleContextOutput is assemble_context's wire shape: the assembled
// context and an honest account of what was and was not included.
type assembleContextOutput struct {
	Profiles         []string `json:"profiles"`
	Context          string   `json:"context"`
	FragmentsLoaded  []string `json:"fragments_loaded"`
	MissingFragments []string `json:"missing_fragments,omitempty"`
	MissingTags      []string `json:"missing_tags,omitempty"`
}

// errNotOnTheLoadout refuses an ask the Loadout cannot answer: the runner
// serves the launch's package, and a profile or tag selection is a catalog
// resolution the originator makes when it resolves a launch.
func errNotOnTheLoadout(what string) error {
	return fmt.Errorf("assemble_context: %s cannot be resolved inside a running session — the runner serves the package its launch carried; ask for fragments by the qualified refs ctxloom://fragments lists", what)
}

func (s loadoutSurface) registerContextTools(server *mcp.Server) []string {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "assemble_context",
			Description: "Assemble context from a profile, fragments, and/or tags. Returns the combined context that would be sent to an AI.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		func(_ context.Context, _ *mcp.CallToolRequest, in assembleContextInput) (*mcp.CallToolResult, *assembleContextOutput, error) {
			if in.Profile != "" {
				return nil, nil, errNotOnTheLoadout(fmt.Sprintf("profile %q", in.Profile))
			}
			if len(in.Tags) > 0 {
				return nil, nil, errNotOnTheLoadout(fmt.Sprintf("tags %v", in.Tags))
			}
			out := &assembleContextOutput{Profiles: s.lo.Package.Selection.Profiles}
			var bodies []string
			for _, ask := range in.Fragments {
				item, ok := s.fragment(ask)
				if !ok {
					out.MissingFragments = append(out.MissingFragments, ask)
					continue
				}
				out.FragmentsLoaded = append(out.FragmentsLoaded, item.Ref)
				bodies = append(bodies, item.Value.Body)
			}
			if len(out.MissingFragments) > 0 && len(out.FragmentsLoaded) == 0 {
				return nil, nil, fmt.Errorf("assemble_context: none of %v is carried by this session's package (the catalog is %s)", out.MissingFragments, resourceFragmentsURI)
			}
			out.Context = textblocks.Join(bodies...)
			return nil, out, nil
		})

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "search_content",
			Description: "Search across all ctxloom content types (fragments, commands, skills, profiles, MCP servers)",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		func(_ context.Context, _ *mcp.CallToolRequest, in searchContentInput) (*mcp.CallToolResult, *operations.SearchContentResult, error) {
			return nil, s.searchIndex(in), nil
		})

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "search_library",
			Description: "Search the library of installable bundles across configured remotes, reading their local git clones (no network). Use this for discovery — search_content only sees content already installed in this project. Returns each match's pull_ref for installing via the CLI.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		func(_ context.Context, _ *mcp.CallToolRequest, in searchRemotesInput) (*mcp.CallToolResult, *operations.SearchRemotesResult, error) {
			// The Index is the catalog the package was assembled from — every
			// installed bundle's items, by ref. A running session discovers
			// what its originator holds; it pulls nothing.
			out := &operations.SearchRemotesResult{Query: in.Query}
			for _, e := range s.lo.Index.Entries {
				if !matchesQuery(in.Query, e.Ref, e.Description, e.Premise) {
					continue
				}
				out.Results = append(out.Results, operations.SearchRemoteEntry{Name: e.Ref, Description: e.Description, PullRef: e.Ref})
			}
			out.Count = len(out.Results)
			return nil, out, nil
		})

	return []string{"assemble_context", "search_content", "search_library"}
}

// searchIndex answers search_content off the Index: refs, kinds,
// descriptions and premises. Profiles and MCP servers are not catalog items
// and so never match here.
func (s loadoutSurface) searchIndex(in searchContentInput) *operations.SearchContentResult {
	out := &operations.SearchContentResult{Query: in.Query}
	wantType := map[string]bool{}
	for _, t := range in.Types {
		wantType[t] = true
	}
	for _, e := range s.lo.Index.Entries {
		typ := indexKindName(e.Kind)
		if len(wantType) > 0 && !wantType[typ] {
			continue
		}
		if !matchesQuery(in.Query, e.Ref, e.Description, e.Premise) {
			continue
		}
		out.Results = append(out.Results, operations.SearchResult{Type: typ, Name: e.Ref, Source: e.Ref})
	}
	out.TotalMatches = len(out.Results)
	sortSearchResults(out.Results, in.SortBy, in.SortOrder)
	out.Results = limitSearchResults(out.Results, in.Limit)
	out.Count = len(out.Results)
	return out
}

// sortSearchResults orders results by name, or by type then name, reversed
// for "desc"; any other sortBy leaves index order.
func sortSearchResults(results []operations.SearchResult, sortBy, order string) {
	if sortBy != "name" && sortBy != "type" {
		return
	}
	sort.Slice(results, func(i, j int) bool {
		a, b := results[i], results[j]
		if sortBy == "type" && a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.Name < b.Name
	})
	if order == "desc" {
		slices.Reverse(results)
	}
}

// limitSearchResults caps results at limit, 50 when unset.
func limitSearchResults(results []operations.SearchResult, limit int) []operations.SearchResult {
	if limit <= 0 {
		limit = 50
	}
	if len(results) > limit {
		return results[:limit]
	}
	return results
}

// indexKindName is the search_content type vocabulary for a catalog kind.
func indexKindName(k trust.ItemKind) string {
	switch k {
	case trust.KindFragment:
		return "fragment"
	case trust.KindPrompt:
		return "command"
	case trust.KindSkill:
		return "skill"
	default:
		return string(k)
	}
}

// matchesQuery is a case-insensitive substring match over the fields a
// catalog entry exposes; an empty query matches everything.
func matchesQuery(query string, fields ...string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return true
	}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	return false
}

// fragment finds a fragment the package carries — loaded or premised — by
// its qualified ref, or by its bare item name when that is unambiguous. A
// package item is named by the ask that selected it (composite.Assemble),
// which for a profile's selection is the qualified ref, so the bare name is
// the ref's item half (bundles.ParseItemAsk), not a second field.
func (s loadoutSurface) fragment(ask string) (composite.Item[composite.Fragment], bool) {
	var byName []composite.Item[composite.Fragment]
	for _, set := range [][]composite.Item[composite.Fragment]{s.lo.Package.Premised, s.lo.Package.Fragments} {
		for _, it := range set {
			if it.Ref == ask {
				return it, true
			}
			if itemName(it.Ref, it.Value.Name) == ask {
				byName = append(byName, it)
			}
		}
	}
	if len(byName) == 1 {
		return byName[0], true
	}
	return composite.Item[composite.Fragment]{}, false
}

// itemName is the bare item name a package item answers to: the ref's
// "#<kind>/<name>" half when the ref is scoped, else the name it carries.
func itemName(ref, name string) string {
	if parsed, err := bundles.ParseItemAsk(ref); err == nil && parsed.Scoped && parsed.Item != "" {
		return parsed.Item
	}
	return name
}

func (s loadoutSurface) registerResources(server *mcp.Server) {
	server.AddResource(&mcp.Resource{
		URI:         resourceHelpURI,
		Name:        "ctxloom help",
		Description: "Documentation of every ctxloom resource URI. Read this first if you need to know what's available.",
		MIMEType:    "text/markdown",
	}, s.handleHelp)
	server.AddResource(&mcp.Resource{
		URI:         resourceFragmentsURI,
		Name:        "fragments",
		Description: "The catalog's context fragments with their qualified refs. A fragment carrying a PREMISE applies conditionally: the premise names the situation it applies under, and the qualified ref is what an assemble_context call quotes back to load it.",
		MIMEType:    "application/yaml",
	}, s.catalog(trust.KindFragment, resourceFragmentsURI))
	server.AddResource(&mcp.Resource{
		URI:         resourceCommandsURI,
		Name:        "commands",
		Description: "The catalog's commands with descriptions.",
		MIMEType:    "application/yaml",
	}, s.catalog(trust.KindPrompt, resourceCommandsURI))
	server.AddResource(&mcp.Resource{
		URI:         resourceSkillsURI,
		Name:        "skills",
		Description: "The catalog's Agent Skill packages (model-invoked SKILL.md directories).",
		MIMEType:    "application/yaml",
	}, s.catalog(trust.KindSkill, resourceSkillsURI))
	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: resourceFragmentsURI + "/{name}",
		Name:        "fragment",
		Description: "A single fragment's content, by qualified ref or name, as this session's package carries it.",
		MIMEType:    "text/markdown",
	}, s.handleFragment)
	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: resourceCommandsURI + "/{name}",
		Name:        "command",
		Description: "A single command's content, by qualified ref or name, as this session's package carries it.",
		MIMEType:    "text/markdown",
	}, s.handleCommand)
}

func (s loadoutSurface) handleHelp(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	body := strings.TrimSpace(`
# ctxloom resources

This endpoint serves the session's own loadout: the package its launch
carried and the catalog it was assembled from.

- ` + resourceFragmentsURI + ` — the catalog's fragments, each with its qualified ref and premise
- ` + resourceFragmentsURI + `/{name} — one fragment's body, as the package carries it
- ` + resourceCommandsURI + ` — the catalog's commands
- ` + resourceCommandsURI + `/{name} — one command's body
- ` + resourceSkillsURI + ` — the catalog's skills
`)
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "text/markdown", Text: body}}}, nil
}

// catalogEntry is one row of a catalog resource.
type catalogEntry struct {
	Ref         string `yaml:"ref"`
	Description string `yaml:"description,omitempty"`
	Premise     string `yaml:"premise,omitempty"`
}

// catalog renders the Index entries of one kind.
func (s loadoutSurface) catalog(kind trust.ItemKind, uri string) mcp.ResourceHandler {
	return func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		var rows []catalogEntry
		for _, e := range s.lo.Index.Entries {
			if e.Kind != kind {
				continue
			}
			rows = append(rows, catalogEntry{Ref: e.Ref, Description: e.Description, Premise: e.Premise})
		}
		return marshalResourceYAML(uri, rows)
	}
}

func (s loadoutSurface) handleFragment(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	name := strings.TrimPrefix(req.Params.URI, resourceFragmentsURI+"/")
	item, ok := s.fragment(name)
	if !ok {
		return nil, fmt.Errorf("fragment %q is not carried by this session's package (the catalog is %s)", name, resourceFragmentsURI)
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "text/markdown", Text: item.Value.Body}}}, nil
}

func (s loadoutSurface) handleCommand(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	name := strings.TrimPrefix(req.Params.URI, resourceCommandsURI+"/")
	for _, it := range s.lo.Package.Commands {
		if it.Ref == name || itemName(it.Ref, it.Value.Name) == name || it.Value.ExportName == name {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "text/markdown", Text: it.Value.Body}}}, nil
		}
	}
	return nil, fmt.Errorf("command %q is not carried by this session's package (the catalog is %s)", name, resourceCommandsURI)
}

func marshalResourceYAML(uri string, v any) (*mcp.ReadResourceResult, error) {
	data, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: "application/yaml", Text: string(data)}}}, nil
}
