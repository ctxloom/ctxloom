// Package triggers holds the PURE logic behind revive-trigger evaluation: the
// evidence-pack shape handed to a batch-triage prompt, the prompt itself, and
// parsing the model's verdicts back out. It performs no I/O and makes no LLM
// call — taskloom owns task state (the append-only event log); ctxloom owns
// execution and all LLM bridging, and calls into this package for the parts
// that need neither.
//
// The purity constraint's real payoff today is determinism and
// testability, not taskloom-shareability: this is the only fuzz-tested,
// property-asserted part of the trigger-evaluation path (see parse_test.go),
// which a package doing I/O or making an LLM call could not be. Nothing
// under cmd/taskloom imports this package yet — "taskloom can import it
// safely" is a latent option this design keeps open, not a current fact.
package triggers
