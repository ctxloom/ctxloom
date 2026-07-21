package main

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestNewMCPServerRegistersTaskTools pins the surface the MCP reference is
// generated from: the doc generator documents whatever `taskloom mcp` serves,
// so both must come from this one factory. A tool added to the server without a
// regenerated page is caught by the gen-docs drift gate; a tool that stops
// being registered at all is caught here.
func TestNewMCPServerRegistersTaskTools(t *testing.T) {
	ctx := context.Background()

	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := newMCPServer().Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("connect server: %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	defer cs.Close()

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
		if tool.Description == "" {
			t.Errorf("tool %q has no description — the reference page would render a blank entry", tool.Name)
		}
	}
	for _, want := range []string{"task_list", "task_add", "task_set_status", "task_edit", "task_tag"} {
		if !got[want] {
			t.Errorf("tool %q not registered", want)
		}
	}
	if len(res.Tools) != 5 {
		t.Errorf("registered %d tools, want 5 — add the new tool to this list and regenerate the docs", len(res.Tools))
	}

	// The server's instructions are the source of truth for the reference
	// page's intro; an empty one would publish a headless page.
	if init := cs.InitializeResult(); init == nil || init.Instructions == "" {
		t.Error("server exposes no instructions — the MCP reference page would have no intro")
	}

	resRes, err := cs.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("list resources: %v", err)
	}
	gotResources := map[string]bool{}
	for _, r := range resRes.Resources {
		gotResources[r.URI] = true
		if r.Description == "" {
			t.Errorf("resource %q has no description — the reference page would render a blank entry", r.URI)
		}
	}
	for _, want := range []string{tagSchemaResourceURI, tagVocabularyResourceURI} {
		if !gotResources[want] {
			t.Errorf("resource %q not registered", want)
		}
	}
	if len(resRes.Resources) != 2 {
		t.Errorf("registered %d resources, want 2 — add the new resource to this list and regenerate the docs", len(resRes.Resources))
	}
}
