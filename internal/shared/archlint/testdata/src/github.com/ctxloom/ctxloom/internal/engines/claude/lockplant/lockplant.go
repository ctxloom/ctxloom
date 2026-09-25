// Package lockplant is the lock-discipline rule's fixture.
package lockplant

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
