package docsgen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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

// GenMCPTools writes the product's MCP reference page (tools, resources,
// resource templates) to <dir>/mcp-tools.md. It enumerates the live server
// surface via an in-memory MCP client so the page can never drift from the
// registered tools/resources. Output is sorted by name/URI for a
// byte-deterministic diff. Sections with nothing in them are omitted, so a
// tools-only product doesn't advertise resources it has none of.
func GenMCPTools(ctx context.Context, p *Product, dir string) error {
	if p.MCPServer == nil {
		return errors.New("docsgen: product " + p.Bin + " has no MCP server to document")
	}
	// U051-F10: mcpFrontmatter interpolates MCPSource/MCPCommand unguarded; an
	// unset field used to render a banner reading "as served by ``" instead of
	// signalling the misconfiguration, exactly like the nil-server case above.
	if p.MCPSource == "" || p.MCPCommand == "" {
		return fmt.Errorf("docsgen: product %s needs both MCPSource and MCPCommand set to document its MCP surface", p.Bin)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create MCP tools dir %s: %w", dir, err)
	}

	tools, resources, templates, err := enumerateMCPSurface(ctx, p.MCPServer)
	if err != nil {
		return err
	}
	// U051-F02(a): a product with an MCPServer but zero registered tools used
	// to still write a page with an empty "## Tools" section and return nil
	// -- exit 0, no payload, the same misconfiguration the nil-server check
	// two lines above this already treats as fatal. Resources/Templates are
	// correctly optional (guarded by len(...) > 0 below); Tools is not.
	if len(tools) == 0 {
		return fmt.Errorf("docsgen: product %s's MCP server has no registered tools to document", p.Bin)
	}

	var b strings.Builder
	b.WriteString(p.mcpFrontmatter())

	if p.MCPIntro != "" {
		b.WriteString(strings.TrimRight(p.MCPIntro, "\n"))
		b.WriteString("\n\n")
	}

	b.WriteString("## Tools\n\n")
	for _, t := range tools {
		if err := writeMCPTool(&b, t); err != nil {
			return fmt.Errorf("docsgen: %w", err)
		}
	}

	if len(resources) > 0 {
		b.WriteString("## Resources\n\n")
		b.WriteString("Read-only listings are exposed as MCP resources rather than tools.\n\n")
		b.WriteString("| URI | Name | Description |\n")
		b.WriteString("|-----|------|-------------|\n")
		for _, r := range resources {
			fmt.Fprintf(&b, "| `%s` | %s | %s |\n", r.URI, mdCell(r.Name), mdCell(r.Description))
		}
		b.WriteString("\n")
	}

	if len(templates) > 0 {
		b.WriteString("## Resource Templates\n\n")
		b.WriteString("Parameterized resources for single-record lookup (RFC 6570 URI templates).\n\n")
		b.WriteString("| URI Template | Name | Description |\n")
		b.WriteString("|--------------|------|-------------|\n")
		for _, t := range templates {
			fmt.Fprintf(&b, "| `%s` | %s | %s |\n", t.URITemplate, mdCell(t.Name), mdCell(t.Description))
		}
		b.WriteString("\n")
	}

	out := filepath.Join(dir, "mcp-tools.md")
	return os.WriteFile(out, []byte(b.String()), 0o644)
}

// enumerateMCPSurface stands up the doc server and an in-memory client, then
// lists the registered tools, resources, and templates. Each slice is sorted
// (tools/resources by name, templates by URI template) for deterministic output.
func enumerateMCPSurface(ctx context.Context, server *mcp.Server) ([]*mcp.Tool, []*mcp.Resource, []*mcp.ResourceTemplate, error) {
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

	// U051-F04: a listing this small (~13 registered ctxloom tools against the
	// SDK's 1000-item default page size) never actually paginates today, but
	// reading only the first page and ignoring NextCursor would silently
	// under-document any surface that grows past the page limit. Follow each
	// cursor to exhaustion instead of trusting a single call.
	var tools []*mcp.Tool
	for cursor := ""; ; {
		res, err := cs.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("list tools: %w", err)
		}
		tools = append(tools, res.Tools...)
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}

	var resources []*mcp.Resource
	for cursor := ""; ; {
		res, err := cs.ListResources(ctx, &mcp.ListResourcesParams{Cursor: cursor})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("list resources: %w", err)
		}
		resources = append(resources, res.Resources...)
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}

	var templates []*mcp.ResourceTemplate
	for cursor := ""; ; {
		res, err := cs.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{Cursor: cursor})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("list resource templates: %w", err)
		}
		templates = append(templates, res.ResourceTemplates...)
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}

	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	sort.Slice(resources, func(i, j int) bool { return resources[i].URI < resources[j].URI })
	sort.Slice(templates, func(i, j int) bool { return templates[i].URITemplate < templates[j].URITemplate })

	return tools, resources, templates, nil
}

// writeMCPTool renders one tool: heading, description, and a parameter table
// built from the tool's reflected InputSchema.
func writeMCPTool(b *strings.Builder, t *mcp.Tool) error {
	fmt.Fprintf(b, "### %s\n\n", t.Name)
	if t.Description != "" {
		fmt.Fprintf(b, "%s\n\n", t.Description)
	}

	schema, err := decodeToolSchema(t.InputSchema)
	if err != nil {
		// U051-F02(b): decodeToolSchema used to swallow both the Marshal and
		// Unmarshal failure and return a zero mcpToolSchema, which reads
		// here as "no parameters" -- indistinguishable from a genuinely
		// parameterless tool. A tool whose schema this generator cannot
		// decode must abort the page, not publish a lie about its inputs.
		return fmt.Errorf("tool %s: %w", t.Name, err)
	}
	if len(schema.Properties) == 0 {
		b.WriteString("_No parameters._\n\n")
		return nil
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
	return nil
}

// decodeToolSchema re-marshals the tool's InputSchema (an `any` that is a
// map[string]any on the client side of the in-memory transport, or a typed
// schema on the server side) through JSON into our narrow view. Going through
// JSON makes it robust to either representation.
func decodeToolSchema(raw any) (mcpToolSchema, error) {
	var schema mcpToolSchema
	if raw == nil {
		return schema, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return schema, fmt.Errorf("marshal input schema: %w", err)
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		return schema, fmt.Errorf("decode input schema: %w", err)
	}
	return schema, nil
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
func (p *Product) mcpFrontmatter() string {
	return fmt.Sprintf(`---
title: "MCP Tools Reference"
---
<!-- GENERATED by %[1]sjust gen-docs%[1]s from the MCP tool/resource registrations in %[2]s.
     Do not edit; edit the mcp.Tool/mcp.Resource definitions and their input structs instead. -->
:::note
This page is generated from %[3]s's registered MCP tools and resources, as served by %[1]s%[4]s%[1]s.
:::

`, "`", p.MCPSource, p.Bin, p.MCPCommand)
}

// mdCell makes a string safe for a single Markdown table cell: escape pipes and
// collapse newlines to spaces.
func mdCell(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.TrimSpace(s)
}
