//go:build conformance

// The cross-agent equity suite. Gated behind the `conformance` build tag (see
// doc.go) and kept in its own package so it composes the engine packages without
// touching their per-module test files — safe alongside concurrent work. Every
// write goes through the one static writer at rest (atrest: what `manage hooks
// install` and `uninstall` run) and every status read through the engine's
// agent.SettingsReader, so it is format-agnostic (a second agent's own format
// must pass the same suite).
package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport/atrest"
)

// agentCase is one agent under test: its engine kind, the settings file its
// hooks land in, and a valid settings file carrying a user-authored entry
// the delivery must preserve.
type agentCase struct {
	name         string
	kind         func(t *testing.T) engine.Engine
	settingsPath func(projectDir string) string
	userFile     string // a valid settings file (in the agent's format) with a user entry
	userMarker   string // substring of userFile that must survive install + uninstall
}

// agentCases returns the agents this suite actually covers today.
//
// WHAT THIS SUITE CURRENTLY PROVES, stated plainly: its premise is CROSS-AGENT
// EQUITY, and with one row it covers one agent and proves nothing
// cross-format. A second row is what restores the premise; until there is one,
// do not read a green run here as equity evidence.
//
// Add a new agent here ONLY once it can actually honor every assertion below
// (or once each assertion gains the same per-agent escape hatch
// backsUpCorruptFile already models for the one divergence that already had
// one) — a new agent here inherits the WHOLE equity suite unconditionally.
func agentCases() []agentCase {
	return []agentCase{
		{"claude-code", func(t *testing.T) engine.Engine {
			k, err := claude.Build()
			require.NoError(t, err)
			return k
		}, claude.ProjectSettingsPath, `{"theme":"dark"}`, "dark"},
	}
}

// project is a's project at projectDir on fs.
func (a agentCase) project(t *testing.T, fs afero.Fs) *atrest.Project {
	return atrest.New(t, safefs.NewMem(fs), a.kind(t), projectDir)
}

// status is the engine's own account of what is wired into p.
func status(t *testing.T, p *atrest.Project) agent.SettingsStatus {
	t.Helper()
	h, ok := p.Kind.(agent.Hosted)
	require.True(t, ok, "the engine carries a settings reader")
	st, err := h.SettingsReader(p.Settings()).Status(p.Dir)
	require.NoError(t, err)
	return st
}

// withHooks is the package delivering hooks.
func withHooks(hooks *wire.HooksConfig) composite.Package {
	return composite.Package{Hooks: *hooks}
}

// coveredEvent is one unified hook event: the setter that places it on a
// wire.UnifiedHooks, and a unique, greppable command suffix so a
// format-agnostic test can prove THAT event reached the settings file
// regardless of what the agent calls it natively.
type coveredEvent struct {
	marker string
	set    func(*wire.UnifiedHooks, []wire.Hook)
}

// coveredEvents are the unified hook events every agent must emit. SessionEnd
// is intentionally absent — not every engine's CLI has such an event.
var coveredEvents = []coveredEvent{
	{"conf-sessionstart", func(u *wire.UnifiedHooks, h []wire.Hook) { u.SessionStart = h }},
	{"conf-pretool", func(u *wire.UnifiedHooks, h []wire.Hook) { u.PreTool = h }},
	{"conf-posttool", func(u *wire.UnifiedHooks, h []wire.Hook) { u.PostTool = h }},
	{"conf-preshell", func(u *wire.UnifiedHooks, h []wire.Hook) { u.PreShell = h }},
	{"conf-postfileedit", func(u *wire.UnifiedHooks, h []wire.Hook) { u.PostFileEdit = h }},
}

func markerCommand(marker string) []wire.Hook {
	// The executable token must be `ctxloom` so every writer recognizes the
	// hook as managed when it comes to remove it.
	return []wire.Hook{{Command: "ctxloom hook " + marker}}
}

// standardHooks configures every covered event at once.
func standardHooks() *wire.HooksConfig {
	var u wire.UnifiedHooks
	for _, ev := range coveredEvents {
		ev.set(&u, markerCommand(ev.marker))
	}
	return &wire.HooksConfig{Unified: u}
}

