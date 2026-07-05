package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/cli"
)

// mcpToolProperty is the subset of a JSON Schema property node we render into
// the per-tool parameter table. The tool input schemas come from the SDK's
// reflection of each handler's `jsonschema:"…"`-tagged input struct.
type mcpToolProperty struct {
	// Type is `any` because the SDK's reflector emits either a plain string
	// ("string") or, for optional/nullable fields, a JSON array of type names
	// (e.g. ["null","array"]). schemaTypeName normalizes both.
	Type        any              `json:"type"`
	Description string           `json:"description"`
	Items       *mcpToolProperty `json:"items"`
	Enum        []any            `json:"enum"`
}

// mcpToolSchema is the shape of a tool's InputSchema we care about: the
// property map and the required list. Everything else in the schema is ignored.
type mcpToolSchema struct {
	Properties map[string]mcpToolProperty `json:"properties"`
	Required   []string                   `json:"required"`
}

// genMCPTools writes the MCP reference page (tools, resources, resource
// templates) to <dir>/mcp-tools.md. It enumerates the live server surface via
// an in-memory MCP client so the page can never drift from the registered
// tools/resources. Output is sorted by name/URI for a byte-deterministic diff.
func genMCPTools(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	ctx := context.Background()
	tools, resources, templates, err := enumerateMCPSurface(ctx)
	if err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString(mcpFrontmatter())

	b.WriteString("Reference for the tools and resources exposed by ctxloom's MCP server (`ctxloom mcp serve`).\n\n")
	b.WriteString("The MCP surface is for **retrieving context during a session**: assembling context, ")
	b.WriteString("searching content, and working with session memory. Everything that *manages* ctxloom ")
	b.WriteString("(creating or editing bundles, profiles, fragments, and skills; pulling remotes; reviewing, ")
	b.WriteString("approving, and trusting changes) is done with the ctxloom CLI, not MCP tools. Task tracking ")
	b.WriteString("lives in the separate `taskloom` binary; its MCP server (`taskloom mcp`) serves the `task_*` tools.\n\n")

	b.WriteString("## Tools\n\n")
	for _, t := range tools {
		writeMCPTool(&b, t)
	}

	b.WriteString("## Resources\n\n")
	b.WriteString("Read-only listings are exposed as MCP resources rather than tools.\n\n")
	b.WriteString("| URI | Name | Description |\n")
	b.WriteString("|-----|------|-------------|\n")
	for _, r := range resources {
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", r.URI, mdCell(r.Name), mdCell(r.Description))
	}
	b.WriteString("\n")

	b.WriteString("## Resource Templates\n\n")
	b.WriteString("Parameterized resources for single-record lookup (RFC 6570 URI templates).\n\n")
	b.WriteString("| URI Template | Name | Description |\n")
	b.WriteString("|--------------|------|-------------|\n")
	for _, t := range templates {
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", t.URITemplate, mdCell(t.Name), mdCell(t.Description))
	}
	b.WriteString("\n")

	out := filepath.Join(dir, "mcp-tools.md")
	return os.WriteFile(out, []byte(b.String()), 0o644)
}

// enumerateMCPSurface stands up the doc server and an in-memory client, then
// lists the registered tools, resources, and templates. Each slice is sorted
// (tools/resources by name, templates by URI template) for deterministic output.
func enumerateMCPSurface(ctx context.Context) ([]*mcp.Tool, []*mcp.Resource, []*mcp.ResourceTemplate, error) {
	server := cli.NewDocMCPServer()

	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		return nil, nil, nil, fmt.Errorf("connect doc server: %w", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "gendocs"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("connect doc client: %w", err)
	}
	defer cs.Close()

	toolsRes, err := cs.ListTools(ctx, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("list tools: %w", err)
	}
	resRes, err := cs.ListResources(ctx, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("list resources: %w", err)
	}
	tmplRes, err := cs.ListResourceTemplates(ctx, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("list resource templates: %w", err)
	}

	tools := toolsRes.Tools
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	resources := resRes.Resources
	sort.Slice(resources, func(i, j int) bool { return resources[i].URI < resources[j].URI })
	templates := tmplRes.ResourceTemplates
	sort.Slice(templates, func(i, j int) bool { return templates[i].URITemplate < templates[j].URITemplate })

	return tools, resources, templates, nil
}

