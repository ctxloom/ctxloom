Feature: LLM backends
  llm lists the available backends and manages the default.

  Scenario: List built-in backends
    Given an initialized ctxloom project
    When I run "ctxloom llm list"
    Then the command succeeds
    And the output contains "claude-code"

  Scenario: Show the default backend
    Given an initialized ctxloom project
    When I run "ctxloom llm default"
    Then the command succeeds

  Scenario: Set the default backend
    Given an initialized ctxloom project
    When I run "ctxloom llm default antigravity"
    Then the command succeeds
    When I run "ctxloom llm default"
    Then the output contains "antigravity"
