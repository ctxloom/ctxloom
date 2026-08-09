package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/gitignore"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/signing/agentkey"
)

// doctorDepBinariesRequired are the non-engine binaries ctxloom's own
// features genuinely hard-depend on regardless of which engines are
// configured: git — worktree isolation shells `git worktree` (internal/lm/
// isolation), remote clone/pull reads/writes real git repos (internal/
// remote/repo_cache.go, internal/git/exec.go), and `ctxloom init`/`manage
// install` themselves clone the default remote. A machine without git
// silently can't do worktrees or pull content; this makes that a visible
// DOCTOR-CHECK-DEPS-a1 warn instead.
var doctorDepBinariesRequired = []string{"git"}

// doctorDepBinariesRecommended are binaries ctxloom ITSELF never execs —
// grepped repo-wide: no exec.Command/LookPath("ssh") or ("ssh-keygen")
// anywhere but this probe and init PRIME's mirror of it (checkSystemDeps,
// init.go) — but that are still worth flagging present:
//
//   - ssh is what `git` ITSELF shells out to for an ssh:// or git@host:
//     remote (irrelevant for the default HTTPS remote ctxloom seeds).
//   - ssh-keygen is the tool a user without an existing SSH key would run BY
//     HAND to make one (`ssh-keygen -t ed25519-sk` — the fix review.go's/
//     agentkey.go's own messages already suggest); ctxloom never runs it
//     for them.
//
// NEITHER is a signing dependency (an earlier version of this comment/the
// Detail below wrongly implied both were "for signing" — an audit caught
// it): ctxloom's signing is pure Go over the ssh-agent protocol
// (SSH_AUTH_SOCK — internal/signing/agentkey/agentkey.go's dialEnvAgent
// net.Dial("unix", ...), never exec) and pure-Go sshsig cryptography
// (internal/signing/sign.go's Sign/Verify, internal/signing/publisher.go's
// VerifyPublisher — both explicitly documented "no ssh-keygen binary" in
// their own doc comments). Their absence still warns (worth having,
// especially ssh-keygen if you don't yet have a key to sign with) but the
// Detail text below says what they're actually for, not "signing".
var doctorDepBinariesRecommended = []string{"ssh", "ssh-keygen"}

// doctorEngineBinaries maps each registered engine backend name to the
// native CLI ctxloom would launch for it, for the DOCTOR-CHECK-DEPS-a1 PATH
// probe. Only backends with a real external client binary are listed.
var doctorEngineBinaries = map[string]string{
	"claude-code": "claude",
	"codex":       "codex",
	"kiro":        "kiro-cli",
	"antigravity": "agy",
	"opencode":    "opencode",
}

// ACP-adapter probing no longer keys off a hardcoded name->binary map here.
// Every registered backend declares its own transport once
// (agent.ACPTransport, set on its internal/lm/backends agentDescriptor) —
// acpAdapterDetail below reads that ONE declaration via
// backends.ACPTransportFor, asking only "is this engine's Kind ==
// agent.ACPAdapter" instead of consulting a second, hand-maintained table
// that could drift from what claude/codex's Chat() gates themselves check.
// kiro/opencode declare agent.ACPNative (they speak ACP natively — no
// separate adapter to probe) and antigravity declares agent.ACPBespoke (no
// ACP at all) — both are correctly skipped by that Kind check, the same
// outcome the old map's absence produced, but derived from the SAME source
// of truth as the Chat() gate instead of a second copy of it.

// doctorStatus is one check's verdict, and there are exactly three of them. It
// is a named type rather than a bare string because the value set IS the
// contract: it is shared with the "ctxloom-doctor" Agent Skill and with every
// consumer of `doctor --format json`, and it was previously written as a free
// literal at more than twenty sites with the legal values recorded only in a
// trailing comment — where a typo ("WARN", "warning") would render, marshal and
// pass review while silently reading as neither ok nor warn to anything that
// matches on the value. The underlying type stays string, so the JSON wire shape
// is unchanged.
type doctorStatus string

const (
	// doctorOK: the check's subject is in the state setup intends.
	doctorOK doctorStatus = "ok"
	// doctorWarn: this command's fail-loud signal. doctor never fails the
	// process, so a warn is how a real problem is reported.
	doctorWarn doctorStatus = "warn"
	// doctorInfo: reported for context, not a verdict — nothing to fix.
	doctorInfo doctorStatus = "info"
)

// doctorCheck is one named check's outcome. Marker is the DOCTOR-CHECK-*
// vocabulary this command shares with the "ctxloom-doctor" Agent Skill, so a
// human or an LLM reading either surface sees one language.
type doctorCheck struct {
	Marker string       `json:"marker"`
	Status doctorStatus `json:"status"`
	Detail string       `json:"detail"`
}

// doctorReport is `ctxloom doctor`'s structured result.
type doctorReport struct {
	Checks []doctorCheck `json:"checks"`
}

