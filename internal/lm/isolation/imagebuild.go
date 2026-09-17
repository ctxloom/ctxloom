package isolation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	containerfiles "github.com/ctxloom/ctxloom/container"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// imageBuildTimeout caps one on-the-fly agent-image build. The production
// builds are network-bound (npm / a vendor install script / an official-image pull)
// so the first build takes minutes; a hung build must still degrade rather than
// block the LLM forever.
const imageBuildTimeout = 10 * time.Minute

// resolveSelfExe is the seam over the running-binary check (overridable in
// tests, like hostHomeDir): it yields the path buildImage bakes into the image.
var resolveSelfExe = selfLinuxExe

// baseImageTagFor derives the local tag the shared base stage builds to from
// the base Containerfile's CONTENT; the agent stages FROM it via --build-arg
// BASE_IMAGE. Content-keyed on purpose: concurrent SESSIONS with different
// isolation_base_containerfile configs build DIFFERENT tags, so one session's
// agent stage can never FROM a base another session just tagged (a fixed
// :latest tag was exactly that cross-contamination), while identical content
// shares one tag — and the runtime's layer cache — as before. A content change
// lands on a fresh tag, which is simply absent and therefore built: the tag
// itself is the base's staleness gate, no separate label check.
func baseImageTagFor(content []byte) string {
	return "ctxloom-agent-base:" + baseContentHash(content)
}

// baseContentHash is the short base-Containerfile content digest shared by the
// base image tag and the provenance suffix (combineProvenance), so an agent
// image's provenance label names the base generation it was built on.
func baseContentHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])[:12]
}

