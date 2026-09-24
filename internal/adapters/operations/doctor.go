package operations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/engine"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/adapters/gitignore"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/adapters/signing/agentkey"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/shared/gitutil"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/shared/version"
)

// doctorDepBinariesRequired are the non-engine binaries ctxloom's own
// features genuinely hard-depend on regardless of which engines are
// configured: git — worktree isolation shells `git worktree`
// (internal/adapters/isolation), remote clone/pull reads/writes real git repos (internal/
// remote/repo_cache.go, internal/adapters/git/exec.go), and `ctxloom init`/`manage
// install` themselves clone the default remote. A machine without git
// silently can't do worktrees or pull content; this makes that a visible
// DOCTOR-CHECK-DEPS-a1 warn instead.
var doctorDepBinariesRequired = []string{"git"}

// doctorDepBinariesRecommended are binaries ctxloom ITSELF never execs —
// grepped repo-wide: no exec.Command/LookPath("ssh") or ("ssh-keygen")
// anywhere but this probe and init PRIME's mirror of it
// (cli.checkSystemDeps) — but that are still worth flagging present:
//
//   - ssh is what `git` ITSELF shells out to for an ssh:// or git@host:
//     remote (irrelevant for the default HTTPS remote ctxloom seeds).
//   - ssh-keygen is the tool a user without an existing SSH key would run BY
//     HAND to make one (`ssh-keygen -t ed25519-sk` — the fix `ctxloom
//     review` and agentkey's own messages already suggest); ctxloom never runs it
//     for them.
//
// NEITHER is a signing dependency (an earlier version of this comment/the
// Detail below wrongly implied both were "for signing" — an audit caught
// it): ctxloom's signing is pure Go over the ssh-agent protocol
// (SSH_AUTH_SOCK — internal/adapters/signing/agentkey/agentkey.go's dialEnvAgent
// net.Dial("unix", ...), never exec) and pure-Go sshsig cryptography
// (internal/adapters/signing/sign.go's Sign/Verify, internal/adapters/signing/publisher.go's
// VerifyPublisher — both explicitly documented "no ssh-keygen binary" in
// their own doc comments). Their absence still warns (worth having,
// especially ssh-keygen if you don't yet have a key to sign with) but the
// Detail text below says what they're actually for, not "signing".
var doctorDepBinariesRecommended = []string{"ssh", "ssh-keygen"}

// DoctorStatus is one check's verdict, and there are exactly three of them. It
// is a named type rather than a bare string because the value set IS the
// contract: it is shared with the "ctxloom-doctor" Agent Skill and with every
// consumer of `doctor --format json`, and it was previously written as a free
// literal at more than twenty sites with the legal values recorded only in a
// trailing comment — where a typo ("WARN", "warning") would render, marshal and
// pass review while silently reading as neither ok nor warn to anything that
// matches on the value. The underlying type stays string, so the JSON wire shape
// is unchanged.
type DoctorStatus string

const (
	// DoctorOK: the check's subject is in the state setup intends.
	DoctorOK DoctorStatus = "ok"
	// DoctorWarn: doctor's fail-loud signal. Doctor never fails the
	// process, so a warn is how a real problem is reported.
	DoctorWarn DoctorStatus = "warn"
	// DoctorInfo: reported for context, not a verdict — nothing to fix.
	DoctorInfo DoctorStatus = "info"
)

// DoctorCheck is one named check's outcome. Marker is the DOCTOR-CHECK-*
// vocabulary doctor shares with the "ctxloom-doctor" Agent Skill, so a
// human or an LLM reading either surface sees one language.
type DoctorCheck struct {
	Marker string       `json:"marker"`
	Status DoctorStatus `json:"status"`
	Detail string       `json:"detail"`
}

// DoctorReport is `ctxloom doctor`'s structured result.
type DoctorReport struct {
	Checks []DoctorCheck `json:"checks"`
}

// DoctorRequest scopes one report.
type DoctorRequest struct {
	// DepsOnly scopes the report to ONLY the machine-capability probes
	// (DEPS-a1's git/ssh/ssh-keygen/container runtime/each configured
	// engine's client, SIGNKEY-k1, and GITIDENT-l2) — questions that are
	// true-or-false regardless of whether a project has been set up yet.
	// init's PRIME and the setup skill's phase 1 run in THIS mode: the full
	// report on a brand-new, never-set-up project is a wall of
	// expected-missing state (no agents, no profiles, no hooks wired) that
	// would needlessly alarm a user at the very start of the setup that is
	// about to configure those things.
	DepsOnly bool
	// Home is the user's home directory, a fact the composition root
	// establishes rather than a read of this service's own; "" when it could
	// not be, in which case the home-rooted local-state rows are skipped.
	Home string
}

// Doctor runs ctxloom's deterministic setup checks and returns their rows in
// the fixed order every frontend renders. It prints nothing and fails on no
// check outcome: DoctorWarn IS the fail-loud signal. The only errors are the
// service's own preconditions (a signer discoverer that cannot be built); a
// configuration that fails to load is a finding the checks report, not an
// error.
func Doctor(ctx context.Context, app *App, req DoctorRequest) (DoctorReport, error) {
	reg := app.Engines()
	cfg, cfgErr := app.Config(ctx)
	discoverer, err := SignerDiscoverer()
	if err != nil {
		return DoctorReport{}, err
	}
	var checks []DoctorCheck
	if req.DepsOnly {
		checks = []DoctorCheck{
			doctorCheckDeps(reg, cfg),
			doctorCheckSignKey(ctx, cfg, discoverer),
			doctorCheckGitIdentity(ctx, discoverer.GitConfig),
		}
	} else {
		checks = []DoctorCheck{
			doctorCheckSetupMarker(cfg, cfgErr),
			doctorCheckDeps(reg, cfg),
			doctorCheckSignKey(ctx, cfg, discoverer),
			doctorCheckGitIdentity(ctx, discoverer.GitConfig),
			doctorCheckAgents(ctx, reg, cfg, cfgErr),
			doctorCheckCapabilityLoss(ctx, reg, cfg, cfgErr),
			doctorCheckVersion(),
			doctorCheckTranscriptReaders(ctx, reg, cfg, app.ProbeEngineVersion),
			doctorCheckHooksTrust(ctx, reg, cfg, cfgErr),
			doctorCheckMCPInvocation(reg, doctorProjectDir(cfg)),
			doctorCheckContentTrust(cfg, cfgErr),
			doctorCheckUpstreamSignatures(cfg, cfgErr),
			doctorCheckSetupLockAndAssembly(ctx, cfg, cfgErr),
			doctorCheckSetupCompanions(cfg, cfgErr, app.NoCompanions),
			doctorCheckSetupAuthPing(),
			doctorCheckIngestionLimit(reg, cfg),
			doctorCheckLocalTierState(cfg, req.Home),
			doctorCheckGitignorePosture(cfg, cfgErr),
			doctorCheckForeignWorktrees(ctx, git.NewExec(), doctorProjectDir(cfg)),
			doctorCheckOrphanContainers(ctx, doctorRuntimes(), isolation.ReapOrphanedContainers),
			doctorCheckLegacyIndex(),
			doctorCheckHarpDurability(),
			doctorCheckSpoolBacklog(),
			doctorCheckSpoolCounters(ctx),
		}
	}
	return DoctorReport{Checks: checks}, nil
}

