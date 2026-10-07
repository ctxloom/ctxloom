# Breaking changes in 0.7.0

**Read this before upgrading from 0.6.x.** ctxloom breaks rather than shims;
breaking *silently* is what it does not do — hence this page.

One change in this release fails **silently** if you do nothing, so it is
first.

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
config, naming the key and the keys its section does know. Under
`--degraded` (or `CTXLOOM_DEGRADED=1`) the config loads best-effort: the key
is warned about and ignored.

## 3. The config-level `hooks:` block is gone

It was a second implementation of something profiles already do, with exactly
one consumer and no writer anywhere. A config carrying `hooks:` now reports it
as an unknown key (see section 2).

**Move each hook to a profile** — a profile of your project bundle
(`.ctxloom/content/bundles/v2/project/profiles/<name>.yaml`), under the same
`hooks:` key, spelled identically.

## 4. Session essences moved to the session's output dir

A session's essence, next step and plans, and each per-rotation essence
(`segments/<sessionID>.md`), now live in the session's output dir —
`<Documents>/ctxloom/<project>/<harp>/` by default, or under the `output_dir`
config key — recorded in the session's `session.yaml` when it is created. The
machine state (transcripts, native engine history, spool) stays under
`~/.ctxloom/sessions/<harp>/`. No sweep or `ctxloom clean` ever deletes the
output dir; only `ctxloom session artifacts purge` removes an essence there. The project-rooted
`<project>/.ctxloom/sessions/<sessionID>.md` store is no longer written or read.
Existing files there are not migrated and are safe to delete; `ctxloom session
adopt` re-indexes an outside session if you want its history back.

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
at the end of an interactive `ctxloom run` and when you `/recover` a session
(the `recover_session` tool), naming the session and the reason:

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
session, and therefore anything downstream of it — a distilled essence, a
`/recover` that replays the conversation. The engine's own transcript is
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

`.ctxloom/lock.yaml` is now keyed by bundle identity (lockfile version 2). A
lockfile keyed by the reference as it was typed — every lockfile an earlier
ctxloom wrote — is refused, and until it is rebuilt no remote bundle loads
(ctxloom warns that it failed to load the remote lockfile). The refusal names
the file and the remedy: delete `.ctxloom/lock.yaml` and run
`ctxloom deps pull`, which re-resolves each bundle and writes the new form. If
the refusal lists holds, re-apply them after the pull; a lockfile with no holds
rebuilds at the same pinned commits.

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

## 12. Everything else marked breaking

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
- `subagent` is renamed to `agent` across config, CLI, API and prompts.
- Every profile file is validated against the profile schema when it loads. A
  key the schema does not have (a `name:` key, for one) or a value of the wrong
  type is a fatal finding naming the file and the offending key; `--degraded`
  downgrades it to a warning and launches with the profile as read.

**Isolation**
- The container runtime axis splits into two ownership modes,
  `container-rootless` and `container-rootful`. There is deliberately no "any
  container" value, and an ownership mismatch is fatal rather than a
  substitution.
- A requested container that cannot start is fatal unless `--degraded`, which
  falls back to the HOST and never to the other ownership mode.
- Container identity contracts are enforced and ownership residue is surfaced.

**Backends**
- The `gemini` and `codex` backends are removed, with no replacement.
  `claude-code` is the only engine. A config entry still typed `gemini` or
  `codex` draws a config-schema warning when the config loads and
  `unknown LLM backend type "<type>"` where it would be used; the entry cannot
  run.
- `taskloom` and `ltk` ship as bundled companions.

## If you ran a 0.7 development build

Some things appeared after 0.6.4 and are gone again in 0.7.0. They were never
in a tagged release, so this only matters if you ran a build from `main`.

- ACP is removed: the `ctxloom acp` commands and the ACP agent transport no
  longer exist.
- The `kiro`, `opencode` and `antigravity` backends are removed. A config
  entry typed one of them fails the same way as `gemini` above. The config
  schema no longer has `codex` or `opencode` branches.
- The `thinking` key under a backend config is removed. It was accepted and
  had no effect; a config still setting it now draws an unknown-key warning.
- A second `ctxloom run` in a project that already had one open was refused.
  It now starts its own coordinator beside the first. Coordinator state moved
  from `~/.ctxloom/coord/<project>/` to one directory per session tree,
  `~/.ctxloom/coord/<project>/<session>/`. Files left directly under
  `<project>/` are no longer read, and you can delete them. A session's
  directory stays after the session exits, so `ctxloom run --session` can
  resume its tree, until `ctxloom session sweep` removes it with the session.

