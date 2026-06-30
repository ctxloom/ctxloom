package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

const hookCmd = "ltk evaluate --config .ltk.yaml"

func decodeSettingsJSON(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, b)
	}
	return m
}

func preToolUse(t *testing.T, m map[string]any) []any {
	t.Helper()
	hooks, ok := m["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("hooks missing/not object: %v", m["hooks"])
	}
	pre, ok := hooks["PreToolUse"].([]any)
	if !ok {
		t.Fatalf("PreToolUse missing/not array: %v", hooks["PreToolUse"])
	}
	return pre
}

func TestClaudeHookCommand(t *testing.T) {
	if got := (ClaudeCode{}).HookCommand("ltk", ".ltk.yaml"); got != hookCmd {
		t.Errorf("HookCommand = %q", got)
	}
	if got := (ClaudeCode{}).HookCommand("ltk", ""); got != "ltk evaluate" {
		t.Errorf("HookCommand without config = %q", got)
	}
}

func TestClaudeInstallIntoEmpty(t *testing.T) {
	out, note, err := ClaudeCode{}.Install(nil, hookCmd)
	if err != nil {
		t.Fatal(err)
	}
	if note != "" {
		t.Errorf("fresh install should have no repair note, got %q", note)
	}
	pre := preToolUse(t, decodeSettingsJSON(t, out))
	if len(pre) != 1 {
		t.Fatalf("want 1 entry, got %d", len(pre))
	}
	if entry := pre[0].(map[string]any); entry["matcher"] != claudeMatcher {
		t.Errorf("matcher = %v", entry["matcher"])
	}
	if !strings.Contains(string(out), hookCmd) {
		t.Error("command missing from output")
	}
}

func TestClaudeInstallPreservesOtherSettings(t *testing.T) {
	existing := `{
      "model": "opus",
      "hooks": {
        "PreToolUse": [{"matcher": "Edit", "hooks": [{"type": "command", "command": "other"}]}],
        "PostToolUse": [{"matcher": "Bash", "hooks": []}]
      }
    }`
	out, _, err := ClaudeCode{}.Install([]byte(existing), hookCmd)
	if err != nil {
		t.Fatal(err)
	}
	m := decodeSettingsJSON(t, out)
	if m["model"] != "opus" {
		t.Error("unrelated setting dropped")
	}
	if _, ok := m["hooks"].(map[string]any)["PostToolUse"]; !ok {
		t.Error("sibling PostToolUse dropped")
	}
	if len(preToolUse(t, m)) != 2 {
		t.Errorf("want 2 PreToolUse entries, got %d", len(preToolUse(t, m)))
	}
}

func TestClaudeInstallIdempotent(t *testing.T) {
	first, _, _ := ClaudeCode{}.Install(nil, hookCmd)
	second, note, err := ClaudeCode{}.Install(first, hookCmd)
	if err != nil {
		t.Fatal(err)
	}
	if len(preToolUse(t, decodeSettingsJSON(t, second))) != 1 {
		t.Error("re-install added a duplicate")
	}
	if note != "" {
		t.Errorf("re-install over a current matcher should have no repair note, got %q", note)
	}
}

// Re-running install over a settings file whose entry still carries an older,
// narrower matcher must upgrade the matcher in place: leaving it stale would
// silently keep the tools added since then (e.g. NotebookEdit) ungated.
func TestClaudeInstallUpgradesStaleMatcher(t *testing.T) {
	stale := `{"hooks":{"PreToolUse":[{"matcher":"Bash|Edit|Write|MultiEdit","hooks":[{"type":"command","command":"` + hookCmd + `"}]}]}}`
	out, note, err := ClaudeCode{}.Install([]byte(stale), hookCmd)
	if err != nil {
		t.Fatal(err)
	}
	pre := preToolUse(t, decodeSettingsJSON(t, out))
	if len(pre) != 1 {
		t.Fatalf("matcher upgrade must not duplicate the entry, got %d", len(pre))
	}
	if m := pre[0].(map[string]any)["matcher"]; m != claudeMatcher {
		t.Errorf("matcher = %v, want %q", m, claudeMatcher)
	}
	if !strings.Contains(note, claudeMatcher) {
		t.Errorf("the upgrade should be reported in the install note, got %q", note)
	}
}

