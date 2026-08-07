# Journey narrative review: do these tell stories about real use?

**Status:** review, 2026-08-01. Base `0f2ffc7d`. Every command spelling in this
document was verified by running `--help` against a binary built from this tree
(`just proto && just build`), not read from a doc. The CLI tree was walked
exhaustively; 108 visible leaves plus the hidden `hook`/`llm`/`plan`/`util`
plumbing.

**Supersedes `docs/journey-coverage-gaps.md`.** That document is a coverage
document written before the verb-spine reorg, against a 43-item allowlist that
is now 22 items and shrank by a mechanism the document could not anticipate.
Correcting its spellings in place would have left a document whose *thesis* —
"here are the journeys that would retire the allowlist" — is no longer the
interesting question, because a third of the allowlist retired without a single
new journey being written. It is left in the tree as the historical record with
a superseded banner; §7 lists what it got factually wrong beyond spelling.

The standard applied throughout is the one J18 sets:

> J3 owns the ADVERSARY; J18 owns the PRODUCTION of the artifacts J3 assumes.

A journey states its own scope, names the seam between itself and its
neighbours, and has an actor whose goal a real person would recognise as their
own. Everything else is a test plan wearing a journey's clothes.

---

## 0. What actually changed, measured

The completeness gate at this commit accepts **22** uncovered surfaces
(`maxKnownUncoveredTotal = 22`, down from 43), and its allowlist is current:

| Uncovered | Items |
|---|---|
| Hidden session callbacks (4) | `hook inject-context`, `hook session-bind`, `hook stamp-plan`, `hook hud` |
| Editor / ACP (2) | `acp serve`, `acp client` |
| Sessions (2) | `session search`, `session watch` |
| MCP (1) | `mcp server edit` |
| Engine matrix (6) | `manage install --engine {codex,kiro,antigravity}`, `config init --engine {codex,kiro,antigravity}` |
| MCP tools, standalone surface (4) | `evaluate_triggers`, `compact_session`, `get_previous_session`, `list_sessions` |
| MCP tools, runner-only surface (3) | `roster`, `agent_report`, `agent_fetch_artifact` |

This reshapes the proposal work substantially. The old J19 (MCP server
management) is **coverage-complete already** — `mcp server create/list/show/
delete`, `mcp register`, `mcp unregister` all left the allowlist when the
deprecated aliases were deleted and `mcp.feature` was re-spelled. Same for
`config show`/`config get`/`config init`, `container tooling`, `init prompt`,
`acp list`, `bundle move`, `session delete`. Coverage is no longer the argument
for those journeys. Whether they tell a story anyone needs is.

---

## 1. The use cases nobody tells

This is the section that matters. Ranked by how much a real person would miss
them, not by how many leaves they would retire.

### 1.1 "Why can't my assistant see this?" — the diagnosis arc

**Nobody narrates finding out why nothing arrived.** This is the single largest
gap in the suite and it is not close.

Every journey here proves that the right thing reaches the assistant when
everything is configured correctly. The most common thing a real user actually
does with this product is discover that something *didn't* arrive and work
backwards. The failure surface is enormous and every branch of it is silent by
design: the item is pending review; the bundle is unsigned; the publisher's key
is not trusted; the fragment is in a bundle the profile doesn't name; the
profile isn't the default; the agent binds a different profile; the hooks were
never installed; the hooks were installed but the engine's settings file was
hand-edited since; the engine reads a surface ctxloom didn't write; the content
is retracted; the session ran `--degraded` and the finding scrolled past.

The tools for this all exist and are individually covered — `ctxloom doctor`,
`ctxloom review --list`, `ctxloom config show`, `ctxloom trust signer list`,
`ctxloom run --dry-run`, `ctxloom manage status`, `ctxloom search`. What does
not exist is a narrative that walks one symptom to one cause, and there is
nothing anywhere that proves the *diagnostic chain is complete* — that for a
given silent failure, at least one of these commands actually names it.

That last clause is the real claim, and it is testable: seed each way content
can go missing, and assert that some deterministic command **names the cause**.
A silent failure whose diagnosis is "read the source" is a product defect, and
this project's own memory calls silent no-op its characteristic bug. There is a
whole feature file (`fault_tolerance.feature`) that is this story told without a
person in it.

This is my top recommendation regardless of what else gets written.

### 1.2 Adopting ctxloom on a repo that already has a life — and backing out

J1 opens `Given Alice has a fresh project directory`. Almost nobody adopts a
tool this way. The real adopter has a repo with 40 contributors, a hand-written
`CLAUDE.md` that someone spent a weekend on, a `.mcp.json` with three servers,
`.claude/settings.json` with hooks from two other tools, and an `AGENTS.md`
because half the team moved to codex.

