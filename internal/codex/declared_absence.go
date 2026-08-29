package codex

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// This file is codex's DECLARED ABSENCE: the single statement, and the single
// refusal path, for the fact that codex's home-keyed surfaces have no durable
// project location a harpless caller can write.
//
// WHY AN ABSENCE HAS TO BE DECLARED. codex reads hooks, MCP servers, prompts
// and skills ONLY from $CODEX_HOME — there is no cwd-keyed equivalent of
// claude's .claude/settings.json or kiro's .kiro/settings. Since the durable
// per-project engine home was retired, the only $CODEX_HOME ctxloom may write
// is a PER-SESSION instance (SessionHome), created at launch and rebuilt fresh
// next session; the alternative is the user's own ~/.codex, which ctxloom never
// writes. A static caller — `ctxloom profile materialize --backend codex`,
// `ctxloom manage install`, a hooks apply outside a run — has no session and
// therefore no target at all.
//
// The shape mirrors agentDescriptor.noHooksReason (internal/lm/backends):
// "a hook set written nowhere is indistinguishable from a hook set nobody
// declared", so the absence is DECLARED once, in one clause, and every surface
// that would otherwise fail silently reports it. Writing into a throwaway
// directory and printing its path was rejected: the next session gets a
// different instance, so it is a silent no-op wearing a success message —
// this project's signature failure.

// LaunchOnlySettingsReason is that clause. It is the ONE string every refusal
// here quotes, and the value internal/lm/backends' codex descriptor carries in
// its launchOnlySettingsReason field, so a caller that never touches this
// package (materialize's loss report, doctor) reports the identical sentence
// rather than a hand-maintained second copy.
const LaunchOnlySettingsReason = "codex settings/prompts/skills are delivered per-session at launch; no durable project home exists — see config_home"

// homeRefusal says WHY a codex surface has no directory to deliver into. There
// are exactly two reasons, they need different words, and neither may be
// reported as the other: one is a static caller with no session (this file),
// the other is a run that legitimately kept the user's own ~/.codex (D2).
type homeRefusal int

const (
	// homeAvailable: a ctxloom-writable codex home was resolved. Deliver.
	homeAvailable homeRefusal = iota
	// homeLaunchOnly: the caller is harpless — the declared absence above.
	homeLaunchOnly
	// homeIsHostOwned: the resolved home IS the user's real ~/.codex, which
	// ctxloom never writes.
	homeIsHostOwned
)

// launchOnlyError is the refusal an entry point that returns an error gives —
// CodexHookWriter.WriteSettings and MCPRegistrar's project scope. Both are
// asked for a FILE, and "" or a nil error would hand the caller a path that
// resolves to nothing while reporting success.
func launchOnlyError(what string) error {
	return fmt.Errorf("%s: %s", what, LaunchOnlySettingsReason)
}

// warnUndelivered is the refusal said OUT LOUD, for the delivery path, which
// has no error channel a skip may use (a skip is not a failure — materialize
// for the other engines, and codex's own cwd-keyed AGENTS.md, must keep
// working). surface names what did not arrive in the user's own words ("hooks
// and MCP servers"), so the message is actionable without knowing ctxloom's
// internals.
func warnUndelivered(surface string, why homeRefusal) {
	switch why {
	case homeLaunchOnly:
		clidiag.Warn("ctxloom", "codex %s were NOT written: %s. They are delivered into this session's own CODEX_HOME when an agent whose binding declares `config_home: project` launches; there is no durable project file to materialize. codex's cwd-keyed AGENTS.md context is unaffected and was still written.", surface, LaunchOnlySettingsReason)
	case homeIsHostOwned:
		real, _ := hostCodexHome()
		clidiag.Warn("ctxloom", "codex reads %s only from %s, which is YOUR OWN codex home — ctxloom does not write it, so this run gets none. Declare `config_home: project` on this agent's binding to give it a per-session codex home ctxloom can deliver into (AGENTS.md context is unaffected and was still written).", surface, real)
	}
}

// warnNothingToRemove is `manage hooks uninstall`'s half of the same
// declaration. An uninstall that removes nothing and says nothing is
// indistinguishable from one that missed the file, so it says so — and names
// the one directory a user may still find on disk and wonder about.
//
// The pre-relocation <workDir>/.codex is NOT ctxloom's to clean: the legacy
// directory gets no handling at all — no migration, no copy-in, no removal.
// Saying whose it is costs one clause and stops the
// obvious wrong fix.
func warnNothingToRemove(workDir string) {
	clidiag.Warn("ctxloom", "codex: nothing home-keyed to remove — %s, so a static install never wrote hooks, MCP servers, prompts or skills anywhere under %s. If a %s/.codex directory exists it predates this model and is yours: ctxloom did not create it and does not remove it.", LaunchOnlySettingsReason, workDir, workDir)
}