// doctorConfiguredEngines returns the sorted, de-duplicated set of registered
// backend names (e.g. claude-code) every configured agent's Engine
// label resolves to. nil cfg (config failed to load) yields none — the
// deps check then reports only the engine-independent binaries.
func doctorConfiguredEngines(reg engine.Registry, cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	set := map[string]bool{}
	for _, a := range cfg.GetConfiguredAgents() {
		backend, _ := ResolveBackend(reg, cfg, a.LLM)
		if backend != "" {
			set[backend] = true
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// doctorContainerRuntimeRequired reports whether a container runtime is a HARD
// dependency for THIS project. It is one only when something would actually try
// to launch a container: the project's `runtime:` default is container, or at
// least one configured agent declares `runtime: container`. The effective axis
// is resolved exactly as ResolveAgent resolves it (agents.go: the
// agent's own choice wins, else the project default, else host), so doctor and
// the launcher cannot disagree about what this project runs.
//
// Everywhere else a container runtime is a convenience: a host-runtime project
// never touches one, and calling it "required" there manufactures a warn on a
// perfectly healthy machine — the fastest way to teach a user to ignore doctor.
func doctorContainerRuntimeRequired(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	projectDefault := cfg.GetRuntime()
	if launch.IsContainerRuntime(projectDefault) {
		return true
	}
	for _, a := range cfg.GetConfiguredAgents() {
		runtime := a.Runtime
		if runtime == "" {
			runtime = projectDefault
		}
		if launch.IsContainerRuntime(runtime) {
			return true
		}
	}
	return false
}

// doctorCheckDeps probes PATH for git (worktree isolation + remote clone/
// pull + init/manage install's own clone) and each configured engine's native
// client — both genuinely REQUIRED — plus ssh/ssh-keygen, which are RECOMMENDED
// but not required (see doctorDepBinariesRecommended's doc for why: ctxloom
// never execs either; signing is pure Go). A container runtime lands in
// whichever bucket THIS project's configuration puts it in
// (doctorContainerRuntimeRequired). The two buckets are reported separately so
// "missing" never conflates an optional convenience with a real hard
// dependency.
func doctorCheckDeps(reg engine.Registry, cfg *config.Config) DoctorCheck {
	const marker = "DOCTOR-CHECK-DEPS-a1"
	missingRequired := doctorMissingFromPath(doctorDepBinariesRequired)
	missingRequired = append(missingRequired, doctorMissingEngineClients(reg, cfg)...)
	missingRecommended := doctorMissingFromPath(doctorDepBinariesRecommended)
	if len(doctorRuntimes()) == 0 {
		if doctorContainerRuntimeRequired(cfg) {
			missingRequired = append(missingRequired,
				"docker/podman (container runtime — this project runs container agents)")
		} else {
			missingRecommended = append(missingRecommended,
				"docker/podman (container runtime — needed only to run `runtime: container` agents, which this project configures none of)")
		}
	}
	if len(missingRequired) == 0 && len(missingRecommended) == 0 {
		return DoctorCheck{Marker: marker, Status: DoctorOK,
			Detail: "git and every configured engine's client are on PATH (required); ssh, ssh-keygen and a container runtime are also present (recommended: ssh is what git itself needs for an ssh:// remote, ssh-keygen is only for generating a NEW signing key by hand — signing itself is pure Go and never execs either; a container runtime is required only for `runtime: container` agents)"}
	}
	sort.Strings(missingRequired)
	sort.Strings(missingRecommended)
	var parts []string
	if len(missingRequired) > 0 {
		parts = append(parts, "missing (required): "+strings.Join(missingRequired, ", "))
	}
	if len(missingRecommended) > 0 {
		parts = append(parts, "missing (recommended, not required — ssh is what git itself needs for an ssh:// remote, ssh-keygen is only for generating a NEW signing key by hand; signing itself is pure Go and never execs either): "+strings.Join(missingRecommended, ", "))
	}
	return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: strings.Join(parts, "; ")}
}

// doctorRuntimes is every OCI runtime available on this host — the one probe
// doctor's runtime rows share. It is doctor's alone: a host run never asks,
// because the first `podman info` a user ever runs creates rootless storage
// under their home.
func doctorRuntimes() []isolation.Runtime {
	var out []isolation.Runtime
	for _, rt := range []isolation.Runtime{isolation.Docker{}, isolation.Podman{}} {
		if rt.Available() {
			out = append(out, rt)
		}
	}
	return out
}

// doctorCheckOrphanContainers is the MANUAL backstop for a runner container
// that outlived its owner. A runner exits on its own once its coordinator has
// been waited on for its owner-loss window (runner.Home.OwnerLost), and --rm then
// removes its container, so nothing sweeps at startup; what is left for this
// row is a runner WEDGED past that — still running, its owner confirmed dead.
// It reaps those on every runtime present (reap's own rules decide, and skip
// on any doubt) and warns when it found one, because a wedged runner is a
// defect worth reporting, not routine tidying.
func doctorCheckOrphanContainers(ctx context.Context, runtimes []isolation.Runtime, reap func(context.Context, isolation.Runtime) isolation.ContainerReapResult) DoctorCheck {
	const marker = "DOCTOR-CHECK-ORPHAN-CONTAINERS-z2"
	if len(runtimes) == 0 {
		return DoctorCheck{Marker: marker, Status: DoctorInfo, Detail: "no container runtime on this host; nothing to check"}
	}
	var reaped []string
	var names []string
	for _, rt := range runtimes {
		names = append(names, rt.Name())
		if r := reap(ctx, rt); r.Reaped > 0 {
			reaped = append(reaped, fmt.Sprintf("%d %s", r.Reaped, rt.Name()))
		}
	}
	if len(reaped) == 0 {
		return DoctorCheck{Marker: marker, Status: DoctorOK,
			Detail: "no runner container outlived its owner (" + strings.Join(names, ", ") + ")"}
	}
	return DoctorCheck{Marker: marker, Status: DoctorWarn,
		Detail: "removed runner container(s) whose owner is dead: " + strings.Join(reaped, ", ") +
			" — a runner exits on its own once its coordinator is gone, so each of these was wedged past that"}
}

// doctorMissingFromPath returns the subset of bins that does not resolve on
// PATH, in the order given (the caller sorts).
func doctorMissingFromPath(bins []string) []string {
	var missing []string
	for _, bin := range bins {
		if _, err := exec.LookPath(bin); err != nil {
			missing = append(missing, bin)
		}
	}
	return missing
}

// doctorMissingEngineClients returns "<binary> (<engine>)" for every CONFIGURED
// engine whose native client is not on PATH, for the DOCTOR-CHECK-DEPS-a1 PATH
// probe. The binary is the one the engine's own grammar declares
// (EngineBinary); a test double declares none and is skipped rather
// than reported as missing.
func doctorMissingEngineClients(reg engine.Registry, cfg *config.Config) []string {
	var missing []string
	for _, engine := range doctorConfiguredEngines(reg, cfg) {
		bin := EngineBinary(reg, engine)
		if bin == "" || IsTestOnlyEngine(reg, engine) {
			continue
		}
		if _, err := exec.LookPath(bin); err != nil {
			missing = append(missing, fmt.Sprintf("%s (%s)", bin, engine))
		}
	}
	return missing
}

// doctorCheckSignKey is a machine-capability probe like DOCTOR-CHECK-DEPS-a1
// (included in --deps scope): it asks whether a signing IDENTITY would
// resolve right now, using the EXACT SAME resolver `ctxloom review`'s
// approve path AND `ctxloom sign`/`--sign` both use (internal/adapters/signing/
// agentkey.Discoverer.Discover, behind ResolveLocalSigner — see
// cli.resolveReviewSigner and `ctxloom sign`) rather than re-deriving
// discovery here. Read-only:
// Discover only lists ssh-agent identities (agent.Agent.Signers/List over
// SSH_AUTH_SOCK), it never signs or reads private key bytes.
//
// This is NOT publishing-only: approving reviewed content (`ctxloom review`)
// countersigns the approval record with this same identity, and review is a
// normal part of setup (pulling/approving a seeded remote's content), not
// something only publishers do. Absence is still never a hard failure —
// review degrades to an explicit unsigned-approval confirmation rather than
// blocking (spec §9.5) — but it is a WARN, not silent, because a project that
// only ever consumes ALREADY-trusted/embedded content (the common case: the
// seeded ctxloom-default remote is pre-trusted, nothing to approve) genuinely
// has no need for one; this is advisory, same posture as the ssh-keygen/
// container-runtime warns beside it. Surfacing it here (and in init PRIME's
// cli.checkSystemDeps) beats a user hitting agentkey.NoKeyError or
// the unsigned-approval prompt cold at their first real `ctxloom review`/
// `ctxloom sign`.
func doctorCheckSignKey(ctx context.Context, cfg *config.Config, discoverer *agentkey.Discoverer) DoctorCheck {
	const marker = "DOCTOR-CHECK-SIGNKEY-k1"
	explicit := ""
	if cfg != nil {
		explicit = cfg.SignKey()
	}
	ok, detail := SignKeyResolutionDetail(ctx, discoverer, explicit)
	if ok {
		return DoctorCheck{Marker: marker, Status: DoctorOK, Detail: detail}
	}
	return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: detail}
}

// SignKeyResolutionDetail runs internal/adapters/signing/agentkey's real resolution
// chain (explicit --key/sign.key, then `git config user.signingkey`, then
// ssh-agent's sole identity — agentkey.go's package doc) and renders the
// outcome as a short, actionable line. Shared between doctorCheckSignKey and
// init PRIME's cli.checkSystemDeps so both surfaces say the exact same
// thing about the exact same resolver, rather than drifting apart.
//
// ok=true names the resolved key the way cli.printSignResult already
// does ("<source> (<fingerprint>)") — the same presentation `ctxloom sign`
// itself prints when it actually signs something.
//
// ok=false distinguishes the three shapes agentkey.Discover can fail with,
// observed directly from agentkey_test.go / this package's own tests:
//   - AmbiguousKeyError: ssh-agent holds MULTIPLE identities and nothing
//     (git config user.signingkey, sign.key) narrowed the choice — Discover
//     deliberately never guesses, it names every candidate instead.
//   - AmbiguousKeyNameError: an explicit --key/sign.key NAME matched more
//     than one agent identity's comment.
//   - NoKeyError (or any other error, e.g. an unreadable git-configured key
//     file): nothing resolves at all.
//
// In every failure shape, the WHY (approving reviewed content and
// publishing/signing your own content both need an identity; merely
// consuming already-trusted/embedded content does not) is stated once,
// alongside the concrete fix.
func SignKeyResolutionDetail(ctx context.Context, discoverer *agentkey.Discoverer, explicit string) (ok bool, detail string) {
	discovered, err := discoverer.Discover(ctx, explicit)
	if err == nil {
		// A probe never signs, so the agent connection is released as soon as
		// the identity has been described.
		defer func() { _ = discovered.Close() }()
		return true, fmt.Sprintf("signing key resolves via %s (%s)", discovered.Source, discovered.Fingerprint)
	}

	const why = "needed to approve reviewed content (`ctxloom review`) and to publish or sign your own content (`ctxloom bundle sign`) — merely consuming already-trusted/embedded content does not require a signing key"

	var ambig *agentkey.AmbiguousKeyError
	if errors.As(err, &ambig) {
		names := make([]string, 0, len(ambig.Candidates))
		for _, c := range ambig.Candidates {
			name := c.Fingerprint
			if c.Comment != "" {
				name = c.Comment + " (" + c.Fingerprint + ")"
			}
			names = append(names, name)
		}
		return false, fmt.Sprintf(
			"ambiguous: ssh-agent holds %d identities and none is picked by `git config user.signingkey` or `sign.key` — %s; disambiguate with `ctxloom config set sign.key <name>` or `git config user.signingkey <path>`: %s",
			len(ambig.Candidates), why, strings.Join(names, ", "))
	}

	var ambigName *agentkey.AmbiguousKeyNameError
	if errors.As(err, &ambigName) {
		return false, fmt.Sprintf(
			"ambiguous: sign.key %q matches %d ssh-agent identities — %s; narrow the name or use a SHA256: fingerprint instead",
			ambigName.Name, len(ambigName.Candidates), why)
	}

	var noKey *agentkey.NoKeyError
	if errors.As(err, &noKey) {
		reason := ""
		if noKey.Detail != "" {
			reason = " (" + noKey.Detail + ")"
		}
		return false, fmt.Sprintf(
			"no signing key resolves%s — %s; run `ssh-add ~/.ssh/<key>` with your intended key loaded, set `sign.key` (`ctxloom config set sign.key <name>`) or `git config user.signingkey <path>`, or generate one: `ssh-keygen -t ed25519`",
			reason, why)
	}

	return false, fmt.Sprintf("signing key resolution failed: %s — %s", err.Error(), why)
}

// gitConfigFunc is agentkey.Discoverer.GitConfig's shape: the one existing
// generic `git config --get <key>` reader in this codebase (internal/
// signing/agentkey/agentkey.go's execGitConfig, defaulted by
// SignerDiscoverer()) — already used to resolve user.signingkey.
// doctorCheckGitIdentity reuses it verbatim for user.name/user.email rather
// than shelling out to git a second, bespoke way.
type gitConfigFunc = func(ctx context.Context, dir, key string) (value string, ok bool, err error)

// doctorCheckGitIdentity is a machine-capability probe like DOCTOR-CHECK-
// DEPS-a1/SIGNKEY-k1 (included in --deps scope): it verifies git's commit
// identity — BOTH user.name AND user.email — is explicitly resolvable via
// `git config` (any scope: local/global/system; `git config --get` already
// searches all three, so this asks nothing beyond what git itself would use
// right now). WHY: agents ctxloom launches do their own work — including
// their own `git commit` — inside isolated worktrees (internal/adapters/isolation/
// worktree.go's teardown guards, e.g. IsDirty/WorktreeRemove around lines
// 391/401, exist BECAUSE that uncommitted work must survive teardown; an
// agent is expected to commit there). Without an explicit identity, a commit
// either fails outright or git silently derives one from the OS account —
// misattributing the work to the wrong identity is the actual danger, so
// "something resolves" is not the bar; "explicitly set" is.
//
// Read-only, informational only: like DOCTOR-CHECK-SIGNKEY-k1 beside it,
// this never blocks — a project that never runs agent worktrees at all has
// no immediate need, so a bare `ctxloom doctor` there must not manufacture a
// false alarm.
func doctorCheckGitIdentity(ctx context.Context, gitConfig gitConfigFunc) DoctorCheck {
	const marker = "DOCTOR-CHECK-GITIDENT-l2"
	ok, detail := GitIdentityDetail(ctx, gitConfig)
	if ok {
		return DoctorCheck{Marker: marker, Status: DoctorOK, Detail: detail}
	}
	return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: detail}
}

