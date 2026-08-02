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
	"strings"

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
	if err := checkSourceInfo(&fds); err != nil {
		fatalf("%v", err)
	}

	p, err := mcpschema.NewProjector(&fds)
	if err != nil {
		fatalf("%v", err)
	}
	res, err := generateSchemas(p, mcpschema.CoordinationBindings(), *out)
	if err != nil {
		fatalf("%v", err)
	}
	fmt.Printf("gen-mcp-schemas: wrote %d schemas to %s\n", res.written, *out)
	if len(res.pruned) > 0 {
		fmt.Printf("gen-mcp-schemas: pruned %d stale schema(s): %s\n", len(res.pruned), strings.Join(res.pruned, ", "))
	}

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

// genResult is one generation run's outcome: the schemas written, and the
// stale ones removed.
type genResult struct {
	written int
	pruned  []string
}

// generateSchemas writes one schema per binding into an EXISTING goldens
// directory, then removes the generated schemas no binding claims any more.
//
// An EMPTY binding table is a failure. The generator used to
// complete having written zero files, printing nothing and exiting 0 — its only
// stdout line lived inside the loop, so a run that did nothing could not say
// so, and these schemas are checked in and embedded as the live MCP tool
// surface. The summary line now reports the count from outside the loop, so
// "wrote 0" cannot masquerade as silence.
func generateSchemas(p projector, bindings []mcpschema.Binding, out string) (genResult, error) {
	if len(bindings) == 0 {
		return genResult{}, fmt.Errorf("no coordination bindings to project — refusing to write an empty tool surface")
	}
	if err := requireGoldensDir(out); err != nil {
		return genResult{}, err
	}
	var res genResult
	keep := map[string]bool{}
	for _, b := range bindings {
		spec, err := p.ProjectTool(b)
		if err != nil {
			return res, err
		}
		name := b.Tool + ".json"
		if err := writeSpec(filepath.Join(out, name), spec); err != nil {
			return res, err
		}
		keep[name] = true
		res.written++
	}
	pruned, err := pruneStaleSchemas(out, keep)
	res.pruned = pruned
	return res, err
}

// requireGoldensDir refuses an output directory that does not already exist.
// The schemas under it are checked in and ARE the goldens, so this generator's
// job is to rewrite a directory somebody already has. os.MkdirAll used to
// create whatever path it was handed, so a mistyped -out succeeded into a
// brand-new tree: the real goldens went untouched and the CI `git diff` gate
// passed on a run that had regenerated nothing. Bringing a new goldens
// directory into existence is a deliberate act, not a typo's side effect.
func requireGoldensDir(out string) error {
	info, err := os.Stat(out)
	if err != nil {
		return fmt.Errorf("output dir %s: %w — create it deliberately; this generator rewrites checked-in goldens, it does not invent a tree", out, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("output dir %s is not a directory", out)
	}
	return nil
}

// pruneStaleSchemas removes generated schemas that no binding claims any more,
// reporting what it deleted. The directory is embedded wholesale
// (//go:embed schemas/*.json), so a renamed or deleted binding otherwise
// leaves its old file behind and every runner keeps registering a live MCP
// tool that nothing serves — invisible to the CI drift gate, which diffs
// tracked files and never asks what else is in the directory.
//
// A .json that is not a generated tool spec ABORTS the prune instead of being
// deleted: pruning plus a mistyped -out is how a sweep destroys somebody's
// unrelated directory.
func pruneStaleSchemas(out string, keep map[string]bool) ([]string, error) {
	entries, err := os.ReadDir(out)
	if err != nil {
		return nil, fmt.Errorf("prune %s: %w", out, err)
	}
	var pruned []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".json" || keep[name] {
			continue
		}
		path := filepath.Join(out, name)
		if err := assertGeneratedSpec(path); err != nil {
			return pruned, err
		}
		if err := os.Remove(path); err != nil {
			return pruned, fmt.Errorf("prune %s: %w", path, err)
		}
		pruned = append(pruned, name)
	}
	return pruned, nil
}

// assertGeneratedSpec confirms a file is one of this generator's own outputs —
// parseable as a ToolSpec, naming the tool its filename claims — before the
// prune is allowed to delete it.
func assertGeneratedSpec(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("prune %s: %w", path, err)
	}
	var spec mcpschema.ToolSpec
	if err := json.Unmarshal(raw, &spec); err != nil || spec.Name == "" {
		return fmt.Errorf("refusing to prune %s: not a generated tool schema", path)
	}
	if filepath.Base(path) != spec.Name+".json" {
		return fmt.Errorf("refusing to prune %s: it names tool %q", path, spec.Name)
	}
	return nil
}

// checkSourceInfo fails loudly unless EVERY agentcoord.v1 file in the set
// carries SourceCodeInfo. Descriptions come from proto comments, which live
// only there, so a file without it projects annotation-only text and the loss
// is visible nowhere but a golden diff read by eye. Checking until the first
// file that HAS source info passed a set where the one carrying the comments
// was the bare one. This is the playbook's verify step — if buf ever stops
// emitting source info, switch the just recipe to the protoc fallback
// (--include_source_info) and this check documents why.
func checkSourceInfo(fds *descriptorpb.FileDescriptorSet) error {
	var seen int
	var bare []string
	for _, f := range fds.GetFile() {
		if f.GetPackage() != "agentcoord.v1" {
			continue
		}
		seen++
		if f.GetSourceCodeInfo() == nil {
			bare = append(bare, f.GetName())
		}
	}
	const remedy = "build it with `buf build -o <file>` (default includes source info) or `protoc --descriptor_set_out --include_source_info`"
	if seen == 0 {
		return fmt.Errorf("descriptor set contains no agentcoord.v1 files — %s", remedy)
	}
	if len(bare) > 0 {
		return fmt.Errorf("descriptor set has no SourceCodeInfo for %s — %s", strings.Join(bare, ", "), remedy)
	}
	return nil
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
