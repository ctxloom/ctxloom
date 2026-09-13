// Package claudeengine is claude-code's engine DESCRIPTOR: the one record the
// engine authors about itself, for the backend registry to install. It is a
// subpackage rather than part of internal/claude because the descriptor's
// export slots are typed on the bundle model, and the lean parent package is
// linked by the ltk and taskloom binaries, which must not carry that model.
//
// Nothing outside this package and internal/claude names this engine; a fact
// about claude that some other package needs is declared here and read back
// through the registry.
package claudeengine

import (
	"path/filepath"
	"runtime"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/engineversion"
	"github.com/ctxloom/ctxloom/internal/lm/engine"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	claudereader "github.com/ctxloom/ctxloom/internal/transcript/vendorreader/claude"
)

// Descriptor returns claude-code's complete declaration. Every fact is built
// from the engine's own constants, so there is no second copy to drift.
func Descriptor() engine.Descriptor {
	credentialRelHome := filepath.ToSlash(filepath.Join(claude.ConfigDirName, claude.CredentialsFileName))
	return engine.Descriptor{
		Name:         claude.EngineName,
		Aliases:      claude.EngineAliases(),
		Distribution: agent.DistributionDefault,
		NewBackend: func(launch agent.Launcher) agent.Backend {
			b := claude.NewClaudeCode()
			b.SetLauncher(launch)
			return b
		},
		NewConfig:           func() agent.BackendConfig { return &claude.ClaudeConfig{} },
		Surfaces:            claude.Surfaces,
		SettingsWriter:      agent.Provide(claude.NewWriter),
		InstanceConfig:      agent.Provide(claude.NewInstanceConfigWriter),
		CredentialProjector: agent.Provide(claude.NewCredentialProjector),
		CommandExports:      agent.Provide(CommandExports),
		SkillExports:        agent.Provide(SkillExports),
		// claude's project settings.json collapses onto its user-global one
		// exactly when workDir == $HOME — found live (`manage hooks install`
		// run from $HOME silently went global).
		HookGlobalScope: agent.Provide(engine.HookGlobalScope{
			Paths: func(workDir string) (string, string, error) {
				global, err := claude.GlobalSettingsPath()
				return claude.ProjectSettingsPath(workDir), global, err
			},
			Label: "Claude Code's user-global settings file",
		}),
		VersionCommand: agent.Provide(engineversion.Command{Args: []string{"--version"}, Parse: parseVersion}),
		// CLAUDE_CONFIG_DIR relocates config AND credentials, so an isolated
		// home is seeded with .credentials.json. ~/.claude.json is NOT seeded:
		// on a real host it is claude's whole top-level config including the
		// user's own mcpServers registrations (and whatever secrets they
		// carry) — a confidentiality leak for mere onboarding convenience —
		// and claude auto-creates its own inside CLAUDE_CONFIG_DIR when none
		// exists there, so .credentials.json alone authenticates.
		Home: agent.Provide(agent.EngineHome{
			Vars: []agent.HomeVar{{EnvVar: claude.ConfigDirEnv, Subdir: claude.HomeLeaf}},
			Credentials: agent.Provide(agent.CredentialSeed{
				Subdir:     claude.HomeLeaf,
				EnvTrigger: "ANTHROPIC_API_KEY",
				LoginHint:  "claude login",
				Files:      []agent.SeedFile{{HostRelHome: credentialRelHome, DestName: claude.CredentialsFileName, Required: true}},
			}),
		}),
		Container: agent.Provide(agent.EngineContainer{
			// No official image: ghcr.io/anthropics/claude-code appears in
			// docs but does not resolve publicly, so the composed install
			// fragment (which fetches the most recent claude) is the build
			// source.
			Install:         installFragment,
			ValidateCommand: "claude --version",
			Auth: agent.Provide(agent.ContainerAuth{
				// ANTHROPIC_AUTH_TOKEN is a trigger too: a gateway host
				// authenticates with AUTH_TOKEN+BASE_URL and carries no API key.
				EnvTriggers: []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"},
				EnvPassthrough: []string{
					"ANTHROPIC_API_KEY",
					"ANTHROPIC_AUTH_TOKEN",
					"ANTHROPIC_BASE_URL",
					"ANTHROPIC_MODEL",
					"ANTHROPIC_SMALL_FAST_MODEL",
				},
				// READ-WRITE, and the REAL host file: claude's OAuth refresh
				// token is single-use and rotating, so any COPY that refreshes
				// invalidates the host's own login. Mounting the one real file
				// keeps host and container on the same rotating token.
				CredentialFiles: []agent.CredentialFile{{HostRelHome: credentialRelHome, ContainerRelHome: credentialRelHome}},
				Hint:            containerAuthHint(),
			}),
			OverlayDirs:        []string{claude.ConfigDirName},
			TranscriptStoreRel: filepath.Join(claude.ConfigDirName, claude.TranscriptsDirName),
		}),
		TranscriptReaders:    agent.Provide(claudereader.VersionedAdapters),
		EnforcesReadOnlyPlan: true, // --permission-mode plan is read-only
		// The ~/.claude/projects/*.jsonl scraper was proven broken (wrong
		// filename) and deleted outright rather than demoted; canonical
		// capture, read back through TranscriptReaders, is the only source.
		NoLegacyHistoryReason: "claude's legacy session scraper was deleted; canonical capture is the only transcript source",
	}
}

// containerAuthHint is platform-aware because the fallback credential path
// differs by OS. On darwin, a subscription login keeps its OAuth token in the
// macOS Keychain, NOT ~/.claude/.credentials.json — naming that file there is
// unfollowable advice, so the darwin hint names the env var instead.
func containerAuthHint() string {
	if runtime.GOOS == "darwin" {
		return "no ANTHROPIC_API_KEY/ANTHROPIC_AUTH_TOKEN to authenticate the in-container engine (a macOS Keychain-held subscription login cannot be mounted — set ANTHROPIC_API_KEY for a containerized run on Mac)"
	}
	return "no ANTHROPIC_API_KEY/ANTHROPIC_AUTH_TOKEN and no ~/.claude credentials to authenticate the in-container engine"
}

// parseVersion reads `claude --version`. MEASURED: "2.1.225 (Claude Code)" —
// the version leads, the product name follows in parentheses.
func parseVersion(output string) (string, error) {
	return engineversion.TokenAt(output, 0)
}

// CommandExports resolves the per-prompt claude-code export config.
func CommandExports(prompts []*bundles.LoadedContent) []agent.CommandExport {
	return engine.BuildCommandExports(prompts, func(p *bundles.LoadedContent) agent.CommandExport {
		cc := p.LLM.ClaudeCode
		return agent.CommandExport{
			Enabled:      cc.IsEnabled(),
			Description:  cc.Description,
			ArgumentHint: cc.ArgumentHint,
			AllowedTools: cc.AllowedTools,
			Model:        cc.Model,
		}
	})
}

// SkillExports resolves claude-code's per-skill enablement.
func SkillExports(skills []*bundles.LoadedSkill) []agent.SkillExport {
	return engine.BuildSkillExports(skills, func(s *bundles.LoadedSkill) bool { return s.LLM.ClaudeCode.IsEnabled() })
}
