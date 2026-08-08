Feature: Adopt without regret — the first hour, and the exit verified before committing

  Journey U1 (FLOWS-UNIFIED.md §3). Alice has a live repo and five assistants
  configured five different ways. She wires ctxloom in, gets suspicious ten
  minutes later that it did anything at all — this codebase's characteristic
  bug is exit-0-with-zero-bytes — and satisfies herself two ways: by reading
  back what landed on disk, and by verifying she can LEAVE. The back-out is
  the half J1/J1b never covered: `manage install` had scenarios, `manage
  uninstall` had one line asserting a single absent string.

  Every assertion below is on the PAYLOAD — the bytes in the file, the words
  in the report — never on an exit code alone. An exit code cannot tell
  "it wired the harness" apart from "cobra printed help", which is exactly
  how tests/integration/cli_test.go's TestConfig_Show and TestConfig_Get pass
  today while driving `manage config`, a command that does not exist.

  # Nothing is written until the arguments are understood. This is the
  # scenario FLOWS-UNIFIED.md §4 finding class (b) predicted would fail
  # ("`manage install --engine <bogus>` exits 0 without validating") — it
  # does NOT: validation is present and loud. Kept as the regression gate for
  # it, and the doc's claim is stale, not the code.
  #
  # BREAK-POINT: the payload half is the load-bearing half. Delete the
  # refusal and the first three assertions still pass on a wrong-but-loud
  # message; only "the file .ctxloom/config.yaml does not exist" catches an
  # install that scaffolded a project around an engine nobody supports.
  Scenario: A misspelled engine is refused by name, and nothing is scaffolded around it
    Given an empty project directory
    When I run "ctxloom manage install --engine totally-bogus-engine"
    Then the command fails
    And the output contains "unknown engine"
    And the output contains "totally-bogus-engine"
    And the output contains "claude-code"
    And the file ".ctxloom/config.yaml" does not exist
    And the file ".mcp.json" does not exist

  # "Ten minutes in she's suspicious it did anything at all." The answer is
  # not `manage status`'s prose — it is the three surfaces themselves. Each
  # assertion names a DIFFERENT file, so a partial install (the shape that
  # exits 0 having written one of three) cannot pass by satisfying the
  # others.
  #
  # The settings.json assertion is a REGEX, not a substring, because the two
  # things ctxloom writes there overlap textually. This used to read
  # `contains "ctxloom hook"`, which the statusLine command
  # (`<abs>/ctxloom hook hud`) satisfies on its own — the SessionStart hook's
  # own command is `'<abs>/ctxloom' hook inject-context ...`, with a quote
  # between the two words, so it never matched the hook at all and deleting
  # the hook outright left this scenario green. Naming the SessionStart key
  # and the inject-context leaf together is what makes the claim checkable.
  Scenario: What ctxloom reports it wired is what is actually on disk
    Given an empty project directory
    When I run "ctxloom manage install --engine claude-code"
    Then the command succeeds
    And the file ".ctxloom/config.yaml" is valid YAML
    And the file ".claude/settings.json" matches "(?s)\"SessionStart\".*\"command\": \"[^\"]*' hook inject-context"
    And the file ".mcp.json" contains "ctxloom"
    And the file ".gitignore" contains ".ctxloom/ephemeral/"
    When I run "ctxloom manage status"
    Then the command succeeds
    And the output contains "claude-code"

  # The deterministic health check, on the harness the scenario above just
  # verified byte-wise. doctor's value to Alice is that it is deterministic —
  # it needs no network and no engine — so it is the one thing she can run on
  # a train. Asserted on the shared DOCTOR-CHECK-* marker vocabulary
  # doctor.feature already established, not on a human-readable summary line.
  Scenario: Alice can check the harness deterministically, with no network and no engine
    Given an empty project directory
    When I run "ctxloom manage install --engine claude-code"
    Then the command succeeds
    When I run "ctxloom doctor"
    Then the output contains "DOCTOR-CHECK"

  # THE EXIT, VERIFIED BEFORE COMMITTING — U1's closing beat and the half
  # this suite was missing. `manage uninstall` must remove what `manage
  # install` wired and keep what Alice authored, and BOTH halves are asserted
  # on payload: the hook command string is gone from settings.json, the MCP
  # registration is gone from .mcp.json, and .ctxloom survives.
  #
  # BREAK-POINT: make uninstall a no-op that prints its success line and the
  # "does not contain" assertions go red; make it delete .ctxloom and the
  # survival assertion goes red. The success message alone distinguishes
  # neither.
  #
  # Both the precondition and the removal check name the hook and the
  # statusLine SEPARATELY. "ctxloom hook" alone reaches only the statusLine
  # (see the regex note above), so on its own it certified neither half: as a
  # precondition it passed with no SessionStart hook ever written, and as a
  # removal check it passed on an uninstall that dropped the statusLine and
  # left the hook behind.
  Scenario: Alice verifies she can leave before she commits
    Given an empty project directory
    When I run "ctxloom manage install --engine claude-code"
    Then the command succeeds
    And the file ".claude/settings.json" contains "hook inject-context"
    And the file ".claude/settings.json" contains "ctxloom hook hud"
    And the file ".mcp.json" contains "ctxloom"
    When I run "ctxloom manage uninstall"
    Then the command succeeds
    And the file ".claude/settings.json" does not contain "hook inject-context"
    And the file ".claude/settings.json" does not contain "ctxloom hook"
    And the file ".mcp.json" does not contain "ctxloom hook"
    And the file ".ctxloom/config.yaml" exists

  # Back to: tests/acceptance/features/j1_setup.feature (the setup half this
  # journey extends) and j4_onboarding.feature (U3, the next hire's day one).
