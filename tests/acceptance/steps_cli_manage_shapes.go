//go:build acceptance

// CLI-noun support for cli/manage.feature: PAYLOAD-level assertions on
// .claude/settings.json's hook table and on an MCP registry's server
// entries, scoped precisely enough that neither can be satisfied by a
// neighboring field.
//
// Both reuse steps_j000400.go's JSON parsing (j000400ReadJSON,
// j000400HookCommandsFrom) rather than re-deriving it: the shape of a
// generated hooks table or mcpServers map is the SAME fact whether the file
// under test came from `profile materialize` (j000400's own steps) or from
// `manage install` / `manage hooks install` / `manage uninstall` (this
// file's).
package acceptance

import (
	"context"
	"fmt"
	"strings"

	"github.com/cucumber/godog"
)

func registerCLIManageShapeSteps(ctx *godog.ScenarioContext) {
	// Claude Code's statusLine command ALSO contains the substring "ctxloom
	// hook" (`ctxloom hook hud`), so a claim about whether a hook was
	// installed or removed has to be scoped to the hooks table itself —
	// never a whole-file substring, which the statusLine satisfies whether
	// or not any hook was ever installed.
	ctx.Step(`^the file "([^"]*)" registers a SessionStart hook whose command contains "([^"]*)"$`, func(c context.Context, rel, want string) error {
		return assertSessionStartHookCommand(worldFrom(c), rel, want, true)
	})

	ctx.Step(`^the file "([^"]*)" registers no SessionStart hook whose command contains "([^"]*)"$`, func(c context.Context, rel, want string) error {
		return assertSessionStartHookCommand(worldFrom(c), rel, want, false)
	})

	// The MCP claim is a real server entry with a command to launch, not the
	// bare string "ctxloom" appearing anywhere in the file — a comment, an
	// unrelated key, or a path segment would all satisfy that just as well.
	ctx.Step(`^the file "([^"]*)" registers an MCP server named "([^"]*)"$`, func(c context.Context, rel, name string) error {
		return assertMCPServerNamed(worldFrom(c), rel, name, true)
	})

	ctx.Step(`^the file "([^"]*)" registers no MCP server named "([^"]*)"$`, func(c context.Context, rel, name string) error {
		return assertMCPServerNamed(worldFrom(c), rel, name, false)
	})
}

// assertSessionStartHookCommand parses rel as JSON and asserts (or refutes)
// that its hooks.SessionStart list carries a command containing want. A
// missing "hooks" table entirely — the shape a refused write leaves behind —
// is treated as zero hooks rather than a parse error, since "no hooks
// object" and "an empty one" both mean the same thing to this claim.
func assertSessionStartHookCommand(w *World, rel, want string, present bool) error {
	doc, err := j000400ReadJSON(w, rel)
	if err != nil {
		return err
	}
	top, _ := doc["hooks"].(map[string]any)
	var cmds []string
	if top != nil {
		cmds = j000400HookCommandsFrom(top["SessionStart"])
	}
	found := false
	for _, cmd := range cmds {
		if strings.Contains(cmd, want) {
			found = true
			break
		}
	}
	w.docStepMaterialized = fmt.Sprintf("%s → hooks.SessionStart\n  commands: %v", rel, cmds)
	if present && !found {
		return fmt.Errorf("%s's SessionStart hooks %v carry no command containing %q", rel, cmds, want)
	}
	if !present && found {
		return fmt.Errorf("%s's SessionStart hooks %v unexpectedly carry a command containing %q", rel, cmds, want)
	}
	return nil
}

// assertMCPServerNamed parses rel as JSON and asserts (or refutes) that its
// mcpServers table carries an entry keyed name with a non-empty command —
// the marker is the field INSIDE the server's body, so a registration with
// the right key and an empty command cannot pass as a working one.
func assertMCPServerNamed(w *World, rel, name string, present bool) error {
	doc, err := j000400ReadJSON(w, rel)
	if err != nil {
		return err
	}
	top, _ := doc["mcpServers"].(map[string]any)
	srv, ok := top[name].(map[string]any)
	if !ok {
		w.docStepMaterialized = fmt.Sprintf("%s → mcpServers: no %q entry", rel, name)
		if present {
			return fmt.Errorf("%s has no mcpServers.%s entry; parsed: %+v", rel, name, doc)
		}
		return nil
	}
	cmd, _ := srv["command"].(string)
	w.docStepMaterialized = fmt.Sprintf("%s → mcpServers.%s\n  command: %s", rel, name, cmd)
	if !present {
		return fmt.Errorf("%s unexpectedly still has a mcpServers.%s entry (command=%q); it should have been removed", rel, name, cmd)
	}
	if strings.TrimSpace(cmd) == "" {
		return fmt.Errorf("%s's mcpServers.%s entry has an empty command — a server with nothing to launch is not a real registration", rel, name)
	}
	return nil
}