// GitIdentityDetail runs gitConfig for user.name and user.email and renders
// the outcome as a short, actionable line. Shared between
// doctorCheckGitIdentity and init PRIME's cli.checkSystemDeps so both
// surfaces say the exact same thing, rather than drifting apart.
func GitIdentityDetail(ctx context.Context, gitConfig gitConfigFunc) (ok bool, detail string) {
	name, nameOK, nameErr := gitConfig(ctx, "", "user.name")
	email, emailOK, emailErr := gitConfig(ctx, "", "user.email")

	if nameErr != nil || emailErr != nil {
		return false, "reading git identity failed: " + joinErrors("; ", nameErr, emailErr)
	}

	nameSet := nameOK && strings.TrimSpace(name) != ""
	emailSet := emailOK && strings.TrimSpace(email) != ""
	if nameSet && emailSet {
		return true, fmt.Sprintf("git identity resolves: %s <%s>", name, email)
	}
	return false, gitIdentityGapDetail(nameSet, emailSet)
}

// joinErrors renders the non-nil errors' messages joined by sep.
func joinErrors(sep string, errs ...error) string {
	var msgs []string
	for _, err := range errs {
		if err != nil {
			msgs = append(msgs, err.Error())
		}
	}
	return strings.Join(msgs, sep)
}

// gitIdentityGapDetail names which halves of git's commit identity are unset
// and the exact command that sets each — misattributed commits are the danger,
// so the fix has to be in the message.
func gitIdentityGapDetail(nameSet, emailSet bool) string {
	var missing, fixes []string
	if !nameSet {
		missing = append(missing, "user.name")
		fixes = append(fixes, `git config --global user.name "Your Name"`)
	}
	if !emailSet {
		missing = append(missing, "user.email")
		fixes = append(fixes, "git config --global user.email you@example.com")
	}
	return fmt.Sprintf(
		"git commit identity not fully set (missing: %s) — agents ctxloom launches commit their own work inside isolated worktrees, and without an explicit identity a commit fails or git silently mis-attributes it to whatever the OS account derives; set it: %s",
		strings.Join(missing, ", "), strings.Join(fixes, "; "))
}

// doctorCheckAgents resolves every configured agent (profile composition +
// engine/runtime) and reports the first failure, or how many resolved
// cleanly. An empty roster is a WARN, not a neutral fact: init-as-skill's
// setup postcondition (§8.2) requires "agents non-empty with resolvable
// profiles", and doctor IS that postcondition check now — a management-only
// project with genuinely zero agents is rare enough that silently calling it
// "info" would hide the far more common case, an interrupted/incomplete
// setup.
func doctorCheckAgents(ctx context.Context, reg engine.Registry, cfg *config.Config, cfgErr error) DoctorCheck {
	if cfgErr != nil {
		return DoctorCheck{Marker: "DOCTOR-CHECK-AGENTS-b2", Status: DoctorWarn, Detail: "config did not load: " + cfgErr.Error()}
	}
	configuredAgents := cfg.GetConfiguredAgents()
	if len(configuredAgents) == 0 {
		return DoctorCheck{Marker: "DOCTOR-CHECK-AGENTS-b2", Status: DoctorWarn,
			Detail: "no agents configured (run `/ctxloom-init` phase 5, or `ctxloom agent set <name> ...`)"}
	}
	names := make([]string, 0, len(configuredAgents))
	for name := range configuredAgents {
		names = append(names, name)
	}
	sort.Strings(names)
	var failed []string
	for _, name := range names {
		if _, err := ResolveAgent(ctx, reg, cfg, name, ""); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", name, err))
		}
	}
	if len(failed) == 0 {
		return DoctorCheck{Marker: "DOCTOR-CHECK-AGENTS-b2", Status: DoctorOK,
			Detail: fmt.Sprintf("%d agent(s) resolve cleanly: %s", len(names), strings.Join(names, ", "))}
	}
	return DoctorCheck{Marker: "DOCTOR-CHECK-AGENTS-b2", Status: DoctorWarn, Detail: strings.Join(failed, "; ")}
}

// doctorCheckCapabilityLoss names, per configured agent, what the engine it
// resolves to has NO structural place for — the hooks its profiles actually
// declare and that engine can never fire.
//
// This is doctor's whole job applied to the one class of breakage every other
// check is legitimately blind to. Hook WIRING (DOCTOR-CHECK-HOOKS-TRUST-d4)
// reports what landed on each backend's own surface, and every line of it is
// true; a hook that could never land anywhere is invisible in it by
// construction. Agent RESOLUTION (DOCTOR-CHECK-AGENTS-b2) reports that the
// binding is valid, which it is — the engine simply cannot do this. So a user
// who switched an agent to an engine without a hook surface would keep a
// guardrail in their config, see two green checks, and lose it silently.
//
// It reads CapabilityLossByAgent, which is CapabilityLoss (the same
// uncarriedSurfaces read `profile materialize` prints as "NOT
// carried" and `agent show` already reuses) asked once per configured agent —
// not a second computation of the same fact.
//
// WARN, not info: the user asked for something their environment cannot give,
// which is exactly the state doctor's fail-loud signal is for. Nothing is
// lost when nothing is configured that cannot be carried, and the check then
// says so rather than staying silent, so a reader can tell "checked, clean"
// apart from "never checked".
func doctorCheckCapabilityLoss(ctx context.Context, reg engine.Registry, cfg *config.Config, cfgErr error) DoctorCheck {
	const marker = "DOCTOR-CHECK-CAPABILITY-LOSS-u1"
	if cfgErr != nil {
		return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: "config did not load: " + cfgErr.Error()}
	}
	if cfg == nil {
		return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: "no config to read an engine binding from"}
	}
	configured := len(cfg.GetConfiguredAgents())
	entries := CapabilityLossByAgent(ctx, reg, cfg)
	if len(entries) == 0 {
		return DoctorCheck{Marker: marker, Status: DoctorOK, Detail: fmt.Sprintf(
			"every configured engine carries what its agent's profiles declare (%d agent(s) checked)", configured)}
	}
	return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: capabilityLossDetail(entries)}
}

