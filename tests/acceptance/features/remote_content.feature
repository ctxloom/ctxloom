@remote
Feature: Remote content
  Fetching content from a remote over the real clone path, exercised hermetically
  against a seeded file:// repository. (Enabled by the NormalizeURL fix that
  preserves non-HTTP schemes.) Remote items are pure references: a local profile
  names a remote bundle via its per-remote short form ("<remote>/<bundle>", a
  bare name is local — see decision A), and `remote pull` fetches and locks the
  dependency closure. The lockfile is pure dependency pinning — a pull/upgrade
  moves pins freely into the active lock; whether the pulled content ever
  reaches the agent is decided per item by `ctxloom review` (see
  review.feature). Top-level profile distribution was retired — profiles ship
  inside bundles — so only bundles are browsed, referenced, and locked at the
  top level.
  #
  # The pin and the payload are two different questions: these scenarios also
  # assert that changed upstream content actually reaches an assembled/
  # materialized context (not just the lockfile), that a stale local clone
  # checkout never leaches into what's served, and that "Skipped (already
  # installed)" — while accurate — does not mean the content is current; only
  # `remote upgrade` moves an existing pin.

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
    And I run "ctxloom profile create dev --bundle origin/demo"
    When I run "ctxloom remote pull"
    Then the command succeeds
    And the file ".ctxloom/lock.yaml" contains "@bundles/demo"

  Scenario: A second pull skips already-locked dependencies
    # Pull is incremental: an item whose lock entry resolves from the clone
    # cache is not re-fetched.
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    And I run "ctxloom remote default origin"
    And I run "ctxloom profile create dev --bundle origin/demo"
    And I run "ctxloom remote pull"
    When I run "ctxloom remote pull"
    Then the command succeeds
    And the output contains "Skipped (kept at their locked commit)"

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
    And I run "ctxloom profile create dev --bundle origin/demo"
    And I run "ctxloom remote pull"
    And the remote "origin" advances its bundle
    When I run "ctxloom remote upgrade"
    Then the command succeeds
    And the output contains "Advanced"

  Scenario: A held dependency is not upgraded
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    And I run "ctxloom remote default origin"
    And I run "ctxloom profile create dev --bundle origin/demo"
    And I run "ctxloom remote pull"
    And I run "ctxloom bundle hold origin/demo"
    And the remote "origin" advances its bundle
    When I run "ctxloom remote upgrade"
    Then the command succeeds
    And the output contains "up to date"

  Scenario: An upstream content change reaches the assembled context
    # The pin is not the point: what actually lands in front of the agent is.
    # Upgrading to changed upstream content, then accepting it, must replace
    # what the agent sees — not just what the lockfile records.
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    And I run "ctxloom remote default origin"
    And I run "ctxloom profile create dev --bundle origin/demo"
    And I run "ctxloom remote pull"
    And I accept the pending item "demo#fragments/demo-frag" from remote "origin"
    And I run "ctxloom profile materialize dev --target before"
    Then the file "before/CLAUDE.md" contains "Demo fragment content."
    When the remote "origin" changes fragment "demo-frag" to "MARKER-BRAVO-second-edition"
    And I run "ctxloom remote upgrade"
    And I accept the pending item "demo#fragments/demo-frag" from remote "origin"
    And I run "ctxloom profile materialize dev --target after"
    Then the command succeeds
    And the file "after/CLAUDE.md" contains "MARKER-BRAVO-second-edition"
    And the file "after/CLAUDE.md" does not contain "Demo fragment content."

  Scenario: A stale local checkout never leaks into what's served
    # A clone's checked-out HEAD is not the source of truth for content — the
    # remote-tracking refs a fetch advances are. Forcing the local checkout
    # back to the very first commit must not resurrect old content.
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    And I run "ctxloom remote default origin"
    And I run "ctxloom profile create dev --bundle origin/demo"
    And I run "ctxloom remote pull"
    And I accept the pending item "demo#fragments/demo-frag" from remote "origin"
    And the remote "origin" changes fragment "demo-frag" to "MARKER-STALE-CHECKOUT-current"
    And I run "ctxloom remote upgrade"
    And I accept the pending item "demo#fragments/demo-frag" from remote "origin"
    And the remote "origin"'s cached clone is forced back to its first commit
    When I run "ctxloom profile materialize dev --target out"
    Then the command succeeds
    And the file "out/CLAUDE.md" contains "MARKER-STALE-CHECKOUT-current"
    And the file "out/CLAUDE.md" does not contain "Demo fragment content."

  Scenario: A skipped pull leaves old content in place, and says so
    # A plain pull never moves an existing pin; only `remote upgrade` does. The
    # BEHAVIOR is correct, but pull used to report it as "Skipped (already
    # installed)", which reads as "you have the latest content" when upstream
    # has in fact moved — once costing an hour diagnosing a false "stale
    # content" bug. So this pins both halves: the old content stays, AND the
    # output says what is actually true rather than implying currency.
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    And I run "ctxloom remote default origin"
    And I run "ctxloom profile create dev --bundle origin/demo"
    And I run "ctxloom remote pull"
    And I accept the pending item "demo#fragments/demo-frag" from remote "origin"
    And the remote "origin" changes fragment "demo-frag" to "MARKER-SKIPPED-PULL-never-seen"
    When I run "ctxloom remote pull"
    Then the command succeeds
    And the output contains "Skipped (kept at their locked commit)"
    And the output contains "may have upstream changes"
    And the output contains "ctxloom remote upgrade"
    And the output does not contain "already installed"
    When I run "ctxloom profile materialize dev --target out"
    Then the command succeeds
    And the file "out/CLAUDE.md" contains "Demo fragment content."
    And the file "out/CLAUDE.md" does not contain "MARKER-SKIPPED-PULL-never-seen"
