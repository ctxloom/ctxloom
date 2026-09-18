# Coordinator notes — architecture audit 2026-09-18

## Verified empirically (not from the graphs)

### Seam 1 F1 — delegated children run WITHOUT ctxloom surfaces. CONFIRMED.
A live `agent_run` child (developer-host, harp wordy-sick-dude, spawned by this
coordinator) runs its claude engine with:
  HOME=/home/babbitt                      (the REAL home; no relocated engine home)
  no CLAUDE_CONFIG_DIR, no --settings, no --append-system-prompt-file
  --mcp-config /tmp/ctxloom-claude-chat-mcp-<rand>/.mcp.json   (os.MkdirTemp)
  CTXLOOM_SESSION_HARP=<harp>
and ~/.ctxloom/sessions/<harp>/ contains only persist/. No engine home, no
settings.json, no hooks, no commands/skills dir, no CLAUDE.md delivery.
Consequence: no managed hook fires for any child (ltk, tool-reflect, hud,
next-step, skill-mates); the agent's composed profiles reach it only through the
first-turn prompt, if at all; the child reads the human's real ~/.claude.
This is the path every child of the last two nights took. It contradicts the
"delivery never degrades; private EngineHome is the root" rule and the memory
"child agents honor composed profiles". Join question for seams 1+3+4.

### Container reuse — ABSENT (checked before the audit landed)
Shutdown exists (--rm; bounded remove/kill in isolation/attach.go;
isolation.ReapOrphanedContainers from operations startup). Reuse does not:
isolation.containerName mints a random token per start, coord.resumeChild
enqueues a fresh run, and under ResumeModeOneShot a new run_id per turn means a
container child pays a container start PER TURN. Candidate design: container
lifetime = session harp, engine recycled inside, idle-container reaper.

### Seam 2 F10 — the forward shim never forwards resource templates, so
ctxloom://fragments/{name} is unreachable behind it. Not yet re-verified here;
consistent with the premise-catalog instruction failing silently for shim users.

### Seam 7 F1 — keep marker swept into persist/ — CONFIRMED and FIXED (a9b61bfae)
HarpTopLevelArtifacts now excludes paths.SessionKeepMarkerFileName and
paths.NextStepFileName; the test names both, and the mutant (omitting them)
reports both as "authored artifacts". Package, build, lint and test-arch green.
Synthesis should treat 7.F1 as closed; the pattern (a hand-listed member
classification with no single predicate, seam 7 F9) stands.