// onlyHooks configures exactly ONE covered event and leaves the rest unset.
func onlyHooks(ev coveredEvent) *wire.HooksConfig {
	var u wire.UnifiedHooks
	ev.set(&u, markerCommand(ev.marker))
	return &wire.HooksConfig{Unified: u}
}

const projectDir = "/project"

// TestConformance_RefusesToOverwriteUnparseableSettings: a corrupt existing
// settings file must NEVER be overwritten. The install must refuse (return a
// non-nil error naming the offending file) and leave the original bytes on
// disk exactly as they were. Whether an engine also backs the corrupt bytes up
// to a sibling "<path>.corrupt-<unix-ts>" file is asserted PER-ENGINE: nothing
// is destroyed by a refusal, so a backup is a courtesy an engine may or may
// not extend.
func TestConformance_RefusesToOverwriteUnparseableSettings(t *testing.T) {
	const corrupt = "!!! not valid !!!"

	// engines whose delivery backs the corrupt original up to a sibling
	// "<path>.corrupt-<ts>" file before refusing to write. An engine that
	// refuses with no backup belongs here as false, not omitted — the map is
	// the per-engine escape hatch, and an absent key reads as false without
	// saying so.
	backsUpCorruptFile := map[string]bool{
		"claude-code": false,
	}

	for _, a := range agentCases() {
		t.Run(a.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			p := a.project(t, fs)
			path := a.settingsPath(projectDir)
			require.NoError(t, afero.WriteFile(fs, path, []byte(corrupt), 0644))

			err := p.Install(withHooks(standardHooks()))
			require.Error(t, err, "the install must refuse rather than overwrite an unparseable prior file")
			assert.Contains(t, err.Error(), path, "the refusal error must name the offending file")

			data, readErr := afero.ReadFile(fs, path)
			require.NoError(t, readErr)
			assert.Equal(t, corrupt, string(data), "the original corrupt bytes must be left untouched, never overwritten")

			entries, dirErr := afero.ReadDir(fs, filepath.Dir(path))
			require.NoError(t, dirErr)
			prefix := filepath.Base(path) + ".corrupt-"
			found := false
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), prefix) {
					found = true
					break
				}
			}
			assert.Equal(t, backsUpCorruptFile[a.name], found, "%s: a sibling %q backup", a.name, prefix+"<unix-ts>")
		})
	}
}

// recordingFs records every path opened for CREATION and every rename. That is
// what makes ATOMICITY observable from outside a writer: an atomic write puts
// the new bytes in a temp file and renames it over the destination, so the
// destination is never itself opened for writing and no reader can ever
// observe it half-written. A writer that truncated the live file and wrote
// into it would leave the same final bytes and the same backup — identical to
// every assertion this suite made before.
type recordingFs struct {
	afero.Fs
	mu       sync.Mutex
	created  []string
	renamedT []string
}

func (r *recordingFs) Create(name string) (afero.File, error) {
	r.note(name)
	return r.Fs.Create(name)
}

func (r *recordingFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if flag&(os.O_CREATE|os.O_WRONLY|os.O_RDWR|os.O_TRUNC) != 0 {
		r.note(name)
	}
	return r.Fs.OpenFile(name, flag, perm)
}

func (r *recordingFs) Rename(oldname, newname string) error {
	r.mu.Lock()
	r.renamedT = append(r.renamedT, newname)
	r.mu.Unlock()
	return r.Fs.Rename(oldname, newname)
}

func (r *recordingFs) note(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.created = append(r.created, name)
}

func (r *recordingFs) snapshot() (created, renamedTo []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.created...), append([]string{}, r.renamedT...)
}

func (r *recordingFs) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.created, r.renamedT = nil, nil
}

