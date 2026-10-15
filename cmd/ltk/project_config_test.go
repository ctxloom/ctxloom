package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/app"
	"github.com/ctxloom/ctxloom/internal/ltk/engine"
	"github.com/ctxloom/ctxloom/internal/ltk/ir"
	"github.com/ctxloom/ctxloom/internal/ltk/rules"
	"github.com/ctxloom/ctxloom/internal/testsupport/sourcedir"
)

// projectConfigPath is this repository's own .ltk/config.yaml. This project has a documented history of a
// redirect rule being written but never actually verified to fire — the only
// way to catch that regression is to run the live config against the exact
// bare commands it claims to redirect, not to eyeball the YAML.
//
// It is located from this package's source directory, not the working
// directory: TestMain sandboxes the cwd to a throwaway root.
func projectConfigPath(t *testing.T) string {
	t.Helper()
	dir, err := sourcedir.Dir()
	if err != nil {
		t.Fatalf("locate cmd/ltk's source directory: %v", err)
	}
	return filepath.Join(dir, "..", "..", ".ltk", "config.yaml")
}

// TestProjectRulesRedirectBareToolInvocations loads the real repository
// .ltk/config.yaml (not a synthetic test fixture) and asserts that each bare
// command it claims to redirect is actually denied, with the expected
// suggestion. A rule ID/message can drift from the YAML's intent (typo'd
// match pattern, wrong program name) without this failing any other test,
// since nothing else evaluates the shipped config against real commands.
func TestProjectRulesRedirectBareToolInvocations(t *testing.T) {
	cfgPath := projectConfigPath(t)
	cfg, _, err := rules.Load(cfgPath)
	if err != nil {
		t.Fatalf("load %s: %v", cfgPath, err)
	}
	a := app.New(cfg, app.Shells{})

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
			name:           "gomutants",
			command:        "gomutants ./...",
			wantReasonHas:  "task runner",
			wantSuggestion: "just test-mutation-pkg internal/shared/tasks/triggers   # or `just test-mutation-diff BASE` for a change",
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
	cfgPath := projectConfigPath(t)
	cfg, _, err := rules.Load(cfgPath)
	if err != nil {
		t.Fatalf("load %s: %v", cfgPath, err)
	}
	a := app.New(cfg, app.Shells{})

	for _, command := range []string{
		"just test",
		"just build",
		"just install",
		"just release-snapshot",
		"just test-mutation-pkg internal/shared/tasks/triggers",
		"just test-mutation-diff HEAD",
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

// TestProjectRulesGuardGitBisectRun pins the guidable-shut rule (owner
// decision #8, 2026-10-13). `git bisect run` exports GIT_DIR to the command it
// runs, so any git inside that command targets the OUTER repo: on 2026-10-12 a
// bisect script's `git -C <tmp> init` re-initialised the shared repo as bare
// and broke every worktree. The only cleared spelling is the run command
// wrapped in `env -u GIT_DIR`; every other bisect subcommand is untouched.
func TestProjectRulesGuardGitBisectRun(t *testing.T) {
	cfgPath := projectConfigPath(t)
	cfg, _, err := rules.Load(cfgPath)
	if err != nil {
		t.Fatalf("load %s: %v", cfgPath, err)
	}
	a := app.New(cfg, app.Shells{})
	const wantSuggestion = "git bisect run env -u GIT_DIR <script> [args...]   # the script's own git commands then find their repo from the cwd, not the bisected one"

	decide := func(command string) engine.Response {
		return a.Decide(context.Background(), engine.Request{Command: command, Shell: ir.ShellBash})
	}

	for _, command := range []string{
		"git bisect run ./probe.sh",
		"git bisect run sh -c 'git -C /tmp/scratch init'",
		// A global option ahead of the subcommand does not hide it.
		"git -C /repo bisect run ./probe.sh",
		// Wrappers ltk unwraps: a shell body, an env/assignment prefix.
		"sh -c 'git bisect run ./probe.sh'",
		"bash -c \"cd /repo && git bisect run make test\"",
		"env GIT_DIR=/x git bisect run ./probe.sh",
		"GIT_DIR= git bisect run ./probe.sh",
		// env without the unset, or setting GIT_DIR instead, is not the guard.
		"git bisect run env ./probe.sh",
		"git bisect run env GIT_DIR=/x ./probe.sh",
		"git bisect run env -i ./probe.sh",
		// Fail-closed limitation: the allow is position-anchored, so a global
		// option that takes an operand slot keeps even the wrapped form out of
		// it. `cd /repo && git bisect run env -u GIT_DIR …` is the way through.
		"git -C /repo bisect run env -u GIT_DIR ./probe.sh",
	} {
		t.Run("deny/"+command, func(t *testing.T) {
			resp := decide(command)
			if resp.Allow {
				t.Fatalf("expected %q to be denied, but it was allowed", command)
			}
			if !strings.Contains(resp.Reason, "GIT_DIR") {
				t.Errorf("reason = %q, want it to name GIT_DIR", resp.Reason)
			}
			if resp.Suggest != wantSuggestion {
				t.Errorf("suggest = %q, want %q", resp.Suggest, wantSuggestion)
			}
		})
	}

	for _, command := range []string{
		"git bisect run env -u GIT_DIR ./probe.sh",
		"git bisect run env -u GIT_DIR sh -c 'git -C /tmp/scratch init && make test'",
		"cd /repo && git bisect run env -u GIT_DIR ./probe.sh",
		"git bisect start HEAD v0.6.0",
		"git bisect good",
		"git bisect bad HEAD",
		"git bisect skip",
		"git bisect log",
		"git bisect reset",
	} {
		t.Run("allow/"+command, func(t *testing.T) {
			if resp := decide(command); !resp.Allow {
				t.Fatalf("expected %q to be allowed, but it was denied: %s", command, resp.Message())
			}
		})
	}
}