// doctorCheckVersion is deliberately best-effort/skill-guided: there is no
// update-check infrastructure (internal/adapters/cli/version.go is a bare print), so
// this reports the running version and defers comparison to the
// ctxloom-doctor skill (or a human) rather than faking a currency verdict.
func doctorCheckVersion() DoctorCheck {
	return DoctorCheck{Marker: "DOCTOR-CHECK-VERSION-c3", Status: DoctorInfo,
		Detail: fmt.Sprintf("running %s; comparing against the newest remote tag is best-effort/skill-guided (no --check-version yet)", version.Version)}
}

// doctorCheckHooksTrust cross-references doctorConfiguredEngines (every
// backend a configured agent resolves to) against HarnessStatus —
// the SAME read `ctxloom manage check`/`ctxloom manage hooks check` already
// expose — reporting the DELIVERY POSTURE per backend: a `ctxloom run`
// session carries its own hooks and MCP in its session home, and the
// project-side files are the explicit `manage hooks install` door's, so
// their absence is the correct state of a project and is reported as such,
// never as a fault (ruled 2026-09-21). Plus how many signers the trust store
// carries (ListSigners — always includes the embedded root, so a healthy
// store is never reported as empty).
func doctorCheckHooksTrust(ctx context.Context, reg engine.Registry, cfg *config.Config, cfgErr error) DoctorCheck {
	const marker = "DOCTOR-CHECK-HOOKS-TRUST-d4"
	if cfgErr != nil {
		return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: "config did not load: " + cfgErr.Error()}
	}
	status := DoctorOK
	hooks, hooksOK := doctorHooksWiringDetail(ctx, reg, cfg)
	trust, trustOK := doctorTrustStoreDetail(ListSigners(cfg, nil))
	if !hooksOK || !trustOK {
		status = DoctorWarn
	}
	return DoctorCheck{Marker: marker, Status: status, Detail: strings.Join([]string{hooks, trust}, "; ")}
}

// doctorHooksWiringDetail reports the delivery posture for every backend a
// configured agent resolves to, reading HarnessStatus (the SAME read
// `ctxloom manage check` exposes): which backends carry project-side hooks
// and MCP (the explicit `manage hooks install` door) and which rely on the
// session's own delivery — both are healthy states. ok=false is the caller's
// warn signal, raised only when the read itself fails.
func doctorHooksWiringDetail(ctx context.Context, reg engine.Registry, cfg *config.Config) (detail string, ok bool) {
	configured := doctorConfiguredEngines(reg, cfg)
	if len(configured) == 0 {
		return "hooks/MCP: no engine is configured to check", true
	}
	result, err := HarnessStatus(ctx, reg, cfg, HarnessStatusRequest{})
	if err != nil {
		return "hooks/MCP: " + err.Error(), false
	}
	byBackend := make(map[string]BackendWiring, len(result.Backends))
	for _, b := range result.Backends {
		byBackend[b.Backend] = b
	}
	var project, session []string
	for _, name := range configured {
		if b, found := byBackend[name]; !found || !b.SettingsExists || !b.HooksPresent {
			session = append(session, name)
			continue
		}
		project = append(project, name)
	}
	sort.Strings(project)
	sort.Strings(session)
	var parts []string
	if len(session) > 0 {
		parts = append(parts, "hooks/MCP delivered per session (the session home; nothing project-side) for: "+strings.Join(session, ", "))
	}
	if len(project) > 0 {
		parts = append(parts, "hooks/MCP also registered in the project (the explicit `manage hooks install` door) for: "+strings.Join(project, ", "))
	}
	return strings.Join(parts, "; "), true
}

// doctorTrustStoreDetail reports how much trust the store actually grants, from
// ListSigners' (listing, error) pair.
//
// An UNREADABLE row is the case worth being careful about: ListSigners is
// deliberately tolerant, so a store it could not open, could not parse, or
// whose lines the parser dropped comes back as SignerListing rows with
// Unreadable set (operations/signer.go's listFromPath) — never as an error.
// Those rows grant no trust, so counting them as active signers reports MORE
// trust than the machine has, and reporting "ok" beside them tells the user
// their trust store is fine when part of it was silently skipped.
//
// The error arm is kept because the signature carries one, but note that
// ListSigners returns `out, nil` unconditionally today: it is defensive, not
// reachable, and no test can drive it through this function.
func doctorTrustStoreDetail(signers []SignerListing, err error) (detail string, ok bool) {
	if err != nil {
		return "trust store: " + err.Error(), false
	}
	active := 0
	var unreadable []string
	for _, s := range signers {
		switch {
		case s.Unreadable != "":
			unreadable = append(unreadable, fmt.Sprintf("%s (%s)", s.Path, s.Unreadable))
		case !s.Suppressed:
			active++
		}
	}
	grants := doctorProjectPowerGrants(signers)
	if len(unreadable) > 0 {
		sort.Strings(unreadable)
		return fmt.Sprintf("trust store: %d active signer(s), and %d entr(y/ies) that could not be read and grant NO trust: %s",
			active, len(unreadable), strings.Join(unreadable, "; ")) + grants, false
	}
	return fmt.Sprintf("trust store: %d active signer(s)", active) + grants, true
}

