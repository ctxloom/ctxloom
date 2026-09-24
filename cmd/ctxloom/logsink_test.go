package main

import (
	"path/filepath"
	"testing"
)

// logHome points HOME at a temp dir and returns ctxloom's log path under it.
func logHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return filepath.Join(home, ".ctxloom", "logs", "ctxloom.log")
}
