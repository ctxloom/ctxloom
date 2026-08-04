package backends

import (
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// This file lands the mock backend on the unified surface-delivery seam
// (internal/shared/agent/cells.go) — the CONTEXT surface only (see
// docs/design/engine-delivery-seam.design.md, "The mock engine implements
// both halves").
//
// Before this file, BuildSurfaces("mock", …) returned agent.EmptySurfaceSet:
// the mock materialized nothing, so no hermetic test could prove a fragment
// actually reached a delivered FILE — every delivery assertion either ran
// against a live engine or was vacuous. This is not a test convenience; it is
// the hermetic vehicle J20's own delivery-matrix scenario names as its own
// untag condition.
//
// mock's context surface writes the managed section of MOCK_CONTEXT.md at the
// target dir's root — a plain, single well-known file, exactly the shape
// antigravity's .agents/AGENTS.md and claude's CLAUDE.md already use — via the
// SAME shared marker-merge core (agent.WriteManagedContext /
// agent.ReadManagedContext) rather than a second implementation of the
// marker split. It additionally implements agent.StateReader, which nothing
// did before this: the read half the design's "manage status" step (3) will
// walk.
//
// Only context is in scope here. MCP/settings/commands/skills stay on
// EmptySurfaceSet's refusal path for mock — deliberately: the design's
// ordering names the mock's CONTEXT route as step 2, and a mock skills/MCP
// surface is unscoped net-new code this change does not need.

// mockContextFilename is the mock engine's well-known context file — its
// analogue of CLAUDE.md / AGENTS.md. It lives at the target dir's ROOT (not
// nested) so Route() names it as a human would look for it.
const mockContextFilename = "MOCK_CONTEXT.md"

// mockContextPath returns the mock context file's path under dir.
func mockContextPath(dir string) string {
	return filepath.Join(dir, mockContextFilename)
}

// mockContextWriter implements agent.ContextWriter for the mock engine: it
// merges the assembled context into MOCK_CONTEXT.md's ctxloom-managed section,
// preserving anything a user hand-wrote outside the markers. This is the exact
// shape antigravity's AGENTS.md and claude's CLAUDE.md writers use — the same
// shared core, a different filename.
type mockContextWriter struct {
	FS afero.Fs
}

// WriteContext merges req.Context into MOCK_CONTEXT.md's managed section via
// the shared marker-merge core.
func (w *mockContextWriter) WriteContext(req agent.ContextWriteRequest) (agent.ContextReport, error) {
	fs := agent.GetFS(w.FS)
	path := mockContextPath(req.ProjectDir)
	return agent.WriteManagedContext(fs, path, mockContextFilename, req.Context, mockContextFilename)
}

// mockContextSurface is mock's context surface: the ctxloom-managed section of
// MOCK_CONTEXT.md. It is the WRITE half (agent.Delivery, via the shared
// agent.DeliverManagedContext shape every native-file context surface uses)
// AND the READ half (agent.StateReader) — the same object answers for what it
// delivered, which is what makes a status report over it truthful by
// construction rather than a second code path that can disagree with the one
// that wrote.
type mockContextSurface struct {
	context string
	fs      afero.Fs
}

// Deliver merges context into MOCK_CONTEXT.md via the shared managed-context
// core and returns a handle whose Cleanup strips the managed section (removing
// the file when nothing user-authored remains) by writing empty context.
func (s *mockContextSurface) Deliver(dir string) (agent.Delivered, error) {
	return agent.DeliverManagedContext(&mockContextWriter{FS: s.fs}, dir, s.context)
}

// UnsafeInfo names mock's context surface for the DeliverShared fallback's
// warning (ResolvedSelection.deliverOneShared's unsafeNamed check, cells.go).
func (s *mockContextSurface) UnsafeInfo() string { return "mock/context" }

// State implements agent.StateReader: it reports what MOCK_CONTEXT.md
// currently carries in its managed section, via the shared read-side helper
// (agent.ReadManagedContext) — the counterpart of WriteManagedContext, reused
// rather than re-parsing the markers here. An absent file or an absent managed
// section reports FileDeliveryState with Found/HasSection false; Currency then
// turns that into the missing verdict.
func (s *mockContextSurface) State(dir string) (agent.DeliveryState, error) {
	fs := agent.GetFS(s.fs)
	state, err := agent.ReadManagedContext(fs, mockContextPath(dir), mockContextFilename)
	if err != nil {
		return nil, err
	}
	return state, nil
}

// mockApproaches is mock's declared per-surface approach table: context only,
// at the native well-known file — mock has no out-of-cwd redirect and no
// SharedRealization, matching antigravity/kiro/codex/opencode (claude is the
// one backend with an out-of-cwd scratch conversion).
var mockApproaches = agent.ApproachTable{
	agent.SurfaceContext: {agent.ApproachUnsafeFile},
}

// MockSurfaces is mock's single-surface SurfaceSet: context only. Every other
// SurfaceKind is absent from mockApproaches, so SupportedApproaches/
// DefaultApproach report it as folded/absent (a permitted no-op for
// WithEverything) exactly as codex's MCP kind does — never an error.
type MockSurfaces struct {
	agent.TableDispatch

	Context *mockContextSurface

	dispatch map[agent.SurfaceKind]agent.Delivery
}

// NewMockSurfaces builds mock's surfaces from a run's shared inputs. fs nil
// defaults to the OS filesystem (agent.GetFS). It takes the SAME
// agent.SurfaceInputs every other backend does — mock simply ignores every
// field but Context, exactly as claude ignores Fragments/AgentName (see
// claude.NewSurfaces's doc for why a shared struct beats a hand-mapped copy).
func NewMockSurfaces(in agent.SurfaceInputs, fs afero.Fs) MockSurfaces {
	fs = agent.GetFS(fs)
	context := &mockContextSurface{context: in.Context, fs: fs}
	return MockSurfaces{
		TableDispatch: agent.TableDispatch{Table: mockApproaches},
		Context:       context,
		dispatch: map[agent.SurfaceKind]agent.Delivery{
			agent.SurfaceContext: context,
		},
	}
}

// SurfaceFor resolves one (kind, approach) to mock's concrete surface — a
// plain table lookup, since mock's context surface (unlike claude's) has only
// the one approach and no Hook/SystemPrompt arm to special-case.
func (s MockSurfaces) SurfaceFor(kind agent.SurfaceKind, a agent.Approach) (agent.Delivery, error) {
	return mockApproaches.SurfaceFor("mock", s.dispatch, kind, a)
}

// SharedRealization reports no out-of-cwd conversion for any kind: mock has no
// race-safe redirect, so a SHARED-cwd delivery of its context surface always
// falls back to the loud well-known write (UnsafeInfo above is that
// fallback's warning).
func (s MockSurfaces) SharedRealization(agent.SurfaceKind) (func() (agent.Delivered, error), bool) {
	return nil, false
}

// Compile-time capability contracts.
var (
	_ agent.Delivery      = (*mockContextSurface)(nil)
	_ agent.StateReader   = (*mockContextSurface)(nil)
	_ agent.ContextWriter = (*mockContextWriter)(nil)
	_ agent.SurfaceSet    = MockSurfaces{}
)
