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

    # @wip — a product gap, not a harness one: the distiller's internal
    # one-shot runs at the plan floor, and the launch refuses a plan-first
    # posture without a human approver (launch.ErrPlansFirstNeedsHuman);
    # every opencode label's approver is none (opencode.Opencode.Approvals is
    # Absent). Measured 2026-10-12: "no reachable engine for distillation ...
    # the posture is plan, but the approver is none". Untag when an
    # approver-none engine can run ctxloom's internal one-shots.
    @opencode @wip
    Examples:
      | agent    |
      | Opencode |

  Scenario Outline: Distilling a prompt records a compressed rendering
    Given a real <agent> agent is available
    And a bundle "lore" with a long fragment "rules"
    When I run "ctxloom command distill lore#commands/guidance -f"
    Then the command succeeds
    And the bundle "lore" records a distillation

    Examples:
      | agent  |
      | Claude |

    # @wip — a product gap, not a harness one: the distiller's internal
    # one-shot runs at the plan floor, and the launch refuses a plan-first
    # posture without a human approver (launch.ErrPlansFirstNeedsHuman);
    # every opencode label's approver is none (opencode.Opencode.Approvals is
    # Absent). Measured 2026-10-12: "no reachable engine for distillation ...
    # the posture is plan, but the approver is none". Untag when an
    # approver-none engine can run ctxloom's internal one-shots.
    @opencode @wip
    Examples:
      | agent    |
      | Opencode |

  Scenario Outline: Distilling a whole bundle distills its fragment
    Given a real <agent> agent is available
    And a bundle "lore" with a long fragment "rules"
    When I run "ctxloom bundle distill .ctxloom/content/bundles/v2/lore/bundle.yaml -f"
    Then the command succeeds
    And the distilled fragment "rules" in bundle "lore" is a real compression
    And the distilled fragment "rules" in bundle "lore" preserves the domain

    Examples:
      | agent  |
      | Claude |

    # @wip — a product gap, not a harness one: the distiller's internal
    # one-shot runs at the plan floor, and the launch refuses a plan-first
    # posture without a human approver (launch.ErrPlansFirstNeedsHuman);
    # every opencode label's approver is none (opencode.Opencode.Approvals is
    # Absent). Measured 2026-10-12: "no reachable engine for distillation ...
    # the posture is plan, but the approver is none". Untag when an
    # approver-none engine can run ctxloom's internal one-shots.
    @opencode @wip
    Examples:
      | agent    |
      | Opencode |