// doctorDepsOnlyFlag backs --deps: scopes the report to ONLY the machine-
// capability probes (DEPS-a1's git/ssh/ssh-keygen/container runtime/each
// configured engine's client, SIGNKEY-k1, GITIDENT-l2, and ACPADAPTER-m3) —
// questions that are true-or-false regardless of whether a project has been
// set up yet. init's PRIME and the setup skill's phase 1 run in THIS mode:
// full `doctor` on a brand-new, never-set-up project is a wall of
// expected-missing state (no agents, no profiles, no hooks wired) that
// would needlessly alarm a user at the very start of the setup that's about
// to configure those things.
var doctorDepsOnlyFlag bool

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Run deterministic setup checks (deps, agents, hooks, MCP, companions, trust)",
	Long: `Run ctxloom's deterministic setup checks — this IS the init-as-skill setup
skill's Phase 6 postcondition check (init-as-skill.plan.md §8.2): the
.ctxloom marker + config validity; required binaries on PATH (git, each
configured engine's own client, a container runtime when this project runs
'runtime: container' agents, and — recommended, not required — ssh/ssh-keygen);
whether the ACP adapter binary (claude-code-acp/
codex-acp) each configured claude-code/codex engine needs for HOST-runtime
structured chat is present; whether every configured agent resolves (profile
composition + engine/runtime) and the roster is non-empty; the seeded
dependency lockfile parses and a real context assembly succeeds; hooks AND
MCP registration per configured backend; the trust store's signers;
companion detection + loadout probing (taskloom/ltk/...); every
paths.TierLocal path (internal/paths.Layout) this checkout is missing — the
local-only state (the dirty-tree-commit acknowledgement, the task-log
project-id marker, distilled sessions, review's cached diff objects) that a
fresh clone has no way to learn it lacks anywhere else; and, always, a stated
reminder of the one boundary no check here crosses: ctxloom can confirm it
WROTE the assembled context onto the engine's own surface, never that the
engine actually READ it — that happens inside a process ctxloom does not own.
Each line is prefixed with a DOCTOR-CHECK-* marker — the SAME vocabulary the
"ctxloom-doctor" Agent Skill uses, so a human or an LLM reading either
surface sees one language.

Version currency has no dedicated check here (best-effort, skill-guided):
compare 'ctxloom version' against your remote's newest tag by hand, or ask
an assistant carrying the ctxloom-doctor skill to do it.

Deliberately does NOT parse any third-party ACP client config (Zed settings,
Nori's config.toml, VSCode acp-client, Toad, ...): client verification is
that config's own AGENT's re-read + live connect, never this command's job —
ctxloom stays unbound to any one frontend (init-as-skill.plan.md §6).

--deps scopes the report to ONLY the machine-capability probes (git/ssh/
ssh-keygen, a container runtime, any already-configured engine's client and
its ACP adapter if it needs one, signing-key readiness, and git identity) —
no agents/profiles/hooks/trust checks, so it reads clean on a project that
hasn't been set up yet. This is the mode init's PRIME and the setup skill's
phase 1 use, before there's anything else to check.

Diagnostic only: no check outcome ever fails the command, and nothing is
blocked or changed. A "warn" status IS this command's fail-loud signal — read
the report, don't grep the exit code. A usage error is still an error (e.g. a
--format value this build cannot render).`,
	Args: cobra.NoArgs,
	RunE: runDoctorCmd,
}

func runDoctorCmd(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	cfg, cfgErr := GetConfig()
	var checks []doctorCheck
	if doctorDepsOnlyFlag {
		checks = []doctorCheck{
			doctorCheckDeps(cfg),
			doctorCheckSignKey(ctx, cfg, agentkey.NewDiscoverer()),
			doctorCheckGitIdentity(ctx, agentkey.NewDiscoverer().GitConfig),
			doctorCheckACPAdapter(cfg),
		}
	} else {
		checks = []doctorCheck{
			doctorCheckSetupMarker(cfg, cfgErr),
			doctorCheckDeps(cfg),
			doctorCheckSignKey(ctx, cfg, agentkey.NewDiscoverer()),
			doctorCheckGitIdentity(ctx, agentkey.NewDiscoverer().GitConfig),
			doctorCheckACPAdapter(cfg),
			doctorCheckAgents(ctx, cfg, cfgErr),
			doctorCheckVersion(),
			doctorCheckHooksTrust(ctx, cfg, cfgErr),
			doctorCheckContentTrust(cfg, cfgErr),
			doctorCheckUpstreamSignatures(cfg, cfgErr),
			doctorCheckSetupLockAndAssembly(ctx, cfg, cfgErr),
			doctorCheckSetupCompanions(cfg, cfgErr),
			doctorCheckSetupAuthPing(),
			doctorCheckIngestionLimit(cfg),
			doctorCheckLocalTierState(cfg),
			doctorCheckGitignorePosture(cfg, cfgErr),
			doctorCheckForeignWorktrees(ctx, git.NewExec(), doctorProjectDir(cfg)),
			doctorCheckHarpDurability(),
		}
	}
	report := doctorReport{Checks: checks}
	return emit(cmd, report, func() error { return renderDoctorReport(cmd.OutOrStdout(), report) })
}

