package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/operations"
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

// DoctorCheck is one named check's outcome. Marker is the DOCTOR-CHECK-*
// vocabulary this command shares with the "ctxloom-doctor" Agent Skill, so a
// human or an LLM reading either surface sees one language.
type DoctorCheck struct {
	Marker string `json:"marker"`
	Status string `json:"status"` // "ok" | "warn" | "info"
	Detail string `json:"detail"`
}

// DoctorReport is `ctxloom doctor`'s structured result.
type DoctorReport struct {
	Checks []DoctorCheck `json:"checks"`
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
configured engine's own client, a container runtime, and — recommended, not
required — ssh/ssh-keygen); whether the ACP adapter binary (claude-code-acp/
codex-acp) each configured claude-code/codex engine needs for HOST-runtime
structured chat is present; whether every configured agent resolves (profile
composition + engine/runtime) and the roster is non-empty; the seeded
dependency lockfile parses and a real context assembly succeeds; hooks AND
MCP registration per configured backend; the trust store's signers; and
companion detection + loadout probing (taskloom/ltk/...). Each line is
prefixed with a DOCTOR-CHECK-* marker — the SAME vocabulary the
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

Diagnostic only: always exits 0, never blocks or changes anything. A "warn"
status IS this command's fail-loud signal — read the report, don't grep the
exit code.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		cfg, cfgErr := GetConfig()
		var checks []DoctorCheck
		if doctorDepsOnlyFlag {
			checks = []DoctorCheck{
				doctorCheckDeps(cfg),
				doctorCheckSignKey(ctx, cfg, agentkey.NewDiscoverer()),
				doctorCheckGitIdentity(ctx, agentkey.NewDiscoverer().GitConfig),
				doctorCheckACPAdapter(cfg),
			}
		} else {
			checks = []DoctorCheck{
				doctorCheckSetupMarker(cfg, cfgErr),
				doctorCheckDeps(cfg),
				doctorCheckSignKey(ctx, cfg, agentkey.NewDiscoverer()),
				doctorCheckGitIdentity(ctx, agentkey.NewDiscoverer().GitConfig),
				doctorCheckACPAdapter(cfg),
				doctorCheckAgents(ctx, cfg, cfgErr),
				doctorCheckVersion(),
				doctorCheckHooksTrust(ctx, cfg, cfgErr),
				doctorCheckSetupLockAndAssembly(ctx, cfg, cfgErr),
				doctorCheckSetupCompanions(cfg, cfgErr),
				doctorCheckSetupAuthPing(),
			}
		}
		report := DoctorReport{Checks: checks}
		return emit(cmd, report, func() error { return renderDoctorReport(cmd.OutOrStdout(), report) })
	},
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

