package backends

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/agent/present"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// This file lands the mock backend on the unified surface-delivery seam
// (internal/shared/agent/cells.go) — the CONTEXT and SKILLS surfaces (see
// docs/design/engine-delivery-seam.design.md, "The mock engine implements
// both halves").
//
// Before this file, mock declared nothing:
// the mock materialized nothing, so no hermetic test could prove a fragment
// actually reached a delivered FILE — every delivery assertion either ran
// against a live engine or was vacuous. This is not a test convenience; it is
// the hermetic vehicle J001400's own delivery-matrix scenario names as its own
// untag condition.
//
// mock's context surface writes the managed section of MOCK_CONTEXT.md at the
// target dir's root — a plain, single well-known file, exactly the shape
// claude's CLAUDE.md already uses — via the
// SAME shared marker-merge core (agent.WriteManagedContext /
// agent.ReadManagedContext) rather than a second implementation of the
// marker split. It additionally implements agent.StateReader, which nothing
// did before this: the read half the design's "manage check" step (3) will
// walk.
//
// mock's SKILLS surface is that same reuse argument applied to a TREE: the
// shared agent.ManagedSkillPackagesDelivery bound to the shared
// agent.WriteManagedSkillPackages writer — byte-for-byte the machinery
// claude's .claude/skills/ and opencode's
// .opencode/skill/ go through, differing only in the directory it targets. A
// second skill-materializing path in the mock would prove the mock, not the
// seam.
//
// mock's hook loss stays DECLARED via noHooksReason (registry.go) where a
// double declares one; the complete mock carries a settings surface.

// mockContextFilename is the mock engine's well-known context file — its
// analogue of CLAUDE.md / AGENTS.md. It lives at the target dir's ROOT (not
// nested) so Route() names it as a human would look for it.
const mockContextFilename = "MOCK_CONTEXT.md"

// mockContextPath returns the mock context file's path under dir, via the
// declared context presenter. The dir-taking form serves the READ side and
// the shared writer cores, whose own contracts hand over a directory; a
// Deliver resolves against the advised Start it received instead.
func mockContextPath(dir string) string {
	return mockSurfacePath(agent.SurfaceContext, present.ProjectOnHost(dir))
}

// mockSurfacePath resolves ONE surface's path against the advised roots,
// through the same rel table every approach's Present reads. Every mock path
// goes through here rather than through a per-kind copy: the bodies differed
// only by which SurfaceKind they looked up, and five copies of a resolve chain
// is five places for it to drift.
func mockSurfacePath(kind agent.SurfaceKind, start present.Start) string {
	return start.UnderProjectRoot(mockRel[kind]).Build().HostPath
}

// mockRel is where each mock surface lands, relative to the project root —
// the one table Present and the dir-taking path helpers both read, so Route()
// and the delivery agree by construction. Everything roots UnderProjectRoot,
// never UnderEngineHome — mock has no out-of-cwd redirect. claude is the one
// shipped backend that does convert to an out-of-cwd scratch, which is what
// this contrast exists to state.
var mockRel = map[agent.SurfaceKind]string{
	agent.SurfaceContext:  mockContextFilename,
	agent.SurfaceSkills:   mockSkillsDirName,
	agent.SurfaceMCP:      mockMCPFilename,
	agent.SurfaceSettings: mockSettingsFilename,
	agent.SurfaceCommands: mockCommandsDirName,
}

// mockContextWriter implements agent.ContextWriter for the mock engine: it
// merges the assembled context into MOCK_CONTEXT.md's ctxloom-managed section,
// preserving anything a user hand-wrote outside the markers. This is the exact
// shape claude's CLAUDE.md writer uses — the same
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

// newMockContext is mock's context approach: the SHARED native-file
// implementation (agent.NativeContextFile) bound to mockContextWriter and
// MOCK_CONTEXT.md — the same merge core and the same read side
// (agent.StateReader) claude's CLAUDE.md goes through, differing only in the
// writer and the filename. A second managed-section implementation here would
// prove the mock, not the seam.
var newMockContext = agent.NativeContextFile("mock/context", mockContextFilename, func(fs afero.Fs) agent.ContextWriter {
	return &mockContextWriter{FS: fs}
})

