@remote
Feature: Remote content
  Fetching content from a remote over the real clone path, exercised hermetically
  against a seeded file:// repository. (Enabled by the NormalizeURL fix that
  preserves non-HTTP schemes.)

  Scenario: Browse a remote's contents
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    When I run "ctxloom remote browse origin"
    Then the command succeeds
    And the output contains "origin/demo"
    And the output contains "origin/base"

  Scenario: Install a profile from a remote
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    When I run "ctxloom profile install origin/base --force"
    Then the command succeeds
    And the file ".ctxloom/profiles/origin/base.yaml" exists

  Scenario: Install a bundle's fragment from a remote
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    When I run "ctxloom fragment install origin/demo --force"
    Then the command succeeds

  Scenario: Install a bundle's prompt from a remote
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    When I run "ctxloom prompt install origin/demo --force"
    Then the command succeeds

  Scenario: Sync locks installed remote dependencies
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    And I run "ctxloom profile install origin/base --force"
    When I run "ctxloom remote sync"
    Then the command succeeds
    And the file ".ctxloom/lock.yaml" contains "origin/base"

  Scenario: Generate a lockfile
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    And I run "ctxloom profile install origin/base --force"
    When I run "ctxloom remote lock"
    Then the command succeeds

  Scenario: Read a remote's contents over MCP
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    When the agent reads resource "ctxloom://remotes/origin/contents"
    Then the resource contains "demo"
