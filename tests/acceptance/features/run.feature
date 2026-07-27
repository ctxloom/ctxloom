Feature: Run
  run assembles context from a profile and hands it to the configured LLM. With
  the mock backend the full path is observable: the assembled context reaches the
  model, and the model's reply reaches the user.

  Scenario: Dry run assembles context without launching a model
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a profile "dev" with bundle "demo"
    When I run "ctxloom run --dry-run --profile dev hello"
    Then the command succeeds
    And the output contains "dev"

  Scenario: Run hands the assembled context to the model and returns its reply
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a profile "dev" with bundle "demo"
    And the mock LLM responds "MOCK-REPLY"
    When I run "ctxloom run --print --profile dev unicorn-prompt"
    Then the command succeeds
    And the output contains "MOCK-REPLY"

  # T2 regression: deny_tools was silently dropped crossing internal/lm/grpc's
  # proto wire (ManagedConfigToProto/managedConfigFromProto). A per-hop test
  # cannot see this class of defect because the proto converter and its mirror
  # struct read as a matched pair; only tracing the payload tip-to-tail (argv ->
  # profile resolution -> proto wire -> the launched backend's Setup) does. This
  # asserts the WIRE PAYLOAD the backend actually received, not just that the
  # command exited 0.
  Scenario: deny_tools configured on a profile reaches the launched backend
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a profile "guarded" with bundle "demo" and deny_tools "WebFetch"
    And the mock LLM responds "MOCK-REPLY"
    When I run "ctxloom run --print --profile guarded unicorn-prompt"
    Then the command succeeds
    And the mock recorded input contains "WebFetch"
