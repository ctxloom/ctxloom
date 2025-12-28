Feature: Profile management
  As a user
  I want to manage profiles
  So that I can quickly switch between different context configurations

  Background:
    Given a project with scm initialized

  # ============================================================================
  # Profile List
  # ============================================================================

  Scenario: List profiles when none exist
    When I run scm "profile list"
    Then the exit code should be 0
    And the output should contain "No profiles"

  Scenario: List profiles shows defined profiles
    Given a config file with:
      """
      profiles:
        my-profile:
          description: My test profile
          fragments:
            - fragment-one
      """
    When I run scm "profile list"
    Then the exit code should be 0
    And the output should contain "my-profile"

  Scenario: List profiles shows multiple profiles
    Given a config file with:
      """
      profiles:
        alpha:
          description: Alpha profile
          fragments:
            - frag-a
        beta:
          description: Beta profile
          fragments:
            - frag-b
      """
    When I run scm "profile list"
    Then the exit code should be 0
    And the output should contain "alpha"
    And the output should contain "beta"

  # ============================================================================
  # Profile Show
  # ============================================================================

  Scenario: Show profile displays details
    Given a config file with:
      """
      profiles:
        detailed:
          description: A detailed profile
          fragments:
            - coding-standards
            - security
          variables:
            language: Go
      """
    When I run scm "profile show detailed"
    Then the exit code should be 0
    And the output should contain "A detailed profile"
    And the output should contain "coding-standards"
    And the output should contain "language"
    And the output should contain "Go"

  Scenario: Show nonexistent profile fails
    When I run scm "profile show nonexistent"
    Then the exit code should be 1
    And the output should contain "not found"

  # ============================================================================
  # Profile Add
  # ============================================================================

  Scenario: Add profile with fragments
    When I run scm "profile add new-profile -f fragment-one -f fragment-two"
    Then the exit code should be 0
    And the output should contain "Added profile"
    When I run scm "profile show new-profile"
    Then the exit code should be 0
    And the output should contain "fragment-one"
    And the output should contain "fragment-two"

  Scenario: Add profile with description
    When I run scm "profile add described --description TestProfile -f test-frag"
    Then the exit code should be 0
    When I run scm "profile show described"
    Then the exit code should be 0
    And the output should contain "TestProfile"

  Scenario: Add profile with parent
    Given a config file with:
      """
      profiles:
        base:
          description: Base profile
          fragments:
            - common-fragment
      """
    When I run scm "profile add child --parent base -f extra-fragment"
    Then the exit code should be 0
    When I run scm "profile show child"
    Then the exit code should be 0
    And the output should contain "base"

  Scenario: Add duplicate profile fails
    Given a config file with:
      """
      profiles:
        existing:
          description: Existing profile
          fragments:
            - some-fragment
      """
    When I run scm "profile add existing -f new-fragment"
    Then the exit code should be 1
    And the output should contain "already exists"

  # ============================================================================
  # Profile Update
  # ============================================================================

  Scenario: Update profile adds fragments
    Given a config file with:
      """
      profiles:
        to-update:
          description: Original description
          fragments:
            - original-fragment
      """
    When I run scm "profile update to-update --add-fragment new-fragment"
    Then the exit code should be 0
    When I run scm "profile show to-update"
    Then the exit code should be 0
    And the output should contain "new-fragment"

  Scenario: Update profile changes description
    Given a config file with:
      """
      profiles:
        update-desc:
          description: OldDescription
          fragments:
            - some-fragment
      """
    When I run scm "profile update update-desc --description NewDescription"
    Then the exit code should be 0
    When I run scm "profile show update-desc"
    Then the exit code should be 0
    And the output should contain "NewDescription"

  Scenario: Update profile removes fragment
    Given a config file with:
      """
      profiles:
        remove-frag:
          description: Has fragments
          fragments:
            - keep-fragment
            - remove-fragment
      """
    When I run scm "profile update remove-frag --remove-fragment remove-fragment"
    Then the exit code should be 0
    And the output should contain "Removed fragment"
    When I run scm "profile show remove-frag"
    Then the exit code should be 0
    And the output should contain "keep-fragment"
    And the output should not contain "remove-fragment"

  Scenario: Update profile adds generator
    Given a config file with:
      """
      profiles:
        add-gen:
          description: Will add generator
          fragments:
            - some-fragment
      """
    When I run scm "profile update add-gen --add-generator my-generator"
    Then the exit code should be 0
    And the output should contain "Added generator"
    When I run scm "profile show add-gen"
    Then the exit code should be 0
    And the output should contain "my-generator"

  Scenario: Update profile removes generator
    Given a config file with:
      """
      profiles:
        remove-gen:
          description: Has generators
          fragments:
            - some-fragment
          generators:
            - keep-generator
            - remove-generator
      """
    When I run scm "profile update remove-gen --remove-generator remove-generator"
    Then the exit code should be 0
    And the output should contain "Removed generator"
    When I run scm "profile show remove-gen"
    Then the exit code should be 0
    And the output should contain "keep-generator"
    And the output should not contain "remove-generator"

  Scenario: Update profile adds parent
    Given a config file with:
      """
      profiles:
        base-profile:
          description: Base
          fragments:
            - base-fragment
        child-profile:
          description: Child
          fragments:
            - child-fragment
      """
    When I run scm "profile update child-profile --add-parent base-profile"
    Then the exit code should be 0
    And the output should contain "Added parent"
    When I run scm "profile show child-profile"
    Then the exit code should be 0
    And the output should contain "base-profile"

  Scenario: Update profile removes parent
    Given a config file with:
      """
      profiles:
        base-one:
          description: Base one
          fragments:
            - base-one-frag
        base-two:
          description: Base two
          fragments:
            - base-two-frag
        child:
          description: Child with parents
          fragments:
            - child-frag
          parents:
            - base-one
            - base-two
      """
    When I run scm "profile update child --remove-parent base-one"
    Then the exit code should be 0
    And the output should contain "Removed parent"
    When I run scm "profile show child"
    Then the exit code should be 0
    And the output should contain "base-two"
    And the output should not contain "base-one"

  Scenario: Update nonexistent profile fails
    When I run scm "profile update nonexistent --add-fragment fragment"
    Then the exit code should be 1
    And the output should contain "not found"

  # ============================================================================
  # Profile Remove
  # ============================================================================

  Scenario: Remove profile deletes it
    Given a config file with:
      """
      profiles:
        to-remove:
          description: Will be removed
          fragments:
            - fragment
        keep-this:
          description: Should remain
          fragments:
            - other
      """
    When I run scm "profile remove to-remove"
    Then the exit code should be 0
    When I run scm "profile show to-remove"
    Then the exit code should be 1
    When I run scm "profile show keep-this"
    Then the exit code should be 0

  Scenario: Remove nonexistent profile fails
    When I run scm "profile remove nonexistent"
    Then the exit code should be 1
    And the output should contain "not found"

  # ============================================================================
  # Profile with Generators
  # ============================================================================

  Scenario: Add profile with generators
    When I run scm "profile add with-gen -f fragment -g my-generator"
    Then the exit code should be 0
    When I run scm "profile show with-gen"
    Then the exit code should be 0
    And the output should contain "my-generator"

