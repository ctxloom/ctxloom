// Package claudeengine is claude-code's HOSTING record: what the backend
// registry still needs to run the engine kind claude.Build declares. It is
// a subpackage rather than part of internal/engines/claude because the
// export slots are typed on the bundle model, and the lean parent package
// is linked by the ltk and taskloom binaries, which must not carry that
// model.
//
// Nothing outside this package and internal/engines/claude names this
// engine; a fact about claude that some other package needs is declared on
// its Definition or here and read back through the registry.
package claudeengine

import (
	"path/filepath"
	"runtime"

	"github.com/ctxloom/ctxloom/internal/adapters/engineversion"
	claudereader "github.com/ctxloom/ctxloom/internal/adapters/transcript/vendorreader/claude"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/lm/hosting"
)

// Hosting returns claude-code's hosting record: what the backend registry
// still needs to run the kind claude.Build declares. Every fact is built
// from the engine's own constants, so there is no second copy to drift.
func Hosting() hosting.Hosting {
	credentialRelHome := filepath.ToSlash(filepath.Join(claude.ConfigDirName, claude.CredentialsFileName))
	return hosting.Hosting{
		Engine: claude.EngineName,
		NewBackend: func(launch agent.Launcher) agent.Backend {
			b := claude.NewClaudeCode()
			b.SetLauncher(launch)
			return b
		},
		NewConfig:      func() agent.BackendConfig { return &claude.ClaudeConfig{} },
		Surfaces:       claude.Declaration(),
		SettingsWriter: agent.Provide(claude.NewWriter),
		InstanceConfig: agent.Provide(claude.NewInstanceConfigWriter),
		// claude's project settings.json collapses onto its user-global one
		// exactly when workDir == $HOME — found live (`manage hooks install`
		// run from $HOME silently went global).
		HookGlobalScope: agent.Provide(hosting.HookGlobalScope{
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
		// Claude's OAuth refresh token is SINGLE-USE and rotating: whichever
		// holder refreshes consumes the token and receives its successor. A
		// mount is therefore the only arrangement with no failure mode at all
		// — one inode, one refresh path, host and instance provably in step.
		// Replication is accepted BELOW it, not beside it: two files kept in
		// step by a watcher leave a window in which an instance can present a
		// token another instance already spent, and the SERVER rejects it. It
		// is still the right second answer, because the alternative where
		// mounting is impossible is material that cannot renew at all.
		//
		// A stripped COPY is deliberately NOT accepted, at any position. It is
		// not a weaker sharing mode, it is a different product with a fuse on
		// it: the copy has the refresh token stripped, so it authenticates
		// until the access token expires and then that instance is stuck with
		// no way back. Accepting it as a last resort would convert a loud
		// launch refusal into a run that dies hours later, far from its cause.
		Provisioning: agent.Provide(agent.ProvisioningPolicy{
			Accept: []agent.MaterialDelivery{agent.MaterialDeliveryMounted, agent.MaterialDeliveryReplicated},
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
		TranscriptReaders: agent.Provide(claudereader.VersionedAdapters),
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
