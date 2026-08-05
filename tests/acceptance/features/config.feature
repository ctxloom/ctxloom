Feature: Configuration
  `ctxloom config` reads the project configuration. Sections are addressable
  individually.

  # The payload assertions here are load-bearing. This scenario used to assert
  # only `the output matches "."` — one arbitrary character — which a single
  # space, a stray newline, or a warning banner satisfies just as well as the
  # whole config does. That is a vacuous assertion over exactly the surface
  # ctxloom's characteristic bug (exit 0, success message, zero real payload)
  # hides in, so it is replaced by keys the rendered config must actually
  # carry.
  Scenario: Show the full configuration
    Given an initialized ctxloom project
    When I run "ctxloom config show"
    Then the command succeeds
    And the output contains "llm:"
    And the output contains "configs:"
    And the output contains "claude-code"
    And the output contains "defaults:"

  # `get <section>` claims to NARROW, and nothing here said so: a full dump
  # contains "claude-code" too, so rendering the whole config passed. The
  # narrowing is asserted by what must be ABSENT — `editor:` and the `llm:`
  # heading itself are in the full rendering and cannot be in this one.
  Scenario: Get the llm section
    Given an initialized ctxloom project
    When I run "ctxloom config get llm"
    Then the command succeeds
    And the output contains "claude-code"
    And the output contains "configs:"
    And the output contains "defaults:"
    And the output does not contain "editor:"
    And the output does not contain "llm:"

  Scenario: Get the profiles section reflects a created profile
    # `config get profiles` renders cfg.Profiles — the INLINE `profiles:
    # definitions:` map in config.yaml — not directory profiles written by
    # `profile create` (.ctxloom/profiles/<name>.yaml never round-trips through
    # this section).
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a profile "dev" is defined inline in config with bundle "demo"
    When I run "ctxloom config get profiles"
    Then the command succeeds
    And the output contains "dev"
    And the output contains "definitions:"
    # Same narrowing claim as the llm section above: these are in the full
    # rendering and must not be in a section view.
    And the output does not contain "editor:"
    And the output does not contain "profiles:"

  Scenario: An unknown section is rejected
    Given an initialized ctxloom project
    When I run "ctxloom config get nonsense"
    Then the command fails
    And the output contains "Available"
