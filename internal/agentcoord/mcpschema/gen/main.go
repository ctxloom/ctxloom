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
	xmllikeOut := flag.String("xmllike-out", "", "output file for the generated <ctxloom-reminder> frame encoders (empty: skip)")
	xmllikePkg := flag.String("xmllike-package", "agentcoord", "Go package name for the generated frame encoders")
	flag.Parse()
	if *descriptor == "" {
		fatalf("usage: gen -descriptor <fds.binpb> [-out <dir>] [-xmllike-out <file>]")
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
	n, err := generateSchemas(p, mcpschema.CoordinationBindings(), *out)
	if err != nil {
		fatalf("%v", err)
	}
	fmt.Printf("gen-mcp-schemas: wrote %d schemas to %s\n", n, *out)

	if *xmllikeOut == "" {
		return
	}
	frames, err := generateXmlLike(p, *xmllikePkg, *xmllikeOut)
	if err != nil {
		// THE BUILD FAILURE (§6.6). An un-annotated field, or free text
		// smuggled into an attribute, stops the generator here — the whole
		// reason frame encoders are generated instead of reflected. Nothing is
		// written, so a rejected run cannot half-update the encoder file.
		fatalf("%v", err)
	}
	fmt.Printf("gen-mcp-schemas: wrote %d <%s> encoder(s) to %s\n", frames, mcpschema.ReminderTag, *xmllikeOut)
}

// generateXmlLike projects, validates, and emits the frame encoders, reporting
// how many it wrote.
func generateXmlLike(p *mcpschema.Projector, pkg, out string) (int, error) {
	frames, err := p.XmlLikeFrames()
	if err != nil {
		return 0, err
	}
	src, err := mcpschema.RenderXmlLikeGo(pkg, frames)
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(out, src, 0o644); err != nil {
		return 0, fmt.Errorf("write %s: %w", out, err)
	}
	return len(frames), nil
}

// projector is the slice of mcpschema.Projector generateSchemas needs.
type projector interface {
	ProjectTool(mcpschema.Binding) (*mcpschema.ToolSpec, error)
}

// generateSchemas writes one schema per binding and reports how many it wrote.
//
// An EMPTY binding table is a failure (U027-F01). The generator used to
// complete having written zero files, printing nothing and exiting 0 — its only
// stdout line lived inside the loop, so a run that did nothing could not say
// so, and these schemas are checked in and embedded as the live MCP tool
// surface. The summary line now reports the count from outside the loop, so
// "wrote 0" cannot masquerade as silence.
func generateSchemas(p projector, bindings []mcpschema.Binding, out string) (int, error) {
	if len(bindings) == 0 {
		return 0, fmt.Errorf("no coordination bindings to project — refusing to write an empty tool surface")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return 0, fmt.Errorf("output dir: %w", err)
	}
	var written int
	for _, b := range bindings {
		spec, err := p.ProjectTool(b)
		if err != nil {
			return written, err
		}
		path := filepath.Join(out, b.Tool+".json")
		if err := writeSpec(path, spec); err != nil {
			return written, err
		}
		written++
	}
	return written, nil
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
