package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const agHookCmd = "ltk evaluate --engine antigravity --config ../.ltk.yaml"

func TestAntigravityHookCommand(t *testing.T) {
	// agy runs hooks with cwd <workspace>/.agents, so a relative rules path is
	// rebased one level up; an absolute path passes through untouched.
	if got := (Antigravity{}).HookCommand("ltk", ".ltk.yaml"); got != agHookCmd {
		t.Errorf("HookCommand = %q", got)
	}
	if got := (Antigravity{}).HookCommand("ltk", "/abs/rules.yaml"); got != "ltk evaluate --engine antigravity --config /abs/rules.yaml" {
		t.Errorf("HookCommand with absolute config = %q", got)
	}
	// evaluate defaults to claude-code, so --engine must always be present.
	if got := (Antigravity{}).HookCommand("ltk", ""); got != "ltk evaluate --engine antigravity" {
		t.Errorf("HookCommand without config = %q", got)
	}
	// A path with spaces is double-quoted so the hook shell-splits correctly
	// (double quotes keep env references expanding).
	if got := (Antigravity{}).HookCommand("ltk", "/my project/.ltk.yaml"); got != `ltk evaluate --engine antigravity --config "/my project/.ltk.yaml"` {
		t.Errorf("HookCommand with spaced config = %q", got)
	}
}

func TestAntigravitySettingsPath(t *testing.T) {
	p, err := (Antigravity{}).SettingsPath("/proj", false)
	if err != nil {
		t.Fatal(err)
	}
	if p != filepath.Join("/proj", ".agents", "hooks.json") {
		t.Errorf("SettingsPath = %q", p)
	}
	// agy v1.0.7 has no usable global hooks file (verified: the candidate
	// locations are ignored or hang headless agy) — global must error, not
	// write a dead file.
	if _, err := (Antigravity{}).SettingsPath("/proj", true); err == nil {
		t.Error("global SettingsPath should error")
	}
}

func TestAntigravityDetect(t *testing.T) {
	dir := t.TempDir()
	if got := (Antigravity{}).Detect(dir); got != 0 {
		t.Errorf("empty dir score = %d, want 0", got)
	}

	// A bare .agents/ is a weak marker (agy subagent scratch, other tools).
	if err := os.MkdirAll(filepath.Join(dir, ".agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := (Antigravity{}).Detect(dir); got != 1 {
		t.Errorf("bare .agents score = %d, want 1", got)
	}

	// hooks.json marks a genuine hook host.
	if err := os.WriteFile(filepath.Join(dir, ".agents", "hooks.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := (Antigravity{}).Detect(dir); got != 2 {
		t.Errorf(".agents/hooks.json score = %d, want 2", got)
	}
}

// TestDetectTiePrefersClaude pins the registry-order tie-break: a repo with
// both .claude/ and .agents/hooks.json resolves to Claude Code.
func TestDetectTiePrefersClaude(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".agents", "hooks.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	e, ok := Detect(dir)
	if !ok || e.Name() != "claude-code" {
		t.Fatalf("Detect = %v, want claude-code", e)
	}
}

func TestAntigravityInstallIntoEmpty(t *testing.T) {
	out, _, err := Antigravity{}.Install(nil, agHookCmd)
	if err != nil {
		t.Fatal(err)
	}
	pre := preToolUse(t, decodeSettingsJSON(t, out))
	if len(pre) != 1 {
		t.Fatalf("want 1 entry, got %d", len(pre))
	}
	if entry := pre[0].(map[string]any); entry["matcher"] != antigravityMatcher {
		t.Errorf("matcher = %v", entry["matcher"])
	}
	if !strings.Contains(string(out), agHookCmd) {
		t.Error("command missing from output")
	}
}

func TestAntigravityInstallPreservesOtherSettings(t *testing.T) {
	existing := `{
      "futureSetting": true,
      "hooks": {
        "PreToolUse": [{"matcher": "write_to_file", "hooks": [{"type": "command", "command": "other"}]}],
        "Stop": [{"hooks": [{"type": "command", "command": "cleanup"}]}]
      }
    }`
	out, _, err := Antigravity{}.Install([]byte(existing), agHookCmd)
	if err != nil {
		t.Fatal(err)
	}
	m := decodeSettingsJSON(t, out)
	if m["futureSetting"] != true {
		t.Error("unrelated setting dropped")
	}
	if _, ok := m["hooks"].(map[string]any)["Stop"]; !ok {
		t.Error("sibling Stop event dropped")
	}
	if len(preToolUse(t, m)) != 2 {
		t.Errorf("want 2 PreToolUse entries, got %d", len(preToolUse(t, m)))
	}
}

func TestAntigravityInstallIdempotent(t *testing.T) {
	first, _, _ := Antigravity{}.Install(nil, agHookCmd)
	second, _, err := Antigravity{}.Install(first, agHookCmd)
	if err != nil {
		t.Fatal(err)
	}
	if len(preToolUse(t, decodeSettingsJSON(t, second))) != 1 {
		t.Error("re-install added a duplicate")
	}
}

func TestAntigravityUninstallIsInverse(t *testing.T) {
	existing := `{"hooks":{"PreToolUse":[{"matcher":"run_command","hooks":[{"type":"command","command":"other"}]}]}}`
	installed, _, err := Antigravity{}.Install([]byte(existing), agHookCmd)
	if err != nil {
		t.Fatal(err)
	}
	out, didRemove, err := Antigravity{}.Uninstall(installed, agHookCmd)
	if err != nil {
		t.Fatal(err)
	}
	if !didRemove {
		t.Error("uninstall should report removed=true when it dropped our hook")
	}
	pre := preToolUse(t, decodeSettingsJSON(t, out))
	if len(pre) != 1 {
		t.Fatalf("want 1 entry after uninstall, got %d", len(pre))
	}
	if strings.Contains(string(out), agHookCmd) {
		t.Error("our hook command should be gone")
	}
}

func TestAntigravityUninstallNoHooksKeyIsNoop(t *testing.T) {
	out, didRemove, err := Antigravity{}.Uninstall([]byte(`{"futureSetting":true}`), agHookCmd)
	if err != nil {
		t.Fatal(err)
	}
	if didRemove {
		t.Error("nothing to remove → removed must be false")
	}
	if !strings.Contains(string(out), "futureSetting") {
		t.Error("uninstall should preserve unrelated settings")
	}
}

func TestAntigravityInstallRejectsMalformed(t *testing.T) {
	if _, _, err := (Antigravity{}).Install([]byte(`{"hooks":"nope"}`), agHookCmd); err == nil {
		t.Error("expected error when hooks is not an object")
	}
}
