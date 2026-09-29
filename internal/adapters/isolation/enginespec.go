package isolation

import (
	"path/filepath"
	"sort"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// engineContainerSpec is this package's working form of ONE engine's
// container declaration (engine.ContainerSpec) — the backend-keyed knobs of
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
//   - declared: the engine declared a container story. The default spec
//     (declared false) fails the container gate closed.
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
// declares it (EngineFacts.Container, with its shipping policy in
// Distribution), read through the one facts accessor (enginefacts.go) by
// the engine NAME every caller here holds. Every engine the accessor knows
// has a declaration — a provided container OR a declared absence — so the
// fail-closed default below is reached only by a name nobody composed or an
// engine that SAID it has no container story, never by a forgotten row.
type engineContainerSpec struct {
	image              string
	engineInstall      []byte
	validate           string
	declared           bool
	overlayDirs        []string
	transcriptStoreRel string
}

// ctxloomCacheOverlayDir is ctxloom's own project-relative cache directory
// (the framed context file), shadowed for EVERY engine's containerized run:
// it is a fact about ctxloom, not about any engine, so no engine declares it.
var ctxloomCacheOverlayDir = filepath.FromSlash(".ctxloom/cache")

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
// story, gets the default spec, which fails the container gate CLOSED
// (declared false): its containerized run aborts rather than run an engine
// nobody vetted for a container. Config validation
// (operations.validateAgentAxes, via HasContainerStory) refuses `runtime:
// container` for such a backend at WRITE time; this arm is the last line for
// the paths that never went through a binding.
func engineContainerSpecFor(backend string) engineContainerSpec {
	if r, ok := engineContainerDeclared(backend); ok {
		if c, ok := r.container.Get(); ok {
			return specFromDeclaration(c)
		}
	}
	return engineContainerSpec{
		image:       defaultContainerImage,
		overlayDirs: []string{ctxloomCacheOverlayDir},
	}
}

// specFromDeclaration is the one place an engine's declared container story
// becomes this package's working spec. Everything engine-specific is read
// off the declaration; the only facts added are ctxloom's own (the image tag
// namespace and the cache overlay dir).
func specFromDeclaration(c engine.ContainerSpec) engineContainerSpec {
	spec := engineContainerSpec{
		image:              defaultContainerImage,
		engineInstall:      c.Install,
		validate:           c.ValidateCommand,
		overlayDirs:        append(append([]string{}, c.OverlayDirs...), ctxloomCacheOverlayDir),
		transcriptStoreRel: c.TranscriptStoreRel,
		declared:           true,
	}
	return spec
}

// The fail-closed default's degrade diagnostic and its fix.
const (
	noContainerHint   = "no container story is declared for this engine"
	noContainerRemedy = "declare Container in the engine's descriptor rather than inherit the default, or run this engine with `runtime: host`"
)

// HasContainerStory reports whether a `runtime: container` run of backend (a
// REGISTERED backend name) can run at all: whether the engine declares a
// container story. Credentials are not part of the question: they are the
// run's own (engine.Auth.Credentials), which every environment satisfies. False means the engine
// reaches engineContainerSpecFor's fail-closed default. A CAPABILITY question, answered whatever the
// engine's shipping policy. Exported for config validation
// (operations.validateAgentAxes), which refuses the binding at write time
// rather than letting the launch discover it.
func HasContainerStory(backend string) bool {
	r, ok := engineContainerDeclared(backend)
	if !ok {
		return false
	}
	_, ok = r.container.Get()
	return ok
}

// ContainerStoryEngines lists the backend names a user may bind `runtime:
// container` to — every engine that declares a container story and is
// OFFERED (DistributionDefault or DistributionOptIn), sorted. It is the
// supported set a rejection message names, so a test double is excluded
// even though HasContainerStory reports its capability: an opt-in engine is
// a legitimate thing to ask for by name, a double is not. (This is the one
// place the offered set differs from the default image set — the composable
// roster is stricter, DistributionDefault only, because composing is
// unasked-for and offering is not.)
func ContainerStoryEngines() []string {
	var names []string
	for name, r := range registeredEngineContainers() {
		if r.distribution == engine.DistributionTestOnly {
			continue
		}
		if _, ok := r.container.Get(); ok {
			names = append(names, name)
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
