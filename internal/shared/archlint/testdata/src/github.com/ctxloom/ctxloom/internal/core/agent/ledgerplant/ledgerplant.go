// Package ledgerplant is the ledger-discipline rule's fixture.
package ledgerplant

type servers struct{}

func (servers) WriteServers() error { return nil }

func syncManagedEntries() {}
func writeLedger()        {}

func writeManagedUnrecorded(s servers) {
	_ = s.WriteServers() // want `writeManagedUnrecorded writes a managed subset of a config file without referencing any ownership record`
	syncManagedEntries()
}

func writeManagedRecorded(s servers) {
	_ = s.WriteServers()
	syncManagedEntries()
	writeLedger()
}