// doctorProjectPowerGrants names every principal the PROJECT store trusts to
// execute companions or to approve content, or "" when it trusts none.
//
// The project store is committed with the repository, so whoever can land a
// commit can add a line to it; these two namespaces are the ones that turn such
// a line into running code or into skipping review. Listing them is
// information, not a fault: a project that ships its own companions
// legitimately grants companion execution here.
func doctorProjectPowerGrants(signers []SignerListing) string {
	powers := []struct{ ns, label string }{
		{signing.NamespaceCompanion, signing.NamespaceCompanion + " (execute companions)"},
		{signing.NamespaceApprove, signing.NamespaceApprove},
	}
	var parts []string
	path := ""
	for _, p := range powers {
		var principals []string
		for _, s := range signers {
			if s.Source != signerSourceProject || s.Unreadable != "" || s.Suppressed || !s.Entry.MatchesNamespace(p.ns) {
				continue
			}
			path = s.Path
			principals = append(principals, strings.Join(s.Entry.Principals, ","))
		}
		if len(principals) > 0 {
			sort.Strings(principals)
			parts = append(parts, p.label+" to "+strings.Join(principals, ", "))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("; the committed project store %s grants %s", path, strings.Join(parts, "; and "))
}

// ===== init-as-skill Phase 6 postcondition checks (plan.md §8.2) =====
//
// These compose the SAME operations/config entry points every other command
// already uses (config.Config, AssembleContext, the resolved
// bundles.Catalog behind config.Config.BundleLoader) rather than
// re-implementing any of their logic — a check's job is to CALL them and
// translate the result into a DoctorCheck, never to re-derive what "locked",
// "resolves", or "registered" means. Deliberately absent: anything that
// opens a third-party client's own config file (Zed settings.json, Nori's
// config.toml, ...) — see `ctxloom doctor --help` and init-as-skill.plan.md
// §6/§8.2. The remaining two §8.2 items — agents non-empty/resolvable and
// hooks/MCP registered per backend — are folded directly into
// doctorCheckAgents and doctorCheckHooksTrust above rather than duplicated
// here: doctor's OWN pre-existing checks already covered that ground, they
// just needed a stricter (WARN, not INFO) reading of the empty/missing case.

// doctorAppDir mirrors ProjectAppDir's fallback (unexported there,
// remotes.go): the .ctxloom directory the reader already resolved, or ""
// when it found none (callers use this to short-circuit rather than probe a
// directory that was never located).
func doctorAppDir(cfg *config.Config) string {
	if cfg != nil && len(cfg.GetAppPaths()) > 0 {
		return cfg.GetAppPaths()[0]
	}
	return ""
}

// doctorProjectDir returns the project root — the directory containing the
// resolved .ctxloom marker (doctorAppDir) — or "" when no marker was found.
// The gitignore-posture and foreign-worktree checks both need a working
// directory to run against (a project's own .gitignore; the repo `git
// worktree list` is scoped to) and share this rather than each re-deriving
// "where is this project rooted" its own way.
func doctorProjectDir(cfg *config.Config) string {
	appDir := doctorAppDir(cfg)
	if appDir == "" {
		return ""
	}
	return filepath.Dir(appDir)
}

// doctorCheckSetupMarker verifies the .ctxloom marker directory the reader
// already resolved (cfg.AppPaths) is present and the project config was read
// without a hard error — the ground-floor precondition every other check in
// this report assumes. Read-only: it inspects the generation's ALREADY-resolved
// record instead of re-globbing the filesystem for .ctxloom.
func doctorCheckSetupMarker(cfg *config.Config, cfgErr error) DoctorCheck {
	const marker = "DOCTOR-CHECK-SETUP-MARKER-e5"
	if cfgErr != nil {
		return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: "config did not load: " + cfgErr.Error()}
	}
	appDir := doctorAppDir(cfg)
	if appDir == "" {
		return DoctorCheck{Marker: marker, Status: DoctorWarn,
			Detail: "no .ctxloom marker directory found (run `ctxloom manage install` or `ctxloom init`)"}
	}
	// A config that FAILED SCHEMA VALIDATION still loads -- config.go's
	// loadConfigFile records the violation as a Warning and keeps going, the
	// same fault-tolerant shape every load-time defect gets (CLAUDE.md). This
	// check used to report "config valid" unconditionally the instant the
	// marker directory existed, in the SAME doctor run that had just printed
	// "ctxloom: warning: config validation warning ..." to the exact same
	// terminal -- a health check asserting the opposite of what it had just
	// printed (task unwatched-discharge, found on `agents.<x>.runtime`
	// carrying the retired "container" spelling, but the defect is general:
	// ANY schema violation this config carries was being reported as
	// "config valid"). cfg.GetWarnings() is EVERY load-time warning
	// (WarnKindRead/Parse/Validate/UnknownKey/MigrationLossy/LayerScope) --
	// see internal/core/config/warnings.go's own doc: "EVERY kind declared below
	// is fatal-class in strict mode". Doctor's own contract (doctor.feature:
	// "why its exit code is not the verdict") means this stays DoctorWarn,
	// never a process exit change -- warn IS doctor's fail-loud signal.
	if warnings := cfg.GetWarnings(); len(warnings) > 0 {
		return DoctorCheck{Marker: marker, Status: DoctorWarn,
			Detail: fmt.Sprintf("marker present, but config.yaml failed schema validation (%d issue(s) -- see the warning line(s) printed above, or `ctxloom manage config edit`): %s", len(warnings), appDir)}
	}
	return DoctorCheck{Marker: marker, Status: DoctorOK, Detail: "marker present, config valid: " + appDir}
}

// doctorCheckSetupLockAndAssembly verifies the two "seeded deps are actually
// usable" postconditions the setup skill's phase 4/5 promise: the lockfile
// (remote.LockfileManager — the SAME reader `ctxloom sync`/`lock` use) parses
// without error, and a real context assembly (AssembleContext —
// the SAME entry point `ctxloom run`'s configured-default path uses) succeeds
// end to end. AssembleContext exercises the trust gate, companion-loadout
// seeding, and fragment/profile resolution for real; none of that is
// reimplemented here.
func doctorCheckSetupLockAndAssembly(ctx context.Context, cfg *config.Config, cfgErr error) DoctorCheck {
	const marker = "DOCTOR-CHECK-SETUP-DEPS-h8"
	if cfgErr != nil {
		return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: "config did not load: " + cfgErr.Error()}
	}
	var parts []string
	status := DoctorOK
	if appDir := doctorAppDir(cfg); appDir == "" {
		parts = append(parts, "lockfile: no .ctxloom directory to check")
		status = DoctorWarn
	} else {
		lf, err := remote.NewLockfileManager(appDir).Load()
		if err != nil {
			parts = append(parts, "lockfile: "+err.Error())
			status = DoctorWarn
		} else {
			parts = append(parts, fmt.Sprintf("lockfile: %d entries parse cleanly", len(lf.AllEntries())))
		}
	}
	// A checkpoint taken immediately around the assembly call captures ONLY
	// the strictness.ClassRef findings THIS call records — the same
	// strictness.Fail sites AssembleContext already fires for a configured
	// default profile that fails to resolve (collectProfileFragments) or a
	// profile-pushed fragment that fails to load (warnFragmentLoadFailure).
	// Assembly is fault-tolerant on the defaults path: it skips a failed ref
	// and keeps going, so a nil error here means "assembly ran to
	// completion", never "every configured ref actually loaded" — reading
	// the findings recorded during this exact call is what tells the two
	// apart, without re-assembling or reimplementing the skip logic.
	mark := strictness.Checkpoint()
	_, err := AssembleContext(ctx, cfg, AssembleContextRequest{})
	skippedRefs := 0
	for _, f := range strictness.Since(mark) {
		if f.Class == strictness.ClassRef {
			skippedRefs++
		}
	}
	switch {
	case err != nil:
		parts = append(parts, "context assembly: "+err.Error())
		status = DoctorWarn
	case skippedRefs > 0:
		parts = append(parts, fmt.Sprintf(
			"context assembly: succeeded, but %d ref(s) in the configured default profile(s) failed to load and were skipped (see the warning(s) above)",
			skippedRefs))
		status = DoctorWarn
	default:
		parts = append(parts, "context assembly: succeeds for the configured default profile(s)")
	}
	return DoctorCheck{Marker: marker, Status: status, Detail: strings.Join(parts, "; ")}
}

// doctorCheckSetupCompanions reports what the session's companions actually
// contributed, from the ONE resolved bundle set (config.Config.BundleLoader)
// AssembleContext's own assembly reads. Reporting only: a project with no
// companions installed is not misconfigured (they are optional add-ons), so
// this is never a "warn".
//
// It discovers nothing itself. The catalog's reads are the loadouts a session
// carries and its candidates are the identities that produced none, each with
// the reason — so "found on PATH" and "actually run" stay different facts
// without a second pass over the machine that could answer differently from
// the session being described. Reporting only the first would tell a user
// their companion is fine while it contributes nothing, the exact silent no-op
// doctor exists to surface.
func doctorCheckSetupCompanions(cfg *config.Config, cfgErr error, noCompanions bool) DoctorCheck {
	const marker = "DOCTOR-CHECK-SETUP-COMPANIONS-i9"
	if cfgErr != nil {
		return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: "config did not load: " + cfgErr.Error()}
	}
	if noCompanions {
		return DoctorCheck{Marker: marker, Status: DoctorInfo, Detail: "companion probing disabled (--no-companions)"}
	}
	decided := readCompanionDecisions(cfg)
	if !decided.discovered() {
		return DoctorCheck{Marker: marker, Status: DoctorInfo, Detail: "no companions discovered"}
	}
	return DoctorCheck{Marker: marker, Status: DoctorOK, Detail: decided.detail()}
}

// companionDecisions is what the resolved catalog decided about each companion
// binary it knows of, read once off that catalog rather than discovering
// companions a second time. It is a struct rather than the rendered detail so
// a consumer can ask WHAT was decided (withheld) without matching the
// rendered text: the startup-findings delivery selects on it, and the doctor
// row's ok status deliberately carries no such signal (add-ons are never a
// doctor failure).
type companionDecisions struct {
	// contributing named companions whose loadout the session actually reads.
	contributing []string
	// absent is on nobody's PATH; notRun is present but never consented to
	// (with its path); failed is present, consented, and its probe broke.
	absent, notRun, failed []string
}

func readCompanionDecisions(cfg *config.Config) companionDecisions {
	cat := cfg.BundleLoader().Catalog()
	var d companionDecisions
	for _, read := range cat.Reads() {
		if read.Provenance == bundles.ProvenanceCompanion {
			d.contributing = append(d.contributing, companionBinOf(read.Key()))
		}
	}
	for _, cand := range cat.Candidates() {
		bin := companionBinOf(cand.Ref)
		switch cand.Reason {
		case bundles.CandidateAbsent:
			d.absent = append(d.absent, bin)
		case bundles.CandidateUnconsented:
			d.notRun = append(d.notRun, fmt.Sprintf("%s (%s)", bin, cand.Path))
		default:
			d.failed = append(d.failed, fmt.Sprintf("%s (%s)", bin, cand.Path))
		}
	}
	return d
}

// discovered reports whether the catalog knew of any companion at all.
func (d companionDecisions) discovered() bool {
	return len(d.contributing) > 0 || d.withheld()
}

// withheld reports whether any companion the session might have expected is
// NOT contributing — absent, unconsented, or failed. This is the decision an
// agent needs to hear: the tool it expects is not there.
func (d companionDecisions) withheld() bool {
	return len(d.absent)+len(d.notRun)+len(d.failed) > 0
}

// detail renders the decisions as the doctor row's text. "(none)" rather than
// an omitted section: what a session actually carries is the fact this check
// exists to state, and a missing line reads as unchecked. Every other section
// is about an exception and is omitted when it has no members. Each carries
// the remedy its reason implies.
func (d companionDecisions) detail() string {
	loadouts := "(none)"
	if len(d.contributing) > 0 {
		loadouts = strings.Join(d.contributing, ", ")
	}
	parts := []string{"loadouts read: " + loadouts}
	for _, section := range []struct {
		label string
		items []string
		hint  string
	}{
		{"NOT RUN", d.notRun, " — allow with 'ctxloom companion trust <path>'"},
		{"probe failed", d.failed, ""},
		{"not installed", d.absent, ""},
	} {
		if len(section.items) == 0 {
			continue
		}
		parts = append(parts, section.label+": "+strings.Join(section.items, ", ")+section.hint)
	}
	return strings.Join(parts, "; ")
}

