# Breaking changes in 0.7.0

**Read this before upgrading from 0.6.x.** ctxloom breaks rather than shims;
breaking *silently* is what it does not do — hence this page.

One change in this release fails **silently** if you do nothing, so it is
first. A second, §12, is silent too: it changes where claude gets its
context.

---

## 1. Published bundle content now requires ctxloom 0.7 — and 0.6 does not say so

Bundle references moved to a canonical URI grammar:

    was   https://github.com/owner/repo@bundles/name#fragments/x
    now   ctxloom+git://github.com/owner/repo//bundles/name#fragments/x

The source class moved into the scheme, and `//` took over the repository/bundle
split that `@` used to carry. Both published bundle repositories now address
their content this way.

**No released ctxloom before 0.7 can parse it.** `internal/shared/refuri` does not
exist at v0.6.4 and nothing there dispatches on the scheme.

**What that looks like if you upgrade the content but not the client.** Measured
with v0.7.0-19f6ada against the `ctxloom-personal` go-development closure:

| content | bundles resolved |
|---|---|
| pre-migration | 9 |
| migrated | 2 |

Seven bundles — `code-quality`, `conduct`, `containers`, `core-practices`,
`developer-mindset`, `go-ai-practices`, `just` — disappear with an **identical
exit code** and an identical `Pulled N items` line. Nothing reports a problem;
your assembled context is simply smaller.

Pins never move on their own, so this begins at `deps upgrade`, never at
install. **Upgrade the client first.**

Nothing lets a bundle declare a minimum client version today, which is why this
is silent rather than refused.

## 2. A config outside the format generations this ctxloom reads is refused

Each `config.yaml` layer declares its format generation as `schema_version`.
A layer declaring a generation below the oldest this ctxloom migrates — or
declaring none — fails with a migration finding naming the file, the version
it declares, and `ctxloom init` as the remedy. A layer declaring a generation
NEWER than this ctxloom knows fails the same way, naming both numbers, with
upgrading ctxloom as the remedy. `version` is not a spelling of
`schema_version`: a layer that declares its generation only that way declares
none.

The same holds for every file ctxloom, ltk and taskloom version with
`schema_version`: one that declares none is refused. Stamp existing stores
with a release that still migrates them (`--write-upgrades`) before upgrading.

What a load changes in memory (an `<alias>/…` ref resolved through the
registry) leaves the file alone; `ctxloom run` no longer offers to rewrite
it. Pass `--write-upgrades` to any command to persist it (the previous file
is kept beside it as `<file>.bak`).

A key the config schema does not describe fails every command that reads the
config, naming the key and the keys its section does know. So does a value the
schema refuses — the wrong type, outside its allowed values, or an
`llm.configs` entry matching no backend's shape — named by file, dotted key
path and the type the schema expected (or the rule the value broke). Until
now such a value drew only a warning and the command ran on it. Under
`--degraded` (or `CTXLOOM_DEGRADED=1`) the config loads best-effort: each is
warned about, the unknown key is ignored and the refused value is loaded as
written.

## 3. The config-level `hooks:` block is gone

It was a second implementation of something profiles already do, with exactly
one consumer and no writer anywhere. A config carrying `hooks:` now reports it
as an unknown key (see section 2).

**Move each hook to a profile** — a profile of your project bundle
(`.ctxloom/content/bundles/v2/project/profiles/<name>.yaml`), under the same
`hooks:` key, spelled identically.

## 4. A session's readable outputs live in its output dir

A session's plans (and published reports) live in the session's output dir —
`<Documents>/ctxloom/<project>/<harp>/` by default, or under the `output_dir`
config key — recorded in the session's `session.yaml` when it is created. The
machine state (transcripts, native engine history, spool) stays under
`~/.ctxloom/sessions/<harp>/`. Nothing ctxloom runs — no destroyer, no sweep,
no `ctxloom clean` — ever deletes the output dir. The project-rooted
`<project>/.ctxloom/sessions/<sessionID>.md` store is no longer written or read.
Existing files there are not migrated and are safe to delete; `ctxloom session
adopt` re-indexes an outside session if you want its history back.

Session essences (`essence.md`, `segments/<sessionID>.md`) and the captured
next step (`next-step.md`) are no longer written at all — session compaction
is removed (§15). Any you already have stay where they are, as notes.

## 5. A hook declared once runs once, however many profiles reach it

The same hook no longer runs twice in one event. Previously, a hook declared by
a profile that two of your profiles both inherit from was applied once per
inheritance path — a shared ancestor reached through two parents ran its hook
twice per event. Two selected profiles each declaring the same hook did the
same.

Hooks are compared on their whole executable content — type, command, prompt and
matcher — **scoped to the event**. So the same command registered on both
`session_start` and `session_end` is still two hooks, and the same command with
a different `matcher` is still two hooks. Only an exact repeat within one event
collapses.

