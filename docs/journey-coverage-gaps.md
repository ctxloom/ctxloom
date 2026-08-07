# Journey coverage gaps: prose journeys for the uncovered CLI surface

> **SUPERSEDED 2026-08-01 by `docs/journey-narrative-review.md`. Do not cite
> command spellings from this document.** It was written before the verb-spine
> reorg and contains 43+ references to spellings that no longer exist
> (`trust signer add`, `mcp server add`, `manage config init`, `acp entries`,
> `session query`, `remote add/remove/browse`, `agent set`, `session forget`,
> bare `ctxloom acp`, and all 20 deprecated aliases). Its premise is also
> obsolete: it proposes journeys to retire a 43-item allowlist that is now 22,
> a third of which retired through mechanical re-spelling rather than any new
> journey. Retained as the pre-reorg record; `docs/cli-surface-recommendation.md`
> §5 is the authoritative rename mapping.

ctxloom's acceptance suite is organised as user **journeys** — end-to-end
narratives a real person would actually perform, expressed as Gherkin. J1–J17
exist; J1–J12 and J14–J17 are wired. J13 (ensemble) is the last unwritten
journey — the `features-draft/` directory it once shared with J14 and J15 is
gone, both having been wired. `tests/acceptance/completeness_test.go`
enumerates every CLI leaf and MCP tool and holds an **exact-set** allowlist of
what is still uncovered: it fails in *both* directions, so the allowlist is a
live measurement rather than a cap.

This document proposes the journeys that would retire that allowlist. It is
prose on purpose. Nothing here is a `.feature` file; each entry is a narrative a
human can review and argue with before anyone writes a step definition.

Everything below was re-derived against the live binary at commit `d4c7da2c`,
not copied from a test log. Where a claim was checkable by running the command,
it was run.

---

## 1. What is actually uncovered

Running the completeness gate at `d4c7da2c` (it **passes** — the allowlist is
accurate) yields **36 CLI leaves** and **7 MCP tools** — 43 items total.

**Signing and trust (9 leaves).** `bundle sign`, `sign`, `signer list`,
`trust accept`, `trust reject`, `trust signer add`, `trust signer list`,
`trust signer remove`, `trust signer show`.

**MCP server management (6).** `mcp register`, `mcp unregister`,
`mcp server add`, `mcp server list`, `mcp server remove`, `mcp server show`.

**Engine matrix (6).** `manage install --engine {codex,kiro,antigravity}` and
`manage config init --engine {codex,kiro,antigravity}`.

**Session hooks (4).** `hook hud`, `hook inject-context`, `hook session-bind`,
`hook stamp-plan`.

**ACP (3).** `acp client`, `acp entries`, `acp server`.

**Config (3).** `config get`, `config init`, `config show`.

**Sessions (2).** `session query`, `session watch`.

**Singletons (3).** `bundle move`, `container tooling`, `init prompt`.

**MCP tools (7).** `evaluate_triggers`, `compact_session`,
`get_previous_session`, `list_sessions` on the standalone `mcp serve` surface;
`roster`, `agent_report`, `agent_fetch_artifact` on the runner-terminated
surface only.

### The shape of the gap, stated honestly

A large fraction of these are not *behaviours* nobody tested — they are **new
spellings of behaviours that are tested under an old name**. The noun-verb CLI
reorg (`2710ff0`) added canonical leaves alongside retained deprecated aliases,
and the corpus still drives the old spellings:

| canonical leaf (uncovered) | deprecated alias (covered by) |
|---|---|
| `trust accept <ref>` | `ctxloom trust <ref>` — `trust_cli.feature`, `trust_surface.feature`, J3 |
| `trust reject <ref>` | `ctxloom blacklist <ref>` — `trust_cli.feature` |
| `trust signer add` | `ctxloom signer add` — J3 Background |
| `trust signer remove` | `ctxloom signer remove` — J3 scenario 6 |
| `trust signer show` | `ctxloom signer show` — J7 |
| `mcp server add/list/remove/show` | `ctxloom mcp add/list/remove/show` |
| `mcp register` / `mcp unregister` | `manage mcp register` / `manage mcp unregister` |
| `config get` / `config show` | `manage config get` / `manage config show` — `config.feature` |
| `config init` | `manage config init` — `manage.feature` |
| `container tooling` | `ctxloom tooling` — `agent.feature` |
| `acp entries` | `ctxloom acp agents` — `agent.feature:52` |

That table is the single most useful thing in this document, because it changes
what the work *is*. For those leaves the cheap fix is to re-spell the existing
scenario to the canonical form — the deprecated twin runs the same code. Doing
that alone would retire roughly 15 of the 36.

**Do not just do that.** Re-spelling buys the gate's green light and buys almost
nothing else, because several of the scenarios being re-spelled assert on the
exit code and a success message rather than on what was written. `config.feature`'s
"Show the full configuration" asserts `the output matches "."` — a regex that
matches any single character. That scenario cannot fail for the reason it
exists. Re-spelling it to `ctxloom config show` propagates a vacuous assertion
onto a canonical leaf and marks the surface covered.

The genuinely-untested behaviours, where no alias covers anything, are:

- **`bundle sign` / `sign`** — no scenario has ever produced a signature through
  the CLI. Every signed fixture in the suite is signed *in Go* by a
  `testenv.TestSigner` (`tests/integration/testenv/signing_acceptance.go`),
  bypassing key discovery, ssh-agent, and the `.sig` writer entirely.