The narrative is: *will this wreck what I already have, and can I get out?*
Pieces of the answer exist scattered as surface tests — J5 has one row proving
a hand-authored `CLAUDE.md` survives materialization; `manage.feature` has
"Installing hooks preserves the exact numbers the user wrote by hand" and "A
statusline ctxloom cannot read refuses the write instead of replacing it"; there
is an `Uninstall strips the harness but keeps .ctxloom` scenario. Those are
three of the best scenarios in the whole corpus and they are filed under a
utility feature with no story attached.

**The back-out half is what makes adoption safe and it is completely
unnarrated.** `ctxloom manage uninstall` removes hooks, statusline, MCP entry
and command files — does the repo come back byte-identical to before install?
Does the hand-written `CLAUDE.md` survive uninstall as well as install? Nobody
asks. A tool that writes into four engines' config files and cannot prove it
reverses cleanly is a tool nobody senior will approve for a shared repo.

### 1.3 The solo developer whose preferences follow them

Every wired journey has a cast: Alice, Carol, Bob, Trent, Mallory, Priya. There
is a company, a team, a publisher, a trust root. **The single largest real user
segment — one person, one machine, no team, no company key, no remote — is not
the protagonist of any journey.**

Their arc is different and simpler: I have opinions about how I want my
assistant to behave; I work in eleven repos; I want my opinions in all eleven
without copying anything, and I want to change them in one place. That is the
user-scope-versus-project-scope story — `~/.ctxloom` content following the
person, project content following the repo, and what happens where they
conflict. J1 gestures at "her personal ctxloom repository" but immediately
routes it through signing and remotes, i.e. through the team machinery. The
plain version is the on-ramp for every other journey in this suite and it is
missing.

### 1.4 Recall — using the sessions you captured

J12 proves every engine's native log becomes a canonical transcript you own.
Nothing proves anyone ever *uses* one. The whole point of capture is the
Tuesday-afternoon moment: "I fixed this exact TLS handshake thing three weeks
ago, where is it."

