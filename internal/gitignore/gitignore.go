// Package gitignore provides idempotent maintenance of a project's .gitignore,
// appending ctxloom-managed ignore patterns without disturbing user entries.
package gitignore

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/ledger"
)

// Comment is the header under which ctxloom's ignore patterns are grouped.
const Comment = "# ctxloom private working state (rebuildable/local — cache, sessions, project id, local state)"

// PrivateStatePatterns are the .ctxloom paths that are rebuildable or purely
// local and so must never ride a distributable tree: the resolved-artifact
// cache, per-project session state, the project-id marker (ADR 0025 — private
// identity), and the third .ctxloom tier (paths.StateDir) — local-only
// checkout state nothing rebuilds (e.g. the dirty-tree-commit acknowledgement,
// paths.DirtyTreeCommitAckPath, and the lock sidecars under state/locks): a
// clone must never arrive pre-carrying somebody else's answer. Everything else
// under .ctxloom/ (config.yaml, remotes.yaml, lock.yaml, allowed_signers,
// approvals/, content/) is committed by omission — it's content, config, or
// trust state the project depends on.
//
// EVERY ENTRY NAMES A PATH SOME WRITER PRODUCES. Two did not: `.ctxloom/pieces/`
// (the sparse-checkout piece fetcher was never built) and `.ctxloom/ephemeral/`
// (the ephemeral tier is HOME-rooted — paths.HarpEphemeralDir resolves
// ~/.ctxloom/sessions/<harp>/ephemeral — and under the ruled layout no project
// ephemeral/ will ever exist). A pattern for a path nothing writes is not
// harmless insurance: it is this list claiming a tier exists, read by anyone
// auditing what ctxloom keeps out of git, and it outlived both features that
// would have justified it. Removing it only stops ctxloom WRITING the line —
// projects that already carry it keep it (Ensure appends and never removes
// anything but a superseded blanket rule), where it is inert.
// `.ctxloom/*.lock` is the one entry here for a path current ctxloom does NOT
// write, and it is deliberate rather than an oversight of the rule above:
// advisory lock sidecars now live under state/locks (filelock.ProjectPathFor),
// covered by `.ctxloom/state/`, but every project written by an earlier
// version has a `.ctxloom/config.yaml.lock` sitting at its root — the bug that
// prompted the move. That file is not going anywhere on its own, and leaving it
// visible in `git status` invites exactly one mistake.
var PrivateStatePatterns = []string{
	".ctxloom/cache/",
	".ctxloom/sessions/",
	".ctxloom/project-id",
	".ctxloom/state/",
	".ctxloom/*.lock",
}

// TransientArtifactPatterns are unambiguous generated artifacts that accumulate
// during hook application: the per-file settings backups, the antigravity
// workspace directory (legacy — antigravity itself was removed in 0.7.0, but
// a pre-upgrade project may still carry its debris, and nothing else writes
// there to reclaim the pattern), and Codex's project config plus the
// credential copy that lands beside it.
//
// GRANULARITY RULE. A directory is ignored WHOLESALE only when everything
// under it is machine-written; anywhere a project's own files share the
// directory, each ctxloom-written file is named individually so what a content
// repo may legitimately track (.claude/commands/*.md, .mcp.json) stays the
// project's choice.
//
//   - .agents/ qualifies wholesale: besides ctxloom's generated
//     hooks.json/mcp_config.json/skills, agy (antigravity, removed in 0.7.0)
//     filled it with per-conversation subagent scratch that must never be
//     committed — kept as a legacy-debris pattern for pre-upgrade projects.
//
//   - .codex/ does not, so its members are listed one by one: config.toml,
//     which ctxloom generates, and auth.json, which
//     internal/lm/isolation/auth.go's SeedCodexHome copies from the host's
//     ~/.codex/auth.json. The latter is a live credential rather than a config
//     artifact, but it has to be kept out of the tree exactly the same way.
//
//     THE .codex ENTRIES ARE NOW LEGACY, kept for pre-migration checkouts.
//     The engine-home policy moved codex's project-scoped home to
//     paths.EngineStateHome (.ctxloom/state/engines/codex), covered by
//     PrivateStatePatterns' ".ctxloom/state/" above — one rule for the whole
//     tier, credential included. ctxloom migrates a legacy <WorkDir>/.codex
//     into the new home the first time it writes (internal/codex's
//     migrateLegacyHome), but that migration only runs where ctxloom runs: a
//     checkout nobody opens again keeps its old files forever, and un-ignoring
//     them would surface a credential in `git status`. So these stay, and
//     blanket_retirement_test pins them.
//
// The rule is stated because it is per-entry and irreversible in one
// direction: broadening an entry to its whole directory later un-tracks
// whatever the project had committed there, silently.
var TransientArtifactPatterns = []string{".agents/", ".codex/config.toml", ".codex/auth.json"}