// MockConfigDirName is the mock engine's project-relative managed-config
// directory — its analogue of each real engine's own ConfigDirName, and
// what mock's descriptor declares as its container overlay dir.
const MockConfigDirName = ".mock"

// mockSkillsDirName is the directory the mock engine "reads" its Agent Skill
// packages from, relative to the delivery dir. Unlike the context file it is
// NESTED, because that is the shape every real engine has (.claude/skills,
// .agents/skills, .opencode/skill, .codex/skills) and because a
// bare top-level `skills/` would collide with the `skills/` directory of a
// bundle content tree materialized into the same project.
const mockSkillsDirName = MockConfigDirName + "/skills"

// mockSkillsPath returns the mock skills directory's path under dir, via the
// declared skills presenter.
func mockSkillsPath(dir string) string {
	return mockSurfacePath(agent.SurfaceSkills, present.ProjectOnHost(dir))
}

// newMockSkillsSurface builds mock's skills surface: the SHARED
// agent.ManagedSkillPackagesDelivery bound to the SHARED
// agent.WriteManagedSkillPackages writer, exactly as claude's newSkillsSurface
// and opencode's do. Everything that makes a skill
// package land correctly — the per-skill directory prefix, the DECLARED mode
// on each file, the manifest-scoped reversal — lives in that shared body, not
// here; this function contributes a directory and a manifest name.
func newMockSkillsSurface(in agent.SurfaceInputs, fs afero.Fs) agent.Approach {
	return agent.NewManagedSkillPackagesDelivery("mock/skills", mockSkillsDirName, in.Skills, func(dir string, skills []agent.SkillExport) error {
		return agent.WriteManagedSkillPackages(agent.GetFS(fs), mockSkillsPath(dir), skills)
	})
}

// mockMCPFilename, mockSettingsFilename and mockCommandsDirName are mock's
// remaining native surfaces, all under its own .mock/ config dir — the shape
// every real engine has (.claude/, .codex/, .kiro/), not a top-level scatter.
const (
	mockMCPFilename      = MockConfigDirName + "/mcp.json"
	mockSettingsFilename = MockConfigDirName + "/settings.json"
	mockCommandsDirName  = MockConfigDirName + "/commands"
)

// mockMCPPath, mockSettingsPath and mockCommandsPath resolve each surface's
// path through its declared presenter, exactly as the context and skills
// halves do — never by joining strings here, so Route() and the delivery agree
// by construction.
func mockMCPPath(dir string) string {
	return mockSurfacePath(agent.SurfaceMCP, present.ProjectOnHost(dir))
}

func mockSettingsPath(dir string) string {
	return mockSurfacePath(agent.SurfaceSettings, present.ProjectOnHost(dir))
}

func mockCommandsPath(dir string) string {
	return mockSurfacePath(agent.SurfaceCommands, present.ProjectOnHost(dir))
}

// mockMCPSurface is mock's MCP surface: .mock/mcp.json, composed by the SHARED
// agent.ComposeChatMCPServers and marshalled by the SHARED
// agent.MarshalChatMCPConfig.
//
// It marshals-then-writes rather than calling agent.WriteChatMCPConfigFile,
// and that is not a style choice: that helper writes through
// iox.WriteFileAtomic to the REAL filesystem, while every mock surface takes
// an injected afero.Fs so a hermetic test can assert on delivered bytes
// without touching the developer's disk. Reusing the marshaller keeps the
// FORMAT shared — which is the part that could drift — while honouring mock's
// own fs seam.
type mockMCPSurface struct {
	bundle   map[string]wire.MCPServer
	override string
	fs       afero.Fs
}

// Present declares .mock/mcp.json beneath the advised project root. No flag:
// mock has no out-of-cwd redirect.
func (s *mockMCPSurface) Present(start present.Start) present.Presentation {
	return start.UnderProjectRoot(mockRel[agent.SurfaceMCP]).Build()
}