// doctorConfiguredEngines returns the sorted, de-duplicated set of registered
// backend names (claude-code/codex/...) every configured agent's Engine
// label resolves to. nil cfg (config failed to load) yields none — the
// deps check then reports only the engine-independent binaries.
func doctorConfiguredEngines(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	set := map[string]bool{}
	for _, a := range cfg.GetConfiguredAgents() {
		backend, _ := operations.ResolveBackend(cfg, a.Engine)
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
// is resolved exactly as operations.ResolveAgent resolves it (agents.go: the
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
	if projectDefault == agent.RuntimeContainer {
		return true
	}
	for _, a := range cfg.GetConfiguredAgents() {
		runtime := a.Runtime
		if runtime == "" {
			runtime = projectDefault
		}
		if runtime == agent.RuntimeContainer {
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
func doctorCheckDeps(cfg *config.Config) doctorCheck {
	const marker = "DOCTOR-CHECK-DEPS-a1"
	missingRequired := doctorMissingFromPath(doctorDepBinariesRequired)
	missingRequired = append(missingRequired, doctorMissingEngineClients(cfg)...)
	missingRecommended := doctorMissingFromPath(doctorDepBinariesRecommended)
	if !(isolation.Docker{}.Available()) && !(isolation.Podman{}.Available()) {
		if doctorContainerRuntimeRequired(cfg) {
			missingRequired = append(missingRequired,
				"docker/podman (container runtime — this project runs container agents)")
		} else {
			missingRecommended = append(missingRecommended,
				"docker/podman (container runtime — needed only to run `runtime: container` agents, which this project configures none of)")
		}
	}
	if len(missingRequired) == 0 && len(missingRecommended) == 0 {
		return doctorCheck{Marker: marker, Status: doctorOK,
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
	return doctorCheck{Marker: marker, Status: doctorWarn, Detail: strings.Join(parts, "; ")}
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
// engine whose native client is not on PATH. Engines with no external client
// binary (none listed in doctorEngineBinaries) are skipped rather than reported
// as missing.
func doctorMissingEngineClients(cfg *config.Config) []string {
	var missing []string
	for _, engine := range doctorConfiguredEngines(cfg) {
		bin, ok := doctorEngineBinaries[engine]
		if !ok {
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
// approve path AND `ctxloom sign`/`--sign` both use (internal/signing/
// agentkey.Discoverer.Discover — see review.go's resolveReviewSigner and
// sign.go's runSign) rather than re-deriving discovery here. Read-only:
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
// checkSystemDeps, see init.go) beats a user hitting agentkey.NoKeyError or
// the unsigned-approval prompt cold at their first real `ctxloom review`/
// `ctxloom sign`.
func doctorCheckSignKey(ctx context.Context, cfg *config.Config, discoverer *agentkey.Discoverer) doctorCheck {
	const marker = "DOCTOR-CHECK-SIGNKEY-k1"
	explicit := ""
	if cfg != nil {
		explicit = cfg.SignKey()
	}
	ok, detail := signKeyResolutionDetail(ctx, discoverer, explicit)
	if ok {
		return doctorCheck{Marker: marker, Status: doctorOK, Detail: detail}
	}
	return doctorCheck{Marker: marker, Status: doctorWarn, Detail: detail}
}

// signKeyResolutionDetail runs internal/signing/agentkey's real resolution
// chain (explicit --key/sign.key, then `git config user.signingkey`, then
// ssh-agent's sole identity — agentkey.go's package doc) and renders the
// outcome as a short, actionable line. Shared between doctorCheckSignKey and
// init PRIME's checkSystemDeps (init.go) so both surfaces say the exact same
// thing about the exact same resolver, rather than drifting apart.
//
// ok=true names the resolved key the way sign.go's printSignResult already
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
func signKeyResolutionDetail(ctx context.Context, discoverer *agentkey.Discoverer, explicit string) (ok bool, detail string) {
	discovered, err := discoverer.Discover(ctx, explicit)
	if err == nil {
		// A probe never signs, so the agent connection is released as soon as
		// the identity has been described.
		defer func() { _ = discovered.Close() }()
		return true, fmt.Sprintf("signing key resolves via %s (%s)", discovered.Source, discovered.Fingerprint)
	}

	const why = "needed to approve reviewed content (`ctxloom review`) and to publish or sign your own content (`ctxloom sign`) — merely consuming already-trusted/embedded content does not require a signing key"

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
// agentkey.NewDiscoverer()) — already used to resolve user.signingkey.
// doctorCheckGitIdentity reuses it verbatim for user.name/user.email rather
// than shelling out to git a second, bespoke way.
type gitConfigFunc = func(ctx context.Context, dir, key string) (value string, ok bool, err error)

// doctorCheckGitIdentity is a machine-capability probe like DOCTOR-CHECK-
// DEPS-a1/SIGNKEY-k1 (included in --deps scope): it verifies git's commit
// identity — BOTH user.name AND user.email — is explicitly resolvable via
// `git config` (any scope: local/global/system; `git config --get` already
// searches all three, so this asks nothing beyond what git itself would use
// right now). WHY: agents ctxloom launches do their own work — including
// their own `git commit` — inside isolated worktrees (internal/lm/isolation/
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
func doctorCheckGitIdentity(ctx context.Context, gitConfig gitConfigFunc) doctorCheck {
	const marker = "DOCTOR-CHECK-GITIDENT-l2"
	ok, detail := gitIdentityDetail(ctx, gitConfig)
	if ok {
		return doctorCheck{Marker: marker, Status: doctorOK, Detail: detail}
	}
	return doctorCheck{Marker: marker, Status: doctorWarn, Detail: detail}
}

// gitIdentityDetail runs gitConfig for user.name and user.email and renders
// the outcome as a short, actionable line. Shared between
// doctorCheckGitIdentity and init PRIME's checkSystemDeps (init.go) so both
// surfaces say the exact same thing, rather than drifting apart.
func gitIdentityDetail(ctx context.Context, gitConfig gitConfigFunc) (ok bool, detail string) {
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

// doctorCheckACPAdapter is a machine-capability probe like DOCTOR-CHECK-
// DEPS-a1/SIGNKEY-k1/GITIDENT-l2 (included in --deps scope): the ACP
// adapter (claude-code-acp, codex-acp) is a SEPARATE npm-installed CLI
// (needs node), distinct from the engine's own client binary DEPS-a1
// already checks (doctorEngineBinaries) — internal/claude/chat.go's Chat()
// (mirrored by internal/codex/chat.go's) HARD-FAILS host-runtime
// structured chat if the adapter is missing on PATH. Structured chat is the
// transport for BOTH agent_run cross-engine delegation AND the `ctxloom
// acp` client surface (the steady-state surface users are pointed at), so a
// missing adapter silently breaks both even though the raw-CLI bootstrap
// interview never touches this path.
//
// For every CONFIGURED engine whose declared agent.ACPTransport.Kind is
// agent.ACPAdapter (backends.ACPTransportFor; kiro/opencode declare
// ACPNative and antigravity ACPBespoke — none of the three need a probe),
// this checks the adapter resolves on PATH — reusing the SAME
// configured-engine enumeration doctorCheckDeps uses for the client binary
// (doctorConfiguredEngines), never re-deriving "which engines are
// configured" a second way.
//
// Read-only, never blocks: like SIGNKEY-k1/GITIDENT-l2 beside it, a missing
// adapter is advisory only, and specifically NOT a problem for every agent:
// a runtime:container agent's image carries its own adapter (chat.go's
// `req.Runtime != agent.RuntimeContainer` gate — the host process's PATH is
// never consulted for a containerized run), so the warn below says so
// explicitly rather than reading as a universal blocker.
func doctorCheckACPAdapter(cfg *config.Config) doctorCheck {
	const marker = "DOCTOR-CHECK-ACPADAPTER-m3"
	ok, detail := acpAdapterDetail(doctorConfiguredEngines(cfg))
	if ok {
		return doctorCheck{Marker: marker, Status: doctorOK, Detail: detail}
	}
	return doctorCheck{Marker: marker, Status: doctorWarn, Detail: detail}
}

// acpAdapterDetail checks, for every engine in configuredEngines whose
// declared agent.ACPTransport.Kind is agent.ACPAdapter
// (backends.ACPTransportFor), whether that adapter binary resolves on PATH,
// and renders the outcome as a short, actionable line. Shared between
// doctorCheckACPAdapter and init PRIME's
// checkSystemDeps (init.go) so both surfaces say the exact same thing about
// the exact same binaries, rather than drifting apart — mirrors
// signKeyResolutionDetail/gitIdentityDetail's shared-detail shape above.
func acpAdapterDetail(configuredEngines []string) (ok bool, detail string) {
	type gap struct{ engine, bin, installCmd string }
	var applicable []string
	var gaps []gap
	for _, engine := range configuredEngines {
		transport := backends.ACPTransportFor(engine)
		if transport.Kind != agent.ACPAdapter {
			continue
		}
		applicable = append(applicable, engine)
		if _, err := exec.LookPath(transport.Binary); err != nil {
			gaps = append(gaps, gap{engine, transport.Binary, transport.InstallCmd})
		}
	}
	if len(applicable) == 0 {
		return true, "no configured engine needs a separate ACP adapter (kiro/opencode speak ACP natively, antigravity has no adapter subprocess at all; claude-code/codex — the only engines that DO need one — are not configured)"
	}
	if len(gaps) == 0 {
		return true, fmt.Sprintf("ACP adapter present for every configured engine that needs one (%s)", strings.Join(applicable, ", "))
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i].engine < gaps[j].engine })
	var missing, installs []string
	for _, g := range gaps {
		missing = append(missing, fmt.Sprintf("%s (%s)", g.bin, g.engine))
		installs = append(installs, g.installCmd)
	}
	return false, fmt.Sprintf(
		"missing ACP adapter: %s — install: %s; needed for HOST-runtime structured chat (agent_run cross-engine delegation and the `ctxloom acp` client surface) — containerized agents (runtime: container) get the adapter from their own image, not host PATH, so this is not a problem for them",
		strings.Join(missing, ", "), strings.Join(installs, "; "))
}

// doctorCheckAgents resolves every configured agent (profile composition +
// engine/runtime) and reports the first failure, or how many resolved
// cleanly. An empty roster is a WARN, not a neutral fact: init-as-skill's
// setup postcondition (§8.2) requires "agents non-empty with resolvable
// profiles", and doctor IS that postcondition check now — a management-only
// project with genuinely zero agents is rare enough that silently calling it
// "info" would hide the far more common case, an interrupted/incomplete
// setup.
func doctorCheckAgents(ctx context.Context, cfg *config.Config, cfgErr error) doctorCheck {
	if cfgErr != nil {
		return doctorCheck{Marker: "DOCTOR-CHECK-AGENTS-b2", Status: doctorWarn, Detail: "config did not load: " + cfgErr.Error()}
	}
	configuredAgents := cfg.GetConfiguredAgents()
	if len(configuredAgents) == 0 {
		return doctorCheck{Marker: "DOCTOR-CHECK-AGENTS-b2", Status: doctorWarn,
			Detail: "no agents configured (run `/ctxloom-init` phase 5, or `ctxloom agent set <name> ...`)"}
	}
	names := make([]string, 0, len(configuredAgents))
	for name := range configuredAgents {
		names = append(names, name)
	}
	sort.Strings(names)
	var failed []string
	for _, name := range names {
		if _, err := operations.ResolveAgent(ctx, cfg, name, ""); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", name, err))
		}
	}
	if len(failed) == 0 {
		return doctorCheck{Marker: "DOCTOR-CHECK-AGENTS-b2", Status: doctorOK,
			Detail: fmt.Sprintf("%d agent(s) resolve cleanly: %s", len(names), strings.Join(names, ", "))}
	}
	return doctorCheck{Marker: "DOCTOR-CHECK-AGENTS-b2", Status: doctorWarn, Detail: strings.Join(failed, "; ")}
}

// doctorCheckVersion is deliberately best-effort/skill-guided: there is no
// update-check infrastructure (internal/cli/version.go is a bare print), so
// this reports the running version and defers comparison to the
// ctxloom-doctor skill (or a human) rather than faking a currency verdict.
func doctorCheckVersion() doctorCheck {
	return doctorCheck{Marker: "DOCTOR-CHECK-VERSION-c3", Status: doctorInfo,
		Detail: fmt.Sprintf("running %s; comparing against the newest remote tag is best-effort/skill-guided (no --check-version yet)", Version)}
}

// doctorCheckHooksTrust cross-references doctorConfiguredEngines (every
// backend a configured agent resolves to) against operations.HarnessStatus —
// the SAME read `ctxloom manage check`/`ctxloom manage hooks check` already
// expose — reporting hooks AND MCP registration per backend (a real
// read, not a bare file-existence guess), plus how many signers the trust
// store carries (operations.ListSigners — always includes the embedded root,
// so a healthy store is never reported as empty).
func doctorCheckHooksTrust(ctx context.Context, cfg *config.Config, cfgErr error) doctorCheck {
	const marker = "DOCTOR-CHECK-HOOKS-TRUST-d4"
	if cfgErr != nil {
		return doctorCheck{Marker: marker, Status: doctorWarn, Detail: "config did not load: " + cfgErr.Error()}
	}
	status := doctorOK
	hooks, hooksOK := doctorHooksWiringDetail(ctx, cfg)
	trust, trustOK := doctorTrustStoreDetail(operations.ListSigners(cfg, nil))
	if !hooksOK || !trustOK {
		status = doctorWarn
	}
	return doctorCheck{Marker: marker, Status: status, Detail: strings.Join([]string{hooks, trust}, "; ")}
}

// doctorHooksWiringDetail reports hooks + MCP registration for every backend a
// configured agent resolves to, reading operations.HarnessStatus (the SAME read
// `ctxloom manage check` exposes). ok=false is the caller's warn signal.
func doctorHooksWiringDetail(ctx context.Context, cfg *config.Config) (detail string, ok bool) {
	configured := doctorConfiguredEngines(cfg)
	if len(configured) == 0 {
		return "hooks/MCP: no engine is configured to check", true
	}
	result, err := operations.HarnessStatus(ctx, cfg, operations.HarnessStatusRequest{})
	if err != nil {
		return "hooks/MCP: " + err.Error(), false
	}
	byBackend := make(map[string]operations.BackendWiring, len(result.Backends))
	for _, b := range result.Backends {
		byBackend[b.Backend] = b
	}
	var present, missing []string
	for _, name := range configured {
		if b, found := byBackend[name]; !found || !b.SettingsExists || !b.HooksPresent {
			missing = append(missing, name)
			continue
		}
		present = append(present, name)
	}
	sort.Strings(present)
	sort.Strings(missing)
	if len(missing) > 0 {
		return "hooks/MCP NOT registered for: " + strings.Join(missing, ", ") + " (run `ctxloom manage install`)", false
	}
	return "hooks/MCP registered for: " + strings.Join(present, ", "), true
}

// doctorTrustStoreDetail reports how much trust the store actually grants, from
// operations.ListSigners' (listing, error) pair.
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
func doctorTrustStoreDetail(signers []operations.SignerListing, err error) (detail string, ok bool) {
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
	if len(unreadable) > 0 {
		sort.Strings(unreadable)
		return fmt.Sprintf("trust store: %d active signer(s), and %d entr(y/ies) that could not be read and grant NO trust: %s",
			active, len(unreadable), strings.Join(unreadable, "; ")), false
	}
	return fmt.Sprintf("trust store: %d active signer(s)", active), true
}

// ===== init-as-skill Phase 6 postcondition checks (plan.md §8.2) =====
//
// These compose the SAME operations/config entry points every other command
// already uses (config.Config, operations.AssembleContext, config.
// DiscoverCompanions/ProbeCompanions/ProbeCompanionLoadouts) rather than
// re-implementing any of their logic — this file's job is to CALL them and
// translate the result into a doctorCheck, never to re-derive what "locked",
// "resolves", or "registered" means. Deliberately absent: anything that
// opens a third-party client's own config file (Zed settings.json, Nori's
// config.toml, ...) — see doctorCmd's Long text and init-as-skill.plan.md
// §6/§8.2. The remaining two §8.2 items — agents non-empty/resolvable and
// hooks/MCP registered per backend — are folded directly into
// doctorCheckAgents and doctorCheckHooksTrust above rather than duplicated
// here: doctor's OWN pre-existing checks already covered that ground, they
// just needed a stricter (WARN, not INFO) reading of the empty/missing case.

// doctorAppDir mirrors operations.getBaseDir's fallback (unexported there,
// remotes.go): the .ctxloom directory config.Load already resolved, or ""
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

// doctorCheckSetupMarker verifies the .ctxloom marker directory config.Load
// already resolved (cfg.AppPaths, config.go:137) is present and the project
// config loaded without a hard error — the ground-floor precondition every
// other check in this report assumes. Read-only: it inspects config.Load's
// ALREADY-resolved record instead of re-globbing the filesystem for .ctxloom.
func doctorCheckSetupMarker(cfg *config.Config, cfgErr error) doctorCheck {
	const marker = "DOCTOR-CHECK-SETUP-MARKER-e5"
	if cfgErr != nil {
		return doctorCheck{Marker: marker, Status: doctorWarn, Detail: "config did not load: " + cfgErr.Error()}
	}
	appDir := doctorAppDir(cfg)
	if appDir == "" {
		return doctorCheck{Marker: marker, Status: doctorWarn,
			Detail: "no .ctxloom marker directory found (run `ctxloom manage install` or `ctxloom init`)"}
	}
	return doctorCheck{Marker: marker, Status: doctorOK, Detail: "marker present, config valid: " + appDir}
}

// doctorCheckSetupLockAndAssembly verifies the two "seeded deps are actually
// usable" postconditions the setup skill's phase 4/5 promise: the lockfile
// (remote.LockfileManager — the SAME reader `ctxloom sync`/`lock` use) parses
// without error, and a real context assembly (operations.AssembleContext —
// the SAME entry point `ctxloom run`'s configured-default path uses) succeeds
// end to end. AssembleContext exercises the trust gate, companion-loadout
// seeding, and fragment/profile resolution for real; none of that is
// reimplemented here.
func doctorCheckSetupLockAndAssembly(ctx context.Context, cfg *config.Config, cfgErr error) doctorCheck {
	const marker = "DOCTOR-CHECK-SETUP-DEPS-h8"
	if cfgErr != nil {
		return doctorCheck{Marker: marker, Status: doctorWarn, Detail: "config did not load: " + cfgErr.Error()}
	}
	var parts []string
	status := doctorOK
	if appDir := doctorAppDir(cfg); appDir == "" {
		parts = append(parts, "lockfile: no .ctxloom directory to check")
		status = doctorWarn
	} else {
		lf, err := remote.NewLockfileManager(appDir).Load()
		if err != nil {
			parts = append(parts, "lockfile: "+err.Error())
			status = doctorWarn
		} else {
			parts = append(parts, fmt.Sprintf("lockfile: %d entries parse cleanly", len(lf.AllEntries())))
		}
	}
	if _, err := operations.AssembleContext(ctx, cfg, operations.AssembleContextRequest{}); err != nil {
		parts = append(parts, "context assembly: "+err.Error())
		status = doctorWarn
	} else {
		parts = append(parts, "context assembly: succeeds for the configured default profile(s)")
	}
	return doctorCheck{Marker: marker, Status: status, Detail: strings.Join(parts, "; ")}
}

// doctorCheckSetupCompanions reports companion discovery + loadout probing —
// the SAME two-stage protocol (config.DiscoverCompanions, config.
// ProbeCompanions, config.ProbeCompanionLoadouts) AssembleContext's
// context assembly already runs internally for a real session. Reporting
// only: a project with no companions installed is not misconfigured (they
// are optional add-ons), so this is never a "warn"; it respects
// --no-companions/CTXLOOM_NO_COMPANIONS (config.CompanionsDisabled) by
// reporting that instead of executing companion binaries a second time.
func doctorCheckSetupCompanions(cfg *config.Config, cfgErr error) doctorCheck {
	const marker = "DOCTOR-CHECK-SETUP-COMPANIONS-i9"
	if cfgErr != nil {
		return doctorCheck{Marker: marker, Status: doctorWarn, Detail: "config did not load: " + cfgErr.Error()}
	}
	if config.CompanionsDisabled() {
		return doctorCheck{Marker: marker, Status: doctorInfo, Detail: "companion probing disabled (--no-companions)"}
	}
	bins := config.DiscoverCompanions()
	if len(bins) == 0 {
		return doctorCheck{Marker: marker, Status: doctorInfo, Detail: "no companions discovered"}
	}
	// "on PATH" and "actually run" are now different facts: a companion whose
	// EXECUTION nobody confirmed is present and skipped. Reporting only the
	// first would tell a user their companion is fine while it contributes
	// nothing — the exact silent no-op doctor exists to surface.
	var present, notRun []string
	for _, st := range config.ProbeCompanions() {
		if st.Path == "" {
			continue
		}
		present = append(present, st.Bin)
		if !st.Executed() {
			notRun = append(notRun, fmt.Sprintf("%s (%s)", st.Bin, st.Admission))
		}
	}
	// Count the loadouts as the READER reported them, rather than probing a
	// second time: asking the loader what it read cannot exec a companion
	// again, and it counts what a session would actually carry.
	loadouts := 0
	for _, read := range cfg.BundleLoader().Reads() {
		if read.Provenance == bundles.ProvenanceCompanion {
			loadouts++
		}
	}
	presentDetail := "(none on PATH)"
	if len(present) > 0 {
		presentDetail = strings.Join(present, ", ")
	}
	notRunDetail := ""
	if len(notRun) > 0 {
		notRunDetail = fmt.Sprintf("; NOT RUN: %s — allow with 'ctxloom trust companion allow <path>'", strings.Join(notRun, ", "))
	}
	return doctorCheck{Marker: marker, Status: doctorOK, Detail: fmt.Sprintf(
		"discovered: %s; on PATH: %s; loadouts read: %d%s",
		strings.Join(bins, ", "), presentDetail, loadouts, notRunDetail)}
}

// doctorCheckSetupAuthPing is a placeholder. init-as-skill's USER RULING (a)
// wants a deterministic auth ping BEFORE the raw-CLI vendor TUI launches, but
// no such surface exists anywhere in this codebase yet (grepped: no
// AuthPing/auth-ping symbol) — that is a different slice's work (init-as-
// skill.plan.md §10④, init bootstrap rework), not this one's. Reported as
// "info" so the gap is VISIBLE in the postcondition report instead of
// silently missing.
func doctorCheckSetupAuthPing() doctorCheck {
	return doctorCheck{Marker: "DOCTOR-CHECK-SETUP-AUTHPING-j0", Status: doctorInfo,
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
func doctorCheckIngestionLimit(cfg *config.Config) doctorCheck {
	const marker = "DOCTOR-CHECK-INGESTION-q7"
	who := "the configured engine"
	if engines := doctorConfiguredEngines(cfg); len(engines) > 0 {
		who = strings.Join(engines, ", ")
	}
	return doctorCheck{Marker: marker, Status: doctorInfo, Detail: fmt.Sprintf(
		"ctxloom writes the assembled context onto %s's own on-disk agent surface; whether %s actually reads what was written happens inside a process ctxloom does not own, and nothing in this product can confirm it — verify by asking the engine itself",
		who, who)}
}

// doctorCheckLocalTierState reports every paths.TierLocal entry (paths.Layout)
// that is absent from THIS checkout — the thing a fresh clone has no way to
// learn today (config-layer-scope design doc, "The .ctxloom classification"):
// local-only state nothing rebuilds, so its absence is silent everywhere else
// (a clone gets no warning that it started a new task-log project-id, lost
// the dirty-tree-commit acknowledgement, has no distilled session history,
// or degraded review's diff to a full-content dump). TierCommitted/TierDerived
// entries are not reported here: a derived entry's absence is fine (its own
// Rebuild command produces it) and a committed entry's absence means the
// repository itself is incomplete, a different class of problem doctor's
// other checks (setup marker, lock/assembly) already cover.
func doctorCheckLocalTierState(cfg *config.Config) doctorCheck {
	const marker = "DOCTOR-CHECK-LOCAL-STATE-p6"
	appDir := doctorAppDir(cfg)
	if appDir == "" {
		return doctorCheck{Marker: marker, Status: doctorWarn,
			Detail: "no .ctxloom marker directory found; nothing to check"}
	}
	fsys := afero.NewOsFs()
	if cfg != nil && cfg.FS() != nil {
		fsys = cfg.FS()
	}
	root := filepath.Dir(appDir)

	var missing []string
	for _, entry := range paths.Layout() {
		if entry.Tier != paths.TierLocal {
			continue
		}
		exists, err := afero.Exists(fsys, filepath.Join(root, entry.Rel))
		if err != nil || exists {
			continue
		}
		missing = append(missing, fmt.Sprintf("%s (%s)", entry.Rel, entry.Lost))
	}
	if len(missing) == 0 {
		return doctorCheck{Marker: marker, Status: doctorOK, Detail: "every local-only state path is present"}
	}
	sort.Strings(missing)
	return doctorCheck{Marker: marker, Status: doctorWarn,
		Detail: fmt.Sprintf("%d local-only path(s) absent from this checkout, and nothing rebuilds them: %s",
			len(missing), strings.Join(missing, "; "))}
}

// renderDoctorReport writes the human-readable check list, one
// "DOCTOR-CHECK-* [status] detail" line per check, in the fixed order the
// checks were run.
func renderDoctorReport(out io.Writer, report doctorReport) error {
	w := iox.NewErrWriter(out)
	w.Println("ctxloom doctor")
	for _, c := range report.Checks {
		w.Printf("  %s [%s] %s\n", c.Marker, c.Status, c.Detail)
	}
	return w.Err()
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorDepsOnlyFlag, "deps", false,
		"check ONLY machine-capability dependencies (git/ssh/ssh-keygen/container runtime/configured engines' clients and ACP adapters/signing key/git identity) — skips agents/profiles/hooks/trust, for use before a project has been set up")
	rootCmd.AddCommand(doctorCmd)
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
func doctorCheckContentTrust(cfg *config.Config, cfgErr error) doctorCheck {
	const marker = "DOCTOR-CHECK-CONTENT-TRUST-n4"
	if cfgErr != nil {
		return doctorCheck{Marker: marker, Status: doctorWarn, Detail: "config did not load: " + cfgErr.Error()}
	}
	infos, err := cfg.BundleLoader().List()
	if err != nil {
		return doctorCheck{Marker: marker, Status: doctorWarn, Detail: "could not list bundles: " + err.Error()}
	}
	var unsigned []string
	for _, info := range infos {
		if info.Deleted || info.Signer != "" || !doctorIsRemoteBundle(info.Name) {
			continue
		}
		unsigned = append(unsigned, info.Name)
	}
	if len(unsigned) == 0 {
		return doctorCheck{Marker: marker, Status: doctorOK,
			Detail: "every remote bundle's content is attributable to a publisher this machine trusts"}
	}
	sort.Strings(unsigned)
	return doctorCheck{Marker: marker, Status: doctorWarn,
		Detail: fmt.Sprintf("%d remote bundle(s) are UNSIGNED to this machine, so their content is withheld from your assistant: %s "+
			"(the publisher never signed these bytes, or signed with a key you do not trust — `ctxloom trust signer create` to trust the key, or ask the publisher to sign)",
			len(unsigned), strings.Join(unsigned, ", "))}
}

// doctorCheckUpstreamSignatures names every revision `remote upgrade` REFUSED
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
// WARN RATHER THAN INFO, deliberately. doctorInfo means "nothing to fix", and
// something does need fixing — just not by the person reading it. It is never
// fatal: doctor fails no process, and this check in particular reports a
// project that is working correctly off its last verified pin.
//
// It makes no trust decision, parses no signature and re-verifies nothing: it
// reads what the upgrade round recorded, filtered by
// operations.LiveRefusedAdvances to those still describing the pin the lockfile
// actually holds, so a record left over from a world that has moved on is
// dropped rather than reported.
func doctorCheckUpstreamSignatures(cfg *config.Config, cfgErr error) doctorCheck {
	const marker = "DOCTOR-CHECK-UPSTREAM-SIGNATURES-o5"
	if cfgErr != nil {
		return doctorCheck{Marker: marker, Status: doctorWarn, Detail: "config did not load: " + cfgErr.Error()}
	}
	refused, err := operations.LiveRefusedAdvances(cfg)
	if err != nil {
		// Reported, never folded onto "nothing was refused": this record's one
		// job is to keep a fact from evaporating, so reading an unreadable
		// store as silence would reproduce the exact gap it closes.
		return doctorCheck{Marker: marker, Status: doctorWarn,
			Detail: "could not read the record of refused upgrades, so this check cannot say whether any revision was refused: " + err.Error()}
	}
	if len(refused) == 0 {
		return doctorCheck{Marker: marker, Status: doctorOK,
			Detail: "no upstream revision has been refused: every pin your last upgrade could advance landed on content whose publisher signature verifies"}
	}
	sort.Slice(refused, func(i, j int) bool { return refused[i].Identity < refused[j].Identity })
	var parts []string
	for _, r := range refused {
		parts = append(parts, fmt.Sprintf("%s at revision %s does not verify, so the pin is being kept at %s (refused %s)",
			r.Identity, shortSHA(r.ProposedSHA), shortSHA(r.KeptSHA), r.RefusedAt.Format("2006-01-02")))
	}
	return doctorCheck{Marker: marker, Status: doctorWarn,
		Detail: fmt.Sprintf("%d upstream revision(s) were REFUSED because the PUBLISHER's signature does not cover the bytes it sits beside: %s. "+
			"Nothing is wrong on this machine and nothing is withheld from your assistant — it is served the content at the kept pin. "+
			"There is nothing to configure here: the publisher must re-sign and republish, and `ctxloom remote upgrade` picks it up "+
			"and clears this the next time it runs",
			len(refused), strings.Join(parts, "; "))}
}

// ===== J001300 close-out: doctor checks 1, 4, 5 (of the feature's numbering) ====
//
// The feature file and its step definitions both describe "doctor's five new
// checks"; this file adds exactly THREE — the ones the J001300 close-out design
// doc (docs/design/j001300-closeout-surfaces.design.md §6, area 4) scopes as
// doctor's share of that journey, and the ones the corresponding scenarios'
// own comments number 1, 4 and 5. Checks 2 and 3 of that numbering belong to
// a different area of J001300 (worktrees/purge/lessons) and are not implemented
// here — see this slice's own commit message / the design doc's §1 table for
// where they actually land.

// doctorCheckGitignorePosture reports a superseded blanket `.ctxloom` ignore
// rule: under it, .ctxloom/content can never be committed at all, so a
// project cannot ship its own authored context. Read-only — it must never
// retire the rule itself, only report it; RetireSupersededFile (which does
// retire it) and this check now share exactly one detector,
// gitignore.SupersededBlanketLines, so the two can never disagree about what
// counts as superseded.
func doctorCheckGitignorePosture(cfg *config.Config, cfgErr error) doctorCheck {
	const marker = "DOCTOR-CHECK-GITIGNORE-f6"
	if cfgErr != nil {
		return doctorCheck{Marker: marker, Status: doctorWarn, Detail: "config did not load: " + cfgErr.Error()}
	}
	projectDir := doctorProjectDir(cfg)
	if projectDir == "" {
		return doctorCheck{Marker: marker, Status: doctorInfo, Detail: "no .ctxloom marker directory found; nothing to check"}
	}
	gitignorePath := filepath.Join(projectDir, ".gitignore")
	lines, err := gitignore.SupersededBlanketLines(gitignorePath)
	if err != nil {
		return doctorCheck{Marker: marker, Status: doctorWarn, Detail: "could not read .gitignore: " + err.Error()}
	}
	if len(lines) == 0 {
		return doctorCheck{Marker: marker, Status: doctorOK, Detail: ".gitignore carries no superseded blanket .ctxloom rule"}
	}
	return doctorCheck{Marker: marker, Status: doctorWarn, Detail: fmt.Sprintf(
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
func doctorCheckForeignWorktrees(ctx context.Context, g git.Git, workDir string) doctorCheck {
	const marker = "DOCTOR-CHECK-FOREIGN-WORKTREES-r8"
	if workDir == "" {
		return doctorCheck{Marker: marker, Status: doctorInfo, Detail: "no project directory to check"}
	}
	if g == nil {
		g = git.NewExec()
	}
	if !g.IsRepo(workDir) {
		return doctorCheck{Marker: marker, Status: doctorInfo, Detail: "not a git repository; nothing to check"}
	}
	ctx, cancel := context.WithTimeout(ctx, doctorForeignWorktreeTimeout)
	defer cancel()

	worktrees, err := g.WorktreeList(ctx, workDir)
	if err != nil {
		return doctorCheck{Marker: marker, Status: doctorWarn, Detail: "could not list worktrees: " + err.Error()}
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
		return doctorCheck{Marker: marker, Status: doctorOK,
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
	return doctorCheck{Marker: marker, Status: doctorWarn, Detail: detail}
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
// directory's TOP LEVEL — neither persist/ (mounted into containers, durable)
// nor ephemeral/ (deliberately excluded, scratch). A containerized agent
// writing a design note there writes into container-ephemeral space and
// loses it on exit.
//
// The walk is two-level: HomeSessionsDir()'s OWN top level holds index.yaml
// alongside the harp directories, so the OUTER iteration skips non-directory
// entries — an exclusion list aimed at the harp level (as an earlier version
// of this design proposed) would never see that file at all, since it never
// walks into a non-directory. Only the INNER iteration, one level under each
// harp dir, applies the essence/transcript name exclusions.
func doctorCheckHarpDurability() doctorCheck {
	const marker = "DOCTOR-CHECK-HARP-DURABILITY-s9"
	sessionsRoot, err := paths.HomeSessionsDir()
	if err != nil {
		return doctorCheck{Marker: marker, Status: doctorWarn, Detail: "cannot resolve sessions dir: " + err.Error()}
	}
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return doctorCheck{Marker: marker, Status: doctorOK, Detail: "no harp directories yet"}
		}
		return doctorCheck{Marker: marker, Status: doctorWarn, Detail: "cannot read sessions dir: " + err.Error()}
	}

	var flagged []string
	for _, e := range entries {
		if !e.IsDir() {
			// e.g. index.yaml, sitting at the sessions root's OWN top level
			// beside the harp directories — not a harp, never walked into.
			continue
		}
		harp := e.Name()
		inner, err := os.ReadDir(filepath.Join(sessionsRoot, harp))
		if err != nil {
			continue // best-effort; an unreadable harp dir is not this check's job
		}
		for _, ie := range inner {
			name := ie.Name()
			if ie.IsDir() {
				continue // persist/, ephemeral/, or any other subdir: not this check's target
			}
			switch name {
			case paths.EssenceFileName, paths.CanonicalTranscriptFileName, "transcript.acp.jsonl", paths.IndexFileName:
				continue
			}
			flagged = append(flagged, harp+"/"+name)
		}
	}
	if len(flagged) == 0 {
		return doctorCheck{Marker: marker, Status: doctorOK,
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
	return doctorCheck{Marker: marker, Status: doctorWarn, Detail: fmt.Sprintf(
		"%d authored file(s) sit in a harp directory's unclassified top level, which is neither persist/ (durable, mounted into containers) nor ephemeral/: %s — move them under persist/",
		len(flagged), list)}
}

// doctorIsRemoteBundle reports whether a listing name is a REMOTE bundle — one
// pulled from a forge, and therefore one a publisher signature is expected for.
//
// Local project bundles, companion loadouts and builtins all legitimately carry
// no publisher signature: local content is trusted by provenance, and a
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
