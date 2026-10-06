package bundles

import "slices"

// The per-invocation switch that waives signature verification. It has no
// config key, deliberately: the bypass is carried by whoever invokes ctxloom
// (a task-runner recipe for bundle development), never persisted in a
// project where every later run would silently inherit it.
const (
	// SigCheckFlag is the persistent flag's name, without its dashes.
	SigCheckFlag = "disable-sig-check"
	// SigCheckEnv is the environment switch. An explicitly set flag wins
	// over it in either direction.
	SigCheckEnv = "CTXLOOM_DISABLE_SIG_CHECK"
	// SigCheckDisabledNotice is said once per process whose composition
	// waived the check.
	SigCheckDisabledNotice = "signature verification is DISABLED for this invocation (--" + SigCheckFlag + " / " + SigCheckEnv + "): " +
		"an installed signed tree edited after it was signed is read instead of refused. " +
		"This session's own hooks and MCP server and the agents it delegates to share the waiver, and signing itself is unaffected."
	// SessionSigCheckNotice is what a command that runs INSIDE a waived session
	// without honouring it (a doctor, a run typed in the session's shell) says:
	// it verifies, and the session around it does not.
	SessionSigCheckNotice = "this invocation verifies bundle signatures, but the session it runs in waives them (--" + SigCheckFlag + " / " + SigCheckEnv + "): " +
		"that session's hooks, its MCP server and the agents it delegates to read without signature verification."
	// EditedSignedTreeWords is what every surface says about an installed
	// signed tree the waiver accepted although its bytes were edited.
	EditedSignedTreeWords = "an installed signed tree was edited after it was signed"
)

// EditedSignedTrees names, sorted, the reads that are REMOTE with an invalid
// signature: installed signed trees whose bytes were edited after signing,
// which only a waived generation's reader carries (WithEditedTreesCarried).
// It is what doctor and the dry run list when the waiver accepted any.
func EditedSignedTrees(reads []BundleRead) []string {
	var out []string
	for _, r := range reads {
		if r.TrustCtx() == TrustCtxRemote && r.Signature() == SignatureInvalid {
			out = append(out, r.DisplayName())
		}
	}
	slices.Sort(out)
	return out
}
