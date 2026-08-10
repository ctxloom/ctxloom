// Package engineversion asks an installed LLM-engine CLI what version it IS,
// so ctxloom can select a vendor transcript reader that was actually validated
// against that version's log format (task wrought-spearman, decision 5 of
// virtuous-evil).
//
// WHY A PROBE AT ALL. .github/engine-versions.env has recorded the
// last-known-good CLI version per engine since the self-healing pipeline's P0,
// but nothing at runtime ever consulted it: ctxloom parsed whatever vendor
// transcript it found with no idea whether that format version had ever been
// seen before. This package is the runtime half of that lock.
//
// WHAT THIS PACKAGE DOES NOT DO. A probe reports what is installed NOW, never
// what WROTE a stored transcript — a session run under claude-code 2.1 and
// read after upgrading to 2.2 must still be read by 2.1's adapter. So the
// probe's output is RECORDED at session start (sessions.Entry.EngineVersion)
// and the RECORD, not a fresh probe, drives reader selection. Selecting on a
// live probe would confidently mis-parse exactly the sessions that most need
// care.
//
// PARSING IS PER-ENGINE, BY MEASUREMENT. `--version` output has no shared
// shape across vendors — measured 2026-08-07 on this project's dev host,
// claude prints "2.1.225 (Claude Code)", codex prints "codex-cli 0.144.4" and
// opencode prints a bare "1.18.4". One regex over all of them is a guess, so
// each engine declares its own Command (flag + parse) in its backend
// descriptor, next to the rest of that engine's per-engine facts.
//
// EVERY FAILURE HERE IS A REFUSAL, NEVER A DEFAULT. A binary that is absent, a
// version command that fails, or output that does not parse each yields a
// typed error naming what could not be determined. There is deliberately no
// "assume the newest" fallback: a default adapter is a guess wearing a version
// number, and the thing it produces — a plausible-looking transcript that is
// silently wrong — lands on the path that feeds a model.
package engineversion

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/Masterminds/semver/v3"
)

// Command declares HOW to ask one engine's binary for its own version: the
// arguments to pass, and how to read a version out of what it prints.
//
// It lives in the backend descriptor (internal/lm/backends) with the engine's
// other per-engine facts, not in a switch here — this package holds the
// mechanism, the descriptor holds the per-engine detail.
//
// Parse receives the command's combined stdout+stderr, trimmed of surrounding
// whitespace, and returns the version string EXACTLY as it should be recorded
// (not a canonicalized re-render: what goes in the session index should be
// what the engine said). Parse must return an error rather than a best guess
// when the output does not have the shape it was written for — that refusal is
// the whole point.
type Command struct {
	// Args are the arguments handed to the binary ("--version" for every
	// engine measured so far, but not assumed).
	Args []string
	// Parse extracts the version from the command's trimmed combined output.
	Parse func(output string) (string, error)
}

// Resolver maps an engine's registry name to the binary that would be launched
// for it and the Command that asks that binary for its version.
//
// It is a function rather than a direct import because the descriptor table
// that answers it (internal/lm/backends) imports THIS package for Command —
// injecting the lookup keeps the dependency one-directional.
//
// A resolver returning an error is reporting that the version cannot be
// determined at all; it should return a *BinaryAbsentError when the engine's
// binary is simply not installed, since that is the one failure a caller may
// reasonably treat as ordinary rather than alarming.
type Resolver func(engine string) (binaryPath string, cmd Command, err error)

// BinaryAbsentError reports that the engine's own CLI could not be found, so
// there is nothing to ask. Distinguished from the other failures because an
// uninstalled engine is an ordinary state of the world (a user who has claude
// but not kiro), not evidence of anything wrong.
type BinaryAbsentError struct {
	Engine string
	Err    error
}

func (e *BinaryAbsentError) Error() string {
	return fmt.Sprintf("engine %q: binary not installed, so its version cannot be determined: %v", e.Engine, e.Err)
}