// WorktreeComment is the header under which the per-agent-worktree exclude block
// is grouped in .git/info/exclude.
const WorktreeComment = "# ctxloom per-agent worktree config (isolation; NEVER merge back)"

// WorktreeArtifactPatterns is the BROADENED set of per-agent config artifacts a
// fan-out member materializes into its isolated worktree cwd — the FULL written
// set across all engines. Unlike TransientArtifactPatterns (which deliberately
// leaves .mcp.json/.claude/commands as the project's choice), this set must keep
// EVERY ctxloom-written config out of a developer member's merge-back, so it
// covers .mcp.json and .claude/ wholesale plus the cache. It is written to
// .git/info/exclude (untracked, common-dir) — NOT the tracked .gitignore, which
// would itself merge back. Safe: excludes only affect UNTRACKED files, so a repo
// that genuinely tracks .mcp.json is unaffected.
//
// CLAUDE.md and the root AGENTS.md belong here too (a worktree
// orphan-accumulation fix): they are TRACKED per-agent context surfaces
// (claude.ClaudeCodeHookWriter.WriteContext, codex.CodexHookWriter.WriteContext
// — internal/shared/agent/managedcontext.go's doc names all three: CLAUDE.md,
// .agents/AGENTS.md, codex's AGENTS.md), and WriteManagedContext DELETES the
// file outright when the merged content is empty and the file was wholly
// ctxloom's. Omitting them here left isolation/worktree.go's
// skipTrackedConfig unable to hide that mutation: a per-agent run's
// materialize step turned a repo's committed CLAUDE.md into a tracked
// deletion (`git status` showed ` D CLAUDE.md`) that no skip-worktree bit
// covered, so teardown's WIP-safety check (correctly) read the worktree as
// dirty and refused `git worktree remove`, permanently orphaning it. agy's
// (antigravity, removed in 0.7.0) .agents/AGENTS.md was unaffected — already
// covered wholesale by the ".agents/" entry below.
var WorktreeArtifactPatterns = []string{
	".mcp.json",
	".claude/",
	".agents/",
	".codex/config.toml",
	".codex/auth.json",
	".kiro/",
	".ctxloom/cache/",
	"CLAUDE.md",
	"AGENTS.md",
	// opencode's own written artifacts (command/skill/context files
	// under .opencode/, its project-local opencode.json, and the managed-MCP
	// sidecar ledger) were missing from this set despite the doc comment above
	// calling it "the FULL written set across all engines" — an opencode-backed
	// per-agent worktree run left these untracked and unhidden.
	".opencode/",
	"opencode.json",
	// The shared managed-content marker, in whatever directory a writer puts
	// it. It replaces the five per-engine sidecar names this list used to
	// enumerate one at a time, and the per-file "*.ctxloom.bak" settings
	// backups, which no longer exist at all — every writer now knows what it
	// owns and edits only that, so nothing is copied aside first.
	ledger.Name,
}

// SupersededPatterns are the canonical spellings of the ignore rule written by
// older ctxloom versions that a current ctxloom must actively RETIRE rather
// than merely stop writing. A blanket .ctxloom/ rule predates
// version-controlled content moving INTO .ctxloom/ (content/, plus config.yaml,
// remotes.yaml and lock.yaml alongside it). Left in place it silently un-tracks
// the project's own content: `git add` reports nothing, and a content repo
// publishes an empty tree while every consumer's bundle refs fail to resolve.
// Ensure only ever appends, so no amount of re-running it can undo this —
// retirement must be explicit.
//
// This list is what ctxloom itself ever WROTE. Retirement matches on effect,
// not on this list (see isSupersededBlanket): a project's rule may have been
// hand-edited into any of the several spellings git treats identically, and
// each of them breaks the project the same way.
var SupersededPatterns = []string{".ctxloom", ".ctxloom/", "/.ctxloom", "/.ctxloom/"}

