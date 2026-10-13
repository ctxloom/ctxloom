package opencode

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/kit"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// This file is opencode's DELIVERY: one typed approach per kind, each landing
// in the session home H and announcing H on OPENCODE_CONFIG_DIR — the config
// dir opencode reads last of all its dirs, so it wins over the user's and the
// project's. Only the file names and the config dialect are opencode's; the
// rooting, the framed file, the MCP table and the managed trees are kit's.

const (
	// ContextFileName is the instruction file opencode reads from its config
	// dir as the "global" rules, added to (never replacing) its own prompt.
	ContextFileName = "AGENTS.md"
	// ConfigFileName is opencode's config file in its config dir.
	ConfigFileName = "opencode.json"
	// CommandsDirName and SkillsDirName are the trees opencode scans in
	// every config dir.
	CommandsDirName = "commands"
	SkillsDirName   = "skills"
	// configSchemaURL is claimed at /$schema: opencode rewrites, in place, a
	// config file it loads that lacks one, which would fight the ownership
	// record over ctxloom's own file.
	configSchemaURL = "https://opencode.ai/config.json"
	// bashTimeoutEnv is opencode's default shell-tool timeout, in ms. It has
	// no maximum: ShellTimeout.Max is a declared loss.
	bashTimeoutEnv = "OPENCODE_EXPERIMENTAL_BASH_DEFAULT_TIMEOUT_MS"
)

// announce names H, the root every approach writes under, on
// OPENCODE_CONFIG_DIR.
func announce(r present.Rooted) present.Rooted { return r.AnnounceEnv(envConfigDir) }

// contextApproach is the framed context as H/AGENTS.md, a whole file the
// approach owns. Empty context writes nothing; OPENCODE_DISABLE_CLAUDE_CODE
// (Exec) keeps opencode from falling back to the user's ~/.claude/CLAUDE.md.
// A named context file (ContextInputs.File) is refused: opencode reads its
// context only as AGENTS.md in its config dir, under no other name.
type contextApproach struct{ kit.Approach }

var agentsFile = kit.FramedFile{Name: kit.FixedName(ContextFileName), Announce: announce}

func (a *contextApproach) DeliverContext(start present.Start, root present.RootKind, in engine.ContextInputs, fs afero.Fs) (present.Delivered, error) {
	if root != present.RootSessionHome {
		return present.Delivered{}, a.ErrRoot(root)
	}
	if in.File != "" {
		return present.Delivered{}, fmt.Errorf("%w: %s at %v reads only %s", engine.ErrContextFileUnsupported, a.Name(), root, ContextFileName)
	}
	return agentsFile.Deliver(a.Approach, start, in.Text, fs)
}

// mcpApproach claims each server's entry under /mcp in H/opencode.json.
type mcpApproach struct{ kit.Approach }

// mcpTable is opencode's MCP table: one entry per server in opencode's own
// dialect (mcpEntry). No empty table is claimed: opencode starts without one.
var mcpTable = kit.MCPTable{Key: "mcp", Entry: mcpEntry}

func (a *mcpApproach) DeliverMCP(start present.Start, root present.RootKind, in engine.MCPInputs, _ afero.Fs) (present.Delivered, error) {
	return deliverConfig(a.Approach, start, root, nil, func() ([]present.Claim, error) { return mcpTable.Claims(in.Servers) })
}

// mcpEntry is one server as opencode.json spells it: a remote server by URL
// with its headers (the session endpoint's bearer among them) and OAuth
// detection off, a local one as its argv and environment. Both are enabled.
func mcpEntry(name string, s wire.MCPServer) (any, error) {
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("mcp server %q: %w", name, err)
	}
	if s.IsSessionEndpoint() {
		return nil, fmt.Errorf("mcp server %q: %w", name, wire.ErrMCPServerUnrendered)
	}
	if s.IsRemote() {
		e := map[string]any{"type": "remote", "url": s.URL, "enabled": true, "oauth": false}
		if len(s.Headers) > 0 {
			e["headers"] = s.Headers
		}
		return e, nil
	}
	e := map[string]any{"type": "local", "command": append([]string{s.Command}, s.Args...), "enabled": true}
	if len(s.Env) > 0 {
		e["environment"] = s.Env
	}
	return e, nil
}

// settingsApproach claims each denied tool as /permission/<tool> = deny in
// H/opencode.json, and carries the shell timeout's default on the env
// channel. The posture itself rides OPENCODE_PERMISSION per turn, applied
// after every config file. Statusline has no opencode form: a declared loss.
type settingsApproach struct{ kit.Approach }