**If you were relying on a hook running twice, it will now run once.** That was
not expressible on purpose and there is no way to ask for it; declare two hooks
that differ in some way if you need two runs.

## 6. `profiles.defaults` was replaced by an always-bound default agent

Set `default_agent: <name>` and `agents.<name>.profiles: [...]`. A config still
carrying `profiles.defaults` is told so by name.

## 7. A session started before 0.7 will not have its transcript converted

Every session already on your disk was started before ctxloom recorded which
engine version was running. Reading an engine's own transcript store back is
now scoped to that version, so those sessions **refuse to convert** rather than
being read by a parser nobody validated against them. That refusal is the
designed outcome, not a fault — this section is here so you can recognise it
and know there is nothing to fix.

**What you will see.** Nothing fails and nothing is deleted. You get a warning,
at the end of an interactive `ctxloom run` and when `ctxloom session adopt
--yes` rebuilds a session's transcript, naming the session and the reason:

    ctxloom: warning: vendor transcript import: fluent-amber-heron records no
    claude-code version, so ctxloom cannot tell which transcript format wrote
    it (sessions started before ctxloom recorded engine versions, or whose
    engine could not be asked, carry none) — refusing to read rather than
    guessing at a format

A session whose version *is* recorded but falls outside every range this build
carries a reader for gets the other refusal, which prints the gap:

    no codex transcript reader is validated for version "0.150.0" (ctxloom
    carries: 0.144.0 – 0.145.0 (exclusive)) — refusing to read rather than
    parsing a format it has never been checked against

**What it costs you.** Only ctxloom's own canonical transcript for that
session, and therefore anything downstream of it — its title in `session
list`, a `ctxloom run --session` that replays the conversation. The engine's own transcript is
untouched on disk in the engine's own store, and the engine's own resume still
works. Nothing is lost; it is not re-stated in ctxloom's format.

**Why it refuses instead of trying.** A reader that guesses does not fail
loudly — it produces a transcript that looks completely fine, is wrong, and
goes straight into a model's context. That is the failure this project has
already been burned by (the per-engine history scrapers deleted in 0.7 were
removed for exactly these mis-parses). A refusal you can read is recoverable; a
plausible fabrication is not.

**There is deliberately no backfill and no re-record**, and both of the obvious
paths are worse than the refusal:

- *Re-running the session to record a version* would record what is installed
  **now**, which says nothing about what wrote bytes from months ago. It
  produces a confident, wrong answer — the exact thing being avoided.
- *A one-off backfill command* would have to pick a reader for a session whose
  format is unknown. The only available guess is "the newest one", and the
  newest reader is the one most likely to be wrong about the oldest session.

**Going forward.** Sessions started under 0.7 record the engine version at
session start and convert normally. Two things can still leave a 0.7 session
unreadable, and `ctxloom doctor` reports both before they bite, on its
`DOCTOR-CHECK-TRANSCRIPT-READER-v2` line: an engine whose version could not be
probed at all (that session records nothing, and will refuse just like a 0.6
one), and an installed engine version outside every range this build carries a
reader for. That line names the version detected on your machine, the reader it
selects, and every range ctxloom carries — which is also the bug report that
gets a reader written for a version that has none.

## 8. ctxloom no longer launches claude with every permission skipped

0.6 passed `--dangerously-skip-permissions` to claude on every `ctxloom run`.
0.7 launches it at a declared permission posture instead.

**Interactive sessions** default to `acceptEdits`: file edits are approved
automatically and claude asks before anything else. Set `permissions:` on an
agent, or pass `--permissions <posture>` (`ctxloom run --help` lists them), to
choose another. `bypass` is only ever used when you declare it.

**Headless runs** (`ctxloom run --one-shot`, and delegated agent runs) have no
one to answer a prompt, so claude denies every call its posture would have
asked about. A run is never refused or widened for its posture: a one-shot
warns at startup, and a delegated child's turn that hit a denial reaches its
parent as `BLOCKED on <tool>: <reason>` rather than as a finished result.
`ctxloom init` gives the default agent the `acceptEdits` posture for its
headless runs (file edits go through without asking) and says so;
`ctxloom agent edit default --permissions <posture>` changes it.

**claude 2.1.283 or newer** is required: an older claude is refused at launch
with the upgrade as its remedy.

## 9. Companions run only once you register them, by name

ctxloom runs exactly the companions you registered. Register one with
`ctxloom companion add <name>`: it finds the companion's binary on PATH (a
shipped first-party companion is its own binary; any other name `<n>` is
`ctxloom-companion-<n>`), checks that it answers
`<binary> loadout --format yaml`, and records the NAME — never the path — in
your home config (`companions:` in `~/.ctxloom/config.yaml`). Nothing is
recorded when the check fails. Each session resolves the registered names on
PATH afresh, so a binary of a registered name placed earlier on PATH is the
one that runs.

A project's config may list more names under the same `companions:` key. They
are added to home's list; a project cannot replace or remove a home
registration.

