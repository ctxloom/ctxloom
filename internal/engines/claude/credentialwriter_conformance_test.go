//go:build conformance

package claude

// Package doc for this file: see the doc comment on
// TestClaudeCredentialWriter_FallsBackThroughEBUSY below for why this exists
// and what it does and does not prove.
//
// Tag-gated like internal/engines/conformance (see its doc.go), for the same
// reason: this probe depends on an INSTALLED, VERSIONED third-party binary
// and on `node` being on PATH, neither of which the default `go test ./...`
// may assume. It is deliberately absent from that package (a different
// third-party surface, a different failure shape) and deliberately not
// wired into any just recipe — it is not part of the build chain, but is
// required reading before shipping anything that depends on the assumption
// it asserts. Run it explicitly:
//
//	go test -trimpath -tags conformance -run TestClaudeCredentialWriter ./internal/engines/claude/...

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestClaudeCredentialWriter_FallsBackThroughEBUSY is the conformance probe
// for the ONE fact about claude's shipped binary that any bind MOUNT of
// claude's credential would depend on. ctxloom mounts no credential today
// (the run authenticates from CLAUDE_CODE_OAUTH_TOKEN); this probe, with
// credentiallink_hazard_unix_test.go, is what a proposal to mount one must
// re-run first. The fact: claude's credential writer, when a rename used to land
// a write fails, checks the failure's errno against a small allow-set and —
// if it is a member — falls back to opening the TARGET path directly
// (O_NOFOLLOW) and writing through it in place, rather than propagating the
// error. EBUSY is one of the members of that set. That fallback is what lets
// a write reach a bind-mounted credential at all: a bind mount is pinned to
// the target's inode/dentry, and only an in-place write through that same
// path — never a rename that swaps in a new inode — is visible on the other
// side of the mount.
//
// HOW IT PROVES THIS, deliberately BEHAVIOURAL rather than structural: it
// extracts the ACTUAL function that performs this fallback from the
// currently-installed claude binary — verbatim, not reimplemented — locates
// it by the SHAPE of the dependency (a Set literal whose members include
// both EXDEV and EBUSY, and the nearest enclosing function that reads it),
// never by the minified names bun's bundler assigns it (those names — the
// Set's variable, the function's name, its helper calls — are read fresh
// every run and are expected to change from release to release; the probe
// does not care what they currently are). It then actually RUNS that
// extracted code, in a real Node runtime, against a real scratch file: it
// injects a renameFn that always rejects with EBUSY (the fallback's
// injectable seam, not a reimplementation of the OS), and asserts on the
// OUTCOME:
//
//   - the write is not rejected (the errno-set membership check still
//     passes EBUSY through to the fallback instead of propagating the
//     error), and
//   - the target's bytes actually change to the new content, and
//   - the target's INODE is unchanged — proving the write landed IN PLACE
//     through the existing path, which is exactly the property a bind
//     mount needs and a rename-based write would not have.
//
// A negative control (see this file's development notes) confirms the probe
// actually goes red: forcing an errno NOT in the set makes the real
// extracted function propagate the error, and the probe reports that
// failure rather than passing vacuously.
//
// WHAT THIS DOES NOT PROVE: it never touches a real credential, never
// forces a token refresh, and never drives a live claude process — the
// function is exercised in isolation, with two of its module-level
// dependencies (an error-code accessor and a retry backoff, whatever their
// current minified names are) replaced by small generic stubs inferred from
// how they are called, not by what they are named. It also does not prove
// this is what happens during a REAL token refresh inside a REAL claude
// process — only that the function claude ships, when handed the same
// injectable failure, behaves the way the mount design assumes. That gap is
// the honest ceiling of what is reachable without touching a live
// credential or a live engine (see the task this closes for the fuller
// argument for why those are out of bounds here).
//
// WHEN IT CANNOT RUN AT ALL: no claude binary on PATH, or no `node` on
// PATH, or the shipped binary's text no longer contains a Set literal
// shaped like this dependency (bun changed encoding, or claude dropped the
// mechanism, or moved to a different fallback shape entirely) — all skip or
// fail LOUDLY, naming what was missing, rather than passing silently.
func TestClaudeCredentialWriter_FallsBackThroughEBUSY(t *testing.T) {
	claudeBin, err := locateClaudeBinary()
	if err != nil {
		t.Skipf("claude not found on PATH; nothing to conform against: %v", err)
	}

	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skipf("node not found on PATH; cannot execute the extracted credential-writer snippet: %v", err)
	}

	data, err := os.ReadFile(claudeBin)
	if err != nil {
		t.Fatalf("reading claude binary %s: %v", claudeBin, err)
	}
	text := string(data)

	snippet, funcName, err := extractCredentialWriterSnippet(text)
	if err != nil {
		t.Fatalf(
			"could not locate claude's rename-fallback errno set in %s: %v\n"+
				"This is the assumption any bind mount of claude's credential would "+
				"depend on: a Set literal containing both "+
				"EXDEV and EBUSY, consumed by a function that falls back to an "+
				"in-place write. Either claude changed how this is encoded (adjust "+
				"the extraction) or claude REMOVED the fallback (a credential mount "+
				"would then never see claude's writes).",
			claudeBin, err,
		)
	}

	// The positive case is asserted alongside a NEGATIVE CONTROL, in the same
	// run against the same extracted snippet: forcing an errno that is NOT a
	// member of the set (ENOSPC) must make the real extracted function
	// propagate the error instead of falling back. Without this, a probe
	// that always printed PROBE-OK — say, because assembleProbeModule's
	// footer had a bug that never actually awaited the target function, or
	// because extraction silently grabbed the wrong function — would look
	// identical to a probe that genuinely observed the fallback. This is
	// what makes the positive assertion trustworthy rather than vacuous.
	t.Run("EBUSY falls back to an in-place write", func(t *testing.T) {
		out, runErr := runFallbackProbe(t, nodeBin, snippet, funcName, "EBUSY")
		if runErr != nil {
			t.Fatalf(
				"claude's rename-fallback dependency no longer holds against %s "+
					"(a bind mount of claude's credential would depend on it): "+
					"node exited with %v\n%s",
				claudeBin, runErr, out,
			)
		}
		if !strings.Contains(out, "PROBE-OK") {
			t.Fatalf("probe did not report PROBE-OK (unexpected pass-through): %s", out)
		}
	})

	t.Run("negative control: ENOSPC is not in the set and must propagate", func(t *testing.T) {
		out, runErr := runFallbackProbe(t, nodeBin, snippet, funcName, "ENOSPC")
		if runErr == nil {
			t.Fatalf(
				"negative control failed to fail: forcing ENOSPC (not a member of "+
					"claude's rename-fallback errno set) should make the extracted "+
					"function propagate the error, but node exited 0 reporting: %s\n"+
					"This means the probe cannot be trusted to catch a real regression "+
					"in the positive case above — it would report PROBE-OK regardless "+
					"of whether the fallback actually ran.",
				out,
			)
		}
		if !strings.Contains(out, "PROBE-FAIL") {
			t.Fatalf("expected the harness's own PROBE-FAIL message on the negative control, got: %s", out)
		}
	})
}

