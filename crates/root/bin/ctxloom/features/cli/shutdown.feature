# world: base
# NOT ported: nothing; the noun is new (owner, 2026-11-15, OD-62), and Go had no daemon.

Feature: shutdown — stop every running ctxloom of this user, as an installer must before it replaces the binary

  One ctxloom version runs at a time. `ctxloom shutdown` drains the user's
  daemon within a time, then asks what still runs to stop: each live
  interactive session's frontend (which outlives its daemon) and the daemon
  itself. Nothing is killed; what will not stop is said.

    ctxloom shutdown [<duration> | now]

  Rule: A shutdown drains within the time given, else the configured one, then stops what remains

    Scenario: With nothing running, a shutdown says so and starts nothing
      # new: OD-62/OD-63, `ctxloom shutdown` and the SIGTERM drain (owner, 2026-11-15).
      Given an initialized ctxloom project
      And no ctxloom daemon is serving for this home
      When I run "ctxloom shutdown now"
      Then the command succeeds
      And the output contains "nothing of ctxloom is running"
      And no ctxloom daemon was started

    Scenario: A shutdown with a time drains within it, and the next daemon resumes what was handed over
      # new: OD-62/OD-63, `ctxloom shutdown` and the SIGTERM drain (owner, 2026-11-15).
      Given a ctxloom daemon is serving for this home, hosting a child "brisk-copper-moth" that does not end its turn
      When I run "ctxloom shutdown 2s"
      Then the command succeeds
      And after two seconds the child's turn in flight was cut and the daemon has exited
      And the next daemon hosts the child again, resuming its own conversation

    Scenario: A bare shutdown drains within the configured daemon.drain_timeout
      # new: OD-62/OD-63, `ctxloom shutdown` and the SIGTERM drain (owner, 2026-11-15).
      Given the home config says "daemon.drain_timeout: 1s"
      And a ctxloom daemon is serving for this home, hosting a child "brisk-copper-moth" that does not end its turn
      When I run "ctxloom shutdown"
      Then the command succeeds
      And after one second the child's turn in flight was cut and the daemon has exited

    Scenario: A shutdown now stops everything at once, its engines asked to stop
      # new: OD-62/OD-63, `ctxloom shutdown` and the SIGTERM drain (owner, 2026-11-15).
      Given an initialized ctxloom project
      And a ctxloom daemon is serving for this home, hosting a child "brisk-copper-moth"
      When I run "ctxloom shutdown now"
      Then the command succeeds
      And the hosted engines were ended cleanly before the daemon exited
      And the daemon has exited, and nothing says it serves

    Scenario: A live interactive session is told at once, left to its human for the drain's time, then asked to stop
      # new: OD-62/OD-63, `ctxloom shutdown` and the SIGTERM drain (owner, 2026-11-15).
      Given an interactive session "amber-swift-owl" is running in a terminal
      When I run "ctxloom shutdown 2s"
      Then the command succeeds
      And the session's human was told at once "ctxloom is shutting down: this session will be stopped in 2s; finish and exit"
      And the output says the session's frontend still ran after the drain's 2s, and was asked to stop
      And the session's engine was stopped and its end recorded

    Scenario: A time that is no duration is refused, and nothing is asked to stop
      # new: OD-62/OD-63, `ctxloom shutdown` and the SIGTERM drain (owner, 2026-11-15).
      Given a ctxloom daemon is serving for this home
      When I run "ctxloom shutdown soon"
      Then the command exits with code 48
      And the error names "soon"
      And the daemon still serves

    Scenario: What will not stop is said, and the shutdown fails
      # new: OD-62/OD-63, `ctxloom shutdown` and the SIGTERM drain (owner, 2026-11-15).
      Given a ctxloom daemon is serving for this home that does not exit when asked
      When I run "ctxloom shutdown now"
      Then the command exits with code 32
      And the error names the daemon's pid

  Rule: A SIGTERM to the daemon is a drain within the configured time, and a draining daemon takes no new work

    Scenario: A SIGTERM drains the daemon, which refuses new invocations meanwhile
      # new: OD-62/OD-63, `ctxloom shutdown` and the SIGTERM drain (owner, 2026-11-15).
      Given the home config says "daemon.drain_timeout: 1h"
      And a ctxloom daemon is serving for this home, hosting a child "brisk-copper-moth" that does not end its turn
      When the daemon is sent SIGTERM
      Then "ctxloom daemon status --format json" reports it draining
      And "ctxloom session list" exits with code 48, naming "daemon-api-draining"

    Scenario: A changed drain timeout lands while the daemon drains, and the drain honours it
      # new: OD-62/OD-63, `ctxloom shutdown` and the SIGTERM drain (owner, 2026-11-15).
      Given the home config says "daemon.drain_timeout: 1h"
      And a ctxloom daemon is serving for this home, hosting a child "brisk-copper-moth" that does not end its turn
      When the daemon is sent SIGTERM
      And the home config is changed to say "daemon.drain_timeout: 1s"
      Then "ctxloom config show" succeeds and shows the new timeout
      And within a few seconds the child's turn in flight was cut and the daemon has exited

    Scenario: A second SIGTERM is the plain stop
      # new: OD-62/OD-63, `ctxloom shutdown` and the SIGTERM drain (owner, 2026-11-15).
      Given a ctxloom daemon is serving for this home, hosting a child "brisk-copper-moth" that does not end its turn
      When the daemon is sent SIGTERM twice
      Then the hosted engines were ended cleanly before the daemon exited

  Rule: A shutdown is done only once the holds are empty

    Scenario: A shutdown delivers what is held before it stops the daemon
      # new: owner ruling 2026-11-16 ("flush before install"): a hold an older binary wrote is delivered before the install.
      Given an initialized ctxloom project
      And a session "amber-swift-owl" whose prompt its hook held while an older daemon served
      And a ctxloom daemon of this version is serving for this home
      When I run "ctxloom shutdown now"
      Then the command succeeds
      And the output says "delivered 1 held fact"
      And nothing is held for "amber-swift-owl"
      And the session's transcript holds the prompt, once

    Scenario: Held facts no daemon can take are named, and the shutdown declines
      # new: owner ruling 2026-11-16 ("flush before install"): a hold left undelivered declines the install.
      Given an initialized ctxloom project
      And a session "amber-swift-owl" whose prompt is held
      And no ctxloom daemon is serving for this home
      When I run "ctxloom shutdown now"
      Then the command exits with code 48
      And it warns "ctxloom-wiring-shutdown-held", naming "amber-swift-owl (1 fact)"
      And no daemon was started

    Scenario: A hold's set-aside part does not block a shutdown
      # new: owner ruling 2026-11-16 ("flush before install"): a set-aside part is never delivered again.
      Given an initialized ctxloom project
      And a session's hold has only a set-aside part
      When I run "ctxloom shutdown now"
      Then the command succeeds
