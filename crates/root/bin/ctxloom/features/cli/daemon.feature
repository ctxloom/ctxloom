# world: base
# NOT ported: nothing; the noun is new, and Go had no daemon.

@doc
Feature: daemon — the one per-user process every ctxloom command is a client of

  Every `ctxloom` execution is a client of the user's daemon: it finds the
  daemon, or starts one, and lets it do the work. The daemon alone opens each
  project's store (`~/.ctxloom/projects/<id>/ctxloom.db`); it plans runs,
  assembles context, applies policy, hosts one-shots and children, takes
  every hook's facts and answers every read from its warm read models. The
  one thing it never does is put an engine on a terminal: the `ctxloom run`
  the user typed starts the interactive TUI, from the launch the daemon
  computed.

  Surfaces covered here:

    ctxloom daemon status            (whether one serves, where, and what it hosts)
    ctxloom daemon stop [--even-if-live]

  `ctxloom daemon serve` is what a client starts; it is hidden, as the hook
  commands are, because nobody types it.

  FINDING IT. A client reads the discovery file,
  `~/.ctxloom/daemon/daemon.endpoint` under ctxloom's home: the daemon's
  endpoint (`https://127.0.0.1:<port>`), its public certificate, the user
  bearer and its pid, written when it serves (0600, in the 0700 daemon
  directory). Whoever holds `~/.ctxloom/daemon/daemon.lock` serves; a file
  whose lock is free is stale. With none serving, the client starts
  `ctxloom daemon serve` detached, in a session of its own, its output in
  `~/.ctxloom/daemon/daemon.log`, waits for the discovery file, then calls.

  PROVING IT IS THE USER. A command presents the user bearer and pins the
  certificate; a run, a hosted child and a hook present their own narrower
  per-run credentials instead. Each daemon start mints a new certificate and
  a new bearer; the daemon keeps only the bearer's digest, and nothing but
  the discovery file holds the bearer.

  KILLED AT ANY INSTANT. Everything the daemon knows is in the stores, the
  locks and the files it owns, and every write it makes is keyed, so a
  redelivery is a duplicate. A daemon killed outright (SIGKILL) at any step
  comes back on the next call, and that call finds the record whole.

  IDLE. The daemon exits by itself once it has had no live run, no hosted
  child and no open client stream for ten minutes.

  Rule: A command finds the daemon, or starts one, and fails when it can do neither

    Scenario: A command with no daemon serving starts one, and waits for it to serve
      # new: spawn on demand.
      Given an initialized ctxloom project
      And no ctxloom daemon is serving for this home
      When I run "ctxloom session list --format json"
      Then the command succeeds
      And a ctxloom daemon serves for this home, started detached in a session of its own
      And the daemon's output goes to "~/.ctxloom/daemon/daemon.log"

    Scenario: Two commands started at once start one daemon between them
      # new: the election.
      Given an initialized ctxloom project
      And no ctxloom daemon is serving for this home
      When two commands are run at once, "ctxloom session list" and "ctxloom bundle list"
      Then both commands succeed
      And exactly one daemon served them both

    Scenario: A discovery file whose daemon is gone is replaced by the next command's daemon
      # new: a stale discovery file.
      Given an initialized ctxloom project
      And the discovery file names a daemon that no longer runs
      When I run "ctxloom session list"
      Then the command succeeds
      And a new daemon serves, and the discovery file names it

    Scenario Outline: A command that cannot start a daemon fails, and reads or writes nothing
      # new: no fallback to the store.
      Given an initialized ctxloom project
      And no ctxloom daemon can be started for this home
      When I run '<command>'
      Then the command exits with code 1
      And the output contains "no ctxloom daemon could be started"
      And the output contains "~/.ctxloom/daemon/daemon.log"
      And no project store was opened, and no engine was started

      Examples:
        | command                             |
        | ctxloom session list                |
        | ctxloom session show amber-swift-owl |
        | ctxloom bundle list                 |
        | ctxloom run --one-shot "hi"         |
        | ctxloom run                         |
        | ctxloom run --dry-run               |

    Scenario Outline: Help, a usage error and the daemon's own commands start no daemon
      # new: what needs no daemon.
      Given an initialized ctxloom project
      And no ctxloom daemon is serving for this home
      When I run '<command>'
      Then the command exits with code <code>
      And no ctxloom daemon was started

      Examples:
        | command                       | code |
        | ctxloom --help                | 0    |
        | ctxloom session --frobnicate  | 2    |
        | ctxloom daemon status         | 0    |
        | ctxloom daemon stop           | 0    |

  Rule: Only the user can find the daemon, or speak to it as her

    Scenario: The discovery file is hers alone, and holds what a client needs
      # new: the discovery file.
      Given an initialized ctxloom project
      When I run "ctxloom session list"
      Then the command succeeds
      And the discovery file is readable by its owner alone, in a directory only its owner can enter
      And the discovery file names the daemon's endpoint, its certificate, a user bearer and its pid

    Scenario: The user bearer is never shown, logged or recorded
      # new: the bearer stays in the discovery file.
      Given an initialized ctxloom project
      When I run "ctxloom daemon status --format json"
      Then the command succeeds
      And the output reports "pid" as the daemon's pid
      And the output reports "endpoint" as the discovery file's endpoint
      And neither the output, the daemon's log nor the project's store holds the user bearer

    Scenario: A call presenting a bearer the daemon did not mint is refused, saying nothing of why
      # new: the user credential.
      Given an initialized ctxloom project
      And a ctxloom daemon is serving for this home
      When a client calls the daemon with a bearer it did not mint
      Then the call is refused as unauthenticated, and the refusal names no reason

    Scenario: Each start of the daemon mints a new certificate and a new bearer
      # new: nothing is reused across starts.
      Given an initialized ctxloom project
      And a ctxloom daemon is serving for this home
      When the daemon is stopped and the next command starts another
      Then the new discovery file holds another certificate and another bearer
      And a client still holding the old bearer is refused

  Rule: daemon status and daemon stop are for when something seems wrong

    Scenario: Status with no daemon serving says so, and starts none
      # new: daemon status.
      Given an initialized ctxloom project
      And no ctxloom daemon is serving for this home
      When I run "ctxloom daemon status --format text"
      Then the command succeeds
      And the output contains "no ctxloom daemon is serving"
      And no ctxloom daemon was started

    Scenario: Status names what the daemon hosts and how long it has been idle
      # new: daemon status.
      Given an initialized ctxloom project
      And a ctxloom daemon is serving for this home, hosting a child "brisk-copper-moth"
      When I run "ctxloom daemon status --format json"
      Then the command succeeds
      And the output reports "runs[0].harp" as "brisk-copper-moth"
      And the output reports "runs[0].kind" as "child"
      And the output reports "idle_limit_seconds" as "600"

    Scenario: Stopping a daemon that hosts nothing ends it, and the next command starts another
      # new: daemon stop.
      Given an initialized ctxloom project
      And a ctxloom daemon is serving for this home
      When I run "ctxloom daemon stop"
      Then the command succeeds
      And the daemon has exited, and nothing says it serves

    Scenario: Stopping a daemon that hosts a live run is refused, naming it, unless --even-if-live
      # new: daemon stop.
      Given an initialized ctxloom project
      And a ctxloom daemon is serving for this home, hosting a child "brisk-copper-moth"
      When I run "ctxloom daemon stop"
      Then the command exits with code 2
      And the output contains "brisk-copper-moth"
      When I run "ctxloom daemon stop --even-if-live"
      Then the command succeeds
      And the daemon has exited, and nothing says it serves

  Rule: An idle daemon exits by itself

    Scenario: A daemon with nothing to do exits after its idle time
      # new: the idle exit.
      Given an initialized ctxloom project
      And a ctxloom daemon is serving for this home, and has nothing running, hosted or open
      When the daemon's idle time passes
      Then the daemon has exited, and nothing says it serves

    Scenario Outline: A daemon with something to do stays
      # new: what keeps a daemon from idling.
      Given an initialized ctxloom project
      And a ctxloom daemon is serving for this home, with <busy>
      When the daemon's idle time passes
      Then the daemon still serves

      Examples:
        | busy                                  |
        | an interactive session running        |
        | a child it hosts                      |
        | a one-shot it hosts                   |
        | a command's stream open               |

  Rule: A hook delivers to the daemon, starting one if it must, and never fails the engine

    Scenario: A hook with no daemon serving starts one and delivers its fact
      # new: a hook is a client too.
      Given an initialized ctxloom project
      And a session "brisk-copper-moth" that is still running
      And no ctxloom daemon is serving for this home
      When the session's assistant fires its UserPromptSubmit hook
      Then the hook exits 0, and says nothing was left unrecorded
      And a ctxloom daemon serves for this home, started by that hook
      And the session's transcript holds the prompt, once

    Scenario: A hook that can neither reach nor start a daemon answers the engine and says what it dropped
      # new: the hook fails open.
      Given an initialized ctxloom project
      And a session "brisk-copper-moth" that is still running
      And no ctxloom daemon can be started for this home
      When the session's assistant fires its UserPromptSubmit hook
      Then the hook exits 0, and its answer to the engine is unchanged
      And the hook warns "hook-cli-not-recorded" for the prompt, within its bound

  Rule: A daemon killed at any instant comes back on the next call, and the record is whole

    Scenario Outline: A daemon killed outright at a step comes back, and the next command finds the record whole
      # new: the crash tolerance, one step at a time.
      Given an initialized ctxloom project
      And the daemon will be killed outright <when>
      When Alice asks her assistant one thing, which the killed daemon leaves unfinished
      And I run "ctxloom session list --format json"
      Then the command succeeds
      And no run is left owed a runner, no incarnation is left open and no fact is recorded twice

      Examples:
        | when                                          |
        | after the run is enqueued                     |
        | after the session is minted                   |
        | after the engine is started                   |
        | while a turn is in flight                     |
        | after a hook's fact is recorded, before it is answered |
        | while the discovery file is written           |
