// Package conformance is the engine port's conformance suite: Run asserts,
// for one engine value, the properties every engine must hold — the
// declarative half here (the Definition validates, its derived views agree
// with its typed fields, its grammar covers its modes, its dynamic approach
// has an MCP approach to name the endpoint). Every engine package copies
// TestEngine_Mock_Conforms verbatim with its own constructor. The helpers
// (SessionFor) build the engine-facing fixtures a test needs without a
// launch.
package conformance
