package codex

import (
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// This file is codex's ENGINE CLI CONTRACT (agent.EngineCLI): the single
// declaration of the `codex` process surfaces ctxloom spawns, read by the
// driver's anti-drift test today and by a stand-in vendor binary later.
//
// It is the SECOND backend to adopt the contract, and that is the point of it:
// a contract validated against one engine describes that engine, not the
// family. codex diverges from claude on four axes that the declaration must
// express WITHOUT being bent:
//
//  1. SUBCOMMAND. codex's oneshot is `codex exec ...`, a distinct subcommand
//     with its OWN flag set; claude has no subcommand on either surface. The
//     Subcommand field carries it, and ParseArgv requires it at argv[0] —
//     matching buildArgs, which appends "exec" before b.Args.
//  2. THE PROMPT IS A POSITIONAL ON BOTH SURFACES. claude's oneshot pipes the
//     task on stdin (argv delivery hit E2BIG on `ctxloom weave` synthesis);
//     codex takes it on argv both times. A fake that assumed claude's shape
//     would report "no prompt received" from a codex run that delivered one.
//  3. NO OUT-OF-CWD REDIRECT. codex has no --mcp-config/--settings/
//     --append-system-prompt-file equivalent, so NOTHING here is a
//     ScopeFlagValue probe (surfaces.go's capability recap: every codex surface
//     is well-known-only). That absence is why concurrent per-agent isolation
//     for codex needs a private cwd or a container, and why this declaration's
//     probes are all cwd- or CODEX_HOME-rooted.
//  4. AGENTS.md, NEVER CLAUDE.md. codex reads the workspace-fixed AGENTS.md
//     natively at session start; claude reads CLAUDE.md and ignores AGENTS.md.
//     The two declarations disagreeing here is the contract earning its keep.
//
// WHAT STAYS IN buildArgs, deliberately: the SANDBOX TIER TABLE (plan/SkipSetup
// → read-only, default/acceptEdits → workspace-write, bypass → the full-access
// escape hatch and NO --sandbox at all), the emission ORDER, and the gates
// (interactive-only --ask-for-approval, non-empty model). Those are conditional
// logic; a declaration able to state them would be a worse switch statement.
// The declaration answers "does this flag exist on this surface, and does it
// take a value"; buildArgs answers "is it emitted right now".
//
// CODEX'S CONTEXT IS HOOK-MEDIATED — the one place the model had to stretch.
// See the ProbeKindContext entries in codexProbes() for the full account.

// The codex path vocabulary. These constants are the SINGLE source of the
// names codex reads, shared by the writers (cellScopedCodexHome,
// cellScopedPromptsDir, cellScopedSkillsDir, CodexHookWriter.SettingsPath,
// codexHome, MCPRegistrar.ConfigPath) and the probe declarations below, so a
// rename cannot leave the declaration describing a path nothing writes.
const (
	// CodexHomeEnv is the environment variable naming codex's home directory.
	// It IS the .codex dir, not its parent (codexHome), and ctxloom sets it per
	// run so prompts/skills/config.toml are cell-scoped (cellCodexHomeEnv).
	CodexHomeEnv = "CODEX_HOME"
	// ConfigDirName is the directory a project-scoped CODEX_HOME points at.
	ConfigDirName = ".codex"
	// ConfigFileName is codex's single config file inside CODEX_HOME. It folds
	// the [hooks] AND [mcp_servers] tables into one document.
	ConfigFileName = "config.toml"
	// PromptsDirName is codex's custom-prompt (slash command) directory inside
	// CODEX_HOME. codex has NO project-level prompts dir — see codexPromptsDir.
	PromptsDirName = "prompts"
	// SkillsDirName is codex's Agent Skill package directory inside CODEX_HOME,
	// global for exactly the same reason as PromptsDirName.
	SkillsDirName = "skills"
	// SessionsDirName is the subdirectory of CODEX_HOME codex accumulates its
	// own native session transcripts under ($CODEX_HOME/sessions) — the
	// container-axis transcript-store root internal/lm/isolation/enginespec.go's
	// codex engineContainerSpec mounts onto.
	SessionsDirName = "sessions"
)

// The codex argv vocabulary. Named constants rather than literals so buildArgs
// and this declaration cannot disagree about spelling. This is NOT ceremony:
// internal/acp/argv.go's equivalent test passes by COINCIDENCE because its
// chatArgv still emits string literals, so a typo there would read as a
// perfectly declared grammar describing a flag the driver never sends.
const (
	// subcommandExec is codex's non-interactive subcommand. Its flag set is not
	// the interactive one's, and an unknown flag is a hard exit-2 "unexpected
	// argument", never a warning.
	subcommandExec = "exec"

	flagModel                     = "--model"
	flagSandbox                   = "--sandbox"
	flagBypassApprovalsAndSandbox = "--dangerously-bypass-approvals-and-sandbox"
	flagAskForApproval            = "--ask-for-approval"
)

// codexCommonFlags are the flags codex's driver can emit on BOTH surfaces.
// --ask-for-approval is deliberately NOT here: `codex exec` rejects it outright
// (see codexEngineCLIs).
func codexCommonFlags() []agent.CLIFlag {
	return []agent.CLIFlag{
		{Name: flagModel, Value: agent.ValueString,
			Note: "empty req.Model omits the flag entirely so codex resolves its own account-scoped default; codex also accepts -m, which ctxloom never emits"},
		{Name: flagSandbox, Value: agent.ValueString,
			Enum: []string{"read-only", "workspace-write", "danger-full-access"},
			Note: "read-only | workspace-write | danger-full-access; EVERY posture names its tier rather than inheriting codex's default, so an upstream default change cannot silently relax ctxloom's stated posture. Absent under bypass, which carries the escape-hatch flag instead"},
		{Name: flagBypassApprovalsAndSandbox, Value: agent.ValueNone,
			ConflictsWith: []string{flagSandbox},
			Note:          "PermissionBypass; codex's own full-access escape hatch, the peer of claude's --dangerously-skip-permissions. Mutually exclusive with --sandbox within one argv"},
	}
}

// codexProbes is codex's ordered context-surface list.
//
// THE CONTEXT KIND CARRIES TWO ENTRIES, and they are ADDITIVE rather than
// first-wins — codex reads BOTH on the same run (surfaces.go composes them into
// one agent.ComposedDelivery). The ordering here is the order the READ becomes
// visible, not a precedence rule:
//
//  1. AGENTS.md — read NATIVELY at session start, directly, with no
//     intermediary. This is the entry a probe can verify the ordinary way.
//  2. the hook-mediated context cache — the entry that does NOT fit CLIProbe's
//     model, declared as a DIRECTORY probe with a note rather than by
//     extending the contract.
//
// WHY THE SECOND ONE IS A DIRECTORY WITH A NOTE. CLIProbe assumes the vendor
// reads the declared path itself. codex does not. Its run-path context is a
// per-run CONTENT-HASH file at <cwd>/.ctxloom/cache/context/<hash>.md
// (agent.WriteContextFile, wired by contextSurface.Deliver) which codex NEVER
// opens. What codex reads is config.toml, whose [hooks] SessionStart command
// has that hash baked into its argv; the hook prints the file and codex ingests
// the hook's OUTPUT. Two facts break the file-probe model: the FILENAME IS
// PER-RUN (there is no fixed Rel to declare), and the READ IS INDIRECT (the
// mediator is the settings surface, not this one).
//
// A directory probe answers both without a new field: the DIRECTORY is fixed
// even though its contents are not, so a consumer can report "the run-path
// context landed / did not land" — which is the observation worth having, this
// project's silent-no-op failure mode being precisely "exit 0, zero bytes
// delivered". A `Via ProbeKind` field naming config.toml as the mediator was
// considered and REJECTED as speculative generality: it would have exactly one
// user, and the note states the same fact for the only consumer that exists.
func codexProbes() []agent.CLIProbe {
	return []agent.CLIProbe{
		{Kind: agent.ProbeKindContext, Scope: agent.ScopeCwd, Rel: AgentsMDFile,
			Note: "read NATIVELY at session start; MANAGED-MARKER MERGE (agent.WriteManagedContext via CodexHookWriter.WriteContext), never a whole-file overwrite. codex's file, NOT claude's CLAUDE.md"},
		{Kind: agent.ProbeKindContext, Scope: agent.ScopeCwd, Rel: agent.SCMContextSubdir, Dir: true,
			Note: "HOOK-MEDIATED, and additive to AGENTS.md rather than overridden by it: the per-run <hash>.md in this directory is read by the config.toml SessionStart command, NOT by codex itself, so there is no fixed filename to declare and the mediator is the settings surface"},
		{Kind: agent.ProbeKindSettings, Scope: agent.ScopeEnvDir, EnvVar: CodexHomeEnv, EnvHomeDefault: ConfigDirName, Rel: ConfigFileName,
			Note: "codex FOLDS hooks and MCP servers into this one file ([hooks] + [mcp_servers]), so ProbesFor(ProbeKindMCP) is legitimately EMPTY — there is no second file to declare. Lives in CODEX_HOME, not the cwd: ctxloom points CODEX_HOME at <dir>/.codex per run (cellCodexHomeEnv), which is why the same probe covers the in-tree, worktree and container axes"},
		{Kind: agent.ProbeKindCommands, Scope: agent.ScopeEnvDir, EnvVar: CodexHomeEnv, EnvHomeDefault: ConfigDirName, Rel: PromptsDirName, Dir: true,
			Note: "codex discovers prompts ONLY from $CODEX_HOME/prompts — there is no project-level prompts dir, so an isolated *directory* would not isolate them without the per-run CODEX_HOME"},
		{Kind: agent.ProbeKindSkills, Scope: agent.ScopeEnvDir, EnvVar: CodexHomeEnv, EnvHomeDefault: ConfigDirName, Rel: SkillsDirName, Dir: true,
			Note: "global for the same reason as prompts; packages are <name>/SKILL.md"},
	}
}

// codexSetEnv is the env ctxloom puts on the child for both CLI surfaces.
//
// CODEX_HOME is the one that makes codex structurally different from every
// sibling backend: codex alone registers a SetExecuteEnv contributor
// (cellCodexHomeEnv), because its config/prompts/skills are CODEX_HOME-relative
// rather than cwd-relative. SkipSetup deliberately omits it (a minimal/distill
// run delivers no surfaces and keeps codex's global home) — a CONDITION, so it
// stays in cellCodexHomeEnv rather than in this list.
//
// CTXLOOM_CONTEXT_FILE arrives from the shared LaunchBackend.ExecuteEnv
// whenever context was provided; it is what the SessionStart hook's peer path
// reads. CTXLOOM_SESSION_HARP rides the run env from the host for every backend
// — codex CONSUMES NOTHING FROM IT, having no session-naming lever of its own
// (claude's --name has no codex peer). Declared anyway, because "set on the
// child and ignored by the engine" is a fact a fake must not invent the
// opposite of.
//
// codex's CLI surfaces STRIP NOTHING.
func codexSetEnv() []string {
	return []string{CodexHomeEnv, agent.SCMContextFileEnv, agent.SessionHarpEnv}
}

// EngineCLIs declares codex's oneshot and interactive process surfaces.
func (b *Codex) EngineCLIs() []agent.EngineCLI { return CodexEngineCLIs() }

// CodexEngineCLIs returns codex's surface declarations. Exposed as a function
// (not a package var) so no consumer can mutate the shared declaration.
//
// The two surfaces differ in exactly two ways, both load-bearing:
//
//	the `exec` subcommand   oneshot only
//	--ask-for-approval      INTERACTIVE only — `codex exec` REJECTS it with a
//	                        hard exit-2 "unexpected argument", because a
//	                        non-interactive run has nobody to ask. Declaring it
//	                        on one surface is what makes ParseArgv reproduce
//	                        codex's real rejection instead of a fake accepting
//	                        a line the vendor would refuse to start on.
func CodexEngineCLIs() []agent.EngineCLI {
	oneshot := agent.EngineCLI{
		Engine:     "codex",
		Surface:    agent.CLISurfaceOneshot,
		Binary:     "codex",
		Subcommand: subcommandExec,
		Prompt:     agent.PromptPositional,
		Flags:      codexCommonFlags(),
		SetEnv:     codexSetEnv(),
		Probes:     codexProbes(),
	}
	interactive := agent.EngineCLI{
		Engine:  "codex",
		Surface: agent.CLISurfaceInteractive,
		Binary:  "codex",
		Prompt:  agent.PromptPositional,
		Flags: append(codexCommonFlags(),
			agent.CLIFlag{Name: flagAskForApproval, Value: agent.ValueString,
				Note: "INTERACTIVE only; `codex exec` rejects it with a hard exit-2 'unexpected argument' (a non-interactive run has nobody to ask). ctxloom emits it as `never` for the read-only postures (plan/SkipSetup)"},
		),
		SetEnv: codexSetEnv(),
		Probes: codexProbes(),
	}
	return []agent.EngineCLI{oneshot, interactive}
}

// Compile-time contract: codex declares its native CLI surfaces.
var _ agent.EngineCLIProvider = (*Codex)(nil)