// isSupersededBlanket reports whether an ignore line excludes the WHOLE
// .ctxloom directory, in any of the spellings git treats as equivalent —
// anchored or not (`/.ctxloom`, `**/.ctxloom`), directory-suffixed or not, and
// with a trailing `/*` or `/**` wildcard.
//
// Only BLANKET forms match. The granular .ctxloom/<subdir>/ patterns in
// PrivateStatePatterns are current and must never be retired, nor may a
// comment or a user's own re-include (`!`) line be mistaken for a rule.
func isSupersededBlanket(line string) bool {
	s := strings.TrimSpace(line)
	if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, "!") {
		return false
	}
	s = strings.TrimSuffix(s, "/**")
	s = strings.TrimSuffix(s, "/*")
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimPrefix(s, "**/")
	s = strings.TrimPrefix(s, "/")
	return s == ".ctxloom"
}

// exactlyOneOf returns a line matcher for a fixed pattern set, matched by
// trimmed-line equality — the retirement rule for blocks ctxloom wrote itself
// and can therefore match verbatim.
func exactlyOneOf(patterns []string) func(string) bool {
	set := make(map[string]bool, len(patterns))
	for _, p := range patterns {
		set[p] = true
	}
	return func(line string) bool { return set[strings.TrimSpace(line)] }
}

// supersededComments are ctxloom-authored headers that head nothing once their
// patterns are retired. Scoped to headers ctxloom itself wrote — a user's own
// comment is never removed, even if it sits above a retired rule.
var supersededComments = []string{"# ctxloom local files"}

// SupersededBlanketLines returns the blanket `.ctxloom` ignore lines present
// in the file at path, reading only — it neither writes nor removes anything.
// Empty means the posture is fine; a missing file is empty, not an error.
//
// This exists because `ctxloom doctor`'s gitignore-posture check must not
// mutate a project's .gitignore just to ask "is it broken", and every other
// entry point in this package writes (Ensure/EnsureFile/RetireSupersededFile
// all call retireBlock, which replaces the file). Read-only detection is a
// genuinely separate need, not an oversight.
func SupersededBlanketLines(path string) ([]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var found []string
	for line := range strings.SplitSeq(string(content), "\n") {
		if isSupersededBlanket(line) {
			found = append(found, strings.TrimSpace(line))
		}
	}
	return found, nil
}

// RetireSupersededFile removes any SupersededPatterns line (and any
// ctxloom-authored header left heading nothing) from path, reporting whether
// the file changed. An absent file is a no-op, not an error. Callers that
// write the private-state block should retire first, then Ensure.
//
// Routed through SupersededBlanketLines first so there is exactly ONE
// detector for "is this line a superseded blanket rule" (isSupersededBlanket,
// reached both ways) rather than two independent scans that could drift apart
// — the read-only doctor check and this mutating retirement must never
// disagree about what counts.
func RetireSupersededFile(path string) (bool, error) {
	lines, err := SupersededBlanketLines(path)
	if err != nil {
		return false, err
	}
	if len(lines) == 0 {
		return false, nil
	}
	return retireBlock(path, supersededComments, isSupersededBlanket)
}

// RetireWorktreeConfigBlock removes the WorktreeComment header and any
// WorktreeArtifactPatterns lines from the file at path — a git common-dir
// info/exclude. Worktree teardown calls this once no linked worktree remains
// that still needs the shared exclude block: the block is
// written into the repo's ONE shared common-dir file (git has no per-
// worktree info/exclude — verified: a linked worktree's own
// .git/worktrees/<name>/info/exclude is NOT honored by `git status`), so it
// cannot be scoped to a single worktree's teardown. It can only be safely
// removed once nothing else is relying on it, or it silently reappears as
// dirty/untracked noise for every OTHER worktree (including the developer's
// own main checkout) the moment it is gone.
func RetireWorktreeConfigBlock(path string) (bool, error) {
	return retireBlock(path, []string{WorktreeComment}, exactlyOneOf(WorktreeArtifactPatterns))
}