`ctxloom companion list` shows the registered names and whether each resolves
on PATH. `ctxloom companion remove <name>` reports what it would unregister;
add `--yes` to apply it. A registered name that does not resolve on PATH is
reported with both ways out: install it, or remove it.

`just install` and the install scripts register the companions they install.
If you installed companions another way, register each once, for example
`ctxloom companion add ltk` and `ctxloom companion add taskloom`.

An agent container image carries the registered first-party companions and
their registration.

## 10. A lockfile keyed by the reference as typed is refused

`.ctxloom/lock.yaml` is now keyed by bundle identity. A
lockfile keyed by the reference as it was typed — every lockfile an earlier
ctxloom wrote — is refused, and until it is rebuilt no remote bundle loads
(ctxloom warns that it failed to load the remote lockfile). The refusal names
the file and the remedy: delete `.ctxloom/lock.yaml` and run
`ctxloom deps pull`, which re-resolves each bundle and writes the new form. If
the refusal lists holds, re-apply them after the pull; a lockfile with no holds
rebuilds at the same pinned commits.

The lockfile also no longer records when it was written (`locked_at`) or when
each bundle was fetched (`fetched_at`). Both changed on every pull, so a pull
that moved no pin still modified a committed `lock.yaml`. A pull at unchanged
pins now leaves the file byte-for-byte as it was. A lockfile that still carries
the two times loads as before. ctxloom drops them in memory, and the file
loses them at its next write, or at once under `--write-upgrades`.

## 11. Your profiles live in the project bundle

A profile is an item of a bundle, and a project's own profiles are the items of
its **project bundle**: the local bundle named `project`, at
`.ctxloom/content/bundles/v2/project/`. A bare profile name (`-p dev`, an
agent's `profiles: [dev]`) is that bundle's profile, so names you already use
keep working. Home follows the same rule under `~/.ctxloom`.

**A `.ctxloom/profiles/` directory now refuses to load**, naming the exact
manual move: the files into `content/bundles/v2/project/profiles/`, a
`bundle.yaml` when the project bundle has none, and the old directory removed.
ctxloom moves nothing itself. A `.yml` profile is renamed `.yaml`, and a profile
in a subdirectory takes a single-segment name: a profile name is one path
segment now, like every bundle item's.

What else follows from profiles being bundle items:

- `ctxloom profile create/modify/remove/edit/import` write into the project
  bundle; `create` and `import` take `--bundle <local bundle>` for another
  local bundle, and a remote bundle's profiles are refused. `profile create`'s
  `-b/--bundle` therefore no longer names the bundles a profile includes: that
  is `-i/--include`, and `-b` is gone.
- Every profile is decoded strictly: a profile item carrying a key the schema
  does not declare stops its bundle loading.
- Every local bundle's profiles are dependency roots: a remote bundle referenced
  only from one of them is locked.
- Bundle envelopes are now `schema_version: 2`: profile refs are stored in the
  canonical `ctxloom+git://` spelling, and an older bundle's profile refs are
  read that way.

## 12. claude started by hand gets no ctxloom context from a hook

In 0.6, `ctxloom manage hooks install` registered a SessionStart hook,
`ctxloom hook inject-context <hash>`, in `.claude/settings.json`. Every claude
session in the project, including one you started yourself with plain
`claude`, received the assembled context through it. A session that
`ctxloom run` launched received the context twice: through the hook and
through its system prompt.

In 0.7 the context reaches claude once, as the system prompt of a session
that `ctxloom run` launches. The hook no longer carries it:

- `hook inject-context` is removed. Its replacement, `ctxloom hook
  session-start`, takes no arguments. It delivers the agent-setup nudge, and
  never the project's context (nor, since §15, a resumed session's essence or
  a "run /recover" notice).
- **A claude session you start by hand gets no ctxloom context from any
  hook.** It reads only what was materialized into the project at rest
  (§13), by `ctxloom materialize --yes`. The refresh that runs after
  `deps pull --yes` or a `run --yes` startup sync does not write claude's
  context. Otherwise
  nothing tells you so: the session starts normally, without your
  profiles' guidance. Start it with `ctxloom run` instead.
- The `hook` context approach (`surfaces: {context: hook}` on a claude agent
  binding) is removed, because no hook delivers context any more. A binding
  that names it is refused, with the approaches claude supports:
  `system-prompt` (the default) and `file`.
