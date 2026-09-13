package isolation

import (
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// engineContainerSpec describes how ONE engine's containerized run is provisioned —
// the backend-keyed knobs of the container policies (Container and the
// worktree-in-container composition), which are otherwise engine-agnostic:
//
//   - image: the agent image tag this engine runs in (it must carry the engine
//     CLI — a foreign-engine run in a claude image would launch a container whose engine
//     spawn fails, which is worse than degrading). For a COMPOSABLE spec
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
//     describe: no spec, registered or hypothetical, ever set them, so the
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
//     copy — auth.go's claudeCredentialMountsAt; other engines mount their real
//     host credential read-only). Every resolver ignores the scratch dir today.
//   - authHint: the degrade diagnostic when resolveAuth finds nothing — names
//     the engine's trigger var/credential source without leaking values.
//   - relocatedCredentialMounts: the credential FILE mount a run whose engine
//     home was RELOCATED (config_home: project — MountEngineHome) needs over
//     the copy seeded into that home, so the engine keeps a credential it
//     can refresh in place. nil for an engine that authenticates against no
//     vendor or whose credential no home var relocates.
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
//     against the CONTAINER home; an engine-home env override (&
//     co.) is deliberately not consulted — the container axis never sets one.
//
// Specs are keyed by the REGISTERED backend name (internal/lm/backends
// registry: "claude-code", ...). The isolation package deliberately does
// not import the backends registry (it would drag the whole backend tree into
// the seam); the names are part of the descriptor contract.
type engineContainerSpec struct {
	image         string
	engineInstall []byte
	validate      string
	resolveAuth   func(containerHome, scratchDir string) (containerAuth, bool)
	authHint      string
	// relocatedCredentialMounts takes the ENGINE-side path of the relocated
	// home (what the engine is told) and returns the file mount(s) into it;
	// ok=false when the host credential material is absent.
	relocatedCredentialMounts func(engineHome string) ([]Mount, bool)
	overlayDirs               []string
	transcriptStoreRel        string
}

// defaultOverlayDirs is the claude-oriented managed-config overlay set:
// .claude (settings.json, commands/, skills) and .ctxloom/cache (the framed
// context file). The project-root file .mcp.json is deliberately absent (the
// flagged single-file residue — see the Container doc).
var defaultOverlayDirs = []string{
	".claude",
	filepath.FromSlash(".ctxloom/cache"),
}

// mockOverlayDirs shadows mock's ONLY project-relative managed-config
// DIRECTORY: .mock/skills (internal/lm/backends/mock_surfaces.go's
// mockSkillsPath — the shared ManagedSkillPackages delivery, the same
// mechanism every other backend's skills surface uses). The whole ".mock"
// parent is shadowed, not just "skills" underneath it, mirroring every other
// spec's whole-managed-dir mount; .mock has no other sibling content today,
// so the wider shadow costs nothing.
// mock's CONTEXT surface (MOCK_CONTEXT.md, mockContextPath) is a PROJECT-ROOT
// SINGLE FILE, deliberately NOT listed here — the same single-file residue
// defaultOverlayDirs' doc flags for claude's .mcp.json: a file bind-mount
// would break the writers' atomic
// write+rename (containerConfigOverlay's own doc: "directories only"). The
// shared .ctxloom/cache rides along like every other spec's set — the
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
// this fragment could do that claudeCodeInstallFragment and its siblings do
// for their own engines.
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
// Its job is narrower: (1) be NON-NIL, so engineContainerSpecFor("mock").
// engineInstall marks the spec composable (buildSources' `p.engineInstall
// != nil` check) and `ctxloom container build mock` stops failing "no local
// build recipe" for a backend that plainly does not need one refused; and (2)
// assert the one thing that genuinely IS mock-specific — `cat` — as a
// build-time gate rather than a bare, unverified assumption, the same
// "prove it, don't assume it" discipline nodeFloorFragment applies to its
// own floor.
//
// NOT a template for a real engine: every other fragment in this file
// installs an actual vendor client and hard-gates it running
// (`<client> --version`). A future real engine's fragment must do the same —
// this shape is correct ONLY because mock has no vendor client at all.
var mockInstallFragment = []byte(`RUN command -v cat >/dev/null 2>&1 \
    || { echo "ctxloom: this base has no cat (needed by the shared-fs probe, sharedfs.go's probeOneRoot)" >&2; exit 1; }
`)

// nodeFloorFragment is the shared prereq every npm-installed engine client
// depends on: a node that can actually PARSE what npm just landed.
//
// A container-delegation defect lived here. The old prereq was
// `command -v npm || apt-get install -y nodejs npm || true` — "best-effort",
// version-blind. On an Ubuntu 24.04 base that resolves to Node 18.19.1, which
// predates import attributes (`import x from "./p.json" with {type:"json"}`,
// Node 18.20/20.10) — syntax a current npm-published client's entry module
// opens with. So the image built GREEN, the binary sat on PATH, and EVERY
// containerized agent of that engine died at startup with
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
    && { [ "$NODE_MAJOR" -ge 20 ] || { echo "ctxloom: this base resolves node $(node --version), below the engine clients' floor (>= 20); provide a newer node in the base image" >&2; exit 1; }; }
`

// claudeCodeInstallFragment installs claude via its OFFICIAL npm package on an
// ARBITRARY base: the asserted node floor (nodeFloorFragment) first, then the
// real install, then a validate gate that RUNS the client (`claude --version`)
// rather than merely locating it.
var claudeCodeInstallFragment = []byte(nodeFloorFragment + `RUN npm install -g @anthropic-ai/claude-code \
    && claude --version
`)

// composableEngines is the deterministic default engine set a composed agent
// image bakes when isolation_engines is unconfigured — every backend with a
// known OFFICIAL-installer fragment (locked decision 3: "all engines CAN be
// present" by default; isolation_engines trims it down), alphabetical order.
func composableEngines() []string {
	return []string{"claude-code"}
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

// engineContainerSpecFor maps a registered backend name to its container spec.
// The key is the ENGINE (the registered backend name), never an agent label or
// an agent name: two agents bound to the same engine authenticate identically,
// and two agents bound to DIFFERENT engines must not (the credential-crossing
// this table exists to prevent). Callers therefore resolve it PER CALL from the
// backend the run actually carries — a constructor that fixes it at build time
// (the deleted NewContainer/NewContainerWorktree, which passed "") could only
// ever name the wrong engine.
//
// An unmapped (or empty) name gets the default spec, whose AUTH fails closed
// (noContainerAuth): its containerized run aborts at PrepareWorkspace's auth
// gate rather than inheriting claude's credentials. The image/overlay/transcript
// defaults it also carries stay claude-oriented, but they never run — the auth
// gate is upstream of them. Config validation (operations.validateAgentAxes,
// via HasContainerAuth) refuses `runtime: container` for such a backend at
// WRITE time; this arm is the last line for the paths that never went through
// a binding.
func engineContainerSpecFor(backend string) engineContainerSpec {
	// Resolved through the repo-wide alias table so a declared alias reaches
	// its engine's arm instead of the fail-closed default. Every case label is
	// a canonical name; ContainerAuthEngines/composableEngines enumerate them
	// and enginekeys.go's init asserts that.
	switch agent.CanonicalEngineName(backend) {
	case "claude-code":
		return engineContainerSpec{
			image: defaultContainerImage,
			// No officialImage: ghcr.io/anthropics/claude-code appears in docs
			// but does not resolve publicly (manifest unknown; verified live
			// 2026-07), so the composed engineInstall fragment (which fetches
			// the most recent claude) is the build source. A user can still
			// overlay onto any client-shipping base via `container build
			// --base-image`.
			engineInstall:             claudeCodeInstallFragment,
			validate:                  "claude --version",
			resolveAuth:               resolveClaudeContainerAuth,
			authHint:                  claudeContainerAuthHint(),
			relocatedCredentialMounts: claudeCredentialMountsAt,
			overlayDirs:               defaultOverlayDirs,
			transcriptStoreRel:        filepath.FromSlash(".claude/projects"),
		}
	// mock is COMPOSABLE (engineInstall != nil, so buildSources stops
	// reporting "no local build recipe" for it) but — unlike every other
	// case above — installs NO vendor CLI at all; see mockInstallFragment's
	// doc for why its only real job is asserting `cat`. Its auth resolver,
	// resolveMockContainerAuth, is the one resolver in this file that never
	// returns ok=false: mock authenticates against no vendor, so there is
	// nothing to resolve (see that function's doc — this is a POSITIVE,
	// verified fact about mock, not a template for a real engine).
	// Deliberately its OWN case rather than falling to `default`: the
	// default arm's resolveAuth (noContainerAuth) exists precisely
	// for engines whose auth needs are UNKNOWN, and mock's are known, so it
	// does not belong there — and NOT added to composableEngines() (that
	// roster question is escalated, not decided here; see this change's own
	// report).
	//
	// transcriptStoreRel is deliberately "" — mock keeps NO transcripts at
	// all (internal/lm/backends' NewMock wires &NilSessionHistory{}, its own
	// doc: "mock keeps no transcripts"), so there is no native store root to
	// bind-mount; sessionStateMounts' `if c.engineSpec.transcriptStoreRel !=
	// ""` guard already treats "" as "nothing to mount for this engine",
	// the CORRECT reading here, not an oversight (contrast
	// TestEngineContainerSpecFor_EverySpecMapsATranscriptStore, whose "every
	// branch sets a non-empty root" invariant is scoped to
	// composableEngines()+default and explicitly carves mock out).
	case "mock":
		return engineContainerSpec{
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
		// unrecognized engine (a real, reachable path: an unrecognized OR
		// EMPTY engine name lands on this default spec — see the
		// engineContainerSpecFor("") call sites above) got the
		// user's ANTHROPIC_API_KEY/ANTHROPIC_AUTH_TOKEN passed through and
		// ~/.claude credentials copy-mounted into a FOREIGN engine's
		// container. Every non-claude engine must earn its OWN resolveAuth
		// for exactly this reason; the default must not hand
		// out Anthropic credentials to an engine nobody vetted. It now fails
		// closed (noContainerAuth) so an unmapped engine degrades
		// honestly instead of silently authenticating as claude.
		return engineContainerSpec{
			image:       defaultContainerImage,
			resolveAuth: noContainerAuth,
			authHint:    noContainerAuthHint,
			overlayDirs: defaultOverlayDirs,
			// The default spec's image/store-map default stays
			// claude-oriented (harmless metadata); only the AUTH default
			// changed — see the resolveAuth comment above.
			transcriptStoreRel: filepath.FromSlash(".claude/projects"),
		}
	}
}

