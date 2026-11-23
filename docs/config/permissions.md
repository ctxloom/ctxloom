# Permissions

An agent's permissions are written **per engine**, in that **engine's own
words**, and ctxloom passes them on as a **verbatim copy**. ctxloom does
**not check** what the block says: a key it does not know, a value the
engine does not take, or a typo reaches the engine as written, and it is the
engine that accepts, warns about or ignores it (owner rulings 133-139).

**One exception: ctxloom allows its own tools.** Every session's engine is
given ctxloom's own MCP server (`ctxloom mcp serve`), so the agent can call
its daemon. On claude-code ctxloom lays `permissions.allow:
["mcp__ctxloom__*"]` into your block, after the block's own `allow` and
once, in the same place and the same way it lays a profile's `deny_tools`
into the block's `deny`: the session home's `settings.json`, or
`ctxloom-hooks.json` under `session_home: host`, where your block lives
there. So those calls are never asked for, and every other word of your
block reaches claude as you wrote it. A run with no context (`run
--no-context`) is offered no server, so nothing is allowed for it. That is
the only permission ctxloom adds of its own (owner ruling OD-4, 2026-11-13,
amended 2026-11-14). To hold one of the tools back, `deny` it: in claude's
rules a deny wins over an allow.

## Where the block goes

The block lives on the agent, under the engine it is for:

    agents:
      <agent>:
        engines:
          <engine>:
            permissions: { ... the engine's own document ... }

It is written only there. The engine block `engines.<engine>` refuses a
`permissions` key (as it refuses `model` and `effort`), and there is no
project-level `permissions` section any more. No command writes it: edit the
config by hand, with `ctxloom config edit`, or let the setup agent write the
YAML. (`agent create|edit --permissions` is gone.)

`agent show`, `agent list`, the create/edit summary and `run --dry-run` show
the block as written and say "passed to <engine> verbatim; ctxloom does not
check it". Their JSON carries the document as written.

What each engine is handed:

| Engine | Where the block is merged |
|---|---|
| claude-code | the `permissions` object of the user-scope `settings.json` of the session's own claude config dir (the session home, which ctxloom sets as `CLAUDE_CONFIG_DIR`): a copy of your own claude `settings.json` (from `$CLAUDE_CONFIG_DIR`, else `~/.claude`) with the block as its `permissions`, apart from `ctxloom-hooks.json`, ctxloom's hooks, which claude is handed with `--settings` |
| opencode | the `permission` object of the `opencode.json` ctxloom writes in the session home (`OPENCODE_CONFIG_DIR`): a copy of your own global `opencode.json` (from `$XDG_CONFIG_HOME/opencode`, else `~/.config/opencode`) with the block as its `permission` |

The block replaces your own `permissions` (`permission`) WHOLE: nothing of
yours in that key reaches the session; the rest of your settings
(attribution, model, env, plugins, hooks, statusline, ...) do (owner ruling
169). A file of yours that is not a JSON object is left out, and the run says
so naming it. Under `session_home: host` nothing of yours is copied.

**A bad value disables the block alone.** claude refuses a settings file
whole when one value in it is invalid ("These settings files did not load,
so nothing in them is in effect, including hooks and permission rules"). The
block is therefore in a file of its own: an invalid value in it turns off
your permissions, and claude says so naming that file, while ctxloom's hooks
(and so the session's transcript) still load (owner ruling 145). A key claude
does not know, alone, is tolerated. Your own settings share that file, so an
invalid value of yours turns the block off too.

These files are in the session's own directory, where the agent's own file
tools can reach them, so ctxloom writes each one again, whole, from your
config at every launch (a new session, a child's turn, a resume). An edit the
agent makes to its own permissions never carries into a launch.

## How long an ask waits: `engines.<engine>.approval_timeout`

When the engine's own rules say "ask" for a tool call of a child (a session
the daemon hosts, or a one-shot), the ask waits for your answer for the
engine's `approval_timeout`; then it is denied, and the engine is told that no
answer came in time (OD-41). It is the engine's setting, shared by all of its
agents; no agent overrides it:

    engines:
      claude-code:
        approval_timeout: 15m

- The value is a span in the config's one grammar: one or more links of a
  number and a unit, `90s`, `5m`, `1h30m` or `1.5h`, in whole seconds. A
  bare number has no unit and is refused.
- `now` denies every ask at once, and nothing ever waits (D16). Use it for a
  session nobody watches.
- Unset, an ask waits 300 seconds (`5m`).

The value is read with the rest of the engine's settings when a child is
launched, and again when it is hosted again (T21). A change to the config
reaches the next launch, never a child already running. `engine show` and
the Settings section of `run --dry-run` give the value and the layer that
said it (`engines.<engine>`, or `default`).

On claude-code, an ask is a call to ctxloom's own approval tool, and claude
stops waiting for an MCP tool call after a limit of its own. When
`approval_timeout` is over 300 seconds, ctxloom gives the child
`MCP_TOOL_TIMEOUT`, in milliseconds, of the timeout plus 60 seconds. Then
the ask expires, and its call is answered, before claude gives up on the
call. At 300 seconds or less, the variable is not set (owner ruling Q12).
When ctxloom sets it, it is a name ctxloom owns: a
`CTXLOOM_PERMIT_MCP_TOOL_TIMEOUT` is ignored, and the run says so. A terminal
session asks at its own terminal and is never given it.

## Answering an ask: `session approvals`

A child or a one-shot has nobody at its terminal, so when its engine's rules
say "ask", the ask is put to you through the daemon, and the engine waits.
List the waiting asks, from any terminal in the project:

    ctxloom session approvals [<harp>]

Each ask is listed under the root of its session tree, with its name
(`ask-<two words>`), the tool and an excerpt of its input, and the options it
offers. Answer it by its name, or by the asking session's harp while that
session has exactly one ask waiting:

    ctxloom session approvals answer <ask|harp> <option> ["<message>"] [--yes]

Only you answer: ctxloom gives agents no tool that answers an ask. A claude
ask offers four options, built by ctxloom from the rule the ask implies, the
tool and its exact input (`Bash(<command>)`; `<Tool>(<path>)` for Read,
Write, Edit, MultiEdit and NotebookEdit):

- `allow-once`: run it, this once.
- `allow-session`: run it, and the same call again, unasked, for the rest of
  the session. claude keeps the rule; nothing is written.
- `allow-always`: as `allow-session`, and the rule is also written into the
  asking agent's `agents.<agent>.engines.claude-code.permissions` `allow`, so
  every later launch carries it (see below).
- `deny`: do not run it. A message, when you give one, is said to the model.

Any other tool's ask offers `allow-once` and `deny` alone. An answer to an
ask already settled (answered, expired or withdrawn) is refused.

**Where `allow-always` writes.** The rule goes where it takes effect. A
project's definition of an agent replaces the home's of the same name whole,
so the rule is written into the project's config when the project defines
the agent, and into the home's otherwise. It starts from that definition's
own permissions document for the engine, and one with none starts from the
engine's default (`{defaultMode: acceptEdits}` on claude-code), so nothing
of that is lost. It is a config write like any other: it previews first,
naming the file (off a terminal without `--yes`; exit 17 under
`--detailed-exitcode`), asks `[y/N]` at a terminal, and applies with
`--yes`. The config is written before the ask is answered; if the ask was
settled meanwhile (it expired, say), the written rule stays and the command
says so (exit 48).

**An ask that is withdrawn.** When the asking turn is interrupted (a steer),
claude cancels its call and the ask is withdrawn at once: it is listed no
more. An ask whose session ends without withdrawing it waits out its
`approval_timeout`.

**What asks, and what does not.** Asks are routed from claude-code children
and one-shots only. An interactive session asks at its own terminal. A
launch with no context (`run --no-context`, and the distiller) is given no
ctxloom server, so no approval tool: claude's headless default denies its
asks, and nothing is listed (owner ruling, 2026-11-16). An opencode child's
permission request is refused, and an opencode one-shot (`opencode run`)
has no way to ask, so its asks are rejected: routing opencode's asks is not
in this build yet. Whether an engine asks at all is its permissions
document's to say.

## claude-code

The block is claude's `permissions` settings object, exactly as claude's own
documentation describes it: https://code.claude.com/docs/en/permissions

    agents:
      reviewer:
        engine: claude-code
        engines:
          claude-code:
            permissions:
              defaultMode: plan
              allow: ["Read", "Grep", "Bash(git diff *)"]
              deny: ["WebFetch"]

The mode a session starts in is claude's `defaultMode`. ctxloom passes no
`--permission-mode` flag.

It is claude's USER scope, its lowest file scope: a single value the
project's own `.claude/settings.json` (or `.claude/settings.local.json`) also
sets, such as `defaultMode`, is the project's; lists from every scope are
merged (https://code.claude.com/docs/en/settings, "Settings precedence" and
"Lists merge instead of overriding"). It is also the scope where a
`defaultMode` of `auto` or `bypassPermissions` takes effect, which project
and local settings do not allow.

Under `session_home: host` the config dir is your own `~/.claude`, and
ctxloom never writes your `settings.json`. There the block is the
`permissions` of `ctxloom-hooks.json` instead, the file handed with
`--settings` (which outranks your user and project files), with the cost the
session home avoids: an invalid value in the block disables ctxloom's hooks
too.

An agent that writes no block for claude-code runs on ctxloom's default
document, `{defaultMode: acceptEdits}`. A block you write replaces it whole:
if you write rules and no `defaultMode`, claude starts in its own default
mode (Manual under `claude -p`, so a child's edits that no rule allows are
denied).

### How it combines with a profile's `deny_tools`

A profile's `deny_tools` is not part of this block. ctxloom writes it, as
`permissions.deny`, into the project's own `.claude/settings.json` when a
surface is applied. The agent's block is in the session's user-scope file.
claude reads both, and combines them by its own rules: the lists (`allow`,
`ask`, `deny`, ...) of the two files are merged, never one replacing the
other, so a tool either file denies stays denied; for a single value the
project's file wins over user scope. ctxloom does not merge the two itself.

## opencode

The block is opencode's `permission` config object, as opencode's
documentation describes it: https://opencode.ai/docs/permissions/

    agents:
      fixer:
        engine: opencode
        engines:
          opencode:
            permissions:
              edit: ask
              bash:
                "*": ask
                "git status *": allow
              webfetch: deny

(opencode 1.18.4 reads this keyed `permission` object and converts it to its
newer `permissions` rule list itself.) An agent that writes no block for
opencode gives it no `permission` key: opencode's own defaults apply.

ctxloom does not choose opencode's agent (`build` or `plan`); there is no
flag for it yet.

## Migrating an older config

These are hard migrations: the old keys are unknown keys and the config is
refused (exit 48) until they are moved.

- `agents.<n>.engines.claude-code.permissions.mode: X` becomes
  `agents.<n>.engines.claude-code.permissions.defaultMode: X`. A block that
  still says `mode:` is not refused: it is passed to claude as written.
  claude documents no `permissions.mode` key, so it does not set the mode;
  how claude reports the stray key (a settings warning, or nothing) is
  claude's, and has not been verified against a live claude.
- opencode's `mode: build|plan|auto` has no replacement in the block: it was
  never passed to opencode. Write opencode's own `permission` rules instead.
- `permissions.approval_timeout`, at the project level and on an agent, is
  deleted. Its replacement is the engine block's `approval_timeout` (above),
  which is the engine's, never an agent's.
- `permissions.approver`, `permissions.sandbox` and `permissions.network`, at
  the project level and on an agent, are deleted, with no replacement in
  ctxloom. Say the intent in the engine's own words instead:
  - who answers a prompt: claude's `defaultMode` (`dontAsk` denies what
    would prompt, `auto` lets its classifier decide) and
    `permissions.denyInsteadOfPrompting`; opencode's `ask`/`deny` effects;
  - a filesystem or network fence: claude's sandbox settings
    (https://code.claude.com/docs/en/sandboxing), a top-level `sandbox`
    object beside `permissions`, which needs a sibling block of its own in
    ctxloom: a follow-up, not built yet. opencode documents no sandbox.
  - process isolation stays ctxloom's own, engine-neutral `runtime`.
