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
    And the output contains "@bundles/demo"
    And the output contains "@profiles/base"

  Scenario: Install a profile from a remote
    # Remote profiles are pure references — not materialized to disk. Installing
    # locks the canonical ref and wires it into a local profile so it's active.
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    When I run "ctxloom profile install origin/base --force"
    Then the command succeeds
    And the file ".ctxloom/lock.yaml" contains "@profiles/base"
    And the file ".ctxloom/profiles/default.yaml" exists

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
    And the file ".ctxloom/lock.yaml" contains "@profiles/base"

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

  Scenario: An upgrade is staged for review and approved
    # References are hash-pinned; passive sync never stages. `remote upgrade`
    # re-pins to HEAD and stages the change for review.
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    And I run "ctxloom profile install origin/base --force"
    And the remote "origin" advances its bundle
    When I run "ctxloom remote upgrade"
    Then the command succeeds
    When I run "ctxloom bundle review"
    Then the command succeeds
    And the output contains "pending review"
    When I run "ctxloom bundle show-pending origin/demo"
    Then the command succeeds
    When I run "ctxloom bundle approve"
    Then the command succeeds
    When I run "ctxloom bundle review"
    Then the output contains "No bundle changes pending review"

  Scenario: A staged upgrade can be declined
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    And I run "ctxloom profile install origin/base --force"
    And the remote "origin" advances its bundle
    And I run "ctxloom remote upgrade"
    When I run "ctxloom bundle decline"
    Then the command succeeds
    When I run "ctxloom bundle review"
    Then the output contains "No bundle changes pending review"

  Scenario: A pinned dependency is not upgraded
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    And I run "ctxloom profile install origin/base --force"
    And I run "ctxloom bundle pin origin/base"
    And the remote "origin" advances its bundle
    When I run "ctxloom remote upgrade"
    Then the command succeeds
    And the output contains "up to date"
    When I run "ctxloom bundle review"
    Then the output contains "No bundle changes pending review"

  Scenario: A trusted remote's upgrade applies without review
    # Trust means "apply without review": `upgrade` rewrites the pinned refs and
    # relocks immediately instead of staging for review.
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    And I run "ctxloom profile install origin/base --force"
    And I run "ctxloom remote trust origin"
    And the remote "origin" advances its bundle
    When I run "ctxloom remote upgrade"
    Then the command succeeds
    And the output contains "trusted"
    When I run "ctxloom bundle review"
    Then the output contains "No bundle changes pending review"
