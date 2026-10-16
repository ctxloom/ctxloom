//go:build !windows

// The host claude stand-in is a #!/bin/sh script.

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestComposeEngines_AgentImagePinsTheHostClaude: the composition root hands
// isolation the App's engine-version probe, so the claude agent image a run
// resolves is keyed on the claude installed on this host.
func TestComposeEngines_AgentImagePinsTheHostClaude(t *testing.T) {
	testsupport.Isolate(t)
	bin := t.TempDir()
	script := "#!/bin/sh\necho '2.1.290 (Claude Code)'\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := composeEngines(); err != nil {
		t.Fatal(err)
	}

	ref, _ := isolation.LiveImageRef(isolation.Host{}, "claude-code", isolation.ImageConfig{Base: "ctxloom"})
	if !strings.Contains(ref, ":cli2.1.290-") && !strings.Contains(ref, "-cli2.1.290-") {
		t.Fatalf("claude agent image %q is not keyed on the host's claude 2.1.290", ref)
	}
}
