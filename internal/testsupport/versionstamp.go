package testsupport

import "github.com/ctxloom/ctxloom/internal/version"

// TestBinaryStamp is the build stamp a TEST BINARY carries.
//
// internal/version.Version has no default: an unstamped ctxloom refuses to
// start, because a binary that cannot name its own build is what lets
// confident wrong work through. A `go test` binary is unstamped by
// construction — no ldflags reach it — so every test that drives a ctxloom
// command would hit that refusal. This is the sanctioned way through, and the
// reason it is sanctioned rather than a loophole is WHERE IT LIVES: production
// code may not import internal/testsupport at all, and that is not a
// convention but a gate — tests/arch's forbiddenPrefix scan fails the build
// when any production package reaches this tree. So this stamp is unreachable
// from a shipped binary by the same mechanism that keeps the rest of this
// package out of one.
//
// The VALUE is deliberately self-identifying rather than plausible. It is a
// complete stamp (it must satisfy version.ValidStamp, or it would not get past
// the gate it exists to satisfy) whose version, sha and timestamp could not
// belong to a real build, so a stamp seen in the wild is never mistaken for
// one this project produced.
const TestBinaryStamp = "v0.0.0-0000000-19700101T000000"

// StampTestBinary gives this process the test stamp. SandboxedMain calls it
// before any test runs; a package whose TestMain does not route through
// SandboxedMain but whose tests drive ctxloom commands calls it directly.
//
// It is a plain assignment and not a t.Cleanup-scoped helper on purpose: the
// stamp is a property of the BINARY, true for its whole life, exactly as the
// ldflags stamp is for a real build. A test that wants to observe a different
// stamp sets version.Version itself and restores it, which several already do.
func StampTestBinary() {
	version.Version = TestBinaryStamp
}

// TestBinaryLDFlags stamps a ctxloom binary that a TEST COMPILES FOR ITSELF.
//
// Several suites build a real ./cmd/ctxloom and then execute it — the MCP wire
// harness, the container cells, the docker exec probes. That binary is a
// separate process: nothing a TestMain does in the parent reaches it, and
// without a stamp it now refuses to start, so the suite would be measuring the
// refusal instead of the behaviour it came to test. Pass this to `go build`
// -ldflags, exactly as the task runner passes the real stamp.
//
// It is one constant rather than a string built at each site so that the flag
// name and the import path cannot drift apart across those suites; a wrong path
// in an -X flag is silently ignored by the linker, which would leave the binary
// unstamped and the failure looking like the gate misfiring.
const TestBinaryLDFlags = "-X github.com/ctxloom/ctxloom/internal/version.Version=" + TestBinaryStamp
