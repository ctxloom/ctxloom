package isolation

import (
	"path/filepath"

	containerfiles "github.com/ctxloom/ctxloom/container"
)

// containerProfile describes how ONE engine's containerized run is provisioned —
// the backend-keyed knobs of the container policies (Container and the
// worktree-in-container composition), which are otherwise engine-agnostic:
//
//   - image: the agent image tag this engine runs in (it must carry the engine
//     CLI — a kiro run in a claude image would launch a container whose engine
//     spawn fails, which is worse than degrading).
//   - containerfile: the embedded Containerfile that can build that image
//     LOCALLY when it is absent (ensureImage); nil = not locally buildable, so
//     an absent image degrades exactly as before.
//   - resolveAuth: how the in-container engine authenticates (scoped env
//     passthrough and/or read-only credential mounts into the fresh HOME).
//   - authHint: the degrade diagnostic when resolveAuth finds nothing — names
//     the engine's trigger var/credential source without leaking values.
//   - overlayDirs: the project-relative managed-config DIRECTORIES ctxloom's
//     writers target under the run's cwd for this engine, shadowed by scratch
//     overlay mounts on the live-project mount so the HOST project stays clean
//     (see containerConfigOverlay; directories only — single-file overlays would
//     break the writers' atomic write+rename).
//
// Profiles are keyed by the REGISTERED backend name (internal/lm/backends
// registry: "claude-code", "kiro", ...). The isolation package deliberately does
// not import the backends registry (it would drag the whole backend tree into
// the seam); the names are part of the descriptor contract.
type containerProfile struct {
	image         string
	containerfile []byte
	resolveAuth   func(containerHome string) (containerAuth, bool)
	authHint      string
	overlayDirs   []string
}

// defaultOverlayDirs is the claude-oriented managed-config overlay set:
// .claude (settings.json, commands/, skills) and .ctxloom/cache (the framed
// context file). The project-root file .mcp.json is deliberately absent (the
// flagged single-file residue — see the Container doc).
var defaultOverlayDirs = []string{
	".claude",
	filepath.FromSlash(".ctxloom/cache"),
}

// kiroOverlayDirs shadows kiro's managed-config surface: everything ctxloom
// writes for kiro lives under .kiro (agents/, settings/mcp.json, steering/,
// skills/) plus the shared .ctxloom/cache. Unlike claude there is no project-root
// single-file residue — kiro's MCP config sits inside .kiro/settings.
var kiroOverlayDirs = []string{
	".kiro",
	filepath.FromSlash(".ctxloom/cache"),
}

// containerProfileFor maps a registered backend name to its container profile.
// Unknown (and empty) names get the DEFAULT profile — today's claude-oriented
// behaviour (the generic agent image + claude auth) with no local-build recipe,
// so engines without a profile keep the pre-profile semantics: run if the
// generic image is present, degrade if not.
func containerProfileFor(backend string) containerProfile {
	switch backend {
	case "claude-code":
		return containerProfile{
			image:         defaultContainerImage,
			containerfile: containerfiles.ClaudeCode,
			resolveAuth:   resolveClaudeContainerAuth,
			authHint:      "no ANTHROPIC_API_KEY and no ~/.claude credentials to authenticate the in-container engine",
			overlayDirs:   defaultOverlayDirs,
		}
	case "kiro":
		return containerProfile{
			image:         "ctxloom-agent-kiro:latest",
			containerfile: containerfiles.Kiro,
			resolveAuth:   resolveKiroContainerAuth,
			authHint:      "no KIRO_API_KEY to authenticate the in-container engine (subscription credential mounts pend live verification)",
			overlayDirs:   kiroOverlayDirs,
		}
	default:
		return containerProfile{
			image:       defaultContainerImage,
			resolveAuth: resolveClaudeContainerAuth,
			authHint:    "no ANTHROPIC_API_KEY and no ~/.claude credentials to authenticate the in-container engine",
			overlayDirs: defaultOverlayDirs,
		}
	}
}
