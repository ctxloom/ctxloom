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

### Design A + adversarial review (21-adversarial-review.md): 25 refuted / 33 held
Shape held (hexagon, composite package, one launch value, one verbs layer). Broke
at the PROCESS BOUNDARY: R1 *Package cannot cross the wire (Plan must be the wire
value, PlanFor in the originator); R2 the MCP endpoint is an output of
Dynamic.Serve per runner process, not a per-harp typed launch input; R3 no single
config owner / invalidation contract, hot reload deleted with nothing replacing it
(proposed composite.Library{Current; Invalidate}); R5 container children cannot
receive mail (runner Inbox sweeps an unmounted spool; RunnerLink has no push verb);
R6 "exactly two depths" contradicts delegation.depth and womanless-quarterly; R7
Resolve mints unconditionally — no resume arm; R8 host facts (Binary(), host home)
handed to a container as if universal; R9 the routing rule is a substitution arm
under a new name; R10 protobuf in core/bus and afero in core/engine violate the
design's own R1/R9 on day one; R11 no reach-back-from-container test in any slice.
These are the divergence axes to weigh B against: wire value, endpoint identity,
config ownership, mail push to containers, depth, resume, container carriers,
routing, core purity, reach-back proof.

### Human rulings after the A/B comparison (2026-09-18, evening)
- go-plugin: DELETE the arm (B's proposal stands). One arm: the runner is spawned
  directly and StartRun carries the launch.
- Engine port: B's shape. "Engines should be largely declarative, reusable parts;
  constructor feeds in session specifics. Pretty much all operations should go
  through engine declarations polymorphically."
- Package on the wire: "the package should serialize and be available for the local
  launcher; that should reduce duplication" — the Package is a serializable value;
  the runner and the local launcher consume the SAME serialized form. (Overrides
  the review's "Package never leaves the originator"; the review's size concern
  still needs an answer.)
- MCP endpoint: "does still need to be minted per SESSION, as it carries the
  dynamic context assembly and needs that session's packages." Per session harp,
  not per run; stable across one-shot turns.
- Package layout (A's rename vs B keeping names): "probably better" — side not
  named; OPEN. Cost both.
