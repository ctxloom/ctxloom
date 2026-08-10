// `ctxloom init`'s targeted system-dependency gate and its informational
// companion probes (signing identity, git identity, ACP adapter). See
// checkSystemDeps for why git is the only hard block among them.

package cli

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/signing/agentkey"
)

// checkSystemDeps is PRIME's targeted, deterministic system-dependency gate —
// NOT `ctxloom doctor` (which comprehensively checks engines/agents/hooks/
// trust; overkill and partly irrelevant this early). This just asks "does the
// machine have what THIS call is about to need."
//
// git is a HARD BLOCK: cloneConfiguredRemotes (called right after this,
// within the same setupNewCtxloomDir call) shells out to git to clone the
// seeded remote, and worktree isolation shells out to it later still — a
// git-less machine cannot complete init's own deterministic steps. Failing
// loud here, with a named fix, beats letting the clone step surface a raw
// "executable file not found" error with no guidance.
//
// ssh-keygen and a container runtime (needed later for containerized
// agents) are INFORMATIONAL ONLY: nothing PRIME itself does needs them yet,
// so their absence surfaces as a warning, not a block. ssh-keygen is NOT a
// signing dependency — an earlier version of this warning wrongly implied
// it was; ctxloom's signing is pure Go over the ssh-agent protocol
// (internal/signing/agentkey/agentkey.go, internal/signing/sign.go — never
// shells out to ssh-keygen) — it is only useful, by hand, to GENERATE a new
// key if you don't already have one (`ssh-keygen -t ed25519-sk`).
//
// engine is the resolved backend this init is configuring (e.g.
// "claude-code", "codex") — used only to know which ACP adapter, if any,
// warnIfACPAdapterMissing should check for.
//
// A sibling slice adds git to `ctxloom doctor`'s own comprehensive dependency
// check on a separate, unmerged branch; the couple of lines of overlap
// between that comprehensive report and this narrow up-front gate are
// intentional (they serve different moments — doctor is a health report,
// this is a "can PRIME even proceed" gate), not something to fold into a
// shared helper here.
func checkSystemDeps(engine string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("git is required (ctxloom is about to clone/pull remote content, and worktree isolation shells out to it later) but was not found on PATH — install it (e.g. `apt install git`, `brew install git`, `winget install Git.Git`) and re-run `ctxloom init`")
	}

	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		clidiag.Warn("ctxloom", "ssh-keygen not found on PATH — recommended, not required (`ctxloom sign` itself is pure Go and never execs ssh-keygen): it's the tool you'd run by hand to generate a new SSH key if you don't already have one to sign with (`ssh-keygen -t ed25519-sk`)")
	}
	warnIfNoSignKey()
	warnIfGitIdentityMissing()
	warnIfACPAdapterMissing(engine)
	if !(isolation.Docker{}.Available()) && !(isolation.Podman{}.Available()) {
		clidiag.Warn("ctxloom", "no container runtime detected (docker/podman) — you'll need one later to run containerized agents")
	}
	return nil
}

// warnIfNoSignKey is checkSystemDeps' companion to the ssh-keygen PATH probe
// above: even with ssh-keygen present, a resolvable signing IDENTITY is
// needed both to approve reviewed content (`ctxloom review` countersigns an
// approval with it — spec §9.5, and review is a normal part of ordinary
// setup, not a publishing-only step) and to publish or sign your own content
// (`ctxloom sign`). It runs the SAME resolver both of those use
// (internal/signing/agentkey.Discoverer.Discover — see review.go's
// resolveReviewSigner and sign.go's runSign) and reuses
// signKeyResolutionDetail (doctor_cmd.go) so this warn says the exact same
// thing `ctxloom doctor --deps`'s DOCTOR-CHECK-SIGNKEY-k1 check reports —
// one resolver, one message, two surfaces. explicit is always "" here: a
// brand-new init has no sign.key configured yet, so this checks the
// zero-config chain (git config user.signingkey, then ssh-agent's sole
// identity) exactly as `ctxloom review`/`ctxloom sign` would try it today.
// Informational only, like ssh-keygen/container-runtime above — never blocks
// init: a project that only ever consumes already-trusted/embedded content
// has nothing to approve and genuinely needs no key.
func warnIfNoSignKey() {
	ok, detail := signKeyResolutionDetail(context.Background(), agentkey.NewDiscoverer(), "")
	if !ok {
		clidiag.Warn("ctxloom", "%s", detail)
	}
}

// warnIfGitIdentityMissing is checkSystemDeps' companion probe for git's
// commit identity (user.name/user.email), same shape and posture as
// warnIfNoSignKey above: informational only, reusing the SAME shared
// gitIdentityDetail (doctor_cmd.go) and the SAME `git config --get` reader
// (internal/signing/agentkey.Discoverer.GitConfig, defaulted by
// agentkey.NewDiscoverer()) that DOCTOR-CHECK-GITIDENT-l2 uses, so this warn
// says the exact same thing `ctxloom doctor --deps` reports. Agents ctxloom
// launches commit their own work inside isolated worktrees (internal/lm/
// isolation/worktree.go's teardown), so an incomplete identity here can
// surface later as a failed or mis-attributed commit deep inside a run —
// surfacing it at init time, before that happens, beats discovering it then.
func warnIfGitIdentityMissing() {
	ok, detail := gitIdentityDetail(context.Background(), agentkey.NewDiscoverer().GitConfig)
	if !ok {
		clidiag.Warn("ctxloom", "%s", detail)
	}
}

// warnIfACPAdapterMissing is checkSystemDeps' companion probe for the ACP
// adapter binary (claude-code-acp/codex-acp) the resolved engine's
// HOST-runtime structured chat needs (see DOCTOR-CHECK-ACPADAPTER-m3,
// doctor_cmd.go, for the full rationale) — reusing the SAME shared
// acpAdapterDetail (doctor_cmd.go) so this warn says the exact same thing
// `ctxloom doctor --deps` reports. Informational only, like the other warns
// beside it: the raw-CLI bootstrap interview this gate protects never
// touches structured chat, so a missing adapter here is a heads-up for
// LATER (agent_run cross-engine delegation, `ctxloom acp run`), not a
// block on init completing now — nothing about ACP (server or client) is
// ever a hard requirement for init; see the acp-setup skill for that
// separate, optional configuration — and it's a non-issue entirely for an
// agent that ends up configured with runtime: container, whose image
// carries its own adapter (the detail text says so).
func warnIfACPAdapterMissing(engine string) {
	ok, detail := acpAdapterDetail([]string{engine})
	if !ok {
		clidiag.Warn("ctxloom", "%s", detail)
	}
}
