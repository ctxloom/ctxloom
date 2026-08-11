package claude

import (
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// This file is claude-code's ENGINE CLI CONTRACT (agent.EngineCLI): the single
// declaration of the `claude` process surfaces ctxloom spawns, read by the
// driver's anti-drift test today and by a stand-in vendor binary later.
//
// WHAT IS DECLARED HERE vs WHAT STAYS IN buildArgs
//
// Declared: which flags EXIST on each surface and what shape their value is,
// where the prompt travels, which env vars cross, and which files claude reads.
// Those are facts about the vendor CLI — stable, and equally true for the real
// binary and a fake.
//
// NOT declared, and deliberately so — these stay as code in buildArgs
// (claudecode.go), the one place that knows them:
//
//   - the permission-posture MAPPING (bypass → --dangerously-skip-permissions,
//     plan → --permission-mode plan + a fixed --disallowedTools deny list). A
//     declaration would have to carry a conditional expression to say it, which
//     is a worse version of the switch statement that already says it.
//   - EMISSION ORDER. claude's argv order is significant (the config
//     passthrough leads, the prompt positional trails) and is a property of the
//     line, not of any one flag.
//   - the GATES: SkipSetup, CellKind == CellKindShared, a non-empty delivered
//     surface path, a harp in the env.
//
// The test applied is "is there exactly one place that knows this fact", not
// "is it a struct literal".
//
// CLAUDE.md ONLY. claude does not read AGENTS.md — that file belongs to
// codex. The divergence is deliberate and is most of why this
// contract is worth having: a fake that reported reading an AGENTS.md the real
// binary ignores would go green on fiction.

// The claude path vocabulary. These constants are the SINGLE source of the
// filenames claude reads: both the writers (ProjectSettingsPath, MCPConfigPath,
// WriteContext, WriteCommandFiles, WriteSkillFiles, WriteAgentFiles) and the
// probe declarations below build their paths from them, so a rename cannot
// leave the declaration describing a file nothing writes.
const (
	// ContextFileName is claude's native instruction file, merged (not
	// overwritten) inside managed markers by agent.WriteManagedContext.
	ContextFileName = "CLAUDE.md"
	// MCPFileName is claude's project MCP server config.
	MCPFileName = ".mcp.json"
	// ConfigDirName is claude's per-project config directory.
	ConfigDirName = ".claude"
	// SettingsFileName is the settings file inside ConfigDirName; it carries
	// hooks, statusline, and permission denies (claude has no separate hooks
	// file).
	SettingsFileName = "settings.json"
	// CommandsDirName is the slash-command directory inside ConfigDirName.
	CommandsDirName = "commands"
	// SkillsDirName is the Agent Skill package directory inside ConfigDirName.
	SkillsDirName = "skills"
	// AgentsDirName is the sub-agent definition directory inside ConfigDirName.
	AgentsDirName = "agents"
)

// The claude ISOLATION vocabulary. Unlike the block above (facts the writers
// and probe declarations both build paths from), these three describe how
// claude relocates its home and where its OWN credential/transcript state
// lives — facts internal/lm/isolation's credentialSeedSpecs/containerProfile
// tables duplicate as literals today (isolation cannot import this package in
// production: claude -> internal/acp -> internal/lm/isolation is a real
// cycle), so tests/arch's engine-layout gate cross-checks those literals
// against these instead.
const (
	// ConfigDirEnv is the environment variable claude honors to relocate its
	// config home away from the default ~/.claude — CLAUDE_CONFIG_DIR.
	// ctxloom's per-agent isolation points this at a private home per run so
	// settings/commands/skills and credentials resolve from an isolated
	// location instead of the shared one.
	ConfigDirEnv = "CLAUDE_CONFIG_DIR"
	// CredentialsFileName is the OAuth/API credential file claude reads and
	// refreshes inside its config home (ConfigDirName, or a
	// CLAUDE_CONFIG_DIR-relocated equivalent) — seeded per-agent by
	// internal/lm/isolation's credentialSeedSpecs.
	CredentialsFileName = ".credentials.json"
	// TranscriptsDirName is the subdirectory of ConfigDirName claude stores
	// its native per-project session transcripts under (~/.claude/projects).
	TranscriptsDirName = "projects"
)

// relSettings, relCommands, relSkills, relAgents are the ConfigDirName-relative
// paths, shared by the writers and the probe declarations.
var (
	relSettings = filepath.Join(ConfigDirName, SettingsFileName)
	relCommands = filepath.Join(ConfigDirName, CommandsDirName)
	relSkills   = filepath.Join(ConfigDirName, SkillsDirName)
	relAgents   = filepath.Join(ConfigDirName, AgentsDirName)
)

// Flag names claude's driver emits. Named constants rather than literals so
// buildArgs and this declaration cannot disagree about spelling — a typo'd
// literal in buildArgs would otherwise read as an undeclared flag only if the
// typo happened to be caught, and as a silently wrong CLI invocation if not.
const (
	flagSkipPermissions  = "--dangerously-skip-permissions"
	flagPermissionMode   = "--permission-mode"
	flagDisallowedTools  = "--disallowedTools"
	flagModel            = "--model"
	flagName             = "--name"
	flagPrint            = "--print"
	flagAppendSystemFile = "--append-system-prompt-file"
	flagMCPConfig        = "--mcp-config"
	flagSettings         = "--settings"
	flagOutputFormat     = "--output-format"
	flagTools            = "--tools"
	flagNoSlashCommands  = "--disable-slash-commands"
	flagNoSessionPersist = "--no-session-persistence"
	flagStrictMCPConfig  = "--strict-mcp-config"
	flagSystemPrompt     = "--system-prompt"
)

// commonFlags are the flags claude's driver can emit on BOTH surfaces. The
// SkipSetup (minimal/distill) set is here rather than on oneshot alone because
// buildArgs applies SkipSetup without consulting the mode — an interactive
// SkipSetup run emits them too.
func commonFlags() []agent.CLIFlag {
	return []agent.CLIFlag{
		{Name: flagSkipPermissions, Value: agent.ValueNone,
			Note: "PermissionBypass; the blanket skip"},
		{Name: flagPermissionMode, Value: agent.ValueString,
			Note: "acceptEdits | plan"},
		{Name: flagDisallowedTools, Value: agent.ValueString,
			Note: "ONE comma-joined value token (Bash,Edit,Write,NotebookEdit), not repeated flags; plan posture only"},
		{Name: flagModel, Value: agent.ValueString},
		{Name: flagAppendSystemFile, Value: agent.ValuePath,
			Note: "the framed out-of-cwd sysprompt scratch; SharedCell delivery only"},
		{Name: flagMCPConfig, Value: agent.ValuePath,
			Note: "layers over the project .mcp.json unless --strict-mcp-config is also present"},
		{Name: flagSettings, Value: agent.ValuePathOrJSON,
			Note: "a FILE PATH on the normal delivery path, a LITERAL inline JSON object under SkipSetup (minimalSettings); mutually exclusive within one argv"},
		{Name: flagOutputFormat, Value: agent.ValueString,
			Note: "SkipSetup only: json, so Execute can read the resolved model id"},
		{Name: flagTools, Value: agent.ValueString,
			Note: `SkipSetup only; the value is an EMPTY STRING passed as its own argv token`},
		{Name: flagNoSlashCommands, Value: agent.ValueNone, Note: "SkipSetup only"},
		{Name: flagNoSessionPersist, Value: agent.ValueNone, Note: "SkipSetup only"},
		{Name: flagStrictMCPConfig, Value: agent.ValueNone, Note: "SkipSetup only"},
		{Name: flagSystemPrompt, Value: agent.ValueString,
			Note: `SkipSetup only; the value is an EMPTY STRING passed as its own argv token, dropping CLAUDE.md/memory`},
	}
}

// probes is claude's ordered context-surface list. Within a kind the first
// entry wins: the out-of-cwd launch flag a SharedCell delivery points at takes
// precedence over the well-known file, because that is exactly why the flag
// exists — it keeps ctxloom out of the user's live cwd.
func probes() []agent.CLIProbe {
	return []agent.CLIProbe{
		{Kind: agent.ProbeKindContext, Scope: agent.ScopeFlagValue, Flag: flagAppendSystemFile,
			Note: "SharedCell: the framed context rides this flag instead of CLAUDE.md"},
		{Kind: agent.ProbeKindContext, Scope: agent.ScopeCwd, Rel: ContextFileName,
			Note: "isolated cells: MANAGED-MARKER MERGE (agent.WriteManagedContext), never a whole-file overwrite; claude does NOT read AGENTS.md"},
		{Kind: agent.ProbeKindMCP, Scope: agent.ScopeFlagValue, Flag: flagMCPConfig,
			Note: "layers on top of the cwd .mcp.json rather than replacing it, unless --strict-mcp-config"},
		{Kind: agent.ProbeKindMCP, Scope: agent.ScopeCwd, Rel: MCPFileName},
		{Kind: agent.ProbeKindSettings, Scope: agent.ScopeFlagValue, Flag: flagSettings,
			Note: "value may be an inline JSON object rather than a path (SkipSetup)"},
		{Kind: agent.ProbeKindSettings, Scope: agent.ScopeCwd, Rel: relSettings,
			Note: "claude folds hooks + statusline + permission denies into this one file"},
		{Kind: agent.ProbeKindCommands, Scope: agent.ScopeCwd, Rel: relCommands, Dir: true,
			Note: "no out-of-cwd flag exists for slash-commands, so a SharedCell delivery falls back to the loud native write"},
		{Kind: agent.ProbeKindSkills, Scope: agent.ScopeCwd, Rel: relSkills, Dir: true},
		{Kind: agent.ProbeKindAgents, Scope: agent.ScopeCwd, Rel: relAgents, Dir: true,
			Note: "claude reads sub-agent definitions here; ctxloom's writer (WriteAgentFiles) is a SEPARATE, not-yet-wired path — no delivery surface targets it, so a probe here reports absent today"},
	}
}

// setEnv is the env ctxloom puts on the child for both CLI surfaces.
// CTXLOOM_CONTEXT_FILE is set by LaunchBackend.ExecuteEnv whenever context was
// provided; CTXLOOM_SESSION_HARP arrives on the run env from the host and is
// what gates --name. claude's CLI surfaces STRIP NOTHING — the CLAUDECODE
// nested-session guard is stripped only on the ACP transport (internal/acp),
// which this contract does not cover.
func setEnv() []string {
	return []string{agent.SCMContextFileEnv, agent.SessionHarpEnv}
}

// EngineCLIs declares claude's oneshot and interactive process surfaces.
//
// The two differ in exactly three ways, all of them load-bearing:
//
//	--print       oneshot only
//	--name <harp> interactive only (gated on CTXLOOM_SESSION_HARP)
//	the prompt    oneshot pipes it on STDIN (argv delivery hit E2BIG on
//	              `ctxloom weave` synthesis); interactive passes it as the
//	              trailing argv positional.
func (b *ClaudeCode) EngineCLIs() []agent.EngineCLI {
	return ClaudeEngineCLIs()
}

// ClaudeEngineCLIs returns claude's surface declarations. Exposed as a function
// (not a package var) so no consumer can mutate the shared declaration.
func ClaudeEngineCLIs() []agent.EngineCLI {
	oneshot := agent.EngineCLI{
		Engine:  "claude-code",
		Surface: agent.CLISurfaceOneshot,
		Binary:  "claude",
		Prompt:  agent.PromptStdin,
		Flags: append(commonFlags(),
			agent.CLIFlag{Name: flagPrint, Value: agent.ValueNone, Required: true,
				Note: "oneshot only, and REQUIRED: `claude --print` IS the oneshot surface. A line without it is an interactive launch that reads the piped prompt as terminal input and hangs on the handshake, so the grammar rejects it rather than letting a stand-in report a green run for a launch that could not start"},
		),
		SetEnv: setEnv(),
		Probes: probes(),
	}
	interactive := agent.EngineCLI{
		Engine:  "claude-code",
		Surface: agent.CLISurfaceInteractive,
		Binary:  "claude",
		Prompt:  agent.PromptPositional,
		Flags: append(commonFlags(),
			agent.CLIFlag{Name: flagName, Value: agent.ValueString,
				Note: "interactive only; names the session after ctxloom's harp (claude's /rename cannot be injected)"},
		),
		SetEnv: setEnv(),
		Probes: probes(),
	}
	return []agent.EngineCLI{oneshot, interactive}
}

// Compile-time contract: claude declares its native CLI surfaces.
var _ agent.EngineCLIProvider = (*ClaudeCode)(nil)
