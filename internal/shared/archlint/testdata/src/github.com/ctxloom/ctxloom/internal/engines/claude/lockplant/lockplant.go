// Package lockplant is the lock-discipline rule's fixture.
package lockplant

import (
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/spf13/afero"
)

type servers struct{}

func (servers) WriteServers() error  { return nil }
func (servers) RemoveServers() error { return nil }

func loadSettings() {}

// WithFileLock stands in for sessions.WithFileLock; the rule matches the name.
func WithFileLock(fn func()) { fn() }

func addUnlocked(s servers) {
	loadSettings() // want `addUnlocked reads and then writes engine settings`
	_ = s.WriteServers()
}

func removeUnlocked(s servers) {
	loadSettings() // want `removeUnlocked reads and then writes engine settings`
	_ = s.RemoveServers()
}

func addLocked(s servers) {
	WithFileLock(func() {
		loadSettings()
		_ = s.WriteServers()
	})
}

// writeUnlocked writes through the write library, which counts as a write by
// its import, not its bare name.
func writeUnlocked(fs afero.Fs) {
	loadSettings() // want `writeUnlocked reads and then writes engine settings`
	_ = safefs.WriteFile(fs, "settings.json", nil, 0o600)
}

type notes struct{}

func (notes) WriteFile() error { return nil }

// notSafefs calls a WriteFile that is not the write library's: no signal.
func notSafefs(n notes) {
	loadSettings()
	_ = n.WriteFile()
}
