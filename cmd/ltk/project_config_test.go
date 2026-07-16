package main

import (
	"context"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/app"
	"github.com/ctxloom/ctxloom/internal/ltk/engine"
	"github.com/ctxloom/ctxloom/internal/ltk/ir"
	"github.com/ctxloom/ctxloom/internal/ltk/rules"
)

// projectConfigPath is this repository's own .ltk/config.yaml, loaded by
// relative path from cmd/ltk. This project has a documented history of a
// redirect rule being written but never actually verified to fire — the only
// way to catch that regression is to run the live config against the exact
// bare commands it claims to redirect, not to eyeball the YAML.
const projectConfigPath = "../../.ltk/config.yaml"

// TestProjectRulesRedirectBareToolInvocations loads the real repository
// .ltk/config.yaml (not a synthetic test fixture) and asserts that each bare
// command it claims to redirect is actually denied, with the expected
// suggestion. A rule ID/message can drift from the YAML's intent (typo'd
// match pattern, wrong program name) without this failing any other test,
// since nothing else evaluates the shipped config against real commands.
func TestProjectRulesRedirectBareToolInvocations(t *testing.T) {
	cfg, err := rules.Load(projectConfigPath)
	if err != nil {
		t.Fatalf("load %s: %v", projectConfigPath, err)
	}
	a := app.New(cfg)

	cases := []struct {
		name           string
		command        string
		wantReasonHas  string
		wantSuggestion string
	}{
		{
			name:           "go test",
			command:        "go test ./...",
			wantReasonHas:  "task runner",
			wantSuggestion: "just test   # or `just test-verbose` for an offline host run",
		},
		{
			name:           "go build",
			command:        "go build ./...",
			wantReasonHas:  "task runner",
			wantSuggestion: "just build   # or `just build-verbose` locally",
		},
		{
			name:           "go install",
			command:        "go install ./...",
			wantReasonHas:  "task runner",
			wantSuggestion: "just install",
		},
		{
			name:           "goreleaser",
			command:        "goreleaser release --snapshot",
			wantReasonHas:  "devcontainer",
			wantSuggestion: "just release-snapshot   # or `just release-check` to validate .goreleaser.yml only",
		},
		{
			name:           "gremlins",
			command:        "gremlins unleash",
			wantReasonHas:  "task runner",
			wantSuggestion: "just test-mutation-pkg internal/shared/tasks/triggers   # or `just test-mutation` for the whole module",
		},
		{
			name:           "bare buf generate",
			command:        "buf generate",
			wantReasonHas:  "unpinned",
			wantSuggestion: "just proto   # or `just gen-mcp-schemas` for the MCP tool-schema regen",
		},
		{
			name:           "bare buf build (used by gen-mcp-schemas)",
			command:        "buf build -o /tmp/out",
			wantReasonHas:  "unpinned",
			wantSuggestion: "just proto   # or `just gen-mcp-schemas` for the MCP tool-schema regen",
		},
		{
			name:           "buf invoked by full host path",
			command:        "/home/babbitt/.local/bin/buf generate",
			wantReasonHas:  "unpinned",
			wantSuggestion: "just proto   # or `just gen-mcp-schemas` for the MCP tool-schema regen",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := a.Decide(context.Background(), engine.Request{
				Command: tc.command,
				Shell:   ir.ShellBash,
			})
			if resp.Allow {
				t.Fatalf("expected %q to be denied by the project's .ltk/config.yaml, but it was allowed", tc.command)
			}
			if !strings.Contains(resp.Reason, tc.wantReasonHas) {
				t.Errorf("reason = %q, want it to contain %q", resp.Reason, tc.wantReasonHas)
			}
			if resp.Suggest != tc.wantSuggestion {
				t.Errorf("suggest = %q, want %q", resp.Suggest, tc.wantSuggestion)
			}
		})
	}
}

// TestProjectRulesAllowTheirOwnJustTargets is the flip side: the just targets
// the rules above redirect to must themselves be allowed through, or the
// redirect is a dead end.
func TestProjectRulesAllowTheirOwnJustTargets(t *testing.T) {
	cfg, err := rules.Load(projectConfigPath)
	if err != nil {
		t.Fatalf("load %s: %v", projectConfigPath, err)
	}
	a := app.New(cfg)

	for _, command := range []string{
		"just test",
		"just build",
		"just install",
		"just release-snapshot",
		"just test-mutation",
		"just proto",
		"just gen-mcp-schemas",
	} {
		t.Run(command, func(t *testing.T) {
			resp := a.Decide(context.Background(), engine.Request{
				Command: command,
				Shell:   ir.ShellBash,
			})
			if !resp.Allow {
				t.Fatalf("expected %q to be allowed, but it was denied: %s", command, resp.Message())
			}
		})
	}
}
