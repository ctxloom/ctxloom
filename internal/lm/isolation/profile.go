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
//   - engineInstall: the composable RUN-layer fragment (locked decision 2/4):
//     THIS engine's OWN official-installer install block, prereq-ensured
//     best-effort on an ARBITRARY base then hard-gated by a `<client>
//     --version` (or PATH-presence) validate — a broken engine layer fails the
//     BUILD, never ships silently. nil = no known official installer yet
//     (documented gap; the engine is excluded from composableEngines() and
//     buildSources returns nothing for it — no locally-buildable recipe exists
//     until an installer fragment is written; see composeAgentContainerfile,
//     buildSources, composedIdentity). A prior fix deleted the legacy
//     officialImage/containerfile fallback fields this comment used to
//     describe: no profile, registered or hypothetical, ever set them, so the
//     fallback branch they gated in buildSources could never produce output.
//   - validate: the in-image command that proves the client is runnable (the
//     `<client> --version` build gate), used by the single-engine `--base-image`
//     overlay escape hatch (overlayContainerfile) — composition uses
//     engineInstall's OWN embedded validate step instead.
//   - resolveAuth: how the in-container engine authenticates (scoped env
//     passthrough and/or credential mounts into the fresh HOME). Takes the
//     run's host-side scratch dir too, a seam-signature remnant: no current
//     resolver writes a credential under it (claude's token-refresh case now
//     bind-mounts the REAL host credential read-write instead of a scratch
//     copy — auth.go's claudeCredentialMounts; codex/opencode mount their real
//     host credential read-only). Every resolver ignores the scratch dir today.
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

// mockOverlayDirs shadows mock's ONLY project-relative managed-config
// DIRECTORY: .mock/skills (internal/lm/backends/mock_surfaces.go's
// mockSkillsPath — the shared ManagedSkillPackages delivery, the same
// mechanism every other backend's skills surface uses). The whole ".mock"
// parent is shadowed, not just "skills" underneath it, mirroring every other
// profile's whole-managed-dir mount (.claude/.kiro/.codex/.opencode); .mock
// has no other sibling content today, so the wider shadow costs nothing.
// mock's CONTEXT surface (MOCK_CONTEXT.md, mockContextPath) is a PROJECT-ROOT
// SINGLE FILE, deliberately NOT listed here — the same single-file residue
// defaultOverlayDirs' doc flags for claude's .mcp.json and opencodeOverlayDirs'
// for opencode.json: a file bind-mount would break the writers' atomic
// write+rename (containerConfigOverlay's own doc: "directories only"). The
// shared .ctxloom/cache rides along like every other profile's set — the
// framed context file cache is engine-agnostic, not mock-specific.
var mockOverlayDirs = []string{
	".mock",
	filepath.FromSlash(".ctxloom/cache"),
}

// mockInstallFragment is mock's composable-engine RUN layer — and it is
// DELIBERATELY THE ODD ONE OUT among every fragment in this file. Mock has NO
// vendor CLI to install: its "engine" is the ctxloom binary itself
// (internal/lm/backends' Mock — compiled into ctxloom, calling no external
// process), so there is no client to fetch, no adapter to validate, nothing
// this fragment could do that claudeCodeInstallFragment/codexInstallFragment/
// etc. do for their own engines.
//
// The two things a mock container run actually needs — ctxloom itself at
// defaultContainerBinary, and a `cat` for the shared-filesystem probe
// (sharedfs.go's probeOneRoot runs `cat /probe/marker` IN THIS IMAGE) — are
// BOTH already guaranteed unconditionally by machinery this fragment does not
// own: composeAgentContainerfile's own trailing `COPY ctxloom …` step runs
// after every engine fragment regardless of which engines were selected, and
// `cat` ships as part of coreutils on every base composeAgentContainerfile
// builds onto (baseContractLayer's apt-get layer, or the embedded default
// base). This fragment adds nothing to either guarantee.
//
// Its job is narrower: (1) be NON-NIL, so containerProfileFor("mock").
// engineInstall marks the profile composable (buildSources' `p.engineInstall
// != nil` check) and `ctxloom container build mock` stops failing "no local
// build recipe" for a backend that plainly does not need one refused; and (2)
// assert the one thing that genuinely IS mock-specific — `cat` — as a
// build-time gate rather than a bare, unverified assumption, the same
// "prove it, don't assume it" discipline nodeFloorFragment and the ACP run
// gates apply to their own floors.
//
// NOT a template for a real engine: every other fragment in this file
// installs an actual vendor client and hard-gates it running
// (`<client> --version` / adapterRunGate / nativeACPRunGate). A future real
// engine's fragment must do the same — this shape is correct ONLY because
// mock has no vendor client at all.
var mockInstallFragment = []byte(`RUN command -v cat >/dev/null 2>&1 \
    || { echo "ctxloom: this base has no cat (needed by the shared-fs probe, sharedfs.go's probeOneRoot)" >&2; exit 1; }
`)

