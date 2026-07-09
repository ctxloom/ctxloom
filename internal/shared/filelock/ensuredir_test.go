package filelock

import "testing"

// ensureDir must tolerate a bare, separator-less path: the hand-rolled parent
// slice underflowed and panicked on Lock("file.lock").
func TestEnsureDir_BareFilename(t *testing.T) {
	if err := ensureDir("file.lock"); err != nil {
		t.Fatalf("ensureDir(bare) = %v, want nil", err)
	}
}