// companionBinOf recovers the binary name a companion identity was minted
// from. A key that will not parse is shown verbatim: it is still the most
// specific thing known about that entry, and hiding it would drop a row.
func companionBinOf(key trust.BundleKey) string {
	ref, err := trust.ParseBundleRef(string(key))
	if err != nil || ref.Bundle == "" {
		return string(key)
	}
	return ref.Bundle
}

// doctorCheckSetupAuthPing is a placeholder. init-as-skill's USER RULING (a)
// wants a deterministic auth ping BEFORE the raw-CLI vendor TUI launches, but
// no such surface exists anywhere in this codebase yet (grepped: no
// AuthPing/auth-ping symbol) — that is a different slice's work (init-as-
// skill.plan.md §10④, init bootstrap rework), not this one's. Reported as
// "info" so the gap is VISIBLE in the postcondition report instead of
// silently missing.
func doctorCheckSetupAuthPing() DoctorCheck {
	return DoctorCheck{Marker: "DOCTOR-CHECK-SETUP-AUTHPING-j0", Status: DoctorInfo,
		Detail: "no auth-ping surface exists in this build yet (deferred; verify by launching the engine's own CLI)"}
}

// doctorCheckIngestionLimit states, rather than tests, the one boundary this
// command cannot see past: delivered → ingested (FLOWS-UNIFIED Appendix A.2,
// verdict NONE/DEFECT). Every check above this line can be green — the
// context assembled, the surface written, hooks and MCP registered — and
// ctxloom still has no way to know whether the vendor engine actually READ
// what landed on disk. A moved config key, a changed surface format, or an
// engine silently ignoring a path all look identical from here, because the
// read happens inside a process ctxloom does not own.
//
// This is not a probe with a pass/fail outcome — there is nothing left on
// ctxloom's side of that boundary to check — so it always reports "info", the
// same as DOCTOR-CHECK-SETUP-AUTHPING-j0 beside it. It exists so a diagnosis
// session that has run every other check ends on a STATED limit instead of a
// report that simply goes quiet, which a reader could otherwise mistake for
// "checked and confirmed read".
func doctorCheckIngestionLimit(reg engine.Registry, cfg *config.Config) DoctorCheck {
	const marker = "DOCTOR-CHECK-INGESTION-q7"
	who := "the configured engine"
	if engines := doctorConfiguredEngines(reg, cfg); len(engines) > 0 {
		who = strings.Join(engines, ", ")
	}
	return DoctorCheck{Marker: marker, Status: DoctorInfo, Detail: fmt.Sprintf(
		"ctxloom writes the assembled context onto %s's own on-disk agent surface; whether %s actually reads what was written happens inside a process ctxloom does not own, and nothing in this product can confirm it — verify by asking the engine itself",
		who, who)}
}

// doctorCheckLocalTierState reports every paths.TierLocal entry (paths.Layout)
// that is absent — the thing a fresh clone (RootProject rows) or a fresh
// machine (RootHome rows) has no way to learn today (config-layer-scope
// design doc, "The .ctxloom classification"): local-only state nothing
// rebuilds, so its absence is silent everywhere else (a clone gets no warning
// that it started a new task-log project-id, lost the dirty-tree-commit
// acknowledgement, has no distilled session history, or degraded review's
// diff to a full-content dump). TierCommitted/TierDerived entries are not
// reported here: a derived entry's absence is fine (its own Rebuild command
// produces it) and a committed entry's absence means the repository itself is
// incomplete, a different class of problem doctor's other checks (setup
// marker, lock/assembly) already cover.
//
// The home is a value the caller established (DoctorRequest.Home), not a
// read of this check's own.
//
// A row's Presence decides how its absence is treated. PresenceMustExist
// (every RootProject row, and the zero value) warns on absence exactly as
// before RootKind/Presence existed. PresenceIfUsed (the RootHome rows added
// by C13 — sessions, approvals, signers, trigger cache, coord, companion
// consent) never warns on absence: a home-rooted store is shared across every
// project on the machine and created lazily by exercising a specific
// feature, so having none of it yet is normal, not a loss. When a
// PresenceIfUsed row IS present, it is reported anyway (the `present` list
// below) — this is the "doctor can finally see them" half of C13: real
// visibility into what home-rooted state exists, without a false warning on
// what doesn't yet.
func doctorCheckLocalTierState(cfg *config.Config, homeDir string) DoctorCheck {
	const marker = "DOCTOR-CHECK-LOCAL-STATE-p6"
	appDir := doctorAppDir(cfg)
	if appDir == "" {
		return DoctorCheck{Marker: marker, Status: DoctorWarn,
			Detail: "no .ctxloom marker directory found; nothing to check"}
	}
	fsys := afero.NewOsFs()
	if cfg != nil && cfg.FS() != nil {
		fsys = cfg.FS()
	}

	var missing []string
	var present []string
	for _, entry := range paths.Layout() {
		if entry.Tier != paths.TierLocal {
			continue
		}
		// A RootHome row cannot be checked without a home; the caller hands
		// "" when it has none, and the row is skipped rather than failing
		// the whole check.
		if entry.Root == paths.RootHome && homeDir == "" {
			continue
		}
		base := entry.ResolveRoot(appDir, homeDir)
		exists, err := afero.Exists(fsys, filepath.Join(base, entry.Rel))
		if err != nil {
			continue
		}
		switch {
		case exists && entry.Presence == paths.PresenceIfUsed:
			present = append(present, entry.Rel)
		case !exists && entry.Presence != paths.PresenceIfUsed:
			missing = append(missing, fmt.Sprintf("%s (%s)", entry.Rel, entry.Lost))
		}
	}
	status := DoctorOK
	detail := "every local-only state path is present"
	if len(missing) > 0 {
		sort.Strings(missing)
		status = DoctorWarn
		detail = fmt.Sprintf("%d local-only path(s) absent, and nothing rebuilds them: %s",
			len(missing), strings.Join(missing, "; "))
	}
	if len(present) > 0 {
		sort.Strings(present)
		detail = fmt.Sprintf("%s; %d home-rooted store(s) in use: %s", detail, len(present), strings.Join(present, ", "))
	}
	return DoctorCheck{Marker: marker, Status: status, Detail: detail}
}

// doctorCheckContentTrust names remote bundles whose content is being WITHHELD
// because ctxloom cannot attribute it to a publisher it trusts.
//
// This is the diagnosis gap J001900's B2 hop exists to close, and it is sharp
// because every other inspector is legitimately silent about it:
//
//   - `review --list` does NOT name it, and that is correct by design — unsigned
//     is not PENDING. Pending means "signed by someone, awaiting your review";
//     unsigned content never enters that queue at all.
//   - `bundle list` shows the bundle as an ordinary installed entry, because it
//     IS installed. The bytes are on disk; it is the EXPOSURE that is withheld.
//   - `doctor`'s trust check reports how many signers the store holds, which
//     says nothing about whether any particular bundle matched one.
//
// So a user whose guidance silently stopped arriving had nothing to run. The
// documented workflow was diffing lockfiles by hand.
//
// It reads Bundle.Signer() carried onto the listing — a value only a load path
// that already VERIFIED a signature against the trust root ever sets, so an
// empty signer means "no signature, or one by a key this machine does not trust
// to publish". It makes no trust decision of its own and parses no signature.
//
// LOCAL bundles are excluded deliberately: project-authored content is trusted
// by provenance and is not expected to carry a publisher signature, so flagging
// it would be noise on every healthy project.
func doctorCheckContentTrust(cfg *config.Config, cfgErr error) DoctorCheck {
	const marker = "DOCTOR-CHECK-CONTENT-TRUST-n4"
	if cfgErr != nil {
		return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: "config did not load: " + cfgErr.Error()}
	}
	// Ask the SAME question `ctxloom review` asks, through the same function.
	//
	// This check used to derive "withheld" from the publisher signature alone,
	// and that is the wrong bar. A countersignature covers the BYTES, so content
	// a human reviewed and accepted is fully attributable without the publisher
	// ever signing it — the two are independent attestations, not a preference
	// and a fallback. Deriving from the signature reported accepted content as a
	// problem and told the user to go ask someone else to fix it.
	//
	// PendingReview answers what is ACTUALLY awaiting a decision, counting a
	// trusted publisher signature and a local acceptance alike, so doctor and
	// the review command can no longer disagree about what the agent can see.
	pending, err := PendingReview(cfg, PendingReviewRequest{Loader: cfg.BundleLoader()})
	if err != nil {
		return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: "could not determine review state: " + err.Error()}
	}
	return classifyContentTrust(marker, pending)
}