- If you keep ctxloom's hooks in the project, run `ctxloom materialize
  --yes --surface settings --surface hooks` once after upgrading (without
  the `--surface` flags it also writes claude's CLAUDE.md). It adds the
  `session-start` hook. It removes the old `inject-context` entries only
  where this machine's ctxloom recorded installing them. An entry written by
  0.6, or by ctxloom on another machine and committed with the file, stays.
  Claude runs each one at every session start, and each one fails with
  `unknown command "inject-context"`. `ctxloom doctor` reports every such
  entry (`DOCTOR-CHECK-STALE-HOOKS-n5`: the file, the event and the verb),
  in the project's and in your user settings file. `ctxloom doctor --fix`
  removes them and leaves everything else in the file as it was. The check
  is general: it flags any hook entry that runs a `ctxloom hook` subcommand
  this ctxloom does not have, not only `inject-context`.

## 13. `ctxloom materialize` is the at-rest delivery, and the native-file approach is `file`

`ctxloom materialize [<profile>...]` writes the assembled profiles into a
directory as each engine's native files. With no profiles it writes the
default agent's; with no `--backend`, the project's configured engines.

- **`--target DIR`** takes any directory, created if missing. It need not be
  a project: profiles and config come from the project you run it in.
- **With no `--target`** the target is the project directory, and the
  command refuses to write (or release) there unless you pass `--yes`.
  `--dry-run` shows what it would do. `--force` is still the separate
  override for a target that is an engine's user-global scope.
- **By default it writes every kind, context included, for every engine** —
  including claude, whose context the 0.6 `manage hooks install` never
  wrote at rest. `--surface KIND[=file][:DEST]` writes only the named kinds and
  leaves the others as they were; `--surface context=file:PATH` writes the
  context into `PATH` inside the target.
- **`--release`** takes out what an earlier materialize wrote, for the
  selected kinds (default all). `--diff FILE` compares instead of writing.
- **What ctxloom wrote is now owned per engine and per kind.** Delivering
  one kind or one engine no longer takes another's files out of the same
  directory. Claims an earlier 0.7 pre-release build recorded under the
  single `project` writer are not recognised at all: a re-install does not
  take them out, `manage uninstall` does not release them (beyond the
  `ctxloom hook <verb>` entries and HUD statusline it sweeps from a settings
  file no record names; see below), and `ctxloom run` does not warn about
  context written under them, so a section it appended may appear twice
  until you remove the old one by hand.
- **The post-sync refresh** (after `deps pull`, or a `ctxloom run` startup
  sync that installs a remote bundle) writes the project root only when you
  pass `--yes`: `ctxloom deps pull --yes`, `ctxloom run --yes`. That is the
  consent `ctxloom materialize` asks before writing the project directory.
  In 0.6 a pull applied hooks unasked. **Without `--yes` the pull (or the
  run) still succeeds**: it installs and locks the closure, writes and
  removes no engine file, and prints `Project refresh skipped`, naming each
  engine's kinds it would have written or released and the two ways to apply
  them (the same command with `--yes`, or the `ctxloom materialize --yes
  --surface ...` that writes exactly those kinds). The note compares against
  the project's files as they stand: a kind is named only when `--yes` would
  change its files (a hand edit `--yes` would undo counts) or release a claim
  it holds, so a pull right after a `--yes` pull prints no note at all. A kind
  the refresh would be refused (a value in the file that is not ctxloom's, a
  file that does not parse) is named on its own line with a `fix:` line; the
  pull still succeeds. A note that names only refused kinds has no apply
  line, since there is nothing `--yes` would write. `--format json` carries the same as
  `refresh_skipped`. A script that relied on a pull refreshing the
  project's hooks or MCP servers must add `--yes`. There is no config
  setting for it. With `--yes`, the refresh runs `ctxloom materialize` into
  the project root. It refreshes hooks, MCP servers, settings, commands and
  skills for every configured engine, and the context file of an engine
  that reads its context from a file at rest. It leaves alone the context of
  an engine whose context rides the launch (claude: its system prompt),
  neither writing nor removing it, so a sync writes no CLAUDE.md.
- **A `ctxloom run` warns** when the project holds context materialized into
  it, naming the file and `ctxloom materialize --release --surface
  context`.
- **Do not commit a materialized file.** The record of what ctxloom wrote
  lives in your home directory; on another machine a committed copy reads
  as your own text, and the next materialize appends a second copy.
- **In a clone with no record, `manage uninstall` still takes out
  ctxloom's hooks and HUD statusline.** When the record in your home
  directory names nothing in a settings file — a fresh clone of a repository
  that tracks `.claude/settings.json` — the release removes every hook entry
  that runs `ctxloom hook <verb>` and a statusline that runs `ctxloom hook
  hud`, and drops each hook group and event left empty. Your own hooks, a
  hook that runs ctxloom with any other subcommand (`ctxloom profile list`),
  and every other key stay. Before, it reported `released` and removed
  nothing. `manage check` now reports `hooks_present` and `status_line` by
  the same rule, so a hook of yours that runs `ctxloom <something else>` no
  longer counts as ctxloom's. ctxloom's `env` values, CLAUDE.md and command
  files committed to a clone are still not removed without a record.
- **`ctxloom profile materialize`, `ctxloom manage hooks install` and
  `ctxloom manage hooks uninstall` are removed.** A script that calls one now
  fails with `unknown command`. Use `ctxloom materialize` (it takes the
  profiles, `--target`, `--backend` and `--diff` the old command took; with
  no `--target` add `--yes`), and `ctxloom materialize --release` in place
  of `hooks uninstall`. `ctxloom manage uninstall` stays: it releases every
  engine's at-rest delivery from the project, including an engine the
  project no longer configures. Unlike `manage hooks install`,
  `ctxloom materialize` writes claude's CLAUDE.md unless you leave context
  out with `--surface`, and it refuses (exit 3) a profile that does not
  resolve, such as a remote parent not yet pulled, unless you pass
  `--degraded`.
- **A refusal writes nothing.** `ctxloom materialize` assembles every
  engine's files before it writes any, and a finding it would refuse over
  (exit 3 — a parent profile not yet pulled, or one that does not exist)
  now stops it first: no engine file, no record of ownership, not even a
  missing `--target` directory. Before, it wrote CLAUDE.md without the
  missing parent's content, the settings, the commands and their records,
  and only then exited 3. `--degraded` still writes past the finding and
  warns. `ctxloom deps pull --yes` refreshes the project through the same
  check: the pull itself lands, the refresh writes nothing, and the command
  exits 3 naming the finding. Before, it wrote the refresh and exited 0. A
  write that fails during that refresh now exits 3 the same way. The abort
  names what refused — `ctxloom: aborting materialize:` or `ctxloom: aborting
  project refresh:` (materialize's said `aborting startup`) — and offers
  `--degraded` "to write anyway", not "to launch anyway".
- **A parent profile that does not exist is no longer sent to `deps pull`.**
  A missing local parent — a bare name, or a `ctxloom+local:` reference —
  is reported as not existing, with a fix that says to create it, correct
  it or remove it from the profile's `parents` (and a "did you mean" when
  one installed profile has that name). Pulling fetches remote bundles and
  cannot create a local profile. A missing default-agent profile that is
  local gets the same fix. A remote parent not yet pulled still names
  `ctxloom deps pull`.
- **`ctxloom manage install` now pulls the project's dependencies**, as
  `ctxloom init` does, so the default profile's parent
  (`ai-developer#profiles/developer` from ctxloom-default) is installed and
  the next `ctxloom run` or `ctxloom materialize --yes` works. Before, both
  refused (exit 3) until you ran `ctxloom deps pull`. If the pull cannot
  complete — offline, or the remote unreachable — install still succeeds
  and keeps what it wrote, and warns naming what failed and that `ctxloom
  deps pull` retries; the next strict command refuses until then. Install
  now reaches the network unless you pass the new `--no-pull`. Its `--format
  json` output gains a `pull` object (`status`: `pulled`, `failed` or
  `skipped`; `installed`; `error`).
