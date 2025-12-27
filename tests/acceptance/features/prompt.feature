Feature: Prompt management
  As a user
  I want to manage saved prompts
  So that I can reuse prompt templates

  Background:
    Given a project with scm initialized

  # ============================================================================
  # Prompt List
  # ============================================================================

  Scenario: List prompts in empty project
    When I run scm "prompt list"
    Then the exit code should be 0
    And the output should contain "No prompts found"

  Scenario: List prompts shows available prompts
    Given a prompt "code-review" in the project with content:
      """
      description: Review code for issues
      content: |
        Please review this code for bugs and improvements.
      """
    When I run scm "prompt list"
    Then the exit code should be 0
    And the output should contain "code-review"

  Scenario: List prompts shows multiple prompts
    Given a prompt "review" in the project with content:
      """
      description: Code review
      content: |
        Review this code.
      """
    And a prompt "explain" in the project with content:
      """
      description: Explain code
      content: |
        Explain this code.
      """
    When I run scm "prompt list"
    Then the exit code should be 0
    And the output should contain "review"
    And the output should contain "explain"

  # ============================================================================
  # Prompt Show
  # ============================================================================

  Scenario: Show prompt displays content
    Given a prompt "show-test" in the project with content:
      """
      description: Test prompt
      content: |
        This is the prompt content to display.
      """
    When I run scm "prompt show show-test"
    Then the exit code should be 0
    And the output should contain "This is the prompt content to display"

  Scenario: Show prompt displays content only
    Given a prompt "described" in the project with content:
      """
      description: A very important prompt
      content: |
        Prompt content here.
      """
    When I run scm "prompt show described"
    Then the exit code should be 0
    And the output should contain "Prompt content here"

  Scenario: Show nonexistent prompt fails
    When I run scm "prompt show nonexistent"
    Then the exit code should be 1
    And the output should contain "not found"

  # ============================================================================
  # Prompt Delete
  # ============================================================================

  Scenario: Delete prompt removes file
    Given a prompt "to-delete" in the project with content:
      """
      description: Temp prompt
      content: |
        This will be deleted.
      """
    When I run scm "prompt delete to-delete"
    Then the exit code should be 0
    And the file ".scm/prompts/to-delete.yaml" should not exist

  Scenario: Delete nonexistent prompt fails
    When I run scm "prompt delete nonexistent"
    Then the exit code should be 1