// doctorCheckDeps probes PATH for git (worktree isolation + remote clone/
// pull + init/manage install's own clone), each configured engine's native
// client, and a reachable container runtime — all genuinely REQUIRED — plus
// ssh/ssh-keygen, which are RECOMMENDED but not required (see
// doctorDepBinariesRecommended's doc for why: ctxloom never execs either;
// signing is pure Go). The two buckets are reported separately so "missing"
// never conflates an optional convenience with a real hard dependency.
func doctorCheckDeps(cfg *config.Config) DoctorCheck {
	var missingRequired []string
	for _, bin := range doctorDepBinariesRequired {
		if _, err := exec.LookPath(bin); err != nil {
			missingRequired = append(missingRequired, bin)
		}
	}
	var missingRecommended []string
	for _, bin := range doctorDepBinariesRecommended {
		if _, err := exec.LookPath(bin); err != nil {
			missingRecommended = append(missingRecommended, bin)
		}
	}
	for _, engine := range doctorConfiguredEngines(cfg) {
		bin, ok := doctorEngineBinaries[engine]
		if !ok {
			continue
		}
		if _, err := exec.LookPath(bin); err != nil {
			missingRequired = append(missingRequired, fmt.Sprintf("%s (%s)", bin, engine))
		}
	}
	if !(isolation.Docker{}.Available() || isolation.Podman{}.Available()) {
		missingRequired = append(missingRequired, "docker/podman (container runtime)")
	}
	if len(missingRequired) == 0 && len(missingRecommended) == 0 {
		return DoctorCheck{Marker: "DOCTOR-CHECK-DEPS-a1", Status: "ok",
			Detail: "git, every configured engine's client, and a container runtime are all on PATH (required); ssh and ssh-keygen are also present (recommended: ssh is what git itself needs for an ssh:// remote, ssh-keygen is only for generating a NEW signing key by hand — signing itself is pure Go and never execs either)"}
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
	return DoctorCheck{Marker: "DOCTOR-CHECK-DEPS-a1", Status: "warn", Detail: strings.Join(parts, "; ")}
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
func doctorCheckSignKey(ctx context.Context, cfg *config.Config, discoverer *agentkey.Discoverer) DoctorCheck {
	const marker = "DOCTOR-CHECK-SIGNKEY-k1"
	explicit := ""
	if cfg != nil {
		explicit = cfg.SignKey()
	}
	ok, detail := signKeyResolutionDetail(ctx, discoverer, explicit)
	if ok {
		return DoctorCheck{Marker: marker, Status: "ok", Detail: detail}
	}
	return DoctorCheck{Marker: marker, Status: "warn", Detail: detail}
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
func doctorCheckGitIdentity(ctx context.Context, gitConfig gitConfigFunc) DoctorCheck {
	const marker = "DOCTOR-CHECK-GITIDENT-l2"
	ok, detail := gitIdentityDetail(ctx, gitConfig)
	if ok {
		return DoctorCheck{Marker: marker, Status: "ok", Detail: detail}
	}
	return DoctorCheck{Marker: marker, Status: "warn", Detail: detail}
}

// gitIdentityDetail runs gitConfig for user.name and user.email and renders
// the outcome as a short, actionable line. Shared between
// doctorCheckGitIdentity and init PRIME's checkSystemDeps (init.go) so both
// surfaces say the exact same thing, rather than drifting apart.
func gitIdentityDetail(ctx context.Context, gitConfig gitConfigFunc) (ok bool, detail string) {
	name, nameOK, nameErr := gitConfig(ctx, "", "user.name")
	email, emailOK, emailErr := gitConfig(ctx, "", "user.email")

	if nameErr != nil || emailErr != nil {
		var errs []string
		if nameErr != nil {
			errs = append(errs, nameErr.Error())
		}
		if emailErr != nil {
			errs = append(errs, emailErr.Error())
		}
		return false, "reading git identity failed: " + strings.Join(errs, "; ")
	}

	nameSet := nameOK && strings.TrimSpace(name) != ""
	emailSet := emailOK && strings.TrimSpace(email) != ""
	if nameSet && emailSet {
		return true, fmt.Sprintf("git identity resolves: %s <%s>", name, email)
	}

	var missing []string
	var fixes []string
	if !nameSet {
		missing = append(missing, "user.name")
		fixes = append(fixes, `git config --global user.name "Your Name"`)
	}
	if !emailSet {
		missing = append(missing, "user.email")
		fixes = append(fixes, "git config --global user.email you@example.com")
	}
	return false, fmt.Sprintf(
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
func doctorCheckACPAdapter(cfg *config.Config) DoctorCheck {
	const marker = "DOCTOR-CHECK-ACPADAPTER-m3"
	ok, detail := acpAdapterDetail(doctorConfiguredEngines(cfg))
	if ok {
		return DoctorCheck{Marker: marker, Status: "ok", Detail: detail}
	}
	return DoctorCheck{Marker: marker, Status: "warn", Detail: detail}
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
func doctorCheckAgents(ctx context.Context, cfg *config.Config, cfgErr error) DoctorCheck {
	if cfgErr != nil {
		return DoctorCheck{Marker: "DOCTOR-CHECK-AGENTS-b2", Status: "warn", Detail: "config did not load: " + cfgErr.Error()}
	}
	configuredAgents := cfg.GetConfiguredAgents()
	if len(configuredAgents) == 0 {
		return DoctorCheck{Marker: "DOCTOR-CHECK-AGENTS-b2", Status: "warn",
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
		return DoctorCheck{Marker: "DOCTOR-CHECK-AGENTS-b2", Status: "ok",
			Detail: fmt.Sprintf("%d agent(s) resolve cleanly: %s", len(names), strings.Join(names, ", "))}
	}
	return DoctorCheck{Marker: "DOCTOR-CHECK-AGENTS-b2", Status: "warn", Detail: strings.Join(failed, "; ")}
}

// doctorCheckVersion is deliberately best-effort/skill-guided: there is no
// update-check infrastructure (internal/cli/version.go is a bare print), so
// this reports the running version and defers comparison to the
// ctxloom-doctor skill (or a human) rather than faking a currency verdict.
func doctorCheckVersion() DoctorCheck {
	return DoctorCheck{Marker: "DOCTOR-CHECK-VERSION-c3", Status: "info",
		Detail: fmt.Sprintf("running %s; comparing against the newest remote tag is best-effort/skill-guided (no --check-version yet)", Version)}
}

// doctorCheckHooksTrust cross-references doctorConfiguredEngines (every
// backend a configured agent resolves to) against operations.HarnessStatus —
// the SAME read `ctxloom manage status`/`ctxloom manage hooks status` already
// expose — reporting hooks AND MCP registration per backend (a real
// read, not a bare file-existence guess), plus how many signers the trust
// store carries (operations.ListSigners — always includes the embedded root,
// so a healthy store is never reported as empty).
func doctorCheckHooksTrust(ctx context.Context, cfg *config.Config, cfgErr error) DoctorCheck {
	if cfgErr != nil {
		return DoctorCheck{Marker: "DOCTOR-CHECK-HOOKS-TRUST-d4", Status: "warn", Detail: "config did not load: " + cfgErr.Error()}
	}
	var parts []string
	status := "ok"
	configured := doctorConfiguredEngines(cfg)
	switch {
	case len(configured) == 0:
		parts = append(parts, "hooks/MCP: no engine is configured to check")
	default:
		result, err := operations.HarnessStatus(ctx, cfg, operations.HarnessStatusRequest{})
		if err != nil {
			parts = append(parts, "hooks/MCP: "+err.Error())
			status = "warn"
		} else {
			byBackend := make(map[string]operations.BackendWiring, len(result.Backends))
			for _, b := range result.Backends {
				byBackend[b.Backend] = b
			}
			var present, missing []string
			for _, name := range configured {
				b, ok := byBackend[name]
				if !ok || !b.SettingsExists || !b.HooksPresent {
					missing = append(missing, name)
					continue
				}
				present = append(present, name)
			}
			sort.Strings(present)
			sort.Strings(missing)
			if len(missing) > 0 {
				parts = append(parts, "hooks/MCP NOT registered for: "+strings.Join(missing, ", ")+" (run `ctxloom manage install`)")
				status = "warn"
			} else {
				parts = append(parts, "hooks/MCP registered for: "+strings.Join(present, ", "))
			}
		}
	}
	signers, err := operations.ListSigners(cfg, nil)
	if err != nil {
		parts = append(parts, "trust store: "+err.Error())
	} else {
		active := 0
		for _, s := range signers {
			if !s.Suppressed {
				active++
			}
		}
		parts = append(parts, fmt.Sprintf("trust store: %d active signer(s)", active))
	}
	return DoctorCheck{Marker: "DOCTOR-CHECK-HOOKS-TRUST-d4", Status: status, Detail: strings.Join(parts, "; ")}
}

// ===== init-as-skill Phase 6 postcondition checks (plan.md §8.2) =====
//
// These compose the SAME operations/config entry points every other command
// already uses (config.Config, operations.AssembleContext, config.
// DiscoverCompanions/ProbeCompanions/ProbeCompanionLoadouts) rather than
// re-implementing any of their logic — this file's job is to CALL them and
// translate the result into a DoctorCheck, never to re-derive what "locked",
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

// doctorCheckSetupMarker verifies the .ctxloom marker directory config.Load
// already resolved (cfg.AppPaths, config.go:137) is present and the project
// config loaded without a hard error — the ground-floor precondition every
// other check in this report assumes. Read-only: it inspects config.Load's
// ALREADY-resolved record instead of re-globbing the filesystem for .ctxloom.
func doctorCheckSetupMarker(cfg *config.Config, cfgErr error) DoctorCheck {
	const marker = "DOCTOR-CHECK-SETUP-MARKER-e5"
	if cfgErr != nil {
		return DoctorCheck{Marker: marker, Status: "warn", Detail: "config did not load: " + cfgErr.Error()}
	}
	appDir := doctorAppDir(cfg)
	if appDir == "" {
		return DoctorCheck{Marker: marker, Status: "warn",
			Detail: "no .ctxloom marker directory found (run `ctxloom manage install` or `ctxloom init`)"}
	}
	return DoctorCheck{Marker: marker, Status: "ok", Detail: "marker present, config valid: " + appDir}
}

// doctorCheckSetupLockAndAssembly verifies the two "seeded deps are actually
// usable" postconditions the setup skill's phase 4/5 promise: the lockfile
// (remote.LockfileManager — the SAME reader `ctxloom sync`/`lock` use) parses
// without error, and a real context assembly (operations.AssembleContext —
// the SAME entry point `ctxloom run`'s configured-default path uses) succeeds
// end to end. AssembleContext exercises the trust gate, companion-loadout
// seeding, and fragment/profile resolution for real; none of that is
// reimplemented here.
func doctorCheckSetupLockAndAssembly(ctx context.Context, cfg *config.Config, cfgErr error) DoctorCheck {
	const marker = "DOCTOR-CHECK-SETUP-DEPS-h8"
	if cfgErr != nil {
		return DoctorCheck{Marker: marker, Status: "warn", Detail: "config did not load: " + cfgErr.Error()}
	}
	var parts []string
	status := "ok"
	if appDir := doctorAppDir(cfg); appDir == "" {
		parts = append(parts, "lockfile: no .ctxloom directory to check")
		status = "warn"
	} else {
		lf, err := remote.NewLockfileManager(appDir).Load()
		if err != nil {
			parts = append(parts, "lockfile: "+err.Error())
			status = "warn"
		} else {
			parts = append(parts, fmt.Sprintf("lockfile: %d entries parse cleanly", len(lf.AllEntries())))
		}
	}
	if _, err := operations.AssembleContext(ctx, cfg, operations.AssembleContextRequest{}); err != nil {
		parts = append(parts, "context assembly: "+err.Error())
		status = "warn"
	} else {
		parts = append(parts, "context assembly: succeeds for the configured default profile(s)")
	}
	return DoctorCheck{Marker: marker, Status: status, Detail: strings.Join(parts, "; ")}
}

// doctorCheckSetupCompanions reports companion discovery + loadout probing —
// the SAME two-stage protocol (config.DiscoverCompanions, config.
// ProbeCompanions, config.ProbeCompanionLoadouts) AssembleContext's
// SeededBundleLoader already runs internally for a real session. Reporting
// only: a project with no companions installed is not misconfigured (they
// are optional add-ons), so this is never a "warn"; it respects
// --no-companions/CTXLOOM_NO_COMPANIONS (config.CompanionsDisabled) by
// reporting that instead of executing companion binaries a second time.
func doctorCheckSetupCompanions(cfg *config.Config, cfgErr error) DoctorCheck {
	const marker = "DOCTOR-CHECK-SETUP-COMPANIONS-i9"
	if cfgErr != nil {
		return DoctorCheck{Marker: marker, Status: "warn", Detail: "config did not load: " + cfgErr.Error()}
	}
	if config.CompanionsDisabled() {
		return DoctorCheck{Marker: marker, Status: "info", Detail: "companion probing disabled (--no-companions)"}
	}
	bins := config.DiscoverCompanions()
	if len(bins) == 0 {
		return DoctorCheck{Marker: marker, Status: "info", Detail: "no companions discovered"}
	}
	var present []string
	for _, st := range config.ProbeCompanions() {
		if st.Path != "" {
			present = append(present, st.Bin)
		}
	}
	loadouts := config.ProbeCompanionLoadouts(cfg.TrustRoot())
	presentDetail := "(none on PATH)"
	if len(present) > 0 {
		presentDetail = strings.Join(present, ", ")
	}
	return DoctorCheck{Marker: marker, Status: "ok", Detail: fmt.Sprintf(
		"discovered: %s; on PATH: %s; loadout verified: %d",
		strings.Join(bins, ", "), presentDetail, len(loadouts))}
}

// doctorCheckSetupAuthPing is a placeholder. init-as-skill's USER RULING (a)
// wants a deterministic auth ping BEFORE the raw-CLI vendor TUI launches, but
// no such surface exists anywhere in this codebase yet (grepped: no
// AuthPing/auth-ping symbol) — that is a different slice's work (init-as-
// skill.plan.md §10④, init bootstrap rework), not this one's. Reported as
// "info" so the gap is VISIBLE in the postcondition report instead of
// silently missing.
func doctorCheckSetupAuthPing() DoctorCheck {
	return DoctorCheck{Marker: "DOCTOR-CHECK-SETUP-AUTHPING-j0", Status: "info",
		Detail: "no auth-ping surface exists in this build yet (deferred; verify by launching the engine's own CLI)"}
}

// renderDoctorReport writes the human-readable check list, one
// "DOCTOR-CHECK-* [status] detail" line per check, in the fixed order the
// checks were run.
func renderDoctorReport(out io.Writer, report DoctorReport) error {
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
