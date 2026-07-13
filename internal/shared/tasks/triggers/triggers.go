// Package triggers holds the PURE logic behind revive-trigger evaluation: the
// evidence-pack shape handed to a batch-triage prompt, the prompt itself, and
// parsing the model's verdicts back out. It performs no I/O and makes no LLM
// call — taskloom owns task state (the append-only event log); ctxloom owns
// execution and all LLM bridging, and calls into this package for the parts
// that need neither, so taskloom can import it safely.
package triggers
