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
