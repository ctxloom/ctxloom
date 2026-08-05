Feature: LLM backends
  llm lists the available backends and manages the default.

  Scenario: List built-in backends
    Given an initialized ctxloom project
    When I run "ctxloom llm list"
    Then the command succeeds
    And the output contains "claude-code"

  # Asserting only the exit code let a show path that returned an EMPTY default
  # pass. The default a project with no llm block resolves to is a fact worth
  # stating, and it is the effect the command exists to report.
  Scenario: Show the default backend
    Given an initialized ctxloom project
    When I run "ctxloom llm default"
    Then the command succeeds
    And the output contains "claude-code"

  Scenario: Set the default backend
    Given an initialized ctxloom project
    When I run "ctxloom llm default antigravity"
    Then the command succeeds
    When I run "ctxloom llm default"
    Then the output contains "antigravity"
