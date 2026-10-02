package mock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// This file is the mock's NAMED FORMS on the agent.Declaration seam: per
// surface kind, the session-rooted form (MockSessionFile, the default) and
// the project form (agent.ApproachUnsafeFile). What survives of the seam is
// its NAMES — a binding's `surfaces:` preference is validated against them
// (Mock.Declaration) — and the settings-writer equity suite; the forms'
// delivery bodies reuse the shared marker-merge and managed-tree writers so
// the mock proves the seam rather than a second implementation of it.

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

// mockRel is where each mock surface lands, relative to the root it is
// delivered under — the one table Present and the dir-taking path helpers
// both read, so Route() and the delivery agree by construction. Every form
// composes UnderProjectRoot; the session form (sessionRooted) hands it a
// Start whose project root is the run's session home.
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
// NESTED, because that is the shape a real engine has (.claude/skills) and
// because a
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
// does. Everything that makes a skill
// package land correctly — the per-skill directory prefix, the DECLARED mode
// on each file, the manifest-scoped reversal — lives in that shared body, not
// here; this function contributes a directory and a manifest name.
func newMockSkillsSurface(in agent.SurfaceInputs, fs afero.Fs) agent.Approach {
	return agent.NewManagedSkillPackagesDelivery("mock/skills", mockSkillsDirName, in.Skills, func(dir string, skills []agent.SkillExport) error {
		return agent.WriteManagedSkillPackages(agent.GetFS(fs), mockSkillsPath(dir), skills, agent.WithWriteReporter(in.Reporter))
	})
}

// mockMCPFilename, mockSettingsFilename and mockCommandsDirName are mock's
// remaining native surfaces, all under its own .mock/ config dir — the shape
// a real engine has (.claude/), not a top-level scatter.
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
// safefs.WriteFile to the REAL filesystem, while every mock surface takes
// an injected afero.Fs so a hermetic test can assert on delivered bytes
// without touching the developer's disk. Reusing the marshaller keeps the
// FORMAT shared — which is the part that could drift — while honouring mock's
// own fs seam.
type mockMCPSurface struct {
	bundle map[string]wire.MCPServer
	fs     afero.Fs
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

	data, err := agent.MarshalChatMCPConfig(agent.ComposeChatMCPServers(s.bundle, nil))
	if err != nil {
		return nil, fmt.Errorf("mock: marshal mcp config: %w", err)
	}
	if err := fs.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mock: create mcp dir: %w", err)
	}
	// safefs.WriteFile, not afero.WriteFile: 0o600 must land EXACTLY
	// rather than be masked by umask, because this file can carry MCP server
	// auth headers and env — the same reason agent.WriteChatMCPConfigFile
	// writes atomically. The Fs variant is what lets mock keep its injected
	// filesystem while still honouring that discipline.
	if err := safefs.WriteFile(fs, path, data, 0o600); err != nil {
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
	if err := safefs.WriteFile(fs, path, append(data, '\n'), 0o600); err != nil {
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
			}, agent.WithWriteReporter(in.Reporter))
	})
}

// MockSessionFile is the mock's session-rooted form of every surface: the
// same well-known file, beneath the run's session home instead of the
// project root. It is each surface's DEFAULT, so a binding that selects no root
// leaves the project tree alone (ruled 2026-09-21); the project form stays
// selectable by name as agent.ApproachUnsafeFile.
const MockSessionFile = "session-file"

// sessionRooted rebases a project-rooted form onto the run's session home:
// the inner approach presents and delivers exactly as it would beneath a
// project root, handed a Start whose project root IS this run's session home.
// A run advising no session home rebases onto "" and the form presents a
// bare relative path — which is precisely what keeps selection from choosing
// it (agent.rootedInThisRun reads that).
func sessionRooted(ctor agent.Construct) agent.Construct {
	return func(in agent.SurfaceInputs, fs afero.Fs) agent.Approach {
		return &sessionForm{inner: ctor(in, fs)}
	}
}

type sessionForm struct{ inner agent.Approach }