// nodeFloorFragment is the shared prereq every ACP-adapter install depends on:
// a node the adapter can actually PARSE.
//
// The 2026-07-24 container-delegation defect lived here. The old prereq was
// `command -v npm || apt-get install -y nodejs npm || true` — "best-effort",
// version-blind. On an Ubuntu 24.04 base that resolves to Node 18.19.1, which
// predates import attributes (`import x from "./p.json" with {type:"json"}`,
// Node 18.20/20.10), the exact syntax @zed-industries/claude-code-acp's entry
// module opens with. So the image built GREEN, the adapter sat on PATH, and
// EVERY containerized claude-code agent's structured chat died at startup with
// `SyntaxError: Unexpected token 'with'` — producing zero ChatEvents, a
// transcript holding only the briefing `user` record at seq 0, and an endless
// coordinator relaunch loop.
//
// So the floor is now asserted, not hoped for: an existing node >= 20 is left
// alone (no network call, no repo added), and only a too-old/absent node
// triggers the vendor's own documented Debian/Ubuntu channel. If the result is
// STILL below the floor, the BUILD fails loudly here rather than shipping an
// image whose agent cannot speak.
//
// SUPPLY CHAIN: deb.nodesource.com is a new download source for these images
// (previously only the distro's own apt repo and the npm registry). It is
// nodejs.org's own documented Debian/Ubuntu install channel, but it is a
// dependency decision — flagged for human review, not slipped in.
const nodeFloorFragment = `RUN set -e \
    && NODE_MAJOR=$( (command -v node >/dev/null 2>&1 && node -p 'process.versions.node.split(".")[0]') || echo 0 ) \
    && if [ "$NODE_MAJOR" -lt 20 ]; then \
         (command -v curl >/dev/null 2>&1 || (apt-get update && apt-get install -y --no-install-recommends curl ca-certificates gnupg && rm -rf /var/lib/apt/lists/*)) \
         && curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
         && apt-get install -y --no-install-recommends nodejs \
         && rm -rf /var/lib/apt/lists/*; \
       fi \
    && NODE_MAJOR=$(node -p 'process.versions.node.split(".")[0]') \
    && { [ "$NODE_MAJOR" -ge 20 ] || { echo "ctxloom: this base resolves node $(node --version), below the ACP adapters' floor (>= 20); provide a newer node in the base image" >&2; exit 1; }; }
`

// acpProbeFailurePatterns is the vocabulary of "this ACP surface did not come
// up" — the one grep both execution gates below share. It unions the two
// mechanisms by which an image can ship a present-but-dead structured-chat
// surface:
//
//   - the NODE MODULE LOADER (SyntaxError … ERR_UNKNOWN_BUILTIN_MODULE): the
//     2026-07-24 claude-code defect (see nodeFloorFragment), where the adapter
//     was installed on an image whose node could not parse it.
//   - the CLIENT'S OWN ARGUMENT PARSER plus the dynamic loader (unrecognized
//     subcommand … symbol lookup error): a client binary that landed but whose
//     `acp` subcommand does not exist (a half-succeeded installer, a pinned-old
//     release) or that cannot be loaded at all.
//
// "Failed to change directory" is opencode's specific shape for the SAME
// condition and is deliberately listed: opencode does not reject an
// unrecognized first argument, it adopts it as the working directory — so an
// opencode with no `acp` command reports `Error: Failed to change directory to
// <cwd>/acp` AND EXITS ZERO. Measured live 2026-07-24. That is precisely why
// these gates match on TEXT rather than exit status.
const acpProbeFailurePatterns = `SyntaxError|Cannot find module|ERR_MODULE_NOT_FOUND|ERR_UNKNOWN_BUILTIN_MODULE|` +
	`unrecognized subcommand|unknown subcommand|unknown command|unexpected argument|` +
	`error while loading shared libraries|symbol lookup error|command not found|Failed to change directory`

