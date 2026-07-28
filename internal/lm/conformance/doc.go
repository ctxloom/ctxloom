// Package conformance holds the cross-agent equity suite: table-driven tests
// asserting THREE of the repo's five agent.SettingsWriter implementations —
// claude-code, antigravity, and codex, the only ones agentCases()
// (conformance_test.go) currently lists — honor the shared contract:
// fault-tolerant load (refuse rather than overwrite unparseable prior
// settings), atomic write + backup, full hook-event coverage, MCP
// auto-register, and managed removal that preserves the user's own settings.
//
// opencode and kiro also implement agent.SettingsWriter and are DELIBERATELY
// absent (U058-F03), each for its own structural reason spelled out at
// agentCases' definition — not because nobody got around to adding them.
// This sentence used to read "every supported agent's SettingsWriter", which
// was true when written but stopped being true once opencode/kiro shipped;
// say "three" here, not "every", so this comment cannot silently drift back
// into overclaiming coverage the suite does not have.
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
