# Permissions

An agent's permissions are written **per engine**, in that **engine's own
words**, and ctxloom passes them on as a **verbatim copy**. ctxloom does
**not check** what the block says: a key it does not know, a value the
engine does not take, or a typo reaches the engine as written, and it is the
engine that accepts, warns about or ignores it (owner rulings 133-139).

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
| claude-code | the `permissions` object of the user-scope `settings.json` of the session's own claude config dir (the session home, which ctxloom sets as `CLAUDE_CONFIG_DIR`): a file that holds the block and nothing else, apart from `ctxloom-hooks.json`, ctxloom's hooks, which claude is handed with `--settings` |
| opencode | the `permission` object of the `opencode.json` ctxloom writes in the session home (`OPENCODE_CONFIG_DIR`) |

ctxloom's own keys are never overwritten by the block: the block becomes the
one key, `permissions` or `permission`, and nothing else.

**A bad value disables the block alone.** claude refuses a settings file
whole when one value in it is invalid ("These settings files did not load,
so nothing in them is in effect, including hooks and permission rules"). The
block is therefore in a file of its own: an invalid value in it turns off
your permissions, and claude says so naming that file, while ctxloom's hooks
(and so the session's transcript) still load (owner ruling 145). A key claude
does not know, alone, is tolerated.

These files are in the session's own directory, where the agent's own file
tools can reach them, so ctxloom writes each one again, whole, from your
config at every launch (a new session, a child's turn, a resume). An edit the
agent makes to its own permissions never carries into a launch.

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
refused (exit 2) until they are moved.

- `agents.<n>.engines.claude-code.permissions.mode: X` becomes
  `agents.<n>.engines.claude-code.permissions.defaultMode: X`. A block that
  still says `mode:` is not refused: it is passed to claude as written.
  claude documents no `permissions.mode` key, so it does not set the mode;
  how claude reports the stray key (a settings warning, or nothing) is
  claude's, and has not been verified against a live claude.
- opencode's `mode: build|plan|auto` has no replacement in the block: it was
  never passed to opencode. Write opencode's own `permission` rules instead.
- `permissions.approver`, `permissions.approval_timeout`,
  `permissions.sandbox` and `permissions.network`, at the project level and
  on an agent, are deleted, with no replacement in ctxloom. Say the intent in
  the engine's own words instead:
  - who answers a prompt: claude's `defaultMode` (`dontAsk` denies what
    would prompt, `auto` lets its classifier decide) and
    `permissions.denyInsteadOfPrompting`; opencode's `ask`/`deny` effects;
  - a filesystem or network fence: claude's sandbox settings
    (https://code.claude.com/docs/en/sandboxing), a top-level `sandbox`
    object beside `permissions`, which needs a sibling block of its own in
    ctxloom: a follow-up, not built yet. opencode documents no sandbox.
  - process isolation stays ctxloom's own, engine-neutral `runtime`.
