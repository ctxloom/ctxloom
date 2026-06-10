// Package conformance holds the cross-agent equity suite: table-driven tests
// asserting every supported agent's SettingsWriter (claude/antigravity/codex) honors
// the shared contract — fault-tolerant load, atomic write + backup, full
// hook-event coverage, MCP auto-register, and managed removal that preserves the
// user's own settings.
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
