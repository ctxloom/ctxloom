Feature: Export and import
  Bundles and profiles move between projects as files. Export writes a portable
  copy; import reads it back.

  Scenario: Export and re-import a bundle
    Given an initialized ctxloom project
    And a bundle "demo" exists
    When I run "ctxloom bundle export demo exported"
    Then the command succeeds
    And the file "exported/demo.yaml" exists
    When I run "ctxloom bundle import exported/demo.yaml -f"
    Then the command succeeds

  Scenario: Export and re-import a profile
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a profile "dev" with bundle "demo"
    When I run "ctxloom profile export dev pexport"
    Then the command succeeds
    And the file "pexport/dev.yaml" exists
    When I run "ctxloom profile import pexport/dev.yaml -f"
    Then the command succeeds
