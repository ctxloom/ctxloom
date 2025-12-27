Feature: Copy fragments and prompts
  As a user
  I want to copy fragments between locations
  So that I can share and customize context

  # Note: Tests use an isolated fake home directory to ensure
  # real user config doesn't affect test results.

  Background:
    Given a project with mlcm initialized
    And a home directory with mlcm config

  # ============================================================================
  # Basic Copy Operations
  # ============================================================================

  Scenario: Copy fragment from home to project
    Given a fragment "my-fragment" in home with content:
      """
      tags:
        - custom
      content: |
        This is my custom fragment content.
      """
    When I run mlcm "copy --from home --to project -f my-fragment"
    Then the exit code should be 0
    And the file ".mlcm/context-fragments/my-fragment.yaml" should exist
    And the file ".mlcm/context-fragments/my-fragment.yaml" should contain "This is my custom fragment content"
    And the output should contain "1 added"

  Scenario: Copy fragment from project to home
    Given a fragment "project-frag" in the project with content:
      """
      tags:
        - local
      content: |
        Project-specific content to share.
      """
    When I run mlcm "copy --from project --to home -f project-frag"
    Then the exit code should be 0
    And the home file ".mlcm/context-fragments/project-frag.yaml" should exist
    And the home file ".mlcm/context-fragments/project-frag.yaml" should contain "Project-specific content to share"

  Scenario: Copy multiple fragments from home to project
    Given a fragment "frag-one" in home with content:
      """
      tags:
        - multi
      content: |
        Fragment one content.
      """
    And a fragment "frag-two" in home with content:
      """
      tags:
        - multi
      content: |
        Fragment two content.
      """
    When I run mlcm "copy --from home --to project -f frag-one -f frag-two"
    Then the exit code should be 0
    And the file ".mlcm/context-fragments/frag-one.yaml" should exist
    And the file ".mlcm/context-fragments/frag-two.yaml" should exist
    And the output should contain "2 added"

  Scenario: Copy all fragments from home to project
    Given a fragment "all-one" in home with content:
      """
      tags:
        - batch
      content: |
        First fragment.
      """
    And a fragment "all-two" in home with content:
      """
      tags:
        - batch
      content: |
        Second fragment.
      """
    When I run mlcm "copy --from home --to project"
    Then the exit code should be 0
    And the file ".mlcm/context-fragments/all-one.yaml" should exist
    And the file ".mlcm/context-fragments/all-two.yaml" should exist

  # ============================================================================
  # Header Handling
  # ============================================================================

  Scenario: Copy adds header when copying to project
    Given a fragment "header-test" in home with content:
      """
      tags:
        - test
      content: |
        Test content for header.
      """
    When I run mlcm "copy --from home --to project -f header-test"
    Then the exit code should be 0
    And the file ".mlcm/context-fragments/header-test.yaml" should contain "DO NOT EDIT"

  Scenario: Copy strips header when copying from project to home
    Given a fragment "strip-header" in the project with content:
      """
      # ┌─────────────────────────────────────────────────────────────────────────────┐
      # │ DO NOT EDIT - Changes will be overwritten on next 'mlcm copy'              │
      # │ To customize: mlcm fragment edit {{name}} then re-run 'mlcm copy'          │
      # └─────────────────────────────────────────────────────────────────────────────┘
      tags:
        - modified
      content: |
        Content with header to strip.
      """
    When I run mlcm "copy --from project --to home -f strip-header"
    Then the exit code should be 0
    And the home file ".mlcm/context-fragments/strip-header.yaml" should not contain "DO NOT EDIT"
    And the home file ".mlcm/context-fragments/strip-header.yaml" should contain "Content with header to strip"

  # ============================================================================
  # Force and Skip Behavior
  # ============================================================================

  Scenario: Copy skips existing files without force flag
    Given a fragment "existing" in home with content:
      """
      tags:
        - original
      content: |
        Original content from home.
      """
    And a fragment "existing" in the project with content:
      """
      tags:
        - modified
      content: |
        Modified project content.
      """
    When I run mlcm "copy --from home --to project -f existing"
    Then the exit code should be 0
    And the output should contain "skipped"
    And the file ".mlcm/context-fragments/existing.yaml" should contain "Modified project content"

  Scenario: Copy with force overwrites existing files
    Given a fragment "to-overwrite" in home with content:
      """
      tags:
        - new
      content: |
        New content from home.
      """
    And a fragment "to-overwrite" in the project with content:
      """
      tags:
        - old
      content: |
        Old project content to be replaced.
      """
    When I run mlcm "copy --from home --to project -f to-overwrite --force"
    Then the exit code should be 0
    And the output should contain "updated"
    And the file ".mlcm/context-fragments/to-overwrite.yaml" should contain "New content from home"

  # ============================================================================
  # Tag-based Filtering
  # ============================================================================

  Scenario: Copy by single tag
    Given a fragment "tagged-one" in home with content:
      """
      tags:
        - security
        - review
      content: |
        Security review fragment.
      """
    And a fragment "tagged-two" in home with content:
      """
      tags:
        - style
      content: |
        Style fragment - should not be copied.
      """
    When I run mlcm "copy --from home --to project -t security"
    Then the exit code should be 0
    And the file ".mlcm/context-fragments/tagged-one.yaml" should exist
    And the file ".mlcm/context-fragments/tagged-two.yaml" should not exist

  Scenario: Copy by multiple tags
    Given a fragment "sec-frag" in home with content:
      """
      tags:
        - security
      content: |
        Security content.
      """
    And a fragment "style-frag" in home with content:
      """
      tags:
        - style
      content: |
        Style content.
      """
    And a fragment "other-frag" in home with content:
      """
      tags:
        - misc
      content: |
        Misc content - should not be copied.
      """
    When I run mlcm "copy --from home --to project -t security -t style"
    Then the exit code should be 0
    And the file ".mlcm/context-fragments/sec-frag.yaml" should exist
    And the file ".mlcm/context-fragments/style-frag.yaml" should exist
    And the file ".mlcm/context-fragments/other-frag.yaml" should not exist

  Scenario: Copy combining fragment name and tag filters
    Given a fragment "explicit-frag" in home with content:
      """
      tags:
        - explicit
      content: |
        Explicitly named fragment.
      """
    And a fragment "tagged-frag" in home with content:
      """
      tags:
        - included
      content: |
        Tag-matched fragment.
      """
    And a fragment "excluded-frag" in home with content:
      """
      tags:
        - excluded
      content: |
        Should not be copied.
      """
    When I run mlcm "copy --from home --to project -f explicit-frag -t included"
    Then the exit code should be 0
    And the file ".mlcm/context-fragments/explicit-frag.yaml" should exist
    And the file ".mlcm/context-fragments/tagged-frag.yaml" should exist
    And the file ".mlcm/context-fragments/excluded-frag.yaml" should not exist

  Scenario: Copy fragments for persona
    Given a fragment "persona-frag-one" in home with content:
      """
      tags:
        - dev
      content: |
        Persona fragment one.
      """
    And a fragment "persona-frag-two" in home with content:
      """
      tags:
        - dev
      content: |
        Persona fragment two.
      """
    And a fragment "other-frag" in home with content:
      """
      tags:
        - other
      content: |
        Should not be copied.
      """
    And a home config file with:
      """
      personas:
        dev-persona:
          description: Developer persona
          fragments:
            - persona-frag-one
            - persona-frag-two
      """
    When I run mlcm "copy --from home --to project --persona dev-persona"
    Then the exit code should be 0
    And the file ".mlcm/context-fragments/persona-frag-one.yaml" should exist
    And the file ".mlcm/context-fragments/persona-frag-two.yaml" should exist
    And the file ".mlcm/context-fragments/other-frag.yaml" should not exist

  # ============================================================================
  # Prompt Operations
  # ============================================================================

  Scenario: Copy prompt from home to project
    Given a prompt "my-prompt" in home with content:
      """
      description: My custom prompt
      content: |
        This is my prompt template.
      """
    When I run mlcm "copy --from home --to project -p my-prompt"
    Then the exit code should be 0
    And the file ".mlcm/prompts/my-prompt.yaml" should exist
    And the file ".mlcm/prompts/my-prompt.yaml" should contain "This is my prompt template"

  Scenario: Copy prompt from project to home
    Given a prompt "project-prompt" in the project with content:
      """
      description: Project prompt
      content: |
        Project-specific prompt content.
      """
    When I run mlcm "copy --from project --to home -p project-prompt"
    Then the exit code should be 0
    And the home file ".mlcm/prompts/project-prompt.yaml" should exist
    And the home file ".mlcm/prompts/project-prompt.yaml" should contain "Project-specific prompt content"

  Scenario: Copy multiple prompts
    Given a prompt "prompt-a" in home with content:
      """
      description: Prompt A
      content: |
        Content A.
      """
    And a prompt "prompt-b" in home with content:
      """
      description: Prompt B
      content: |
        Content B.
      """
    When I run mlcm "copy --from home --to project -p prompt-a -p prompt-b"
    Then the exit code should be 0
    And the file ".mlcm/prompts/prompt-a.yaml" should exist
    And the file ".mlcm/prompts/prompt-b.yaml" should exist

  # ============================================================================
  # Verbose Output
  # ============================================================================

  Scenario: Verbose flag shows individual files
    Given a fragment "verbose-frag" in home with content:
      """
      tags:
        - verbose
      content: |
        Verbose test content.
      """
    When I run mlcm "copy --from home --to project -f verbose-frag --verbose"
    Then the exit code should be 0
    And the output should contain "verbose-frag.yaml"

  # ============================================================================
  # Short Location Aliases
  # ============================================================================

  Scenario: Use short alias 'h' for home
    Given a fragment "alias-test" in home with content:
      """
      tags:
        - alias
      content: |
        Testing short aliases.
      """
    When I run mlcm "copy --from h --to p -f alias-test"
    Then the exit code should be 0
    And the file ".mlcm/context-fragments/alias-test.yaml" should exist

  # ============================================================================
  # Error Cases
  # ============================================================================

  Scenario: Copy fails with same source and destination
    When I run mlcm "copy --from project --to project"
    Then the exit code should be 1
    And the output should contain "cannot be the same"

  Scenario: Copy fails when copying to resources without dev flag
    When I run mlcm "copy --from home --to resources"
    Then the exit code should be 1
    And the output should contain "cannot copy to resources"

  Scenario: Copy fails with invalid source location
    When I run mlcm "copy --from invalid --to project"
    Then the exit code should be 1
    And the output should contain "invalid location"

  Scenario: Copy fails with invalid destination location
    When I run mlcm "copy --from home --to badloc"
    Then the exit code should be 1
    And the output should contain "invalid location"

  Scenario: Copy fails when required flags missing
    When I run mlcm "copy --from home"
    Then the exit code should be 1

  # ============================================================================
  # Isolation Verification
  # ============================================================================

  Scenario: Empty fake home has no fragments to copy
    # This verifies the test uses an isolated home directory
    # If real home leaked through, this would copy real user fragments
    When I run mlcm "copy --from home --to project"
    Then the exit code should be 0
    And the output should contain "No fragments to copy"

  # ============================================================================
  # Subdirectory Handling
  # ============================================================================

  Scenario: Copy preserves subdirectory structure
    Given a fragment "lang/golang" in home with content:
      """
      tags:
        - golang
      content: |
        Go language guidelines.
      """
    When I run mlcm "copy --from home --to project -f lang/golang"
    Then the exit code should be 0
    And the file ".mlcm/context-fragments/lang/golang.yaml" should exist
    And the file ".mlcm/context-fragments/lang/golang.yaml" should contain "Go language guidelines"

  # ============================================================================
  # Copy from Resources (Embedded Fragments)
  # ============================================================================

  Scenario: Copy fragment from resources to project
    When I run mlcm "copy --from resources --to project -f general/security"
    Then the exit code should be 0
    And the file ".mlcm/context-fragments/general/security.yaml" should exist

  Scenario: Copy fragment from resources to home
    When I run mlcm "copy --from resources --to home -f general/code-quality"
    Then the exit code should be 0
    And the home file ".mlcm/context-fragments/general/code-quality.yaml" should exist

  Scenario: Copy multiple fragments from resources
    When I run mlcm "copy --from resources --to project -f general/security -f general/tdd"
    Then the exit code should be 0
    And the file ".mlcm/context-fragments/general/security.yaml" should exist
    And the file ".mlcm/context-fragments/general/tdd.yaml" should exist

  Scenario: Copy fragments by tag from resources
    When I run mlcm "copy --from resources --to project -t golang"
    Then the exit code should be 0
    And the file ".mlcm/context-fragments/lang/golang/golang.yaml" should exist

  Scenario: Use short alias 'r' for resources
    When I run mlcm "copy --from r --to p -f general/documentation"
    Then the exit code should be 0
    And the file ".mlcm/context-fragments/general/documentation.yaml" should exist

  Scenario: Copy nonexistent fragment from resources fails gracefully
    When I run mlcm "copy --from resources --to project -f nonexistent/fragment"
    Then the exit code should be 0
    And the output should contain "0 added"
