<!--
J21 narration companion.

Prose ONLY. It never restates what the Gherkin already says business-readably,
and it carries no assertions of its own — j21_delegation.feature next to it is
the single source of truth for what J21 promises. What lives here is the
connective tissue a terse Given/When/Then cannot carry.

Marker convention: an opening prose block, one block per scenario keyed to
that scenario's exact name, and a closing block — the same three HTML-comment
marker pairs j2_setup.doc.md / j4_multi_engine.doc.md split on.
-->

<!-- doc:intro -->
A coordinator that delegates work is only as trustworthy as the wall between
its children. Alice spawns a "reviewer" agent to look things over and a
"fixer" agent to make changes — and the whole point of giving them different
jobs is that they should not be able to reach each other's tools. If the
fixer's child can quietly see the reviewer's MCP server, or vice versa, then
splitting the work into separate agents bought nothing: it is one shared
blast radius wearing two names.

This journey proves that boundary holds, and — just as important — that it is
AUDITABLE. It is not enough for the boundary to exist somewhere deep in the
spawn code; an operator looking at the coordinator's own run journal later
must be able to see, for any given run, exactly which permission mode and
which MCP servers it was actually granted. Before the work this journey stands
on, that journal recorded a run's escalation ladder, its runtime, its
credential hash — but nothing about what tools or posture the child actually
got. The privilege boundary was real, but it was invisible to anyone auditing
from outside the process. That gap is now closed: the journal records the
same class of fact for permissions and MCP servers that it already recorded
for the escalation ladder, and for the identical reason — so a later edit to
the config can never rewrite, after the fact, what a live run was actually
given.

One honest limit shapes how this journey is written, and it is worth stating
plainly rather than leaving a reader to discover it: `tests/acceptance` only
ever talks to a `ctxloom` subprocess over MCP stdio, so it can only observe
what crosses that wire or lands on disk. There is no MCP tool literally named
"roster" reachable from that subprocess — the tool by that name lives only on
a spawned session's own runner-terminated socket, a different process this
harness never becomes. Every scenario below instead reads the coordinator's
run-registry journal, `runs.jsonl`, directly off disk. That is not a
consolation-prize observable — it is the exact same data roster itself is
built from — but it is why every assertion here quotes a `run_id` and a raw
journal fact rather than a tool response.

Three tempting scenarios are deliberately absent, and are worth naming so
nobody mistakes their absence for an oversight. A child's assembled CONTEXT
contents are not asserted here: the mock backend used to spawn a real child
in this harness never echoes its fragments back, and no tool reachable from
outside returns a child's raw transcript. Child WORKSPACE isolation (writes
landing in its own worktree, not Alice's live checkout) is not asserted
either: the mock engine never touches the directory it is handed. And
artifact publish/fetch/tamper-refusal is not here at all — `agent_report` and
`agent_fetch_artifact` are registered only on a spawned session's own
per-cell runner socket, unreachable from an external MCP client. All three
need a different harness than this one to prove honestly, and faking any of
them would repeat a mistake this project has already been caught making once.
<!-- /doc:intro -->

<!-- doc:scenario: Each child is granted only its own MCP servers, never a sibling's or the coordinator's -->
This is the sharpest claim in the whole journey, and the reason it exists.
Alice's coordinator spawns two children from disjoint profiles — "reviewer",
whose bundle ships a `docs-lookup` server, and "fixer", whose bundle ships a
`deploy-tool` server. Each child's journaled grant is read back off the
coordinator's own run journal, and the check is not just "did its own server
appear" — it is also "did the OTHER child's server NOT appear". A coordinator
that unioned every resolved profile together, instead of scoping each child to
its own, would still pass the first half of that check and fail the second —
which is exactly the shape of bug this scenario exists to catch, and the one
its break-point verification (union all resolved profiles in
`childMCPServers`) was confirmed to trip.
<!-- /doc:scenario -->

<!-- doc:scenario: Each child's permission mode is recorded, not just implied -->
"Reviewer" runs in `plan` (read-only — it should never be silently allowed to
change anything) and "fixer" runs in `bypass` (it exists to make changes
without asking). Both are deliberately different modes, not the same value
asserted twice, because a permission mode journaled once and then reused as
the assumed value for a second run would prove nothing about whether it is
resolved per-child at all. This scenario reads both children's journaled
grants and confirms each carries its own real, resolved mode — visible to
anyone auditing the run later, not just true somewhere in memory while the
child was alive.
<!-- /doc:scenario -->

<!-- doc:scenario: A child's grant is journaled at enqueue and survives a later config edit unchanged -->
This is the durability claim, and it borrows its justification directly from
a mechanism that already existed for a different field: the escalation
ladder is journaled at enqueue precisely so a later config edit cannot
retroactively change what a live run's policy was, and permission mode and
MCP servers are now held to the same standard, for the same reason.

To make that a real proof and not a tautology about append-only files, this
scenario spawns "fixer" once, remembers its journaled grant, edits its
definition to take on "reviewer"'s profile and permission mode, and spawns it
again. The SECOND run's journaled grant genuinely reflects the edit — proving
the edit actually took effect, not that it was silently ignored — while the
FIRST run's journaled grant, read again after the edit, has not moved at all.
An operator auditing that first run a week from now, long after the config
changed twice more, still sees exactly what it was actually granted the
moment it was enqueued.
<!-- /doc:scenario -->

<!-- doc:scenario: The audit trail carries a server's name, never the command that can carry a secret -->
Both fixture bundles in this journey ship an MCP server whose command line
carries a plausible secret-shaped argument — the kind of thing a real MCP
server's launch command sometimes actually needs (an API token, a deploy
credential). The journal records that a server named `docs-lookup` or
`deploy-tool` was granted; it does not, and by design cannot, record the
command, arguments, or environment that launch it. This is the same boundary
the journal already draws for a session's bearer credential — it records a
SHA-256 hash of the token, never the token itself. Naming a capability and
carrying the means to exploit it are different things, and only the first
belongs in a file meant to be read by an auditor.
<!-- /doc:scenario -->

<!-- doc:outro -->
Four scenarios, each proving something the journal could not show an outside
observer before today: that a child's granted MCP servers are its own and
never a sibling's, that its permission mode is recorded rather than assumed,
that the record survives a later config edit unchanged, and that the record
never carries more than a name. What this journey does NOT prove — a child's
assembled context, its workspace isolation, its artifact exchange — needs a
different harness to prove honestly, and is left for exactly that, not faked
here to pad the count.
<!-- /doc:outro -->