// TestConformance_AtomicWriteLeavesNoBackup: overwriting an existing settings
// file replaces it ATOMICALLY and leaves NO .ctxloom.bak beside it. A backup
// of a file the user still owns is litter the user did not ask for, and it
// duplicates secrets into a second path with the same permissions.
func TestConformance_AtomicWriteLeavesNoBackup(t *testing.T) {
	for _, a := range agentCases() {
		t.Run(a.name, func(t *testing.T) {
			fs := &recordingFs{Fs: afero.NewMemMapFs()}
			p := a.project(t, fs)
			path := a.settingsPath(projectDir)
			require.NoError(t, afero.WriteFile(fs, path, []byte(a.userFile), 0644))
			fs.reset() // that setup write is the test's, not the delivery's

			require.NoError(t, p.Install(withHooks(standardHooks())))

			_, bakErr := fs.Stat(path + ".ctxloom.bak")
			assert.True(t, os.IsNotExist(bakErr),
				"no .ctxloom.bak may be left beside the settings file; got err=%v", bakErr)

			created, renamedTo := fs.snapshot()
			assert.NotContains(t, created, path,
				"the live settings file must never be opened for writing: an atomic write renames a temp over it, so no reader can see it half-written")
			assert.Contains(t, renamedTo, path,
				"the new content must arrive by rename; created=%v", created)

			entries, dirErr := afero.ReadDir(fs, filepath.Dir(path))
			require.NoError(t, dirErr)
			for _, e := range entries {
				assert.NotContains(t, e.Name(), ".tmp", "a successful atomic write leaves no temp behind")
			}
		})
	}
}

// TestConformance_HookEventCoverage: every unified hook event must reach the
// settings file. This is what catches an absent per-event mapping — a
// delivery with no PreShell or PostFileEdit translation drops the command
// silently.
//
// WHAT IT DOES NOT PROVE, deliberately: that a command landed under the RIGHT
// native event. The assertion is a substring search over the file's bytes,
// because this suite is format-agnostic by construction, and asserting slot
// attachment needs per-agent format knowledge. That knowledge lives — and is
// asserted — in the per-agent tests.
func TestConformance_HookEventCoverage(t *testing.T) {
	for _, a := range agentCases() {
		t.Run(a.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			require.NoError(t, a.project(t, fs).Install(withHooks(standardHooks())))

			data, err := afero.ReadFile(fs, a.settingsPath(projectDir))
			require.NoError(t, err)
			for _, ev := range coveredEvents {
				assert.Containsf(t, string(data), ev.marker, "unified event %q must be emitted", ev.marker)
			}
		})
	}
}

// TestConformance_HookEventsAreEmittedIndependently: each unified event must
// carry its OWN command through, on its own.
//
// The all-at-once test above cannot distinguish a delivery that translates
// five events from one that emits a fixed bundle whenever any hook is
// configured, or one that cross-wires two events into a single slot — every
// marker is present either way. Configuring exactly one event and requiring
// that exactly one marker appears separates those cases.
func TestConformance_HookEventsAreEmittedIndependently(t *testing.T) {
	for _, a := range agentCases() {
		for _, ev := range coveredEvents {
			t.Run(a.name+"/"+ev.marker, func(t *testing.T) {
				fs := afero.NewMemMapFs()
				require.NoError(t, a.project(t, fs).Install(withHooks(onlyHooks(ev))))

				data, err := afero.ReadFile(fs, a.settingsPath(projectDir))
				require.NoError(t, err)
				assert.Containsf(t, string(data), ev.marker,
					"unified event %q must be emitted when it is the only one configured", ev.marker)
				for _, other := range coveredEvents {
					if other.marker == ev.marker {
						continue
					}
					assert.NotContainsf(t, string(data), other.marker,
						"only %q was configured, yet %q was written too", ev.marker, other.marker)
				}
			})
		}
	}
}

