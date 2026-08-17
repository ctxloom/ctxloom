//go:build acceptance

// Package acceptance: the ISOLATION PROBE — a standalone, reusable live
// regression oracle for ctxloom's isolation seam.
//
// WHAT THIS IS, AND WHY IT IS NOT A j002200 SCENARIO. j002200_isolation.feature's own
// matrix (steps_j002200_isolation_matrix.go) proves ctxloom's SIDE of isolation —
// the right env var, pointed at the right scratch dir, seeded with the right
// bytes — against a cooperative recording SPY, on a PATH sanitized so no real
// engine binary is ever resolved. That is fast, hermetic, and exactly what a
// unit-adjacent regression test should be. It CANNOT prove the other half of
// the claim: that a real vendor engine actually reads the variable it was
// handed and actually writes only where the boundary says it may. This file
// is the live counterpart, and it is built to be invoked on its own, for one
// engine and one axis at a time, because its real job is not "pass once in
// this repo's CI" — it is "answer the same question again, unattended, every
// time claude-code/codex/kiro/opencode ship a new version." A
// scenario welded into j002200's own feature file could not serve that; see
// features/isolation_probe.feature and website/src/content/docs/security/
// isolation.md's "The executable probe" section for how to invoke it for a
// single engine/axis and how to read a failure.
//
// REUSE, NOT A FORK: this file shares live_engine_registry.go's liveAgents
// (binary/credDir/copyCreds/authCheck) rather than re-declaring per-engine
// knowledge, and shares the SAME resolveEnvOrMountAuth precedence production
// isolation code uses (env key first, host file second) so the probe can
// never claim to have proven a path it did not actually take — see
// probeAuthPath below.
//
// THE TWO OBSERVATION PROBLEMS THIS FILE SOLVES:
//
//  1. Worktree axis: internal/lm/isolation/worktree.go's
//     worktreeWorkspace.Cleanup removes the ENTIRE per-agent scratch tree
//     (worktree checkout + config-home) unconditionally the moment the run's
//     own process returns — there is no post-run inspection window at all.
//     watchScratch (below) polls for that scratch tree's appearance under
//     ~/.ctxloom/sessions/<harp>/ephemeral/ WHILE the live run is still in
//     flight and keeps the last non-empty snapshot observed — the same
//     "only vantage point is DURING the run" constraint
//     steps_j002200_isolation_matrix.go's package doc states for its own spy.
//  2. Container axis: `docker run --rm` (internal/lm/isolation/runtime.go)
//     auto-removes the container the instant its process exits, so
//     `docker diff` after the run has nothing to inspect either.
//     watchContainerDiff races the SAME way: it waits for the container to
//     start, then polls `docker diff` until the container disappears,
//     keeping the last successful enumeration.
//
// Both watchers share one shape: poll fast (40ms), never overwrite a
// non-empty last-known snapshot with an empty one — so the exact stop timing
// relative to teardown does not matter, only that polling STARTED before the
// run did.
//
// SECURITY: every function in this file that touches credential material
// (census, scratch snapshot, container diff) carries PATHS, SIZES, and
// SHA-256 HASHES only. None of them ever reads a credential file's bytes into
// a string that could be logged — see probeCensus/hashFile.
package acceptance

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/lm/isolation"
)

// probeAxis names the isolation axis under test.
type probeAxis string

const (
	probeAxisWorktree  probeAxis = "worktree"
	probeAxisContainer probeAxis = "container"
)

// probeAuthPath names WHICH of the two mutually exclusive auth resolution
// paths a probe run actually took — THE TRAP a credentialed CI lane must not
// fall into. internal/lm/isolation/auth.go's resolveEnvOrMountAuth (and
// worktree.go's seedCredentials, which follows the identical envTrigger-first
// precedence) prefers an API key riding the environment over a host
// credential file; when the env key is present, SEEDING IS SKIPPED ENTIRELY.
// A cell that ran the env-key path never exercised the credential-copy
// behavior at all, and must never be allowed to report as having proven it.
// See probeDecideAuthPath.
type probeAuthPath string

const (
	// probeAuthSeeded: no API key env var was set, so a host credential file
	// was copied into the isolated config-home (or container-mounted). This
	// is the path that proves credential-copy isolation: byte-identical
	// content, host original untouched.
	probeAuthSeeded probeAuthPath = "seeded-from-host-file"
	// probeAuthEnvKey: an API key rode the environment, so credential
	// seeding was skipped by design (the key is its own proof of intent).
	// This path proves NOTHING about credential-copy isolation — only that
	// the engine authenticated and ran.
	probeAuthEnvKey probeAuthPath = "env-api-key-bypass"
	// probeAuthNone: neither path is available; the cell must skip loudly
	// rather than silently.
	probeAuthNone probeAuthPath = "no-credentials"
)

// probeCensusEntry is one file's identity in a census: never its content.
// ModTime is included deliberately, not just Size+SHA256: a live,
// independently reproduced measurement found kiro-cli's
// `whoami` — a nominally read-only auth probe — advances
// ~/.local/share/kiro-cli/data.sqlite3's mtime with its SIZE UNCHANGED, a
// genuine write a size/hash-only census would miss entirely. Two "isolated"
// agents sharing that global file therefore share a mutable resource, not
// merely an identity — the census must be able to see a touch even when the
// touched bytes happen to round-trip unchanged.
type probeCensusEntry struct {
	Size    int64
	ModTime int64 // unix seconds
	SHA256  string
}

