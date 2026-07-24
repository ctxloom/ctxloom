package mockengine_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/mockengine"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// sha256hex mirrors the runtime's documented hash (sha256, lowercase hex, raw
// bytes) so the test asserts against the same algorithm without reaching into
// the package's unexported hasher.
func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// claudeOneshot resolves claude's oneshot EngineCLI from the REAL declaration,
// so these tests exercise the same probe list the driver reads.
func claudeOneshot(t *testing.T) agent.EngineCLI {
	t.Helper()
	cli, ok := agent.EngineCLIFor(claude.ClaudeEngineCLIs(), agent.CLISurfaceOneshot)
	if !ok {
		t.Fatal("claude declares no oneshot surface")
	}
	return cli
}

// TestWalk_AbsentSurfaceIsPresentFalse is the highest-value assertion in the
// whole mock: a surface the engine PROBES but that was NOT delivered must
// produce a present:false record — not a crash, and not a silently dropped row.
// A silent no-op looks EXACTLY like a missing present:false row, so this is the
// test written first and watched fail. .claude/agents/ is the natural subject:
// claude reads it, but ctxloom has no delivery surface that writes it today.
func TestWalk_AbsentSurfaceIsPresentFalse(t *testing.T) {
	cli := claudeOneshot(t)
	cwd := t.TempDir()

	// Deliver ONLY the context file; leave .claude/agents/ absent.
	body := []byte("# CLAUDE.md\nproject guidance\n")
	if err := os.WriteFile(filepath.Join(cwd, "CLAUDE.md"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	argv, err := cli.ParseArgv([]string{"--print"})
	if err != nil {
		t.Fatalf("parse argv: %v", err)
	}
	recs := mockengine.Walk(cli, argv, mockengine.Resolver{
		Cwd:    cwd,
		Home:   t.TempDir(),
		Getenv: func(string) string { return "" },
	})
	rep := mockengine.BuildReport(cli.Engine, string(cli.Surface), recs, nil)

	// The present:false row for the absent agents directory.
	agents, ok := rep.Record("agents")
	if !ok {
		t.Fatal("no probe record for the agents surface — a dropped row is exactly what a silent no-op looks like")
	}
	if agents.Present {
		t.Fatalf("agents surface reported present, but it was never delivered: %+v", agents)
	}

	// The present:true row for the delivered context file, with a matching hash.
	ctx, ok := rep.Record("context")
	if !ok {
		t.Fatal("no probe record for the context surface")
	}
	if !ctx.Present {
		t.Fatalf("CLAUDE.md was delivered but reported absent: %+v", ctx)
	}
	wantHash := sha256hex(body)
	if ctx.SHA256 != wantHash {
		t.Fatalf("context hash mismatch: got %s want %s", ctx.SHA256, wantHash)
	}
}
