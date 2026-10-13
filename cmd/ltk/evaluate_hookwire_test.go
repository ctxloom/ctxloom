package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A hook command that names no engine (the bundle's `ltk evaluate`, delivered
// to every engine) reads THE HOOK WIRE, claude's PreToolUse (ruling #28):
// claude writes it natively, and opencode's bridge writes it with opencode's
// tools normalised to claude's names (apply_patch keeps its own). Every deny
// is claude's deny-with-reason; an allow writes nothing.
func TestEvaluateWithoutEngine_AnswersTheHookWire(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "rules.yaml")
	cfg := "schema_version: 1\nrules:\n  - id: no-force-push\n    match: { command: [git, push], args_all: [--force] }\n    message: no force pushes\npath_rules:\n  - id: no-vendor\n    match: { path: [\"/p/vendor/**\"] }\n    message: no vendor edits\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	const deny = `"permissionDecision":"deny"`
	engineName := newEvaluateCmd().Flag("engine").DefValue // the bundle's `ltk evaluate` names none
	for _, c := range []struct{ name, payload, want string }{
		{"claude bash", `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git push --force"}}`, "no force pushes"},
		{"bridge bash", `{"session_id":"ses_1","cwd":"/p","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git push --force","workdir":"/p"},"tool_use_id":"call_1"}`, "no force pushes"},
		{"bridge edit", `{"hook_event_name":"PreToolUse","tool_name":"Edit","tool_input":{"file_path":"/p/vendor/x.go","old_string":"a","new_string":"b"}}`, "no vendor edits"},
		{"bridge apply_patch", `{"hook_event_name":"PreToolUse","tool_name":"apply_patch","tool_input":{"patchText":"*** Begin Patch\n*** Update File: /p/vendor/x.go\n@@\n-a\n+b\n*** End Patch"}}`, "no vendor edits"},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, err := evaluate(engineName, cfgPath, "", strings.NewReader(c.payload))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(out.Stdout), deny) || !strings.Contains(string(out.Stdout), c.want) {
				t.Errorf("want claude's deny carrying %q, got %s", c.want, out.Stdout)
			}
		})
	}
	out, err := evaluate(engineName, cfgPath, "", strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git status"}}`))
	if err != nil || len(out.Stdout) != 0 || out.ExitCode != 0 {
		t.Errorf("an allowed command writes nothing, got %+v, %v", out, err)
	}
}

// ltk keeps ONE adapter, claude's: the hook wire is claude's on every engine,
// so --engine opencode names no adapter, and fails closed in the wire.
func TestEvaluate_OpencodeIsNoAdapter(t *testing.T) {
	if got := newEvaluateCmd().Flag("engine").DefValue; got != "claude-code" {
		t.Errorf("--engine defaults to claude-code, got %q", got)
	}
	out, err := evaluate("opencode", "", "", strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out.Stdout), `"permissionDecision":"deny"`) || !strings.Contains(string(out.Stdout), `unknown --engine \"opencode\"`) {
		t.Errorf("want claude's deny naming the unknown engine, got %s", out.Stdout)
	}
}
