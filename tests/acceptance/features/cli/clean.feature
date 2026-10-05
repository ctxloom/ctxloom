Feature: clean — reclaiming this project's regenerable cache

  `ctxloom clean` removes what a named command rebuilds and nothing else.
  Like every destroyer, it only reports until it is told --yes.

  Scenario: Without --yes, clean reports the cache it would remove and removes nothing
    Given an initialized ctxloom project
    And the project already has the file ".ctxloom/cache/refused_advances.yaml":
      """
      {}
      """
    When I run "ctxloom clean"
    Then the command succeeds
    And the output contains "refused_advances.yaml"
    And the output contains "ctxloom clean --yes"
    And the file ".ctxloom/cache/refused_advances.yaml" exists

  Scenario: With --yes, clean removes the cache it reported
    Given an initialized ctxloom project
    And the project already has the file ".ctxloom/cache/refused_advances.yaml":
      """
      {}
      """
    When I run "ctxloom clean --yes"
    Then the command succeeds
    And the output contains "refused_advances.yaml"
    And the file ".ctxloom/cache/refused_advances.yaml" does not exist
