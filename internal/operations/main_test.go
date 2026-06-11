package operations

import (
	"os"
	"os/exec"
	"testing"

	"github.com/ctxloom/ctxloom/internal/config"
)

// TestMain isolates the package from the developer's real home directory.
// Several operations fall back to the home config (homeDefaultProfiles,
// config.HomeConfigDir consumers); without isolation, whatever profiles or
// remotes the developer has configured in ~/.ctxloom leak into unit tests
// and change collection counts and sync statuses.
//
// It also makes companion-binary detection deterministic: built-in bundle
// fragments (and hooks/MCP) inject only when their companion (ltk, taskloom) is
// on PATH, so without this a developer who has those binaries installed sees
// them leak into context-assembly assertions. Tests that exercise built-in
// injection opt back in via config.SetLookPathForTesting.
func TestMain(m *testing.M) {
	os.Exit(func() int {
		home, err := os.MkdirTemp("", "ctxloom-test-home-*")
		if err == nil {
			os.Setenv("HOME", home)
			defer os.RemoveAll(home)
		}
		restore := config.SetLookPathForTesting(func(string) (string, error) {
			return "", exec.ErrNotFound
		})
		defer restore()
		return m.Run()
	}())
}