// acpRunProbe is the shared body of both ACP execution gates: run the
// structured-chat surface with stdin closed under a timeout, capture
// everything it says, and fail the BUILD if it said any of
// acpProbeFailurePatterns.
//
// probe is the shell command to run; label names it in the diagnostic. The
// exit status is deliberately DISCARDED (`|| true`): a healthy ACP server and
// a dead one can both exit zero or non-zero (claude-code-acp exits non-zero on
// a clean EOF shutdown; opencode exits ZERO when its `acp` command is missing
// — both measured live), and it was precisely an exit-status-shaped check that
// let the original defect through.
func acpRunProbe(label, probe string) string {
	return `    && { timeout 20 ` + probe + ` </dev/null >/tmp/ctxloom-acp-probe.log 2>&1 || true; } \
    && { ! grep -qE '` + acpProbeFailurePatterns + `' /tmp/ctxloom-acp-probe.log \
         || { echo "ctxloom: ` + label + ` is installed but its ACP surface cannot start in this image:" >&2; cat /tmp/ctxloom-acp-probe.log >&2; exit 1; }; } \
    && rm -f /tmp/ctxloom-acp-probe.log
`
}

// adapterRunGate returns the image-time validation for an engine whose ACP
// surface is a SEPARATE adapter binary (agent.ACPAdapter — claude-code's
// claude-code-acp, codex's codex-acp).
//
// PATH presence — the old gate, `command -v <adapter>` — is exactly the
// silent-no-op this project bans: it proved a FILE existed and nothing about
// whether it could run (see nodeFloorFragment for the defect it let through).
// The adapter has no --version (running it bare serves ACP on stdio), so this
// starts it with stdin closed under a timeout and fails the build on any
// module-loader error in its output. A healthy adapter reaches its stdio loop
// and says nothing of the sort; a broken one dies before it ever reads a byte.
func adapterRunGate(adapter string) string {
	return `    && command -v ` + adapter + ` \
` + acpRunProbe(adapter, adapter)
}

// nativeACPRunGate returns the image-time validation for an engine whose ACP
// surface is a SUBCOMMAND OF ITS OWN CLIENT (agent.ACPNative — kiro's
// `kiro-cli acp`, opencode's `opencode acp`; see internal/lm/backends'
// kiroACPTransport / opencodeACPTransport and internal/kiro,
// internal/opencode's chatACPConfig).
//
// These engines install from shell installers, not npm, so they were never
// exposed to the node-version defect — but they carried the SAME silent-no-op
// class by a different mechanism. Their only build gate was `<client>
// --version`, which proves the client binary loads and NOTHING about the
// surface every structured-chat run actually spawns. A half-succeeded
// installer, a pinned-old release predating `acp`, or a binary that cannot
// resolve a shared library would all ship a green image whose delegated agents
// produce zero ChatEvents — exactly claude-code's failure with a different
// first cause. `--version` is not a substitute: it exercises a different code
// path from the one that has to work.
//
// Measured live 2026-07-24: a healthy `kiro-cli acp </dev/null` and a healthy
// `opencode acp </dev/null` each exit 0 having printed NOTHING, so a
// well-behaved image passes this silently.
func nativeACPRunGate(client, sub string) string {
	return acpRunProbe(client+" "+sub, client+" "+sub)
}

// claudeCodeInstallFragment installs claude via its OFFICIAL npm package plus
// the claude-code-acp adapter ctxloom's structured chat needs, on an ARBITRARY
// base: the asserted node floor (nodeFloorFragment) first, then the real
// install, then a validate gate that RUNS both the client (`claude --version`)
// and the adapter (adapterRunGate) rather than merely locating them.
var claudeCodeInstallFragment = []byte(nodeFloorFragment + `RUN npm install -g @anthropic-ai/claude-code @zed-industries/claude-code-acp \
    && claude --version \
` + adapterRunGate("claude-code-acp"))

