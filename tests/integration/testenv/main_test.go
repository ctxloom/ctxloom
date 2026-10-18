package testenv

import (
	"os"
	"testing"
)

// TestMain scrubs the host session's variables before any test, which
// NewTestEnvironment requires of every binary that builds one.
func TestMain(m *testing.M) {
	ScrubProcessEnv()
	os.Exit(m.Run())
}
