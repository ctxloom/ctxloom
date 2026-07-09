package isolation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	containerfiles "github.com/ctxloom/ctxloom/container"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// imageBuildTimeout caps one on-the-fly agent-image build. The production
// builds are network-bound (npm / the kiro installer / an official-image pull)
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
// Containerfile on disk (its directory is the build context, so its COPYs
// work), or the embedded default base.
type baseStage struct {
	desc          string
	path          string // user Containerfile path ("" = embedded default)
	containerfile []byte // embedded default content (used when path == "")
}

// defaultBaseStage is the embedded shared base (container/base/Containerfile).
func defaultBaseStage() *baseStage {
	return &baseStage{desc: "default base Containerfile", containerfile: containerfiles.Base}
}

// userBaseStage is a user-provided base Containerfile on disk.
func userBaseStage(path string) *baseStage {
	return &baseStage{desc: "user base Containerfile " + path, path: path}
}

// fromUserBase reports whether this build source builds on a user-CONFIGURED
// base Containerfile (isolation_base_containerfile) rather than an embedded /
// official base. A failure of THIS source is an explicit-request failure: a
// silent fallthrough to a different base ships an image the user never asked
// for, so runEnsureImage records a finding instead of degrading quietly.
func (s buildSource) fromUserBase() bool {
	return s.base != nil && s.base.path != ""
}

// staleRebuildFixIt is attached to the finding raised when a STALE image's
// refresh build fails and the run would otherwise launch the existing stale
// image (which, pre-entrypoint, can run as root). userBaseBuildFixIt is
// attached when an explicitly-configured base Containerfile fails to build.
const (
	staleRebuildFixIt  = "check the build output above and reinstall/rebuild the agent image (`ctxloom container build`), or pass --degraded (env CTXLOOM_DEGRADED=1) to run the existing STALE image anyway"
	userBaseBuildFixIt = "fix the configured base Containerfile (isolation_base_containerfile) so it builds, or pass --degraded (env CTXLOOM_DEGRADED=1) to fall back to another build source"
)