// Deliver writes .mock/mcp.json and returns a handle whose Cleanup removes it.
func (s *mockMCPSurface) Deliver(start present.Start) (agent.Delivered, error) {
	fs := agent.GetFS(s.fs)
	path := mockSurfacePath(agent.SurfaceMCP, start)

	data, err := agent.MarshalChatMCPConfig(agent.ComposeChatMCPServers(s.override, s.bundle, nil))
	if err != nil {
		return nil, fmt.Errorf("mock: marshal mcp config: %w", err)
	}
	if err := fs.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mock: create mcp dir: %w", err)
	}
	// iox.WriteFileAtomicFs, not afero.WriteFile: 0o600 must land EXACTLY
	// rather than be masked by umask, because this file can carry MCP server
	// auth headers and env — the same reason agent.WriteChatMCPConfigFile
	// writes atomically. The Fs variant is what lets mock keep its injected
	// filesystem while still honouring that discipline.
	if err := iox.WriteFileAtomicFs(fs, path, data, 0o600); err != nil {
		return nil, fmt.Errorf("mock: write %s: %w", path, err)
	}
	return agent.DeliveredFunc(func() error { return fs.Remove(path) }), nil
}

// UnsafeInfo names mock's MCP surface for the DeliverShared fallback warning.
func (s *mockMCPSurface) UnsafeInfo() string { return "mock/mcp" }

// mockSettingsSurface is mock's settings surface: .mock/settings.json carrying
// the session's managed hooks.
//
// Its existence is what lets a configured session_start hook actually LAND for
// mock. Before it, mock declared a hook loss via noHooksReason — an honest
// declaration of a real gap, but a gap that made mock unusable as the second
// engine in any scenario about hook delivery.
type mockSettingsSurface struct {
	hooks *wire.HooksConfig
	fs    afero.Fs
}

// Deliver merges the managed hooks into .mock/settings.json, PRESERVING every
// top-level key it did not write. That preservation is the whole contract a
// settings surface has — a delivery that clobbered a user's own settings would
// Present declares .mock/settings.json beneath the advised project root.
func (s *mockSettingsSurface) Present(start present.Start) present.Presentation {
	return start.UnderProjectRoot(mockRel[agent.SurfaceSettings]).Build()
}

// model the opposite of what every real engine's writer promises.
func (s *mockSettingsSurface) Deliver(start present.Start) (agent.Delivered, error) {
	fs := agent.GetFS(s.fs)
	path := mockSurfacePath(agent.SurfaceSettings, start)

	doc, err := readMockSettings(fs, path)
	if err != nil {
		return nil, err
	}
	if s.hooks == nil {
		delete(doc, mockSettingsHooksKey)
	} else {
		raw, merr := json.Marshal(s.hooks)
		if merr != nil {
			return nil, fmt.Errorf("mock: marshal hooks: %w", merr)
		}
		doc[mockSettingsHooksKey] = raw
	}
	if err := writeMockSettings(fs, path, doc); err != nil {
		return nil, err
	}

	return agent.DeliveredFunc(func() error {
		current, rerr := readMockSettings(fs, path)
		if rerr != nil {
			return rerr
		}
		delete(current, mockSettingsHooksKey)
		// Nothing of ours left AND nothing of theirs: remove the file rather
		// than leave an empty object behind, matching how the context surface
		// removes MOCK_CONTEXT.md when its managed section was all there was.
		if len(current) == 0 {
			if err := fs.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			return nil
		}
		return writeMockSettings(fs, path, current)
	}), nil
}

// UnsafeInfo names mock's settings surface for the DeliverShared fallback.
func (s *mockSettingsSurface) UnsafeInfo() string { return "mock/settings" }

// mockSettingsHooksKey is the settings document key mock's managed hooks live
// under. Named once so the write and the cleanup cannot disagree about it.
const mockSettingsHooksKey = "hooks"

// readMockSettings loads path as a key->raw-JSON map, so keys ctxloom does not
// own survive a merge byte-for-byte. An absent file is an empty document, not
// an error: delivering into a project that has none is the normal case.
func readMockSettings(fs afero.Fs, path string) (map[string]json.RawMessage, error) {
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]json.RawMessage{}, nil
		}
		return nil, fmt.Errorf("mock: read %s: %w", path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	doc := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("mock: parse %s: %w", path, err)
	}
	return doc, nil
}

// writeMockSettings renders doc and writes it at 0o600 — a settings file can
// carry hook commands, so it gets the same mode as the MCP config.
func writeMockSettings(fs afero.Fs, path string, doc map[string]json.RawMessage) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("mock: marshal settings: %w", err)
	}
	if err := fs.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mock: create settings dir: %w", err)
	}
	if err := iox.WriteFileAtomicFs(fs, path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("mock: write %s: %w", path, err)
	}
	return nil
}