// composedContentHash digests a resolved base's content TOGETHER WITH the
// ENGINE keyed onto it — the content-key for a COMPOSABLE spec's agent image
// tag and provenance suffix: a change to the base content, a devcontainer.json
// edit, OR a different engine produces a different hash, so any of the three
// lands on a fresh tag (simply absent, therefore built) through the SAME
// mechanism baseContentHash gives the stage-1 base tag alone.
func composedContentHash(content []byte, engine string) string {
	h := sha256.New()
	h.Write(content)
	h.Write([]byte{0})
	h.Write([]byte(engine))
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// composedImageTagFor is the shared image tag a COMPOSABLE spec's build
// resolves to: one tag per (ctxloom version, resolved base content, ENGINE) —
// the same triple across different projects and sessions shares the SAME tag
// and the runtime's layer cache. One engine per image, so the identity is a
// function of the engine alone and cannot shift when some other project binds
// a different set.
//
// The CTXLOOM VERSION is in the tag so images built by DIFFERENT ctxloom
// versions COEXIST. Keyed on content and engine alone, one tag was shared by
// every version: a newer ctxloom found the tag present, its provenance stale,
// and rebuilt OVER it — then switching back rebuilt again, because the tag the
// older one wanted now held the newer one's image. Two versions could not both
// hold an image. It is versionCommitKey and not the raw stamp because the
// stamp embeds a build timestamp; see versionCommitKey.
//
// The ENGINE IS IN THE TAG, not only in the hash. A hash-only tag made two
// images for different engines indistinguishable in `docker images`, so a
// wrong-engine image could not be spotted by looking — and image identity is
// content-addressed, meaning a wrong composition builds, runs, and is simply
// the wrong thing. Naming the engine makes that visible at a glance; naming
// the version does the same for "which ctxloom built this".
//
// An empty versionKey (an unstamped binary, which internal/cli's root gate
// refuses to run) is OMITTED rather than interpolated: a leading "-" is not a
// legal tag, so a degraded key must not be allowed to mint an unusable name.
func composedImageTagFor(content []byte, engine, versionKey string) string {
	tag := composedContentHash(content, engine)
	if versionKey != "" {
		tag = versionKey + "-" + tag
	}
	return "ctxloom-agent-" + engine + ":" + tag
}

// baseForIdentity resolves WHICH base stage a spec's local build would use
// — without building anything — for content-keying the composed tag/
// provenance: an explicit user Containerfile beats an auto-detected
// devcontainer base beats the embedded default, mirroring composableBuildSources'
// precedence exactly (kept in the ONE place callers share, so the two can
// never drift).
func baseForIdentity(baseContainerfile string, devBase *baseStage) *baseStage {
	if baseContainerfile != "" {
		return userBaseStage(baseContainerfile)
	}
	if devBase != nil {
		return devBase
	}
	return defaultBaseStage()
}

// composedIdentity resolves a COMPOSABLE spec's (image tag, provenance
// label) for the given base/engine configuration. ok=false when the spec
// isn't composable (no known engine fragment) or the resolved base's content
// can't be read — callers fall back to the spec's static image field and
// the legacy HostProvenanceDigest.
func composedIdentity(p engineContainerSpec, baseContainerfile string, devBase *baseStage, engine string) (image, provenance string, ok bool) {
	if p.engineInstall == nil {
		return "", "", false
	}
	content, err := baseForIdentity(baseContainerfile, devBase).content()
	if err != nil {
		return "", "", false
	}
	image = composedImageTagFor(content, engine, versionCommitKey(binaryVersion))
	if vk := hostVersionKey(); vk != "" {
		provenance = vk + "-" + composedContentHash(content, engine)
	}
	return image, provenance, true
}

// buildSource is one way to produce the agent image locally: the agent-stage
// Containerfile (a rendered overlay onto a client-shipped base, or the embedded
// install recipe), optionally preceded by a BASE stage, plus a description for
// degrade diagnostics.
type buildSource struct {
	desc          string
	containerfile []byte
	// base, when non-nil, is stage 1: a base image built first and tagged by
	// its content (baseImageTagFor), which the agent stage above FROMs via
	// --build-arg.
	base *baseStage
}

// baseStage describes the shared stage-1 base image build: a user-provided
// Containerfile on disk, an auto-detected project devcontainer (a build
// Dockerfile, or a synthetic FROM wrapping an "image:" ref), or the embedded
// default base.
type baseStage struct {
	desc          string
	path          string // Containerfile path on disk ("" = embedded content below)
	containerfile []byte // embedded content (used when path == "")
	// context overrides the build CONTEXT dir when it differs from path's own
	// directory (a devcontainer's "build.context", or a compose service's
	// build context) — "" means path's directory (today's default behaviour).
	context string
	// buildArgs are extra --build-arg entries threaded into the base build
	// (a devcontainer's "build.args"); nil for the embedded default / a plain
	// user Containerfile.
	buildArgs []string
	// kind marks an EXPLICITLY-adopted base ("user" | "devcontainer") whose
	// build failure is an explicit-request failure — see fromUserBase /
	// fromDevcontainerBase. "" (the embedded default) is never explicit.
	kind string
}

// baseStageKindUser and baseStageKindDevcontainer are the two "explicitly
// adopted" baseStage.kind values; see fromUserBase / fromDevcontainerBase.
const (
	baseStageKindUser         = "user"
	baseStageKindDevcontainer = "devcontainer"
)

// content returns the base stage's Containerfile bytes: the embedded content
// when path is unset, or a fresh read of the on-disk file otherwise (a user
// Containerfile, or an auto-detected devcontainer build.dockerfile).
func (b *baseStage) content() ([]byte, error) {
	if b.path == "" {
		return b.containerfile, nil
	}
	return os.ReadFile(b.path)
}

// defaultBaseStage is the embedded shared base (container/base/Containerfile).
func defaultBaseStage() *baseStage {
	return &baseStage{desc: "default base Containerfile", containerfile: containerfiles.Base()}
}

// userBaseStage is a user-provided base Containerfile on disk
// (isolation_base_containerfile / --base-containerfile).
func userBaseStage(path string) *baseStage {
	return &baseStage{desc: "user base Containerfile " + path, path: path, kind: baseStageKindUser}
}

// devcontainerImageStage wraps a devcontainer.json (or compose service)
// "image:" ref as a synthetic single-line FROM base — a BASE, not a finished
// agent image: composed engine fragments still layer on top of it (unlike the
// `--base-image` overlay escape hatch, which asserts the client is ALREADY
// present and never installs one).
func devcontainerImageStage(desc, ref string) *baseStage {
	return &baseStage{desc: desc, containerfile: []byte("FROM " + ref + "\n"), kind: baseStageKindDevcontainer}
}

// devcontainerBuildStage wraps a devcontainer.json (or compose service)
// "build:" Dockerfile as the base stage, threading its own context dir and
// build args through so its COPYs and ARGs resolve exactly as the editor's
// own `devcontainer build` would.
func devcontainerBuildStage(desc, dockerfile, contextDir string, args map[string]string) *baseStage {
	var buildArgs []string
	for k, v := range args {
		buildArgs = append(buildArgs, k+"="+v)
	}
	sort.Strings(buildArgs) // deterministic --build-arg ordering
	return &baseStage{desc: desc, path: dockerfile, context: contextDir, buildArgs: buildArgs, kind: baseStageKindDevcontainer}
}

// fromUserBase reports whether this build source builds on a user-CONFIGURED
// base Containerfile (isolation_base_containerfile) rather than an embedded,
// auto-detected, or official base. A failure of THIS source is an
// explicit-request failure: a silent fallthrough to a different base ships an
// image the user never asked for, so runEnsureImage records a finding instead
// of degrading quietly.
func (s buildSource) fromUserBase() bool {
	return s.base != nil && s.base.kind == baseStageKindUser
}

// fromDevcontainerBase reports whether this build source builds on the
// project's AUTO-DETECTED devcontainer.json — likewise an explicit-request
// failure (the human's own .devcontainer/ was auto-adopted; falling through
// to the embedded default silently produces a DIFFERENT environment than the
// one they develop in, the exact failure the devcontainer-base feature exists
// to prevent).
func (s buildSource) fromDevcontainerBase() bool {
	return s.base != nil && s.base.kind == baseStageKindDevcontainer
}

// staleRebuildFixIt is attached to the finding raised when a STALE image's
// refresh build fails and the run would otherwise launch the existing stale
// image (which, pre-entrypoint, can run as root). userBaseBuildFixIt is
// attached when an explicitly-configured base Containerfile fails to build;
// devcontainerBaseBuildFixIt when the auto-detected project devcontainer
// fails to build; devcontainerDetectFixIt when the devcontainer.json itself
// could not be resolved to a base at all (malformed JSON, an unresolvable
// dockerComposeFile).
const (
	// staleRebuildFixIt names no flag: running the stale image anyway is
	// exactly what is being refused (a pre-entrypoint image can start as
	// ROOT), so --degraded is no longer a way through. `runtime: host` is
	// offered as the deliberate, stated alternative for a user who would
	// rather run unsandboxed than fix the image — said out loud instead of
	// arrived at by a flag that means something else.
	staleRebuildFixIt = "check the build output above and reinstall/rebuild the agent image (`ctxloom container build`), or ask for a host run deliberately with `runtime: host`"
	// userBaseBuildFixIt / devcontainerBaseBuildFixIt name no flag. Falling
	// back to another build source is exactly what is being refused, so
	// --degraded is no longer a way through; each instead offers the way to
	// accept ctxloom's own base DELIBERATELY, by dropping the declaration.
	// That is the point of the ruling: the substitution may happen, but only
	// because the user said so, never because a flag about startup faults
	// happened to be set.
	userBaseBuildFixIt         = "fix the configured base Containerfile (isolation_base_containerfile) so it builds, or remove that setting to accept ctxloom's own base deliberately"
	devcontainerBaseBuildFixIt = "fix the project .devcontainer/devcontainer.json (or its build.dockerfile) so it builds, or opt out with isolation_devcontainer_base: false to accept ctxloom's own base deliberately"
	devcontainerDetectFixIt    = "fix the project .devcontainer/devcontainer.json (malformed JSON, or a dockerComposeFile with no resolvable service — set isolation_devcontainer_service), or opt out with isolation_devcontainer_base: false / --no-devcontainer-base"
	// noComposableEnginesFixIt is attached when isolation_engines resolves to
	// an empty set: every configured name was unknown or
	// non-composable, so the composed agent image would otherwise build
	// green with zero engine-install layers and fail every run.
	noComposableEnginesFixIt = "set isolation_engines to at least one supported, composable engine (see `ctxloom container build --help`), or leave it unset to compose every composable engine"
)

// buildSourcesOptions carries every input buildSources needs to order a
// spec's local-build sources: the CLI/config overrides plus the
// already-RESOLVED devcontainer base (detection happens once, in the caller —
// see resolveDevBase — so a detection failure can be handled per-caller:
// fatal-unless-degraded in runEnsureImage, a hard CLI error in
// BuildAgentImage, an advisory line in Diagnose).
type buildSourcesOptions struct {
	// baseOverride is --base-image: overlay ctxloom onto a base that ALREADY
	// ships the client, skipping any install. Wins outright.
	baseOverride string
	// baseContainerfile is isolation_base_containerfile / --base-containerfile:
	// an explicit user base. Beats auto-detection (locked decision 8).
	baseContainerfile string
	// devBase is the auto-detected project devcontainer base (nil = none
	// detected, or opted out) — see resolveDevBase.
	devBase *baseStage
	// engine is THIS agent's engine for a COMPOSABLE spec (p.engineInstall !=
	// nil); ignored for a non-composable spec. One engine, not a set: an agent
	// image carries exactly the engine that agent runs, so there is no
	// composition to resolve and no other vendor's installer to fail on.
	engine string
}

// buildSources orders a spec's local-build sources. An explicit base-IMAGE
// override wins outright (the caller asserts the client lives there). A
// COMPOSABLE spec (engineInstall != nil — every engine in
// composableEngines()) then builds the generated SINGLE-ENGINE Containerfile
// (composeAgentContainerfile) onto, in order: the explicit user base
// Containerfile, the auto-detected project devcontainer, and the embedded
// default base — precedence locked decision 8 (explicit beats auto-detect
// beats default). A non-composable spec (no known official-installer
// fragment yet — an unknown/unmapped backend, e.g. engineContainerSpecFor's
// `default` arm) has no local-build recipe at all: empty means the image
// cannot be built locally, and the caller must have a preexisting image or
// degrade. (A prior fix deleted the LEGACY officialImage/user-Containerfile/
// embedded-Containerfile fallback this used to fall through to: no spec,
// registered or hypothetical, ever populated those fields, so the fallback
// could never produce a source.)
func buildSources(p engineContainerSpec, opts buildSourcesOptions) []buildSource {
	if opts.baseOverride != "" {
		if p.validate == "" {
			// The overlay's only proof the base really ships the client is
			// the spec's validate command, and an unmapped backend has
			// none — the image then builds, tags and passes every ctxloom/
			// companion gate while the engine may be absent, surfacing only
			// as a run-time exec failure. The caller's assertion still wins
			// (this is the escape hatch), but it must not pass unremarked.
			clidiag.Warn("ctxloom",
				"no client-validation command is registered for this engine, so the agent image overlaid onto %q cannot be checked for a runnable client at build time; if the base does not ship one, every containerized run fails with the engine binary missing",
				opts.baseOverride)
		}
		return []buildSource{{
			desc:          "overlay on base image " + opts.baseOverride,
			containerfile: overlayContainerfile(opts.baseOverride, p.validate),
		}}
	}
	if p.engineInstall != nil {
		return composableBuildSources(p, opts)
	}
	return nil
}

// composableBuildSources builds the ordered source list for a COMPOSABLE
// spec: the generated SINGLE-ENGINE Containerfile layered onto each candidate
// base in precedence order.
func composableBuildSources(p engineContainerSpec, opts buildSourcesOptions) []buildSource {
	engine := opts.engine
	if engineContainerSpecFor(engine).engineInstall == nil {
		// composeAgentContainerfile with an unknown engine still renders a
		// complete, buildable Containerfile with ZERO engine-install layers —
		// it builds, tags, and passes every image gate, then fails every run
		// with the engine binary simply absent. Fail loud here instead of
		// silently building a green, empty image.
		strictness.Fail(strictness.ClassIsolation, noComposableEnginesFixIt,
			"no known engine-install recipe for engine %q; the agent image would contain no engine at all", engine)
		return nil
	}
	composed := composeAgentContainerfile(engine)
	enginesDesc := engine
	var out []buildSource
	if opts.baseContainerfile != "" {
		out = append(out, buildSource{
			desc:          fmt.Sprintf("agent stage (engine: %s) on the user base Containerfile %s", enginesDesc, opts.baseContainerfile),
			containerfile: composed,
			base:          userBaseStage(opts.baseContainerfile),
		})
	}
	if opts.devBase != nil {
		out = append(out, buildSource{
			desc:          fmt.Sprintf("agent stage (engine: %s) on the auto-detected project devcontainer (%s)", enginesDesc, opts.devBase.desc),
			containerfile: composed,
			base:          opts.devBase,
		})
	}
	out = append(out, buildSource{
		desc:          fmt.Sprintf("agent stage (engine: %s) on the embedded default base", enginesDesc),
		containerfile: composed,
		base:          defaultBaseStage(),
	})
	return out
}

// overlayContainerfile renders the generated OVERLAY Containerfile: the running
// ctxloom binary layered onto a base image that already ships the client CLI
// (the vendor's official image, or a user-provided base). The ctxloom
// entrypoint replaces whatever the base set (the isolation runtime supplies the
// full in-container argv; the entrypoint only remaps identity); the validate
// command gates the build so a base without a runnable client never ships as an
// agent image. The user/gosu layer ATTEMPTS stay best-effort (`|| true` — an
// arbitrary base may be non-Debian) but overlayUserGate then hard-verifies the
// identity machinery, so a base that cannot host the remap contract fails the
// BUILD with a fix-it instead of shipping an image whose engine runs as root.
func overlayContainerfile(baseImage, validate string) []byte {
	var b strings.Builder
	b.WriteString("# Generated by ctxloom: the running ctxloom binary overlaid onto a base\n")
	b.WriteString("# image that already ships the client CLI.\n")
	b.WriteString("FROM " + baseImage + "\n")
	if validate != "" {
		b.WriteString("RUN " + validate + "\n")
	}
	b.WriteString(overlayUserLayer + "\n")
	b.WriteString(overlayUserGate + "\n")
	b.WriteString("COPY ctxloom /usr/local/bin/ctxloom\n")
	b.WriteString("COPY companions/ /usr/local/bin/\n")
	b.WriteString("COPY ctxloom-entrypoint /usr/local/bin/ctxloom-entrypoint\n")
	b.WriteString("RUN chmod 0755 /usr/local/bin/ctxloom-entrypoint\n")
	b.WriteString("ENTRYPOINT [\"/usr/local/bin/ctxloom-entrypoint\"]\n")
	b.WriteString("ARG CTXLOOM_VERSION=\"\"\n")
	b.WriteString("ARG CTXLOOM_PROVENANCE=\"\"\n")
	b.WriteString("LABEL ctxloom.version=\"${CTXLOOM_VERSION}\"\n")
	b.WriteString("LABEL ctxloom.provenance=\"${CTXLOOM_PROVENANCE}\"\n")
	b.WriteString("RUN /usr/local/bin/ctxloom version\n")
	b.WriteString(companionGate + "\n")
	return []byte(b.String())
}

// baseContractLayer best-effort installs the coding-agent tool layer
// (git, ripgrep, curl, ca-certificates, unzip, jq) that composeAgentContainerfile's
// engine fragments and the entrypoint's runtime assume — the SAME contract
// container/base/Containerfile bakes for the embedded default base. On the
// default base this is a harmless no-op (already installed); it is
// LOAD-BEARING for an auto-detected devcontainer or user base that may not
// carry it. `|| true` mirrors overlayUserLayer's best-effort posture (an
// arbitrary base may be non-Debian) — a base that genuinely lacks these tools
// surfaces as a later, specific failure (an engine fragment's own install
// step, or a missing `git`/`rg` at RUN time), not a mysterious one here.
// WHERE A SYMBOL SERVER GOES, and it is a division rather than a preference.
// serena is a DRIVER over ~65 language-server adapters and carries no analysis
// of its own, so the thing that varies per image is not serena — it is the
// SERVER underneath it. The driver therefore belongs HERE, in the floor every
// base gets; the language server belongs to the image that serves that
// language (a Go image carries go + gopls, a Node image its own, and so on).
// Ruled 2026-09-10; the driver half is not installed at this layer yet —
// taskloom deceased-pretext tracks it.
//
// DO NOT ADD A LANGUAGE TOOLCHAIN HERE to stop serena refusing. Without Go,
// serena's Go support fails LOUDLY ("go: Go is not installed") rather than
// answering from a degraded index, and on an image that does not serve Go that
// refusal is the CORRECT answer — that image was never meant for Go work.
// Silencing it costs ~1.05 GB on every base to suppress an honest error, which
// is the exact trade that got serena reverted from the shipped base once
// already (f3cfee19).
const baseContractLayer = `RUN (command -v apt-get >/dev/null 2>&1 \
    && apt-get update \
    && apt-get install -y --no-install-recommends git ripgrep curl ca-certificates unzip jq strace \
    && rm -rf /var/lib/apt/lists/* || true)`

// composeAgentContainerfile generates the MULTI-ENGINE agent Containerfile
// (locked decisions 2-4): the base-contract fragment (best-
// effort tool layer for an ARBITRARY base) → the common scaffold (identity/
// entrypoint — the exact overlayUserLayer/overlayUserGate contract
// overlayContainerfile already uses) → THE one engine-install RUN layer →
// the version labels, the running ctxloom binary + companions, and the
// ctxloom/companion ABI gates. The engine sits ABOVE everything that changes
// per build, so a ctxloom rebuild never re-runs the vendor installer. An
// engine with no known fragment (engineContainerSpecFor(engine).engineInstall
// == nil) is silently skipped here, but that path is unreachable in practice:
// composableBuildSources, this function's only caller, already fails loudly
// (strictness.Fail) before calling it when the engine has no install
// fragment. This check is a second line of defense against that invariant
// breaking, not a warning site of its own.
func composeAgentContainerfile(engine string) []byte {
	var b strings.Builder
	b.WriteString("# Generated by ctxloom: a SINGLE-ENGINE agent image — one engine (via its\n")
	b.WriteString("# OWN official installer) plus the running ctxloom binary, layered onto the\n")
	b.WriteString("# resolved base via ARG BASE_IMAGE (the embedded default, a user\n")
	b.WriteString("# Containerfile, or the project's auto-detected .devcontainer/devcontainer.json).\n")
	b.WriteString("#\n")
	b.WriteString("# ONE ENGINE PER IMAGE, and that is the design rather than a simplification.\n")
	b.WriteString("# A composed multi-engine image made every run hostage to every OTHER\n")
	b.WriteString("# vendor's installer. Image identity is now a function of ONE engine, so it\n")
	b.WriteString("# caches across projects and cannot fail on a vendor this run does not use.\n")
	b.WriteString("#\n")
	b.WriteString("# engine: " + engine + "\n")
	b.WriteString("#\n")
	b.WriteString("# AUTH is NOT baked in — it crosses at RUN time, chosen by the container\n")
	b.WriteString("# policy per backend. This image ships no secrets.\n")
	b.WriteString("ARG BASE_IMAGE=ctxloom-agent-base:latest\n")
	b.WriteString("FROM ${BASE_IMAGE}\n")
	b.WriteString(baseContractLayer + "\n")
	b.WriteString(overlayUserLayer + "\n")
	b.WriteString(overlayUserGate + "\n")
	b.WriteString("COPY ctxloom-entrypoint /usr/local/bin/ctxloom-entrypoint\n")
	b.WriteString("RUN chmod 0755 /usr/local/bin/ctxloom-entrypoint\n")
	b.WriteString("ENTRYPOINT [\"/usr/local/bin/ctxloom-entrypoint\"]\n")
	// LAYER ORDER IS LOAD-BEARING: [base tooling] -> [engine install] -> [ctxloom
	// binary + companions]. Docker invalidates every layer after a changed one,
	// and the ctxloom binary changes on essentially every build while an engine
	// installer almost never does — so the binary goes LAST and a ctxloom rebuild
	// invalidates only a thin COPY.
	//
	// THE VERSION LABELS MUST STAY BELOW THE ENGINE INSTALL. They interpolate ARG
	// CTXLOOM_VERSION, which changes on every build; sited ABOVE the engine (where
	// they were until 2026-08-25) they invalidated the engine layer every single
	// time, re-running the vendor installer on every ctxloom change. That is the
	// path that killed a live probe cell when one of those installers hit
	// GitHub's anonymous API quota.
	if frag := engineContainerSpecFor(engine).engineInstall; frag != nil {
		b.WriteString("# engine: " + engine + "\n")
		b.Write(frag)
	}
	b.WriteString("ARG CTXLOOM_VERSION=\"\"\n")
	b.WriteString("ARG CTXLOOM_PROVENANCE=\"\"\n")
	b.WriteString("LABEL ctxloom.version=\"${CTXLOOM_VERSION}\"\n")
	b.WriteString("LABEL ctxloom.provenance=\"${CTXLOOM_PROVENANCE}\"\n")
	b.WriteString("LABEL ctxloom.engine=\"" + engine + "\"\n")
	b.WriteString("COPY ctxloom /usr/local/bin/ctxloom\n")
	b.WriteString("COPY companions/ /usr/local/bin/\n")
	b.WriteString("RUN /usr/local/bin/ctxloom version\n")
	b.WriteString(companionGate + "\n")
	return []byte(b.String())
}

// overlayUserLayer creates the generic ctxloom user (1000:1000, remapped at
// container start via PUID/PGID) on a base ctxloom does not author: useradd
// with an adduser (busybox/alpine) fallback, gosu install attempted only where
// apt exists. The ATTEMPTS are best-effort (an arbitrary base brings an
// arbitrary package manager); overlayUserGate below hard-verifies the result.
const overlayUserLayer = `RUN (userdel -r node 2>/dev/null || true) \
    && (groupadd -o -g 1000 ctxloom 2>/dev/null || addgroup -g 1000 ctxloom 2>/dev/null || true) \
    && (useradd -o -m -u 1000 -g 1000 -d /home/ctxloom -s /bin/sh ctxloom 2>/dev/null \
        || adduser -D -u 1000 -G ctxloom -h /home/ctxloom -s /bin/sh ctxloom 2>/dev/null || true) \
    && mkdir -p /home/ctxloom \
    && (command -v apt-get >/dev/null 2>&1 \
        && apt-get update && apt-get install -y --no-install-recommends gosu \
        && rm -rf /var/lib/apt/lists/* || true)`

// overlayUserGate FAILS the build when the base could not grow the identity
// machinery the entrypoint's remap contract needs: the ctxloom user itself,
// plus a privilege-drop path — setpriv (numeric ids, immune to a failed
// remap) or gosu with usermod/groupmod (gosu drops to the NAMED user, so it
// needs a working remap). Without this gate the image shipped and the engine
// ran as ROOT (or an un-remapped 1000:1000) behind a buried in-container
// warning — a wrong-identity container that STARTS is invisible to the fatal
// launch gate, so the BUILD is where the fault must surface (with a fix-it).
const overlayUserGate = `RUN id ctxloom >/dev/null 2>&1 \
    || { echo "ctxloom: this base cannot host the ctxloom user (no useradd/adduser); install one, or create user ctxloom (uid 1000, home /home/ctxloom) in the base" >&2; exit 1; }; \
    command -v setpriv >/dev/null 2>&1 \
    || { command -v gosu >/dev/null 2>&1 && command -v usermod >/dev/null 2>&1 && command -v groupmod >/dev/null 2>&1; } \
    || { echo "ctxloom: this base has no privilege-drop path for the identity remap (need setpriv, or gosu plus usermod/groupmod); install util-linux (setpriv) or gosu and shadow in the base" >&2; exit 1; }`

// companionBinaries are the ctxloom-family companions mirrored from the host
// into every locally-built agent image alongside ctxloom itself: the project's
// managed config references them inside the container (taskloom's task-store
// MCP server, ltk's pre-tool command gate, reprise's pre-commit gate), so an
// image without them turns those surfaces into in-container launch failures.
// Mirroring the HOST's binaries keeps versions consistent with the launching
// environment and needs no release artifacts; a companion absent on the host is
// skipped — the image still builds, just without it.
var companionBinaries = []string{"taskloom", "ltk", "reprise"}

// companionGate is the in-image ABI gate for whichever companions shipped: each
// present binary must RUN on this base (`--version`); one that cannot (e.g. a
// host binary dynamically linked against a newer glibc than the base ships) is
// DROPPED with a warning rather than failing the build — companions are
// auxiliary, and one incompatible tool must not block the whole agent image
// (CLAUDE.md: partial success is success). Unlike ctxloom's own gate, which
// fails the build: the image is useless without a runnable ctxloom. Rendered
// from companionBinaries so an added companion can never silently miss the gate.
var companionGate = companionGateFor(companionBinaries)

// companionGateFor renders the gate's shell loop over the given binary names.
func companionGateFor(names []string) string {
	return `RUN set -e; for b in ` + strings.Join(names, " ") + `; do \
        if command -v "$b" >/dev/null && ! "$b" --version; then \
            echo "warning: companion $b cannot run on this base (ABI mismatch); dropping it from the image" >&2; \
            rm -f "/usr/local/bin/$b"; \
        fi; \
    done`
}

// stageCompanions populates <contextDir>/companions with every companion binary
// found on the host PATH. The directory always exists — the agent stages'
// `COPY companions/` requires it even when empty — and a missing companion
// warns and is skipped (CLAUDE.md fault tolerance: the image builds without
// it); a copy failure of a FOUND binary errors, since shipping a silently
// truncated tool would be worse than no image.
func stageCompanions(contextDir string) error {
	dir := filepath.Join(contextDir, "companions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("companions build context: %w", err)
	}
	for _, name := range companionBinaries {
		src, err := companionLookPath(name)
		if err != nil {
			clidiag.Warn("ctxloom", "companion %s not on PATH; the agent image builds without it", name)
			continue
		}
		if err := copyExecutable(src, filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("companions build context: stage %s: %w", name, err)
		}
	}
	return nil
}

// binaryVersion is the running binary's version stamp, injected by the CLI at
// startup (isolation cannot import internal/cli — the dependency runs the other
// way). It is baked into built images as the ctxloom.version label, and it IS
// the staleness signal: versionCommitKey keys the image TAG and
// versionProvenanceKey keys the ctxloom.provenance label.
//
// It replaced a content digest over the running ctxloom and its companions.
// That digest changed on every `just build`, so every agent image read as
// stale and was rebuilt even when nothing meaningful had changed. An
// uncommitted change is still caught — the stamp carries a tracked-dirty
// marker, and versionProvenanceKey turns that into a per-build key.
var binaryVersion string

// SetBinaryVersion injects the running binary's version stamp. Called once by
// the CLI at startup. Everything downstream of the image-staleness gate reads
// it, so an empty value disables that gate loudly (hostVersionKey).
func SetBinaryVersion(v string) { binaryVersion = v }

const provenanceLabel = "ctxloom.provenance"

// HostProvenanceDigest returns the provenance label an agent image built NOW —
// by this ctxloom, on the given base Containerfile config ("" = the embedded
// default) — would carry: this build's version key (ctxloom's own version plus
// the staged companions' versions — see hostVersionKey), suffixed with the base
// config's content hash. It is the STALENESS SIGNAL: a new ctxloom version, an
// uncommitted (tracked-dirty) rebuild, an updated companion, or a changed base
// config changes it, and ensureImage rebuilds. Empty when this binary carries no usable stamp or
// the base config can't be read — the check then disables rather than churn.
// Exported so the build tooling (`ctxloom container provenance`) can stamp a
// matching label.
func HostProvenanceDigest(baseContainerfile string) string {
	return combineProvenance(hostVersionKey(), baseContainerfile)
}

// hostVersionKey is the running binary's provenance key, shared by
// HostProvenanceDigest and composedIdentity's engine-aware suffix. It is empty
// only for a binary carrying no usable stamp — which internal/cli's root gate
// refuses to run — and that emptiness is ANNOUNCED, because a check that
// silently stops checking is indistinguishable from one that passed.
//
// It keys on EVERYTHING the image bakes, not just ctxloom: the companion half
// (companionVersionKey) is what makes a new ltk/taskloom/reprise invalidate an
// image the way a new ctxloom does. The companion digest is appended
// unconditionally — including when no companion is installed at all — so
// "no companions" is a stated state rather than a missing suffix.
func hostVersionKey() string {
	key := versionProvenanceKey(binaryVersion)
	if key == "" {
		warnProvenanceDisabled(binaryVersion)
		return ""
	}
	return key + companionKeySeparator + companionVersionKey()
}

// combineProvenance suffixes the version key with the base-config content
// hash, so the ONE existing staleness gate (the ctxloom.provenance label vs
// imageStale) also rebuilds an agent image whose base Containerfile config
// changed — no parallel staleness mechanism. The suffix is baseContentHash,
// i.e. the baseImageTagFor generation the image rode on. Either half unknown
// yields "" — an untrustable key disables the check rather than forcing a
// wrong rebuild.
func combineProvenance(versionKey, baseContainerfile string) string {
	if versionKey == "" {
		return ""
	}
	content, err := baseContent(baseContainerfile)
	if err != nil {
		return ""
	}
	return versionKey + "-" + baseContentHash(content)
}

// baseContent resolves the base Containerfile content a build on this config
// would layer the agent stage onto: the user-provided file
// (isolation_base_containerfile), or the embedded default.
func baseContent(baseContainerfile string) ([]byte, error) {
	if baseContainerfile == "" {
		return containerfiles.Base(), nil
	}
	return os.ReadFile(baseContainerfile)
}

// warnProvenanceDisabled names the capability lost when this build cannot be
// keyed. Emitted at most once per process.
//
// The degrade is ANNOUNCED rather than silent, and it does not fall back to
// rebuilding: imageRunsAsIs turns the staleness comparison off entirely on an
// empty key, and `ctxloom container provenance` prints the empty key and exits
// 0. A check that silently stops checking is indistinguishable from one that
// passed.
func warnProvenanceDisabled(stamp string) {
	clidiag.WarnOnce("ctxloom", "this ctxloom carries no usable version stamp (%q); the container image-staleness check is DISABLED, so an agent image built by an older ctxloom will be run as-is", stamp)
}

// imageEnsureFlight is one in-flight ensureImage outcome: err is set before
// done closes, and followers read it only after <-done.
type imageEnsureFlight struct {
	done chan struct{}
	err  error
}

// imageEnsureFlights dedupes CONCURRENT ensureImage calls per (runtime binary,
// image tag) — the sharedFSResults keying: a delegated fan-out (agent_run)
// drives many members through the same tag from parallel goroutines, and N racing builds
// of one tag waste N-1 multi-minute builds and can untag each other mid-build,
// flaking another member's post-build presence recheck. Followers share the
// in-flight leader's outcome. NOTHING outlives the flight (unlike
// sharedFSResults): a present, current image re-checks cheaply, and a
// transient build failure must stay retryable in a long-lived server process
// (mcp/acp) instead of pinning every later run degraded.
var (
	imageEnsureMu      sync.Mutex
	imageEnsureFlights = map[string]*imageEnsureFlight{}
)

// ensureImage is the image half of the container degrade gate, used by run and
// a delegated (agent_run) fan-out alike. Concurrent callers of one tag share a single
// flight (imageEnsureFlights) — one build per tag, every member getting its
// outcome; the work itself is runEnsureImage.
func (c Container) ensureImage(ctx context.Context) error {
	key := c.runtime.Binary() + "|" + c.image
	imageEnsureMu.Lock()
	if f, ok := imageEnsureFlights[key]; ok {
		imageEnsureMu.Unlock()
		select {
		case <-f.done:
			return f.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f := &imageEnsureFlight{done: make(chan struct{})}
	imageEnsureFlights[key] = f
	imageEnsureMu.Unlock()

	f.err = c.runEnsureImage(ctx)
	imageEnsureMu.Lock()
	delete(imageEnsureFlights, key)
	imageEnsureMu.Unlock()
	close(f.done)
	return f.err
}

// runEnsureImage performs one un-deduped ensure. Present and current → run.
// Absent (or present but STALE — its baked ctxloom/companion binaries or base
// Containerfile config no longer match the host's) and locally buildable →
// build (official-image overlay first, then the install Containerfile), with
// the RUNNING static ctxloom binary layered in — no go toolchain, no ctxloom
// release needed. A failed REBUILD of a stale-but-present image still LAUNCHES
// the stale image (returns nil, so the container axis is never taken down), but
// records a fatal ClassIsolation finding: a stale, pre-entrypoint image can run
// as ROOT, so strict mode aborts before spawning it while --degraded runs it
// as-is. A failed build from an EXPLICITLY-configured base Containerfile
// likewise records a finding rather than silently substituting another base.
// Anything else (image absent and unbuildable, or a hard build failure) errors,
// so the caller degrades down the chain — a fatal finding (ClassIsolation) the
// choke owner aborts on unless --degraded.
func (c Container) runEnsureImage(ctx context.Context) error {
	sources, devBase, devErr := c.containerBuildSources("")
	present := c.imagePresent(ctx)
	wantProvenance := c.provenanceFor(devBase)
	if c.imageRunsAsIs(ctx, present, sources, wantProvenance) {
		return nil
	}
	if len(sources) == 0 {
		return fmt.Errorf("container image %q is not present (no local build recipe for this engine; provide the image, or configure isolation_images)", c.image)
	}
	selfExe, err := resolveSelfExe()
	if err != nil {
		if present {
			// This used to return nil here with NO warning and NO
			// finding at all — the isolation boundary silently degraded to a
			// stale, possibly pre-entrypoint (root-running) image. Route the
			// same fail-loud finding the parallel "rebuild attempted and
			// failed" branch below already uses for the identical outcome.
			strictness.FailAlways(strictness.ClassIsolation, staleRebuildFixIt,
				"refusing to run container image %q: it is STALE and cannot be rebuilt from this binary (%v), and a stale image predating the identity entrypoint can start as ROOT", c.image, err)
			// Still nil, NOT an error: an error here makes the caller degrade
			// down the chain to the HOST, which is the bypass this audit
			// removed. The non-degradable finding above is what stops the run.
			return nil
		}
		return fmt.Errorf("container image %q is not present and cannot be built from this binary: %w", c.image, err)
	}
	if present {
		clidiag.Warn("ctxloom", "container image %q was built by a different ctxloom version (or from different base Containerfile/devcontainer/engine-set config) than is running now; rebuilding it", c.image)
	} else {
		clidiag.Warn("ctxloom", "container image %q not found; building it locally (first run — this may take a few minutes)", c.image)
	}
	if devErr != nil {
		// The project's own .devcontainer/devcontainer.json was auto-adopted
		// but could not be resolved to a base (malformed JSON, an
		// unresolvable dockerComposeFile). Building without it produces a
		// DIFFERENT environment than the human develops in — the exact failure
		// the devcontainer-base feature exists to prevent.
		//
		// NON-DEGRADABLE, on the same ruling as recordBuildSourceFailure: a
		// devcontainer this project ships is a DECLARATION, and ctxloom cannot
		// read it to find out whether what it declares matters. A
		// devcontainer.json that is PRESENT but unreadable is refused on the
		// same terms as one that fails to build — "I could not parse it" is not
		// evidence that it was unimportant. A project with no devcontainer at
		// all declares nothing and never reaches here.
		strictness.FailAlways(strictness.ClassIsolation, devcontainerDetectFixIt,
			"refusing to build the agent image without this project's own devcontainer: auto-detection failed (%v), and ctxloom cannot read it to know what it provides", devErr)
	}
	lastErr := c.buildFirstWorkingSource(ctx, sources, selfExe, wantProvenance)
	if lastErr == nil {
		return nil
	}
	if present {
		// A stale, pre-entrypoint image can run as ROOT, so shipping it is a
		// security-relevant downgrade of the requested isolation. Refused in
		// BOTH modes: the harm is done by launching it, and --degraded means "a
		// thinner run", never "a container that might not confine anything".
		//
		// Returns nil rather than an error for the same reason as the
		// unbuildable arm above: an error makes the caller degrade down the
		// chain to the unsandboxed HOST, which is strictly worse than the stale
		// image. The non-degradable finding is what stops the run; the return
		// value only decides WHICH wrong thing we would otherwise fall to.
		strictness.FailAlways(strictness.ClassIsolation, staleRebuildFixIt,
			"refusing to run container image %q: its rebuild failed (%v) and the existing image is STALE (its baked ctxloom/companion binaries or base config are outdated), and a stale image predating the identity entrypoint can start as ROOT", c.image, lastErr)
		return nil
	}
	return fmt.Errorf("local build of container image %q failed: %w", c.image, lastErr)
}

// imageRunsAsIs reports that the image already present needs no build. Two
// cases qualify, and only two:
//
//   - no local build recipe at all (a user-owned isolation_images override),
//     which is never inspected or rebuilt — the user owns its lifecycle; and
//   - a locally-buildable image whose provenance can be VERIFIED and matches.
//
// The second clause's insistence on a NON-EMPTY wanted digest is load-bearing:
// an unresolvable host binary or unreadable base Containerfile yields "", and
// treating "cannot tell" as "current" accepted any present image, however old,
// while putting the unresolvable-binary case beyond the reach of the fail-loud
// branch in runEnsureImage that exists to report exactly it.
func (c Container) imageRunsAsIs(ctx context.Context, present bool, sources []buildSource, wantProvenance string) bool {
	if !present {
		return false
	}
	if len(sources) == 0 {
		return true
	}
	return wantProvenance != "" && !imageStale(c.imageLabels(ctx), wantProvenance)
}

// buildFirstWorkingSource walks the ordered build sources and returns nil on
// the first that produces a PRESENT image, or the last failure when none does.
// A build that reports success but leaves the tag absent counts as a failure —
// a green build whose image nobody can run is this project's characteristic
// silent no-op.
func (c Container) buildFirstWorkingSource(ctx context.Context, sources []buildSource, selfExe, wantProvenance string) error {
	var lastErr error
	for _, src := range sources {
		err := buildFromSource(ctx, c.runtime, c.image, src, selfExe, wantProvenance, false, nil)
		if err == nil && !c.imagePresent(ctx) {
			err = fmt.Errorf("image %q is still absent after a build via the %s", c.image, src.desc)
		}
		if err == nil {
			return nil
		}
		recordBuildSourceFailure(src, err)
		lastErr = err
	}
	return lastErr
}

// recordBuildSourceFailure reports one failed build source at the severity its
// PROVENANCE earns. A base the user CONFIGURED, or the project's own
// auto-detected devcontainer, is a DECLARATION, not a preference — so a build
// that cannot use it refuses in BOTH modes rather than shipping an image built
// on some other base. Every other source (ctxloom's own embedded default) is a
// plain warn-and-continue: nothing was declared, so nothing is being
// substituted.
//
// WHY THIS REFUSES, given that nothing here is a security boundary (ruled
// 2026-09-15). The audit's usual test — does LAUNCHING cause the harm? — comes
// back NO for this site: the container is still built, still runs the ctxloom
// entrypoint, still drops privileges, and nothing is lost. The ruling turns on
// a different point. CTXLOOM CANNOT READ THE CONTAINERFILE. It cannot know
// whether the declared base carries a hardened user, a pinned toolchain, a
// seccomp profile, or nothing at all — so substituting it silently is the
// program asserting knowledge it does not have. That is what is refused: not a
// measured harm, but an unmeasurable one being treated as absent.
//
// It is therefore UNCONDITIONAL. The deciding fact lives in bytes ctxloom
// cannot interpret, so there is no predicate to condition on — which is
// precisely why this is the simplest conversion of the set rather than the
// hardest.
//
// Accepted cost, recorded: a project whose configured base is genuinely a
// nice-to-have now fails a --degraded run that previously worked. The remedy is
// one line of config (drop the declaration), and it is named in the fix-it.
//
// The build loop still walks to the next source — the finding is what stops the
// run, exactly as in runEnsureImage's stale arms. Returning early here would
// only change WHICH wrong thing we would fall to if the gate were ever removed.
func recordBuildSourceFailure(src buildSource, err error) {
	switch {
	case src.fromUserBase():
		strictness.FailAlways(strictness.ClassIsolation, userBaseBuildFixIt,
			"refusing to build the agent image on a base you did not declare: the configured base Containerfile (%s) failed to build (%v), and ctxloom cannot know what that base provides, so it will not silently substitute another", src.desc, err)
	case src.fromDevcontainerBase():
		strictness.FailAlways(strictness.ClassIsolation, devcontainerBaseBuildFixIt,
			"refusing to build the agent image on a base this project did not declare: the auto-detected project devcontainer (%s) failed to build (%v), and substituting another would give this agent a different environment than the one you develop in", src.desc, err)
	default:
		clidiag.Warn("ctxloom", "agent image build (%s) failed: %v", src.desc, err)
	}
}

// imageStale reports whether a PRESENT, locally-buildable image's baked
// binaries no longer match the host's — its ctxloom.provenance label differs
// from the digest of the ctxloom+companions this build would stage now. An
// empty wanted digest (unresolvable host binary) disables the check; a missing
// label (an image predating provenance) counts as stale — it rebuilds once and
// gains the label.
func imageStale(labels map[string]string, wantProvenance string) bool {
	if wantProvenance == "" {
		return false
	}
	return labels[provenanceLabel] != wantProvenance
}

// imageLabels fetches the image's config labels. Best-effort: any inspect
// error yields nil (which, with a known wanted provenance, reads as stale — a
// rebuild is the safe recovery for an uninspectable image).
func (c Container) imageLabels(ctx context.Context) map[string]string {
	if c.runtime.Binary() == "" {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, c.runtime.Binary(), "image", "inspect", c.image, "--format", "{{json .Config.Labels}}").Output()
	if err != nil {
		return nil
	}
	var labels map[string]string
	if err := json.Unmarshal(bytes.TrimSpace(out), &labels); err != nil {
		return nil
	}
	return labels
}

// imageIdentity is the slice of an image's config the run-as-is identity
// contract check reads: which entrypoint governs PID 1, and the baked USER.
type imageIdentity struct {
	Entrypoint []string `json:"Entrypoint"`
	User       string   `json:"User"`
}

// imageIdentityConfig inspects the image's config for the identity contract
// check (checkRunAsIsIdentity). Unlike imageLabels it ERRORS on an
// uninspectable image: an unverifiable identity must fail loud, not read as
// "fine".
func (c Container) imageIdentityConfig(ctx context.Context) (imageIdentity, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, c.runtime.Binary(), "image", "inspect", c.image, "--format", "{{json .Config}}").Output()
	if err != nil {
		return imageIdentity{}, fmt.Errorf("inspect image config: %w", err)
	}
	var id imageIdentity
	if err := json.Unmarshal(bytes.TrimSpace(out), &id); err != nil {
		return imageIdentity{}, fmt.Errorf("parse image config: %w", err)
	}
	return id, nil
}

// buildFromSource executes one build source: the base stage first when the
// source has one (tagged by content via buildBaseImage, handed to the agent
// stage via --build-arg BASE_IMAGE), then the agent/overlay stage with the
// running ctxloom binary in its context. `fresh` pulls + skips cache on stages
// whose FROM is an external image; the agent stage over a just-built local
// base never --pulls (the tag exists only locally) but still skips cache so
// the client install re-runs. `provenance` is the PRECOMPUTED provenance
// label (HostProvenanceDigest for a legacy spec, composedIdentity's
// engine-aware digest for a composable one) — the caller computes it once so
// the stamped label always equals what the caller's own staleness check used
// for the same configuration (a mismatch would re-flag the image stale on
// every run).
func buildFromSource(ctx context.Context, rt Runtime, image string, src buildSource, selfExe, provenance string, fresh bool, output io.Writer) error {
	var buildArgs []string
	if src.base != nil {
		baseTag, err := buildBaseImage(ctx, rt, src.base, fresh, output)
		if err != nil {
			return fmt.Errorf("base image (%s): %w", src.base.desc, err)
		}
		buildArgs = append(buildArgs, "BASE_IMAGE="+baseTag)
	}
	// Stamp the diagnostic version label and — the staleness signal — the
	// precomputed provenance digest (empty when unknown — dev seams).
	buildArgs = append(buildArgs, "CTXLOOM_VERSION="+binaryVersion)
	buildArgs = append(buildArgs, "CTXLOOM_PROVENANCE="+provenance)
	return buildImage(ctx, rt, image, src.containerfile, selfExe, buildFlags{
		pull:      fresh && src.base == nil,
		noCache:   fresh,
		buildArgs: buildArgs,
	}, output)
}

// buildBaseImage builds the stage-1 base image and returns the content-keyed
// tag it built (baseImageTagFor). A user-provided or auto-detected-devcontainer
// Containerfile builds with ITS OWN context dir (base.context when set — a
// devcontainer's build.context, or a compose service's build context — else
// the Containerfile's own directory, so its COPYs resolve) plus any extra
// --build-arg entries (base.buildArgs, a devcontainer's build.args); the
// embedded default (and a synthetic "image:" FROM base) build from a scratch
// context.
func buildBaseImage(ctx context.Context, rt Runtime, base *baseStage, fresh bool, output io.Writer) (string, error) {
	flags := buildFlags{pull: fresh, noCache: fresh, buildArgs: append([]string(nil), base.buildArgs...)}
	if base.path != "" {
		abs, err := filepath.Abs(base.path)
		if err != nil {
			return "", fmt.Errorf("base containerfile: %w", err)
		}
		// Through the accessor, not a second hand-rolled read: content() is
		// the ONE place a base stage's bytes are resolved, so the tag this
		// derives can never be keyed off a different notion of "the base's
		// content" than composedIdentity's.
		content, err := base.content()
		if err != nil {
			return "", fmt.Errorf("base containerfile: %w", err)
		}
		tag := baseImageTagFor(content)
		contextDir := filepath.Dir(abs)
		if base.context != "" {
			contextDir = base.context
		}
		return tag, runImageBuild(ctx, rt, tag, abs, contextDir, flags, output)
	}

	dir, err := os.MkdirTemp("", "ctxloom-imgbase-")
	if err != nil {
		// dir is normally "" here (MkdirTemp itself failed) — this is
		// defensive against a mutation-testing mutant that flips this check
		// and discards a dir MkdirTemp actually created, which would
		// otherwise leak it under the OS temp dir with no reference left
		// anywhere to remove it (see container.go's prepareContainerScratch
		// for the same hardening).
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("base build context: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	file := filepath.Join(dir, "Containerfile")
	if err := os.WriteFile(file, base.containerfile, 0o644); err != nil {
		return "", fmt.Errorf("base build context: %w", err)
	}
	tag := baseImageTagFor(base.containerfile)
	return tag, runImageBuild(ctx, rt, tag, file, dir, flags, output)
}

// ImageBuildOptions parameterize an explicit agent-image build
// (`ctxloom container build`).
type ImageBuildOptions struct {
	// BaseImage overlays ctxloom onto this user-chosen base — which must
	// already ship the client CLI — instead of the spec's build sources.
	BaseImage string
	// BaseContainerfile builds the shared base stage from this user-provided
	// Containerfile instead of the embedded default; the engine's agent stage
	// layers on top. Mutually exclusive with BaseImage. Beats devcontainer
	// auto-detection (locked decision 8).
	BaseContainerfile string
	// AppRoot is the project root devcontainer auto-detection resolves
	// .devcontainer/devcontainer.json (or .devcontainer.json) against; ""
	// disables auto-detection (same effect as NoDevcontainerBase).
	AppRoot string
	// NoDevcontainerBase opts out of devcontainer auto-detection
	// (config isolation_devcontainer_base: false / --no-devcontainer-base).
	NoDevcontainerBase bool
	// DevcontainerService names the docker-compose service to use as the base
	// when the detected devcontainer.json declares dockerComposeFile
	// (config isolation_devcontainer_service).
	DevcontainerService string
	// Engines names WHICH per-engine images to build (config
	// isolation_engines / --engines); empty = just this invocation's backend.
	// NOT a composition set: an agent image carries exactly ONE engine.
	// Ignored for a non-composable backend.
	Engines []string
	// Runtime prefers a container runtime by name ("docker" | "podman");
	// empty auto-detects.
	Runtime string
	// KeepCache reuses cached layers. Default false: the build runs with
	// --pull --no-cache so the base image is refreshed and the client install
	// re-runs — the MOST RECENT client, never pinned.
	KeepCache bool
	// Output streams build output when set (the CLI); nil captures output and
	// surfaces only a failure tail.
	Output io.Writer
}

// buildRuntimeProbe is the unit-test seam for build-time runtime selection.
//
// It is ProbeRuntime, NOT the ownership-filtered SelectRuntime that a RUN
// goes through: an image is a rootless/rootful-agnostic artifact, and a build
// has no run whose isolation boundary it could weaken by picking either. The
// two seams are separate so that a future ownership demand on a run can never
// silently start filtering builds.
var buildRuntimeProbe = ProbeRuntime

// selectBuildRuntime resolves the container runtime for an agent-image build,
// failing loud when an EXPLICITLY-requested runtime (opts.Runtime / --runtime)
// is not the one selected. Selection silently substitutes a DIFFERENT daemon
// when the requested one is unavailable — which would build the image into a
// daemon the user never asked for (and a later run, which auto-selects, may then
// not find it there). Auto-detect (empty prefer) has nothing to honor and only
// fails when NO runtime is reachable. Uses the buildRuntimeProbe seam so the
// choice is unit-testable without a live daemon.
func selectBuildRuntime(prefer string) (Runtime, error) {
	rt := buildRuntimeProbe(prefer)
	if _, isHost := rt.(Host); isHost {
		return nil, fmt.Errorf("no container runtime (docker/podman) is available to build with")
	}
	if prefer != "" && rt.Name() != prefer {
		return nil, fmt.Errorf("requested container runtime %q is not available; refusing to build with %q instead (start/enable %q, or pass --runtime %q to build with it deliberately)", prefer, rt.Name(), prefer, rt.Name())
	}
	return rt, nil
}

// BuildAgentImage builds the agent image for the REGISTERED backend name from
// the best available source — the caller's base-image overlay, the composed
// single-engine agent stage on a user base Containerfile / the auto-detected
// project devcontainer / the embedded default base (locked decision 8:
// explicit beats auto-detect beats default), or (for a non-composable
// backend) the client's official image / embedded install recipe — layering
// the RUNNING ctxloom binary in (any dev build works; no ctxloom release
// needed). Each source validates the client inside the build
// (`<client> --version`), so a broken image never ships. A devcontainer.json
// that is present but cannot be resolved to a base (malformed JSON, an
// unresolvable dockerComposeFile) is a HARD error here — this is the explicit
// `container build` command, so there is no chain to degrade down; fix the
// devcontainer, pass --no-devcontainer-base, or configure
// isolation_devcontainer_service. Returns the image tag it built.
func BuildAgentImage(ctx context.Context, backend string, opts ImageBuildOptions) (string, error) {
	if opts.BaseImage != "" && opts.BaseContainerfile != "" {
		return "", fmt.Errorf("base-image and base-containerfile are mutually exclusive (an image asserts the client is preinstalled; a containerfile gets the client layered on)")
	}
	p := engineContainerSpecFor(backend)
	devBase, err := resolveDevBase(opts.AppRoot, opts.NoDevcontainerBase, opts.DevcontainerService)
	if err != nil {
		return "", fmt.Errorf("project devcontainer: %w", err)
	}
	sources := buildSources(p, buildSourcesOptions{
		baseOverride:      opts.BaseImage,
		baseContainerfile: opts.BaseContainerfile,
		devBase:           devBase,
		engine:            backend,
	})
	if len(sources) == 0 {
		return "", fmt.Errorf("backend %q has no local build recipe (no official client image and no embedded Containerfile); pass --base-image with the client preinstalled", backend)
	}
	rt, err := selectBuildRuntime(opts.Runtime)
	if err != nil {
		return "", err
	}
	selfExe, err := resolveSelfExe()
	if err != nil {
		return "", err
	}
	image, provenance, composable := composedIdentity(p, opts.BaseContainerfile, devBase, backend)
	if !composable {
		image, provenance = p.image, HostProvenanceDigest(opts.BaseContainerfile)
	}
	if err := buildExplicitFromSources(ctx, rt, image, sources, selfExe, provenance, opts); err != nil {
		return "", err
	}
	return image, nil
}

// buildExplicitFromSources runs `ctxloom container build`'s sources in
// precedence order, announcing each attempt when the caller streams output,
// and returns nil on the first that builds or the last failure when none does.
// The sibling of buildFirstWorkingSource for the EXPLICIT command: this one has
// no chain to degrade down, so every failure is a plain warning and the last
// one is returned to the CLI rather than recorded as a strictness finding.
func buildExplicitFromSources(ctx context.Context, rt Runtime, image string, sources []buildSource, selfExe, provenance string, opts ImageBuildOptions) error {
	var lastErr error
	for _, src := range sources {
		if opts.Output != nil {
			fmt.Fprintf(opts.Output, "ctxloom: building %s via the %s (%s)\n", image, src.desc, rt.Name())
		}
		err := buildFromSource(ctx, rt, image, src, selfExe, provenance, !opts.KeepCache, opts.Output)
		if err == nil {
			return nil
		}
		clidiag.Warn("ctxloom", "agent image build (%s) failed: %v", src.desc, err)
		lastErr = err
	}
	return lastErr
}

// selfLinuxExe returns the running executable's path when it can serve as the
// in-container ctxloom binary: a linux host (the container shares the host
// kernel/arch) and an ELF. Dynamic linkage is deliberately ALLOWED — the daily
// `just build` ctxloom is CGO/glibc-linked and runs fine on the glibc image
// bases; whether THIS binary actually runs in THIS image is proven by the
// in-image `ctxloom version` build gate (every build source ends with one), so
// an ABI mismatch fails the build and degrades instead of shipping a broken
// image. Non-linux / non-ELF errors so the caller degrades up front.
func selfLinuxExe() (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("host is %s and the agent image needs a linux ctxloom (build it ahead of time via the container-build just recipes)", runtime.GOOS)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve running executable: %w", err)
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", fmt.Errorf("resolve running executable: %w", err)
	}
	f, err := elf.Open(exe)
	if err != nil {
		return "", fmt.Errorf("running executable is not an ELF binary: %w", err)
	}
	_ = f.Close()
	return exe, nil
}

// buildFlags are the per-stage `<runtime> build` knobs: --pull (refresh an
// EXTERNAL base — never set when the FROM is the locally-built base tag, which
// no registry has), --no-cache (re-run installs → most recent client), and
// --build-arg values (BASE_IMAGE for agent stages).
type buildFlags struct {
	pull      bool
	noCache   bool
	buildArgs []string
}

// buildImage runs one agent/overlay stage over a temp context holding the
// Containerfile plus the running static ctxloom binary.
func buildImage(ctx context.Context, rt Runtime, image string, containerfile []byte, selfExe string, flags buildFlags, output io.Writer) error {
	dir, err := os.MkdirTemp("", "ctxloom-imgbuild-")
	if err != nil {
		// See the identical guard in buildBaseImage above: defends against a
		// mutant flipping this check and orphaning a dir MkdirTemp actually
		// created.
		_ = os.RemoveAll(dir)
		return fmt.Errorf("image build context: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	file := filepath.Join(dir, "Containerfile")
	if err := os.WriteFile(file, containerfile, 0o644); err != nil {
		return fmt.Errorf("image build context: %w", err)
	}
	if err := copyExecutable(selfExe, filepath.Join(dir, "ctxloom")); err != nil {
		return fmt.Errorf("image build context: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ctxloom-entrypoint"), containerfiles.Entrypoint(), 0o755); err != nil {
		return fmt.Errorf("image build context: %w", err)
	}
	if err := stageCompanions(dir); err != nil {
		return err
	}
	return runImageBuild(ctx, rt, image, file, dir, flags, output)
}

// runImageBuild executes `<runtime> build -t <image> -f <file> <contextDir>`.
// output streams the build live when set; otherwise output is captured and
// surfaced (tail only) on failure. Capped at imageBuildTimeout.
func runImageBuild(ctx context.Context, rt Runtime, image, file, contextDir string, flags buildFlags, output io.Writer) error {
	args := []string{"build", "-t", image}
	if flags.pull {
		args = append(args, "--pull")
	}
	if flags.noCache {
		args = append(args, "--no-cache")
	}
	for _, ba := range flags.buildArgs {
		args = append(args, "--build-arg", ba)
	}
	args = append(args, "-f", file, contextDir)

	bctx, cancel := context.WithTimeout(ctx, imageBuildTimeout)
	defer cancel()
	cmd := exec.CommandContext(bctx, rt.Binary(), args...)
	if output != nil {
		cmd.Stdout, cmd.Stderr = output, output
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s build: %w", rt.Name(), err)
		}
		return nil
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s build: %w\n%s", rt.Name(), err, tailLines(out.String(), 20))
	}
	return nil
}

// copyExecutable streams src to dst atomically (via iox.NewAtomicFile: a
// unique temp file in dst's directory, fsynced, then chmod 0o755 EXACTLY and
// renamed into place) rather than truncating dst in place, so a reader can
// never observe a half-copied binary. The explicit chmod is load-bearing even
// over a pre-narrowed source file: O_CREATE's mode is umask-narrowed, and a
// narrowed binary (0700) still passes the root-run in-image build gates but
// cannot exec for the dropped ctxloom-user at runtime.
func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := iox.NewAtomicFile(dst, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Abort()
		return err
	}
	return out.Commit()
}

// tailLines returns the last n lines of s — enough build-failure context to
// diagnose without dumping a whole multi-minute build log into a warning.
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
