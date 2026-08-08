<!--
J4 narration companion.

Prose ONLY. It never restates what the Gherkin already says business-readably,
and it carries no assertions of its own — j4_multi_engine.feature next to it is
the single source of truth for what J4 promises. What lives here is the
connective tissue a terse Given/When/Then cannot carry.

Marker convention: an opening prose block, one block per scenario keyed to
that scenario's exact name (a Scenario Outline's marker uses the Outline's own
name once — it is not repeated per Examples row), and a closing block — the
same three HTML-comment marker pairs the generator splits j2_setup.doc.md on.
-->

<!-- doc:intro -->
A team never actually standardizes on one assistant. One developer lives in
claude-code, another in codex, another in kiro, a fourth in antigravity — and
every one of them still needs to onboard onto the same team standard: the same
fragment of context, the same shared skill, the same MCP tool, the same hook.
Without a common delivery mechanism, a team either forces everyone onto one
engine, or someone hand-translates the standard into four different config
formats and lets them drift the moment anyone forgets to update all four.

ctxloom's answer is to let the team author the standard exactly once — as a
profile — and materialize it into each engine's own native surface on demand.
Nobody forks the profile per engine. Nobody hand-writes a `.mcp.json` next to
a `.codex/config.toml` next to a `.kiro/settings/mcp.json` and prays they stay
in sync. One profile in, four genuinely different file layouts out, each one
exactly the shape its own engine already expects.

That "genuinely different" part is the whole point, not an inconvenience to
paper over. claude-code's context lands in a plain `CLAUDE.md`; kiro's lands
in `.kiro/steering/ctxloom-context.md`; antigravity's in `.agents/AGENTS.md`;
codex has no native context file at all. claude-code and kiro and antigravity
each get their own MCP file; codex folds its MCP servers into the same
`config.toml` that also carries its hooks. This journey proves those four
shapes are actually produced — by parsing each generated file in its own
format (JSON, TOML, or plain text) and checking the real field, never a bare
"the file exists" or "the key name shows up somewhere in it."

And it draws an honest line most journeys of this kind blur: writing a file in
an engine's native format is not the same claim as that engine having read it.
The first table below (materialization) covers three engines — claude-code,
kiro, and antigravity — and needs no engine binary at all: it is pure file
generation, provable today, for all three. codex is deliberately NOT a row in
that table. Its MCP, hook, and skill surfaces materialize exactly like the
other three, but its context does not, so it gets its own separate (`@wip`)
scenario below instead of a silent pass folded into the main table — the gap
is the reason for the split, not an oversight. The second table (live
delivery) covers claude, antigravity, and kiro, because those are the three
engines with a real, working, unattended path on this project's dev hosts
right now. Nobody reading this journey can mistake either table for more
coverage than it has — extending either later is adding a row, not rewriting
a claim.
<!-- /doc:intro -->

<!-- doc:scenario: The same profile materializes into each engine's own native surfaces -->
Carol's team authors one shared bundle — a fragment, a skill, an MCP server,
a hook — and Alice materializes that same profile once per engine. For each
of claude-code, kiro, and antigravity, this scenario opens the actual
generated file and reads the actual field: the assembled context carries the
fragment's own text, the MCP config's own JSON has the server's own command
under its own key, the hook config has the hook's own command under the
right event name, and the skill file has the skill's own body. Nothing here
is asserted by proxy — a stray file existing, or a key name appearing
somewhere in a blob, would not be enough, and previous acceptance coverage
elsewhere in this project (`manage.feature`'s bare `.mcp.json`-exists check)
is exactly the gap this scenario exists not to repeat.
<!-- /doc:scenario -->

<!-- doc:scenario: codex materializes its MCP, hook, and skill surfaces, but not its context — a confirmed product gap -->
This scenario is marked `@wip` and excluded from the default suite on
purpose: it documents a real product gap this journey found while proving
the other three engines, rather than hiding it.

codex's MCP, hook, and skill surfaces materialize correctly — the same
`.codex/config.toml` and prompt files land with the shared server's command,
the shared hook's command, and the shared skill's body, exactly like the
other three engines. But codex's *context* does not reach its target at all.
codex has no native context file by design (its only delivery mechanism is a
hook that reads a cache file at runtime) — that much is an intentional
difference in shape, not a bug, and the materialization scenario above
correctly does not claim a codex row for context. What this scenario catches
is narrower and real: `profile materialize` never populates the underlying
data codex's context hook needs, so on this one delivery path, codex's
context is a silent no-op — the only one of the four engines whose
materialized target ends up with no context at all, and nothing about the
run tells you that happened.

The gap is silent, which is the part worth naming out loud: a team could
materialize a codex target, hand it to a codex user, and that developer would
have the MCP server, the hook, and the skill, but never notice their
assistant was never given the actual shared context. This scenario pins that
exact, current, honest behavior so it cannot regress further and so it fails
loudly — forcing an intentional update — the day someone fixes it.
<!-- /doc:scenario -->

<!-- doc:scenario: A real engine actually receives the shared context and can use it -->
Materialization proves the bytes were written in the right shape. It does not
prove any assistant ever read them. This scenario closes that gap for the
four engines where it can be closed honestly today: a fragment carrying one
distinctive sentinel phrase is planted in the shared profile, a real engine is
launched against it, and the assistant is asked to repeat back the one marker
phrase it can see. If the marker comes back in the actual reply, the context
genuinely reached a genuinely running assistant — not a mock, not a captured
request, a real reply from a real engine process.

kiro joined this table once a logged-in `kiro-cli` became available to test
against: the same row, the same steps, no new Go and no new step definitions
— exactly the row-not-code claim this table was built to make. Its assistant
read the materialized `.kiro/steering/ctxloom-context.md` steering file
(kiro's stand-in for a SessionStart hook) and echoed the sentinel back through
kiro's own agentSpawn hook and skill-resource wiring, live.

codex joined this table once its context surface actually fired on the real
materialize/run path: a logged-in `codex` CLI reads the materialized
`AGENTS.md` and echoes the sentinel back, live, the same row and the same
steps as every other engine here — no new Go, no new step definitions. Every
row in this table still self-skips without live credentials, exactly like the
claude row already does in the setup journey this scenario borrows its shape
from.
<!-- /doc:scenario -->

<!-- doc:outro -->
Four engines, one authored standard, and an honest map of exactly how far
that standard's delivery is proven today: fully proven for the bytes on disk
across three of them — claude-code, kiro, antigravity. codex's MCP, hook, and
skill surfaces prove out the same way, but its context does not — a
confirmed, silent gap this journey names explicitly instead of folding into a
passing row. All four are additionally proven end-to-end through a real
running assistant, not just bytes on disk. That is the shape of claim this
journey is built to make — never more than what was actually checked, and
structured so that adding real coverage later means adding a row, not
rewriting a claim.
<!-- /doc:outro -->