- The native-file delivery approach is renamed **`file`** (it was spelled
  with an `unsafe-` prefix): in `agent set --surface`, an agent binding's
  `surfaces:`, and the config schema. The old spelling is refused as an
  unknown approach; there is no alias.
- A premised fragment delivered to claude as a skill now carries its
  premise as the skill's description. Before, claude refused every such
  skill while the materialize report said it was delivered.

## 14. Everything else marked breaking

Grouped by what you would have to change.

**CLI surface**
- `deps upgrade` previews by default: it shows every pin that would move and
  what each brings in — items added, removed or changed, what hooks and MCP
  servers run before and after, a diff of every changed script — and writes
  nothing. Env and header values appear only as a fingerprint of the value
  (`<a1b2c3d4>`, in text and JSON alike), so a changed credential shows as
  changed without being printed. `deps upgrade --yes` applies it. It is the only command that moves
  an existing pin.
- `deps pull`, `init` and startup sync never move an existing pin. A changed
  constraint is reported and takes effect only on `deps upgrade --yes`;
  `deps pull --force` reinstalls each reference at its pin instead of
  re-resolving it. Each new pin is shown with everything it brings in. A
  reference re-pulled at the pin it already had is counted as `reinstalled`
  ("Reinstalled at their pin: N"; JSON field and item status `reinstalled`),
  replacing `updated`. A cached bundle tree checked out at a commit other
  than its pin — a lockfile that moved through git leaves the cache behind —
  is not installed: `deps pull` and startup sync reinstall it at the pin
  without `--force`. A sync that succeeds in full also deletes the cached
  checkout of every bundle the lockfile no longer names, and reports each one
  ("Pruned the checkout at …"; JSON `pruned_checkouts`). It prunes nothing
  after a failed or incomplete sync, nor when the cache is not physically the
  project's own (a symlink points it elsewhere).
- `session delete` actually destroys the session (it previously did not).
- `session purge` fans out to the population that owns each destroyer.
- `session backfill` is deleted, and nothing replaces it — see §7 for what that
  means for the sessions you already have.
