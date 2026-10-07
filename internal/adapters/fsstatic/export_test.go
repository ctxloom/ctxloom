package fsstatic

// SetBeforeCommit parks every delivery s makes between its approach run and
// its commit, in f.
func SetBeforeCommit(s *Static, f func()) { s.beforeCommit = f }
