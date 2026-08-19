//go:build conformance

// The cross-agent equity suite. Gated behind the `conformance` build tag (see
// doc.go) and kept in its own package so it composes claude/codex without
// touching their per-module test files — safe alongside concurrent work. Every
// assertion goes through the public agent.SettingsWriter interface, so it is
// format-agnostic (claude JSON, codex TOML both pass the same suite).
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

	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/codex"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// settingsWriter is the suite's view of an agent writer: the shared
// agent.SettingsWriter contract plus the concrete writers' SettingsPath method
// (no longer part of the interface; every concrete writer still exposes it).
type settingsWriter interface {
	agent.SettingsWriter
	SettingsPath(projectDir string) string
}

// agentCase is one agent under test: its writer constructor plus a valid
// settings file carrying a user-authored entry the writer must preserve.
type agentCase struct {
	name       string
	newWriter  func(agent.SettingsOptions) settingsWriter
	userFile   string // a valid settings file (in the agent's format) with a user entry
	userMarker string // substring of userFile that must survive write + remove
}

// agentCases returns the agents this suite actually covers today — NOT every
// agent.SettingsWriter implementation in the repo. Three are absent, for three
// DIFFERENT reasons, not one shared oversight:
//
//   - codex's settings surface is LAUNCH-ONLY. Its config.toml lives in
//     $CODEX_HOME, and the only $CODEX_HOME ctxloom writes is a per-session
//     instance; its harpless agent.SettingsWriter methods therefore DECLARE
//     that absence rather than target a path (SettingsPath returns "",
//     WriteSettings refuses — internal/codex/declared_absence.go). Every
//     assertion below is "write at SettingsPath, read it back", which cannot be
//     asked of a writer that correctly has no path. The suite HONOURS that
//     declaration by not asking, exactly as it honours opencode's
//     noHooksReason below.
//     WHERE THE COVERAGE WENT, so this reads as a move and not a loss:
//     internal/codex's own settings_test.go / settings_wipe_test.go /
//     settings_sessionend_test.go drive the identical assertions — unparseable
//     refusal, per-event hook emission, MCP auto-registration,
//     remove-preserves-user — through writeSettingsIn, the resolved-home entry
//     point that is now the only writer there is.
//     WHAT IT COSTS, stated plainly: this suite's premise is CROSS-AGENT
//     EQUITY ("claude JSON, codex TOML both pass the same suite"), and with
//     codex gone it covers one agent and proves nothing cross-format. Restoring
//     that needs the suite reworked around a RESOLVED ENGINE HOME rather than a
//     project dir — a real change to its shape, not a table edit, and out of
//     scope for the slice that created this gap.
//
//   - kiro's writer has no SettingsPath method at all — its settings are
//     genuinely multi-file (agentPath/mcpPath/mcpLedgerPath/steeringPath,
//     see kiro/settings.go), so it does not implement this suite's
//     settingsWriter interface and `concrete[*kiro.KiroWriter]` does not
//     COMPILE (see concrete's doc). Covering kiro needs this suite's
//     path-based assertions (WriteSettings/AtomicWriteBackup/
//     RemovePreservesUser all read/write ONE file at SettingsPath) redesigned
//     for a multi-file writer first — not a one-line table addition.
//
//   - opencode's WriteSettings explicitly IGNORES the hooks argument (it "has
//     no ctxloom-style hook mechanism", see opencode/settings.go's own doc
//     comment) — TestConformance_HookEventCoverage would fail immediately,
//     correctly, on a documented and deliberate design gap this suite has no
//     vocabulary to distinguish from a real regression (the way the codex
//     no-backup case above IS distinguished, via backsUpCorruptFile).
//
// Add a new agent here ONLY once it can actually honor every assertion below
// (or once each assertion gains the same per-agent escape hatch
// backsUpCorruptFile already models for the one divergence that already had
// one) — a new agent here inherits the WHOLE equity suite unconditionally.
func agentCases() []agentCase {
	return []agentCase{
		{"claude-code", concrete[*claude.ClaudeCodeHookWriter](claude.NewWriter), `{"theme":"dark"}`, "dark"},
	}
}

// TestConformance_CodexIsAbsentByDECLARATION keeps the omission above from
// decaying into an unnoticed hole. A commented-out table row is a hole; an
// assertion that the row's PREMISE is still false is a statement that gets
// re-checked on every run — and goes red the day codex gains a project-keyed
// settings path, which is the day it belongs back in this table.
func TestConformance_CodexIsAbsentByDECLARATION(t *testing.T) {
	assert.Empty(t, (&codex.CodexHookWriter{}).SettingsPath("/project"),
		"codex is absent from agentCases because it has NO project-keyed settings path; if it has one again, add the row back")

	err := (&codex.CodexHookWriter{}).WriteSettings(standardHooks(), nil, "/project")
	assert.Error(t, err, "and because its harpless writer declares the absence rather than writing")
}