- **`signer list` / `trust signer list`** — the allowed_signers store has never
  been *read back* through the CLI in any spelling.
- **`bundle move`** — the signature-preserving relocation path.
- **The four `hook` callbacks** — they run on every session and no scenario
  drives their stdin/argv payload.
- **`session query`**, **`session watch`**, the three `acp` leaves, and
  `init prompt`.
- **The engine matrix** — codex/kiro/antigravity install paths.

---

## 2. Drift report

The 43-item list is **exactly correct**. No leaf was added, removed, renamed, or
since covered. The gate passes at `d4c7da2c` with the allowlist as written.

Four things are worth recording anyway.

**(a) `opencode` is a supported engine the engine matrix does not know about.**
`engineMatrixLeaves` lists four engines — claude-code, codex, kiro, antigravity.
The binary accepts and fully supports a fifth: `ctxloom manage install --engine
opencode` succeeds and writes `opencode.json`, `.opencode/ctxloom-context.md`,
and `.opencode/command/*`. `manage install` even reports `Hooks applied for:
[antigravity claude-code codex kiro opencode]`. So `manage install --engine
opencode` and `manage config init --engine opencode` are two *further*
uncovered engine variants that the gate structurally cannot flag — the same
blind spot `requiredHiddenLeaves` exists to close, one level up. This is drift
in the gate, not in the allowlist, and it means the real engine-matrix gap is
8 variants, not 6.

**(b) `--engine` does not scope what gets written, and an unknown engine is
accepted silently.** Verified by running it in a scratch repo: `manage install
--engine codex` writes claude-code's `.claude/settings.json`, antigravity's
`.agents/`, kiro's `.kiro/`, opencode's `.opencode/` and codex's
`.codex/config.toml` — all of them. `--engine` only selects the value recorded
at `agents.default.engine` in `config.yaml`. And `--engine bogus` exits **0**,
prints `Initialized ctxloom directory`, and writes `engine: bogus` into
`config.yaml`, producing a file that fails ctxloom's own JSON-schema validation
on the very next command (`value must be "claude-code"`). Exit 0, a success
message, and a corrupt payload — the house failure mode exactly. J20 below
carries a scenario for it, which will be RED until the validation lands.

**(c) RESOLVED — the `J14` draft was written against a command group that no
longer existed.** It narrated `ctxloom memory compact/list/show`; that surface
is now `session distill` / `session list` / `session show`. The draft was
retired rather than retargeted, and `j14_session_distill.feature` was written
against the live surface instead.

**(d) Five `excludedLeaves` entries are dormant.** `ctxloom llm serve`,
`ctxloom completion {zsh,fish,powershell}`, `ctxloom help`, and
`ctxloom profile push` all exist in the binary but never appear in the gate's
"excluded:" log, because they are `Hidden` rather than merely `Deprecated` and
so never enter `leafCommands`' walk. Their stated exclusion reasons are
therefore unverified — dead entries carrying live-looking justifications. Not
harmful, but they should be deleted or the leaves un-hidden, so the map means
what it says.

---

## 3. Proposed journeys

Six new journeys, J18–J23 (J17 is the highest existing number; verified across
`docs/`, `tests/`, and `scripts/`), plus four folds into existing coverage.

### Coverage map

| Journey | Leaves covered |
|---|---|
| **J18** — A signature somebody can check | `bundle sign`, `sign`, `trust signer add`, `trust signer list`, `signer list`, `trust signer show`, `trust signer remove`, `trust accept`, `trust reject`, `bundle move` |
| **J19** — One MCP server, every engineer's assistant | `mcp server add`, `mcp server list`, `mcp server show`, `mcp server remove`, `mcp register`, `mcp unregister` |
| **J20** — Joining a team that does not use claude-code | `manage install --engine {codex,kiro,antigravity}`, `manage config init --engine {codex,kiro,antigravity}`, `config init` |
| **J21** — The four callbacks every session already depends on | `hook inject-context`, `hook session-bind`, `hook stamp-plan`, `hook hud` |
| **J22** — Driving ctxloom from an editor | `acp entries`, `acp server`, `acp client` |
| **J23** — Finding the session where you already solved this | `session query`, `session watch` |
| fold → `config.feature` | `config get`, `config show` |
| fold → J15 draft (container) | `container tooling` |
| fold → J1 (`j1_setup`) | `init prompt` |
| fold → J6 / J17 / J11 / J23 | the 7 MCP tools (see §4) |

All 36 CLI leaves and all 7 MCP tools are assigned. Nothing is declined.

---

### J18 — A signature somebody can check

**Actor and goal.** Trent runs platform engineering at a company that has
decided its coding standards will be *enforced*, not merely published. Alice is
an engineer on a different machine, in a different repository, who will receive
those standards. Trent's goal is that what reaches Alice's assistant provably
came from him and was not altered on the way. Alice's goal is the mirror image:
she wants to accept exactly one publisher's authority and retain the right to
override it item by item.

**Why this is the highest-value gap.** Signed, verifiable context is the thing
ctxloom has that its competitors do not. J3 already proves the *consumption*
half beautifully — tamper detection, executable gating, retraction, key
revocation — but it proves it against fixtures signed **in Go**, by a
`TestSigner`, with the trust root sometimes written directly to disk. The
production path a real publisher takes has never executed in an acceptance run.
`ctxloom bundle sign` has never produced a byte in this suite. If key discovery
regressed, if the `.sig` sibling were written empty, if `--all` silently signed
nothing, every existing trust scenario would still pass.

**Narrative arc.**