// codexInstallFragment installs the codex CLI via its npm package (the
// official installer table also lists the chatgpt.com/codex/install.sh shell
// script; npm is used here to mirror claude's prereq/validate shape and keep
// the fragment self-contained), PLUS the codex-acp adapter ctxloom's
// structured chat needs on the container-runtime axis (internal/codex/
// chat.go's `req.Runtime != agent.RuntimeContainer` gate is what makes this
// image-time install load-bearing instead of merely convenient — without it
// a containerized codex agent's structured chat fails at LookPath, exactly
// the CodexACPAdapter-missing error the host-PATH gate would report, just
// inside the container instead of on the host).
//
// Mirrors claudeCodeInstallFragment's shape (single `npm install -g` line
// carrying BOTH the client and its adapter, one hard validate gate at the
// end) — closes the real gap this generalization found: this fragment
// previously installed `codex` only, never `codex-acp`, so EVERY
// containerized codex agent's structured chat was silently broken.
//
// TODO(acp-transport-generalization): this literal ("codex-acp") is NOT yet
// sourced from the SAME single declaration internal/codex/chat.go's Chat()
// gate and DOCTOR-CHECK-ACPADAPTER-m3 read (internal/lm/backends'
// agent.ACPTransport, ACPTransportFor("codex")) — this package deliberately
// does not import internal/lm/backends (containerProfile's own doc, above:
// "it would drag the whole backend tree into the seam"), so wiring this
// fragment onto that descriptor is a separate, more invasive slice (e.g.
// threading the resolved InstallCmd through composeAgentContainerfile's
// caller instead of a package-level []byte var). Until then, keep this
// binary name in sync BY HAND with codex.CodexACPAdapter/CodexACPTransport's
// InstallCmd if either ever changes.
var codexInstallFragment = []byte(nodeFloorFragment + `RUN npm install -g @openai/codex @zed-industries/codex-acp \
    && codex --version \
` + adapterRunGate("codex-acp"))

// kiroInstallFragment installs kiro-cli via its official installer script,
// mirroring the retired Containerfile-kiro's install block exactly: curl/
// unzip ensured best-effort, the binary relocated to /usr/local/bin when the
// installer lands it under ~/.local/bin instead (any uid finds it there —
// the rootful-docker path runs the container as the host uid, not root).
//
// The validate gate is TWO steps, not one: `kiro-cli --version` proves the
// client binary loads, and nativeACPRunGate proves `kiro-cli acp` — the
// surface every structured-chat run actually spawns — comes up. The second is
// not implied by the first; see nativeACPRunGate's doc.
var kiroInstallFragment = []byte(`RUN (command -v curl >/dev/null 2>&1 || (apt-get update && apt-get install -y --no-install-recommends curl ca-certificates unzip && rm -rf /var/lib/apt/lists/*) || true) \
    && curl -fsSL https://cli.kiro.dev/install | bash \
    && { command -v kiro-cli >/dev/null 2>&1 \
         || install -m 0755 /root/.local/bin/kiro-cli /usr/local/bin/kiro-cli; } \
    && kiro-cli --version \
` + nativeACPRunGate("kiro-cli", "acp"))

// opencodeInstallFragment installs opencode via its official install script.
// The installer lands the binary under $HOME/.opencode/bin (per the opencode
// backend's own binary_path finding — it is NOT put on PATH), so it is
// relocated the same way kiro's is. Its own auth resolver is
// resolveOpencodeContainerAuth (auth.go: OpenRouter env / seeded
// ~/.local/share/opencode/auth.json) — see the opencode case below.
//
// Like kiro's, the validate gate is TWO steps: `opencode --version` for the
// client, then nativeACPRunGate for `opencode acp`, the surface structured
// chat spawns. opencode is the sharpest case for gating on TEXT rather than
// status — with no `acp` command it adopts "acp" as a directory, prints
// `Error: Failed to change directory to <cwd>/acp`, and EXITS ZERO
// (measured live 2026-07-24).
var opencodeInstallFragment = []byte(`RUN (command -v curl >/dev/null 2>&1 || (apt-get update && apt-get install -y --no-install-recommends curl ca-certificates unzip && rm -rf /var/lib/apt/lists/*) || true) \
    && curl -fsSL https://opencode.ai/install | bash \
    && { command -v opencode >/dev/null 2>&1 \
         || install -m 0755 "$HOME/.opencode/bin/opencode" /usr/local/bin/opencode; } \
    && opencode --version \
` + nativeACPRunGate("opencode", "acp"))

// composableEngines is the deterministic default engine set a composed agent
// image bakes when isolation_engines is unconfigured — every backend with a
// known OFFICIAL-installer fragment (locked decision 3: "all engines CAN be
// present" by default; isolation_engines trims it down), alphabetical order.
func composableEngines() []string {
	return []string{"claude-code", "codex", "kiro", "opencode"}
}

