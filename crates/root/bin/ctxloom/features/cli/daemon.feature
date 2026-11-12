# world: base
# NOT ported: nothing; the noun is new, and Go had no daemon.

@doc
Feature: daemon — the one per-user process every ctxloom command is a client of

  Every `ctxloom` execution is a client of the user's daemon: it finds the
  daemon, or starts one, and lets it do the work. The daemon alone opens the
  ONE store that holds every project's sessions (`~/.ctxloom/ctxloom.db`;
  taskloom's `state.db` stays taskloom's); it plans runs,
  assembles context, applies policy, hosts one-shots and children, takes
  every hook's facts and answers every read from its warm read models. The
  one thing it never does is put an engine on a terminal: the `ctxloom run`
  the user typed starts the interactive TUI, from the launch the daemon
  computed.

  THE API IS THE COMMAND LINE. A command parses its own command line (so
  `-h`, a usage error and `daemon status|stop` need no daemon). The CLI's
  nouns are the daemon's resources and its verbs their methods: a session
  is got, listed, searched and deleted; a bundle is got, listed, created,
  updated, deleted, exported and imported. A domain write (a message sent,
  a session closed, kept or renamed, a child launched, a trust given, an
  ask answered) is the domain's own command, sent through the daemon's
  command coordinator, which waits for the read models the output is
  rendered from and returns them. The command renders what comes back, as
  text or as JSON; the work is the daemon's. Every write carries an id the
  command chose, so a command whose daemon died under it starts another and
  sends the write once more, and a write that had already applied is not
  applied twice.

  QUESTIONS ARE PART OF THE PREVIEW. A write is previewed first: the
  preview says what it would do, what a person must answer first (a
  confirmation, a choice, a value, an edited document) and a tag of what it
  was planned against. The client presents the questions (the CLI at the
  terminal, an MCP client as elicitations to its engine) and then applies,
  sending the answers and the tag; if what the preview was planned against
  has changed, the apply is refused as stale and the person previews again.
  `--yes` is one call that plans and applies. Off a terminal without
  `--yes`, the preview is printed and nothing is applied: it exits 0, or,
  with `--detailed-exitcode`, 1 when the change would do something. Nothing
  pauses a call.

  EXIT CODES. 0 succeeded (an apply, or a preview); 1 a preview that would
  change something, only with `--detailed-exitcode`; 10 failed (something
  broke: the store, an engine, the daemon could not be started); 20
  refused (the request or the state forbids it, a usage error included); 21
  fatal findings; 128 plus n, stopped by signal n.

  HARPS ARE PERMANENT. A session is named by its project's harp and its
  own, `projects/{project}/sessions/{session}`, both minted once and never
  changed; `session rename <harp> <title>` sets the free-text title shown
  beside it.

  JSON. `--format json` prints the resource the daemon answered, as its
  standard proto3 JSON, with snake_case field names. The existing features
  whose JSON assertions change with that when the daemon lands: running,
  cli/agent, auth, command, container, doctor, engine, fragment, init,
  manage, mcp, profile, run and skill; pending, cli/bundle, companion,
  config, deps, fragment_draft_premise, remote, search, session and version.

  Surfaces covered here:

    ctxloom daemon status            (whether one serves, where, and what it hosts)
    ctxloom daemon stop [--even-if-live]

  `ctxloom daemon serve` is what a client starts; it is hidden, as the hook
  commands are, because nobody types it.

  FINDING IT. A client reads the discovery file,
  `~/.ctxloom/daemon/daemon.discovery` under ctxloom's home: the daemon's
  endpoint (`https://127.0.0.1:<port>`), its public certificate, the user
  bearer and its pid, written when it serves (0600, in the 0700 daemon
  directory). Whoever holds `~/.ctxloom/daemon/daemon.lock` serves, and a
  serving daemon answers the standard gRPC health check; a file whose lock
  is free, or whose daemon does not answer, is stale. With none serving,
  the client starts
  `ctxloom daemon serve` detached, in a session of its own, its output in
  `~/.ctxloom/daemon/daemon.log`, waits for the discovery file, then calls.

  ONE PRINCIPAL, THE USER. Every caller (a command, a hook, the MCP server)
  presents the user bearer and pins the certificate. Each daemon start mints
  a new certificate and a new bearer; the daemon keeps only the bearer's
  digest, and nothing but the discovery file holds the bearer. The bearer
  and the pinned certificate keep NON-USER callers off the socket: no other
  user, web page, container or machine can speak to the daemon. Nothing
  separates the user's own processes: an assistant running as her can read
  the discovery file, and with it do anything she can. A call names its run
  and session as plain fields, an address and never an identity.

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
      Then the command exits with code 10
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
        | ctxloom session --frobnicate  | 20   |
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
      And the output reports "runs[0].kind" as "RUN_KIND_CHILD"
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
      Then the command exits with code 20
      And the output contains "brisk-copper-moth"
      When I run "ctxloom daemon stop --even-if-live"
      Then the command succeeds
      And the hosted engines were ended cleanly before the daemon exited
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
        | a session's frontend subscribed       |

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

    Scenario: A child its killed daemon hosted is started again as the next incarnation of its session
      # new: a re-host after a kill.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" that is in the middle of a turn
      And the daemon will be killed outright while a turn is in flight
      When I run "ctxloom session list"
      Then the command succeeds
      And the run the killed daemon hosted ended lost, and its turn in flight ended failed, with its owner told
      And "brisk-copper-moth" runs again as its session's next incarnation, in its own conversation
      And its incarnation was recorded with the cause "rehost"

    Scenario: A child re-hosted past its budget ends lost, and its owner is told
      # new: re-hosting is budgeted, 60 per child per hour.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" that has been re-hosted 60 times in the last hour
      And the daemon will be killed outright while a turn is in flight
      When I run "ctxloom session list"
      Then the command succeeds
      And "brisk-copper-moth" was not re-hosted again, and ended runner-lost
      And "amber-quiet-heron" was told that its child ended

    Scenario: Every launch and re-host writes the session's settings again from the user's config
      # new: an agent's edit to its own settings never survives.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" whose session settings its agent has edited
      And the daemon will be killed outright while a turn is in flight
      When I run "ctxloom session list"
      Then the command succeeds
      And the re-hosted child's session settings are the ones the user's config makes, and its agent's edit is gone

  Rule: A write is sent once more to a new daemon, and is applied once

    Scenario Outline: A write whose daemon is killed under it is retried once on a new daemon, and applied once
      # new: every write carries the id its command chose.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" that is open and idle
      And the daemon will be killed outright after the write is applied, before it is answered
      When I run '<write>'
      Then the command succeeds
      And the command started a new daemon and sent the write once more, under the same id
      And the write was applied once, and its second delivery was a duplicate

      Examples:
        | write                                                       |
        | ctxloom session send brisk-copper-moth wake "check the tests" |
        | ctxloom session close brisk-copper-moth                     |
        | ctxloom bundle create notes --description "my notes"        |

  Rule: The preview carries the questions, and the apply answers them

    Scenario Outline: Off a terminal, a preview applies nothing, and says with --detailed-exitcode whether it would change something
      # new: the preview's exit codes.
      Given an initialized ctxloom project
      And a finished session "amber-swift-owl" whose transcript retention has passed
      And a bundle "notes" exists
      When I run '<command>'
      Then the command exits with code <code>
      And nothing was applied, and the daemon answered the preview

      Examples:
        | command                                                 | code |
        | ctxloom session remove amber-swift-owl                  | 0    |
        | ctxloom session remove amber-swift-owl --detailed-exitcode | 1 |
        | ctxloom bundle remove notes --detailed-exitcode         | 1    |
        | ctxloom session remove nobody-ever-ran --detailed-exitcode | 20 |

    Scenario: At her terminal, the CLI asks Alice the preview's confirmation, and applies her answer
      # new: a client with no elicitation presents the needs itself.
      Given an initialized ctxloom project
      And a finished session "amber-swift-owl" whose transcript retention has passed
      When Alice removes it at her terminal, and answers yes when asked:
        """
        ctxloom session remove amber-swift-owl
        """
      Then the daemon's confirmation was asked at her terminal, and the session is gone
      And the removal was previewed first, and applied with her answer and the preview's tag

    Scenario: A client that supports elicitation passes the preview's questions through to its engine
      # new: an MCP client turns needs into elicitations.
      Given an initialized ctxloom project
      And a session "amber-quiet-heron" that is still running
      When its orchestrator calls a ctxloom tool whose daemon call asks for confirmation
      Then the question reached its engine as an MCP elicitation, and the engine's answer reached the daemon

    Scenario: A change between the preview and the answer is refused as stale, and previewed again
      # new: what was shown is what is applied.
      Given an initialized ctxloom project
      And a bundle "notes" exists
      And the bundle "notes" changes after its removal is previewed and before Alice answers
      When Alice removes it at her terminal, and answers yes when asked:
        """
        ctxloom bundle remove notes
        """
      Then the apply was refused as stale, and the CLI previewed the removal again

  Rule: A harp is permanent; a title is what changes

    Scenario: A rename sets the session's title, and leaves its harp and its name as they were
      # new: harps never change.
      Given an initialized ctxloom project
      And a finished session "amber-swift-owl" with a transcript
      When I run 'ctxloom session rename amber-swift-owl "the auth refactor"'
      Then the command succeeds
      When I run "ctxloom session show amber-swift-owl --format json"
      Then the command succeeds
      And the output reports "title" as "the auth refactor"
      And the output reports "name" containing "/sessions/amber-swift-owl"
      And the session's directory, lock and output folder were not moved

    Scenario: A session's name is its project's harp and its own
      # new: six words.
      Given an initialized ctxloom project with a recorded session "amber-swift-owl"
      When I run "ctxloom session list --format json"
      Then the command succeeds
      And the output reports "sessions[0].name" as the project's harp and the session's, "projects/<project harp>/sessions/amber-swift-owl"

  Rule: One store holds every project's sessions

    Scenario: Two projects' sessions are kept in the one store, each listed under its own project
      # new: one per-user store.
      Given an initialized ctxloom project with a recorded session "amber-swift-owl"
      And a second initialized project with a recorded session "brisk-copper-moth"
      When I run "ctxloom session list --format json"
      Then the command succeeds
      And the output reports "sessions[0].name" containing "/sessions/amber-swift-owl"
      And the output does not contain "brisk-copper-moth"
      And both sessions are in the one store under ctxloom's home, and no project has a store of its own

    Scenario: A store of one project's own, from before, is refused with the fix to remove it
      # new: no import of the per-project stores.
      Given an initialized ctxloom project
      And the project has a store of its own from before the one store
      When I run "ctxloom session list"
      Then the command exits with code 20
      And the output contains "projects/"
      And the output contains "remove"
      And nothing was imported from it
