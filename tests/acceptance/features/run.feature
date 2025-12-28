Feature: Run command
  As a user
  I want to run AI with assembled context
  So that I can interact with AI using my configured fragments

  Background:
    Given a project with scm initialized
    And a mock LM is configured

  # ============================================================================
  # Basic Run Operations
  # ============================================================================

  Scenario: Run with single fragment
    Given a fragment "test-fragment" in the project with content:
      """
      tags:
        - testing
      content: |
        This is test content.
      """
    And the mock LM will respond with:
      """
      LM response.
      """
    When I run scm "run -f test-fragment --print test prompt"
    Then the exit code should be 0
    And the LM should have received context containing "This is test content"
    And the LM should have received context containing "test prompt"

  Scenario: Run with multiple fragments
    Given a fragment "frag-one" in the project with content:
      """
      tags:
        - first
      content: |
        Content from fragment one.
      """
    And a fragment "frag-two" in the project with content:
      """
      tags:
        - second
      content: |
        Content from fragment two.
      """
    And the mock LM will respond with:
      """
      Response.
      """
    When I run scm "run -f frag-one -f frag-two --print combined test"
    Then the exit code should be 0
    And the LM should have received context containing "Content from fragment one"
    And the LM should have received context containing "Content from fragment two"

  # ============================================================================
  # Run with Profile
  # ============================================================================

  Scenario: Run with profile loads profile's fragments
    Given a fragment "profile-frag" in the project with content:
      """
      tags:
        - profile
      content: |
        Profile fragment content.
      """
    And a config file with:
      """
      lm:
        default_plugin: claude-code
        plugins:
          claude-code:
            binary_path: "{{MOCK_LM_PATH}}"
      profiles:
        test-profile:
          description: Test profile
          fragments:
            - profile-frag
      """
    And the mock LM will respond with:
      """
      OK
      """
    When I run scm "run -p test-profile --print profile test"
    Then the exit code should be 0
    And the LM should have received context containing "Profile fragment content"

  # ============================================================================
  # Run with Tags
  # ============================================================================

  Scenario: Run with tag loads matching fragments
    Given a fragment "security-frag" in the project with content:
      """
      tags:
        - security
      content: |
        Security guidelines here.
      """
    And a fragment "style-frag" in the project with content:
      """
      tags:
        - style
      content: |
        Style guidelines here.
      """
    And the mock LM will respond with:
      """
      OK
      """
    When I run scm "run -t security --print tag test"
    Then the exit code should be 0
    And the LM should have received context containing "Security guidelines"
    And the LM should have received context not containing "Style guidelines"

  Scenario: Run with multiple tags
    Given a fragment "review-frag" in the project with content:
      """
      tags:
        - review
      content: |
        Review content.
      """
    And a fragment "testing-frag" in the project with content:
      """
      tags:
        - testing
      content: |
        Testing content.
      """
    And the mock LM will respond with:
      """
      OK
      """
    When I run scm "run -t review -t testing --print multi-tag test"
    Then the exit code should be 0
    And the LM should have received context containing "Review content"
    And the LM should have received context containing "Testing content"

  # ============================================================================
  # Run with Variables
  # ============================================================================

  Scenario: Run substitutes variables from profile
    Given a fragment "var-frag" in the project with content:
      """
      tags:
        - variables
      content: |
        The language is {{language}}.
        The version is {{version}}.
      """
    And a config file with:
      """
      lm:
        default_plugin: claude-code
        plugins:
          claude-code:
            binary_path: "{{MOCK_LM_PATH}}"
      profiles:
        var-profile:
          description: Variable profile
          fragments:
            - var-frag
          variables:
            language: Go
            version: "1.21"
      """
    And the mock LM will respond with:
      """
      OK
      """
    When I run scm "run -p var-profile --print var test"
    Then the exit code should be 0
    And the LM should have received context containing "The language is Go"
    And the LM should have received context containing "The version is 1.21"

  # ============================================================================
  # Dry Run Mode
  # ============================================================================

  Scenario: Dry run shows command without executing
    Given a fragment "dry-frag" in the project with content:
      """
      tags:
        - dry
      content: |
        Dry run content.
      """
    When I run scm "run -f dry-frag --dry-run test prompt"
    Then the exit code should be 0
    And the output should contain "Dry run content"

  # ============================================================================
  # No Distill Flag
  # ============================================================================

  Scenario: Fragment with no_distill preserves content
    Given a fragment "no-distill-frag" in the project with content:
      """
      tags:
        - protected
      no_distill: true
      content: |
        This exact content must be preserved.
      """
    And the mock LM will respond with:
      """
      OK
      """
    When I run scm "run -f no-distill-frag --print no-distill test"
    Then the exit code should be 0
    And the LM should have received context containing "This exact content must be preserved"

  # ============================================================================
  # Error Handling
  # ============================================================================

  Scenario: Run with nonexistent fragment fails
    When I run scm "run -f nonexistent --print test"
    Then the exit code should be 1
    And the output should contain "not found"

  Scenario: Run with nonexistent profile fails
    When I run scm "run -p nonexistent --print test"
    Then the exit code should be 1
    And the output should contain "unknown profile"

  # ============================================================================
  # Print Mode
  # ============================================================================

  Scenario: Print mode outputs response and exits
    Given a fragment "print-frag" in the project with content:
      """
      tags:
        - print
      content: |
        Print test.
      """
    And the mock LM will respond with:
      """
      This is the LM response.
      """
    When I run scm "run -f print-frag --print test"
    Then the exit code should be 0
    And the output should contain "This is the LM response"

  # ============================================================================
  # Quiet Mode
  # ============================================================================

  Scenario: Quiet mode suppresses warnings
    Given a fragment "quiet-frag" in the project with content:
      """
      tags:
        - quiet
      content: |
        Quiet test.
      """
    And the mock LM will respond with:
      """
      OK
      """
    When I run scm "run -f quiet-frag --quiet --print test"
    Then the exit code should be 0

  # ============================================================================
  # Run with Saved Prompt
  # ============================================================================

  Scenario: Run with saved prompt using --run-prompt
    Given a fragment "prompt-frag" in the project with content:
      """
      tags:
        - prompts
      content: |
        Fragment for prompt test.
      """
    And a prompt "saved-prompt" in the project with content:
      """
      description: A saved prompt
      content: |
        This is saved prompt content.
      """
    And the mock LM will respond with:
      """
      OK
      """
    When I run scm "run -f prompt-frag -r saved-prompt --print"
    Then the exit code should be 0
    And the LM should have received context containing "This is saved prompt content"

  Scenario: Run with --prompt flag as alternative to positional args
    Given a fragment "alt-frag" in the project with content:
      """
      tags:
        - alt
      content: |
        Alternative prompt test.
      """
    And the mock LM will respond with:
      """
      OK
      """
    When I run scm "run -f alt-frag --prompt alternative-prompt-text --print"
    Then the exit code should be 0
    And the LM should have received context containing "alternative-prompt-text"

  # ============================================================================
  # Subdirectory Fragments
  # ============================================================================

  Scenario: Run with fragment in subdirectory
    Given a fragment "lang/golang" in the project with content:
      """
      tags:
        - golang
      content: |
        Go coding guidelines from subdirectory.
      """
    And the mock LM will respond with:
      """
      OK
      """
    When I run scm "run -f lang/golang --print test"
    Then the exit code should be 0
    And the LM should have received context containing "Go coding guidelines from subdirectory"
