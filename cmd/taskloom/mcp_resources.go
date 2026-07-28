package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/tagschema"
)

// tagSchemaResourceURI/tagVocabularyResourceURI are the protocol's own
// discovery mechanism for the project's tag vocabulary — MCP resources, not
// tool output. An agent reads these to learn what tags a project actually
// declares/uses BEFORE it writes one, rather than inferring a vocabulary
// from tool descriptions (which must stay project-agnostic — see
// registerTaskTools's doc on never naming enum values there).
const (
	tagSchemaResourceURI     = "taskloom://tag-schema"
	tagVocabularyResourceURI = "taskloom://tag-vocabulary"
)

// registerTaskResources mounts the two read-only resources newMCPServer
// serves alongside the five task_* tools: the project's resolved tag_schema
// (declared vocabulary/formulas) and its tag vocabulary in use (with
// counts). Both handlers resolve their data at READ time (see
// handleTagSchemaResource/handleTagVocabularyResource), never at
// registration time, so a long-lived `taskloom mcp` process never serves a
// schema/vocabulary snapshot frozen at startup.
func registerTaskResources(server *mcp.Server) {
	server.AddResource(&mcp.Resource{
		URI:  tagSchemaResourceURI,
		Name: "tag-schema",
		Description: "The project's RESOLVED tag_schema: every declared target " +
			"(namespace:key), whether it's arity=scalar (re-tagging retracts the " +
			"previous value), its closed enum values or numeric range if declared, " +
			"and its priority_fn/decay_fn formulas if declared. This is the live " +
			"vocabulary a task tag's value is validated against on write — read it " +
			"before choosing a tag, rather than guessing.",
		MIMEType: "application/json",
	}, handleTagSchemaResource)

	server.AddResource(&mcp.Resource{
		URI:  tagVocabularyResourceURI,
		Name: "tag-vocabulary",
		Description: "Tags currently in use across the project's tasks, with " +
			"active/total counts (the MCP twin of `taskloom tags`). \"active\" " +
			"counts tasks visible in the default list view; \"total\" includes " +
			"completed/Deferred ones too. Use this to spot an existing near-twin " +
			"before coining a new ad-hoc tag.",
		MIMEType: "application/json",
	}, handleTagVocabularyResource)
}

// tagSchemaTargetDoc is one declared target's full schema-resource entry.
type tagSchemaTargetDoc struct {
	Target     string          `json:"target"`
	Scalar     bool            `json:"scalar,omitempty"`
	Enum       []string        `json:"enum,omitempty"`
	Range      *tagSchemaRange `json:"range,omitempty"`
	PriorityFn string          `json:"priority_fn,omitempty"`
	DecayFn    string          `json:"decay_fn,omitempty"`
}

type tagSchemaRange struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

type tagSchemaDoc struct {
	Targets []tagSchemaTargetDoc `json:"targets"`
}

// handleTagSchemaResource resolves the CURRENT project's tag_schema exactly
// as the tool handlers do (resolveTagSchema, mirroring handleTaskList) and
// renders it as tagSchemaDoc. Resolved fresh on every read — never cached
// from server construction — so a project's config.yaml edit is visible on
// the very next read without restarting `taskloom mcp`.
func handleTagSchemaResource(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	tc, err := taskContext()
	if err != nil {
		return nil, err
	}
	tc, err = resolveTagSchema(tc)
	if err != nil {
		return nil, err
	}
	doc, err := buildTagSchemaDoc(tc.TagSchema)
	if err != nil {
		return nil, err
	}
	return jsonResourceResult(tagSchemaResourceURI, doc)
}

// buildTagSchemaDoc unions every target named across schema's arity, enum,
// range, priority_fn, and decay_fn facets (tagschema.Schema.Targets) into
// one sorted tagSchemaDoc entry per target — a target declared under more
// than one facet (e.g. triage:kind: scalar AND enum AND priority_fn AND
// decay_fn)
// gets one entry carrying all of it, rather than one row per facet. Display
// (tagma.hide) declarations are deliberately excluded: this resource
// describes the WRITE-time vocabulary an agent chooses a value from, not
// what a listing happens to show.
func buildTagSchemaDoc(schema *tagschema.Schema) (*tagSchemaDoc, error) {
	entries := map[string]*tagSchemaTargetDoc{}
	get := func(target string) *tagSchemaTargetDoc {
		e, ok := entries[target]
		if !ok {
			e = &tagSchemaTargetDoc{Target: target}
			entries[target] = e
		}
		return e
	}

	for _, target := range schema.Targets(tagschema.ArityFacet) {
		get(target).Scalar = schema.IsScalar(target)
	}
	for _, target := range schema.Targets(tagschema.EnumFacet) {
		enum, _ := schema.Enum(target)
		get(target).Enum = enum
	}
	for _, target := range schema.Targets(tagschema.RangeFacet) {
		min, max, ok, err := schema.Range(target)
		if err != nil {
			return nil, fmt.Errorf("tag-schema resource: %w", err)
		}
		if ok {
			get(target).Range = &tagSchemaRange{Min: min, Max: max}
		}
	}
	for _, target := range schema.Targets(tagschema.PriorityFnFacet) {
		fn, _ := schema.Get(tagschema.PriorityFnFacet, target)
		get(target).PriorityFn = fn
	}
	for _, target := range schema.Targets(tagschema.DecayFnFacet) {
		fn, _ := schema.Get(tagschema.DecayFnFacet, target)
		get(target).DecayFn = fn
	}

	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	doc := &tagSchemaDoc{Targets: make([]tagSchemaTargetDoc, 0, len(names))}
	for _, name := range names {
		doc.Targets = append(doc.Targets, *entries[name])
	}
	return doc, nil
}

// tagVocabularyDoc is the tag-vocabulary resource's payload: the CLI-visible
// twin of `taskloom tags`' output (same operations.ListTagCounts +
// visibleTagCounts logic, reused rather than reimplemented).
type tagVocabularyDoc struct {
	ProjectID  string                `json:"project_id,omitempty"`
	ProjectDir string                `json:"project_dir,omitempty"`
	Warning    string                `json:"warning,omitempty"`
	Tags       []operations.TagCount `json:"tags"`
}

// handleTagVocabularyResource is the MCP twin of `taskloom tags`
// (cmd/taskloom/commands.go's tagsCmd): it calls the SAME
// operations.ListTagCounts + visibleTagCounts(hideConfigFor(tc)) the CLI
// command does, rather than reimplementing the enumeration or the
// display-hide filter.
func handleTagVocabularyResource(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	tc, err := taskContextSingle()
	if err != nil {
		return nil, err
	}
	res, err := operations.ListTagCounts(tc)
	if err != nil {
		return nil, err
	}
	visible := visibleTagCounts(res.Tags, hideConfigFor(tc))
	if visible == nil {
		visible = []operations.TagCount{}
	}
	doc := tagVocabularyDoc{
		ProjectID:  res.ProjectID,
		ProjectDir: res.ProjectDir,
		Warning:    res.Warning,
		Tags:       visible,
	}
	return jsonResourceResult(tagVocabularyResourceURI, doc)
}

// jsonResourceResult marshals body as indented JSON into a single-content
// ReadResourceResult for uri, the shared tail every resource handler in this
// file ends with.
func jsonResourceResult(uri string, body any) (*mcp.ReadResourceResult, error) {
	data, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal resource %s: %w", uri, err)
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: "application/json", Text: string(data)}},
	}, nil
}
