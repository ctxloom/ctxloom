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
	rep := mockengine.BuildReport(cli, recs, nil, nil)

	// The present:false row for the absent agents directory.
	agents, ok := recordByKind(rep, "agents")
	if !ok {
		t.Fatal("no probe record for the agents surface — a dropped row is exactly what a silent no-op looks like")
	}
	if agents.Present {
		t.Fatalf("agents surface reported present, but it was never delivered: %+v", agents)
	}

	// claude declares TWO context probes in precedence order: the out-of-cwd
	// launch flag (--append-system-prompt-file, absent here) then the cwd
	// CLAUDE.md. Both must appear; the flag one absent, the cwd one present with
	// a matching hash.
	ctxFlag := recordAt(t, rep, "context", "flag-value")
	if ctxFlag.Present {
		t.Fatalf("append-system-prompt-file flag was absent but reported present: %+v", ctxFlag)
	}
	ctxCwd := recordAt(t, rep, "context", "cwd")
	if !ctxCwd.Present {
		t.Fatalf("CLAUDE.md was delivered but reported absent: %+v", ctxCwd)
	}
	if want := sha256hex(body); ctxCwd.SHA256 != want {
		t.Fatalf("context hash mismatch: got %s want %s", ctxCwd.SHA256, want)
	}
}

// recordAt returns the record for a (kind, scope) pair, failing if absent.
func recordAt(t *testing.T, rep mockengine.Report, kind, scope string) mockengine.ProbeRecord {
	t.Helper()
	for _, r := range rep.Records {
		if r.Kind == kind && r.Scope == scope {
			return r
		}
	}
	t.Fatalf("no probe record for kind=%s scope=%s", kind, scope)
	return mockengine.ProbeRecord{}
}

// recordByKind returns the first record of the given kind, or false. U079-F15:
// this used to be the exported Report.Record method, but it had no production
// caller (test-only convenience) and Report.Records is already an exported
// field every test in this package can range over directly — recordFor/
// recordForRel in container_docker_integration_test.go already duplicate this
// exact idea with a better (kind, scope) key. Kept as a plain test helper
// rather than deleted outright because two different _test.go files
// (discovery_test.go, arch_test.go) genuinely want "first record of this
// kind" with no scope to narrow by.
func recordByKind(rep mockengine.Report, kind string) (mockengine.ProbeRecord, bool) {
	for _, r := range rep.Records {
		if r.Kind == kind {
			return r, true
		}
	}
	return mockengine.ProbeRecord{}, false
}
