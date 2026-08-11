package backends

import (
	"sort"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/spf13/afero"
)

// Settings options + shared write helpers live in shared/agent (the
// engine-agnostic core) so the per-agent writers can use them without importing
// backends. SettingsOption and the With* funcs are re-exported for external
// callers (internal/operations) that reach the KEPT settings-writer dispatch —
// GetSettingsWriter / RemoveSettings / BackendStatus. (The cross-backend settings
// WRITE now rides the surfaces × cells seam — see BuildSurfaces + agent.Select.)
type SettingsOption = agent.SettingsOption

// WithSettingsFS is the only option this facade re-exports, because the
// filesystem seam is the only thing SettingsOptions carries: statusline policy
// rides DeliverSettings on the surfaces seam.
var WithSettingsFS = agent.WithSettingsFS

// GetSettingsWriter returns a settings writer for the named backend, or nil if
// not supported. If fs is provided, it will be used for filesystem operations;
// otherwise the OS filesystem is used. The per-backend writer constructors
// live in the descriptor table (registry.go).
func GetSettingsWriter(name string, fs afero.Fs) agent.SettingsWriter {
	if d, ok := descriptors[name]; ok && d.newWriter != nil {
		return d.newWriter(agent.SettingsOptions{FS: fs})
	}
	return nil
}

// BackendsWithSettings returns the names of all backends that support
// settings, sorted (List()'s settings-scoped twin).
func BackendsWithSettings() []string {
	names := make([]string, 0, len(descriptors))
	for name, d := range descriptors {
		if d.newWriter != nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// The per-agent settings-writer helpers (hook-hash, managed-command detection,
// fs/atomic-write, ctxloom binary/args) now live in shared/agent and are used
// directly by the claude/codex writer modules — the transitional wrappers
// that used to bridge them here are gone.
