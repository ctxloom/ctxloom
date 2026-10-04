package sessions

import (
	"errors"
	"fmt"
)

// The process-boundary carriers. These constants are the ONLY spellings; the
// env-literals-once arch gate forbids the literals outside this package.
//
// Two carriers, two codecs, no overlap:
//   - the RUNNER process receives the reach-back trio (EncodeReach);
//   - the ENGINE process (and its hook subprocesses) receives the two
//     identity values hooks and taskloom key on (HookEnv), from a waived
//     generation only EnvSigCheckWaived, and for the session owner only
//     EnvSessionOwner.
const (
	// EnvSigCheckWaived carries a session's --disable-sig-check to the
	// ENGINE's own ctxloom children (owner rulings 2026-10-02), so the whole
	// session decides alike. The launch sets it on the engine's environment
	// from the generation's Trust and nothing else (launch.Resolve), and only
	// the commands that serve a session (`ctxloom hook`) honour it. A
	// delegated agent decides with its parent's generation, so its launch
	// sets it for the child's own hooks in turn. Every ctxloom process removes
	// it at start, so a `ctxloom run` or `ctxloom doctor` typed in the
	// engine's shell is a new invocation that verifies — and names the
	// session's waiver (operations.Switches.SessionSigCheckWaived).
	EnvSigCheckWaived = "CTXLOOM_SESSION_DISABLE_SIG_CHECK"
	// SigCheckWaivedOn is EnvSigCheckWaived's value when set.
	SigCheckWaivedOn = "1"
	// EnvCoordURL is the coordinator's MCP endpoint URL
	// (http://host:port/mcp); the gRPC RunnerChannel rides the same
	// host:port (one h2c listener, content-type routed).
	EnvCoordURL = "CTXLOOM_COORD_URL"
	// EnvCoordCred is the key of the caller's bearer token (identity, not
	// just admission). 256-bit, hex; only its SHA-256 is ever persisted. It
	// keys the credential in the in-process spawn request (EncodeReach) and
	// in the run's secrets file (EncodeSecrets); it is never a variable in
	// any process's environment, nor in an MCP config structure.
	EnvCoordCred = "CTXLOOM_COORD_CRED"
	// EnvCoordCredFile names the run's secrets file, which holds the
	// credential under EnvCoordCred. Every runner, host and container, reads
	// its credential from there (DecodeReach).
	EnvCoordCredFile = "CTXLOOM_COORD_CRED_FILE"
	// EnvRunID is the coordinator-minted run id for a spawned child's
	// runner, so RunnerChannel Hello correlates the runner to the run the
	// coordinator will StartRun on it. Absent on the parent-session
	// credential.
	EnvRunID = "CTXLOOM_RUN_ID"
	// EnvRunnerOwnerLossWindow is the operator's override of how long a
	// runner WAITS on an unreachable coordinator before it exits on its own
	// (runner.DefaultOwnerLossWindow), Go duration syntax. It is set where the
	// operator runs ctxloom and forwarded onto each runner process
	// (spawn.StartRunner), because a container runner inherits nothing from
	// the host's environment.
	EnvRunnerOwnerLossWindow = "CTXLOOM_RUNNER_OWNER_LOSS_WINDOW"
	// EnvDiagnosticsLog is the file an INTERACTIVE runner writes its
	// diagnostics to instead of its stderr. That stderr is the engine's pty,
	// which the originator composites onto a terminal it has handed to its
	// terminal UI, so a warning printed there lands in the middle of the
	// engine's screen. The originator sets it to the session's diagnostics
	// log, where its own warnings go for the same reason.
	EnvDiagnosticsLog = "CTXLOOM_DIAGNOSTICS_LOG"
	// EnvOutputDir names the session's output dir to every process of a
	// containerized run (the container's own path to it): the sidecar that
	// records the host path is not mounted there (OutputDirIn).
	EnvOutputDir = "CTXLOOM_OUTPUT_DIR"
	// EnvHarp carries the run's session harp to the ENGINE process and its
	// hook subprocesses; it names the session dir and the spool.
	EnvHarp = "CTXLOOM_SESSION_HARP"
	// EnvSessionOwner marks the ENGINE process of the session OWNER — a
	// human's interactive session, the one recipient no runner delivers mail
	// to — and no other. The turn-start mail-drain hook (`ctxloom hook
	// mail-drain`) claims the spool EnvHarp names only under it: a child
	// engine that loads the same hooks (a trusted repository's own settings
	// file) carries its OWN harp, and would otherwise claim its own in/ and
	// race the runner that delivers it. The launch sets it for the owner and
	// removes it for every other run (launch.markOwner), and every ctxloom
	// process removes it at start, so nothing a session's engine starts
	// inherits it through os.Environ().
	EnvSessionOwner = "CTXLOOM_SESSION_OWNER"
	// SessionOwnerOn is EnvSessionOwner's value when set.
	SessionOwnerOn = "1"
	// EnvProjectID carries the project id the run serves, so a containerized
	// child's taskloom keys the SAME shared host log.
	EnvProjectID = "CTXLOOM_PROJECT_ID"
	// EnvHookURL and EnvHookToken carry the session endpoint's approval-hook
	// address and its bearer to the ENGINE process, whose approval hook
	// (`ctxloom hook permission`) posts the engine's ask there.
	EnvHookURL   = "CTXLOOM_HOOK_URL"
	EnvHookToken = "CTXLOOM_HOOK_TOKEN"
)

