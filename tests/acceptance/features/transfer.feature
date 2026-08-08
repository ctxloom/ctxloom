Feature: Export and import
  Bundles and profiles move between projects as files. Export writes a portable
  copy; import reads it back. The round-trip is observable: delete the original,
  re-import, and it is listed again.

  # "Observable" is the claim, and existence plus a name did not observe it.
  # The export was asserted by the file EXISTING and the import by the bundle
  # NAME — which is also the filename — so an export that marshalled only the
  # scalar header and dropped the fragments:/commands: maps was green: the file
  # existed, import reconstructed a bundle called demo, and bundle list printed
  # it, with the entire payload lost in transit. transfer.feature is the only
  # place bundle export appears in the suite, so nothing else caught it.
  # Both ends now name a body: the exported file carries the seeded fragment's
  # and command's content, and so does the RE-IMPORTED manifest.
  Scenario: Export, delete, and re-import a bundle
    Given an initialized ctxloom project
    And a bundle "demo" exists
    When I run "ctxloom bundle export demo exported"
    Then the command succeeds
    And the file "exported/demo.yaml" contains "Example prompt content. Describe what this prompt does."
    And the file "exported/demo.yaml" contains "# Example Fragment"
    When I run "ctxloom bundle delete demo -f"
    Then the command succeeds
    And the file ".ctxloom/content/bundles/demo.yaml" does not exist
    When I run "ctxloom bundle import exported/demo.yaml -f"
    Then the command succeeds
    When I run "ctxloom bundle list"
    Then the output contains "demo"
    And the file ".ctxloom/content/bundles/demo.yaml" contains "Example prompt content. Describe what this prompt does."
    And the file ".ctxloom/content/bundles/demo.yaml" contains "# Example Fragment"

  # Same shape: a profile export that dropped the bundles: list still imported
  # as a profile named dev and still listed. That list is the profile's entire
  # content, so both ends assert it.
  Scenario: Export, delete, and re-import a profile
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a profile "dev" with bundle "demo"
    When I run "ctxloom profile export dev pexport"
    Then the command succeeds
    And the file "pexport/dev.yaml" contains "- demo"
    When I run "ctxloom profile delete dev"
    Then the command succeeds
    When I run "ctxloom profile import pexport/dev.yaml -f"
    Then the command succeeds
    When I run "ctxloom profile list"
    Then the output contains "dev"
    When I run "ctxloom profile show dev"
    Then the command succeeds
    And the output contains "Bundles:"
    And the output contains "- demo"
