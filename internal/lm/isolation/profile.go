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
//   - officialImage: the client's OFFICIAL upstream image, when the vendor ships
//     one. Preferred local-build source: ctxloom is overlaid onto it (see
//     overlayContainerfile), so a fresh `--pull` build rides the vendor's most
//     recent client. Empty = no official image (community images don't count —
//     they are not a trustworthy base).
//   - containerfile: the embedded INSTALL Containerfile that builds the image
//     from a distro base by fetching the MOST RECENT client CLI (never pinned).
//     Fallback build source when there is no official image (or its build
//     fails); nil + no officialImage = not locally buildable, so an absent
//     image degrades exactly as before.
//   - validate: the in-image command that proves the client is runnable (the
//     `<client> --version` build gate) — a broken image never ships.
//   - resolveAuth: how the in-container engine authenticates (scoped env
//     passthrough and/or read-only credential mounts into the fresh HOME).
//   - authHint: the degrade diagnostic when resolveAuth finds nothing — names
//     the engine's trigger var/credential source without leaking values.
//   - overlayDirs: the project-relative managed-config DIRECTORIES ctxloom's
//     writers target under the run's cwd for this engine, shadowed by scratch
//     overlay mounts on the live-project mount so the HOST project stays clean
//     (see containerConfigOverlay; directories only — single-file overlays would
//     break the writers' atomic write+rename).
//   - transcriptStoreRel: the engine's native transcript/session STORE ROOT,
//     relative to the container HOME — the bind target sessionStateMounts maps
//     the harp's persist/transcripts dir onto so in-container transcripts
//     survive teardown. The ROOT, never a leaf: the transcript file name is a
//     runtime-generated sessionID/uuid the host cannot pre-create, and the
//     container's fresh HOME already scopes the root to this one run. Resolved
//     against the CONTAINER home; an engine-home env override (CODEX_HOME &
//     co.) is deliberately not consulted — the container axis never sets one.
//
// Profiles are keyed by the REGISTERED backend name (internal/lm/backends
// registry: "claude-code", "kiro", ...). The isolation package deliberately does
// not import the backends registry (it would drag the whole backend tree into
// the seam); the names are part of the descriptor contract.
type containerProfile struct {
	image              string
	officialImage      string
	containerfile      []byte
	validate           string
	resolveAuth        func(containerHome string) (containerAuth, bool)
	authHint           string
	overlayDirs        []string
	transcriptStoreRel string
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
			image: defaultContainerImage,
			// No officialImage: ghcr.io/anthropics/claude-code appears in docs
			// but does not resolve publicly (manifest unknown; verified live
			// 2026-07), so the npm install Containerfile — which fetches the
			// most recent claude — is the build source. A user can still
			// overlay onto any client-shipping base via `container build
			// --base-image`.
			containerfile:      containerfiles.ClaudeCode,
			validate:           "claude --version",
			resolveAuth:        resolveClaudeContainerAuth,
			authHint:           "no ANTHROPIC_API_KEY and no ~/.claude credentials to authenticate the in-container engine",
			overlayDirs:        defaultOverlayDirs,
			transcriptStoreRel: filepath.FromSlash(".claude/projects"),
		}
	case "kiro":
		return containerProfile{
			image: "ctxloom-agent-kiro:latest",
			// No officialImage: kiro ships no official container image (only
			// community ones); the install Containerfile fetches the most recent
			// kiro-cli via the official installer instead.
			containerfile: containerfiles.Kiro,
			validate:      "kiro-cli --version",
			resolveAuth:   resolveKiroContainerAuth,
			authHint:      "no KIRO_API_KEY to authenticate the in-container engine (subscription credential mounts pend live verification)",
			overlayDirs:   kiroOverlayDirs,
			// Kiro keeps its whole session store directly under $KIRO_HOME
			// (per-session json triples), so the engine home IS the store root.
			transcriptStoreRel: ".kiro",
		}
	// codex and antigravity have no dedicated agent image/auth yet: both
	// inherit the default (claude-oriented) profile exactly as they did
	// through the default case — these cases exist ONLY to map each engine's
	// native transcript store so a containerized run persists it correctly.
	case "codex":
		p := containerProfileFor("")
		p.transcriptStoreRel = filepath.FromSlash(".codex/sessions")
		return p
	case "antigravity":
		p := containerProfileFor("")
		p.transcriptStoreRel = filepath.FromSlash(".gemini/antigravity-cli/brain")
		return p
	default:
		return containerProfile{
			image:       defaultContainerImage,
			resolveAuth: resolveClaudeContainerAuth,
			authHint:    "no ANTHROPIC_API_KEY and no ~/.claude credentials to authenticate the in-container engine",
			overlayDirs: defaultOverlayDirs,
			// The default profile is claude-oriented throughout (image, auth),
			// including the store map.
			transcriptStoreRel: filepath.FromSlash(".claude/projects"),
		}
	}
}