func TestClaudeUninstallIsInverse(t *testing.T) {
	// install then uninstall should leave other entries intact and ours gone.
	existing := `{"hooks":{"PreToolUse":[{"matcher":"Edit","hooks":[{"type":"command","command":"other"}]}]}}`
	installed, _, err := ClaudeCode{}.Install([]byte(existing), hookCmd)
	if err != nil {
		t.Fatal(err)
	}
	if len(preToolUse(t, decodeSettingsJSON(t, installed))) != 2 {
		t.Fatal("install should have added our entry")
	}
	out, didRemove, err := ClaudeCode{}.Uninstall(installed, hookCmd)
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
	if strings.Contains(string(out), hookCmd) {
		t.Error("our hook command should be gone")
	}
}

func TestClaudeUninstallNoHooksKeyIsNoop(t *testing.T) {
	out, didRemove, err := ClaudeCode{}.Uninstall([]byte(`{"model":"opus"}`), hookCmd)
	if err != nil {
		t.Fatal(err)
	}
	if didRemove {
		t.Error("nothing to remove → removed must be false")
	}
	if !strings.Contains(string(out), "opus") {
		t.Error("uninstall should preserve unrelated settings")
	}
}

// Uninstalling with a command that doesn't match any installed hook (e.g.
// different --bin/--config flags than the install) must report removed=false so
// the caller can skip the rewrite instead of claiming a removal.
func TestClaudeUninstallNonMatchingReportsNotRemoved(t *testing.T) {
	installed, _, err := ClaudeCode{}.Install(nil, hookCmd)
	if err != nil {
		t.Fatal(err)
	}
	_, didRemove, err := ClaudeCode{}.Uninstall(installed, "ltk evaluate --config OTHER.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if didRemove {
		t.Error("a non-matching command must report removed=false")
	}
}

// A PreToolUse entry whose hooks array holds both our command and a sibling must
// survive uninstall with the sibling intact and ours gone.
func TestClaudeUninstallKeepsSiblingHook(t *testing.T) {
	existing := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"` + hookCmd + `"},{"type":"command","command":"sibling"}]}]}}`
	out, didRemove, err := ClaudeCode{}.Uninstall([]byte(existing), hookCmd)
	if err != nil {
		t.Fatal(err)
	}
	if !didRemove {
		t.Error("our hook should have been removed")
	}
	if strings.Contains(string(out), hookCmd) {
		t.Error("our hook command should be gone")
	}
	if !strings.Contains(string(out), "sibling") {
		t.Error("the sibling hook must be preserved")
	}
	if len(preToolUse(t, decodeSettingsJSON(t, out))) != 1 {
		t.Error("the entry should survive because it still has the sibling hook")
	}
}

func TestClaudeInstallRejectsMalformed(t *testing.T) {
	if _, _, err := (ClaudeCode{}).Install([]byte(`{"hooks":"nope"}`), hookCmd); err == nil {
		t.Error("expected error when hooks is not an object")
	}
}

// quotePathIfNeeded must neutralize shell metacharacters: the path is spliced
// into a shell-executed hook command line, so an embedded quote, $(…), or
// backtick must never escape the quoting. Plain paths stay byte-stable.
func TestQuotePathIfNeeded(t *testing.T) {
	cases := []struct{ in, want string }{
		{".ltk/config.yaml", ".ltk/config.yaml"}, // safe set: unquoted, byte-stable
		{"/abs/p-2/.ltk_v1.yaml~", "/abs/p-2/.ltk_v1.yaml~"},
		{"my dir/rules.yaml", `"my dir/rules.yaml"`}, // whitespace
		{"a\tb.yaml", "\"a\tb.yaml\""},               // tab
		{`pa"th.yaml`, `"pa\"th.yaml"`},              // embedded quote can't close ours
		{"$(rm -rf x).yaml", `"\$(rm -rf x).yaml"`},  // command substitution neutralized
		{"`id`.yaml", "\"\\`id\\`.yaml\""},           // backticks neutralized
		{"a;b.yaml", `"a;b.yaml"`},                   // command separator quoted
		{`back\slash.yaml`, `"back\\slash.yaml"`},    // backslash can't un-escape
		{"${HOME}/r.yaml", `"\${HOME}/r.yaml"`},      // env reference stays literal
	}
	for _, c := range cases {
		if got := quotePathIfNeeded(c.in); got != c.want {
			t.Errorf("quotePathIfNeeded(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
