@remote
Feature: Remote content
  Fetching content from a remote over the real clone path, exercised hermetically
  against a seeded file:// repository. (Enabled by the NormalizeURL fix that
  preserves non-HTTP schemes.) Remote items are pure references: a local profile
  names a remote bundle (a bare ref expands against the default remote), and
  `remote pull` fetches and locks the dependency closure. The lockfile is pure
  dependency pinning — a pull/upgrade moves pins freely into the active lock;
  whether the pulled content ever reaches the agent is decided per item by
  `ctxloom review` (see review.feature). Top-level profile distribution was
  retired — profiles ship inside bundles — so only bundles are browsed,
  referenced, and locked at the top level.

  Scenario: Browse a remote's contents
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    When I run "ctxloom remote browse origin"
    Then the command succeeds
    And the output contains "@bundles/demo"

  Scenario: Reference a remote bundle and pull it
    # A pull records the pin straight into the active lock, trusted or not; the
    # bundle's content is gated per item at exposure, not by the lockfile.
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    And I run "ctxloom remote default origin"
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

  Scenario: An upgrade advances the pin directly
    # `remote upgrade` re-resolves the closure to HEAD and writes the new pin
    # straight to the active lock — no staging, no approval. Changed untrusted
    # content is then withheld per item until accepted via `ctxloom review`.
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    And I run "ctxloom remote default origin"
    And I run "ctxloom profile create dev --bundle demo"
    And I run "ctxloom remote pull"
    And the remote "origin" advances its bundle
    When I run "ctxloom remote upgrade"
    Then the command succeeds
    And the output contains "Advanced"

  Scenario: A held dependency is not upgraded
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    And I run "ctxloom remote default origin"
    And I run "ctxloom profile create dev --bundle demo"
    And I run "ctxloom remote pull"
    And I run "ctxloom bundle hold origin/demo"
    And the remote "origin" advances its bundle
    When I run "ctxloom remote upgrade"
    Then the command succeeds
    And the output contains "up to date"
