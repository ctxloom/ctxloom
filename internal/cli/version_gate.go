package cli

import (
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/version"
)

// versionStampFixIt is the remedy for an unstamped binary. The refusal is the
// entire user interface for this failure, so it names the command that ends
// the problem rather than describing the complaint again.
const versionStampFixIt = "build through the task runner: `just build` applies the -X internal/version.Version ldflag that stamps the binary"

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
// It is DEGRADABLE — `--degraded` warns and launches — because the harm is
// diagnosability, not the launch itself: the user still gets a working LLM,
// having explicitly asked for one from a binary that cannot name itself. The
// remedy above stays followable in both modes, which FailAlways would require
// and this does not.
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
	strictness.FailOnce(strictness.ClassConfig, versionStampFixIt,
		"this binary carries no usable version stamp (%q): it cannot say which build or commit is answering, and nothing downstream can tell it apart from any other ctxloom",
		version.Version)
	return g.close(PhaseStartup)
}
