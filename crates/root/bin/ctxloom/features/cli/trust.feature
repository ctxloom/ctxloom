# world: base
# NOT ported: nothing; the noun is new.

@doc
Feature: trust — the user's verdict on a project's own settings, or a remote's content

  A project layer may say things that reach past the project: the program
  that runs (`engines.<e>.command`), a credentials file (`engines.<e>.auth.file`),
  running in the user's own home (`engines.<e>.session_home: host`), and a
  project `ctxloom` bundle that replaces the prompts setting ctxloom up.
  The daemon holds each back until the user trusts the project: a launch
  that needs one is refused, `runs-untrusted-project-settings`, naming each
  and `ctxloom trust` as the fix; a project `ctxloom` bundle is passed over
  for the built-in prompts, with a notice. A remote's content is trusted the
  same way.

  Surfaces covered here:

    ctxloom trust [--remote <name>] [--yes]   (preview, then trust what was shown)
    ctxloom trust --undo [--remote <name>] [--yes]
    ctxloom trust list

  A trust is recorded with the user's name and the time; a trust already
  given records nothing more.

  ONLY THE USER. Trust is a command of the trust aggregate, sent through the
  daemon's command coordinator with the user's credential; an agent's
  credential (the capture or orchestration scope) is refused, so no
  assistant can trust its own repository.

  CONFIRMATION. Trust is previewed first (the daemon's trust resource,
  naming what it would allow, and a tag of what it showed): at a terminal
  the CLI asks, off one it prints the preview, changes nothing and exits 0
  without `--yes` (1 with `--detailed-exitcode`). The grant names the tag, so a change between the preview
  and the answer is refused as stale.

  UNTIL REVOKED. A trust covers the project until the user takes it back,
  whatever its settings later say: a pull that changes them applies without
  asking again.

  Rule: The project's own settings are held back until trust is given

    Scenario Outline: A launch on an untrusted project-only setting is refused, naming it and the fix
      # new: the daemon checks trust as policy.
      Given an initialized ctxloom project whose project config says "<setting>"
      And Alice has exported her assistant's token
      When I run 'ctxloom run --one-shot "hello"'
      Then the command exits with code 20
      And the refusal is "runs-untrusted-project-settings"
      And the output contains "<path>"
      And the output contains "ctxloom trust"

      Examples:
        | setting                                       | path                         |
        | engines.claude-code.command: /opt/their-claude | engines.claude-code.command  |
        | engines.claude-code.auth.file: ~/their-login  | engines.claude-code.auth.file |
        | engines.claude-code.session_home: host        | engines.claude-code.session_home |

    Scenario: An untrusted project bundle named ctxloom is passed over for the built-in setup prompts
      # new: ruling 128's accepted cost, now gated.
      Given an initialized ctxloom project whose own "ctxloom" bundle replaces the setup prompt
      When I run "ctxloom init --format json"
      Then the command succeeds
      And the setup used the built-in prompt, and said the project's own waits for trust

  Rule: Trust is previewed, given by the user alone, and covers what was shown

    Scenario: Off a terminal, trust previews what it would allow and changes nothing
      # new: trust confirms.
      Given an initialized ctxloom project whose project config says "engines.claude-code.command: /opt/their-claude"
      When I run "ctxloom trust --format json --detailed-exitcode"
      Then the command exits with code 1
      And the output reports "settings[0].path" as "engines.claude-code.command"
      And the output reports "name" containing "trusts/project-"
      And the project is not trusted

    Scenario: Trust with --yes is recorded with the user and the time, and listed
      # new: trust is the user's act.
      Given an initialized ctxloom project whose project config says "engines.claude-code.command: /opt/their-claude"
      When I run "ctxloom trust --yes --format json"
      Then the command succeeds
      When I run "ctxloom trust list --format json"
      Then the command succeeds
      And the project is listed as trusted by the user running ctxloom, with the time it was given

    Scenario: A trust whose settings changed between the preview and the yes is refused
      # new: what was seen is what is trusted.
      Given an initialized ctxloom project whose project config says "engines.claude-code.command: /opt/their-claude"
      And the project's settings change after the preview and before the answer
      When I run "ctxloom trust --yes"
      Then the command exits with code 20
      And the refusal is "trust-plan-stale"

    Scenario: An assistant's own credential cannot trust its project
      # new: an agent never trusts.
      Given an initialized ctxloom project whose project config says "engines.claude-code.command: /opt/their-claude"
      And a session "amber-quiet-heron" that is still running
      When its assistant sends a trust of its project with its own credential
      Then the trust is refused as not the user's, and the project is not trusted

    Scenario: Undoing trust holds the project's settings back again
      # new: trust --undo.
      Given an initialized ctxloom project that the user has trusted
      When I run "ctxloom trust --undo --yes"
      Then the command succeeds
      And the project is not trusted

    Scenario: A remote's content is trusted by its name
      # new: a remote's trust.
      Given an initialized ctxloom project with a remote "acme"
      When I run "ctxloom trust --remote acme --yes --format json"
      Then the command succeeds
      And the remote "acme" is listed as trusted by the user running ctxloom
