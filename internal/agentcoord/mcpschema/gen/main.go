// Command gen regenerates the proto-canonical MCP tool schemas
// (internal/agentcoord/mcpschema/schemas/*.json) from a FileDescriptorSet
// built WITH source info — `buf build -o <file>` (buf includes
// SourceCodeInfo by default; verified), or the protoc fallback
// `protoc --descriptor_set_out=<file> --include_source_info`.
//
// Run via `just gen-mcp-schemas`. The emitted files are checked in and ARE
// the goldens: CI regenerates and diffs (gen-mcp-schemas-check), exactly the
// gen-docs-check pattern.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/ctxloom/ctxloom/internal/agentcoord/mcpschema"
)

func main() {
	descriptor := flag.String("descriptor", "", "path to a FileDescriptorSet built with source info (buf build -o)")
	out := flag.String("out", "internal/agentcoord/mcpschema/schemas", "output directory for the generated *.json schemas")
	flag.Parse()
	if *descriptor == "" {
		fatalf("usage: gen -descriptor <fds.binpb> [-out <dir>]")
	}

	raw, err := os.ReadFile(*descriptor)
	if err != nil {
		fatalf("read descriptor set: %v", err)
	}
	var fds descriptorpb.FileDescriptorSet
	// proto.Unmarshal resolves the D3 annotation extensions through the
	// global registry (this binary imports the generated agentcoord package).
	if err := proto.Unmarshal(raw, &fds); err != nil {
		fatalf("parse descriptor set: %v", err)
	}
	assertSourceInfo(&fds)

	p, err := mcpschema.NewProjector(&fds)
	if err != nil {
		fatalf("%v", err)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fatalf("output dir: %v", err)
	}
	for _, b := range mcpschema.CoordinationBindings() {
		spec, err := p.ProjectTool(b)
		if err != nil {
			fatalf("%v", err)
		}
		path := filepath.Join(*out, b.Tool+".json")
		if err := writeSpec(path, spec); err != nil {
			fatalf("%v", err)
		}
		fmt.Printf("gen-mcp-schemas: wrote %s\n", path)
	}
}

// assertSourceInfo fails loudly when the descriptor set carries no
// SourceCodeInfo for the agentcoord module: the generated descriptions would
// silently degrade to annotation-only. This is the playbook's verify step —
// if buf ever stops emitting source info, switch the just recipe to the
// protoc fallback (--include_source_info) and this assert documents why.
func assertSourceInfo(fds *descriptorpb.FileDescriptorSet) {
	for _, f := range fds.GetFile() {
		if f.GetPackage() == "agentcoord.v1" && f.GetSourceCodeInfo() != nil {
			return
		}
	}
	fatalf("descriptor set has no SourceCodeInfo for agentcoord.v1 — build it with `buf build -o <file>` (default includes source info) or `protoc --descriptor_set_out --include_source_info`")
}

// writeSpec emits one tool spec as stable, indented JSON with a trailing
// newline (diff-friendly goldens; Go maps marshal with sorted keys, so the
// output is deterministic).
func writeSpec(path string, spec *mcpschema.ToolSpec) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(spec); err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "gen-mcp-schemas: "+format+"\n", args...)
	os.Exit(1)
}