// writeMCPTool renders one tool: heading, description, and a parameter table
// built from the tool's reflected InputSchema.
func writeMCPTool(b *strings.Builder, t *mcp.Tool) {
	fmt.Fprintf(b, "### %s\n\n", t.Name)
	if t.Description != "" {
		fmt.Fprintf(b, "%s\n\n", t.Description)
	}

	schema := decodeToolSchema(t.InputSchema)
	if len(schema.Properties) == 0 {
		b.WriteString("_No parameters._\n\n")
		return
	}

	required := make(map[string]bool, len(schema.Required))
	for _, r := range schema.Required {
		required[r] = true
	}

	names := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		names = append(names, name)
	}
	sort.Strings(names)

	b.WriteString("| Name | Type | Required | Description |\n")
	b.WriteString("|------|------|----------|-------------|\n")
	for _, name := range names {
		p := schema.Properties[name]
		req := "No"
		if required[name] {
			req = "Yes"
		}
		fmt.Fprintf(b, "| `%s` | %s | %s | %s |\n", name, propertyType(p), req, mdCell(propertyDescription(p)))
	}
	b.WriteString("\n")
}

// decodeToolSchema re-marshals the tool's InputSchema (an `any` that is a
// map[string]any on the client side of the in-memory transport, or a typed
// schema on the server side) through JSON into our narrow view. Going through
// JSON makes it robust to either representation.
func decodeToolSchema(raw any) mcpToolSchema {
	var schema mcpToolSchema
	if raw == nil {
		return schema
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return schema
	}
	_ = json.Unmarshal(data, &schema)
	return schema
}

// propertyType renders a JSON-schema property type as a compact string,
// collapsing arrays to `elem[]`.
func propertyType(p mcpToolProperty) string {
	t := schemaTypeName(p.Type)
	if t == "array" && p.Items != nil {
		if it := schemaTypeName(p.Items.Type); it != "" {
			return it + "[]"
		}
		return "array"
	}
	if t == "" {
		return "any"
	}
	return t
}

// schemaTypeName normalizes a JSON-schema `type` value — a plain string, or a
// nullable array like ["null","array"] — to the single meaningful type name.
func schemaTypeName(t any) string {
	switch v := t.(type) {
	case string:
		return v
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s != "null" {
				return s
			}
		}
	}
	return ""
}

// propertyDescription appends any enum constraint to the base description so
// the allowed values surface in the table.
func propertyDescription(p mcpToolProperty) string {
	desc := p.Description
	if len(p.Enum) > 0 {
		vals := make([]string, len(p.Enum))
		for i, v := range p.Enum {
			vals[i] = fmt.Sprintf("`%v`", v)
		}
		enumStr := "One of: " + strings.Join(vals, ", ")
		if desc == "" {
			desc = enumStr
		} else {
			desc = desc + " (" + enumStr + ")"
		}
	}
	return desc
}

// mcpFrontmatter is the Starlight frontmatter + generated-file banner + note,
// matching the CLI reference pages' convention.
func mcpFrontmatter() string {
	return fmt.Sprintf(`---
title: "MCP Tools Reference"
---
<!-- GENERATED by %[1]sjust gen-docs%[1]s from the MCP tool/resource registrations in internal/cli.
     Do not edit; edit the mcp.Tool/mcp.Resource definitions and their input structs instead. -->
:::note
This page is generated from ctxloom's registered MCP tools and resources (%[1]sctxloom mcp serve%[1]s).
:::

`, "`")
}

// mdCell makes a string safe for a single Markdown table cell: escape pipes and
// collapse newlines to spaces.
func mdCell(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.TrimSpace(s)
}
