package operations

import (
	"os"
	"testing"
)

// TestMain isolates the package from the developer's real home directory.
// Several operations fall back to the home config (homeDefaultProfiles,
// config.HomeConfigDir consumers); without isolation, whatever profiles or
// remotes the developer has configured in ~/.ctxloom leak into unit tests
// and change collection counts and sync statuses.
func TestMain(m *testing.M) {
	os.Exit(func() int {
		home, err := os.MkdirTemp("", "ctxloom-test-home-*")
		if err == nil {
			os.Setenv("HOME", home)
			defer os.RemoveAll(home)
		}
		return m.Run()
	}())
}