// concrete widens a constructor's agent.SettingsWriter result to this suite's
// richer settingsWriter view, naming the CONCRETE type it is expected to
// return.
//
// The type parameter is the point. Every NewWriter in the repo is declared as
// returning the narrow agent.SettingsWriter, so widening needs an assertion
// somewhere; making it `newWriter(o).(settingsWriter)` put that assertion at
// RUN time, inside a range expression, where a writer that stopped exposing
// SettingsPath produced a bare interface-conversion panic with no agent named
// — a compile-time contract spent as a runtime crash. Binding W instead makes
// the compiler check it at the call site: `concrete[*kiro.KiroWriter]` does
// not build, because KiroWriter's settings are genuinely multi-file and it has
// no SettingsPath at all. That is exactly the failure this table must produce
// for an agent it cannot yet cover.
func concrete[W settingsWriter](newWriter func(agent.SettingsOptions) agent.SettingsWriter) func(agent.SettingsOptions) settingsWriter {
	return func(o agent.SettingsOptions) settingsWriter { return newWriter(o).(W) }
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
// is intentionally absent — codex's CLI has no such event.
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
// settings file must NEVER be overwritten. WriteSettings must refuse (return a
// non-nil error naming the offending file) and must leave the original bytes
// on disk exactly as they were. This is the opposite of what this test used to
// assert — it formerly required WriteSettings to succeed and apply hooks over
// the corrupt file, which is precisely the silent-data-loss shape production
// now refuses (see agent.RefuseCorrupt). claude-code also backs
// the corrupt bytes up to a sibling "<path>.corrupt-<unix-ts>" file before
// refusing; that backup is asserted per-engine below since codex's writer
// (internal/codex/settings.go loadSettings/load) returns a bare error with no
// backup file — a genuine behavioural divergence between engines, not
// something this test papers over.
func TestConformance_RefusesToOverwriteUnparseableSettings(t *testing.T) {
	const corrupt = "!!! not valid !!!"

	// engines whose writer backs the corrupt original up to a sibling
	// "<path>.corrupt-<ts>" file before refusing to write. codex is
	// deliberately absent: its loader (internal/codex/settings.go) returns a
	// bare "refusing to write over a config.toml ctxloom could not read"
	// error with no backup file at all.
	backsUpCorruptFile := map[string]bool{
		"claude-code": true,
		"codex":       false,
	}

	for _, a := range agentCases() {
		t.Run(a.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			w := a.newWriter(agent.SettingsOptions{FS: fs})
			path := w.SettingsPath(projectDir)
			require.NoError(t, afero.WriteFile(fs, path, []byte(corrupt), 0644))

			err := w.WriteSettings(standardHooks(), nil, projectDir)
			require.Error(t, err, "WriteSettings must refuse rather than overwrite an unparseable prior file")
			assert.Contains(t, err.Error(), path, "the refusal error must name the offending file")

			data, readErr := afero.ReadFile(fs, path)
			require.NoError(t, readErr)
			assert.Equal(t, corrupt, string(data), "the original corrupt bytes must be left untouched, never overwritten")

			if backsUpCorruptFile[a.name] {
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
				assert.True(t, found, "%s must back the corrupt original up to a sibling %q file", a.name, prefix+"<unix-ts>")
			}
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

// TestConformance_AtomicWriteBackup: overwriting an existing settings file
// leaves a .ctxloom.bak of the prior content AND replaces the file atomically —
// both halves of the contract doc.go names, where the suite used to assert only
// the backup and take the atomicity on trust.
func TestConformance_AtomicWriteBackup(t *testing.T) {
	for _, a := range agentCases() {
		t.Run(a.name, func(t *testing.T) {
			fs := &recordingFs{Fs: afero.NewMemMapFs()}
			w := a.newWriter(agent.SettingsOptions{FS: fs})
			path := w.SettingsPath(projectDir)
			require.NoError(t, afero.WriteFile(fs, path, []byte(a.userFile), 0644))
			fs.reset() // that setup write is the test's, not the writer's

			require.NoError(t, w.WriteSettings(standardHooks(), nil, projectDir))

			bak, err := afero.ReadFile(fs, path+".ctxloom.bak")
			require.NoError(t, err, "a .ctxloom.bak of the prior settings must exist")
			assert.Contains(t, string(bak), a.userMarker, "backup holds the pre-write content")

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
// settings file. This is what catches an absent per-event mapping — a writer
// with no PreShell or PostFileEdit translation drops the command silently.
//
// WHAT IT DOES NOT PROVE, deliberately: that a command landed under the RIGHT
// native event. The assertion is a substring search over the file's bytes,
// because this suite is format-agnostic by construction (claude JSON,
// codex TOML, both through one interface), and asserting slot
// attachment needs per-agent format knowledge. That knowledge lives — and is
// asserted — in the per-agent tests: claude/hooks_wire_test.go and
// claude/surfacedelivery_test.go on "PreToolUse", codex/settings_test.go on
// "[[hooks.PreToolUse]]". doc.go used
// to call this "full hook-event coverage", which reads as the stronger claim.
func TestConformance_HookEventCoverage(t *testing.T) {
	for _, a := range agentCases() {
		t.Run(a.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			w := a.newWriter(agent.SettingsOptions{FS: fs})
			require.NoError(t, w.WriteSettings(standardHooks(), nil, projectDir))

			data, err := afero.ReadFile(fs, w.SettingsPath(projectDir))
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
// The all-at-once test above cannot distinguish a writer that translates five
// events from one that emits a fixed bundle whenever any hook is configured,
// or one that cross-wires two events into a single slot — every marker is
// present either way. Configuring exactly one event and requiring that exactly
// one marker appears separates those cases, which is as close to per-event
// attachment as a format-agnostic assertion can get.
func TestConformance_HookEventsAreEmittedIndependently(t *testing.T) {
	for _, a := range agentCases() {
		for _, ev := range coveredEvents {
			t.Run(a.name+"/"+ev.marker, func(t *testing.T) {
				fs := afero.NewMemMapFs()
				w := a.newWriter(agent.SettingsOptions{FS: fs})
				require.NoError(t, w.WriteSettings(onlyHooks(ev), nil, projectDir))

				data, err := afero.ReadFile(fs, w.SettingsPath(projectDir))
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
// path the test would have to know.
//
// A "<path>.ctxloom.bak" records the state BEFORE the write that produced it,
// and a ".corrupt-<ts>" file records bytes ctxloom refused to overwrite.
// Neither is a live managed artifact, so neither counts.
func liveFilesContaining(t *testing.T, fs afero.Fs, marker string) []string {
	t.Helper()
	var hits []string
	require.NoError(t, afero.Walk(fs, "/", func(path string, info os.FileInfo, err error) error {
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

// TestConformance_MCPAutoRegister: ctxloom's own MCP server is auto-registered
// with no explicit MCP config — reported by Status AND present in the bytes on
// disk, so the writer's own account of itself is not the only witness.
func TestConformance_MCPAutoRegister(t *testing.T) {
	for _, a := range agentCases() {
		t.Run(a.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			w := a.newWriter(agent.SettingsOptions{FS: fs})
			require.NoError(t, w.WriteSettings(standardHooks(), nil, projectDir))

			st, err := w.Status(projectDir)
			require.NoError(t, err)
			assert.True(t, st.MCPPresent, "ctxloom MCP server auto-registered")

			assert.NotEmpty(t, liveFilesContaining(t, fs, agent.MCPServerName),
				"the registration must exist in a file, not only in Status()")
		})
	}
}

// TestConformance_RemovePreservesUser: RemoveSettings strips every managed
// artifact while preserving the user's own settings. The managed hook commands
// are asserted PRESENT after the write and ABSENT after the removal, so the
// same predicate is shown to flip — a removal test that only asks Status()
// cannot tell "stripped" from "never written".
func TestConformance_RemovePreservesUser(t *testing.T) {
	for _, a := range agentCases() {
		t.Run(a.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			w := a.newWriter(agent.SettingsOptions{FS: fs})
			path := w.SettingsPath(projectDir)
			require.NoError(t, afero.WriteFile(fs, path, []byte(a.userFile), 0644))

			require.NoError(t, w.WriteSettings(standardHooks(), nil, projectDir))
			for _, ev := range coveredEvents {
				require.NotEmptyf(t, liveFilesContaining(t, fs, ev.marker),
					"precondition: managed event %q must be on disk before removal can be shown to strip it", ev.marker)
			}

			require.NoError(t, w.RemoveSettings(projectDir))

			st, err := w.Status(projectDir)
			require.NoError(t, err)
			assert.False(t, st.Wired(), "no managed artifacts remain after removal")
			for _, ev := range coveredEvents {
				assert.Emptyf(t, liveFilesContaining(t, fs, ev.marker),
					"managed event %q survived removal on disk while Status reported it gone", ev.marker)
			}

			data, err := afero.ReadFile(fs, path)
			require.NoError(t, err)
			assert.Contains(t, string(data), a.userMarker, "user settings preserved through write + remove")
		})
	}
}
