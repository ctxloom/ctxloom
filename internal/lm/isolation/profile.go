package isolation

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// containerProfile describes how ONE engine's containerized run is provisioned —
// the backend-keyed knobs of the container policies (Container and the
// worktree-in-container composition), which are otherwise engine-agnostic:
//
//   - image: the agent image tag this engine runs in (it must carry the engine
//     CLI — a kiro run in a claude image would launch a container whose engine
//     spawn fails, which is worse than degrading). For a COMPOSABLE profile
//     (engineInstall != nil) this is only the FALLBACK when the shared composed
//     tag cannot be computed (e.g. the base content is unreadable); containerFor
//     normally overrides it with the content-keyed composed tag (see
//     composedIdentity).
//   - officialImage: the client's OFFICIAL upstream image, when the vendor ships
//     one. Preferred local-build source: ctxloom is overlaid onto it (see
//     overlayContainerfile), so a fresh `--pull` build rides the vendor's most
//     recent client. Empty = no official image (community images don't count —
//     they are not a trustworthy base).
//   - containerfile: a LEGACY embedded INSTALL Containerfile for a profile with
//     no engineInstall fragment (today: none — kept as the hook non-composable
//     profiles could still use). nil for every composable engine; the retired
//     per-engine production Containerfiles are replaced by engineInstall.
//   - engineInstall: the composable RUN-layer fragment (locked decision 2/4):
//     THIS engine's OWN official-installer install block, prereq-ensured
//     best-effort on an ARBITRARY base then hard-gated by a `<client>
//     --version` (or PATH-presence) validate — a broken engine layer fails the
//     BUILD, never ships silently. nil = no known official installer yet
//     (documented gap; the engine is excluded from composableEngines() and the
//     backend falls back to the legacy image/officialImage/containerfile
//     fields exactly as before this existed — see composeAgentContainerfile,
//     buildSources, composedIdentity).
//   - validate: the in-image command that proves the client is runnable (the
//     `<client> --version` build gate), used by the single-engine `--base-image`
//     overlay escape hatch (overlayContainerfile) — composition uses
//     engineInstall's OWN embedded validate step instead.
//   - resolveAuth: how the in-container engine authenticates (scoped env
//     passthrough and/or credential mounts into the fresh HOME). Takes the
//     run's host-side scratch dir too (a resolver that needs to COPY a host
//     credential before mounting it read-write — claude's token-refresh case,
//     auth.go's claudeCredentialCopyMounts — writes the copy under there; it
//     is removed at teardown with the rest of the scratch root). Resolvers
//     that only need read-only mounts or env passthrough ignore it.
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
	engineInstall      []byte
	validate           string
	resolveAuth        func(containerHome, scratchDir string) (containerAuth, bool)
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

// codexOverlayDirs shadows codex's managed-config surface: everything ctxloom
// writes for codex (config.toml, prompts/, skills/ — internal/codex's
// commandfiles.go/skillfiles.go/settings.go) lives under .codex, project-
// relative, plus the shared .ctxloom/cache. No project-root single-file
// residue (unlike claude's .mcp.json) — codex's own config.toml already sits
// inside .codex.
var codexOverlayDirs = []string{
	".codex",
	filepath.FromSlash(".ctxloom/cache"),
}

// opencodeOverlayDirs shadows opencode's managed-config surface: ctxloom's
// custom commands/skills/context land under .opencode (command/, skill/,
// ctxloom-context.md — internal/opencode's commandfiles.go/skillfiles.go/
// settings.go) plus the shared .ctxloom/cache. Like claude's .mcp.json,
// opencode's project-ROOT opencode.json (opencodeConfigFile) is a single-file
// residue NOT covered here — see defaultOverlayDirs' doc (single-file
// overlays would break the writers' atomic write+rename).
var opencodeOverlayDirs = []string{
	".opencode",
	filepath.FromSlash(".ctxloom/cache"),
}

// claudeCodeInstallFragment installs claude via its OFFICIAL npm package plus
// the claude-code-acp adapter ctxloom's structured chat needs, on an
// ARBITRARY base: node/npm ensured best-effort (the embedded default base and
// most devcontainers already ship node; a bare distro falls back to Debian's
// nodejs via apt), then the real install + a hard validate gate — the
// adapter has no --version (running it bare would serve ACP on stdio and
// hang), so its gate is PATH presence, mirroring the retired
// Containerfile-claude-code's install block exactly.
var claudeCodeInstallFragment = []byte(`RUN (command -v npm >/dev/null 2>&1 || (apt-get update && apt-get install -y --no-install-recommends nodejs npm && rm -rf /var/lib/apt/lists/*) || true) \
    && npm install -g @anthropic-ai/claude-code @zed-industries/claude-code-acp \
    && claude --version \
    && command -v claude-code-acp
`)

