# world: base
# NOT ported: nothing; the root flag is new.
@doc
Feature: the project root — `--root`, `CTXLOOM_ROOT`, and where a project is looked for

  Covers: the global `--root[=<dir>]` flag, the flag form of `CTXLOOM_ROOT`.

  Which directory is the project:

  1. `--root=<dir>` names the project's root: `<dir>/.ctxloom` is its app dir,
     whether it exists yet or not.
  2. a bare `--root` makes her home directory the project root (the explicit
     ask for a project at her home).
  3. omitted: `CTXLOOM_ROOT` when it is set (the flag, given, wins over it);
     else the walk up from the working directory to the nearest `.ctxloom`,
     which stops at the first git root. Outside git no git root stops the
     walk, and it never adopts her home's `.ctxloom` nor the daemon root's:
     they are hers, a project root only when she asks for it with `--root`
     or `CTXLOOM_ROOT`. A `.ctxloom` of a directory below her home is a
     project, in git or not.

  A directory that resolves to no project, outside git and with no explicit
  root, is not given her home as its root: `init` makes the project in the working
  directory; any other command that needs a project is refused, its fix naming
  `--root`.

  The `--home` of `config set` and `agent create|edit` is another thing, the
  home CONFIG layer, and is not this flag.

  Her daemon root (`CTXLOOM_DAEMON_ROOT`: the store, the daemon's directory) is
  another directory than her home in these scenarios (as `just install`'s
  makes it) and her home holds a `.ctxloom`. The project root and the daemon
  root are two things; neither is taken for the other.

  Rule: With no `--root` and no git, the walk never lands on her home's or the daemon root's `.ctxloom`

    Scenario: init in a fresh directory below her home makes the project there
      # new: the project root flag, owner ruling 2026-11-22.
      Given an empty project directory
      And her daemon root is elsewhere, and her home holds a ctxloom directory of her own
      And a directory "newproj" in her home
      When I run "ctxloom init --format text" in her home's directory "newproj"
      Then the command succeeds
      And the output does not contain "already exists"
      And the home's file "newproj/.ctxloom/remotes.yaml" exists
      And the home's file ".ctxloom/remotes.yaml" does not exist

    Scenario: a command that needs a project is refused there, naming `--root`
      # new: the project root flag, owner ruling 2026-11-22.
      Given an empty project directory
      And her daemon root is elsewhere, and her home holds a ctxloom directory of her own
      And a directory "newproj" in her home
      When I run "ctxloom run --one-shot hello" in her home's directory "newproj"
      Then the command exits with code 48
      And the output contains "ctxloom-compose-project-root-needed"
      And the output contains "--root"
      And the home's file ".ctxloom/sessions" does not exist

  Rule: `--root` names the project

    Scenario: a bare `--root` makes her home the project root
      # new: the project root flag, owner ruling 2026-11-22.
      Given an empty project directory
      And her daemon root is elsewhere, and her home holds a ctxloom directory of her own
      And a directory "newproj" in her home
      When I run "ctxloom init --root --format text" in her home's directory "newproj"
      Then the command succeeds
      And the output contains "ctxloom directory already exists"
      And the home's file "newproj/.ctxloom/remotes.yaml" does not exist

    Scenario: `--root=<dir>` names the project directory, wherever she stands
      # new: the project root flag, owner ruling 2026-11-22.
      Given an empty project directory
      And her daemon root is elsewhere, and her home holds a ctxloom directory of her own
      And a directory "newproj" in her home
      When I run "ctxloom init --root=$PROJECT_DIR --format text" in her home's directory "newproj"
      Then the command succeeds
      And the file ".ctxloom/remotes.yaml" exists
      And the home's file "newproj/.ctxloom/remotes.yaml" does not exist
      And the home's file ".ctxloom/remotes.yaml" does not exist

    Scenario: `CTXLOOM_ROOT` names the project when the flag is omitted
      # new: the project root flag, owner ruling 2026-11-22.
      Given an empty project directory
      And her daemon root is elsewhere, and her home holds a ctxloom directory of her own
      And a directory "newproj" in her home
      And the environment variable "CTXLOOM_ROOT" names the project directory
      When I run "ctxloom init --format text" in her home's directory "newproj"
      Then the command succeeds
      And the file ".ctxloom/remotes.yaml" exists
      And the home's file "newproj/.ctxloom/remotes.yaml" does not exist

    Scenario: the flag wins over `CTXLOOM_ROOT`
      # new: the project root flag, owner ruling 2026-11-22.
      Given an empty project directory
      And her daemon root is elsewhere, and her home holds a ctxloom directory of her own
      And a directory "newproj" in her home
      And the environment variable "CTXLOOM_ROOT" names the project directory
      When I run "ctxloom init --root --format text" in her home's directory "newproj"
      Then the command succeeds
      And the output contains "ctxloom directory already exists"
      And the file ".ctxloom/remotes.yaml" does not exist

  Rule: inside git nothing changes

    Scenario: init at a git root makes the project there
      # new: the project root flag, owner ruling 2026-11-22.
      Given an empty project directory that is a git repository
      When I run "ctxloom init --format text"
      Then the command succeeds
      And the file ".ctxloom/remotes.yaml" exists

  Rule: `--daemon-root` names the daemon root, never the project

    Scenario: `--daemon-root=<dir>` puts the store there, whatever her home holds
      # new: the daemon root flag, owner ruling 2026-11-23.
      Given an empty project directory that is a git repository
      When I run "ctxloom init --daemon-root=elsewhere --format text"
      Then the command succeeds
      And the file ".ctxloom/remotes.yaml" exists
      And the file "elsewhere/.ctxloom/ctxloom.db" exists
      And the home's file ".ctxloom/ctxloom.db" does not exist

    Scenario: the flag wins over `CTXLOOM_DAEMON_ROOT`
      # new: the daemon root flag, owner ruling 2026-11-23.
      Given an empty project directory that is a git repository
      And her daemon root is elsewhere, and her home holds a ctxloom directory of her own
      When I run "ctxloom init --daemon-root=flagged --format text"
      Then the command succeeds
      And the file "flagged/.ctxloom/ctxloom.db" exists
      And the home's file "../daemon-root/.ctxloom/ctxloom.db" does not exist
