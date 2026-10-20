package archrules

// StdStreamsAllowed is the std-streams gate's reasoned, symbol-keyed
// allowlist, in the same "file.go#Symbol" shape as WriteDisciplineAllowed:
// each entry names a function outside the CLI that reaches the process's own
// stdout or stderr, mapped to why that cannot draw over a terminal ctxloom run's
// split view owns. The analyzer fails an entry the scan no longer reports.
var StdStreamsAllowed = map[string]string{
	"internal/adapters/remote/github.go#loggingTransport.RoundTrip": "CTXLOOM_DEBUG_HTTP's request trace: an operator's explicit ask for HTTP tracing on the terminal, off unless that variable is 1",
	"internal/core/agent/contextfile.go#applyContextOptions":        "the oversize-context warning's default writer; its only production caller is the mock engine's backend, which runs inside the runner process, whose stderr is captured — a core component should take a report.Sink instead",
	"internal/engines/kit/transport.go#spawnGrace":                  "an engine process's stderr passes through to the runner's own, which the launcher captures (a host runner's bounded ring, a container's log); the engine never runs in the process that owns the split view",
	"internal/engines/mock/runtime/runtime.go#Runtime.stdout":       "the mock engine's own wire: it IS the engine process, and stdout is its protocol",
	"internal/engines/mock/runtime/runtime.go#Runtime.stderr":       "the mock engine's own diagnostics: it IS the engine process, whose stderr the runner captures",
	"internal/shared/logboot/logboot.go#Install":                    "CTXLOOM_VERBOSE's tee of the structured log to stderr: an operator's explicit ask for terminal output",
	"internal/shared/stderrtail/stderrtail.go#TeeStderr":            "hands a child's stderr to the host's on purpose (additive capture); no launch on a split-view path uses it — they capture with stderrtail.New",
}
