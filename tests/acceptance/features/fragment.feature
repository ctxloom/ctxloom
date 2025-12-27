Feature: Fragment management
  As a user
  I want to manage context fragments
  So that I can organize reusable context for AI interactions

  Background:
    Given a project with scm initialized

  # ============================================================================
  # Fragment List
  # ============================================================================

  Scenario: List fragments in empty project
    When I run scm "fragment list"
    Then the exit code should be 0
    And the output should contain "No fragments found"

  Scenario: List fragments shows available fragments
    Given a fragment "test-fragment" in the project with content:
      """
      tags:
        - testing
      content: |
        Test content here.
      """
    When I run scm "fragment list"
    Then the exit code should be 0
    And the output should contain "test-fragment"

  Scenario: List fragments shows multiple fragments
    Given a fragment "alpha" in the project with content:
      """
      tags:
        - first
      content: |
        Alpha content.
      """
    And a fragment "beta" in the project with content:
      """
      tags:
        - second
      content: |
        Beta content.
      """
    When I run scm "fragment list"
    Then the exit code should be 0
    And the output should contain "alpha"
    And the output should contain "beta"

  Scenario: List fragments in subdirectories
    Given a fragment "lang/golang" in the project with content:
      """
      tags:
        - golang
      content: |
        Go guidelines.
      """
    When I run scm "fragment list"
    Then the exit code should be 0
    And the output should contain "lang/golang"

  # ============================================================================
  # Fragment Show
  # ============================================================================

  Scenario: Show fragment displays content
    Given a fragment "show-test" in the project with content:
      """
      tags:
        - demo
      content: |
        This is the fragment content to display.
      """
    When I run scm "fragment show show-test"
    Then the exit code should be 0
    And the output should contain "This is the fragment content to display"

  Scenario: Show fragment displays tags
    Given a fragment "tagged" in the project with content:
      """
      tags:
        - important
        - review
      content: |
        Tagged content.
      """
    When I run scm "fragment show tagged"
    Then the exit code should be 0
    And the output should contain "important"

  Scenario: Show nonexistent fragment fails
    When I run scm "fragment show nonexistent"
    Then the exit code should be 1
    And the output should contain "not found"

  Scenario: Show fragment in subdirectory
    Given a fragment "tools/git" in the project with content:
      """
      tags:
        - git
      content: |
        Git workflow guidelines.
      """
    When I run scm "fragment show tools/git"
    Then the exit code should be 0
    And the output should contain "Git workflow guidelines"

  # ============================================================================
  # Fragment Delete
  # ============================================================================

  Scenario: Delete fragment removes file
    Given a fragment "to-delete" in the project with content:
      """
      tags:
        - temp
      content: |
        This will be deleted.
      """
    When I run scm "fragment delete to-delete"
    Then the exit code should be 0
    And the file ".scm/context-fragments/to-delete.yaml" should not exist

  Scenario: Delete nonexistent fragment fails
    When I run scm "fragment delete nonexistent"
    Then the exit code should be 1

  Scenario: Delete fragment in subdirectory
    Given a fragment "sub/dir/frag" in the project with content:
      """
      tags:
        - nested
      content: |
        Nested fragment.
      """
    When I run scm "fragment delete sub/dir/frag"
    Then the exit code should be 0
    And the file ".scm/context-fragments/sub/dir/frag.yaml" should not exist
