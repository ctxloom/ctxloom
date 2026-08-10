@live
Feature: Live distillation with a real agent
  Distillation delegates compression to a real backend. The assertions are
  loose about wording but strict about behavior: the output must be a genuine
  compression (not a passthrough, not a stub) and must keep the load-bearing
  facts. The same scenarios run against every supported agent via the Examples
  table; each row skips when no credentials for that agent are present.

  Scenario Outline: Distilling a fragment compresses it while preserving the key fact
    Given a real <agent> agent is available
    And a bundle "lore" with a long fragment "rules"
    When I run "ctxloom fragment distill lore#fragments/rules -f"
    Then the command succeeds
    And the distilled fragment "rules" in bundle "lore" is a real compression
    And the distilled fragment "rules" in bundle "lore" preserves the domain

    Examples:
      | agent  |
      | Claude |

  Scenario Outline: Distilling a prompt records a compressed rendering
    Given a real <agent> agent is available
    And a bundle "lore" with a long fragment "rules"
    When I run "ctxloom command distill lore#commands/guidance -f"
    Then the command succeeds
    And the bundle "lore" records a distillation

    Examples:
      | agent  |
      | Claude |

  Scenario Outline: Distilling a whole bundle distills its fragment
    Given a real <agent> agent is available
    And a bundle "lore" with a long fragment "rules"
    When I run "ctxloom bundle distill .ctxloom/content/bundles/lore.yaml -f"
    Then the command succeeds
    And the distilled fragment "rules" in bundle "lore" is a real compression
    And the distilled fragment "rules" in bundle "lore" preserves the domain

    Examples:
      | agent  |
      | Claude |
