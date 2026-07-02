@remote
Feature: Remote content
  Fetching content from a remote over the real clone path, exercised hermetically
  against a seeded file:// repository. (Enabled by the NormalizeURL fix that
  preserves non-HTTP schemes.) Remote items are pure references: a local profile
  names a remote bundle (a bare ref expands against the default remote), and
  `remote pull` fetches and locks the dependency closure. Top-level profile
  distribution was retired — profiles ship inside bundles — so only bundles are
  browsed, referenced, and locked at the top level.

  Scenario: Browse a remote's contents
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    When I run "ctxloom remote browse origin"
    Then the command succeeds
    And the output contains "@bundles/demo"

  Scenario: Reference a remote bundle and pull it
    # A TRUSTED source locks directly on pull; an untrusted one stages the
    # new bundle pending review instead (see the upgrade scenarios below).
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    And I run "ctxloom remote default origin"
    And I run "ctxloom remote trust origin"
    And I run "ctxloom profile create dev --bundle demo"
    When I run "ctxloom remote pull"
    Then the command succeeds
    And the file ".ctxloom/lock.yaml" contains "@bundles/demo"

  Scenario: A second pull skips already-locked dependencies
    # Pull is incremental: an item whose lock entry resolves from the clone
    # cache is not re-fetched.
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    And I run "ctxloom remote default origin"
    And I run "ctxloom remote trust origin"
    And I run "ctxloom profile create dev --bundle demo"
    And I run "ctxloom remote pull"
    When I run "ctxloom remote pull"
    Then the command succeeds
    And the output contains "Skipped (already installed)"

  Scenario: Read a remote's contents over MCP
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    When the agent reads resource "ctxloom://remotes/origin/contents"
    Then the resource contains "demo"

  Scenario: An upgrade is staged for review and approved
    # Untrusted source: the initial pull stages the new bundle pending review
    # (approved here to reach a locked baseline); `remote upgrade` then re-pins
    # to HEAD and stages the CHANGE for review.
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    And I run "ctxloom remote default origin"
    And I run "ctxloom profile create dev --bundle demo"
    And I run "ctxloom remote pull"
    And I run "ctxloom bundle approve"
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
    And I run "ctxloom remote default origin"
    And I run "ctxloom profile create dev --bundle demo"
    And I run "ctxloom remote pull"
    And I run "ctxloom bundle approve"
    And the remote "origin" advances its bundle
    And I run "ctxloom remote upgrade"
    When I run "ctxloom bundle decline"
    Then the command succeeds
    When I run "ctxloom bundle review"
    Then the output contains "No bundle changes pending review"

  Scenario: A pinned dependency is not upgraded
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    And I run "ctxloom remote default origin"
    And I run "ctxloom profile create dev --bundle demo"
    And I run "ctxloom remote pull"
    And I run "ctxloom bundle approve"
    And I run "ctxloom bundle pin origin/demo"
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
    And I run "ctxloom remote default origin"
    And I run "ctxloom profile create dev --bundle demo"
    And I run "ctxloom remote pull"
    And I run "ctxloom bundle approve"
    And I run "ctxloom remote trust origin"
    And the remote "origin" advances its bundle
    When I run "ctxloom remote upgrade"
    Then the command succeeds
    And the output contains "trusted"
    When I run "ctxloom bundle review"
    Then the output contains "No bundle changes pending review"
