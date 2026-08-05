Feature: Search
  search finds local content by name, tag, and type.

  # RESTRICT is a claim about what is left OUT, and asserting only that the
  # match appears says nothing about it: a search that ignored --type entirely
  # still returns the fragment. The fixture seeds a command whose name the same
  # query matches, so the restriction has something to exclude and the result
  # count states the narrowing outright.
  Scenario: Restrict search to fragments
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "testing" in bundle "demo" exists
    And a command "testing-review" in bundle "demo" exists
    When I run "ctxloom search --local testing"
    Then the command succeeds
    And the output contains "Results (2):"
    And the output contains "testing-review"
    When I run "ctxloom search --local --type fragment testing"
    Then the command succeeds
    And the output contains "Results (1):"
    And the output contains "testing"
    And the output does not contain "testing-review"
    And the output does not contain "Commands:"
