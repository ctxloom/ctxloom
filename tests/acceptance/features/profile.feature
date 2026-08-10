Feature: Profiles
  A profile names a collection of bundles. Creating one writes a profile file,
  registers it as a default, and exposes it to the agent through the profiles
  resource.

  Scenario: Create a profile across every axis
    Given an initialized ctxloom project
    And a bundle "demo" exists
    When I run "ctxloom profile create dev -b demo"
    Then the command succeeds
    And the file ".ctxloom/profiles/dev.yaml" exists
    When I run "ctxloom profile list"
    Then the command succeeds
    And the output contains "dev"
    When the agent reads resource "ctxloom://profiles"
    Then the resource contains "dev"

  Scenario: Show a profile
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a profile "dev" with bundle "demo"
    When I run "ctxloom profile show dev"
    Then the command succeeds
    And the output contains "demo"

  Scenario: Read a single profile over MCP
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a profile "dev" with bundle "demo"
    When the agent reads resource "ctxloom://profiles/dev"
    Then the resource contains "demo"

  # Bare `remove` is a preview: it must name what it would destroy AND leave
  # the profile in place. A guard that quietly destroyed anyway would still
  # pass a scenario that only checked exit code or the report text — the
  # file-exists check is what actually catches that.
  Scenario: Bare profile remove reports and destroys nothing
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a profile "dev" with bundle "demo"
    When I run "ctxloom profile remove dev"
    Then the command succeeds
    And the output contains "Nothing was removed"
    And the output contains "--yes"
    And the file ".ctxloom/profiles/dev.yaml" exists

  Scenario: Remove a profile removes its file
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a profile "dev" with bundle "demo"
    When I run "ctxloom profile remove dev --yes"
    Then the command succeeds
    And the file ".ctxloom/profiles/dev.yaml" does not exist

  # `profile default` was RETIRED: the default context is now whatever the
  # always-bound default AGENT composes (`ctxloom agent default`). There is no
  # "unset" — the replacement binds a name, not a settable list. That verb is
  # a leaf of the agent noun and is specified with the rest of them in
  # cli/agent.feature, which asserts the config key the binding actually
  # lands in; the duplicate scenario that used to sit here is gone rather
  # than kept in two places to drift.
