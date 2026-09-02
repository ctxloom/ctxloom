// Package conformance holds the cross-agent equity suite: table-driven tests
// asserting that the agent.SettingsWriter implementations agentCases()
// (conformance_test.go) lists honor the shared contract:
// fault-tolerant load (refuse rather than overwrite unparseable prior
// settings), atomic write + backup, hook-event REACH, MCP auto-register, and
// managed removal that preserves the user's own settings.
//
// "Reach", not "full coverage": the hook assertions search the settings file's
// BYTES for each unified event's command, and additionally configure one event
// at a time to show each is emitted independently. They therefore prove that
// every unified event's command arrives, and that no event drags another's
// along — but NOT that a command landed under the right NATIVE event. Slot
// attachment needs per-agent format knowledge, which is exactly what this
// suite refuses to hold, and is asserted where that knowledge lives: the
// per-agent tests (claude/hooks_wire_test.go, claude/surfacedelivery_test.go,
// and each other engine's own settings tests). This sentence used
// to say "full hook-event coverage", which reads as the stronger claim.
//
// An agent.SettingsWriter implementation may be DELIBERATELY absent for a
// structural reason spelled out at agentCases' definition — not because nobody
// got around to adding it. Say what agentCases lists, never "every supported
// agent", so this comment cannot silently drift back into overclaiming
// coverage the suite does not have.
//
// The tests are gated behind the `conformance` build tag (see
// conformance_test.go) so they never run in the default `go test ./...` that
// concurrent work relies on. Run them explicitly:
//
//	go test -tags conformance ./internal/lm/conformance/...
//
// This file is intentionally tag-free: it keeps the package present for default
// builds so the tag-gated test file can't trip "build constraints exclude all Go
// files" during `go test ./...`.
package conformance