- The transcript has its own sub-noun.
- A session is renamed by assigning its name, not by a verb.
- `session list` and `session search` show each session's state in a new
  STATE column: `live`, `ended` or `unknown`. The state comes from the
  session's liveness lock, never from its recorded end time: a held lock is
  `live`, a free one `ended`, and anything the lock cannot prove (no lock
  file, an untrusted filesystem, a failed probe) is `unknown`. END is blank
  while a session is live, including a resumed session that still carries
  its previous run's end. Under `--format json` (and yaml, toml) each row
  gains `state` and omits `end` while the session is live, so a script that
  read a present `end` as "finished" must read `state` instead. Both
  commands take `--state live|ended|unknown` to list only sessions in that
  state; any other value is refused.
- A `distill` prompt the delivery pipeline withholds now REFUSES the run —
  `bundle distill`, `fragment distill`, `command distill` and item edits exit 2
  and name the item — instead of silently distilling with ctxloom's built-in
  default. A project that never configured a `distill` prompt is unaffected:
  absence still falls back to the default, because absence is not a decision.
- `config get <section>` is deleted. `config show <section>` prints one section;
  bare `config show` prints the whole document.

**Configuration and content**
- MCP servers come from bundles only; ctxloom's own ships as a builtin.
- `select_tags` (selects content) is split from `tags` (descriptive only). A
  profile relying on `tags` to select content selects nothing until updated.