func (e *BinaryAbsentError) Unwrap() error { return e.Err }

// NoVersionCommandError reports that this engine declares no way to be asked
// for its version. It is a gap in ctxloom's own descriptor table, not a fact
// about the user's machine — an engine whose transcripts we read MUST declare
// one (locked by TestVersionCommands_DeclaredForEveryVendorReaderEngine).
type NoVersionCommandError struct {
	Engine string
}

func (e *NoVersionCommandError) Error() string {
	return fmt.Sprintf("engine %q declares no version command, so its version cannot be determined", e.Engine)
}

// CommandFailedError reports that the version command ran and failed — a
// non-zero exit, a binary that is not executable, a cancelled context. Output
// carries whatever it managed to print, because that is usually the only clue
// about why.
type CommandFailedError struct {
	Engine string
	Binary string
	Args   []string
	Output string
	Err    error
}

func (e *CommandFailedError) Error() string {
	return fmt.Sprintf("engine %q: version command %q %v failed, so its version cannot be determined: %v (output: %q)",
		e.Engine, e.Binary, e.Args, e.Err, e.Output)
}

func (e *CommandFailedError) Unwrap() error { return e.Err }

// UnparseableVersionError reports that the version command SUCCEEDED but
// printed something the engine's own parser did not recognise — the signal
// that the vendor changed its `--version` output shape, which is exactly the
// kind of drift this machinery exists to notice rather than paper over.
type UnparseableVersionError struct {
	Engine string
	Binary string
	Output string
	Err    error
}

func (e *UnparseableVersionError) Error() string {
	return fmt.Sprintf("engine %q: could not read a version out of %q's output %q, so its version cannot be determined: %v",
		e.Engine, e.Binary, e.Output, e.Err)
}

func (e *UnparseableVersionError) Unwrap() error { return e.Err }

// Prober probes engine binaries for their version, LAZILY and with a cache.
//
// Lazily: ctxloom's CLI is invoked constantly and this project treats startup
// cost as real (UPX was reverted from local installs over ~1.6s per exec), so
// nothing probes every engine at process start. A probe happens when a session
// starts under one engine, and nowhere else.
//
// Cached by BINARY FINGERPRINT — path, modification time and size — not by
// engine name. That is what makes the cache safe across an upgrade: `npm i -g`
// replaces the binary, the fingerprint moves, and the next probe re-executes
// instead of returning the pre-upgrade answer. Caching by name alone would
// hand back a stale version for the rest of the process's life, which on this
// path means selecting the wrong reader.
//
// Only SUCCESSES are cached. A failure is left uncached deliberately: the
// failures here (binary absent, command failed) are the transient,
// environment-shaped ones, and pinning one until the binary changes would
// outlast its cause.
//
// A Prober is safe for concurrent use.
type Prober struct {
	resolve Resolver

	// run executes the version command and returns its trimmed combined
	// output. A field rather than a direct exec call so tests can drive every
	// branch (success, non-zero exit, junk output) without installing engines.
	run func(ctx context.Context, binary string, args []string) (string, error)

	mu    sync.Mutex
	cache map[string]string
}

// NewProber returns a Prober that resolves engines through resolve and
// executes real binaries.
func NewProber(resolve Resolver) *Prober {
	return &Prober{
		resolve: resolve,
		run:     runVersionCommand,
		cache:   map[string]string{},
	}
}

