Feature: Version and completion
  Smoke coverage for the always-scriptable utility commands.

  # The version this binary was stamped with is not knowable to the suite (the
  # CLI is stamped by ldflags at build time, the test process is not), so the
  # assertion is the strongest one that IS available: the printed string is
  # version-shaped, and `ctxloom version`, `ctxloom --format json version` and
  # `ctxloom --version` all report the SAME string. This replaces `the output
  # matches "."` — one arbitrary character, which the literal
  # "MUTATION-not-the-version" satisfies exactly as well as the truth does.
  Scenario: Print the version
    Given an initialized ctxloom project
    Then every version surface reports the same version-shaped string

  Scenario: Generate a shell completion script
    Given an initialized ctxloom project
    When I run "ctxloom completion bash"
    Then the command succeeds
    And the output contains "complete"