// ComposableEngines exports composableEngines() (one of the four
// independently-maintained engine-identity rosters found spread across the
// codebase — see tests/arch/engine_identity_arch_test.go's
// TestArch_EngineIdentityRosters_MembersAreRegisteredBackends, which
// validates every name returned here is still a real, currently-registered
// internal/lm/backends name). Exported read-only so that gate can reach this
// package's otherwise-unexported roster without isolation importing backends
// (which would cycle: backends already imports isolation).
func ComposableEngines() []string {
	return composableEngines()
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
		// has its OWN auth/overlay set — it no longer inherits
		// the default (claude) profile's resolveAuth/authHint/overlayDirs,
		// which was a security edge (a containerized codex run
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
	// mock is COMPOSABLE (engineInstall != nil, so buildSources stops
	// reporting "no local build recipe" for it) but — unlike every other
	// case above — installs NO vendor CLI at all; see mockInstallFragment's
	// doc for why its only real job is asserting `cat`. Its auth resolver,
	// resolveMockContainerAuth, is the one resolver in this file that never
	// returns ok=false: mock authenticates against no vendor, so there is
	// nothing to resolve (see that function's doc — this is a POSITIVE,
	// verified fact about mock, not a template for a real engine).
	// Deliberately its OWN case rather than falling to `default`: the
	// default arm's resolveAuth (noContainerAuthProfile) exists precisely
	// for engines whose auth needs are UNKNOWN, and mock's are known, so it
	// does not belong there — and NOT added to composableEngines() (that
	// roster question is escalated, not decided here; see this change's own
	// report).
	//
	// transcriptStoreRel is deliberately "" — mock keeps NO transcripts at
	// all (internal/lm/backends' NewMock wires &NilSessionHistory{}, its own
	// doc: "mock keeps no transcripts"), so there is no native store root to
	// bind-mount; sessionStateMounts' `if c.profile.transcriptStoreRel !=
	// ""` guard already treats "" as "nothing to mount for this engine",
	// the CORRECT reading here, not an oversight (contrast
	// TestContainerProfileFor_EveryProfileMapsATranscriptStore, whose "every
	// branch sets a non-empty root" invariant is scoped to
	// composableEngines()+default and explicitly carves mock out).
	case "mock":
		return containerProfile{
			image:              defaultContainerImage,
			engineInstall:      mockInstallFragment,
			validate:           "cat --version",
			resolveAuth:        resolveMockContainerAuth,
			authHint:           "unreachable: resolveMockContainerAuth never returns ok=false (mock authenticates against no vendor)",
			overlayDirs:        mockOverlayDirs,
			transcriptStoreRel: "",
		}
	default:
		// This used to be resolveAuth: resolveClaudeContainerAuth —
		// the unknown-backend default failed OPEN on credentials, so any
		// unrecognized engine (a real, reachable path: registry.go registers
		// a generic "acp" backend, and container_transport.go treats an
		// unrecognized/empty engine name as this default profile) got the
		// user's ANTHROPIC_API_KEY/ANTHROPIC_AUTH_TOKEN passed through and
		// ~/.claude credentials copy-mounted into a FOREIGN engine's
		// container. codex/kiro/opencode above each earned their
		// own resolveAuth for exactly this reason; the default must not hand
		// out Anthropic credentials to an engine nobody vetted. It now fails
		// closed (noContainerAuthProfile) so an unprofiled engine degrades
		// honestly instead of silently authenticating as claude.
		return containerProfile{
			image:       defaultContainerImage,
			resolveAuth: noContainerAuthProfile,
			authHint:    "no container-auth profile is registered for this engine; add one in containerProfileFor rather than inheriting the default",
			overlayDirs: defaultOverlayDirs,
			// The default profile's image/store-map default stays
			// claude-oriented (harmless metadata); only the AUTH default
			// changed — see the resolveAuth comment above.
			transcriptStoreRel: filepath.FromSlash(".claude/projects"),
		}
	}
}

// ContainerOverlayDirsFor returns a copy of containerProfileFor(backend)'s
// overlayDirs — the project-relative managed-config directories a
// containerized run of backend shadows. Exported read-only so tests/arch's
// engine-layout gate (TestArch_EngineLayoutAgreement) can check this
// package's defaultOverlayDirs/kiroOverlayDirs/codexOverlayDirs/
// opencodeOverlayDirs/mockOverlayDirs literals against each owning engine
// package's own ConfigDirName constant, the same import-cycle reasoning as
// ComposableEngines/CredentialSeedEngineNames above applies here too.
func ContainerOverlayDirsFor(backend string) []string {
	dirs := containerProfileFor(backend).overlayDirs
	out := make([]string, len(dirs))
	copy(out, dirs)
	return out
}

// ContainerTranscriptStoreRelFor returns containerProfileFor(backend)'s
// transcriptStoreRel — the engine's native transcript-store root, relative to
// the container HOME (empty when the engine keeps no transcripts, e.g.
// mock). Exported for the same engine-layout gate as
// ContainerOverlayDirsFor.
func ContainerTranscriptStoreRelFor(backend string) string {
	return containerProfileFor(backend).transcriptStoreRel
}
