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
    Given a profile file "my-profile" with:
      """
      description: My test profile
      bundles:
        - bundle-one
      """
    When I run scm "profile list"
    Then the exit code should be 0
    And the output should contain "my-profile"

  Scenario: List profiles shows multiple profiles
    Given a profile file "alpha" with:
      """
      description: Alpha profile
      bundles:
        - bundle-a
      """
    And a profile file "beta" with:
      """
      description: Beta profile
      bundles:
        - bundle-b
      """
    When I run scm "profile list"
    Then the exit code should be 0
    And the output should contain "alpha"
    And the output should contain "beta"

  # ============================================================================
  # Profile Show
  # ============================================================================

  Scenario: Show profile displays details
    Given a profile file "detailed" with:
      """
      description: A detailed profile
      bundles:
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
  # Profile Create
  # ============================================================================

  Scenario: Create profile with bundles
    When I run scm "profile create new-profile -b bundle-one -b bundle-two"
    Then the exit code should be 0
    And the output should contain "Created profile"
    When I run scm "profile show new-profile"
    Then the exit code should be 0
    And the output should contain "bundle-one"
    And the output should contain "bundle-two"

  Scenario: Create profile with description
    When I run scm "profile create described --description TestProfile -b test-bundle"
    Then the exit code should be 0
    When I run scm "profile show described"
    Then the exit code should be 0
    And the output should contain "TestProfile"

  Scenario: Create profile with parent
    Given a profile file "base" with:
      """
      description: Base profile
      bundles:
        - common-bundle
      """
    When I run scm "profile create child --parent base -b extra-bundle"
    Then the exit code should be 0
    When I run scm "profile show child"
    Then the exit code should be 0
    And the output should contain "base"

  Scenario: Create duplicate profile fails
    Given a profile file "existing" with:
      """
      description: Existing profile
      bundles:
        - some-bundle
      """
    When I run scm "profile create existing -b new-bundle"
    Then the exit code should be 1
    And the output should contain "already exists"

  # ============================================================================
  # Profile Update
  # ============================================================================

  Scenario: Modify profile adds bundles
    Given a profile file "to-update" with:
      """
      description: Original description
      bundles:
        - original-bundle
      """
    When I run scm "profile modify to-update --add-bundle new-bundle"
    Then the exit code should be 0
    When I run scm "profile show to-update"
    Then the exit code should be 0
    And the output should contain "new-bundle"

  Scenario: Modify profile changes description
    Given a profile file "update-desc" with:
      """
      description: OldDescription
      bundles:
        - some-bundle
      """
    When I run scm "profile modify update-desc --description NewDescription"
    Then the exit code should be 0
    When I run scm "profile show update-desc"
    Then the exit code should be 0
    And the output should contain "NewDescription"

  Scenario: Modify profile removes bundle
    Given a profile file "has-bundles" with:
      """
      description: Has bundles
      bundles:
        - keep-bundle
        - old-bundle
      """
    When I run scm "profile modify has-bundles --remove-bundle old-bundle"
    Then the exit code should be 0
    And the output should contain "Removed bundle"
    When I run scm "profile show has-bundles"
    Then the exit code should be 0
    And the output should contain "keep-bundle"
    And the output should not contain "old-bundle"

  Scenario: Modify profile adds parent
    Given a profile file "base-profile" with:
      """
      description: Base
      bundles:
        - base-bundle
      """
    And a profile file "child-profile" with:
      """
      description: Child
      bundles:
        - child-bundle
      """
    When I run scm "profile modify child-profile --add-parent base-profile"
    Then the exit code should be 0
    And the output should contain "Added parent"
    When I run scm "profile show child-profile"
    Then the exit code should be 0
    And the output should contain "base-profile"

  Scenario: Modify profile removes parent
    Given a profile file "base-one" with:
      """
      description: Base one
      bundles:
        - base-one-bundle
      """
    And a profile file "base-two" with:
      """
      description: Base two
      bundles:
        - base-two-bundle
      """
    And a profile file "child" with:
      """
      description: Child with parents
      bundles:
        - child-bundle
      parents:
        - base-one
        - base-two
      """
    When I run scm "profile modify child --remove-parent base-one"
    Then the exit code should be 0
    And the output should contain "Removed parent"
    When I run scm "profile show child"
    Then the exit code should be 0
    And the output should contain "base-two"
    And the output should not contain "base-one"

  Scenario: Modify nonexistent profile fails
    When I run scm "profile modify nonexistent --add-bundle bundle"
    Then the exit code should be 1
    And the output should contain "not found"

  # ============================================================================
  # Profile Delete
  # ============================================================================

  Scenario: Delete profile removes it
    Given a profile file "to-remove" with:
      """
      description: Will be removed
      bundles:
        - bundle
      """
    And a profile file "keep-this" with:
      """
      description: Should remain
      bundles:
        - other
      """
    When I run scm "profile delete to-remove"
    Then the exit code should be 0
    When I run scm "profile show to-remove"
    Then the exit code should be 1
    When I run scm "profile show keep-this"
    Then the exit code should be 0

  Scenario: Delete nonexistent profile fails
    When I run scm "profile delete nonexistent"
    Then the exit code should be 1
    And the output should contain "not found"

