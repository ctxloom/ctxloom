// Package conformance is the engine port's conformance suite: Run asserts,
// for one engine value, the properties every engine must hold — the
// declarative half (the Definition validates, its derived views agree with
// its typed fields, its grammar covers its modes, its dynamic approach has
// an MCP approach to name the endpoint) and the instance half (Instance
// binds every declared mode, Exec parses against the mode's grammar,
// Structured iff a driver, home vars under the session home, Home
// validates, Container is real or refuses, Hooks is a codec). Every engine
// package copies TestEngine_Mock_Conforms verbatim with its own
// constructor. The helpers (SessionFor, PresentAll, RouteFor,
// PackageFixture, Canonical) build the engine-facing fixtures a test needs
// without a launch.
package conformance
