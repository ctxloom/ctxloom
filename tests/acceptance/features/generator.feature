Feature: Generator management
  As a user
  I want to manage context generators
  So that I can include dynamic context in AI interactions

  Background:
    Given a project with scm initialized

  # ============================================================================
  # Generator List
  # ============================================================================

  Scenario: List generators shows built-in generators
    When I run scm "generator list"
    Then the exit code should be 0
    And the output should contain "Built-in"
    And the output should contain "simple"

  # ============================================================================
  # Generator Run (Built-in)
  # ============================================================================

  Scenario: Run nonexistent generator fails
    When I run scm "generator run nonexistent"
    Then the exit code should be 1
    And the output should contain "not found"

  # ============================================================================
  # Generator Alias
  # ============================================================================

  Scenario: Use gen alias for generator list
    When I run scm "gen list"
    Then the exit code should be 0
    And the output should contain "Built-in"
    And the output should contain "simple"