// rebase re-roots the composition at the session home, keeping BOTH of its
// sides: the roots are already relocated, so they are advised as they are.
func (s *sessionForm) rebase(start present.Start) present.Start {
	p := start.Paths()
	return present.New(present.Advised(present.Paths{
		ProjectRoot: p.SessionHome,
		SessionHome: p.SessionHome,
	}))
}

func (s *sessionForm) Present(start present.Start) present.Presentation {
	return s.inner.Present(s.rebase(start))
}

func (s *sessionForm) Deliver(start present.Start) (agent.Delivered, error) {
	if start.Paths().SessionHome.Host == "" {
		return nil, fmt.Errorf("%w: mock's %s form writes beneath the run's session home and this delivery advised none; select %s to deliver into a project root", agent.ErrUnrootedDelivery, MockSessionFile, agent.ApproachUnsafeFile)
	}
	return s.inner.Deliver(s.rebase(start))
}

// Declaration is agent.Hosted's: the named forms per surface kind this
// double carries — the session-rooted form (MockSessionFile, the default)
// and the project form (agent.ApproachUnsafeFile) of one constructor. It is
// derived from the kind's own surfaces: a kind the double does not carry
// (the launch double keeps only its context surface; the rest arrive per
// session, inside an engine home) has no form to name, so a static
// materialize skips it. The settings form strips the hook events THIS
// double declares it cannot fire, so the delivered file matches the loss
// report. STATIC — no roots, no run state — so Names and Default stay pure
// for --help.
func (m Mock) Declaration() agent.Declaration {
	name := string(m.Name)
	both := func(kind agent.SurfaceKind, ctor agent.Construct) agent.Presentations {
		return agent.Presents(name, kind, MockSessionFile, sessionRooted(ctor)).Or(agent.ApproachUnsafeFile, ctor)
	}
	all := agent.Declaration{
		agent.SurfaceContext: both(agent.SurfaceContext, newMockContext),
		agent.SurfaceSkills:  both(agent.SurfaceSkills, newMockSkillsSurface),
		agent.SurfaceMCP: both(agent.SurfaceMCP, func(in agent.SurfaceInputs, fs afero.Fs) agent.Approach {
			return &mockMCPSurface{bundle: in.BundleMCP, fs: agent.GetFS(fs)}
		}),
		agent.SurfaceSettings: both(agent.SurfaceSettings, func(in agent.SurfaceInputs, fs afero.Fs) agent.Approach {
			return &mockSettingsSurface{hooks: m.stripUnfiredHooks(in.Hooks), fs: agent.GetFS(fs)}
		}),
		agent.SurfaceCommands: both(agent.SurfaceCommands, newMockCommandsSurface),
	}
	d := agent.Declaration{}
	for kind, p := range all {
		if m.Carries(kind) {
			d[kind] = p
		}
	}
	return d
}

// stripUnfiredHooks removes the hook events this double declares it has no
// native form for (Definition.HookLosses), so the delivered file matches
// the loss report — a file that carried an event the report called lost
// would have the report and the filesystem disagree, and the report is the
// one people act on. Returns the input unchanged when nothing is declared,
// and never mutates the caller's config: the same HooksConfig is handed to
// every surface in a run. Nothing left to deliver is reported as NO CONFIG,
// not an empty one: an empty-but-present config writes a managed block
// claiming ctxloom manages hooks here and found none.
func (m Mock) stripUnfiredHooks(hooks *wire.HooksConfig) *wire.HooksConfig {
	if hooks == nil || len(m.HookLosses) == 0 {
		return hooks
	}
	stripped := *hooks
	for event := range m.HookLosses {
		stripped.Unified.SetEvent(event, nil)
	}
	if len(stripped.Unified.All()) == 0 && !carriesNativeHook(stripped) {
		return nil
	}
	return &stripped
}

// carriesNativeHook reports whether the backend-native passthrough map still
// has a hook to deliver.
func carriesNativeHook(h wire.HooksConfig) bool {
	for _, hs := range h.Ext {
		if len(hs) > 0 {
			return true
		}
	}
	return false
}

// Compile-time capability contracts.
var (
	_ agent.ContextWriter = (*mockContextWriter)(nil)
	_ agent.Approach      = (*mockMCPSurface)(nil)
	_ agent.Approach      = (*mockSettingsSurface)(nil)
)