// hashFile returns a file's SHA-256 as hex, WITHOUT ever materializing its
// content as a string a caller could log — io.Copy streams straight into the
// hasher.
func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// probeCensus walks each of roots (relative to base) and returns a
// base-relative-path -> {size, sha256} census. A missing root is not an
// error (an engine that has never run yet has no ~/.claude at all) — it
// simply contributes nothing.
func probeCensus(base string, roots []string) (map[string]probeCensusEntry, error) {
	out := map[string]probeCensusEntry{}
	for _, root := range roots {
		full := filepath.Join(base, root)
		err := filepath.Walk(full, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if info.IsDir() {
				return nil
			}
			rel, rerr := filepath.Rel(base, p)
			if rerr != nil {
				return rerr
			}
			h, herr := hashFile(p)
			if herr != nil {
				return herr
			}
			out[rel] = probeCensusEntry{Size: info.Size(), ModTime: info.ModTime().Unix(), SHA256: h}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	return out, nil
}

// probeCensusDiff compares two censuses and returns a sorted list of
// "added:"/"removed:"/"modified:" PATHS ONLY — never content, never a hash
// value rendered anywhere a human would mistake for the secret itself (the
// hash is compared, not printed, unless a caller explicitly wants the
// diagnostic — callers here only print the path label). Empty means
// byte-identical, the host census unchanged.
func probeCensusDiff(before, after map[string]probeCensusEntry) []string {
	var out []string
	for p, b := range before {
		a, ok := after[p]
		switch {
		case !ok:
			out = append(out, "removed: "+p)
		case a.Size != b.Size || a.SHA256 != b.SHA256:
			out = append(out, "modified (content): "+p)
		case a.ModTime != b.ModTime:
			// Touched but byte-identical — a WRITE that happened to
			// round-trip the same bytes (fsync/rewrite), not a no-op. See
			// probeCensusEntry's doc: this is exactly the shape kiro-cli
			// `whoami` produces against its own global sqlite.
			out = append(out, "touched (mtime only, content unchanged): "+p)
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok {
			out = append(out, "added: "+p)
		}
	}
	sort.Strings(out)
	return out
}

// probeCensusRoots returns the host-census roots (relative to HOME) for the
// named REGISTERED backend type (claude-code/codex/kiro/opencode)
// — reusing liveAgents[...].credDir, the SAME root copyCreds
// already knows to copy from, rather than re-declaring the path a second
// time. claude-code carries one extra sibling file (.claude.json, the
// onboarding-state file copyClaudeCredentials also copies).
func probeCensusRoots(backendType string) ([]string, error) {
	key := backendTypeToLiveKey(backendType)
	a, ok := liveAgents[key]
	if !ok {
		return nil, fmt.Errorf("isolation probe: %q is not a registered live engine", backendType)
	}
	roots := []string{a.credDir}
	if backendType == "claude-code" {
		roots = append(roots, ".claude.json")
	}
	return roots, nil
}

// probeDecideAuthPath reports which of the two credential paths a run for
// backendType will take, GIVEN THE CURRENT AMBIENT ENVIRONMENT — the exact
// same precedence production code applies (resolveEnvOrMountAuth /
// seedCredentials: env key first, host file second), so this can never
// disagree with what the run itself actually does. Returns probeAuthNone
// when neither an env key nor a host credential file is available.
// MEASUREMENT SAFETY, load-bearing for every census this file takes: this
// function (and probeWorktreeAuthAvailable / probeContainerAuthAvailable
// below) deliberately NEVER call a liveAgent's authCheck — only os.Getenv and
// plain file reads (via copyCreds into a throwaway scratch dir). This is not
// an arbitrary style choice: liveAgents["kiro"].authCheck shells out to
// `kiro-cli whoami`, which is NOT side-effect-free — a live, independently
// reproduced measurement (before/after mtime on
// ~/.local/share/kiro-cli/data.sqlite3 across a session whose only kiro
// command was `whoami`) showed the file's mtime advance with its size
// unchanged, a genuine WRITE from a nominally read-only probe. Calling it
// from inside this file's before/after census window would make the probe
// itself the source of the very host-state change a kiro cell measures —
// indistinguishable, from the outside, from the real vendor leak this file
// exists to catch. See the isolation-probe doc page's "measurement safety"
// section for the full writeup; this comment is the enforcement point.
func probeDecideAuthPath(backendType string) (probeAuthPath, string) {
	key := backendTypeToLiveKey(backendType)
	a, ok := liveAgents[key]
	if !ok {
		return probeAuthNone, fmt.Sprintf("unknown engine %q", backendType)
	}
	if envKey := matchedEnv(a.apiKeyEnvs); envKey != "" {
		return probeAuthEnvKey, envKey + " set in the environment"
	}
	if realHomeDir == "" {
		return probeAuthNone, "no real HOME captured to look for a host credential file"
	}
	// A host credential file is present iff the SAME copyCreds this file
	// would run finds at least one source file to copy — cheapest correct
	// check is a dry probe against a throwaway scratch dir.
	probe, err := os.MkdirTemp("", "ctxloom-probe-authcheck-*")
	if err != nil {
		return probeAuthNone, fmt.Sprintf("could not probe host credential file: %v", err)
	}
	defer os.RemoveAll(probe)
	// The dry copy's own error is deliberately not checked here: this
	// function's whole job is to DECIDE whether a host credential file
	// exists, and the census emptiness check right below is that decision —
	// a copy error (zero files copied) and "no census entries" are the same
	// outcome for this purpose (this call site already gates on the copy's
	// real effect, unlike the @live gate steps).
	_ = a.copyCreds(realHomeDir, probe)
	roots, _ := probeCensusRoots(backendType)
	census, _ := probeCensus(probe, roots)
	if len(census) == 0 {
		return probeAuthNone, fmt.Sprintf("no %s env key and no host credential file under ~/%s", strings.Join(a.apiKeyEnvs, "/"), a.credDir)
	}
	return probeAuthSeeded, fmt.Sprintf("host credential file present under ~/%s", a.credDir)
}

// --- worktree-axis live observation -----------------------------------

// probeScratchSnapshot is the best-effort evidence gathered by watching the
// per-agent ephemeral scratch (worktree checkout + config-home) WHILE a live
// run is in flight. See this file's package doc, problem (1).
type probeScratchSnapshot struct {
	CheckoutDir  string
	ConfigHome   string
	CheckoutTree []string // relative paths seen under the worktree checkout
	ConfigTree   []string // relative paths seen under the config-home scratch
	// TokenFound/TokenContent: the probe's own trivial-write payload
	// (probeTokenFileName), read live from the checkout WHILE it still
	// exists — never a credential, safe to capture. "" / false when not yet
	// (or no longer) present at the moment of a given poll.
	TokenFound   bool
	TokenContent string
}

// watchScratch polls homeDir/.ctxloom/sessions for the per-agent scratch
// tree isolation/worktree.go's worktreeScratchPath creates
// (ctxloom-wt-*/ctxloom-cfg-*/ctxloom-home-*) and returns the LAST non-empty
// snapshot observed once ctx is cancelled. Start it BEFORE launching the live
// run; cancel ctx once the run's process has exited.
func watchScratch(ctx context.Context, homeDir string) <-chan probeScratchSnapshot {
	out := make(chan probeScratchSnapshot, 1)
	go func() {
		var last probeScratchSnapshot
		ticker := time.NewTicker(40 * time.Millisecond)
		defer ticker.Stop()
		sessionsDir := filepath.Join(homeDir, ".ctxloom", "sessions")
		for {
			select {
			case <-ctx.Done():
				out <- last
				close(out)
				return
			case <-ticker.C:
				// FIELD-WISE sticky merge, never a wholesale replace:
				// isolation/worktree.go's Cleanup removes the config-home
				// UNCONDITIONALLY but the checkout only conditionally (a
				// dirty checkout — e.g. our own probe token file — is left
				// in place). That means there is a real in-between tick,
				// late in the run, where ConfigTree has already gone empty
				// but CheckoutTree is still populated — a wholesale
				// `last = snap` on THAT tick would silently erase the good
				// config-home snapshot an earlier tick already captured.
				// Merging field-by-field, only overwriting a field when the
				// new scan actually saw something, makes the final `last`
				// the union of everything ever observed, independent of
				// which field happened to still be present on the very
				// last tick before cancellation.
				snap := scanScratchOnce(sessionsDir)
				if len(snap.CheckoutTree) > 0 {
					last.CheckoutDir = snap.CheckoutDir
					last.CheckoutTree = snap.CheckoutTree
				}
				if snap.TokenFound {
					last.TokenFound = true
					last.TokenContent = snap.TokenContent
				}
				if len(snap.ConfigTree) > 0 {
					last.ConfigHome = snap.ConfigHome
					last.ConfigTree = snap.ConfigTree
				}
			}
		}
	}()
	return out
}

func scanScratchOnce(sessionsDir string) probeScratchSnapshot {
	var snap probeScratchSnapshot
	harps, err := os.ReadDir(sessionsDir)
	if err != nil {
		return snap
	}
	for _, h := range harps {
		if !h.IsDir() {
			continue
		}
		ephemeral := filepath.Join(sessionsDir, h.Name(), "ephemeral")
		entries, err := os.ReadDir(ephemeral)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			full := filepath.Join(ephemeral, name)
			switch {
			case strings.HasPrefix(name, "ctxloom-wt-"):
				snap.CheckoutDir = full
				snap.CheckoutTree = listRelFiles(full)
				if data, rerr := os.ReadFile(filepath.Join(full, probeTokenFileName)); rerr == nil {
					snap.TokenFound = true
					snap.TokenContent = string(data)
				}
			case strings.HasPrefix(name, "ctxloom-cfg-"), strings.HasPrefix(name, "ctxloom-home-"):
				snap.ConfigHome = full
				snap.ConfigTree = listRelFiles(full)
			}
		}
	}
	return snap
}

func listRelFiles(root string) []string {
	var out []string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if rel, rerr := filepath.Rel(root, p); rerr == nil {
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// --- container-axis live observation ------------------------------------

// probeContainerSnapshot is the LAST successful `docker diff` enumeration
// observed before the container's `--rm` teardown removed it. See this
// file's package doc, problem (2).
type probeContainerSnapshot struct {
	Name string
	Diff []string // "A /path", "C /path", "D /path" — paths only, never content
}

// watchContainerDiff waits for a container named "ctxloom-iso-*"
// (internal/lm/isolation/container.go's containerName) to start, then polls
// `docker diff` on it every 40ms until ctx is cancelled, keeping the last
// successful enumeration (a "no such container" error, expected once `--rm`
// tears it down, never overwrites a good snapshot with nothing).
func watchContainerDiff(ctx context.Context, runtimeBin string) <-chan probeContainerSnapshot {
	out := make(chan probeContainerSnapshot, 1)
	go func() {
		var snap probeContainerSnapshot
		name := waitForContainerName(ctx, runtimeBin)
		if name != "" {
			snap.Name = name
			ticker := time.NewTicker(40 * time.Millisecond)
			defer ticker.Stop()
		loop:
			for {
				select {
				case <-ctx.Done():
					break loop
				case <-ticker.C:
					// len(lines) >= 0 is tautological (a slice
					// length is never negative), so this used to overwrite
					// snap.Diff on EVERY successful poll, including a
					// genuinely empty enumeration -- silently erasing a
					// prior good (non-empty) snapshot and contradicting this
					// function's own doc ("never overwrites a good snapshot
					// with nothing"). Only a non-empty enumeration updates
					// the kept snapshot now; a transient empty `docker diff`
					// mid-run no longer wipes out real evidence already
					// captured.
					if lines, err := dockerDiff(runtimeBin, name); err == nil && len(lines) > 0 {
						snap.Diff = lines
					}
				}
			}
		}
		out <- snap
		close(out)
	}()
	return out
}

// waitForContainerName polls `docker ps` for a container whose name carries
// the ctxloom-iso- prefix, returning "" if ctx is cancelled first.
//
// CONCURRENCY CAVEAT (found by hand, running two container-axis cells at
// once during this probe's own development): the "probe" agent name is
// identical across every cell, so containerName(agentID) — internal/lm/
// isolation/container.go — differs only in its random suffix, which this
// function has no way to predict or match against. Two container-axis cells
// running truly concurrently on the SAME host can race here: this function
// grabs whichever ctxloom-iso-* container `docker ps` returns first, which
// may belong to the OTHER cell's run, producing a docker-diff enumeration
// for the wrong engine entirely (confirmed by hand: an opencode cell run
// concurrently with a codex cell once reported codex's own paths). `just
// isolation-probe <engine> <axis>` runs one cell at a time and is immune to
// this; do not fan multiple container-axis cells out concurrently on one
// host/runner without first giving each a distinguishable agent name.
func waitForContainerName(ctx context.Context, runtimeBin string) string {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ""
		case <-ticker.C:
			cmd := exec.Command(runtimeBin, "ps", "--filter", "name=ctxloom-iso-", "--format", "{{.Names}}")
			cmdOut, err := cmd.Output()
			if err != nil {
				continue
			}
			for _, line := range strings.Split(strings.TrimSpace(string(cmdOut)), "\n") {
				if strings.HasPrefix(line, "ctxloom-iso-") {
					return line
				}
			}
		}
	}
}

// dockerDiff runs `docker diff <name>` and parses it into "A /path" /
// "C /path" / "D /path" lines — the writable-layer path list, paths only.
func dockerDiff(runtimeBin, name string) ([]string, error) {
	cmd := exec.Command(runtimeBin, "diff", name)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

// containerBootstrapAllowlist names the paths Docker itself (not the engine,
// not ctxloom) always writes into a fresh container's writable layer at
// start — /etc/hostname, /etc/hosts, /etc/resolv.conf carry the container's
// network identity, stamped by the runtime before any entrypoint code runs.
// A path under this list is not evidence of anything the engine did.
// Live-observed against docker 29.3.0 — re-verify if this
// allowlist ever needs widening (a new runtime version adding another
// bootstrap file is possible; see the isolation-probe doc page).
var containerBootstrapAllowlist = []string{
	"/etc/hostname",
	"/etc/hosts",
	"/etc/resolv.conf",
	"/etc/mtab",
	"/run",
	"/tmp",
	"/var/log",
	"/root/.cache", // present only when the entrypoint runs briefly as root before dropping privileges (PUID/PGID remap)
	"/home/ctxloom/.cache",
	"/home/ctxloom/.npm",
	"/home/ctxloom/.config/configstore",
}

// probeContainerUnexpected filters a docker-diff path list down to entries
// NOT under the container's fresh HOME (containerHome, expected: engine
// config-home writes) and not in containerBootstrapAllowlist — the enumerated
// "nowhere else" check assertion (d) is built on. Each returned entry is the
// RAW docker-diff line ("A /path" etc.), paths only.
func probeContainerUnexpected(diff []string, containerHome string) []string {
	var out []string
	for _, line := range diff {
		parts := strings.SplitN(line, "\t", 2)
		path := line
		if len(parts) == 2 {
			path = parts[1]
		} else if i := strings.IndexByte(line, ' '); i > 0 {
			path = line[i+1:]
		}
		if strings.HasPrefix(path, containerHome) {
			continue
		}
		// The read-observation instrument bind-mounts its trace dir here; it
		// surfaces in `docker diff` as a fresh writable-layer path but is the
		// PROBE's own scaffolding, not an engine write — never a leak. Excluding
		// it keeps the instrument from corrupting the very write-census (d)
		// asserts on (the measurement-debris failure mode).
		if path == isolation.ProbeTraceContainerDir || strings.HasPrefix(path, isolation.ProbeTraceContainerDir+"/") {
			continue
		}
		// containerHome's own ancestor directories (e.g. "/home", the
		// parent of "/home/ctxloom") always show as Changed ("C") the
		// instant anything is created under them — a parent-directory
		// metadata change, not a write anyone made TO that ancestor. EXACT
		// match only (never a prefix match): "/home" must not accidentally
		// allow "/home/someOtherUser/..." — a real leak would land there.
		if isAncestorOf(path, containerHome) {
			continue
		}
		allowed := false
		for _, a := range containerBootstrapAllowlist {
			if path == a || strings.HasPrefix(path, a+"/") {
				allowed = true
				break
			}
		}
		if allowed {
			continue
		}
		out = append(out, line)
	}
	return out
}

// isAncestorOf reports whether path is a (possibly non-immediate) parent
// directory of target, e.g. isAncestorOf("/home", "/home/ctxloom") == true.
func isAncestorOf(path, target string) bool {
	return path != target && strings.HasPrefix(target, strings.TrimSuffix(path, "/")+"/")
}

// --- run orchestration ---------------------------------------------------

// probeRunTimeout bounds every live `ctxloom run` this probe launches — a
// generous ceiling (real container image builds plus a real model call can
// legitimately take minutes) but a REAL one: a hung engine CLI (a wedged
// login prompt, a stalled network call) must not hang this probe forever.
// Found the hard way: a kiro --degraded run once ran past 4 minutes with no
// prior timeout at all to bound it.
const probeRunTimeout = 5 * time.Minute

// runWithTimeout starts cmd, kills it (best-effort) if it has not exited
// within probeRunTimeout, and returns cmd.Wait's error either way — a
// killed-on-timeout run still reports SOME exit error, which the caller's
// existing exit-code handling turns into a FAILED (not a silent hang).
func runWithTimeout(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	timer := time.AfterFunc(probeRunTimeout, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	defer timer.Stop()
	return cmd.Wait()
}

// probeToken returns a short, obviously-synthetic, random token safe to
// write into a live-engine prompt and assert on — never derived from or
// resembling credential material.
func probeToken() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("CTXLOOM-PROBE-%d", time.Now().UnixNano())
	}
	return "CTXLOOM-PROBE-" + hex.EncodeToString(b)
}

// probeTokenFileName is the fixed filename every probe run asks the engine
// to create — deliberately generic (no engine-specific naming) so the SAME
// assertion works for every row of the matrix.
const probeTokenFileName = "ctxloom-probe-token.txt"

// probePrompt is the trivial, minimal one-turn action every live cell asks
// the engine to perform: write a known token into a known file, nothing
// else. Kept short — these are real, paid engine calls.
func probePrompt(token string) string {
	return fmt.Sprintf("Write the exact text %s into a new file named %s in the current directory. Do nothing else: no explanation, no other files, no other changes.", token, probeTokenFileName)
}

// probeConfigYAML renders config.yaml for one probe run: the engine's own
// liveAgents config (model pinned cheap) plus a "probe" agent bound to it,
// permissions bypass (the file-write action needs it — every backend maps
// agent.PermissionBypass to its own skip-prompts flag, see
// internal/{claude,codex,kiro,opencode}/backend.go), and,
// for the container axis only, runtime: container.
func probeConfigYAML(backendType string, axis probeAxis) string {
	key := backendTypeToLiveKey(backendType)
	agent, ok := liveAgents[key]
	if !ok {
		// liveAgents[key] with no ok check used to silently produce a
		// zero-value liveAgent (empty base config), so an unregistered engine
		// name -- a new @live Examples row added without a matching
		// liveAgents entry -- rendered a config.yaml missing the engine's own
		// llm.configs block entirely, surfacing much later as a confusing,
		// unrelated failure rather than naming the actual mistake here.
		panic(fmt.Sprintf("probeConfigYAML: %q (resolved key %q) is not registered in liveAgents -- add it there before using it in a @live Examples row", backendType, key))
	}
	base := agent.config
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("agents:\n  probe:\n    llm: ")
	b.WriteString(key)
	b.WriteString("\n    profiles: []\n    permissions: bypass\n")
	if axis == probeAxisContainer {
		b.WriteString("    runtime: container\n")
	}
	return b.String()
}

// probeWorktreeAuthAvailable mirrors worktree.go's seedCredentials
// precedence for the ONE engine where the worktree axis's own auth gate is
// NOT simply "env key or host file", the same divergence
// probeContainerAuthAvailable documents for the container axis: kiro's
// XDG_DATA_HOME HomeVar is GatedOnCreds (auth.go's credentialSeedSpecs
// entry) — a worktree run REFUSES to start (fatal isolation finding, exit 3)
// rather than silently leaving a fresh, unauthenticated XDG_DATA_HOME in
// place, UNLESS KIRO_API_KEY is set. The host's kiro-cli SUBSCRIPTION login
// authenticates kiro itself but does NOT satisfy this specific gate, so a
// naive "does a host credential file exist" check (probeDecideAuthPath)
// would wrongly report kiro as probeable here — this override corrects
// that. Every other engine's worktree gate is the plain env-key-or-host-file
// shape probeDecideAuthPath already answers correctly.
func probeWorktreeAuthAvailable(backendType string) (probeAuthPath, string) {
	if backendType == "kiro" {
		if os.Getenv("KIRO_API_KEY") != "" {
			return probeAuthEnvKey, "KIRO_API_KEY set in the environment"
		}
		return probeAuthNone, "kiro's worktree axis REFUSES to start without KIRO_API_KEY (ctxloom's own fail-loud gate — internal/lm/isolation/auth.go's credentialSeedSpecs[\"kiro\"] gates XDG_DATA_HOME on KIRO_API_KEY and aborts rather than silently sharing the host's global credential store); the host's kiro-cli SUBSCRIPTION login authenticates kiro itself but does not satisfy this gate. This probe does not pass --degraded here (that would test the escape hatch, not isolation) — see the separate kiro-credential-store-leak scenario, which intentionally does."
	}
	return probeDecideAuthPath(backendType)
}

// probeContainerAuthAvailable mirrors internal/lm/isolation/auth.go's
// resolveXContainerAuth precedence for EACH engine, deliberately re-derived
// here rather than imported (this package cannot reach that package's
// unexported resolvers) — a change to production container-auth resolution
// and a change to this probe's understanding of it could, in principle,
// drift; the isolation-probe doc page flags this as the one place to
// re-verify by hand whenever internal/lm/isolation/auth.go's
// resolveXContainerAuth functions change.
//
//   - claude-code/codex/opencode: env key OR a mounted host credential file
//     — the SAME file and the SAME precedence as the worktree axis
//     (claudeCredentialCopyMounts/codexCredentialMounts/
//     opencodeCredentialMounts all source the identical host path
//     copyCreds does), so probeDecideAuthPath answers correctly for the
//     container axis too.
//   - kiro: ONLY KIRO_API_KEY (resolveKiroContainerAuth has NO
//     credential-mount fallback at all) — the host's subscription sqlite
//     that authenticates the WORKTREE axis cannot authenticate a
//     containerized kiro today.
func probeContainerAuthAvailable(backendType string) (probeAuthPath, string) {
	switch backendType {
	case "claude-code", "codex", "opencode":
		return probeDecideAuthPath(backendType)
	case "kiro":
		if os.Getenv("KIRO_API_KEY") != "" {
			return probeAuthEnvKey, "KIRO_API_KEY set in the environment"
		}
		return probeAuthNone, "kiro's container axis has NO credential-mount fallback in production (internal/lm/isolation/auth.go's resolveKiroContainerAuth) — only KIRO_API_KEY authenticates a containerized kiro run, and it is not set; the host's subscription credential (which DOES authenticate the worktree axis) cannot reach a container today"
	default:
		return probeAuthNone, fmt.Sprintf("unknown engine %q", backendType)
	}
}

// probeResult is one cell's full evidence, gathered whether the run PASSED
// or FAILED — callers decide pass/fail by reading it, this struct never
// decides for them.
type probeResult struct {
	Engine     string
	Axis       probeAxis
	AuthPath   probeAuthPath
	AuthReason string
	Token      string
	ExitCode   int
	Output     string

	// worktree axis
	Scratch  probeScratchSnapshot
	HostDiff []string // the before/after censuses themselves are written but never read anywhere; only their diff is consumed

	// container axis
	Container     probeContainerSnapshot
	ContainerHome string
	Unexpected    []string

	// Reads is the strace-observed read-set (container axis): which context
	// surfaces the real vendor CLI actually OPENED/stat'd/probed, INCLUDING the
	// ENOENT rows for paths it looked for and did not find — the half `docker
	// diff` (write-only) can never see. Empty on the worktree axis and on a
	// container run whose trace could not be retrieved (surfaced as a finding).
	Reads    []isolation.TraceRead
	ReadsErr string // why the trace could not be read/parsed, if it couldn't
}

// runProbeWorktree drives one live worktree-axis cell end to end: decide the
// auth path (forcedPath, if non-empty, requires the ambient environment to
// actually resolve to that path — a cell explicitly proving the env-key
// bypass must not silently fall back to the seeded path), seed credentials
// accordingly, census the host before/after, run `ctxloom run --agent probe
// --workspace worktree --one-shot` in the background while watchScratch races
// to observe the ephemeral scratch tree, and assemble the evidence. Returns
// an error only for a HARNESS failure (bad config, can't start the process);
// a live run that fails/times out/misbehaves is still a *result* the caller
// asserts against, not a Go error.
func runProbeWorktree(w *World, backendType string, forcedPath probeAuthPath, degraded bool) (*probeResult, error) {
	key := backendTypeToLiveKey(backendType)
	a, ok := liveAgents[key]
	if !ok {
		return nil, fmt.Errorf("isolation probe: unknown engine %q", backendType)
	}
	res := &probeResult{Engine: backendType, Axis: probeAxisWorktree}

	// The kiro-leak scenario intentionally runs WITHOUT KIRO_API_KEY and
	// WITH --degraded — probeWorktreeAuthAvailable's refusal is exactly the
	// gate --degraded exists to bypass, so degraded runs use the plain
	// env-key-or-host-file decision (probeDecideAuthPath) instead of the
	// kiro-specific override.
	authPath, reason := probeDecideAuthPath(backendType)
	if forcedPath != "" && authPath != forcedPath {
		return nil, fmt.Errorf("isolation probe: requested auth path %q for %s, but the ambient environment resolves to %q (%s) — set up the environment for the path you want to force", forcedPath, backendType, authPath, reason)
	}
	res.AuthPath, res.AuthReason = authPath, reason

	roots, err := probeCensusRoots(backendType)
	if err != nil {
		return nil, err
	}
	if authPath == probeAuthSeeded {
		// DELIBERATELY still a COPY, not the mapping the @live gates now use
		// (seedLiveCredentials, erased-collar): this probe's whole
		// measurement is a before/after census of a STAND-IN host home,
		// watching what production's own seeding and the engine leak into
		// it. Mapping would point that census at the developer's REAL
		// ~/.claude / ~/.codex and destroy the thing being measured. The
		// consequence is that a codex isolation-probe row still carries
		// jovial-employee's refresh-token-consumption hazard — flagged for
		// the human, not silently resolved here.
		//
		// probeDecideAuthPath already confirmed (via its own dry copy) that
		// a host credential file exists; a zero-files-copied error here
		// means the real copy disagreed with that dry probe (a race, a
		// permission change) — an anomaly worth failing loud on rather than
		// silently proceeding with an empty isolated credential dir.
		if err := a.copyCreds(realHomeDir, w.env.HomeDir); err != nil {
			return nil, fmt.Errorf("isolation probe: seeding %s credentials: %w", backendType, err)
		}
	}

	before, err := probeCensus(w.env.HomeDir, roots)
	if err != nil {
		return nil, fmt.Errorf("census before: %w", err)
	}

	token := probeToken()
	res.Token = token

	if err := w.env.WriteFile(".ctxloom/config.yaml", probeConfigYAML(backendType, probeAxisWorktree)); err != nil {
		return nil, err
	}
	if err := w.env.GitCommit("isolation probe config for " + backendType); err != nil {
		return nil, err
	}

	args := []string{"run", "--agent", "probe", "--workspace", "worktree", "--one-shot"}
	if degraded {
		args = append(args, "--degraded")
	}
	args = append(args, probePrompt(token))
	cmd := w.env.Command(nil, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	ctx, cancel := context.WithCancel(context.Background())
	scratchCh := watchScratch(ctx, w.env.HomeDir)

	runErr := runWithTimeout(cmd)
	// Grace window: lets the watcher's ticker take at least a couple more
	// polls right as the process exits and Cleanup begins, without changing
	// the "keep last non-empty" semantics that make the exact stop time
	// non-critical.
	time.Sleep(150 * time.Millisecond)
	cancel()
	res.Scratch = <-scratchCh

	res.Output = stdout.String() + stderr.String()
	if exitErr, ok := runErr.(*exec.ExitError); ok {
		res.ExitCode = exitErr.ExitCode()
	} else if runErr != nil {
		res.ExitCode = -1
	}

	after, err := probeCensus(w.env.HomeDir, roots)
	if err != nil {
		return nil, fmt.Errorf("census after: %w", err)
	}
	res.HostDiff = probeCensusDiff(before, after)

	return res, nil
}

// runProbeContainer drives one live container-axis cell end to end: decide
// container auth availability (probeContainerAuthAvailable), write config
// with runtime: container and workspace "none" — deliberately NOT worktree:
// a synthetic container HOME with no worktree mount keeps every
// writable-layer path attributable to either the container's fresh HOME or
// the plain project-dir bind mount, with no worktree-checkout machinery in
// between to confuse the picture — run `ctxloom run` in the background
// while watchContainerDiff races the container's own `--rm` teardown, and
// check the token file directly in the bind-mounted project dir (a plain
// bind mount, unlike the worktree axis's ephemeral checkout — the project
// dir is never torn down, so this needs no race at all).
func runProbeContainer(w *World, backendType, runtimeBin string) (*probeResult, error) {
	res := &probeResult{Engine: backendType, Axis: probeAxisContainer}

	authPath, reason := probeContainerAuthAvailable(backendType)
	res.AuthPath, res.AuthReason = authPath, reason
	if authPath == probeAuthNone {
		return res, nil // caller renders this as a loud, specific skip
	}
	// The container axis's own mount-fallback auth (claudeCredentialCopyMounts
	// / codexCredentialMounts / opencodeCredentialMounts) reads the SAME host
	// file copyCreds does, but from `hostHomeDir()` — this process's own
	// os.UserHomeDir(), which is w.env.HomeDir (HOME is overridden there) —
	// NOT the real developer's actual home. So the "seeded" path needs the
	// SAME credential copy the worktree axis performs, into the SAME
	// isolated stand-in-for-host directory, or production's own container
	// auth resolver finds nothing there and degrades — this is production
	// behavior working correctly, not a probe bug; the probe must set up its
	// stand-in host the same way for both axes. Same reason as the worktree
	// axis above for why this one call site keeps COPYING while the @live
	// gates map: the stand-in host IS the measurement.
	if authPath == probeAuthSeeded {
		key := backendTypeToLiveKey(backendType)
		if err := liveAgents[key].copyCreds(realHomeDir, w.env.HomeDir); err != nil {
			return nil, fmt.Errorf("isolation probe: seeding %s credentials: %w", backendType, err)
		}
	}

	token := probeToken()
	res.Token = token

	if err := w.env.WriteFile(".ctxloom/config.yaml", probeConfigYAML(backendType, probeAxisContainer)); err != nil {
		return nil, err
	}
	if err := w.env.GitCommit("isolation probe config for " + backendType); err != nil {
		return nil, err
	}

	// READ OBSERVATION: hand the production run path a host directory via the
	// dedicated probe-only env var. buildRunnerSpec's traceProbeFromEnv picks it
	// up (nil for any run that lacks this var — i.e. every production run) and
	// marks the RunSpec's Trace, which makes renderRunSpec grant
	// --cap-add=SYS_PTRACE, bind-mount this dir to /ctxloom-probe-trace, and wrap
	// the in-container engine exec in strace. The strace output lands in this
	// host dir, so it survives the container's --rm teardown with no race.
	traceDir, err := os.MkdirTemp("", "ctxloom-probe-trace-*")
	if err != nil {
		return nil, fmt.Errorf("probe trace dir: %w", err)
	}
	defer os.RemoveAll(traceDir)
	// World-writable so the in-container run user (remapped to the launching uid,
	// or container-root→host-user under rootless) can create the trace file.
	_ = os.Chmod(traceDir, 0o777)
	probeEnv := []string{isolation.ProbeTraceEnvVar + "=" + traceDir}

	cmd := w.env.Command(probeEnv, "run", "--agent", "probe", "--workspace", "none", "--one-shot", probePrompt(token))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	ctx, cancel := context.WithCancel(context.Background())
	diffCh := watchContainerDiff(ctx, runtimeBin)

	runErr := runWithTimeout(cmd)
	time.Sleep(150 * time.Millisecond)
	cancel()
	res.Container = <-diffCh

	res.Output = stdout.String() + stderr.String()
	if exitErr, ok := runErr.(*exec.ExitError); ok {
		res.ExitCode = exitErr.ExitCode()
	} else if runErr != nil {
		res.ExitCode = -1
	}

	res.ContainerHome = "/home/ctxloom" // defaultContainerHome, internal/lm/isolation/container.go
	res.Unexpected = probeContainerUnexpected(res.Container.Diff, res.ContainerHome)

	// Parse the strace output the wrapped engine exec wrote INTO the bind-mounted
	// host dir. Unlike the docker-diff race, this file is written from inside and
	// mounted out, so it is simply on disk once the run returns — no polling.
	tracePath := filepath.Join(traceDir, isolation.ProbeTraceOutFile)
	if raw, rerr := os.ReadFile(tracePath); rerr != nil {
		res.ReadsErr = fmt.Sprintf("strace trace file %s absent/unreadable after the run (%v) — strace may not be in the image, the probe seccomp profile may not have been applied, or the run never reached the engine exec", tracePath, rerr)
	} else {
		res.Reads = isolation.ParseStraceReads(raw)
		if len(res.Reads) == 0 {
			res.ReadsErr = fmt.Sprintf("strace trace file %s was present but held no parseable file reads (%d bytes)", tracePath, len(raw))
		}
	}

	return res, nil
}

// --- assertions ------------------------------------------------------------

// assertProbeWorktree checks the worktree axis's four guarantees. Every
// engine that reaches this function (i.e. was not already turned away by
// probeWorktreeAuthAvailable) is expected to hold ALL FOUR cleanly — kiro's
// known leak is asserted separately (see the feature file's dedicated
// --degraded scenario), because a bare worktree run for kiro without
// KIRO_API_KEY never reaches this far, and WITH KIRO_API_KEY kiro is no
// longer a leak case at all (both HomeVars relocate, per j002200's own hermetic
// proof).
func assertProbeWorktree(res *probeResult) error {
	if res.ExitCode != 0 {
		return fmt.Errorf("(a) response: run exited %d, want 0 — the credential or the engine itself is the suspect here, not isolation; output:\n%s", res.ExitCode, res.Output)
	}
	if !res.Scratch.TokenFound || !strings.Contains(res.Scratch.TokenContent, res.Token) {
		return fmt.Errorf("(b) token file: never observed %s carrying the probe's token under the isolated checkout %s (checkout tree seen: %v)", probeTokenFileName, res.Scratch.CheckoutDir, res.Scratch.CheckoutTree)
	}
	if len(res.Scratch.ConfigTree) == 0 {
		return fmt.Errorf("(c) config-home evidence: no writes were ever observed under the isolated config-home %s — either the watcher's polling window missed the whole run, or isolation never engaged", res.Scratch.ConfigHome)
	}
	if len(res.HostDiff) != 0 {
		return fmt.Errorf("(d) host census: changed under isolation (an unexpected leak): %v", res.HostDiff)
	}
	return nil
}

// assertProbeContainer checks the container axis's four guarantees. The
// token-file check (b) is NOT here — it is a plain bind mount at the
// project dir's identical host path (no teardown race at all, unlike the
// worktree axis's ephemeral checkout), so the caller checks it directly
// against the TestEnvironment.
func assertProbeContainer(res *probeResult) error {
	if res.ExitCode != 0 {
		return fmt.Errorf("(a) response: run exited %d, want 0 — the credential or the engine itself is the suspect here, not isolation; output:\n%s", res.ExitCode, res.Output)
	}
	if res.Container.Name == "" {
		return fmt.Errorf("(c)/(d) container evidence: no container named ctxloom-iso-* was ever observed running — either the run never launched a container, or the watcher's polling missed it entirely")
	}
	if len(res.Container.Diff) == 0 {
		return fmt.Errorf("(c) config-home evidence: `docker diff %s` enumerated ZERO writes — either the engine wrote nothing (surprising for a real oneshot call) or the diff race lost entirely; widen the polling window before trusting this", res.Container.Name)
	}
	if len(res.Unexpected) != 0 {
		return fmt.Errorf("(d) enumerated write set: %d write(s) landed OUTSIDE the isolated config-home (%s) and outside the known docker-bootstrap allowlist — this IS the leak this axis exists to catch: %v", len(res.Unexpected), res.ContainerHome, res.Unexpected)
	}
	// (e) READ observation — the half docker diff (write-only) cannot see. A real
	// vendor CLI oneshot MUST open files (its own config surfaces at minimum), so
	// an empty read-set means the strace instrument did not engage (strace absent
	// from the image, SYS_PTRACE not granted, or the trace never made it out) —
	// the probe would be silently back to write-only. The ENOENT rows within are
	// the point (a surface probed and not found = the silent-no-op shape), but
	// even a single successful read proves the instrument works.
	if len(res.Reads) == 0 {
		return fmt.Errorf("(e) read observation: strace captured ZERO file reads — %s", res.ReadsErr)
	}
	return nil
}

// probeReadsHasFailedResult reports whether the read-set contains at least one
// ENOENT/EACCES — a path the CLI PROBED and did not find. Surfaced as evidence,
// not required to pass (a given engine on a given box may find everything it
// probes), but its presence is the clearest proof the probe now sees what a
// write-only instrument never could.
func probeReadsHasFailedResult(reads []isolation.TraceRead) bool {
	for _, r := range reads {
		if r.Failed() {
			return true
		}
	}
	return false
}

// printProbeReport is THE loud, per-cell, always-printed line every scenario
// in this feature ends on, whether it ran, skipped, or failed — a run where
// every cell skipped must be visibly distinguishable from a run where every
// cell passed (see the isolation-probe doc page). It NEVER prints credential
// content — only the engine, axis, which of the two auth paths fired and
// why, and the verdict.
func printProbeReport(engine string, axis probeAxis, authPath probeAuthPath, authReason, verdict, detail string) {
	fmt.Printf("ISOLATION PROBE: engine=%-12s axis=%-9s authPath=%-22s (%s) result=%s%s\n",
		engine, axis, authPath, authReason, verdict, detail)
}