// buildSources orders a profile's local-build sources. An explicit base-IMAGE
// override wins outright (the caller asserts the client lives there). Else, for
// a profile with an agent-stage recipe, a user base CONTAINERFILE leads (their
// environment, our agent layers), the client's OFFICIAL image overlay follows
// (a fresh --pull build rides the vendor's most recent client), and the
// embedded install recipe over the default base (which fetches the MOST RECENT
// client CLI — never pinned) is the fallback. Empty means the image cannot be
// built locally.
func buildSources(p containerProfile, baseOverride, baseContainerfile string) []buildSource {
	if baseOverride != "" {
		return []buildSource{{
			desc:          "overlay on base image " + baseOverride,
			containerfile: overlayContainerfile(baseOverride, p.validate),
		}}
	}
	var out []buildSource
	if len(p.containerfile) > 0 && baseContainerfile != "" {
		out = append(out, buildSource{
			desc:          "agent stage on the user base Containerfile " + baseContainerfile,
			containerfile: p.containerfile,
			base:          userBaseStage(baseContainerfile),
		})
	}
	if p.officialImage != "" {
		out = append(out, buildSource{
			desc:          "overlay on the official client image " + p.officialImage,
			containerfile: overlayContainerfile(p.officialImage, p.validate),
		})
	}
	if len(p.containerfile) > 0 {
		out = append(out, buildSource{
			desc:          "embedded install Containerfile",
			containerfile: p.containerfile,
			base:          defaultBaseStage(),
		})
	}
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
		src, err := exec.LookPath(name)
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
// way). It is baked into built images as the ctxloom.version label for
// diagnostics only. It is NOT the staleness signal: the stamp is git-derived
// (commit sha + commit time), so it cannot see an uncommitted dev change or a
// same-commit rebuild — that is what provenance (a content digest) is for.
var binaryVersion string

// SetBinaryVersion injects the running binary's version stamp for the
// diagnostic image label. Called once by the CLI at startup.
func SetBinaryVersion(v string) { binaryVersion = v }

const provenanceLabel = "ctxloom.provenance"

var (
	provenanceOnce   sync.Once
	provenanceCached string
)

// HostProvenanceDigest returns the provenance label an agent image built NOW —
// from this host's binaries, on the given base Containerfile config ("" = the
// embedded default) — would carry: a content digest over the running ctxloom
// plus each companion present on the host PATH, suffixed with the base
// config's content hash. It is the STALENESS SIGNAL: a changed binary (a dev
// `just install` of ctxloom, a taskloom/ltk/reprise update, even an
// uncommitted rebuild the version stamp can't see) or a changed base config
// changes the digest, and ensureImage rebuilds. Empty when the running binary
// can't be resolved (non-linux dev hosts, test seams) or the base config can't
// be read — the check then disables rather than churn. The binaries half is
// computed once per process (fixed for a running ctxloom). Exported so the
// build tooling (`ctxloom container provenance`) can stamp a matching label.
func HostProvenanceDigest(baseContainerfile string) string {
	provenanceOnce.Do(func() { provenanceCached = computeProvenanceDigest() })
	return combineProvenance(provenanceCached, baseContainerfile)
}

// combineProvenance suffixes the binaries digest with the base-config content
// hash, so the ONE existing staleness gate (the ctxloom.provenance label vs
// imageStale) also rebuilds an agent image whose base Containerfile config
// changed — no parallel staleness mechanism. The suffix is baseContentHash,
// i.e. the baseImageTagFor generation the image rode on. Either half unknown
// yields "" — an untrustable digest disables the check rather than forcing a
// wrong rebuild, matching computeProvenanceDigest.
func combineProvenance(binariesDigest, baseContainerfile string) string {
	if binariesDigest == "" {
		return ""
	}
	content, err := baseContent(baseContainerfile)
	if err != nil {
		return ""
	}
	return binariesDigest + "-" + baseContentHash(content)
}

// baseContent resolves the base Containerfile content a build on this config
// would layer the agent stage onto: the user-provided file
// (isolation_base_containerfile), or the embedded default.
func baseContent(baseContainerfile string) ([]byte, error) {
	if baseContainerfile == "" {
		return containerfiles.Base, nil
	}
	return os.ReadFile(baseContainerfile)
}

// computeProvenanceDigest hashes the running ctxloom (which also covers the
// embedded entrypoint script and Containerfiles — they compile into the binary)
// followed by each present companion, in companionBinaries order, every file
// tagged by name so an added/removed/renamed companion changes the digest even
// when two binaries share content. Any read failure yields "" — an untrustable
// digest disables the check rather than forcing a wrong rebuild.
func computeProvenanceDigest() string {
	selfExe, err := resolveSelfExe()
	if err != nil {
		return ""
	}
	h := sha256.New()
	if err := hashFileTagged(h, "ctxloom", selfExe); err != nil {
		return ""
	}
	for _, name := range companionBinaries {
		p, lerr := exec.LookPath(name)
		if lerr != nil {
			continue // absent on host → not baked → not part of the digest
		}
		if err := hashFileTagged(h, name, p); err != nil {
			return ""
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// hashFileTagged folds a NUL-terminated name tag then the file's full content
// into h. The tag makes the digest sensitive to WHICH binary a set of bytes
// belongs to (a rename, or an absent→present transition), not just the bytes.
func hashFileTagged(h hash.Hash, name, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := fmt.Fprintf(h, "%s\x00", name); err != nil {
		return err
	}
	_, err = io.Copy(h, f)
	return err
}

// imageEnsureFlight is one in-flight ensureImage outcome: err is set before
// done closes, and followers read it only after <-done.
type imageEnsureFlight struct {
	done chan struct{}
	err  error
}

// imageEnsureFlights dedupes CONCURRENT ensureImage calls per (runtime binary,
// image tag) — the sharedFSResults keying: a map/weave fan-out drives many
// members through the same tag from parallel goroutines, and N racing builds
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
// the map/weave fan-out alike. Concurrent callers of one tag share a single
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
	sources := buildSources(c.profile, "", c.baseContainerfile)
	present := c.imagePresent(ctx)
	if present && len(sources) == 0 {
		// No local recipe (user-owned isolation_images override): run AS-IS,
		// never inspected or rebuilt — the user owns that image's lifecycle.
		return nil
	}
	if present && !imageStale(c.imageLabels(ctx), HostProvenanceDigest(c.baseContainerfile)) {
		return nil
	}
	if len(sources) == 0 {
		return fmt.Errorf("container image %q is not present (no local build recipe for this engine; provide the image, or configure isolation_images)", c.image)
	}
	selfExe, err := resolveSelfExe()
	if err != nil {
		if present {
			return nil // stale but unbuildable from this binary — run what exists
		}
		return fmt.Errorf("container image %q is not present and cannot be built from this binary: %w", c.image, err)
	}
	if present {
		clidiag.Warn("ctxloom", "container image %q was built from different ctxloom/companion binaries (or base Containerfile config) than are installed now; rebuilding it", c.image)
	} else {
		clidiag.Warn("ctxloom", "container image %q not found; building it locally (first run — this may take a few minutes)", c.image)
	}
	var lastErr error
	for _, src := range sources {
		err := buildFromSource(ctx, c.runtime, c.image, src, selfExe, c.baseContainerfile, false, nil)
		if err == nil && !c.imagePresent(ctx) {
			err = fmt.Errorf("image %q is still absent after a build via the %s", c.image, src.desc)
		}
		if err == nil {
			return nil
		}
		if src.fromUserBase() {
			// The user EXPLICITLY configured this base Containerfile; falling
			// through to the official/embedded base silently builds an image
			// they never asked for. Record a finding (the choke owner aborts in
			// strict mode) rather than substitute quietly — --degraded still
			// falls through to the next source.
			strictness.Fail(strictness.ClassIsolation, userBaseBuildFixIt,
				"agent image build from the configured base Containerfile (%s) failed: %v", src.desc, err)
		} else {
			clidiag.Warn("ctxloom", "agent image build (%s) failed: %v", src.desc, err)
		}
		lastErr = err
	}
	if present {
		// The stale image still runs, so a failed refresh must not take the
		// container axis down with it — in DEGRADED mode the chain launches the
		// existing stale image (return nil). But a stale, pre-entrypoint image can
		// run as ROOT, so silently shipping it is a security-relevant downgrade of
		// the requested isolation: record a fatal ClassIsolation finding the choke
		// owner aborts on in strict mode (before the stale image spawns), while
		// --degraded records nothing and runs the stale image exactly as before.
		strictness.Fail(strictness.ClassIsolation, staleRebuildFixIt,
			"rebuild of stale image %q failed (%v); the existing image is STALE (its baked ctxloom/companion binaries or base config are outdated) and would run as-is", c.image, lastErr)
		return nil
	}
	return fmt.Errorf("local build of container image %q failed: %w", c.image, lastErr)
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
// the client install re-runs. baseContainerfile is the CONFIGURED base ("" =
// default) — the provenance stamp uses it whether or not this source's base
// is that config, so the stamp always equals what runEnsureImage's staleness
// check computes for the same config (a mismatch would re-flag the image
// stale on every run).
func buildFromSource(ctx context.Context, rt Runtime, image string, src buildSource, selfExe, baseContainerfile string, fresh bool, output io.Writer) error {
	var buildArgs []string
	if src.base != nil {
		baseTag, err := buildBaseImage(ctx, rt, src.base, fresh, output)
		if err != nil {
			return fmt.Errorf("base image (%s): %w", src.base.desc, err)
		}
		buildArgs = append(buildArgs, "BASE_IMAGE="+baseTag)
	}
	// Stamp the diagnostic version label and — the staleness signal — the
	// content digest of the ctxloom+companion binaries baked in plus the base
	// config, so a later ensureImage rebuilds when they change (both empty
	// when unknown — dev seams).
	buildArgs = append(buildArgs, "CTXLOOM_VERSION="+binaryVersion)
	buildArgs = append(buildArgs, "CTXLOOM_PROVENANCE="+HostProvenanceDigest(baseContainerfile))
	return buildImage(ctx, rt, image, src.containerfile, selfExe, buildFlags{
		pull:      fresh && src.base == nil,
		noCache:   fresh,
		buildArgs: buildArgs,
	}, output)
}

// buildBaseImage builds the stage-1 base image and returns the content-keyed
// tag it built (baseImageTagFor). A user-provided Containerfile builds with
// ITS OWN directory as the context (so its COPYs resolve); the embedded
// default builds from a scratch context.
func buildBaseImage(ctx context.Context, rt Runtime, base *baseStage, fresh bool, output io.Writer) (string, error) {
	flags := buildFlags{pull: fresh, noCache: fresh}
	if base.path != "" {
		abs, err := filepath.Abs(base.path)
		if err != nil {
			return "", fmt.Errorf("base containerfile: %w", err)
		}
		content, err := os.ReadFile(abs)
		if err != nil {
			return "", fmt.Errorf("base containerfile: %w", err)
		}
		tag := baseImageTagFor(content)
		return tag, runImageBuild(ctx, rt, tag, abs, filepath.Dir(abs), flags, output)
	}

	dir, err := os.MkdirTemp("", "ctxloom-imgbase-")
	if err != nil {
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
	// already ship the client CLI — instead of the profile's build sources.
	BaseImage string
	// BaseContainerfile builds the shared base stage from this user-provided
	// Containerfile instead of the embedded default; the engine's agent stage
	// layers on top. Mutually exclusive with BaseImage.
	BaseContainerfile string
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

// selectBuildRuntime resolves the container runtime for an agent-image build,
// failing loud when an EXPLICITLY-requested runtime (opts.Runtime / --runtime)
// is not the one selected. SelectRuntime silently substitutes a DIFFERENT daemon
// when the requested one is unavailable — which would build the image into a
// daemon the user never asked for (and a later run, which auto-selects, may then
// not find it there). Auto-detect (empty prefer) has nothing to honor and only
// fails when NO runtime is reachable. Uses the selectRuntimeProbe seam so the
// choice is unit-testable without a live daemon.
func selectBuildRuntime(prefer string) (Runtime, error) {
	rt := selectRuntimeProbe(prefer)
	if _, isHost := rt.(Host); isHost {
		return nil, fmt.Errorf("no container runtime (docker/podman) is available to build with")
	}
	if prefer != "" && rt.Name() != prefer {
		return nil, fmt.Errorf("requested container runtime %q is not available; refusing to build with %q instead (start/enable %q, or pass --runtime %q to build with it deliberately)", prefer, rt.Name(), prefer, rt.Name())
	}
	return rt, nil
}

// BuildAgentImage builds the agent image for the REGISTERED backend name from
// the best available source — the caller's base-image overlay, the agent stage
// on a user base Containerfile, the client's official image, or the embedded
// install recipe over the default base — layering the RUNNING ctxloom binary in
// (any dev build works; no ctxloom release needed). Each source validates the
// client inside the build (`<client> --version`), so a broken image never
// ships. Returns the image tag it built.
func BuildAgentImage(ctx context.Context, backend string, opts ImageBuildOptions) (string, error) {
	if opts.BaseImage != "" && opts.BaseContainerfile != "" {
		return "", fmt.Errorf("base-image and base-containerfile are mutually exclusive (an image asserts the client is preinstalled; a containerfile gets the client layered on)")
	}
	p := containerProfileFor(backend)
	sources := buildSources(p, opts.BaseImage, opts.BaseContainerfile)
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
	var lastErr error
	for _, src := range sources {
		if opts.Output != nil {
			fmt.Fprintf(opts.Output, "ctxloom: building %s via the %s (%s)\n", p.image, src.desc, rt.Name())
		}
		if err := buildFromSource(ctx, rt, p.image, src, selfExe, opts.BaseContainerfile, !opts.KeepCache, opts.Output); err != nil {
			clidiag.Warn("ctxloom", "agent image build (%s) failed: %v", src.desc, err)
			lastErr = err
			continue
		}
		return p.image, nil
	}
	return "", lastErr
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
	if err := os.WriteFile(filepath.Join(dir, "ctxloom-entrypoint"), containerfiles.Entrypoint, 0o755); err != nil {
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

// copyExecutable copies src to dst with mode 0755 EXACTLY (the build context
// is a fresh temp dir, so a plain copy suffices — no atomicity needed). The
// explicit Chmod is load-bearing: O_CREATE's mode is umask-narrowed, and a
// narrowed binary (0700) still passes the root-run in-image build gates but
// cannot exec for the dropped ctxloom-user at runtime.
func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if err := out.Chmod(0o755); err != nil {
		_ = out.Close()
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
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