// Probe returns the version the named engine's installed binary reports.
//
// The returned string is the engine's own rendering (e.g. "2.1.225"), suitable
// for recording verbatim in the session index. Every error is one of this
// package's typed errors and every one of them means "this version could not
// be determined" — never "here is a reasonable guess".
func (p *Prober) Probe(ctx context.Context, engine string) (string, error) {
	binary, cmd, err := p.resolve(engine)
	if err != nil {
		return "", err
	}
	if cmd.Parse == nil || len(cmd.Args) == 0 {
		return "", &NoVersionCommandError{Engine: engine}
	}

	// Fingerprint BEFORE consulting the cache: the stat is the thing that
	// notices an upgrade, so skipping it on a cache hit would defeat the
	// invalidation entirely.
	fp, err := fingerprint(engine, binary)
	if err != nil {
		return "", &BinaryAbsentError{Engine: engine, Err: err}
	}
	if v, ok := p.cached(fp); ok {
		return v, nil
	}

	out, err := p.run(ctx, binary, cmd.Args)
	if err != nil {
		return "", &CommandFailedError{Engine: engine, Binary: binary, Args: cmd.Args, Output: out, Err: err}
	}
	version, err := cmd.Parse(out)
	if err != nil {
		return "", &UnparseableVersionError{Engine: engine, Binary: binary, Output: out, Err: err}
	}

	p.store(fp, version)
	return version, nil
}

func (p *Prober) cached(fp string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.cache[fp]
	return v, ok
}

func (p *Prober) store(fp, version string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cache[fp] = version
}

// fingerprint identifies one exact build of one binary: absolute-ish path,
// modification time and size. Two of the three would do for the ordinary
// package-manager upgrade; all three are cheap and an in-place rewrite that
// preserves size is not rare enough to ignore.
func fingerprint(engine, binary string) (string, error) {
	st, err := os.Stat(binary)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s\x00%s\x00%d\x00%d", engine, binary, st.ModTime().UnixNano(), st.Size()), nil
}

// runVersionCommand executes the version command and returns its trimmed
// COMBINED output. Combined, not stdout alone: engines differ on which stream
// they print a version banner to, and a parser handed an empty string because
// the version went to stderr would report "unparseable" for a working engine.
func runVersionCommand(ctx context.Context, binary string, args []string) (string, error) {
	out, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// TokenAt is the shared building block the per-engine parsers use: take the
// first non-empty line of output, split it on whitespace, and require the
// token at index i to be a semver.
//
// It is deliberately POSITIONAL rather than "find the first thing that looks
// like a version anywhere in the output". A scan-for-anything parser keeps
// succeeding when a vendor reshapes its banner — picking up a build number, a
// node version, an unrelated component — and this whole mechanism exists to
// make a shape change visible instead of silently absorbing it.
func TokenAt(output string, i int) (string, error) {
	line := firstNonEmptyLine(output)
	fields := strings.Fields(line)
	if i >= len(fields) {
		return "", fmt.Errorf("expected a version token at position %d of %q, but it has only %d token(s)", i, line, len(fields))
	}
	return validate(fields[i])
}

// FirstSemverToken scans the first non-empty line for the first token that
// parses as a semver, and is the DELIBERATELY TOLERANT counterpart of TokenAt.
//
// It exists only for engines whose real `--version` output this project has
// never measured, because their binary is not installed on any dev host
// (kiro-cli as of 2026-08-07). Guessing a POSITION for those
// would refuse a working engine on a formatting detail nobody has checked;
// scanning at least accepts any of the three shapes actually seen in the wild.
// It is still a refusal when nothing on the line is semver-shaped.
//
// Replace a caller of this with TokenAt the moment that engine's output is
// measured on a real install — the strict form is the one that reports drift.
func FirstSemverToken(output string) (string, error) {
	line := firstNonEmptyLine(output)
	for _, f := range strings.Fields(line) {
		if v, err := validate(f); err == nil {
			return v, nil
		}
	}
	return "", fmt.Errorf("no semver-shaped token in %q", line)
}

func firstNonEmptyLine(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			return s
		}
	}
	return ""
}

// validate accepts a token only if it is a real version, and returns it
// UNCHANGED — the recorded value should be what the engine said, so that a
// human comparing the session index against `claude --version` sees the same
// characters.
func validate(token string) (string, error) {
	if _, err := semver.NewVersion(token); err != nil {
		return "", fmt.Errorf("%q is not a version: %w", token, err)
	}
	return token, nil
}