// classifyContentTrust turns the review state into the check.
//
// It groups by WHY an item is still pending, because the reasons are not
// variations on one problem and their remedies differ sharply:
//
//   - an invalid signature means the bytes and the signature disagree. That is
//     indistinguishable from tampering, and neither trusting a key nor accepting
//     the content is an answer to it — accepting bytes their own signature
//     refutes is the one remedy that must never be suggested.
//   - an untrusted key needs recognising, OR the content reviewing.
//   - no signature at all is not a defect to be fixed by the publisher: a
//     countersignature covers the bytes, so reviewing and accepting it is a
//     complete attestation on its own.
func classifyContentTrust(marker string, pending *PendingReviewResult) DoctorCheck {
	if pending == nil || pending.Total == 0 {
		return DoctorCheck{Marker: marker, Status: DoctorOK,
			Detail: "no content is awaiting review: everything installed is either signed by a publisher you trust or accepted by you"}
	}

	var invalid, untrusted, unsigned []string
	for _, b := range pending.Bundles {
		switch b.Publisher {
		case bundles.ReasonTampered:
			invalid = append(invalid, b.Ref)
		case bundles.ReasonUntrustedSigner:
			untrusted = append(untrusted, b.Ref)
		default:
			unsigned = append(unsigned, b.Ref)
		}
	}

	var parts []string
	if len(invalid) > 0 {
		sort.Strings(invalid)
		parts = append(parts, fmt.Sprintf("%d with a signature that does NOT cover their bytes — treat as tampered until the publisher explains it; do not trust a key or accept the content to make this go away: %s",
			len(invalid), strings.Join(invalid, ", ")))
	}
	if len(untrusted) > 0 {
		sort.Strings(untrusted)
		parts = append(parts, fmt.Sprintf("%d signed by a key this machine does not trust (`ctxloom signer trust` if you recognise the publisher, or review the content and accept it): %s",
			len(untrusted), strings.Join(untrusted, ", ")))
	}
	if len(unsigned) > 0 {
		sort.Strings(unsigned)
		parts = append(parts, fmt.Sprintf("%d never reviewed (accept them with `ctxloom review` — your acceptance countersigns exactly those bytes and re-pends if they change, so an unsigned publisher is not a blocker): %s",
			len(unsigned), strings.Join(unsigned, ", ")))
	}

	return DoctorCheck{Marker: marker, Status: DoctorWarn,
		Detail: fmt.Sprintf("%d item(s) are withheld from your assistant pending review — %s",
			pending.Total, strings.Join(parts, "; "))}
}

// doctorCheckUpstreamSignatures names every revision `deps upgrade` REFUSED
// to advance onto because the publisher signature at that commit does not
// verify over its bytes — and the pin it kept instead.
//
// IT EXISTS BECAUSE THE REFUSAL FIXES THE PROBLEM AND THEREBY HIDES IT.
// DOCTOR-CHECK-CONTENT-TRUST-n4 above asks "is any installed content withheld
// from your assistant?", and after a refusal the honest answer is NO: the pin
// stayed on content that verifies, so it reports [ok] and is right to. The
// thing that went wrong is not in the project at all — it is a REVISION that
// exists upstream and was not taken. Nothing on this machine is in a bad
// state, which is precisely why no other inspector has anything to say, and
// why without this check the fact lives only in the transient stdout of the
// sync that refused it.
//
// THE FRAMING IS THE POINT, and it is the opposite of n4's. n4 names something
// the user can act on locally (trust a key, or ask for a signature). This one
// must not: there is nothing to configure, no key to add, no flag to pass. The
// publisher has to re-sign and republish. A message that reads as a local
// misconfiguration would send someone editing their trust store to fix a
// problem that is not on their machine.
//
// WARN RATHER THAN INFO, deliberately. DoctorInfo means "nothing to fix", and
// something does need fixing — just not by the person reading it. It is never
// fatal: doctor fails no process, and this check in particular reports a
// project that is working correctly off its last verified pin.
//
// It makes no trust decision, parses no signature and re-verifies nothing: it
// reads what the upgrade round recorded, filtered by
// LiveRefusedAdvances to those still describing the pin the lockfile
// actually holds, so a record left over from a world that has moved on is
// dropped rather than reported.
func doctorCheckUpstreamSignatures(cfg *config.Config, cfgErr error) DoctorCheck {
	const marker = "DOCTOR-CHECK-UPSTREAM-SIGNATURES-o5"
	if cfgErr != nil {
		return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: "config did not load: " + cfgErr.Error()}
	}
	refused, err := LiveRefusedAdvances(cfg)
	if err != nil {
		// Reported, never folded onto "nothing was refused": this record's one
		// job is to keep a fact from evaporating, so reading an unreadable
		// store as silence would reproduce the exact gap it closes.
		return DoctorCheck{Marker: marker, Status: DoctorWarn,
			Detail: "could not read the record of refused upgrades, so this check cannot say whether any revision was refused: " + err.Error()}
	}
	if len(refused) == 0 {
		return DoctorCheck{Marker: marker, Status: DoctorOK,
			Detail: "no upstream revision has been refused: every pin your last upgrade could advance landed on content whose publisher signature verifies"}
	}
	sort.Slice(refused, func(i, j int) bool { return refused[i].Identity < refused[j].Identity })
	var parts []string
	for _, r := range refused {
		parts = append(parts, fmt.Sprintf("%s at revision %s does not verify, so the pin is being kept at %s (refused %s)",
			r.Identity, gitutil.AbbrevSHA(r.ProposedSHA, 16), gitutil.AbbrevSHA(r.KeptSHA, 16), r.RefusedAt.Format("2006-01-02")))
	}
	return DoctorCheck{Marker: marker, Status: DoctorWarn,
		Detail: fmt.Sprintf("%d upstream revision(s) were REFUSED because the PUBLISHER's signature does not cover the bytes it sits beside: %s. "+
			"Nothing is wrong on this machine and nothing is withheld from your assistant — it is served the content at the kept pin. "+
			"There is nothing to configure here: the publisher must re-sign and republish, and `ctxloom deps upgrade` picks it up "+
			"and clears this the next time it runs",
			len(refused), strings.Join(parts, "; "))}
}

// ===== J001300 close-out: doctor's share of the journey's checks ====
//
// The journey's feature numbers five checks; the gitignore posture, the
// foreign worktrees and the harp durability below are doctor's (its
// scenarios' 1, 4 and 5). The other two are the worktree/purge/lessons
// surfaces and are theirs, not doctor's.

// doctorCheckGitignorePosture reports a superseded blanket `.ctxloom` ignore
// rule: under it, .ctxloom/content can never be committed at all, so a
// project cannot ship its own authored context. Read-only — it must never
// retire the rule itself, only report it; RetireSupersededFile (which does
// retire it) and this check now share exactly one detector,
// gitignore.SupersededBlanketLines, so the two can never disagree about what
// counts as superseded.
func doctorCheckGitignorePosture(cfg *config.Config, cfgErr error) DoctorCheck {
	const marker = "DOCTOR-CHECK-GITIGNORE-f6"
	if cfgErr != nil {
		return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: "config did not load: " + cfgErr.Error()}
	}
	projectDir := doctorProjectDir(cfg)
	if projectDir == "" {
		return DoctorCheck{Marker: marker, Status: DoctorInfo, Detail: "no .ctxloom marker directory found; nothing to check"}
	}
	gitignorePath := filepath.Join(projectDir, ".gitignore")
	lines, err := gitignore.SupersededBlanketLines(gitignorePath)
	if err != nil {
		return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: "could not read .gitignore: " + err.Error()}
	}
	if len(lines) == 0 {
		return DoctorCheck{Marker: marker, Status: DoctorOK, Detail: ".gitignore carries no superseded blanket .ctxloom rule"}
	}
	return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: fmt.Sprintf(
		".gitignore carries a blanket `%s` rule; .ctxloom/content can never be committed under it (run `ctxloom manage gitignore install` to retire it)",
		strings.Join(lines, ", "))}
}

// doctorForeignWorktreeTimeout bounds the total time doctorCheckForeignWorktrees
// spends per candidate tree, across BOTH the merged-ness and dirty probes —
// beyond the single MergedBranches call git.execGit itself already bounds
// (git.go's mergedBranchesTimeout), the report as a whole must not stall
// `ctxloom doctor` — including the deps-independent run `ctxloom init`
// performs — behind a wedged foreign checkout (e.g. on a stale network
// filesystem).
const doctorForeignWorktreeTimeout = 5 * time.Second

