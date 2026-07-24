// Package mockengine is a deterministic, credential-free, network-free stand-in
// for a real vendor coding-agent CLI. ctxloom's EXISTING, UNMODIFIED backends
// spawn and drive it exactly as they drive the real engine; it answers the one
// question a normal engine cannot be made to answer honestly: "what context did
// I receive, and where did I find it?".
//
// WHY IT EXISTS. ctxloom's characteristic bug is the silent no-op — exit 0, a
// success message, and zero bytes actually delivered. Every ordinary test can
// assert is that a turn appeared. This runtime inverts the assertion from
// EXISTENCE to EVIDENCE: it walks the exact context surfaces the engine's L1 CLI
// declaration says the vendor reads, stats and hashes each one, and reports the
// result — INCLUDING the present:false rows for surfaces that were probed and
// found absent, which is precisely what a silent no-op looks like.
//
// THE LAYERS.
//   - L1 (internal/shared/agent + each backend's enginecli.go) is the single
//     declaration of a vendor CLI: its argv grammar, where the prompt travels,
//     and the context surfaces it probes. This package NEVER restates that; it
//     reads L1 via the backends resolver (backends.EngineCLIsFor) so a mock-side
//     copy can never drift out of step with the real driver.
//   - L2 (this package) is the backend-agnostic runtime: the discovery walk
//     (discovery.go), one hasher and report shaper (report.go), the sentinel
//     dispatcher (sentinel.go), and the Runtime that ties them together
//     (runtime.go). Adding a backend adds an L1 profile, not code here.
//   - L3 is the per-surface wire adapter (oneshot.go for claude -p).
//   - L4 is the thin binary (cmd/mockengine) that selects a personality.
//
// SCOPE BOUNDARY — READ THIS BEFORE CITING A GREEN MOCK RUN (task fiery-pasta).
// This mock is written in Go. A Go binary dropped in place of a vendor CLI runs
// fine REGARDLESS of the container image's own runtime health — it does not
// share the Node version, the npm module graph, or the auth that the real
// engine needs. The 2026-07-24 container-delegation incident was an image
// shipping a Node too old to load claude-code-acp; a Go mock in that same image
// would have run perfectly and reported the container path "healthy" while every
// real agent in it was dead. Therefore:
//
//	This mock proves CONTEXT DELIVERY — did ctxloom put the right files at the
//	paths the CLI probes, and deliver the prompt through the right channel. It
//	proves NOTHING about whether the real engine binary can RUN in an image.
//
// Image runtime health is owned by a SEPARATE, non-skippable check —
// adapterRunGate / the ACP adapter-runs execution probe in
// internal/lm/isolation (profile.go, acpadapter_runs_docker_integration_test.go)
// — which validates the REAL engine by execution. Neither substitutes for the
// other, and a green mock run must never be offered as evidence that the image's
// real engine works.
package mockengine