The surfaces are all there and all uncovered or excluded: `ctxloom session
search <words>` (uncovered), `ctxloom session show <harp>`, `ctxloom session
distill` (excluded @live), and — the piece the old J14 draft explicitly
declined — **`ctxloom run --session <harp>`, which folds a recorded transcript
into a new run's assembled context, and `ctxloom run --session <harp>
--distill`, which folds the essence instead**. That is the delivery half. It
exists today, it is the actual payoff, and the draft that was supposed to cover
this ruled it out of scope in an honesty note.

The three uncovered MCP session tools (`list_sessions`,
`get_previous_session`, `compact_session`) are the same story told from inside
an agent's own reasoning loop.

### 1.5 Switching engines mid-project

J5 proves one profile reaching four engines *simultaneously*. J4 proves Bob
arrives on a different engine from Alice. Neither is the migration: I used
claude-code for three months, my team is moving to codex, what happens to my
agents, my default, my sessions, and — the part that bites — **the surfaces the
old engine left behind**.

`ctxloom agent edit <name> --engine codex` and `ctxloom manage install --engine
codex` both exist. Does the stale `.claude/settings.json` get cleaned up, or
does the old engine keep firing ctxloom hooks against a project that no longer
targets it? Does `ctxloom manage status` tell the truth about a half-migrated
project? Nobody knows, because `--engine` is measurably not a scoping flag —
see §7(b) — and no scenario ever removes an engine.

### 1.6 Review as a recurring chore, not a single decision

J3, J18, `review.feature` and `trust_surface.feature` between them prove the
review *mechanism* to a level of rigour I have not seen elsewhere in this
codebase. All of them review one item. The lived experience is fourteen items
pending after a Monday pull, most of them re-review of things you already
approved because the publisher edited a typo, and the real risk is that a human
facing that list every week learns to accept everything without reading.

That is a UX-shaped failure with a testable core: does re-review actually only
fire on *meaningful* change; does `ctxloom review --list` group so a human can
triage; does `--project` let a lead decide once for the team so twelve people
don't each face the same list. J18's content-hash-rebinding scenario is one
piece of this told at item scale. The volume story is where trust models
actually fail and it is untold.

### 1.7 Watching a delegated agent and steering it

J6 proves the privilege boundary. J17 proves the bus carries words. Neither has
a human in the loop while the child is running. The operator's actual arc is:
spawn it, watch it (`ctxloom session watch <harp>`), notice it going the wrong
way, and either redirect it (`agent_send`) or kill it (`agent_stop`).

`session watch` is the only uncovered session leaf with no natural home, and the
gaps doc parked it with `session search` purely because both are session
subcommands — two different people doing two unrelated things, grouped by noun.
Its real home is here, alongside `roster` (who is running), `agent_report`
(what a child says) and `agent_fetch_artifact` (what a child produced) — the
three runner-only tools that also have no home.

### 1.8 "It broke after I upgraded"

This project has an explicit no-backward-compat policy: old lockfile keys, old
selector grammars, old config shapes are broken deliberately, and re-init is the
documented upgrade path. **That policy makes the upgrade failure message part of
the product**, and nothing tests it. Nothing feeds ctxloom a config, lockfile,
approvals store or allowed_signers file written by an older version and asserts
it fails loudly with an actionable instruction rather than half-working.

`fault_tolerance.feature` proves malformed input degrades gracefully — which is
the *opposite* posture from the one the versioning policy requires. Both are
defensible; the suite should say which applies where.

### 1.9 Staying current without being surprised

`remote_content.feature` has nine scenarios that are genuinely narrative
material: an upgrade advances a pin, a held dependency is not upgraded, an
upstream change reaches the assembled context, a stale local checkout never
leaks into what's served, a skipped pull leaves old content and says so. That is
the "I depend on someone else's standards and I want to control when they change
under me" story, complete, told with no person and no arc. Two of its leaves
(`remote update`, `remote upgrade`) are still on `excludedLeaves` with reasons
the old gaps doc already showed were stale.

---

## 2. Narrative-quality verdict, per wired journey

| Journey | Verdict |
|---|---|
| **J1 setup** | **Strong.** Real actor, real goal, and the two-phase configure-then-restart arc is a genuine product truth most suites would have faked. Only flaw: it starts from a fresh directory, which is the least common real starting point (§1.2). |
| **J1b source augmentation** | **Weak — not its own journey.** No protagonist of its own; it is three riders on J1's setup interview. Every scenario is marked PROPOSED against unlanded work, so the file is RED by construction. The `b` suffix is itself the admission that nobody believed this was a journey. Fold into J1. |
| **J2 team authoring** | **Strong but thin.** Carol authors, Bob receives — a real loop, told three times (author, distill, change). The `@future` scenario about Carol's own live session is honest. Would benefit from the failure direction: Carol authors something malformed. |
| **J3 corporate signed** | **Exemplary.** Named adversary, stated scope ("provenance not secrecy, there is no Eve"), and every scenario is a way the claim could be a lie. This and J18 are the standard. |
| **J4 onboarding** | **Exemplary.** "Cloning IS the onboarding" is a thesis, and the scenarios attack it: pinning, the trust gate surviving a fresh machine, degradation on a machine missing companions, a different engine. Best-argued journey in the suite. |
| **J5 multi-engine** | **Strong.** The four-engine table is a claim about the world, and the preamble is careful to distinguish "we wrote the bytes" from "an engine read them". The hand-authored-file-survives scenario is one of the most valuable in the corpus. |
| **J6 delegation** | **Strong mechanism, weak narrative — and it should say so.** Alice appears in the Background and never again; every scenario is `the agent calls tool "agent_run"`. This is an excellent *threat model*, not a journey. `trust_surface.feature` handles exactly this situation by declaring "this is a REFERENCE, not a journey" in its preamble; J6 should do the same rather than keep a persona it doesn't use. |
| **J7 incident** | **Strong, slightly mixed.** The multi-developer retraction and the remote-goes-dark scenarios are a real incident shape and state precisely what they decline to restate from J3. The third scenario (embedded publisher key visibility) is a different story — trust-store honesty, not incident response — and sits oddly. |
| **J8 guardrails** | **Strong and unusual.** It narrates the limit of the product's own claim, and the overclaim scenario ("ltk is a cooperative redirect, not a sandbox") turns an honesty commitment into an executable assertion. More journeys should do this. |
| **J9 isolation** | **Weakest narrative-to-length ratio in the suite.** 289 lines, ~12 scenarios, and the actor's goal is "runs the mock agent under workspace worktree" — nobody's goal. It is a superb engineering matrix with the best honesty commentary in the repo, and it never states the story that motivates it: *I want to run a task I don't fully trust without it reaching my machine, my credentials, or my other work.* Add that opening and half the criticism dissolves; the scenarios themselves are excellent. |
| **J10 agent skill** | **Medium — J5's row promoted to a file.** Real fixture (the ctxloom-doctor skill), but the arc is "Carol materializes for five engines", which is J5's outline with a different payload. Its distinct claim (an engine loads this on its own, by progressive disclosure, without the user invoking anything) is stated in the preamble and never tested. |
| **J11 taskloom tags** | **Weak as a journey, excellent as a contract test.** Its own preamble says so: "proves the seam those tiers cannot see". No persona anywhere. The final scenario — an agent discovering the tag surface purely from what the MCP server advertises, with no prior knowledge — is the one genuine narrative in the file and is worth more than the rest combined. |
| **J12 transcript capture** | **Strong promise, mechanical body.** "Every engine's native log becomes one transcript you own" is a claim a person cares about, and the honesty block is exemplary. But every scenario is `I run "ctxloom session backfill <harp>"`; the person who wanted the transcript never appears and never reads one (§1.4). |
| **J16 worktree task store** | **Medium — a regression test with a good sentence on top.** "The finding dies with the worktree" is a real motivation crisply stated, and the exactly-one-store-file assertion is exactly right. Two scenarios; honest about being narrow. |
| **J17 cross-engine delegation** | **Strong.** Names the differentiator, states its seam against J6 precisely, and its `@wip` comment reporting a live-verified vendor limitation is a model of how to fail honestly in a test file. |
| **J18 signing** | **Exemplary — the stated standard, earned.** Named actor with an ordinary setup ("he configures nothing ctxloom-specific"), an explicit scope seam, payload assertions that read bytes fresh off disk, and a product bug filed as a `@wip` scenario rather than hidden. |

**Named weak:** J1b (fold into J1), J6 (relabel as reference), J9 (missing its
own motivating story), J10 (subsumed claim), J11 (contract test, no persona),
J16 (regression test, honestly narrow).

---

## 3. J19–J23, rewritten

The old numbering does not survive, because two of the five were coverage
groupings rather than use cases and one is now redundant. New proposals, in
priority order. Every spelling below was verified against the built binary.

### Declined outright: old J19 — "One MCP server, every engineer's assistant"

Its six leaves are all covered (`mcp.feature` drives `mcp server create/list/
show/delete`; `read_extras.feature` drives the auto-registration toggle). Its
one genuinely unnarrated claim — *declare a server once and it appears in
whatever file each engine actually reads, and a `--backend` filter is honoured*
— is J5's thesis applied to MCP, and J5 already carries a per-engine MCP row.
**Add two rows to J5 rather than write J19.** A journey whose whole arc is
"create a thing, list it, delete it" is a CRUD test with a persona pasted on;
Priya deserves better than being the name on a CRUD test.

### Declined as a standalone: old J20 — "Joining a team that does not use claude-code"

Sam/Ravi/Mei are three names for three rows of an Examples table. The six
engine-matrix leaves are a real gap, but they are a *matrix*, and the honest
place for a matrix is inside a journey that already owns the multi-engine axis.
Its two genuinely narrative pieces — install onto a non-claude engine, and the
unknown-engine fail-loud — are absorbed below.

---

### J19 — Adopting ctxloom on a repo that already has a life

**Actor and goal.** Dev leads a service repo with two years of history: a
hand-written `AGENTS.md` the team argued over, `.mcp.json` with servers from
another tool, and half the team on codex. He will not adopt anything that
rewrites those files, and he will not adopt anything he cannot remove. His goal
is to find out, in one sitting, whether ctxloom takes over cleanly and reverses
cleanly.

**Arc.** He runs `ctxloom manage install --engine codex` in the live repo, not a
fresh directory. His `AGENTS.md` survives byte-for-byte outside the managed
markers and ctxloom's content lands alongside it. His pre-existing MCP entries
survive; his hand-written hook numbers survive. `ctxloom manage status` names
codex and lists exactly what was wired. Ravi does the same for kiro and Mei for
antigravity. Then Dev fat-fingers `--engine kodex` and finds out immediately
rather than on the next command. Finally he backs out: `ctxloom manage
uninstall` removes what ctxloom wrote and the repo is what it was, including the
`AGENTS.md` he started with.

**Leaves covered (current spellings).** `ctxloom manage install --engine codex`,
`--engine kiro`, `--engine antigravity`; `ctxloom config init --engine codex`,
`--engine kiro`, `--engine antigravity` (the gate's `engineMatrixLeaves` already
points at `ctxloom config init`, so no deprecated spelling is needed — the old
gaps doc's both-spellings note is dead); `ctxloom manage status`; `ctxloom
manage uninstall`.

**The scenario that will be RED, deliberately.** `--engine bogus` exits **0**
today, prints a success line, and writes `engine: bogus` into a `config.yaml`
that fails ctxloom's own schema validation on the next command. Write it for the
intended behaviour. Also worth pinning honestly: `--engine` does *not* scope
what gets written — every engine's surfaces land regardless — so the scenario
should assert what it actually controls (`agents.default.engine`) rather than
implying a scoping it does not have.

**Blockers.** None. Fully hermetic; no engine binary required.

### J20 — Why can't my assistant see this?

**Actor and goal.** Alice's teammate says the commit-convention guidance is
reaching *his* assistant and not hers. She has one goal: find out why, using
ctxloom, in under five minutes, without reading source.

**Arc.** Each scenario seeds exactly one way content can go missing and asserts
that a deterministic command **names that cause**: the item is pending review
(`ctxloom review --list` names it and says why); the publisher's key is not
trusted (`ctxloom trust signer list` shows the key absent, `ctxloom doctor`
reports the signer count); the fragment is in a bundle her profile does not name
(`ctxloom run --dry-run` shows the assembled context without it, `ctxloom
search` finds the fragment and shows which bundle holds it); the hooks were
never installed (`ctxloom manage status`); the config is malformed (reads still
work, and the warning names the file). The closing scenario is the one that
makes this a journey rather than five unit tests: for every seeded cause, *some*
command names it — a silent failure with no diagnostic is a failing scenario.

**Leaves covered.** `ctxloom doctor`, `ctxloom review --list`, `ctxloom manage
status`, `ctxloom config show`, `ctxloom trust signer list`, `ctxloom run
--dry-run`, `ctxloom search`. All already covered as leaves — **this journey
buys no coverage and is still the most valuable item on this list.**

**What it absorbs.** `fault_tolerance.feature` and `doctor.feature` in full.

**Blockers.** None.

### J21 — The four callbacks every session already depends on

**Actor and goal.** Alice installed ctxloom weeks ago and has not thought about
it since. Four ctxloom callbacks run on every session she starts. She never
invokes them and would never notice if one started returning nothing — her
assistant would just be a bit worse and she would blame the model.

**Arc.** Unchanged from the old proposal, which was already good, and it is the
right shape: the journey **reads the hook command lines back out of the
generated `.claude/settings.json` and runs those exact lines** with the payload
the engine would send. That is the difference between this and four unit tests —
if the installer writes a command line the binary cannot honour, nothing else in
the suite would catch it. `hook inject-context <hash>` handed a hash with no
file behind it must fail loudly, not return an empty `additionalContext` and
exit 0; `hook hud` fed malformed JSON must not render a plausible statusline out
of zero values; `hook stamp-plan` must leave the rest of the plan file
byte-identical and must not stamp twice; `hook session-bind`'s binding must be
readable afterwards through `ctxloom session list`.

**Leaves covered.** `ctxloom hook inject-context`, `ctxloom hook session-bind`,
`ctxloom hook stamp-plan`, `ctxloom hook hud` — all four verified present and
hidden in this tree. The gate credits these only on a literal `I run "ctxloom
hook ..."` step (`ranAsCommand`).