// runFallbackProbe assembles and runs the probe module against snippet,
// injecting a renameFn that always rejects with forcedErrno. It returns the
// process's combined output and its exec error (nil on exit 0).
func runFallbackProbe(t *testing.T, nodeBin, snippet, funcName, forcedErrno string) (string, error) {
	t.Helper()
	workdir := t.TempDir()
	assembled, err := assembleProbeModule(workdir, snippet, funcName, forcedErrno)
	if err != nil {
		t.Fatalf("assembling the probe module from the extracted snippet: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, nodeBin, assembled)
	cmd.Dir = workdir
	out, runErr := cmd.CombinedOutput()
	return string(out), runErr
}

// locateClaudeBinary resolves the claude on PATH to its real, versioned
// binary (claude ships as a symlink from a launcher path into
// ~/.local/share/claude/versions/<ver>). CTXLOOM_CLAUDE_BINARY_CONFORMANCE
// overrides discovery, for pointing this probe at a specific version by
// hand.
func locateClaudeBinary() (string, error) {
	if p := os.Getenv("CTXLOOM_CLAUDE_BINARY_CONFORMANCE"); p != "" {
		return p, nil
	}
	p, err := exec.LookPath("claude")
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p, nil // fall back to the unresolved path rather than fail discovery
	}
	return real, nil
}

var (
	setLiteralRe    = regexp.MustCompile(`([A-Za-z_$][A-Za-z0-9_$]*)=new Set\(\[((?:"[A-Z]+",?)+)\]\)`)
	quotedMemberRe  = regexp.MustCompile(`"([A-Z]+)"`)
	importStmtRe0   = regexp.MustCompile(`import\{[^}]*\}from"[^"]*";`)
	funcDeclRe      = regexp.MustCompile(`(?:async\s+)?function\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*\(`)
	importRewriteRe = regexp.MustCompile(`import\{([^}]*)\}from"([^"]*)"`)
)

var conformanceBuiltinModules = map[string]bool{
	"fs": true, "fs/promises": true, "crypto": true, "path": true, "os": true,
	"node:fs": true, "node:fs/promises": true, "node:crypto": true, "node:path": true, "node:os": true,
}

// extractCredentialWriterSnippet locates, within the raw text of a claude
// binary, the errno-fallback Set literal that must contain both EXDEV and
// EBUSY (the shape of the dependency, not any name), the nearest function
// that consumes it via `<name>.has(`, and the contiguous run of import
// statements immediately preceding the Set declaration (everything the
// function's dependencies were declared from, in program order). It returns
// the self-contained snippet (imports + every top-level declaration in
// between + the target function) and the target function's discovered name.
func extractCredentialWriterSnippet(text string) (snippet, funcName string, err error) {
	// A 200+MB binary makes running setLiteralRe (with its nested quantifier)
	// over the WHOLE text needlessly slow. "EBUSY" as a quoted string is a
	// literal substring the regex must contain wherever it matches at all,
	// so find its (few) occurrences with a fast substring scan first and
	// only run the real regex on a small window around each candidate.
	var anchorLoc []int
	var setVar string
	searchFrom := 0
	for {
		rel := strings.Index(text[searchFrom:], `"EBUSY"`)
		if rel == -1 {
			break
		}
		hitPos := searchFrom + rel
		searchFrom = hitPos + len(`"EBUSY"`)

		probeStart := hitPos - 400
		if probeStart < 0 {
			probeStart = 0
		}
		probeEnd := hitPos + 400
		if probeEnd > len(text) {
			probeEnd = len(text)
		}
		probe := text[probeStart:probeEnd]

		loc := setLiteralRe.FindStringSubmatchIndex(probe)
		if loc == nil {
			continue
		}
		members := quotedMemberRe.FindAllString(probe[loc[4]:loc[5]], -1)
		hasEXDEV, hasEBUSY := false, false
		for _, m := range members {
			switch m {
			case `"EXDEV"`:
				hasEXDEV = true
			case `"EBUSY"`:
				hasEBUSY = true
			}
		}
		if hasEXDEV && hasEBUSY {
			// Re-express loc in terms of the full text, not the probe window.
			anchorLoc = []int{loc[0] + probeStart, loc[1] + probeStart, loc[2] + probeStart, loc[3] + probeStart, loc[4] + probeStart, loc[5] + probeStart}
			setVar = text[anchorLoc[2]:anchorLoc[3]]
			break
		}
	}
	if anchorLoc == nil {
		return "", "", fmt.Errorf("no `new Set([...])` literal containing both \"EXDEV\" and \"EBUSY\" found")
	}
	anchorPos := anchorLoc[0]

	winStart := anchorPos - 3000
	if winStart < 0 {
		winStart = 0
	}
	winEnd := anchorPos + 20000
	if winEnd > len(text) {
		winEnd = len(text)
	}
	window := text[winStart:winEnd]
	anchorRel := anchorPos - winStart

	// Chunk start: walk backward through the CONTIGUOUS run of import
	// statements immediately preceding the anchor. Minified code has zero
	// whitespace between statements, so "contiguous" means byte-adjacent.
	imatches := importStmtRe0.FindAllStringIndex(window[:anchorRel], -1)
	chunkStart := anchorRel - 500
	if chunkStart < 0 {
		chunkStart = 0
	}
	if len(imatches) > 0 {
		idx := len(imatches) - 1
		chunkStart = imatches[idx][0]
		for idx > 0 && imatches[idx-1][1] == imatches[idx][0] {
			idx--
			chunkStart = imatches[idx][0]
		}
	}

	hasPat := regexp.MustCompile(regexp.QuoteMeta(setVar) + `\.has\(`)
	hasLoc := hasPat.FindStringIndex(window[anchorRel:])
	if hasLoc == nil {
		return "", "", fmt.Errorf("found errno set %q but no `%s.has(` consumption site after it", setVar, setVar)
	}
	hasPos := anchorRel + hasLoc[0]

	fdMatches := funcDeclRe.FindAllStringSubmatchIndex(window[:hasPos], -1)
	if len(fdMatches) == 0 {
		return "", "", fmt.Errorf("no enclosing function declaration found before the %s.has( consumption site", setVar)
	}
	last := fdMatches[len(fdMatches)-1]
	funcStart := last[0]
	funcName = window[last[2]:last[3]]

	parenIdx := strings.Index(window[funcStart:], "(")
	if parenIdx == -1 {
		return "", "", fmt.Errorf("malformed function declaration for %s: no opening paren", funcName)
	}
	parenIdx += funcStart
	parenEnd, err := matchBracket(window, parenIdx)
	if err != nil {
		return "", "", fmt.Errorf("matching %s's parameter list: %w", funcName, err)
	}
	braceIdx := strings.Index(window[parenEnd:], "{")
	if braceIdx == -1 {
		return "", "", fmt.Errorf("no function body found for %s", funcName)
	}
	braceIdx += parenEnd
	funcEnd, err := matchBracket(window, braceIdx)
	if err != nil {
		return "", "", fmt.Errorf("matching %s's body braces: %w", funcName, err)
	}

	snippet = window[chunkStart : funcEnd+1]
	return snippet, funcName, nil
}

// matchBracket returns the index of the bracket that closes the one at
// s[openIdx] (one of '(', '{', '['), skipping over the contents of single-
// and double-quoted strings and template literals so a brace or paren
// inside a string or a `${...}` interpolation is never mistaken for
// structural nesting.
func matchBracket(s string, openIdx int) (int, error) {
	pairs := map[byte]byte{'(': ')', '{': '}', '[': ']'}
	closers := map[byte]bool{')': true, '}': true, ']': true}

	open := s[openIdx]
	want, ok := pairs[open]
	if !ok {
		return 0, fmt.Errorf("byte at %d (%q) is not an opening bracket", openIdx, open)
	}
	stack := []byte{want}
	i := openIdx + 1
	n := len(s)
	for i < n {
		c := s[i]
		switch c {
		case '"', '\'', '`':
			i++
			for i < n && s[i] != c {
				if s[i] == '\\' {
					i++
				}
				i++
			}
			i++
			continue
		}
		if closeWant, isOpen := pairs[c]; isOpen {
			stack = append(stack, closeWant)
			i++
			continue
		}
		if closers[c] {
			if len(stack) == 0 {
				return 0, fmt.Errorf("unbalanced closing bracket %q at %d", c, i)
			}
			expect := stack[len(stack)-1]
			if c != expect {
				return 0, fmt.Errorf("mismatched bracket at %d: got %q, expected %q", i, c, expect)
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return i, nil
			}
			i++
			continue
		}
		i++
	}
	return 0, fmt.Errorf("bracket opened at %d never closed", openIdx)
}

// assembleProbeModule rewrites any import in snippet whose module specifier
// is not a recognized Node builtin (i.e. an internal bun chunk this probe
// has no access to) to a small generated stub file exporting exactly the
// names that import requested, as one generic function usable both as a
// synchronous error-code accessor (`fn(err)` -> `err.code`) and as an
// awaited backoff delay (`await fn(ms)` -> resolves immediately) — the two
// calling conventions the two stubbed imports actually use here, inferred
// from usage rather than from whatever they happen to be named this
// release. It then appends a harness that drives funcName with an
// always-forcedErrno renameFn against a scratch file and reports PROBE-OK or
// PROBE-FAIL (with a reason) on stdout/stderr, and writes the result plus
// every stub to workdir. Returns the path to the assembled entry module.
func assembleProbeModule(workdir, snippet, funcName, forcedErrno string) (string, error) {
	if !regexp.MustCompile(`^[A-Z]+$`).MatchString(forcedErrno) {
		return "", fmt.Errorf("forcedErrno %q is not a bare uppercase errno name", forcedErrno)
	}
	stubIdx := 0
	rewritten := importRewriteRe.ReplaceAllStringFunc(snippet, func(m string) string {
		sub := importRewriteRe.FindStringSubmatch(m)
		names, mod := sub[1], sub[2]
		if conformanceBuiltinModules[mod] {
			return m
		}
		stubFile := fmt.Sprintf("stub%d.mjs", stubIdx)
		stubIdx++

		var exported []string
		for _, part := range strings.Split(names, ",") {
			part = strings.TrimSpace(part)
			orig := strings.TrimSpace(strings.SplitN(part, " as ", 2)[0])
			if orig != "" {
				exported = append(exported, orig)
			}
		}
		var body strings.Builder
		for _, n := range exported {
			fmt.Fprintf(&body, "export function %s(a){ if (a && typeof a === 'object' && 'code' in a) return a.code; return Promise.resolve(); }\n", n)
		}
		if err := os.WriteFile(filepath.Join(workdir, stubFile), []byte(body.String()), 0o600); err != nil {
			// ReplaceAllStringFunc has no error return; surface it via a
			// module that fails loudly when node tries to load it instead
			// of silently dropping the stub.
			return fmt.Sprintf(`import{}from"data:text/javascript,throw new Error(%q)"`, err.Error())
		}
		return fmt.Sprintf(`import{%s}from"./%s"`, names, stubFile)
	})

	footer := fmt.Sprintf(`
import { mkdtempSync, writeFileSync, readFileSync, statSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const dir = mkdtempSync(join(tmpdir(), "ctxloom-credprobe-"));
const target = join(dir, "credential.json");
writeFileSync(target, "OLD-CONTENT", { mode: 0o600 });
const beforeIno = statSync(target).ino;

const forcedRename = async () => {
  const e = new Error("%s: injected by the conformance probe's negative-control renameFn");
  e.code = "%s";
  throw e;
};

try {
  await %s(target, "NEW-CONTENT", { mode: 0o600, renameFn: forcedRename });
} catch (e) {
  console.error("PROBE-FAIL: the credential writer did not fall back on %s -- it propagated: " + (e && e.message));
  process.exit(1);
}

const afterContent = readFileSync(target, "utf8");
const afterIno = statSync(target).ino;

if (afterContent !== "NEW-CONTENT") {
  console.error("PROBE-FAIL: write did not land; target still reads " + JSON.stringify(afterContent));
  process.exit(1);
}
if (afterIno !== beforeIno) {
  console.error("PROBE-FAIL: target was replaced (inode changed from " + beforeIno + " to " + afterIno + ") instead of written in place -- a bind mount would not see this write");
  process.exit(1);
}
console.log("PROBE-OK");
process.exit(0);
`, forcedErrno, forcedErrno, funcName, forcedErrno)

	full := rewritten + "\n" + footer
	mainFile := filepath.Join(workdir, "probe.mjs")
	if err := os.WriteFile(mainFile, []byte(full), 0o600); err != nil {
		return "", err
	}
	return mainFile, nil
}