// newMockCommandsSurface builds mock's commands surface: the SHARED
// agent.ManagedCommandsDelivery bound to the SHARED
// agent.WriteManagedCommandFiles, exactly as the skills surface binds the
// shared skill-package writer. The render func contributes a filename and a
// body; everything that makes a command file land correctly lives in the
// shared writer.
func newMockCommandsSurface(in agent.SurfaceInputs, fs afero.Fs) agent.Approach {
	return agent.NewManagedCommandsDelivery("mock/commands", mockCommandsDirName, in.Commands, func(dir string, cmds []agent.CommandExport) error {
		return agent.WriteManagedCommandFiles(agent.GetFS(fs), mockCommandsPath(dir), cmds,
			func(c agent.CommandExport) (string, []byte, error) {
				return filepath.Base(c.Name) + ".md", []byte(c.Content), nil
			})
	})
}

// mockDeclaration is mock's DECLARATION for the registered backend name: per
// surface, the one approach (unsafe-file) it constructs. It carries EVERY
// SurfaceKind, deliberately: mock is a complete engine with no real model
// behind it, not a hole in the registry. A partial double makes its gaps
// load-bearing somewhere else, where nothing states that they are.
//
// It is a function of the registered NAME rather than a package-level literal
// because four doubles share these constructors: the settings approach strips
// the hook kinds the NAMED descriptor declares unsupported, and a refusal that
// spelled a hardcoded "mock" told a caller asking about mock-lossy or
// mock-launch about a different backend entirely. It is still STATIC — no
// roots, no run state — so Names and Default stay pure for --help.
//
// mock has no out-of-cwd redirect (no approach here implements
// agent.OutOfCwd), so a SHARED-cwd delivery of any surface always falls back
// to the loud well-known write; each surface's own UnsafeInfo is that
// fallback's warning. No engine has an out-of-cwd flag for a skill package at
// all, so the skills half is not a mock shortcut.
func mockDeclaration(name string) agent.Declaration {
	return agent.Declaration{
		agent.SurfaceContext: agent.Presents(name, agent.SurfaceContext, agent.ApproachUnsafeFile, newMockContext),
		agent.SurfaceSkills:  agent.Presents(name, agent.SurfaceSkills, agent.ApproachUnsafeFile, newMockSkillsSurface),
		agent.SurfaceMCP: agent.Presents(name, agent.SurfaceMCP, agent.ApproachUnsafeFile, func(in agent.SurfaceInputs, fs afero.Fs) agent.Approach {
			return &mockMCPSurface{bundle: in.BundleMCP, override: in.MCPCommandOverride, fs: agent.GetFS(fs)}
		}),
		agent.SurfaceSettings: agent.Presents(name, agent.SurfaceSettings, agent.ApproachUnsafeFile, func(in agent.SurfaceInputs, fs afero.Fs) agent.Approach {
			return &mockSettingsSurface{hooks: stripUnsupportedHookKinds(name, in.Hooks), fs: agent.GetFS(fs)}
		}),
		agent.SurfaceCommands: agent.Presents(name, agent.SurfaceCommands, agent.ApproachUnsafeFile, newMockCommandsSurface),
	}
}

// mockLaunchDeclaration is the launch-delivered double's declaration: CONTEXT
// ONLY. The other four surfaces are absent because a launch-keyed engine has
// no stable path a static materialize could write them to — they arrive per
// session, inside an engine home this harpless call cannot name.
//
// Absent from the declaration is what makes materialize SKIP them. That is
// only half the contract: the descriptor's launchOnlySettingsReason supplies
// the other half, the report line saying where they DO come from. Skipping
// without declaring writes four true "wrote" lines and stays silent about
// everything that went nowhere — this project's characteristic silent no-op.
// Declaring without skipping reports a surface as not-carried while its file
// sits in the tree. Neither half is optional.
func mockLaunchDeclaration(name string) agent.Declaration {
	return agent.Declaration{
		agent.SurfaceContext: mockDeclaration(name)[agent.SurfaceContext],
	}
}

// Compile-time capability contracts.
var (
	_ agent.ContextWriter = (*mockContextWriter)(nil)
	_ agent.Approach      = (*mockMCPSurface)(nil)
	_ agent.Approach      = (*mockSettingsSurface)(nil)
)