**Blockers.** None. One new step shape ("I run the hook command the harness
installed for <event>"), which is the point.

### J22 — Driving ctxloom from an editor

**Actor and goal.** Dana works in Zed and does not want a second terminal. She
wants her editor's agent panel to talk to ctxloom directly — assembled context,
her profiles, her engine binding — and to choose which of her agents the panel
drives.

**Arc.** `ctxloom acp list` gives her one advertisable entry for plain ctxloom
plus one per agent binding, with a ready-to-paste Zed `agent_servers` block. Her
editor launches `ctxloom acp serve`, which initialises, opens a session, rides
her assembled context on the first turn, records the session under a harp, and
streams back. Her profile sets appear as ACP session modes; switching mode
re-assembles the lead context while the engine stays pinned at launch;
`session/load` on the harp replays history. Separately, before binding anything,
she smoke-tests a configured ACP-client entry with `ctxloom acp client --llm
kiro "say hello"` — one headless turn, one answer.

**Leaves covered.** `ctxloom acp serve` (was `acp server`), `ctxloom acp client`
— both uncovered. `ctxloom acp list` (was `acp entries`) is already covered
weakly by `agent.feature`; this journey should take it and assert the entry
**count and names** against the configured agents plus valid JSON, because a
missing entry means an agent is silently unreachable from the editor.

**Note for the preamble.** Both `acp serve` and `acp client` print
"Experimental — interfaces may change and it is not yet verified against all
editors" in their own help. Say so, so the journey's guarantees are not read as
stability promises.

**Blockers.** An in-harness ACP client driver (for `acp serve`) and a stub ACP
agent (for `acp client`). Real work; schedule after J19–J21.

### J23 — Finding the session where you already solved this

**This is the retargeted J14, not a renumbered J23.** The old J23 bolted
`session search` to `session watch` because both are session subcommands. They
are two people doing two unrelated things; the watch half moves to J24.

**Actor and goal.** Marcus debugged this exact TLS handshake failure about three
weeks ago, in some session, in some repo. He wants it back — not just to *find*
the session, but to start today's work with what he learned in it.

**Arc.** `ctxloom session search tls handshake` searches session metadata and,
for distilled sessions, the essence body, requiring every word to match. He
narrows with a third word and the set shrinks; `--all` widens beyond the current
project; `--full` prints the matched essences. He reads one with `ctxloom
session show <harp>`. Then the payoff the old J14 declined: **`ctxloom run
--session <harp>`** folds that session's recorded transcript into today's
assembled context, and **`ctxloom run --session <harp> --distill`** folds the
essence instead. The assertion is on the payload the mock engine receives — the
same `=== Prompt ===` technique J1 and J9 already use — because "the command
succeeded" proves nothing here.

The MCP half is the same story from inside an agent's reasoning loop:
`list_sessions` to see what came before, `get_previous_session` to pull the
prior essence, `compact_session` to distil the one it is in. Assert the essence
**body** reaches the caller.

**Leaves and tools covered.** `ctxloom session search` (uncovered); `ctxloom
session show`, `ctxloom session list` (covered, re-homed); MCP `list_sessions`,
`get_previous_session`, `compact_session` (all three uncovered).

**Fixture note.** `session.feature`'s existing `a recorded session
"amber-swift-owl"` step already seeds an index entry *and* an essence. Seed two
or three with distinct essence bodies and the search half is wirable today with
zero new fixture work — the cheapest item on this list. `ctxloom session
distill` is currently `excludedLeaves`-excluded as `@live`; whether
`compact_session` is hermetic against the mock is worth one experiment, because
if it is, that exclusion is stale.

### J24 — Watching a delegated agent, and steering it before it finishes

**Actor and goal.** Alice hands a refactor to a delegated child and does not
want to find out how it went forty minutes later. She wants to see the turns as
they happen, and to intervene — redirect or kill — while intervening still
costs less than starting over.

**Arc.** She spawns two children, asks `roster` who is running, and watches one
with `ctxloom session watch <harp>`: recorded entries replay as scrollback, live
events follow, each completed response marked by a boundary; `--format json`
gives NDJSON, one event per line carrying exactly one of `entry` or `boundary`.
`--source live` against a harp no orchestrator holds must **error**, per its own
help, not fall back silently. She sees the child heading the wrong way, sends it
a correction (`agent_send`), and the correction lands in its next turn. The
other child reports back (`agent_report`) and she collects what it produced
(`agent_fetch_artifact`) — the **bytes the child wrote**, not a path or a
success flag. Then she stops one (`agent_stop`).

**Leaves and tools covered.** `ctxloom session watch` (uncovered); MCP `roster`,
`agent_report`, `agent_fetch_artifact` (all three uncovered, runner-only).

**Blockers, stated plainly.** `session watch` has no bounded hermetic exit
today; the tractable route is `--source live` against a delegated child that
terminates, which J6 and J17 already know how to spawn and stop. The three
runner-only tools are reachable **only** through the runner-terminated MCP
surface — a scenario written against a standalone `ctxloom mcp serve` will
silently never see them. J6/J17 already run there. That colocation is the
argument for putting all four in one journey; it is also why this one is
genuinely harder than J19–J21 and should be scheduled accordingly.

---

### Second tier — named, not specified

Worth writing, in this order, once the above land: **J25 the solo developer**
(§1.3, user-scope vs project-scope content), **J26 switching engines
mid-project** (§1.5, including stale-surface cleanup), **J27 upgrade breakage**
(§1.8, the no-backward-compat failure message as product), **J28 staying current
without surprise** (§1.9, absorbing `remote_content.feature`), **J29 review at
volume** (§1.6).

`mcp server edit` (uncovered) opens `$EDITOR` on a bundle-scoped MCP entry; its
predecessor `bundle mcp edit` was `excludedLeaves`-excluded for exactly that
reason. Recommend moving it to `excludedLeaves` alongside `config edit` rather
than leaving it on the uncovered list indefinitely. `evaluate_triggers` (the
last uncovered standalone tool) folds into J11 as the gaps doc proposed — a
deferred task with a revive trigger, the tool returning *that* task and not a
task whose trigger has not fired.

---

## 4. The 31 surface features

**Legitimately unit-style; keep as they are (14).** `bundle`, `command`,
`fragment`, `profile`, `llm`, `version`, `search`, `read_extras`, `editing`,
`transfer`, `mcp_resources`, `config`, `remote`, `mcp`. These are CRUD and read
surfaces. A journey for "create a fragment and list it" would be a test plan
with a story bolted on, which is the failure mode this review exists to name.
`config.feature`'s assertions have already been fixed since the old gaps doc
flagged its vacuous `the output matches "."` — the current file asserts real
sections and a fixture-set value.

**Legitimate references — and two of them should say so (3).**
`trust_surface.feature` already opens "This is a REFERENCE, not a journey: there
is no persona and no story arc." That is exactly right and it is the model.
`coordination_contract.feature` and `mcp_tools.feature` are the same kind of
document and should carry the same disclaimer. So should J6, which is a
reference wearing a Background persona.

**@live twins; keep (4).** `distill_live`, `remote_content_live`,
`isolation_probe`, and the `@live` rows inside journeys. `isolation_probe` is
notable: it is where J9's *narrative* actually lives (a real engine, real
credentials, a real leak found by running kiro), and J9's own comments say so.

**Should be absorbed into narratives (7).**

| File | Goes to | Why |
|---|---|---|
| `fault_tolerance.feature` | **J20** | Five scenarios that *are* the diagnosis story, told with no person. |
| `doctor.feature` | **J20** | Doctor is the diagnosis entry point; it currently proves marker vocabulary, not that anything gets diagnosed. |
| `manage.feature` | **J19** | Contains the three best adoption-risk scenarios in the corpus (hand-written numbers preserved, unreadable statusline refused, uninstall keeps `.ctxloom`) filed under a utility heading. |
| `review.feature` | **J29** (second tier) | The mechanism is proven; the chore is not. |
| `remote_content.feature` | **J28** (second tier) | Nine narrative-shaped scenarios with no narrative. |
| `session.feature` | **J23** | Its seeded-session fixture is J23's fixture. |
| `run.feature` | **J20** / **J1** | `--dry-run` is a diagnostic; the deny_tools and empty-tag-selection scenarios are guardrail material. |

**Duplicates or dead (3).**

- `trust_cli.feature` — one scenario, "A per-item acceptance and a rejection are
  recorded", fully subsumed by J18's accept/reject scenarios and
  `trust_surface.feature`'s exhaustive table. Delete or merge.
- `init.feature` — **dead and hidden.** Its single scenario runs `ctxloom manage
  init`, a command that **does not exist in this tree**. `manage_test.go:43`
  asserts it was deleted; the generated reference page for `ctxloom manage` says
  the duplicate `manage init` entry point was removed. The scenario survives
  only because it is tagged `@network` and therefore never executes. This is the
  exact failure the brief warns about — a stale spelling nobody caught — living
  *inside the acceptance suite*. `ctxloom manage install --help` also still
  refers to `manage init` in its prose. Both need fixing; neither is in this
  pass's scope.

  **RESOLVED 2026-08-06 (`wieldable-washcloth`).** `init.feature` is deleted and
  the `manage install` help prose now contrasts against root `ctxloom init`
  instead of the removed command. The class is guarded too:
  `TestExcludedScenarios_InvokeCommandsThatStillExist` resolves every excluded
  scenario's command against the live cobra tree, so a deleted command breaks
  its scenario the same day rather than hiding behind a tag.
- `skill.feature` — half journey already (Alice authors a package, a tampered
  signature fails, a bundle skill beats a kiro builtin). Overlaps J10
  substantially. Recommend merging J10 into it rather than the reverse: the
  authoring arc is the story, the five-engine materialization is the matrix.

---

## 5. J13 / J14 / J15 verdicts

### J13 ensemble — **right story, stale surface. Do not wire as written.**

The brief flagged J14 as the stale draft. **J13 is stale too, and nobody
noticed.** It narrates `ctxloom map` throughout and cites `internal/cli/map.go`.
Neither exists. There is no `map` leaf in this tree and no `map.go` in
`internal/cli`. The surface is now `ctxloom weave --map-only` (its own help
names `--map-only` as an alias for `--no-synthesize`), with members supplied by
`--agents a,b` or `-p prof1,prof2` and the synthesizer by `-s`.

The *story* is sound and worth telling: fan one task across several members,
each on its own binding and its own workspace, then reduce. The claim that an
ensemble is not a shared blast radius wearing a plural name is exactly the right
thesis and it echoes J6's correctly. Its per-member-record-file discipline
(honesty note 1) is a genuinely good piece of harness design.

Retarget every `ctxloom map` to `ctxloom weave --map-only`, re-verify the source
line citations (they are all against a file that no longer exists, so treat all
of them as unverified), and note that `ctxloom weave` is currently on
`excludedLeaves` as "@live-only, no hermetic fixture" — a claim this draft's own
analysis contradicts, since the mock-backed fan is hermetic and only the
*synthesis quality* needs a model. That exclusion should be narrowed or dropped
when this lands.

### J14 memory — **wrong surface AND wrong scope. Rewrite as J23.**

Two problems, and the second is worse than the rename.

The known one: it narrates `ctxloom memory compact/list/show` and cites
`internal/cli/memory.go`. That group is gone; the surface is `ctxloom session
distill` / `session list` / `session show`, plus `ctxloom session search`.

The unknown one: **the draft rules out the only part anyone cares about.** Its
honesty note 2 says delivery into a fresh session is "a DIFFERENT surface and is
deliberately NOT claimed here", leaving the journey to prove that compaction
writes a file and that a read command reads it back. That is a store/recall
round-trip, not a memory journey. And the reason given is no longer true:
`ctxloom run --session <harp>` and `--distill` fold a recorded transcript or its
essence into a new run's assembled context, today, on the covered `run` path
with the mock engine, observable through the mock record.

Do not retarget this draft. **Write J23 (§3) instead**, which keeps the good
parts — the mock-as-compaction-engine technique, the transcript-must-exist
caveat, the "Alice's earlier self is the only party" framing — and puts the
delivery half back where it belongs.

### J15 container — **still the right story; wire it, with one correction.**

The framing is genuinely good: an operator deciding whether to mandate
`runtime: container` across a fleet needs to know *before* running anything, and
the honesty note about which parts of `container check`'s payload are
host-dependent is exactly the right discipline. The axis-labels-not-values
assertion is the honest hermetic reading.

Two corrections. Its "deliberately not restated" list cites `agent set/show`,
which no longer exists (`agent create` / `agent edit` / `agent show` — the
upsert was split); and `agent.feature` now drives `ctxloom container check
claude-code` and `ctxloom container tooling` in their canonical spellings, so
the overlap note needs re-checking rather than assuming.

The unlanded harness work it names is real and small: `@container` is not yet in
the default tag-exclusion set (`acceptance_test.go` excludes
`@live/@network/@future/@wip`), and the reachability gate step needs writing.
Worth doing — it is the only draft of the three that can be wired mostly as
written.

---

## 6. Which document I chose, and why

**Superseded, not revised in place.** `docs/journey-coverage-gaps.md` gets a
one-line superseded banner pointing here; its body is left intact as the record
of what the surface looked like before the reorg. Revising it in place would
have produced a document with the same title and a different thesis: it was
written to answer "what journeys retire the 43-item allowlist", and a third of
that allowlist retired without anyone writing a journey. Its most-cited artefact
— the canonical-leaf-versus-deprecated-alias table — describes a world with 20
deprecated aliases in it, and there are now zero. Preserving it as history and
arguing the current question here is more honest than editing until the two
disagree silently.

---

## 7. What `docs/journey-coverage-gaps.md` got wrong beyond the spellings

Spelling drift is documented in the brief and is real (43+ dead spellings).
Beyond it:

**(a) Its central premise is obsolete.** "This document proposes the journeys
that would retire that allowlist" — a third of the allowlist retired via alias
deletion and mechanical re-spelling, exactly the "cheap fix" the document warned
against doing alone. That warning was right and the work was still done that
way; the resulting scenarios (`mcp.feature`, `remote.feature`, `session.feature`)
carry canonical spellings with the same thin assertions they always had. The
document's own prediction came true and it is not recorded anywhere.

**(b) Its `--engine` finding is correct and still unfixed.** Verified again at
this commit: `--engine` only sets `agents.default.engine`; every engine's
surfaces are written regardless. `--engine bogus` still exits 0 with a success
message and writes a config that fails ctxloom's own schema validation on the
next command. Carried into J19.

**(c) Its `opencode` drift finding is still live.** `engineMatrixLeaves` lists
four engines; the binary supports five. `manage install --engine opencode` and
`config init --engine opencode` are two further uncovered variants the gate is
structurally blind to. J9 and J10 both already drive opencode as a first-class
engine, which makes the omission harder to defend now than when it was written.

**(d) It did not know J13 was stale.** It calls out J14's dead `ctxloom memory`
group and says nothing about J13's dead `ctxloom map` — same class of defect,
same drafts directory, unflagged. See §5.

**(e) Its excluded-leaf verdicts are still accurate and still unactioned.**
`remote update` and `remote upgrade` are still excluded with "needs a second
remote commit" / "needs an update cycle" reasons that `testenv.AdvanceSignedRemote`
already invalidates. `session distill`'s `@live` exclusion is still questionable
for the reason it gave. `bundle push`'s reason still overstates the blocker.
Nothing moved.

**(f) `config.feature`'s vacuous assertion has been fixed** — the one concrete
defect it named that someone acted on. The current file asserts real sections
and a fixture-set value. Worth recording so nobody re-files it.

**(g) A defect it could not have known about: `init.feature` is dead.** Its
single scenario drives `ctxloom manage init`, deleted from the binary and
asserted deleted by `manage_test.go`, surviving only because `@network` keeps it
from ever running. See §4. **Resolved 2026-08-06** — file deleted, help prose
corrected, and the blind spot itself closed by a guard over every excluded
scenario.

---

## 8. Scope of this pass

Analysis and proposal only. No `.feature` file, no Go source, no
`docs/architecture/findings-index.md` and no `docs/cli-surface-recommendation.md`
was modified. `just test-arch` passes (exit 0, 53 gates).