// retireBlock is RetireSupersededFile's mechanism, generalized: remove every
// line retire reports, from the file at path, along with an orphaned header
// (one of headers) left immediately above a removed run. An absent file is a
// no-op, not an error.
func retireBlock(path string, headers []string, retire func(string) bool) (bool, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	isOrphanHeader := exactlyOneOf(headers)
	kept := keepLines(strings.Split(string(content), "\n"), retire, isOrphanHeader)

	updated := strings.Join(kept, "\n")
	if updated == string(content) {
		return false, nil
	}
	if err := replaceFile(path, []byte(updated)); err != nil {
		return false, err
	}
	return true, nil
}

// replaceFile overwrites path with data ATOMICALLY, keeping the file's
// existing mode.
//
// The file being rewritten here is USER-AUTHORED — a project's .gitignore, or
// a repo's .git/info/exclude — and it is rewritten by removing lines from it.
// A plain truncate-then-write loses all of it if the process dies in between,
// which for the migration path is the worst possible moment: Ensure has
// already decided the blanket rule must go and has nothing to restore it
// from. Staging beside the target and renaming makes the replacement a single
// step for any reader, and preserves the ORIGINAL mode rather than handing
// the user's file whatever the umask says.
func replaceFile(path string, data []byte) error {
	mode := os.FileMode(0644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}

	// Same directory, so the rename is within one filesystem.
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".ctxloom-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeds

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	// Close is checked, not deferred-and-discarded: it is where a write that
	// never reached disk surfaces.
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("gitignore: writing %s: %w", path, err)
	}
	return os.Rename(tmpName, path)
}

// keepLines returns lines with every retired line dropped, along with any
// ctxloom-authored header left immediately above a removed run — which would
// otherwise be left heading nothing.
func keepLines(lines []string, retire, isOrphanHeader func(string) bool) []string {
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if !retire(line) {
			kept = append(kept, line)
			continue
		}
		for len(kept) > 0 && isOrphanHeader(kept[len(kept)-1]) {
			kept = kept[:len(kept)-1]
		}
	}
	return kept
}

// Ensure appends the given patterns to projectDir/.gitignore under a single
// comment header, creating the file if absent. It is idempotent: only patterns
// not already present (by exact trimmed-line match) are written, and when none
// are missing the file is left untouched. An empty patterns list is a no-op.
//
// It first retires any SupersededPatterns. Retirement lives HERE, not at the
// call sites, because appending is powerless against a blanket .ctxloom/: git
// gives no way to re-include a path whose parent directory is excluded, so the
// granular patterns would be written and silently overridden, leaving the
// project's own .ctxloom/content/ invisible. Every writer of a project
// .gitignore must repair that rule, so no caller is given the chance to forget.
func Ensure(projectDir, comment string, patterns ...string) error {
	path := filepath.Join(projectDir, ".gitignore")

	retired, err := RetireSupersededFile(path)
	if err != nil {
		return err
	}
	if retired {
		// The blanket rule just removed WAS what kept private state out of git.
		// Install its replacement in the SAME block as the caller's patterns —
		// not every caller passes PrivateStatePatterns (the hook path passes only
		// transient artifacts), and retiring without replacing would leak cache/
		// and sessions/ into the repo. Retirement and its replacement are one
		// migration.
		// A caller list that already carries one of the private-state patterns
		// is fine: missingPatterns emits each pattern at most once.
		patterns = append(append([]string{}, PrivateStatePatterns...), patterns...)
		clidiag.WarnOnce("ctxloom",
			"removed a blanket .ctxloom/ rule from .gitignore: it predates version-controlled content living under .ctxloom/content/ and was hiding that content from git — replaced it with the granular private-state rules; review and commit the .gitignore change")
	}

	return EnsureFile(path, comment, patterns...)
}