// noContainerAuthHint is the default spec's degrade diagnostic AND the marker
// that identifies it: HasContainerAuth reads it back rather than comparing
// resolveAuth function values (Go func values are not comparable), so the
// "which engines have container auth" question is answered by the SAME table
// that resolves the auth — there is no second roster to drift out of sync.
const noContainerAuthHint = "no container auth is registered for this engine; register one in engineContainerSpecFor rather than inheriting the default"

// HasContainerAuth reports whether backend (a REGISTERED backend name) has a
// container-auth mapping — i.e. whether a `runtime: container` run of that
// engine can authenticate at all. False means the engine reaches
// engineContainerSpecFor's fail-closed default arm, so PrepareWorkspace would
// abort on the auth gate. Exported for config validation
// (operations.validateAgentAxes), which refuses the binding at write time
// rather than letting the launch discover it.
func HasContainerAuth(backend string) bool {
	return engineContainerSpecFor(backend).authHint != noContainerAuthHint
}

// ContainerAuthEngines lists the backend names that DO have a container-auth
// mapping, in the order engineContainerSpecFor declares them — the supported
// set a rejection message names. Pinned against HasContainerAuth by
// TestContainerAuthEngines_AllHaveAuth, so a spec added to the table without a
// listing here (or vice versa) fails loudly.
func ContainerAuthEngines() []string {
	return []string{"claude-code", "mock"}
}

// ContainerOverlayDirsFor returns a copy of engineContainerSpecFor(backend)'s
// overlayDirs — the project-relative managed-config directories a
// containerized run of backend shadows. Exported read-only so tests/arch's
// engine-layout gate can check this package's
// defaultOverlayDirs/mockOverlayDirs literals against each owning engine
// package's own ConfigDirName constant, the same import-cycle reasoning as
// ComposableEngines/CredentialSeedEngineNames above applies here too.
func ContainerOverlayDirsFor(backend string) []string {
	dirs := engineContainerSpecFor(backend).overlayDirs
	out := make([]string, len(dirs))
	copy(out, dirs)
	return out
}

// ContainerTranscriptStoreRelFor returns engineContainerSpecFor(backend)'s
// transcriptStoreRel — the engine's native transcript-store root, relative to
// the container HOME (empty when the engine keeps no transcripts, e.g.
// mock). Exported for the same engine-layout gate as
// ContainerOverlayDirsFor.
func ContainerTranscriptStoreRelFor(backend string) string {
	return engineContainerSpecFor(backend).transcriptStoreRel
}
