package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// check is the GUI/query path: evaluate one command and return a structured
// {decision, message, suggestion} with the message and suggestion as discrete
// fields (never the concatenated hook-reason string).
func TestRunCheck(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "rules.yaml")
	cfg := `version: 1
rules:
  - id: no-force-push
    match: { command: [git, push, --force] }
    message: "no force pushes"
    suggest: "git push --force-with-lease"
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("denied command returns deny with discrete message and suggestion", func(t *testing.T) {
		var buf bytes.Buffer
		if err := runCheck(&buf, "git push --force", cfgPath, "", "json"); err != nil {
			t.Fatal(err)
		}
		var got checkResult
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatalf("output should be valid JSON, got %s (%v)", buf.String(), err)
		}
		want := checkResult{Decision: "deny", Message: "no force pushes", Suggestion: "git push --force-with-lease"}
		if got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("allowed command returns a bare allow", func(t *testing.T) {
		var buf bytes.Buffer
		if err := runCheck(&buf, "git status", cfgPath, "", "json"); err != nil {
			t.Fatal(err)
		}
		var got checkResult
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Decision != "allow" || got.Message != "" || got.Suggestion != "" {
			t.Errorf("an allow must carry no message/suggestion, got %+v", got)
		}
	})

	t.Run("wrapped denied command is still denied", func(t *testing.T) {
		var buf bytes.Buffer
		if err := runCheck(&buf, "bash -ec 'git push --force'", cfgPath, "", "json"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(buf.String(), `"deny"`) {
			t.Errorf("the bash -c wrapper must not bypass the rule, got %s", buf.String())
		}
	})

	t.Run("text format prints a human verdict", func(t *testing.T) {
		var buf bytes.Buffer
		if err := runCheck(&buf, "git push --force", cfgPath, "", "text"); err != nil {
			t.Fatal(err)
		}
		out := buf.String()
		if !strings.Contains(out, "deny") || !strings.Contains(out, "no force pushes") ||
			!strings.Contains(out, "Use instead: git push --force-with-lease") {
			t.Errorf("text output should carry the verdict, message and suggestion, got %q", out)
		}
	})

	// Routing structured output through clifmt widens check from text/json to
	// the full five formats: yaml now renders {decision, message, suggestion}.
	t.Run("yaml renders the structured verdict", func(t *testing.T) {
		var buf bytes.Buffer
		if err := runCheck(&buf, "git push --force", cfgPath, "", "yaml"); err != nil {
			t.Fatal(err)
		}
		out := buf.String()
		if !strings.Contains(out, "decision: deny") || !strings.Contains(out, "message: no force pushes") {
			t.Errorf("yaml output should carry the structured verdict, got %q", out)
		}
	})

	t.Run("toml renders the structured verdict", func(t *testing.T) {
		var buf bytes.Buffer
		if err := runCheck(&buf, "git push --force", cfgPath, "", "toml"); err != nil {
			t.Fatal(err)
		}
		out := buf.String()
		if !strings.Contains(out, "decision = 'deny'") || !strings.Contains(out, "message = 'no force pushes'") {
			t.Errorf("toml output should carry the structured verdict, got %q", out)
		}
	})

	t.Run("markdown renders the structured verdict", func(t *testing.T) {
		var buf bytes.Buffer
		if err := runCheck(&buf, "git push --force", cfgPath, "", "markdown"); err != nil {
			t.Fatal(err)
		}
		out := buf.String()
		if !strings.Contains(out, "deny") || !strings.Contains(out, "no force pushes") {
			t.Errorf("markdown output should carry the structured verdict, got %q", out)
		}
	})

	t.Run("unknown format errors", func(t *testing.T) {
		var buf bytes.Buffer
		if err := runCheck(&buf, "git status", cfgPath, "", "xml"); err == nil {
			t.Error("an unsupported --format should error")
		}
	})

	t.Run("unknown shell errors (loud, unlike the hook)", func(t *testing.T) {
		var buf bytes.Buffer
		if err := runCheck(&buf, "git status", cfgPath, "fish", "json"); err == nil {
			t.Error("check is an explicit command; an unknown --shell must error, not fail closed")
		}
	})

	t.Run("broken config errors (loud, unlike the hook)", func(t *testing.T) {
		broken := filepath.Join(t.TempDir(), "broken.yaml")
		if err := os.WriteFile(broken, []byte("version: 1\nrulez:\n  - id: oops\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := runCheck(&buf, "git status", broken, "", "json"); err == nil {
			t.Error("a broken config must error on the explicit check path")
		}
	})
}
