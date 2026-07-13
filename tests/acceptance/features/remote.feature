Feature: Remote registry
  remote manages the registry of remote sources. These are local registry
  operations (no fetch): adding, defaulting, trusting, and removing. Content
  operations that require fetchable remote content are exercised at the
  operations layer in tests/integration.

  Scenario: Add a remote and list it
    Given an initialized ctxloom project
    When I run "ctxloom remote add origin file:///tmp/acceptance-remote.git --forge git"
    Then the command succeeds
    When I run "ctxloom remote list"
    Then the command succeeds
    And the output contains "origin"
    And the file ".ctxloom/remotes.yaml" contains "origin"

  Scenario: Set a default remote
    Given an initialized ctxloom project
    And I run "ctxloom remote add origin file:///tmp/acceptance-remote.git --forge git"
    When I run "ctxloom remote default origin"
    Then the command succeeds
    And the file ".ctxloom/remotes.yaml" contains "default: origin"

  Scenario: Remove a remote
    Given an initialized ctxloom project
    And I run "ctxloom remote add origin file:///tmp/acceptance-remote.git --forge git"
    When I run "ctxloom remote remove origin"
    Then the command succeeds
    When I run "ctxloom remote list"
    Then the output does not contain "origin"