Trent has an ordinary developer setup: an ed25519 key in his ssh-agent, and
`git config user.signingkey` already pointing at it because he signs his
commits. He does not configure ctxloom to sign — he simply runs `ctxloom bundle
sign secure-coding` and it works, which is the design claim ("key discovery is
zero-config") and the first thing to assert. A detached `secure-coding.yaml.sig`
appears beside the bundle. Trent's private key never leaves ssh-agent; ctxloom
reads, generates, and stores nothing.

He then signs the rest of his published bundles with `ctxloom bundle sign
--all`, and the journey counts the signatures produced against the bundles the
project actually publishes — because "signed everything" that signs nothing and
exits 0 is this codebase's characteristic bug, and `--all` over an empty
publish set is precisely where it would live.

Trent distributes his trust root the way a team does: `ctxloom trust signer add
context@acme.com --key acme-publish.pub --project`, writing the **committable**
`.ctxloom/allowed_signers` that every clone inherits — as opposed to the default
user store at `~/.ctxloom/allowed_signers` that follows one person across
projects. He confirms what he just wrote with `ctxloom trust signer list` and
`ctxloom trust signer show context@acme.com`, and the entry names his key's
fingerprint and the `publish` namespace it was granted.

Alice clones. She never runs `signer add` — she inherits the project store, and
her own `ctxloom trust signer list` shows *both* stores' contents distinguished
by scope, plus ctxloom's embedded release key. She references Trent's bundle;
its guidance reaches her assistant because a key she trusts signed it. That much
J3 already proves; here it holds for a signature the CLI actually made.

Then Alice exercises her own authority, in the canonical spelling. There is one
fragment in Trent's bundle she wants pinned to its current wording, so
`ctxloom trust accept secure-coding#fragments/tdd` — and the acceptance binds to
the item's *current content hashes*, so when Trent edits that fragment it
returns to pending rather than silently riding his trust. There is one hook she
will not run at all, so `ctxloom trust reject secure-coding#hooks/PreToolUse/0`
— and rejection beats a trusted publisher's signature, permanently and by ref,
not merely for these bytes.

Trent reorganises: `ctxloom bundle move secure-coding --to shared-standards`
relocates the bundle and its `.sig` **verbatim**, never re-parsing or
re-serialising, so the signature that covered the old bytes still covers the new
location's bytes. This is the scenario that catches a re-signing "helpful fix":
if the mover ever round-trips the YAML, the signature dies and the only visible
symptom is content quietly going unsigned at the destination.

Finally the withdrawal. Trent's key is compromised. Alice runs
`ctxloom trust signer remove context@acme.com --project`. The command means "I
will review this myself from now on", not "deny" — so the content does not
error or vanish; it falls back to the path any unsigned content takes, held for
review. `ctxloom trust signer list` no longer names him, and — the assertion
that makes the removal real rather than cosmetic — the on-disk
`.ctxloom/allowed_signers` no longer contains his key line.

**Leaves exercised.** `ctxloom bundle sign` (and its deprecated twin `ctxloom
sign`, in one scenario, asserting the deprecation notice goes to stderr while
the `.sig` still lands), `ctxloom trust signer add`, `ctxloom trust signer
list` (and `ctxloom signer list`), `ctxloom trust signer show`, `ctxloom trust
signer remove`, `ctxloom trust accept`, `ctxloom trust reject`, `ctxloom bundle
move`.

**What must be asserted — payload, not exit status.**

1. `secure-coding.yaml.sig` exists **and is non-empty** and parses as a
   signature envelope over the bundle file's exact bytes. Verify it
   independently — with the same verifier `internal/signing` exposes, against
   the bundle bytes read fresh off disk — not by trusting ctxloom's own success
   line.
2. `bundle sign --all` produces one signature per published bundle; assert the
   **count** against the publish set. A zero-signature `--all` that exits 0 must
   fail this journey.
3. `bundle sign my-tools#fragments/go-testing` (an item ref) signs the
   *containing bundle file*, and the output says so. Assert the `.sig` landed
   next to `my-tools.yaml`, and that no per-fragment signature file was created.
4. `trust signer add --project` writes `.ctxloom/allowed_signers`, not
   `~/.ctxloom/allowed_signers`. Assert both paths — the one that gained a line
   and the one that did not. A trust root written to the wrong store is invisible
   until a teammate's clone silently fails to trust anything.
5. The written allowed_signers line contains the key's real fingerprint and the
   `publish` namespace. Not "the file exists".
6. `trust signer list` output names the principal, the fingerprint, the
   namespace, and the store it came from; `trust signer show` on a principal
   with entries in both stores shows both.
7. `trust accept` writes a record bound to the item's current content-hash pair;
   editing the fragment returns it to pending. Assert the *delivered payload* to
   the assistant, not just the review state.
8. `trust reject` writes both the ref-level rejection and the content-hash
   denylist entries, and the item is absent from the delivered surface even
   though the publisher signature still verifies.
9. `bundle move`: destination bundle bytes are **byte-identical** to the source
   (hash both), the `.sig` moved with it, the moved signature still verifies,
   and the source is gone. Also the failure path: when the signature cannot be
   carried, the move fails and the **source is untouched** — assert the source
   still exists rather than asserting an error string.
10. `trust signer remove` removes the key line from the store file itself, and
    the previously-flowing content is now held for review rather than erroring.

**Fixture and setup.** Hermetic, but it needs **one new harness capability that
does not exist today**: an ssh-agent the acceptance world can sign against.
`ctxloom bundle sign` resolves its key through `internal/signing/agentkey`,
which needs `SSH_AUTH_SOCK`; the suite deliberately avoids that today —
`steps_skill.go:19-27` says so outright, and `steps_j3.go:473-478` goes further
and *blanks* `SSH_AUTH_SOCK` so an ambient developer agent cannot contaminate a
run.

The fix is small and fully hermetic, and it does not need the `ssh-agent`
binary: stand up `golang.org/x/crypto/ssh/agent.NewKeyring` in-process, add the
existing `testenv.TestSigner`'s key to it, serve it on a unix socket in the
world's temp dir, and point the world's `SSH_AUTH_SOCK` at it.
`internal/signing/agent_signer_test.go` already does exactly this over a
`net.Pipe` — the only new work is a real socket so a subprocess can dial it.
Call it `testenv.StartSSHAgent(signer) (sockPath string, stop func())`.

With that in place, everything in J18 is hermetic. The three key-discovery
branches can each be driven deliberately: `git config user.signingkey` (write it
into the fixture repo's git config), sole-agent-identity (add exactly one key),
and `--key` by fingerprint / path / agent comment substring.

**What blocks wiring today.** Only the ssh-agent fixture above. Nothing needs
`@live`, no network, no TTY, no container. J18 is the highest-value journey on
this list and also one of the cheapest to unblock — one helper in
`tests/integration/testenv/`.

**One thing to decide before wiring.** J18 and J3 will overlap at the edges
(both have a publisher, a trusted key, and content reaching Alice). Keep the
seam explicit: **J3 owns the adversary** — tamper, retraction, revocation,
rejection beating trust. **J18 owns the production of the artifacts J3 assumes**
— the signature, the trust root, the stores on disk. J18 should state that
scope in its feature preamble the way J3 states its "provenance not secrecy"
scope, and should not re-prove tamper detection.

---

### J19 — One MCP server, every engineer's assistant

**Actor and goal.** Priya maintains her team's developer platform. Staging has a
read-only Postgres replica, and she wants every engineer's assistant to be able
to query it without twelve people each hand-editing `.mcp.json`,
`.codex/config.toml`, `.kiro/settings/mcp.json` and `.agents/mcp_config.json`.
She wants to declare the server once, in ctxloom, and have it appear in whatever
file each engineer's engine actually reads.

**Narrative arc.** Priya adds the server: `ctxloom mcp server add postgres
--command psql-mcp --args "--dsn,$STAGING_DSN"`. ctxloom records it and tells her
plainly that nothing has reached any engine yet — she must run `ctxloom manage
hooks install` (or start a session) to apply it. That two-step is the honest
part of the design and the first thing worth pinning: an "added" server that
never lands anywhere is exactly the silent success this codebase is prone to.

She confirms what she declared with `ctxloom mcp server list` and inspects one
with `ctxloom mcp server show postgres --format json`. She applies, and the
server appears in each engine's own native shape.

She then scopes a second server to one backend only —
`ctxloom mcp server add claude-only --command ./srv --backend claude-code` —
and after applying, it is present in claude-code's `.mcp.json` and **absent**
from the other three engines' files. A backend filter that is recorded but not
honoured is invisible until someone's codex session mysteriously has a tool it
should not.

Priya decides ctxloom's own MCP server should not be auto-registered into
engineers' settings — the team drives ctxloom from the CLI, and an extra tool
surface in every session is noise. `ctxloom mcp unregister` flips it off; after
re-applying, ctxloom's own entry is gone from the generated backend files while
`postgres` remains. `ctxloom mcp register` puts it back.

Finally she retires the server: `ctxloom mcp server remove postgres`, re-apply,
and it is gone from config **and** from every engine file it had reached. A
remove that drops the config entry but leaves the generated file stale is the
worst outcome here, because the credentialed tool keeps working.

**Leaves exercised.** `mcp server add`, `mcp server list`, `mcp server show`,
`mcp server remove`, `mcp register`, `mcp unregister`.

**What must be asserted.**

- After `add`: `.ctxloom/config.yaml` has `mcp.servers.postgres.command` and the
  `args` **list**, parsed as YAML. Verified live: this is where it lands.
- After applying: each engine's file **parsed in its own format** — `.mcp.json`
  as JSON, `.codex/config.toml` as TOML, `.kiro/settings/mcp.json` as JSON,
  `.agents/mcp_config.json` as JSON — with the command *and* the args asserted,
  following J5's rule that a row must parse the file rather than substring-match
  a key name. (`manage.feature`'s existing bare `the file ".mcp.json" exists`
  is exactly the assertion this journey must not repeat.)
- `mcp server list` names the server, its command, its args and its scope;
  `mcp server show --format json` round-trips the same values.
- `--backend claude-code` present in one file, absent in three.
- `unregister` → `mcp.auto_register_ctxloom: false` in config **and** ctxloom's
  entry absent from the regenerated engine files. `register` → both restored.
- `remove` → absent from config **and** from the regenerated engine files.
- `mcp server show` on a name that does not exist fails, with a nonzero exit.

**Fixture and setup.** Fully hermetic. No engine binary is required —
`manage install` and `manage hooks install` write every engine's surfaces
without any engine present (verified). The existing "an initialized ctxloom
project" fixture plus `manage hooks install` is enough.

**Blockers.** None.

---

### J20 — Joining a team that does not use claude-code

**Actor and goal.** Sam joins a team standardised on codex. Later the same
journey follows Ravi (kiro) and Mei (antigravity). Each wants one command that
leaves their project wired for *their* engine. The product claim is that ctxloom
is engine-neutral; today only the claude-code path is proven, so the other
engines' install paths can rot behind a single green scenario.

**Narrative arc.** Sam runs `ctxloom manage install --engine codex` in a fresh
repository. ctxloom scaffolds `.ctxloom/`, records codex as his default agent's
engine, and writes the harness. He checks `ctxloom manage status` and it names
codex. Ravi and Mei do the same for kiro and antigravity and get `.kiro/` and
`.agents/` respectively. Separately, someone who only wants configuration —
no hooks, no scaffold — runs `ctxloom config init --engine codex` in a bare
directory and gets a `config.yaml` and a `remotes.yaml`, and running it again
refuses to clobber the existing `config.yaml`.

And then the case that matters most: someone typos the engine name.

**Leaves exercised.** `manage install --engine codex|kiro|antigravity`,
`manage config init --engine codex|kiro|antigravity`, and `config init` (the
canonical spelling of the last). Wiring note: the completeness gate's
`engineMatrixLeaves` requires a literal `I run "ctxloom manage config init
--engine codex"` for the matrix rows, so those scenarios must use the
**deprecated** spelling; add one further scenario using `ctxloom config init
--engine codex` to credit the canonical leaf. Both spellings, deliberately, and
the feature should say why in a comment so a later reader does not "tidy" it.

Consider also adding `opencode` rows — see the drift report. They are free once
the outline exists, and `engineMatrixLeaves` should gain the fifth engine.

**What must be asserted.**

- `.ctxloom/config.yaml` parses and `agents.default.engine` is the requested
  engine. Verified live: this is the *only* thing `--engine` actually changes.
- The requested engine's own surfaces exist and parse: `.codex/config.toml` as
  TOML with a `[hooks]` section; `.kiro/` steering + `settings/mcp.json`;
  `.agents/AGENTS.md` with managed markers + `mcp_config.json`. Reuse J5's
  per-engine table rather than inventing a second one.
- **The unknown-engine scenario.** `ctxloom manage install --engine bogus` must
  fail, and must write nothing. Today it exits **0**, prints `Initialized
  ctxloom directory`, and writes `engine: bogus` into a `config.yaml` that fails
  ctxloom's own schema validation on the next command. Same for `config init
  --engine bogus`. This scenario will be RED until the validation is added; that
  redness is the point, and it should be written as the desired behaviour rather
  than as documentation of the current one.
- `config init` twice: the second run refuses to overwrite `config.yaml`. Its
  help documents that `remotes.yaml` **is** rewritten with defaults — assert
  that too, because it is a documented way to lose a customised remotes file and
  a journey is where a user would find out.

**Fixture and setup.** Fully hermetic — **verified by running all three engines'
installs in a scratch git repo at this commit**. No engine binary, no network,
no credentials. `manage install` emits warnings about uninstallable default
remote profiles; those are warnings and the exit is 0.

**Blockers.** None. The allowlist comment says "codex/kiro/antigravity need
their own fixtures"; that is **stale**. They need no fixtures at all.

---

### J21 — The four callbacks every session already depends on

**Actor and goal.** Alice installed ctxloom weeks ago and has not thought about
it since. Every session she starts silently runs four ctxloom callbacks that the
harness wired into `.claude/settings.json`: one injects her assembled context at
SessionStart, one binds the backend session to its harp name, one stamps that
harp into any plan file she edits, and one renders her statusline. She never
invokes them. She would never notice if one of them started returning nothing —
her assistant would simply be a bit worse, and she would blame the model.

**Why this matters more than its size suggests.** These are registered
`Hidden: true`, so `leafCommands`' walk cannot see them at all; the gate lists
them explicitly in `requiredHiddenLeaves` precisely because it would otherwise
be structurally blind to them. They are the single best fit for this codebase's
characteristic bug: `hook inject-context <hash>` returning
`{"hookSpecificOutput":{"additionalContext":""}}` and exiting 0 gives every
session in the company a context-free assistant with no error anywhere.

**Narrative arc.** Alice installs the harness, then the journey does something
no existing scenario does: it **reads the hook command lines back out of the
generated `.claude/settings.json`** and runs those exact command lines, with the
exact payload the engine would send. That is the arc — not "run four commands",
but "run what is actually wired, the way it is actually invoked". If the
installer writes a command line the binary cannot honour, this journey is the
only thing that would catch it.

`hook inject-context <hash>` is handed the hash of a real assembled-context file
and must return JSON whose `additionalContext` contains the fragment marker
Alice's profile carries. Handed a hash with no file behind it, it must fail
loudly rather than return an empty string, because an empty injection is
indistinguishable from a working one at the engine. Its `--part`/`--of`
chunking must return the right ordered slice, and the concatenation of all parts
must equal the single-shot body.

`hook session-bind` reads the backend's session identity and binds it to the
active harp; assert the binding is readable afterwards through
`ctxloom session list`, not that the command exited 0.

`hook stamp-plan` is run against a plan file, and the assertion is on the file's
frontmatter afterwards — it gained the harp name, and the rest of the file is
byte-identical. Re-running it does not stamp twice.

`hook hud` is fed the statusline JSON shape on stdin and must render a line
naming the active profile and the model. Fed malformed JSON, it must not print a
plausible-looking statusline built from zero values.

**Leaves exercised.** `hook inject-context`, `hook session-bind`,
`hook stamp-plan`, `hook hud`.

**What must be asserted.** In every case, the payload: the injected context
string contains the profile's fragment marker; the session index gained the
binding; the plan file's frontmatter gained the harp and nothing else changed;
the HUD line contains the profile name. Plus, for each, the empty/missing-input
case fails loudly.

**Fixture and setup.** Hermetic. Everything needed exists: `manage hooks
install` generates the settings file, the assembled-context cache is produced by
an ordinary run, the session index is seedable (`a recorded session`), and a
plan file is just a file. Note the gate requires a literal `I run "ctxloom hook
..."` for these (`ranAsCommand`), so they must be invoked through the standard
run step, not only through a Go helper.

**Blockers.** None. One design note: reading the command line out of
`settings.json` and executing it is a new step shape (`I run the hook command
the harness installed for <event>`), and it is what makes this journey worth
more than four unit tests.

---

### J22 — Driving ctxloom from an editor

**Actor and goal.** Dana works in Zed and does not want a second terminal.
She wants her editor's agent panel to talk to ctxloom directly — assembled
context, her profiles, her configured engine — and she wants to pick which of
her agents the panel drives. Separately, before wiring anything, she wants to
confirm an ACP-speaking agent she has configured actually answers.

**Narrative arc.** Dana runs `ctxloom acp entries` and gets one advertisable
entry for plain ctxloom plus one per agent binding, along with a ready-to-paste
Zed `agent_servers` block. She pastes it. Her editor launches
`ctxloom acp server`, initialises, opens a session, and sends a turn; ctxloom
assembles her context, rides it on the first turn as a lead block, records the
session under a harp name, and streams the reply back. Her profile sets appear
as ACP session modes; switching mode re-assembles the lead context for the next
turn while the engine stays pinned at launch. Later she reopens the harp and its
history replays.

In the other direction, Dana has a `kiro-cli acp` entry configured under `llm:`
and wants a smoke test before binding an agent to it:
`ctxloom acp client --llm kiro "say hello"` drives one headless turn and prints
the answer.

**Leaves exercised.** `acp entries`, `acp server`, `acp client`.

**What must be asserted.**

- `acp entries` lists the plain entry plus exactly one per configured agent
  binding — assert the **count and the names** against the configured agents,
  and that the emitted Zed block is valid JSON with the right `command`/`args`
  for each entry. Since ACP has no in-protocol agent selection, a missing entry
  means an agent is simply unreachable from the editor, invisibly.
- `acp server`: a real ACP client in the test drives initialise → session/new →
  session/prompt against the **mock engine**, and the assertion is that the
  assembled context reached the engine (the mock record's `=== Prompt ===`
  block carries the profile's fragment marker — the same technique J1 and J9
  already use) and that the reply streamed back. Then `session/set_mode` changes
  the assembled lead context for the next turn while the engine binding is
  unchanged; then `session/load` on the recorded harp replays history.
- `acp client`: one turn, one answer, and — the failure this journey exists to
  catch — a misconfigured or non-answering target must fail loudly rather than
  return an empty completion with exit 0.

**Fixture and setup.** Mixed.

- `acp entries` is **hermetic today** and near-free: `agent.feature:52` already
  drives the deprecated `ctxloom acp agents`. Re-spelling that scenario covers
  the leaf immediately; the count/JSON assertions above are the real work.
- `acp server` is hermetic **if** the harness can act as an ACP client over the
  subprocess's stdio. ctxloom already has an ACP client implementation
  (`internal/acp`), and `steps_coordination_contract.go` already drives the
  runner-terminated surface, so the machinery is close by. This is the one piece
  of new harness work in J22.
- `acp client` needs an ACP-speaking agent on the other end. Hermetically, that
  means a **stub ACP agent** — a small scripted responder the fixture spawns —
  configured as an `llm:` entry with `type: acp`. Against a real
  `claude-code-acp` / `kiro-cli acp` it is `@live`. Recommend the stub for the
  hermetic path and one `@live` scenario against a real adapter.

**Blockers.** An in-harness ACP client driver (for `acp server`) and a stub ACP
agent (for `acp client`). Both are ordinary test code, but they are real work
and they are why J22 should be scheduled after J18–J21. Note also that both
`acp server` and `acp client` are marked **Experimental** in their own help
text — worth stating in the feature preamble so the journey's guarantees are not
read as stability promises.

---

### J23 — Finding the session where you already solved this

**Actor and goal.** Marcus knows he debugged this exact TLS handshake failure
about three weeks ago, in some session, in some repository. He wants to find it
without grepping his shell history. Separately, when he has a delegated agent
running, he wants to watch its turns go by rather than wait for a summary.

**Narrative arc.** Marcus runs `ctxloom session query tls handshake`. ctxloom
searches session metadata — harp name, summary, start and end — and, for
sessions already distilled, the essence body itself, requiring **every** word to
match somewhere (an AND, not an OR). It returns the same lightweight rows
`session list` renders; the essence body is read off disk, scanned, and
discarded, never handed to a model. He narrows with a third word and the match
set shrinks. `--all` widens beyond the current project. `--full` prints the
matched essences.

Then `ctxloom session watch <harp>` on a running delegated child: recorded
entries replay first as scrollback, live events follow, and each completed
response is marked by a boundary. With `--format json` it is NDJSON, one event
per line, each carrying exactly one of `entry` or `boundary`.

**Leaves exercised.** `session query`, `session watch`.

**What must be asserted.**

- `session query` returns the seeded session when a word matches its **essence
  body** and not merely its metadata — otherwise the essence-search half is
  untested and could be a no-op.
- The AND semantics: two words that each match different sessions return
  **neither**. This is the assertion that distinguishes a real AND from a
  substring-of-the-concatenation.
- Case-insensitivity; `--all` vs. the default cwd filter (assert a session from
  another project is excluded by default and included with `--all`); `--full`
  includes the essence body and the default output does not.
- No matches → a clear empty result, not exit 1 and not a silent zero-row table
  that looks like a crash.
- `session watch --source json`: at least one `entry` event with a `type` and
  `content`, and a `boundary` marking a completed response, with `fromIndex`/
  `toIndex` bracketing the entries actually emitted.
- `--source live` against a harp no orchestrator holds must **error**, per its
  own help — not fall back silently.

**Fixture and setup.** Split, and the split matters:

- **`session query` is wirable today with zero new fixture work.**
  `session.feature`'s existing `a recorded session "amber-swift-owl"` step
  already seeds an index entry *and* an essence (its sibling scenario asserts
  `Seeded essence` from `session show`). Seed two or three with distinct essence
  bodies and every assertion above is reachable. This is the cheapest item on
  the whole list.
- **`session watch` is blocked.** It is a long-lived watcher: `--source store`
  polls until interrupted, `--source live` ends only when the child's engine
  exits. There is no bounded hermetic exit today. The tractable path is
  `--source live` against a delegated child that terminates — J6 and J17 already
  spawn and stop children — so the watch ends naturally when the child does. If
  that proves fragile, `session watch` should move to `excludedLeaves` with the
  same reason `plan watch` already carries, rather than sitting on the
  uncovered list indefinitely.

**Recommendation.** Wire `session query` on its own; do not let it wait for
`session watch`.

---

## 4. Folds into existing coverage

These leaves do not deserve journeys of their own. A journey for a single
read-only command is a test plan with a story bolted on.

**`config get`, `config show` → `config.feature`.** These are the canonical
spellings of `manage config get` / `manage config show`, which that file already
drives. Re-spell the scenarios. **But fix the assertions while you are there**:
"Show the full configuration" currently asserts `the output matches "."`, which
any single character satisfies — it cannot fail. Replace it with assertions on
the actual sections and on a value the fixture set. Re-spelling a vacuous
assertion onto a canonical leaf is worse than leaving the leaf uncovered,
because it converts a known gap into a false green.

**`container tooling` → J15 (`j15_container.feature`, now wired).** J15
narrates the container command surface and notes that `agent.feature` covers the deprecated
`tooling` spelling. Add a scenario there in the canonical spelling, and make it
prove the **trust gate**, which is the interesting claim and is currently
unasserted anywhere: an unreviewed bundle's `tooling` declaration is withheld,
and a trusted bundle's is emitted verbatim. Verified live: with nothing trusted
the command prints `No trusted bundles declare container tooling` and exits 0 —
so the withheld case and the "genuinely nothing declared" case currently produce
**the same output**, which is exactly the distinction a scenario should force
apart.

**`init prompt` → J1 (`j1_setup.feature`).** It is a re-entry pointer onto the
same setup prompt body `ctxloom init` hands the engine at bootstrap and
`/ctxloom-init` loads in an ordinary session. J1 is the setup journey; this is
one scenario there. Assert the emitted body is non-empty and **identical** to
the body the other two paths deliver — a drifting triple is the failure worth
catching, and a one-command journey would never think to compare them.

### The 7 MCP tools

These are tool names, not CLI leaves, and the gate credits them only on a
literal `the agent calls tool "<name>"` step (`ranAsTool`). They should be
covered *inside* journeys where an agent has a reason to call them, not in a
tools-only feature — `mcp_tools.feature` already demonstrates the failure mode
of naming tools in prose without invoking them, which is why `ranAsTool` exists.

**`roster`, `agent_report`, `agent_fetch_artifact` → fold into J6 and J17.**
These exist **only** on the runner-terminated (`cli.NewDocMCPServer`) surface —
the one a real harness sees through `ctxloom run` / `ctxloom acp` — and are
invisible to the standalone `mcp serve` enumeration. They are a coordinator's
view of its own children: `roster` is "who is running", `agent_report` is a
child reporting back, `agent_fetch_artifact` is the coordinator collecting what
a child produced. J6 already spawns delegated children and audits their journaled
privilege grants; J17 already drives the two-way bus in both directions with
content asserted on each side. Add to J6: after spawning two children, the
coordinator calls `roster` and the result names both, with their run ids and
states; a child calls `agent_report` and the coordinator's next `roster` or
fetch reflects it; the coordinator calls `agent_fetch_artifact` and gets the
**bytes the child wrote**, not a path or a success flag.

*Blocker:* these must be invoked against the runner surface. J6/J17 already run
there, so the step machinery exists — but any new `mcp_tools.feature`-style
scenario would hit the standalone server and never see these tools at all. That
mismatch is the trap; state it in whichever feature ends up carrying them.

**`compact_session`, `get_previous_session`, `list_sessions` → retarget and wire
the J14 memory draft.** These are the session-memory tools, and J14 is the
journey about knowledge surviving a session boundary. **J14 must be retargeted
first**: it is written against `ctxloom memory compact/list/show` and cites
`internal/cli/memory.go`, a command group that no longer exists — the surface is
now `session distill` / `session list` / `session show`. Once retargeted, the
three tools are the MCP-side half of the same story: a fresh session calls
`list_sessions` to see what came before, `get_previous_session` to pull the
prior session's essence, and `compact_session` to distil the one it is in.
Assert the essence **body** reaches the caller, not that the call succeeded.
Note that `session distill` is currently in `excludedLeaves` as `@live`; the MCP
tools may be reachable hermetically against the mock backend even so — worth
checking, because if `compact_session` is hermetic then so is the CLI path and
the exclusion is stale.

**`evaluate_triggers` → fold into J11 (`j11_taskloom_tags`).** Trigger
evaluation is the deferred-task revive mechanism: a task parked with a revive
trigger becomes live again when its condition fires. J11 is the taskloom
journey and already has the task store in play. Add a scenario where a task is
deferred with a trigger, the agent calls `evaluate_triggers`, and the tool
returns **that specific task** as fired — with a negative case in the same
scenario (a task whose trigger has not fired is not returned), because a tool
that returns everything and a tool that evaluates correctly look identical
against a single-task fixture.

---

## 5. Verdict on the 13 excluded leaves

The gate printed exactly 13 exclusions at `d4c7da2c`. Reasons re-checked against
current fixture capability:

| Leaf | Stated reason | Verdict |
|---|---|---|
| `manage config edit` | opens `$EDITOR`; TTY-only | **Holds.** |
| `config edit` | alias of the above | **Holds.** |
| `mcp serve` | the server itself; exercised by every `@mcp` scenario | **Holds** — and it is a genuine exclusion, not a gap. |
| `remote discover` | network discovery; no deterministic fixture | **Holds.** |
| `bundle mcp edit` | needs a bundle-embedded MCP fixture (niche) | **Weakening.** `trust_surface.feature` already builds bundles shipping an MCP server, so the fixture the reason calls missing now exists. The honest reason is "niche", not "no fixture" — reword it or write the scenario. |
| `remote update` | needs a second remote commit | **Stale.** `testenv.AdvanceSignedRemote` (used by J3 and `trust_surface`) exists precisely to add a second commit to a seeded remote. The stated blocker is gone; only the scenario is missing. Recommend moving to the uncovered list with a backfill task. |
| `remote upgrade` | needs an update cycle | **Stale, same reason.** `AdvanceSignedRemote` plus an installed bundle is an update cycle. |
| `bundle push` | needs a writable remote | **Holds** for a real forge — but the suite already seeds `file://` bare repos it can push to, so a hermetic push to a local bare remote is reachable. The reason should be narrowed to "PR creation against a real forge", which is what is genuinely out of scope. |
| `command push` | same | **Same verdict.** |
| `container build` | needs a container runtime + network pulls | **Holds.** The J15 draft's own analysis reaches the same conclusion and tags it `@container`. |
| `plan watch` | long-lived watcher, no hermetic exit | **Holds.** |
| `session distill` | `@live` + needs a real backend transcript | **Questionable.** `steps_recover_session.go` seeds a canonical `transcript.jsonl` directly and reaches the recover path with a **mock** backend, no real backend needed. If the distiller can be pointed at the mock as its compaction engine — which the J14 draft asserts it can, reading the prompt out of `CTXLOOM_MOCK_RECORD_FILE` — this exclusion is stale. Worth one experiment before accepting it. |
| `weave` | fans out across parallel LLM sessions + LLM synthesis; `@live`-only | **Holds** for the real thing; a mock-backend fan-out would prove the orchestration but not the synthesis, which is the part that needs a model. |

**Summary: 8 hold, 2 are stale (`remote update`, `remote upgrade`), 3 have
reasons that overstate the blocker (`bundle mcp edit`, `bundle push` /
`command push`, `session distill`).** None should be silently re-scoped; each
needs either a reworded reason or a backfill task.

---

## 6. Blockers, collected

Everything that stands between this document and wired journeys:

1. **No ssh-agent in the acceptance world.** Blocks `bundle sign` / `sign`, and
   therefore the production half of J18. Fix: `testenv.StartSSHAgent(signer)`
   serving `agent.NewKeyring` on a unix socket; `internal/signing/agent_signer_test.go`
   is the template. Small, hermetic, and it unblocks the highest-value journey.
   Note the interaction with `steps_j3.go:473-478`, which deliberately blanks
   `SSH_AUTH_SOCK`: J18 must set it, J3 must keep clearing it, and neither
   should quietly change the other's behaviour.
2. **No in-harness ACP client.** Blocks `acp server` (J22).
3. **No stub ACP agent.** Blocks the hermetic path for `acp client` (J22);
   without one it is `@live`.
4. **`session watch` has no bounded exit.** Blocks it in J23. Most promising
   route: `--source live` against a delegated child that terminates. If that
   fails, exclude it with `plan watch`'s reason.
5. **Runner-only MCP tools are unreachable from the standalone server.**
   `roster`, `agent_report`, `agent_fetch_artifact` must be driven through
   J6/J17's runner-terminated surface; a scenario written against `mcp serve`
   will silently never see them.
6. **The J14 draft is stale** and must be retargeted from `ctxloom memory` to
   `session distill`/`list`/`show` before it or its three MCP tools can be
   wired.
7. **`manage install --engine <unknown>` does not validate.** J20's fail-loud
   scenario will be RED until the product is fixed. That is a product bug found
   while writing this document, not a journey blocker — but the scenario should
   be written for the intended behaviour, not the current one.

Nothing else on the list needs a fixture that does not exist. In particular
J19 (MCP servers), J20 (engine matrix), J21 (hooks), the `session query` half of
J23, and all four folds are wirable against today's harness.