// ErrNoHookReach is DecodeHookReach's refusal: the process environment
// carries no approval-hook endpoint.
var ErrNoHookReach = errors.New("sessions: no approval-hook endpoint in the process environment")

// EncodeHookReach renders the approval-hook endpoint for the ENGINE process.
func EncodeHookReach(hook Endpoint) map[string]string {
	return map[string]string{EnvHookURL: hook.URL, EnvHookToken: hook.Credential}
}

// DecodeHookReach reads the approval-hook endpoint back; a missing URL or
// bearer is ErrNoHookReach.
func DecodeHookReach(getenv func(string) string) (Endpoint, error) {
	ep := Endpoint{URL: getenv(EnvHookURL), Credential: getenv(EnvHookToken)}
	if ep.URL == "" || ep.Credential == "" {
		return Endpoint{}, ErrNoHookReach
	}
	return ep, nil
}

// ErrNoReachBack is DecodeReach's refusal: the process environment carries no
// coordinator endpoint.
var ErrNoReachBack = errors.New("sessions: no reach-back endpoint in the process environment")

// EncodeReach renders the reach-back trio as the SPAWN REQUEST for a runner:
// an in-process map the environment that starts the runner turns into its
// exec env, moving the credential into the run's secrets file and naming the
// file (EnvCoordCredFile) in its place.
func EncodeReach(reach Endpoint, runID string) map[string]string {
	return map[string]string{EnvCoordURL: reach.URL, EnvCoordCred: reach.Credential, EnvRunID: runID}
}

// DecodeReach reads a runner's reach-back: the URL and run id from its
// environment, the credential from the secrets file EnvCoordCredFile names
// (read through readFile). A missing URL or credential, or a named file that
// cannot be read or decoded, is ErrNoReachBack.
func DecodeReach(getenv func(string) string, readFile func(string) ([]byte, error)) (Endpoint, string, error) {
	ep := Endpoint{URL: getenv(EnvCoordURL)}
	if name := getenv(EnvCoordCredFile); name != "" {
		b, err := readFile(name)
		if err != nil {
			return Endpoint{}, "", fmt.Errorf("%w: reading the secrets file %s: %w", ErrNoReachBack, name, err)
		}
		secrets, err := DecodeSecrets(b)
		if err != nil {
			return Endpoint{}, "", fmt.Errorf("%w: decoding the secrets file %s: %w", ErrNoReachBack, name, err)
		}
		ep.Credential = secrets[EnvCoordCred]
	}
	if ep.URL == "" || ep.Credential == "" {
		return Endpoint{}, "", ErrNoReachBack
	}
	return ep, getenv(EnvRunID), nil
}

// HookEnv renders the identity carriers the ENGINE process forwards to its
// hook subprocesses.
func HookEnv(id Identity) map[string]string {
	return map[string]string{EnvHarp: id.Harp, EnvProjectID: id.Project}
}

// DecodeHookEnv reads the identity carriers back and applies the harp rule.
func DecodeHookEnv(getenv func(string) string) (Identity, error) {
	id := Identity{Harp: getenv(EnvHarp), Project: getenv(EnvProjectID)}
	return id, id.Validate()
}
