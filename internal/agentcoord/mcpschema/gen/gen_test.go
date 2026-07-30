package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/ctxloom/ctxloom/internal/agentcoord/mcpschema"
)

// fakeProjector stands in for the descriptor-backed projector: the guards under
// test are about the BINDING TABLE and the output directory, which need no
// descriptors.
type fakeProjector struct{}

func (fakeProjector) ProjectTool(b mcpschema.Binding) (*mcpschema.ToolSpec, error) {
	return &mcpschema.ToolSpec{Name: b.Tool}, nil
}

// goldensDir makes an existing output directory, which the generator now
// requires.
func goldensDir(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "schemas")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	return out
}

// U027-F01: the generator could complete having written ZERO files, printing
// nothing and exiting 0 — its only stdout line was inside the loop, so a
// generator that did nothing could not say so. These schemas are checked in and
// embedded as the live MCP tool surface, so an empty run means the binary's tool
// surface silently goes away.
func TestGenerateSchemas_NoBindingsIsAnError(t *testing.T) {
	out := filepath.Join(t.TempDir(), "schemas")
	res, err := generateSchemas(fakeProjector{}, nil, out)
	if err == nil {
		t.Fatalf("an empty binding table must not report success (wrote %d)", res.written)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("a refused run must not leave an output directory behind")
	}
}

func TestGenerateSchemas_WritesOnePerBinding(t *testing.T) {
	out := goldensDir(t)
	res, err := generateSchemas(fakeProjector{}, []mcpschema.Binding{{Tool: "agent_run"}, {Tool: "agent_send"}}, out)
	if err != nil {
		t.Fatal(err)
	}
	if res.written != 2 {
		t.Errorf("wrote %d, want 2", res.written)
	}
	for _, name := range []string{"agent_run.json", "agent_send.json"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Errorf("expected %s: %v", name, err)
		}
	}
}

// U027-F02: the output directory is embedded wholesale (//go:embed
// schemas/*.json), so a removed or renamed binding used to leave its old file
// behind and every runner kept registering a live MCP tool that nothing
// serves. The CI drift gate diffs tracked files and never asks what else the
// directory holds, so nothing caught it.
func TestGenerateSchemas_PrunesASchemaNoBindingClaims(t *testing.T) {
	out := goldensDir(t)
	if _, err := generateSchemas(fakeProjector{}, []mcpschema.Binding{{Tool: "agent_run"}, {Tool: "agent_retired"}}, out); err != nil {
		t.Fatal(err)
	}
	res, err := generateSchemas(fakeProjector{}, []mcpschema.Binding{{Tool: "agent_run"}}, out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "agent_retired.json")); !os.IsNotExist(err) {
		t.Errorf("a schema no binding claims must not survive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "agent_run.json")); err != nil {
		t.Errorf("a claimed schema must survive: %v", err)
	}
	if len(res.pruned) != 1 || res.pruned[0] != "agent_retired.json" {
		t.Errorf("pruned %v, want [agent_retired.json] — a silent deletion is not a report", res.pruned)
	}
}

// The prune must never become a directory shredder: a .json this generator did
// not write aborts it rather than being deleted.
func TestGenerateSchemas_RefusesToPruneAForeignFile(t *testing.T) {
	out := goldensDir(t)
	foreign := filepath.Join(out, "notes.json")
	if err := os.WriteFile(foreign, []byte(`{"unrelated": true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := generateSchemas(fakeProjector{}, []mcpschema.Binding{{Tool: "agent_run"}}, out); err == nil {
		t.Error("a foreign .json must abort the prune, not be deleted")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("the foreign file must still be there: %v", err)
	}
}

// A generated spec whose filename disagrees with the tool it names is not
// something to delete on a guess either.
func TestGenerateSchemas_RefusesToPruneAMisnamedSpec(t *testing.T) {
	out := goldensDir(t)
	odd := filepath.Join(out, "wrong_name.json")
	if err := os.WriteFile(odd, []byte(`{"name":"agent_send","description":"d","inputSchema":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := generateSchemas(fakeProjector{}, []mcpschema.Binding{{Tool: "agent_run"}}, out); err == nil {
		t.Error("a spec whose filename disagrees with its tool name must abort the prune")
	}
	if _, err := os.Stat(odd); err != nil {
		t.Errorf("the misnamed file must still be there: %v", err)
	}
}

// U027-F03: os.MkdirAll created whatever path it was handed, so a mistyped
// -out succeeded into a brand-new tree — the real goldens went untouched, and
// the CI `git diff` gate passed on a run that had regenerated nothing at all.
func TestGenerateSchemas_RefusesAnOutputDirThatDoesNotExist(t *testing.T) {
	out := filepath.Join(t.TempDir(), "internla", "schemas") // the typo
	if _, err := generateSchemas(fakeProjector{}, []mcpschema.Binding{{Tool: "agent_run"}}, out); err == nil {
		t.Fatal("a nonexistent output dir must not be conjured into existence")
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("the refused path must not have been created")
	}
}

// agentcoordFDS builds a descriptor set of agentcoord.v1 files, marking which
// ones carry SourceCodeInfo.
func agentcoordFDS(withSourceInfo ...bool) *descriptorpb.FileDescriptorSet {
	fds := &descriptorpb.FileDescriptorSet{}
	names := []string{"annotations.proto", "coordination.proto", "artifacts.proto"}
	for i, has := range withSourceInfo {
		f := &descriptorpb.FileDescriptorProto{
			Name:    proto.String(names[i]),
			Package: proto.String("agentcoord.v1"),
			Syntax:  proto.String("proto3"),
		}
		if has {
			f.SourceCodeInfo = &descriptorpb.SourceCodeInfo{}
		}
		fds.File = append(fds.File, f)
	}
	return fds
}

// U026-F10 / U027-F04: descriptions come from proto comments, which live only
// in SourceCodeInfo. The guard returned on the FIRST agentcoord.v1 file that
// had it, so a set where annotations.proto carried source info and
// coordination.proto — the file holding every tool description — did not, sailed
// straight through, and the whole surface degraded to annotation-only text with
// nothing said about it.
func TestCheckSourceInfo_EveryAgentcoordFileMustCarryIt(t *testing.T) {
	err := checkSourceInfo(agentcoordFDS(true, false, true))
	if err == nil {
		t.Fatal("a file without SourceCodeInfo must fail the check even when an earlier one has it")
	}
	if !strings.Contains(err.Error(), "coordination.proto") {
		t.Errorf("the error must name the bare file, got: %v", err)
	}
}

func TestCheckSourceInfo_AllPresentPasses(t *testing.T) {
	if err := checkSourceInfo(agentcoordFDS(true, true, true)); err != nil {
		t.Fatalf("a fully annotated set must pass: %v", err)
	}
}

// A set with no agentcoord.v1 files at all is not "nothing to check" — it is a
// descriptor set built against the wrong module.
func TestCheckSourceInfo_NoAgentcoordFilesIsAnError(t *testing.T) {
	fds := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{{
		Name:    proto.String("other.proto"),
		Package: proto.String("other.v1"),
	}}}
	if err := checkSourceInfo(fds); err == nil {
		t.Error("a descriptor set with no agentcoord.v1 files must fail loudly")
	}
}
