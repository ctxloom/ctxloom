// This file is the per-engine half of the version probe (task
// wrought-spearman): WHICH flag asks each engine for its version, and HOW to
// read the answer out of what it prints. The mechanism — lazy probing,
// fingerprint-keyed caching, the typed refusals — lives in
// internal/engineversion; only the per-engine facts live here, in and beside
// the descriptor table, with the rest of what ctxloom knows about each engine.
//
// The three parsers below are three DIFFERENT shapes, measured on this
// project's dev host, not three copies of one guess:
//
//	claude    --version  ->  "2.1.225 (Claude Code)"   version first, name in parens
//	codex     --version  ->  "codex-cli 0.144.4"       name first, then version
//	opencode  --version  ->  "1.18.4"                  bare version
//
// kiro-cli is not installed anywhere this project can reach, so its output
// shape is UNMEASURED and it uses the tolerant scanner with that fact stated
// at its site. Tighten it to a positional parser the first time it is run on
// a real install.
package backends

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/engineversion"
)

// engineVersionProber is the process-wide probe cache. One instance, so the
// fingerprint cache is actually shared across everything in a `ctxloom run`
// that might ask; per-call Probers would each re-exec the engine.
var engineVersionProber = engineversion.NewProber(ResolveEngineVersionCommand)

// VersionCommandFor returns the named backend's declared version command, and
// whether it declares one at all. mock and acp deliberately do not: mock has no
// binary, and the generic acp backend drives WHATEVER command config names, so
// there is no single binary whose version would mean anything.
func VersionCommandFor(name string) (engineversion.Command, bool) {
	d, ok := descriptors[name]
	if !ok || d.versionCommand.Parse == nil {
		return engineversion.Command{}, false
	}
	return d.versionCommand, true
}

// ResolveEngineVersionCommand is this registry's engineversion.Resolver: it
// pairs the engine's resolved binary (AvailabilityOf — the same PATH and
// login-shell-PATH resolution every other availability check uses) with the
// version command its descriptor declares.
//
// An unresolvable binary is reported as *engineversion.BinaryAbsentError so a
// caller can tell "this engine is not installed" (ordinary) from "this engine
// is installed and misbehaved" (worth saying out loud).
func ResolveEngineVersionCommand(engine string) (string, engineversion.Command, error) {
	cmd, ok := VersionCommandFor(engine)
	if !ok {
		return "", engineversion.Command{}, &engineversion.NoVersionCommandError{Engine: engine}
	}
	binary, err := AvailabilityOf(engine)
	if err != nil {
		return "", engineversion.Command{}, &engineversion.BinaryAbsentError{Engine: engine, Err: err}
	}
	return binary, cmd, nil
}

// ProbeEngineVersion reports the version the named engine's installed CLI says
// it is, through the shared cached prober. Every error is one of
// internal/engineversion's typed refusals; there is no fallback value.
func ProbeEngineVersion(ctx context.Context, engine string) (string, error) {
	return engineVersionProber.Probe(ctx, engine)
}

// parseClaudeCodeVersion reads claude-code's `--version` output.
// MEASURED: "2.1.225 (Claude Code)" — the version leads, the
// product name follows in parentheses.
func parseClaudeCodeVersion(output string) (string, error) {
	return engineversion.TokenAt(output, 0)
}

// parseCodexVersion reads codex's `--version` output.
// MEASURED: "codex-cli 0.144.4" — the binary name leads, so the
// version is the SECOND token. A parser that took the first token here would
// refuse every working codex install.
func parseCodexVersion(output string) (string, error) {
	return engineversion.TokenAt(output, 1)
}

// parseOpencodeVersion reads opencode's `--version` output.
// MEASURED: a bare "1.18.4" — no name, no decoration.
func parseOpencodeVersion(output string) (string, error) {
	return engineversion.TokenAt(output, 0)
}

// parseKiroVersion reads kiro-cli's `--version` output.
// UNMEASURED: kiro-cli is not installed on any host this project can reach, so
// the tolerant scanner is used rather than a guessed token position — see
// engineversion.FirstSemverToken. Replace with a positional TokenAt once the
// real output is seen.
func parseKiroVersion(output string) (string, error) {
	return engineversion.FirstSemverToken(output)
}
