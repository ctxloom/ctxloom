package cli

import (
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/shared/version"
)

// versionStampFixIt is the remedy for an unstamped binary. The refusal is the
// entire user interface for this failure, so it names the command that ends
// the problem rather than describing the complaint again.
const versionStampFixIt = "build through the task runner: `just build` applies the -X internal/shared/version.Version ldflag that stamps the binary"

// refuseUnstampedBuild aborts before dispatch when this binary carries no
// usable version stamp.
//
// WHY A BINARY THAT CANNOT NAME ITSELF MAY NOT RUN. Everything this project
// verifies by rests on being able to say WHICH build answered — a stale binary
// plus an exit-code check agrees with anything. A version that cannot tell two
// ctxloom builds apart is not a smaller version; it is a value that makes
// several downstream mechanisms silently stop working while continuing to
// report success: an agent image is keyed by this stamp and would be reused
// across builds it does not belong to, and a daemon-skew comparison has
// nothing to compare. Proceeding under an unidentifiable identity is the
// always-launch failure this project pivoted away from.
//
// It is NOT DEGRADABLE — `--degraded` refuses too — ruled by the maintainer on
// 2026-09-03 against the opposite reading. The deciding argument is the one
// above: this stamp KEYS the agent container image, so a degraded launch can
// tag or reuse an image under an unusable version, and the launch itself is
// then the harm rather than merely a loss of diagnosability.
//
// That makes this the one finding which is neither a trust nor an isolation
// boundary and still refuses under `--degraded`. It is a deliberate widening of
// that exception to cover "cannot identify itself", and it is recorded here so
// the next reader does not mistake it for a mis-classified ordinary finding and
// quietly relax it.
//
// THE STANDING PROMISE, as narrowed on 2026-09-14 by the degradation audit
// (obstinate-judiciary): `--degraded` reaches a working LLM WHEREVER REACHING
// ONE DAMAGES NOTHING. It never reaches one by dropping a requested isolation
// boundary, by running an image that can start as root, or by consenting to
// any other elevated privilege — those REFUSE in both modes, and the refusal
// names what to do instead.
//
// The promise was absolute until that audit. It was narrowed rather than
// deleted, and the cost is recorded: a conditional promise is weaker guidance
// than an absolute one. It was taken deliberately, because the absolute had
// just become false, and a false absolute keeps all of its authority while
// lying — the next author reads it, reaches for a host fallback, and ships the
// bypass believing the codebase told them to.
//
// So the guidance this comment exists to give is unchanged in the case that
// matters: do NOT copy the non-degradable pattern for an ORDINARY finding. A
// profile that will not parse, a bundle that will not load, a sync that fails
// — all still degrade, and a user who passes `--degraded` still gets a working
// LLM with less context. The test for the other side is strictness.FailAlways's
// own: does LAUNCHING cause the harm?
//
// The remedy above is followable in both modes — build through the task runner
// so the stamp is applied — which is what makes refusing in both modes fair
// rather than a dead end.
//
// It fires from the root PersistentPreRunE rather than from `run`'s startup
// gate on purpose: the stamp is a property of the BINARY, so every command is
// equally unidentifiable without it, and a gate that covered only the launch
// paths would leave `ctxloom version` cheerfully reporting nothing.
func refuseUnstampedBuild(cmd *cobra.Command) error {
	if version.ValidStamp(version.Version) {
		return nil
	}
	// Opened only on the failing path, so a stamped run adds no checkpoint.
	g := newPhaseGates(cmd.ErrOrStderr())
	strictness.FailAlways(strictness.ClassConfig, versionStampFixIt,
		"this binary carries no usable version stamp (%q): it cannot say which build or commit is answering, and nothing downstream can tell it apart from any other ctxloom",
		version.Version)
	return g.close(PhaseStartup)
}
