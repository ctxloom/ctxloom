// Package agent holds the lock-discipline rule's exempt primitive fixture.
package agent

type servers struct{}

func (servers) WriteServers() error { return nil }

func readSettings() {}

// persist has the read-then-write shape but lives in an exempt primitive file.
func persist(s servers) {
	readSettings()
	_ = s.WriteServers()
}