// liveFilesContaining lists every file on fs whose bytes carry marker, ignoring
// ctxloom's own backup and quarantine copies.
//
// It exists because Status() is the writer's OPINION of what it wrote. A
// writer whose Status were wrong in the same direction as its writer — a
// registration that never lands and a probe that reports it anyway — passes a
// Status-only assertion in both directions at once, which is the tautology
// this suite must not rest on. Walking the filesystem is the
// independent evidence, and it stays format-agnostic (JSON, TOML) and
// location-agnostic (claude keeps its MCP registration in a different
// file — .mcp.json — from its hooks) precisely because it looks at bytes rather than at a
// path the test would have to know. It walks the PROJECT, not the whole fs:
// the claims record holds the values it claimed too, and it is not a file
// the engine reads.
//
// A "<path>.ctxloom.bak" records the state BEFORE the write that produced it,
// and a ".corrupt-<ts>" file records bytes ctxloom refused to overwrite.
// Neither is a live managed artifact, so neither counts.
func liveFilesContaining(t *testing.T, fs afero.Fs, marker string) []string {
	t.Helper()
	var hits []string
	require.NoError(t, afero.Walk(fs, projectDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return err
		}
		if strings.HasSuffix(path, ".ctxloom.bak") || strings.Contains(filepath.Base(path), ".corrupt-") {
			return nil
		}
		data, readErr := afero.ReadFile(fs, path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(data), marker) {
			hits = append(hits, path)
		}
		return nil
	}))
	return hits
}

// TestConformance_MCPWritesExactlyWhatItIsGiven: a delivery registers the
// MCP servers it is handed and invents none. Both directions are asserted,
// because only checking the empty case would pass against a delivery that had
// stopped registering anything at all.
func TestConformance_MCPWritesExactlyWhatItIsGiven(t *testing.T) {
	for _, a := range agentCases() {
		t.Run(a.name, func(t *testing.T) {
			// The probe name is deliberately not MCPServerName. That constant is
			// "ctxloom", which every hook command also contains, so a substring
			// search for it proves nothing either way.
			const probeName = "conformance-probe-mcp"

			t.Run("given none, registers none", func(t *testing.T) {
				p := a.project(t, afero.NewMemMapFs())
				require.NoError(t, p.Install(withHooks(standardHooks())))
				assert.False(t, status(t, p).MCPPresent,
					"a delivery handed no MCP servers must not invent one")
			})

			t.Run("given one, registers exactly it, in the bytes", func(t *testing.T) {
				fs := afero.NewMemMapFs()
				p := a.project(t, fs)
				pkg := withHooks(standardHooks())
				pkg.MCP = map[string]wire.MCPServer{probeName: {Command: "probe-bin", Args: []string{"serve"}}}
				require.NoError(t, p.Install(pkg))

				assert.True(t, status(t, p).MCPPresent, "the handed server must be reported present")
				assert.NotEmpty(t, liveFilesContaining(t, fs, probeName),
					"and must reach a file, not only Status(): a reader whose own account is the only witness proves nothing")
			})
		})
	}
}

// TestConformance_RemovePreservesUser: the uninstall strips every managed
// artifact while preserving the user's own settings. The managed hook commands
// are asserted PRESENT after the install and ABSENT after the uninstall, so
// the same predicate is shown to flip — a removal test that only asks Status()
// cannot tell "stripped" from "never written".
func TestConformance_RemovePreservesUser(t *testing.T) {
	for _, a := range agentCases() {
		t.Run(a.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			p := a.project(t, fs)
			path := a.settingsPath(projectDir)
			require.NoError(t, afero.WriteFile(fs, path, []byte(a.userFile), 0644))

			pkg := withHooks(standardHooks())
			pkg.MCP = map[string]wire.MCPServer{"conformance-probe-mcp": {Command: "probe-bin"}}
			require.NoError(t, p.Install(pkg))
			for _, ev := range coveredEvents {
				require.NotEmptyf(t, liveFilesContaining(t, fs, ev.marker),
					"precondition: managed event %q must be on disk before removal can be shown to strip it", ev.marker)
			}
			require.True(t, status(t, p).Wired(), "precondition: the install is reported")

			require.NoError(t, p.Uninstall())

			assert.False(t, status(t, p).Wired(), "no managed artifacts remain after removal")
			for _, ev := range coveredEvents {
				assert.Emptyf(t, liveFilesContaining(t, fs, ev.marker),
					"managed event %q survived removal on disk while Status reported it gone", ev.marker)
			}

			data, err := afero.ReadFile(fs, path)
			require.NoError(t, err)
			assert.Contains(t, string(data), a.userMarker, "user settings preserved through install + uninstall")
		})
	}
}