- `Hook.Order` and the hook sidecar are dropped.
- A bundle hook narrows to a kind of tool with `tool:` — `shell`, `file_edit`
  or `skill` — which each engine maps to its own tool names. `matcher:` in a
  bundle hook (it named one engine's tools) is refused when the bundle is
  read, as is a `tool:` outside those three; there is no alias, so replace
  `matcher: Bash` with `tool: shell` and `matcher: Edit|Write` with
  `tool: file_edit`. A profile's own hooks may still give `matcher:`.
- Under claude a class names every tool of that kind: `shell` is
  `Bash|PowerShell` and `file_edit` is `Edit|Write|MultiEdit|NotebookEdit`.
  So the `pre_shell` and `post_file_edit` events, and any hook narrowed by
  class, now also fire on PowerShell, MultiEdit and NotebookEdit.
- `subagent` is renamed to `agent` across config, CLI, API and prompts.
- Distillation runs tool-less on every engine: `bundle distill`,
  `fragment distill`, `command distill` and item edits give the model no tool
  and attach no MCP server. They are not plan mode, so they need no approval channel. They
  still run as the `distiller` agent (or the `--llm` you name), but that
  agent's `permissions:` block and its profiles' MCP servers no longer apply
  to them. An engine runs a tool-less session only if it implements one
  (claude and opencode do); on any other engine the distillation is refused
  rather than run with tools. Trigger triage and premise authoring still
  run at plan.
- Every profile file is validated against the profile schema when it loads. A
  key the schema does not have (a `name:` key, for one) or a value of the wrong
  type is a fatal finding naming the file and the offending key; `--degraded`
  downgrades it to a warning and launches with the profile as read.
- Every YAML file ctxloom saves is written in one format, two-space indented:
  the same bytes `--write-upgrades` writes. A file you wrote by hand is
  reindented the next time ctxloom saves it. `.ctxloom/config.yaml` keeps its
  comments and key order; a file ctxloom re-renders from its own model, such
  as a bundle's `bundle.yaml`, does not keep its comments.
- `--format yaml` command output is two-space indented too (it was four), so
  every command prints YAML in the same layout as the files ctxloom saves.
- `--format yaml` command output lists keys in the same order as `--format
  json` (each struct's field order) instead of alphabetically. Keys and values
  are unchanged. `--format toml` stays alphabetical: TOML output has no
  ordered form.
- In `--format text` and `--format markdown`, a map in a command's result
  renders as a section with one line per key instead of one `k=v, k=v` line,
  and a map of records (`bundle show`'s fragments, commands, MCP servers,
  skills and env) renders one sub-section per entry instead of Go struct
  syntax. A list of maps that share their keys and hold plain values renders
  as a table.
- `ctxloom` reports a failure as `ctxloom: <message>` instead of
  `Error: <message>`, and its `fix:` line is indented two spaces, under
  `--format text` and `markdown` alike (an explicit `--format markdown` no
  longer prints `**Error:** <message>`). The `{"error": …}` envelope under an
  explicit `--format json|yaml|toml` and the exit codes (1 for an error, 2 for
  a refusal, an engine's own status passed through) are unchanged. `--format`
  values now complete in the shell.
- `taskloom` reports a failure as `taskloom: <message>` (plus a `fix:` line
  when there is one) instead of `Error: <message>`, under `--format text` and
  `markdown` alike; `ltk` and `harp` keep their `ltk:`/`harp:` prefix and gain
  the `fix:` line. Under an explicit `--format json|yaml|toml`, `harp` now
  reports a failure as an `{"error": …}` envelope on stderr like the other
  binaries, and `ltk`'s and `harp`'s warnings become JSON Lines.
- `harp --help` states `--format`'s real default (text on a terminal, json
  when output is piped or redirected) instead of a fixed `text`. A pipe already
  got json; only the help was wrong. `harp`, `ltk` and `taskloom` complete
  `--format` values in the shell.
- New config key `non_tty_format` (`json` | `yaml` | `toml` | `text` |
  `markdown`): the format a ctxloom command writes when stdout is not a
  terminal and no `--format`/`--json` was given. Unset, it is `json` — so
  nothing changes until you set it. A terminal still gets text, and an
  explicit `--format` always wins. Warnings on stderr follow the same choice:
  JSON Lines under json/yaml/toml, the plain `ctxloom: warning:` line
  otherwise. Honoured from every layer, `CTXLOOM_CONFIG_NON_TTY_FORMAT`
  included; any other value is refused at startup, never silently read as
  json. `ctxloom --help` names it in `--format`'s default.

**Isolation**
- The container runtime axis splits into two ownership modes,
  `container-rootless` and `container-rootful`. There is deliberately no "any
  container" value, and an ownership mismatch is fatal rather than a
  substitution.
- A requested container that cannot start is fatal unless `--degraded`, which
  falls back to the HOST and never to the other ownership mode.
- Container identity contracts are enforced and ownership residue is surfaced.
- The claude agent image installs the claude CLI version installed on the
  host (`npm install -g @anthropic-ai/claude-code@<host version>`), never the
  latest release. The version is in the image tag (`…-cli<version>-…`) and the
  `ctxloom.engine-cli-version` label, so upgrading claude on the host rebuilds
  the image on the next containerized run. When the host has no claude, or its
  version cannot be read, the image pins ctxloom's tested claude version
  (`CLAUDE_CODE_CLI_VERSION` in `.github/engine-versions.env`) and a warning
  names it. Images you supply through `isolation_images` or `--overlay-image`
  are not changed.

**Backends**
- The `gemini` and `codex` backends are removed, with no replacement.
  `claude-code` is the only engine. A config entry still typed `gemini` or
  `codex` breaks the config schema, so every command that reads the config
  refuses it (see section 2); under `--degraded` it is a warning, and
  `unknown LLM backend type "<type>"` where it would be used; the entry cannot
  run.
- `taskloom` and `ltk` ship as bundled companions.

- Every `ctxloom hook <verb>` that reads an engine's payload now takes
  `--engine <name>`, naming the engine that fires it, and reads and answers
  through that engine's codec. ctxloom writes the flag itself when it
  delivers its hooks. A hook entry without it — installed by an earlier
  ctxloom, or written by hand (`ctxloom hook stamp-plan` in your own
  `.claude/settings.json`) — keeps working: it is read as the default
  engine's hook (claude-code), which is what every such entry was. An
  `--engine` naming an engine ctxloom does not know is refused.
- `ctxloom hook session-bind` and `hook session-start` used to exit 0 on a
  payload they could not parse, so a session could go its whole life
  unbound with nothing failing. They now exit non-zero.
- `ctxloom hook stamp-plan` reads the edited file from claude's real payload
  shape (`tool_input.file_path`, or `notebook_path`); a bare top-level
  `file_path`, which no engine sends, is no longer read.
- `ltk` reads a zsh command line containing an empty parameter name, `${}`,
  as ordinary text: the parameter is dropped and the words around it are
  kept, so rules match the command. It used to be reported as a parse
  error, so your `on_parse_error` setting decided the outcome instead.
  Upgrading the shell parser (mvdan.cc/sh v3.14.1) fixed the crash behind that
  error.
- ctxloom's own git no longer inherits a variable that points git at another
  repository or overrides its config. Every variable `git rev-parse
  --local-env-vars` names is removed before ctxloom runs git — now including
  `GIT_CONFIG_PARAMETERS`, `GIT_CONFIG_COUNT` with its
  `GIT_CONFIG_KEY_<n>`/`GIT_CONFIG_VALUE_<n>` pairs, `GIT_SHALLOW_FILE` and
  `GIT_ALTERNATE_OBJECT_DIRECTORIES` — so ctxloom run from a git hook or an
  alias that passes `-c` no longer picks up those overrides. When git cannot
  answer, every `GIT_` variable is removed.

## 15. ctxloom no longer compacts sessions — the engines do

Session compaction — distilling a session's transcript into an `essence.md`
that a later session resumed from — is removed. Every engine ctxloom drives
compacts its own context, and resumes its own sessions. **Prompt compression
is unaffected**: `bundle distill`, `fragment distill`, `command distill`,
auto-distill on edit, `.distilled.md` and `use_distilled` all work as before.

**Removed.**
- `ctxloom session compact`, `ctxloom session list --compact`, and
  `ctxloom run --session <name> --compact` (a bare `--session` still resumes,
  folding the session's full transcript into the run's context).
- `ctxloom session artifacts` (`list`, `purge`). `session purge` and
  `session remove` no longer reach the output dir (§4).
- `session list --full` and `session search --full` (they printed essence
  bodies). `session search` matches the session's name, title and times.
- The MCP tools `compact_session`, `load_session`, `recover_session`,
  `get_previous_session` and `list_sessions`. There is no in-session recall
  through ctxloom; use your engine's own resume (claude's `/resume`), or
  `ctxloom run --session <name>`.
- `ctxloom hook next-step` and the TurnEnd hook entry that ran it. The next
  `ctxloom materialize --yes` (or any re-delivery of ctxloom's managed hooks)
  removes the entry from a project's `.claude/settings.json`.
- What the session-start hook delivered for compaction: the resumed essence,
  and the "context cleared, run /recover" notice after `/clear`. The start
  banner no longer points at a previous session or a "resume" skill.
- The `config.essence_max_chars` key. The config moves to format generation
  8; a generation-7 config is migrated in memory (the key is dropped) and
  persisted under `--write-upgrades`.
- From JSON output: `session show`'s `compacted`, `essence` and
  `essence_path` (it now prints the session's record — see below);
  `session list`/`search`'s `essence_path`; `run --dry-run`'s
  `resumed_essence` and `resumed_essence_note`. The session sidecar stops
  recording `source_entries` (generation 3; older sidecars migrate in
  memory).

**A session's title is its first prompt.** The `summary` a `session list` or
`session search` row carries — in the table and in JSON, where an editor's
session picker reads it — is now the first thing the session's user asked:
the first user prompt in the session's recorded transcript, cut to its first
line and 80 characters, read when the session is listed. A session whose
transcript holds no prompt shows `(no summary)`. `session show <name>` prints
the session's record: its title, state, engine, native session, project,
start and end, and whether it was purged or is kept.

**A transcript is kept until its session ended 30 days ago.** With no
essence standing in for it, a session's transcript is its record. Every
destroyer of transcripts — `session transcript purge`, `session purge`,
`session remove`, `session sweep` and `clean --include-persist` — refuses
one whose session ended within `session_transcript_retention` (a new config
key, default `30d`), and refuses a session marked keep at any age. `ctxloom
session keep <name>` marks one (stored in the session's sidecar, which moves
to generation 2; `session list` shows `kept`), and `--undo` releases it. An
internal one-shot's transcript stays reclaimable at any age. `--uncompacted`
is removed, and no flag overrides retention.

**Files already on disk are left alone.** `essence.md`, `segments/*.md` and
`next-step.md` files an older ctxloom wrote stay in each session's output
dir as notes; nothing reads, rewrites or deletes them. There is no cleanup
step.

## If you ran a 0.7 development build

Some things appeared after 0.6.4 and are gone again in 0.7.0. They were never
in a tagged release, so this only matters if you ran a build from `main`.

- ACP is removed: the `ctxloom acp` commands and the ACP agent transport no
  longer exist.
- The `kiro` and `antigravity` backends are removed. A config entry typed
  one of them fails the same way as `gemini` above. The config schema no
  longer has a `codex` branch.
- The development build's `opencode` backend is replaced by a new one with a
  different label shape. It requires `model`. With a `provider` block
  (`base_url`, `output_limit`, plus `context_window` on the label) it drives
  opencode against that OpenAI-compatible endpoint, with the key taken from
  `CTXLOOM_OPENCODE_API_KEY`. Without one, `model` names one of opencode's
  built-in providers and passes through as written (`opencode/big-pickle`, a
  free hosted model), and no key is needed: the run authenticates keyless
  when `CTXLOOM_OPENCODE_API_KEY` is not exported. An entry in the old shape does
  not validate if it has no `model`, or if its `permissions` is a string (the
  old posture spelling; the new branch takes a permissions block). See
  Configuration → LLMs.
- The `thinking` key under a backend config is removed. It was accepted and
  had no effect; a config still setting it is now refused as an unknown key
  (see section 2). Remove it, or run with `--degraded` to ignore it.
- The `ctxloom hook tool-reflect` PostToolUse hook and the
  `tool_reflect_bytes` config setting are removed. A config still setting
  `tool_reflect_bytes` is refused as an unknown key (see section 2). Remove
  it, or run with `--degraded` to ignore it.
- A second `ctxloom run` in a project that already had one open was refused.
  It now starts its own coordinator beside the first. Coordinator state moved
  from `~/.ctxloom/coord/<project>/` to one directory per session tree,
  `~/.ctxloom/coord/<project>/<session>/`. Files left directly under
  `<project>/` are no longer read, and you can delete them. A session's
  directory stays after the session exits, so `ctxloom run --session` can
  resume its tree, until `ctxloom session sweep` removes it with the session.

- A ctxloom-launched session — `ctxloom run`, interactive or one-shot, and
  every `agent_run` child — received its assembled context twice: once
  through the engine's context surface (claude's system prompt) and again
  as the text of its first turn. The first turn is now the prompt alone. An
  interactive `ctxloom run` with no prompt opens with no turn at all, rather
  than with the context posted as your first message.
