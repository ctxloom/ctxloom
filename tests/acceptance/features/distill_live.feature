@live
Feature: Live distillation with the real Claude agent
  Distillation delegates compression to a real Claude backend. The assertions are
  loose about wording but strict about behavior: the output must be a genuine
  compression (not a passthrough, not a stub) and must keep the load-bearing
  facts. These scenarios skip when no credentials are present.

  Background:
    Given a real Claude agent is available

  Scenario: Distilling a fragment compresses it while preserving the key fact
    Given a bundle "lore" with a long fragment "rules"
    When I run "ctxloom fragment distill lore#fragments/rules -f"
    Then the command succeeds
    And the distilled fragment "rules" in bundle "lore" is a real compression
    And the distilled fragment "rules" in bundle "lore" preserves the domain

  Scenario: Distilling a prompt records a compressed rendering
    Given a bundle "lore" with a long fragment "rules"
    When I run "ctxloom prompt distill lore#prompts/guidance -f"
    Then the command succeeds
    And the bundle "lore" records a distillation

  Scenario: Distilling a whole bundle distills its fragment
    Given a bundle "lore" with a long fragment "rules"
    When I run "ctxloom bundle distill .ctxloom/cache/bundles/lore.yaml -f"
    Then the command succeeds
    And the distilled fragment "rules" in bundle "lore" is a real compression
    And the distilled fragment "rules" in bundle "lore" preserves the domain