func (a *settingsApproach) DeliverSettings(start present.Start, root present.RootKind, in engine.SettingsInputs, _ afero.Fs) (present.Delivered, error) {
	var env map[string]string
	if in.ShellTimeout != (engine.ShellTimeout{}) {
		env = map[string]string{bashTimeoutEnv: strconv.FormatInt(in.ShellTimeout.Default.Milliseconds(), 10)}
	}
	return deliverConfig(a.Approach, start, root, env, func() ([]present.Claim, error) {
		var claims []present.Claim
		seen := map[string]bool{}
		for _, tool := range in.DenyTools {
			if tool == "" || seen[tool] {
				continue
			}
			seen[tool] = true
			claims = append(claims, present.Claim{Pointer: present.PointerKey("permission") + present.PointerKey(tool), Value: "deny"})
		}
		return claims, nil
	})
}

// deliverConfig claims into H/opencode.json — the MCP and settings kinds
// both claim there, and the static writer folds their claims into one write
// — beside the /$schema both claim with the one value, announced, with env
// on the presentation's env channel. With nothing to claim the file is not
// conjured.
func deliverConfig(a kit.Approach, start present.Start, root present.RootKind, env map[string]string, claims func() ([]present.Claim, error)) (present.Delivered, error) {
	r, err := a.Rooted(start, root, ConfigFileName, "")
	if err != nil {
		return present.Delivered{}, err
	}
	p := announce(r).Build()
	for k, v := range env {
		p.Env[k] = v
	}
	cs, err := claims()
	if err != nil || len(cs) == 0 {
		return present.Delivered{Presented: p}, err
	}
	cs = append([]present.Claim{{Pointer: present.PointerKey("$schema"), Value: configSchemaURL}}, cs...)
	return present.Delivered{Presented: p, Claims: map[string][]present.Claim{p.HostPath: cs}}, nil
}

// commandsApproach writes each enabled command as H/commands/<name>.md.
type commandsApproach struct{ kit.Approach }

func (a *commandsApproach) DeliverCommands(start present.Start, root present.RootKind, in engine.CommandsInputs, files safefs.Root) (present.Delivered, error) {
	return kit.DeliverCommands(a.Approach, start, root, CommandsDirName, "", files, in, renderCommand, announce)
}

// renderCommand is opencode's command file: frontmatter with a description
// (the name when the command carries none) and the model when set, then the
// body with {{var}} placeholders turned into the $1.. opencode substitutes.
// An empty body is refused: opencode would load a command that does nothing.
// Argument hints and tool restrictions have no opencode form.
func renderCommand(c agent.CommandExport) (string, []byte, error) {
	if strings.TrimSpace(c.Content) == "" {
		return "", nil, fmt.Errorf("%s: command %q has an empty body", EngineName, c.Name)
	}
	description := c.Description
	if description == "" {
		description = c.Name
	}
	var b strings.Builder
	b.WriteString("---\ndescription: " + agent.EscapeYAMLString(description) + "\n")
	if c.Model != "" {
		b.WriteString("model: " + agent.EscapeYAMLString(c.Model) + "\n")
	}
	b.WriteString("---\n\n")
	b.WriteString(agent.TransformMustacheToPositional(c.Content))
	return strings.ReplaceAll(c.Name, "/", "-") + ".md", []byte(b.String()), nil
}

// skillsApproach writes each enabled skill package opencode will load under
// H/skills/<name>/. opencode itself applies no name rule and silently skips
// a skill it cannot parse; the Agent Skills standard (no reserved words)
// turns that silent skip into ctxloom's loud refusal.
type skillsApproach struct{ kit.Approach }

var acceptedSkills = kit.AgentSkillRules(EngineName)

func (a *skillsApproach) DeliverSkills(start present.Start, root present.RootKind, in engine.SkillsInputs, files safefs.Root) (present.Delivered, error) {
	return kit.DeliverSkills(a.Approach, start, root, SkillsDirName, "", files, in, acceptedSkills, announce)
}

// sessionEndpoint is the dynamic approach opencode provides: the session's
// MCP endpoint as a remote entry carrying its bearer (engine.BearerEntry).
type sessionEndpoint struct{ kit.Approach }

func (*sessionEndpoint) Endpoint(ep sessions.Endpoint) wire.MCPServer { return engine.BearerEntry(ep) }

// Compile-time contracts: each typed approach fills its kind's field.
var (
	_ engine.ContextApproach  = (*contextApproach)(nil)
	_ engine.MCPApproach      = (*mcpApproach)(nil)
	_ engine.SettingsApproach = (*settingsApproach)(nil)
	_ engine.CommandsApproach = (*commandsApproach)(nil)
	_ engine.SkillsApproach   = (*skillsApproach)(nil)
	_ engine.DynamicApproach  = (*sessionEndpoint)(nil)
)