// codexInstallFragment installs the codex CLI via its npm package (the
// official installer table also lists the chatgpt.com/codex/install.sh shell
// script; npm is used here to mirror claude's prereq/validate shape and keep
// the fragment self-contained). Its own auth resolver is
// resolveCodexContainerAuth (auth.go, bony-spoof) — see the codex case below.
var codexInstallFragment = []byte(`RUN (command -v npm >/dev/null 2>&1 || (apt-get update && apt-get install -y --no-install-recommends nodejs npm && rm -rf /var/lib/apt/lists/*) || true) \
    && npm install -g @openai/codex \
    && codex --version
`)

// kiroInstallFragment installs kiro-cli via its official installer script,
// mirroring the retired Containerfile-kiro's install block exactly: curl/
// unzip ensured best-effort, the binary relocated to /usr/local/bin when the
// installer lands it under ~/.local/bin instead (any uid finds it there —
// the rootful-docker path runs the container as the host uid, not root).
var kiroInstallFragment = []byte(`RUN (command -v curl >/dev/null 2>&1 || (apt-get update && apt-get install -y --no-install-recommends curl ca-certificates unzip && rm -rf /var/lib/apt/lists/*) || true) \
    && curl -fsSL https://cli.kiro.dev/install | bash \
    && { command -v kiro-cli >/dev/null 2>&1 \
         || install -m 0755 /root/.local/bin/kiro-cli /usr/local/bin/kiro-cli; } \
    && kiro-cli --version
`)

// opencodeInstallFragment installs opencode via its official install script.
// The installer lands the binary under $HOME/.opencode/bin (per the opencode
// backend's own binary_path finding — it is NOT put on PATH), so it is
// relocated the same way kiro's is. Its own auth resolver is
// resolveOpencodeContainerAuth (auth.go: OpenRouter env / seeded
// ~/.local/share/opencode/auth.json) — see the opencode case below.
var opencodeInstallFragment = []byte(`RUN (command -v curl >/dev/null 2>&1 || (apt-get update && apt-get install -y --no-install-recommends curl ca-certificates unzip && rm -rf /var/lib/apt/lists/*) || true) \
    && curl -fsSL https://opencode.ai/install | bash \
    && { command -v opencode >/dev/null 2>&1 \
         || install -m 0755 "$HOME/.opencode/bin/opencode" /usr/local/bin/opencode; } \
    && opencode --version
`)

// composableEngines is the deterministic default engine set a composed agent
// image bakes when isolation_engines is unconfigured — every backend with a
// known OFFICIAL-installer fragment (locked decision 3: "all engines CAN be
// present" by default; isolation_engines trims it down). antigravity is
// excluded: no known official CLI installer exists yet (task bare-goes) — it
// keeps running via the default (claude-oriented) profile/image exactly as
// before this feature, entirely unaffected by composition.
func composableEngines() []string {
	return []string{"claude-code", "codex", "kiro", "opencode"}
}