// doctorCheckForeignWorktrees reports long-lived worktrees this repository has
// that ctxloom did NOT create — everything outside the sessions root. Report
// only: ctxloom removes no worktree it did not create (WorktreeRemove has no
// force escape hatch by construction, and this check adds no path that could
// ever act), so the report carries the exact commands a human runs instead.
//
// Dirty/merge state is reported ONLY when it was actually measured this run —
// never assumed. A tree a fixture makes dirty and then commits over IS clean
// by the time doctor sees it; claiming otherwise would fabricate a claim
// about what is safe to delete, which is exactly the defect this check exists
// to prevent.
func doctorCheckForeignWorktrees(ctx context.Context, g git.Git, workDir string) DoctorCheck {
	const marker = "DOCTOR-CHECK-FOREIGN-WORKTREES-r8"
	if workDir == "" {
		return DoctorCheck{Marker: marker, Status: DoctorInfo, Detail: "no project directory to check"}
	}
	if g == nil {
		g = git.NewExec()
	}
	if !g.IsRepo(workDir) {
		return DoctorCheck{Marker: marker, Status: DoctorInfo, Detail: "not a git repository; nothing to check"}
	}
	ctx, cancel := context.WithTimeout(ctx, doctorForeignWorktreeTimeout)
	defer cancel()

	worktrees, err := g.WorktreeList(ctx, workDir)
	if err != nil {
		return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: "could not list worktrees: " + err.Error()}
	}
	sessionsRoot, _ := paths.HomeSessionsDir() // best-effort; "" excludes nothing extra
	mainPath := filepath.Clean(workDir)
	var foreign []git.Worktree
	for _, wt := range worktrees {
		if wt.Bare {
			continue
		}
		p := filepath.Clean(wt.Path)
		if p == mainPath {
			continue
		}
		if sessionsRoot != "" && doctorUnderDir(sessionsRoot, p) {
			continue
		}
		foreign = append(foreign, wt)
	}
	if len(foreign) == 0 {
		return DoctorCheck{Marker: marker, Status: DoctorOK,
			Detail: "no worktrees ctxloom did not create outside the sessions root"}
	}
	sort.Slice(foreign, func(i, j int) bool { return foreign[i].Path < foreign[j].Path })

	// A merged-ness primitive did not exist anywhere in this codebase before
	// this check needed one; printing "unmerged" unconditionally would be a
	// lie, so it is only claimed when MergedBranches actually answered.
	merged, mergedErr := g.MergedBranches(ctx, workDir, "")
	mergedSet := make(map[string]bool, len(merged))
	for _, b := range merged {
		mergedSet[b] = true
	}

	var lines []string
	for _, wt := range foreign {
		branch := strings.TrimPrefix(wt.Branch, "refs/heads/")
		name := filepath.Base(wt.Path)

		mergeState := "merge state unknown"
		if mergedErr == nil {
			if mergedSet[branch] {
				mergeState = "merged"
			} else {
				mergeState = "unmerged"
			}
		}

		dirtyState := "dirty state unknown"
		if dirty, dirtyErr := g.IsDirty(ctx, wt.Path); dirtyErr == nil {
			if dirty {
				dirtyState = "dirty"
			} else {
				dirtyState = "clean"
			}
		}

		lines = append(lines, fmt.Sprintf(
			"%s (branch %s, %s, %s) — ctxloom will not remove it; run `git worktree remove %s` then `git branch -d %s`",
			name, branch, mergeState, dirtyState, wt.Path, branch))
	}
	detail := fmt.Sprintf("%d worktree(s) ctxloom did not create: %s", len(foreign), strings.Join(lines, "; "))
	if mergedErr != nil {
		detail += fmt.Sprintf(" (could not determine merge state: %s)", mergedErr.Error())
	}
	return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: detail}
}

// doctorUnderDir reports whether p is root itself or a descendant of it.
func doctorUnderDir(root, p string) bool {
	root = filepath.Clean(root)
	p = filepath.Clean(p)
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

// doctorHarpDurabilityMaxNamed caps how many unclassified-top-level paths
// doctorCheckHarpDurability names individually before summarizing the rest as
// a count — the same "cap at ~5 with a count" shape doctorCheckLocalTierState
// and the other listing checks in this file use, so one huge project does not
// turn a single check's line into an unreadable wall of paths.
const doctorHarpDurabilityMaxNamed = 5

// doctorCheckHarpDurability warns about authored artifacts sitting at a harp
// directory's TOP LEVEL, where no paths.HarpMembers row classifies them —
// neither under persist/ (mounted into containers, durable) nor under an
// Ephemeral member. A containerized agent writing a design note there writes
// into container-ephemeral space and loses it on exit. Nothing moves them;
// the human does, and this check says where.
//
// The walk is two-level: the sessions root's OWN top level holds files (lock
// files) alongside the harp directories, so the OUTER iteration skips
// non-directory entries; the INNER level is HarpTopLevelArtifacts,
// the table-derived predicate.
func doctorCheckHarpDurability() DoctorCheck {
	const marker = "DOCTOR-CHECK-HARP-DURABILITY-s9"
	sessionsRoot, err := paths.HomeSessionsDir()
	if err != nil {
		return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: "cannot resolve sessions dir: " + err.Error()}
	}
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return DoctorCheck{Marker: marker, Status: DoctorOK, Detail: "no harp directories yet"}
		}
		return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: "cannot read sessions dir: " + err.Error()}
	}

	var flagged []string
	for _, e := range entries {
		if !e.IsDir() {
			// a lock file at the sessions root's OWN top level beside the
			// harp directories — not a harp, never walked into.
			continue
		}
		harp := e.Name()
		names, err := HarpTopLevelArtifacts(filepath.Join(sessionsRoot, harp))
		if err != nil {
			continue // best-effort; an unreadable harp dir is not this check's job
		}
		for _, name := range names {
			flagged = append(flagged, harp+"/"+name)
		}
	}
	if len(flagged) == 0 {
		return DoctorCheck{Marker: marker, Status: DoctorOK,
			Detail: "no authored files sit in a harp directory's unclassified top level"}
	}
	sort.Strings(flagged)
	shown := flagged
	var more int
	if len(shown) > doctorHarpDurabilityMaxNamed {
		shown = shown[:doctorHarpDurabilityMaxNamed]
		more = len(flagged) - doctorHarpDurabilityMaxNamed
	}
	list := strings.Join(shown, ", ")
	if more > 0 {
		list += fmt.Sprintf(", … +%d more", more)
	}
	return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: fmt.Sprintf(
		"%d authored file(s) sit in a harp directory's unclassified top level, which is neither %s/ (durable, mounted into containers) nor a disposable member: %s — move each under its session's %s/ directory, where a containerized run keeps it",
		len(flagged), paths.PersistDirName, list, paths.PersistDirName)}
}

// doctorIsRemoteBundle reports whether a listing name is a REMOTE bundle — one
// pulled from a forge, and therefore one a publisher signature is expected for.
//
// Local project bundles and companion loadouts legitimately carry no
// publisher signature: local content is trusted by provenance, and a
// companion's bytes are verified by its own loadout envelope. Flagging them
// would put a warning on every healthy project, which is how a check trains
// users to ignore it.
func doctorIsRemoteBundle(name string) bool {
	// The LOCAL and COMPANION sources are scheme-qualified too, so they parse as
	// canonical refs — "ctxloom:companion@ltk" is a perfectly well-formed
	// canonical ref. Excluding them by prefix rather than by parse result is the
	// difference between a check that fires on a real gap and one that fires on
	// every project that has ltk installed.
	if strings.HasPrefix(name, remote.LocalSource+"@") || strings.HasPrefix(name, remote.CompanionSource+"@") {
		return false
	}
	ref, err := remote.ParseReference(name)
	return err == nil && ref.IsCanonical()
}

// StartupFindingsMarker is the DOCTOR-CHECK row under which the findings a
// launch recorded and proceeded past are delivered. It has no counterpart in
// `ctxloom doctor` because doctor is a separate process and cannot see what a
// run recorded; the run is the only place these rows can be produced.
const StartupFindingsMarker = "DOCTOR-CHECK-STARTUP-FINDINGS-x4"

// StartupFindings is what a started agent is told about the ground it stands
// on, scoped to ONE launch: the findings the launch itself recorded (config
// warnings, a degraded isolation axis, a sync or coordinator fault —
// everything a --degraded run proceeded past; in strict mode the gates
// already aborted on any of these, so the list is empty), plus doctor's own
// checks of the state this run reads — the marker and config validity, the
// companion decisions, and the local-only paths a fresh clone has no way to
// know it lacks. The rows ARE doctor's rows: same markers, same wording.
//
// Only what is NOT the intended state is a finding. An ok row is omitted; an
// info row is context, not a verdict, and is omitted too. The companions row
// is the exception that proves the rule: doctor reports it ok even when a
// companion was withheld, so it is selected on the decision itself.
func StartupFindings(app *App, cfg *config.Config, home string, recorded []strictness.Finding) DoctorReport {
	var checks []DoctorCheck
	for _, f := range recorded {
		detail := "[" + string(f.Class) + "] " + f.Message
		if f.FixIt != "" {
			detail += " (fix: " + f.FixIt + ")"
		}
		checks = append(checks, DoctorCheck{Marker: StartupFindingsMarker, Status: DoctorWarn, Detail: detail})
	}
	for _, c := range []DoctorCheck{
		doctorCheckSetupMarker(cfg, nil),
		doctorCheckLocalTierState(cfg, home),
	} {
		if c.Status == DoctorWarn {
			checks = append(checks, c)
		}
	}
	if !app.NoCompanions && readCompanionDecisions(cfg).withheld() {
		checks = append(checks, doctorCheckSetupCompanions(cfg, nil, app.NoCompanions))
	}
	return DoctorReport{Checks: checks}
}
