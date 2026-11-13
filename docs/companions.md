# Companions

A companion is a program ctxloom runs to ask what it contributes to a session.
ctxloom runs only the companions you register, and you register one by NAME
alone (OD-10): never a path or a command, so the registration holds wherever
the program is installed (a container's paths are not the host's). Its program
is found where the agent runs, on that place's `PATH`, every time it is asked:
the shipped companions `ltk`, `taskloom` and `reprise` are their own
programs, and any other name `<n>` is the program `ctxloom-companion-<n>`. A
program of that name earlier on the `PATH` is the one that runs. Nothing else
on the `PATH` is ever run for being there.

## Registering one

    ctxloom companion add acme      # finds ctxloom-companion-acme on PATH

ctxloom finds the program and runs `<program> loadout --format yaml` once, in
the project's root with your environment, and records the name only if it
exits successfully and prints a loadout. A name whose program is not on the
`PATH` is refused, naming the program it looked for.

    ctxloom companion list            # name, program, whether it is on PATH
    ctxloom companion disable acme    # stays registered, contributes nothing
    ctxloom companion enable acme
    ctxloom companion remove acme --yes

`companion list` only looks for each program; it never runs a companion.
`companion remove` without `--yes` says what it would do. `enable` and
`disable` of a companion already so write nothing.

The registration lives in your home config (`~/.ctxloom/config.yaml`); a
project's config may add names, and cannot remove one the home registers.
`ctxloom doctor` asks each enabled registered companion for its loadout and
says which answered.

## The config shape (schema 9)

    schema_version: 9
    companions:
      acme:
        state: enabled
      ltk:
        state: disabled

Names are `^[A-Za-z0-9][A-Za-z0-9._-]*$`. `state` is a word (OD-9):
`enabled`, the default, or `disabled`. A name's state is the highest layer's
that says one. Anything else in a registration is an unknown key, refused: the
`command:` an earlier build recorded is gone (a hard migrate; register the
name again).

### Go compatibility

Go ctxloom reads schema 8 only, where `companions` is a list of bare names
(`companions: [ltk]`). This build reads such a list (schema 7 or 8) as the
names it registers, each enabled, in memory; the file is not rewritten until
a companion write replaces the section.

## What a companion contributes

A companion's `loadout --format yaml` prints one document with a `run:`
section (a bundle, consumed every session) and an `init:` section (typed
fields consumed once at setup); either may be omitted, a document with
neither is refused, and a key the document does not model is refused.

This build delivers the `run:` bundle's fragments, in every assembly
(`materialize`, `context assemble`, `run`): after the profile-selected
fragments, companions by name and fragments by name, under the reference
`ctxloom+companion:<name>#fragments/<fragment>`. A fragment with a `premise`
is held back from the context and offered in the premise index, as any
fragment is. A companion fragment that duplicates one already assembled is
assembled once. The loadout is fetched fresh at each command, under a
3-second limit. A disabled companion is not asked. An enabled one whose
program is not on the `PATH` costs only what it would have brought: the
session starts, and a notice names the companion and the program that would
bring its guidance.

It also delivers the bundle's hooks, commands and MCP servers to the engine's
native files (`materialize`, and the hook listing), by the same path
a bundle's take. All are unconditional: not chosen by a profile, and a
profile's curation of commands does not remove a companion's.

- **Hooks** follow the profiles' own and precede the profiles' bundles' (then
  ctxloom's own callbacks), companions by name (Go's `ResolveBundleHooksFor`).
  A `pre_tool` hook may declare `tool: shell` or `tool: file_edit`; the engine
  maps the class to its own tool names (claude: `Bash|PowerShell` and
  `Edit|Write|MultiEdit|NotebookEdit`).
- **Commands** are named `<companion>/<command>`, companions by name.
- **MCP servers** claim their names before the profiles' bundles do; a
  profile bundle declaring a name a companion holds is refused as any contest
  for a name is, and `exclude_mcp:` removes a companion's server too.
- **Skills** are not delivered: a loadout is one document, and a document
  carries no skill files (only a tree-form bundle does), so there is nothing
  of a skill to deliver. Go delivers none either.

`--no-companions` delivers none of it. The `init:` section is parsed and not
yet used.

### When a companion fails

A companion never stops a command. The command still runs, and one warning
says why that companion contributed nothing:

| outcome | what happened | warning |
|---|---|---|
| program not found | the command's program does not exist | `companion-not-on-path` |
| exited with a status | it ran and offers no loadout (an older companion) | none |
| probe failed | killed by a signal, timed out, could not be started, or printed nothing | `loadout-probe-failed` |
| unparseable | it printed a loadout that does not parse | `loadout-unparseable` |

Warnings are warnings under `--strict`-style runs too.

### Turning companions off

`--no-companions`, or `CTXLOOM_NO_COMPANIONS` set to `1`, `t`, `true`, `y`,
`yes` or `on` (any case), runs no companion. This build treats the flag and
the variable as an OR; Go lets an explicitly given flag win in either
direction.