// resolveEngines normalizes a configured isolation_engines set (config /
// --engines) against composableEngines(): empty/nil means "every known
// fragment" (the default, biggest-image, "one instance runs any engine"
// posture); a configured set is filtered to known engines in
// composableEngines() ORDER (never widened) — an unknown or non-composable
// name is DROPPED with a warning rather than silently ignored or promoted to
// "use everything".
func resolveEngines(configured []string) []string {
	if len(configured) == 0 {
		return composableEngines()
	}
	want := map[string]bool{}
	for _, c := range configured {
		want[c] = true
	}
	var out []string
	for _, e := range composableEngines() {
		if want[e] {
			out = append(out, e)
			delete(want, e)
		}
	}
	if len(want) > 0 {
		unknown := make([]string, 0, len(want))
		for name := range want {
			unknown = append(unknown, name)
		}
		sort.Strings(unknown)
		clidiag.Warn("ctxloom", "isolation_engines: unknown or non-composable engine(s) %s (known: %s); dropping them from the composed agent image",
			strings.Join(unknown, ", "), strings.Join(composableEngines(), ", "))
	}
	return out
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
			// 2026-07), so the composed engineInstall fragment (which fetches
			// the most recent claude) is the build source. A user can still
			// overlay onto any client-shipping base via `container build
			// --base-image`.
			engineInstall:      claudeCodeInstallFragment,
			validate:           "claude --version",
			resolveAuth:        resolveClaudeContainerAuth,
			authHint:           claudeContainerAuthHint(),
			overlayDirs:        defaultOverlayDirs,
			transcriptStoreRel: filepath.FromSlash(".claude/projects"),
		}
	case "kiro":
		return containerProfile{
			image: "ctxloom-agent-kiro:latest",
			// No officialImage: kiro ships no official container image (only
			// community ones); the composed engineInstall fragment fetches the
			// most recent kiro-cli via the official installer instead.
			engineInstall: kiroInstallFragment,
			validate:      "kiro-cli --version",
			resolveAuth:   resolveKiroContainerAuth,
			authHint:      "no KIRO_API_KEY to authenticate the in-container engine (AWS_* creds ride along when present but are not yet a standalone trigger, pending live kiro verification; subscription credential mounts pend live verification too)",
			overlayDirs:   kiroOverlayDirs,
			// ".kiro" is the ROOT of kiro's engine-home state (agents/,
			// settings/, skills/, steering/, sessions/), not the transcript
			// leaf directory itself — real per-session json+jsonl triples live
			// one level deeper, under sessions/cli/ (see internal/kiro/session.go).
			// Mounting the root still captures them (per this field's ROOT,
			// never a leaf contract above), so no change is needed here, only
			// this comment. NOTE a separate real gap this mount does NOT
			// cover: a `kiro-cli chat --no-interactive` oneshot run persists
			// into $XDG_DATA_HOME/kiro-cli/data.sqlite3 instead — a location
			// outside containerHome/.kiro entirely, so oneshot session state
			// is not captured by this mount at all.
			transcriptStoreRel: ".kiro",
		}
	case "codex":
		// codex is now COMPOSABLE (its own official-installer fragment) AND
		// has its OWN auth/overlay set (bony-spoof) — it no longer inherits
		// the default (claude) profile's resolveAuth/authHint/overlayDirs,
		// which was the paced-even security edge (a containerized codex run
		// silently mounting/passing the user's ANTHROPIC_* credentials into a
		// foreign, non-Anthropic engine). image stays the default fallback
		// tag: the REAL image a codex run gets is the composed multi-engine
		// tag computed from engineInstall by composedIdentity
		// (imagebuild.go), which already carries the codex CLI — this field
		// is only the name used when that composition cannot be computed.
		p := containerProfileFor("")
		p.engineInstall = codexInstallFragment
		p.validate = "codex --version"
		p.resolveAuth = resolveCodexContainerAuth
		p.authHint = "no OPENAI_API_KEY and no ~/.codex/auth.json to authenticate the in-container engine"
		p.overlayDirs = codexOverlayDirs
		p.transcriptStoreRel = filepath.FromSlash(".codex/sessions")
		return p
	case "opencode":
		// opencode is now COMPOSABLE (its own official-installer fragment)
		// AND has its OWN auth/overlay set — see codex's case comment above
		// for why inheriting the default (claude) auth was wrong for a
		// non-Anthropic engine; the same fix applies here.
		p := containerProfileFor("")
		p.engineInstall = opencodeInstallFragment
		p.validate = "opencode --version"
		p.resolveAuth = resolveOpencodeContainerAuth
		p.authHint = "no OPENROUTER_API_KEY and no seeded ~/.local/share/opencode/auth.json to authenticate the in-container engine"
		p.overlayDirs = opencodeOverlayDirs
		p.transcriptStoreRel = filepath.FromSlash(".local/share/opencode")
		return p
	// antigravity has no dedicated agent image/installer yet (task
	// bare-goes: the official install path is unverified) — its real
	// image/auth build-out folds into that separate workstream. It keeps
	// mapping its own native transcript store, but — per the same
	// paced-even fix as codex/opencode above — it does NOT inherit the
	// default (claude) profile's auth/overlay: silently mounting the user's
	// ANTHROPIC_* credentials into an antigravity container is exactly the
	// wrong-provider security edge this fix closes. Until bare-goes lands a
	// real resolver, antigravity honestly degrades instead.
	case "antigravity":
		p := containerProfileFor("")
		p.resolveAuth = resolveAntigravityContainerAuth
		p.authHint = "antigravity has no container auth resolver yet (its image/auth build-out is a separate workstream); the in-container engine cannot authenticate"
		// No known project-relative managed-config surface to shadow yet:
		// antigravity's writers (internal/antigravity) all target GLOBAL
		// ~/.gemini/* paths, not anything under the project dir (vast-rut).
		p.overlayDirs = nil
		p.transcriptStoreRel = filepath.FromSlash(".gemini/antigravity-cli/brain")
		return p
	default:
		return containerProfile{
			image:       defaultContainerImage,
			resolveAuth: resolveClaudeContainerAuth,
			authHint:    claudeContainerAuthHint(),
			overlayDirs: defaultOverlayDirs,
			// The default profile is claude-oriented throughout (image, auth),
			// including the store map.
			transcriptStoreRel: filepath.FromSlash(".claude/projects"),
		}
	}
}
