package isolation

import (
	"path"
	"path/filepath"
	"sort"
	"sync"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// engineContainerSpec is this package's working form of ONE engine's
// container declaration (agent.EngineContainer) — the backend-keyed knobs of
// the container policies (Container and the worktree-in-container
// composition), which are otherwise engine-agnostic:
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
//     BUILD, never ships silently. nil = no known official installer
//     (the engine is excluded from composableEngines() and buildSources
//     returns nothing for it — no locally-buildable recipe exists until the
//     engine declares an installer fragment; see composeAgentContainerfile,
//     buildSources, composedIdentity).
//   - validate: the in-image command that proves the client is runnable (the
//     `<client> --version` build gate), used by the single-engine `--base-image`
//     overlay escape hatch (overlayContainerfile) — composition uses
//     engineInstall's OWN embedded validate step instead.
//   - resolveAuth: how the in-container engine authenticates (scoped env
//     passthrough and/or credential mounts into the fresh HOME), built from
//     the engine's declared agent.ContainerAuth by resolveDeclaredAuth. Takes
//     the run's host-side scratch dir too, a seam-signature remnant no
//     resolver writes under today.
//   - authHint: the degrade diagnostic when resolveAuth finds nothing — names
//     the engine's trigger var/credential source without leaking values.
//   - relocatedCredentialMounts: the credential FILE mount a run whose engine
//     home was RELOCATED (engine_home: session — MountEngineHome) needs over
//     the copy seeded into that home, so the engine keeps a credential it
//     can refresh in place. nil for an engine that authenticates against no
//     vendor or declares no credential files.
//   - overlayDirs: the project-relative managed-config DIRECTORIES ctxloom's
//     writers target under the run's cwd for this engine, shadowed by scratch
//     overlay mounts on the live-project mount so the HOST project stays clean
//     (see containerConfigOverlay; directories only — single-file overlays would
//     break the writers' atomic write+rename). The engine declares its own;
//     ctxloom's framed-context cache dir rides along for every engine.
//   - transcriptStoreRel: the engine's native transcript/session STORE ROOT,
//     relative to the container HOME — the bind target sessionStateMounts maps
//     the harp's persist/transcripts dir onto so in-container transcripts
//     survive teardown. The ROOT, never a leaf: the transcript file name is a
//     runtime-generated sessionID/uuid the host cannot pre-create, and the
//     container's fresh HOME already scopes the root to this one run. Resolved
//     against the CONTAINER home; an engine-home env override is deliberately
//     not consulted — the container axis never sets one. "" when the engine
//     keeps no transcripts.
//
// WHAT an engine's container story is, is not decided here. Each engine
// declares it on its own descriptor (hosting.Hosting.Container, with its
// shipping policy in Distribution), and internal/lm/backends pushes both —
// a provided container OR a declared absence — into this package for every
// engine it registers (RegisterEngineContainer). This package cannot import
// the registry (backends imports it) and every caller here holds a backend
// NAME, so a name-keyed table populated at registration is the only
// direction the wiring can run. The consequence is the invariant that
// matters: every registered engine has an entry, so the fail-closed default
// below is reached only by a name nobody registered or an engine that SAID
// it has no container story — never by a forgotten table row.
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

// ctxloomCacheOverlayDir is ctxloom's own project-relative cache directory
// (the framed context file), shadowed for EVERY engine's containerized run:
// it is a fact about ctxloom, not about any engine, so no engine declares it.
var ctxloomCacheOverlayDir = filepath.FromSlash(".ctxloom/cache")

// engineContainerRegistration is what the registry pushes per engine: the
// declaration (capability) and the shipping policy that decides which
// user-facing rosters it appears in.
type engineContainerRegistration struct {
	container    agent.Declared[agent.EngineContainer]
	distribution engine.Distribution
}

var (
	engineContainerMu sync.RWMutex
	engineContainers  = map[string]engineContainerRegistration{}
)

// RegisterEngineContainer installs engine's container declaration and
// shipping policy. Called from internal/lm/backends' Register for EVERY
// descriptor, whether the container story is provided or declared absent;
// re-registering a name replaces it, and an undecided (zero) declaration
// deletes the entry, so a test can unwind its synthetic engine.
func RegisterEngineContainer(engine string, container agent.Declared[agent.EngineContainer], distribution engine.Distribution) {
	engineContainerMu.Lock()
	defer engineContainerMu.Unlock()
	if !container.Decided() {
		delete(engineContainers, engine)
		return
	}
	engineContainers[engine] = engineContainerRegistration{container: container, distribution: distribution}
}

// engineContainerDeclared returns engine's registration and whether the
// engine is registered at all.
func engineContainerDeclared(engine string) (engineContainerRegistration, bool) {
	engineContainerMu.RLock()
	defer engineContainerMu.RUnlock()
	r, ok := engineContainers[engine]
	return r, ok
}

// registeredEngineContainers returns every registration, keyed by engine
// name — a snapshot for the roster filters below.
func registeredEngineContainers() map[string]engineContainerRegistration {
	engineContainerMu.RLock()
	defer engineContainerMu.RUnlock()
	out := make(map[string]engineContainerRegistration, len(engineContainers))
	for k, v := range engineContainers {
		out[k] = v
	}
	return out
}

// composableEngines is the deterministic set of engines whose single-engine
// agent image is worth building UNASKED, alphabetical: every engine that CAN
// be composed (declares an installer fragment) AND ships by default
// (DistributionDefault). Capability and policy, each read from where it is
// declared. It is a roster of NAMES, not a composition set — an agent image
// carries exactly one engine (composeAgentContainerfile), and a run composes
// its own backend's image whether or not that backend is listed here. An
// opt-in engine or a test double is therefore fully buildable and runnable
// in a container; it is only never pre-built or offered by name.
func composableEngines() []string {
	var names []string
	for name, r := range registeredEngineContainers() {
		c, ok := r.container.Get()
		if ok && c.Install != nil && r.distribution == engine.DistributionDefault {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// ComposableEngines exports composableEngines() read-only, for the CLI's
// engine-selection help and for tests/arch's roster gates, which cannot
// reach this package's unexported roster otherwise (isolation cannot import
// backends: backends already imports isolation).
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
// An unregistered (or empty) name, or an engine that declared NO container
// story, gets the default spec, whose AUTH fails closed (noContainerAuth): its
// containerized run aborts at PrepareWorkspace's auth gate rather than
// inheriting another engine's credentials. Config validation
// (operations.validateAgentAxes, via HasContainerAuth) refuses `runtime:
// container` for such a backend at WRITE time; this arm is the last line for
// the paths that never went through a binding.
func engineContainerSpecFor(backend string) engineContainerSpec {
	if r, ok := engineContainerDeclared(backend); ok {
		if c, ok := r.container.Get(); ok {
			return specFromDeclaration(backend, c)
		}
	}
	// This used to be a claude-oriented default that failed OPEN on
	// credentials, so any unrecognized engine got the user's ANTHROPIC_*
	// vars passed through and ~/.claude credentials mounted into a FOREIGN
	// engine's container. Every engine must earn its own auth by declaring
	// it; the default must not hand out anyone's credentials to an engine
	// nobody vetted. It fails closed (noContainerAuth) so an unmapped engine
	// degrades honestly instead of silently authenticating as another.
	return engineContainerSpec{
		image:       defaultContainerImage,
		resolveAuth: noContainerAuth,
		authHint:    noContainerAuthHint,
		overlayDirs: []string{ctxloomCacheOverlayDir},
	}
}

// specFromDeclaration is the one place an engine's declared container story
// becomes this package's working spec. Everything engine-specific is read
// off the declaration; the only facts added are ctxloom's own (the image tag
// namespace and the cache overlay dir). engine is the REGISTERED backend
// name, threaded through so relocatedCredentialMounts can read the SAME
// engine's credential-seed declaration (credentialSeedFor) rather than
// re-deriving a leaf name credentialSeed.Files already states.
func specFromDeclaration(engine string, c agent.EngineContainer) engineContainerSpec {
	spec := engineContainerSpec{
		image:              defaultContainerImage,
		engineInstall:      c.Install,
		validate:           c.ValidateCommand,
		overlayDirs:        append(append([]string{}, c.OverlayDirs...), ctxloomCacheOverlayDir),
		transcriptStoreRel: filepath.FromSlash(c.TranscriptStoreRel),
	}
	a, ok := c.Auth.Get()
	if !ok {
		spec.resolveAuth = noContainerAuth
		spec.authHint = noContainerAuthHint
		return spec
	}
	spec.resolveAuth = func(containerHome, _ string) (containerAuth, bool) {
		return resolveDeclaredAuth(a, containerHome)
	}
	spec.authHint = a.Hint
	if a.Vendorless != "" {
		spec.authHint = "unreachable: a vendorless engine's auth never fails to resolve (" + a.Vendorless + ")"
	}
	if len(a.CredentialFiles) > 0 {
		files := a.CredentialFiles
		spec.relocatedCredentialMounts = func(engineHome string) ([]Mount, bool) {
			return relocatedCredentialMounts(engine, files, engineHome)
		}
	}
	return spec
}

// relocatedCredentialMounts overlays each declared host credential FILE onto
// the RELOCATED engine home, at the SAME leaf the seeded copy landed at, with
// the mode the declaration gives it. ok=false when any host file is absent: a
// bind mount of a missing file would create a directory in its place, which
// is worse than the seeded copy alone.
//
// The seeded copy's leaf is whatever the engine's agent.CredentialSeed
// declares as SeedFile.DestName (credentialSeedFor, matched on the shared
// HostRelHome) — never re-derived from ContainerRelHome. ContainerRelHome
// describes the file's place in the container's UNRELOCATED $HOME layout
// (see credentialFileMounts), which shares a leaf with the seeded DestName
// only when the engine does not rename the file on seed; claude's two
// declarations happen to agree, but an engine whose descriptor renames the
// file would otherwise get a mount that misses the seeded copy and silently
// authenticate off a stale credential. Falling back to
// path.Base(ContainerRelHome) when no matching SeedFile is registered (no
// seed declared for this engine, or its HostRelHome does not appear in one)
// preserves today's coincidental-match behavior for that corner rather than
// refusing to mount at all — a provisioner redesign, not this fix, owns
// deciding whether that corner should exist.
func relocatedCredentialMounts(engine string, files []agent.CredentialFile, engineHome string) ([]Mount, bool) {
	home, err := hostHomeDir()
	if err != nil || home == "" {
		return nil, false
	}
	mounts := make([]Mount, 0, len(files))
	for _, f := range files {
		host := filepath.Join(home, filepath.FromSlash(f.HostRelHome))
		if !fileExists(host) {
			return nil, false
		}
		leaf, ok := seededLeafFor(engine, f.HostRelHome)
		if !ok {
			leaf = path.Base(f.ContainerRelHome)
		}
		mounts = append(mounts, Mount{
			Host:      host,
			Container: path.Join(engineHome, leaf),
			ReadOnly:  f.ReadOnly,
		})
	}
	return mounts, true
}

// seededLeafFor reads the leaf name engine's credential seed declares for the
// host file at hostRelHome (agent.SeedFile.DestName, matched by the
// HostRelHome the two declarations share) — the destination the file was
// ACTUALLY copied to under the relocated home, as opposed to any assumption
// drawn from the container-auth declaration alone. ok=false when engine has
// no registered seed, or none of its Files shares this HostRelHome.
func seededLeafFor(engine, hostRelHome string) (string, bool) {
	seed, ok := credentialSeedFor(engine)
	if !ok {
		return "", false
	}
	for _, f := range seed.Files {
		if f.HostRelHome == hostRelHome {
			return f.DestName, true
		}
	}
	return "", false
}

// noContainerAuthHint is the fail-closed default's degrade diagnostic.
const noContainerAuthHint = "no container auth is declared for this engine; its descriptor must provide Container.Auth rather than inherit the default"

// HasContainerAuth reports whether backend (a REGISTERED backend name) declares
// a container-auth plan — i.e. whether a `runtime: container` run of that
// engine can authenticate at all. False means the engine reaches
// engineContainerSpecFor's fail-closed default, so PrepareWorkspace would
// abort on the auth gate. A CAPABILITY question, answered whatever the
// engine's shipping policy. Exported for config validation
// (operations.validateAgentAxes), which refuses the binding at write time
// rather than letting the launch discover it.
func HasContainerAuth(backend string) bool {
	r, ok := engineContainerDeclared(backend)
	if !ok {
		return false
	}
	c, ok := r.container.Get()
	if !ok {
		return false
	}
	_, ok = c.Auth.Get()
	return ok
}

// ContainerAuthEngines lists the backend names a user may bind `runtime:
// container` to — every engine that declares a container-auth plan and is
// OFFERED (DistributionDefault or DistributionOptIn), sorted. It is the
// supported set a rejection message names, so a test double is excluded
// even though HasContainerAuth reports its capability: an opt-in engine is
// a legitimate thing to ask for by name, a double is not. (This is the one
// place the offered set differs from the default image set — the composable
// roster is stricter, DistributionDefault only, because composing is
// unasked-for and offering is not.)
func ContainerAuthEngines() []string {
	var names []string
	for name, r := range registeredEngineContainers() {
		if r.distribution == engine.DistributionTestOnly {
			continue
		}
		if c, ok := r.container.Get(); ok {
			if _, ok := c.Auth.Get(); ok {
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names
}

// ContainerOverlayDirsFor returns a copy of engineContainerSpecFor(backend)'s
// overlayDirs — the project-relative managed-config directories a
// containerized run of backend shadows. Exported read-only for tests/arch's
// engine-layout gate.
func ContainerOverlayDirsFor(backend string) []string {
	dirs := engineContainerSpecFor(backend).overlayDirs
	out := make([]string, len(dirs))
	copy(out, dirs)
	return out
}

// ContainerTranscriptStoreRelFor returns engineContainerSpecFor(backend)'s
// transcriptStoreRel — the engine's native transcript-store root, relative to
// the container HOME (empty when the engine keeps no transcripts). Exported
// for the same engine-layout gate as ContainerOverlayDirsFor.
func ContainerTranscriptStoreRelFor(backend string) string {
	return engineContainerSpecFor(backend).transcriptStoreRel
}