// EnsureFile is Ensure targeting an arbitrary ignore file (e.g. a common-dir
// .git/info/exclude for per-agent worktrees), so the append/idempotency logic is
// written once. path's parent must already exist. An empty patterns list is a
// no-op.
func EnsureFile(path, comment string, patterns ...string) error {
	if len(patterns) == 0 {
		return nil
	}

	content, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	missing := missingPatterns(content, patterns)
	if len(missing) == 0 {
		return nil
	}
	warnOverriddenNegations(path, content, missing)
	return appendBlock(path, content, comment, missing)
}

// warnOverriddenNegations reports patterns being appended that will silently
// take precedence over a re-include (`!`) line the user already wrote.
//
// .gitignore is LAST-MATCH-WINS and this package only ever APPENDS, so a
// pattern written at the end of the file beats every earlier line — including
// a deliberate negation of the very path being ignored. Reordering is not
// available as a remedy: git cannot re-include a path whose parent DIRECTORY
// is excluded at all, so for a directory pattern the user's negation is dead
// however the file is ordered. What is available is not doing it silently —
// their line is still sitting there looking effective.
//
// Only negations the appended pattern actually shadows are reported: the
// same path, or anything beneath it when the pattern names a directory.
func warnOverriddenNegations(path string, content []byte, appended []string) {
	for line := range strings.SplitSeq(string(content), "\n") {
		neg := strings.TrimSpace(line)
		if !strings.HasPrefix(neg, "!") {
			continue
		}
		target := strings.TrimPrefix(neg, "!")
		for _, p := range appended {
			shadowed := target == p || (strings.HasSuffix(p, "/") && strings.HasPrefix(target, p))
			if !shadowed {
				continue
			}
			clidiag.Warn("ctxloom",
				"%s already contains %q, but ctxloom just appended %q below it. .gitignore is last-match-wins, so the ctxloom rule now takes precedence and your re-include no longer has any effect (git also cannot re-include a path whose parent directory is excluded, so moving the lines will not restore it). Your line was left untouched — remove or rework it if you meant to keep that path tracked.",
				path, neg, p)
		}
	}
}

// missingPatterns returns the patterns not already present in content, matched
// by exact trimmed-line equality, each emitted at most ONCE.
//
// Deduping against the FILE alone is not enough: a caller list carrying a
// repeated pattern (the private-state replacement prepended to a caller list
// that already contains it, see Ensure) would write the same line twice, and
// compensating for that needs a second map-based filter over the same
// []string. Seeding `present` and marking each emitted pattern makes one
// filter answer both questions.
func missingPatterns(content []byte, patterns []string) []string {
	present := make(map[string]bool)
	for line := range strings.SplitSeq(string(content), "\n") {
		present[strings.TrimSpace(line)] = true
	}
	var missing []string
	for _, p := range patterns {
		if present[p] {
			continue
		}
		present[p] = true
		missing = append(missing, p)
	}
	return missing
}

// appendBlock appends a comment header and the patterns to the file at path,
// inserting a separating newline when the existing content lacks a trailing one.
func appendBlock(path string, content []byte, comment string, patterns []string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	if err := writeBlock(f, content, comment, patterns); err != nil {
		_ = f.Close()
		return err
	}
	// A deferred, discarded Close() hides an ENOSPC/EDQUOT/EIO
	// write failure behind a nil error — the worst case being the migration
	// path in Ensure, which has already committed the REMOVAL of the
	// superseded blanket rule before this append runs; a silently-failed
	// append then leaves the project with FEWER ignore rules than before.
	return closeChecked(f, path)
}

// closeChecked closes f and, on failure, wraps the error naming path — split
// out so a test can drive Close-error propagation (via an already-closed
// *os.File) without needing to force a real ENOSPC/EDQUOT/EIO.
func closeChecked(f *os.File, path string) error {
	if err := f.Close(); err != nil {
		return fmt.Errorf("gitignore: writing %s: %w", path, err)
	}
	return nil
}

// writeBlock writes the separating newline (if needed), the comment header,
// and every pattern to w.
func writeBlock(w io.Writer, content []byte, comment string, patterns []string) error {
	if len(content) > 0 && content[len(content)-1] != '\n' {
		if _, err := io.WriteString(w, "\n"); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "\n%s\n", comment); err != nil {
		return err
	}
	for _, p := range patterns {
		if _, err := fmt.Fprintf(w, "%s\n", p); err != nil {
			return err
		}
	}
	return nil
}
