# world: base
# NOT ported: nothing; the noun is new.

@doc
Feature: trust — the user's verdict on a project's own settings

  A project layer may say things that reach past the project: the program
  that runs (`engines.<e>.command`), a credentials file
  (`engines.<e>.auth.file`), running in the user's own home
  (`engines.<e>.session_home: host`, `agents.<a>.session_home: host`), and
  a project `ctxloom` bundle that replaces the prompts ctxloom's own work
  runs on (its setup interview, its distill prompt). The daemon holds each
  back until the user trusts the project: a read (`engine show`, `config
  show`, a dry run's plan, any of the library's) answers as if the setting
  were not there and says which it held back, as the notice
  `library-application-notice-untrusted-setting`; a launch that reads one is
  refused, `runs-untrusted-project-settings`, naming each and `ctxloom
  trust` as the fix (a setting the launch does not read, another engine's
  or another agent's, refuses nothing); a project `ctxloom` bundle is
  passed over for the built-in prompts, with a notice; and a child launched
  to work in another project (`session launch --project`, the
  `child_launch` tool's `project`) is refused, `sessions-untrusted-target`,
  until the user trusts that project.

  Surfaces covered here:

    ctxloom trust [--yes]           (preview, then trust what was shown)
    ctxloom trust --undo [--yes]
    ctxloom trust list

  A project's trust is `projects/<harp>/trust`, under the project's harp.
  It is recorded with the user's name and the time; a trust already given
  records nothing more.

  NOT HELD BACK (an accepted cost): what else a project's content can bring
  with it, its bundles' hooks and MCP servers, its companions,
  `editor.command` and its env names, applies without trust.

  THE HUMAN IS ASKED. Trust is a command of the trust aggregate, sent through
  the daemon's command coordinator: the user's consent to a project's
  settings, asked of the human at her terminal (as with direnv or a workspace
  trust prompt). ctxloom gives agents no trust tool; on the host it cannot
  tell the human from her own processes (one principal, the user).

  CONFIRMATION. Trust is previewed first (the project's trust resource,
  naming what it would allow, and a tag of what it showed): at a terminal
  the CLI asks, off one it prints the preview, changes nothing and exits 0
  without `--yes` (1 with `--detailed-exitcode`). The grant names the tag,
  so a change between the preview and the answer is refused as stale.

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
        | setting                                        | path                             |
        | engines.claude-code.command: /opt/their-claude | engines.claude-code.command      |
        | engines.claude-code.auth.file: ~/their-login   | engines.claude-code.auth.file    |
        | engines.claude-code.session_home: host         | engines.claude-code.session_home |
        | agents.default.session_home: host              | agents.default.session_home      |

    Scenario Outline: Every launch the daemon makes is refused alike
      # new: the terminal session's and a child's launch read trust too.
      Given an initialized ctxloom project whose project config says "engines.claude-code.command: /opt/their-claude"
      And Alice has exported her assistant's token
      And a session "amber-quiet-heron" that is still running
      When I run '<command>'
      Then the command exits with code 20
      And the refusal is "runs-untrusted-project-settings"
      And the output contains "engines.claude-code.command"

      Examples:
        | command                                                                               |
        | ctxloom run                                                                           |
        | ctxloom run --dry-run                                                                 |
        | ctxloom session launch amber-quiet-heron --name brisk-copper-moth "review the change" |

    Scenario: A read holds an untrusted project's own setting back and says so, and shows it once the project is trusted
      # new: step 10 U7 (D7), trust gates every daemon read; ruling 46's strip-and-notice is gone.
      Given an initialized ctxloom project whose project config says "engines.claude-code.command: /opt/their-claude"
      When I run "ctxloom engine show claude-code --format json"
      Then the command succeeds
      And the output reports "command.value" as "claude"
      And the output reports "command.source" as "default"
      And the output contains "library-application-notice-untrusted-setting"
      And the output contains "ctxloom trust"
      When I run "ctxloom trust --yes"
      And I run "ctxloom engine show claude-code --format json"
      Then the command succeeds
      And the output reports "command.value" as "/opt/their-claude"
      And the output reports "command.source" as "engine"
      And the output does not contain "library-application-notice-untrusted-setting"
      When I run "ctxloom trust --undo --yes"
      And I run "ctxloom engine show claude-code --format json"
      Then the command succeeds
      And the output reports "command.value" as "claude"

    Scenario: An untrusted setting the launch does not read refuses nothing
      # new: only the keys this launch reads are judged.
      Given an initialized ctxloom project whose project config says "engines.opencode.command: /opt/their-opencode"
      And Alice has exported her assistant's token
      When I run 'ctxloom run --one-shot "hello"'
      Then the command succeeds
      And the assistant's reply is printed

    Scenario: Beside another refusal, the untrusted settings are one refusal of the launch's
      # new: the check is the last of the launch's checks.
      Given an initialized ctxloom project whose project config says "engines.claude-code.command: /opt/their-claude"
      And Alice has exported her assistant's token
      When I run 'ctxloom run --one-shot ""'
      Then the command exits with code 20
      And the refusal is "runs-launch-refused"
      And the output contains "runs-no-prompt"
      And the output contains "runs-untrusted-project-settings"

    Scenario: An untrusted project bundle named ctxloom is passed over for the built-in setup prompt
      # new: the prompt override, gated by trust.
      Given an initialized ctxloom project
      And the project already has the file ".ctxloom/content/bundles/v2/ctxloom/bundle.yaml":
        """
        schema_version: 4
        version: 9.9.9
        """
      And the project already has the file ".ctxloom/content/bundles/v2/ctxloom/prompts/setup-interview.md":
        """
        OUR-OWN-SETUP-INTERVIEW
        """
      When I run "ctxloom init prompt"
      Then the command succeeds
      And the output contains "Phase 1 — Open, orient, scan"
      And the output does not contain "OUR-OWN-SETUP-INTERVIEW"
      And the output contains "library-application-notice-prompt-untrusted"
      And the output contains "ctxloom trust"

    Scenario: An untrusted project bundle named ctxloom is passed over for the built-in distill prompt
      # new: the pass-over covers every operational prompt.
      Given an initialized ctxloom project
      And the project already has the file ".ctxloom/content/bundles/v2/ctxloom/bundle.yaml":
        """
        schema_version: 4
        version: 9.9.9
        """
      And the project already has the file ".ctxloom/content/bundles/v2/ctxloom/prompts/distill.md":
        """
        OUR-OWN-DISTILL-PROMPT
        """
      And the mock LLM responds "DISTILLED-BY-THE-CONFIGURED-LLM: retries, tokens, tenants"
      And a bundle "lore" with a long fragment "rules"
      When I run "ctxloom fragment distill lore#fragments/rules -f"
      Then the command succeeds
      And the mock recorded input contains "You are a text compression assistant"
      And the mock recorded input does not contain "OUR-OWN-DISTILL-PROMPT"
      And the output contains "ctxloom trust"

  Rule: Trust is previewed, given by the user alone, and covers what was shown

    Scenario: Off a terminal, trust previews what it would allow and changes nothing
      # new: trust confirms.
      Given an initialized ctxloom project whose project config says "engines.claude-code.command: /opt/their-claude"
      When I run "ctxloom trust --format json --detailed-exitcode"
      Then the command exits with code 1
      And the output reports "settings[0].path" as "engines.claude-code.command"
      And the output reports "name" as the project's harp under "projects/", then "/trust"
      When I run "ctxloom trust list --format json"
      Then the command succeeds
      And the output reports "trusts" having "0" entries

    Scenario: Trust with --yes is recorded with the user and the time, and listed
      # new: trust is the user's act.
      Given an initialized ctxloom project whose project config says "engines.claude-code.command: /opt/their-claude"
      When I run "ctxloom trust --yes --format json"
      Then the command succeeds
      When I run "ctxloom trust list --format json"
      Then the command succeeds
      And the output reports "trusts[0].name" containing "/trust"
      And the project is listed as trusted by the user running ctxloom, with the time it was given

    Scenario: A trust whose settings changed between the preview and the yes is refused
      # new: what was seen is what is trusted.
      Given an initialized ctxloom project whose project config says "engines.claude-code.command: /opt/their-claude"
      When I run "ctxloom trust" at a terminal, and before I answer the project config is changed to say "engines.claude-code.command: /opt/another-claude", then I answer "yes"
      Then the command exits with code 20
      And the refusal is "trust-plan-stale"
      When I run "ctxloom trust list --format json"
      Then the command succeeds
      And the output reports "trusts" having "0" entries

    Scenario: Undoing trust holds the project's settings back again
      # new: trust --undo.
      Given an initialized ctxloom project whose project config says "engines.claude-code.command: /opt/their-claude"
      And I run "ctxloom trust --yes"
      When I run "ctxloom trust --undo --yes"
      Then the command succeeds
      When I run "ctxloom trust list --format json"
      Then the command succeeds
      And the output reports "trusts" having "0" entries

  Rule: A child works in another project only once the user trusts that project

    Scenario: A child launched with --project into a project the user has not trusted is refused
      # new: a cross-repository child reads the target's trust.
      Given an initialized ctxloom project
      And Alice has exported her assistant's token
      And a session "amber-quiet-heron" that is still running
      And a second project "api" beside this one, initialized with ctxloom
      When I run 'ctxloom session launch amber-quiet-heron --name brisk-copper-moth --project ../api "review the change"'
      Then the command exits with code 20
      And the refusal is "sessions-untrusted-target"
      And the output contains "api"
      And no hand-off file was left behind

    Scenario: A child its orchestrator launches into a project the user has not trusted is refused
      # new: the child_launch tool's project reads the target's trust too.
      Given an initialized ctxloom project
      And Alice has exported her assistant's token
      And a session "amber-quiet-heron" that is still running
      And a second project "api" beside this one, initialized with ctxloom
      When the engine of "amber-quiet-heron" calls "child_launch" for "brisk-copper-moth" with project "../api" and task "review the change"
      Then the tool failed with "sessions-untrusted-target", naming "api"

    Scenario: Once the user trusts the other project, the same launch runs there
      # new: a cross-repository child reads the target's trust.
      Given an initialized ctxloom project
      And Alice has exported her assistant's token
      And a session "amber-quiet-heron" that is still running
      And a second project "api" beside this one, initialized with ctxloom
      And I run "ctxloom trust --yes" in the project "api"
      When I run 'ctxloom session launch amber-quiet-heron --name brisk-copper-moth --project ../api "review the change"'
      Then the command succeeds
      When I run "ctxloom session show brisk-copper-moth --format json" in the project "api"
      Then the command succeeds
      And the output reports "name" containing "/sessions/brisk-copper-moth"
