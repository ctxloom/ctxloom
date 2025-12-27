Feature: Persona management
  As a user
  I want to manage personas
  So that I can quickly switch between different context configurations

  Background:
    Given a project with mlcm initialized

  # ============================================================================
  # Persona List
  # ============================================================================

  Scenario: List personas when none exist
    When I run mlcm "persona list"
    Then the exit code should be 0
    And the output should contain "No personas"

  Scenario: List personas shows defined personas
    Given a config file with:
      """
      personas:
        my-persona:
          description: My test persona
          fragments:
            - fragment-one
      """
    When I run mlcm "persona list"
    Then the exit code should be 0
    And the output should contain "my-persona"

  Scenario: List personas shows multiple personas
    Given a config file with:
      """
      personas:
        alpha:
          description: Alpha persona
          fragments:
            - frag-a
        beta:
          description: Beta persona
          fragments:
            - frag-b
      """
    When I run mlcm "persona list"
    Then the exit code should be 0
    And the output should contain "alpha"
    And the output should contain "beta"

  # ============================================================================
  # Persona Show
  # ============================================================================

  Scenario: Show persona displays details
    Given a config file with:
      """
      personas:
        detailed:
          description: A detailed persona
          fragments:
            - coding-standards
            - security
          variables:
            language: Go
      """
    When I run mlcm "persona show detailed"
    Then the exit code should be 0
    And the output should contain "A detailed persona"
    And the output should contain "coding-standards"
    And the output should contain "language"
    And the output should contain "Go"

  Scenario: Show nonexistent persona fails
    When I run mlcm "persona show nonexistent"
    Then the exit code should be 1
    And the output should contain "not found"

  # ============================================================================
  # Persona Add
  # ============================================================================

  Scenario: Add persona with fragments
    When I run mlcm "persona add new-persona -f fragment-one -f fragment-two"
    Then the exit code should be 0
    And the output should contain "Added persona"
    When I run mlcm "persona show new-persona"
    Then the exit code should be 0
    And the output should contain "fragment-one"
    And the output should contain "fragment-two"

  Scenario: Add persona with description
    When I run mlcm "persona add described --description TestPersona -f test-frag"
    Then the exit code should be 0
    When I run mlcm "persona show described"
    Then the exit code should be 0
    And the output should contain "TestPersona"

  Scenario: Add persona with parent
    Given a config file with:
      """
      personas:
        base:
          description: Base persona
          fragments:
            - common-fragment
      """
    When I run mlcm "persona add child --parent base -f extra-fragment"
    Then the exit code should be 0
    When I run mlcm "persona show child"
    Then the exit code should be 0
    And the output should contain "base"

  Scenario: Add duplicate persona fails
    Given a config file with:
      """
      personas:
        existing:
          description: Existing persona
          fragments:
            - some-fragment
      """
    When I run mlcm "persona add existing -f new-fragment"
    Then the exit code should be 1
    And the output should contain "already exists"

  # ============================================================================
  # Persona Update
  # ============================================================================

  Scenario: Update persona adds fragments
    Given a config file with:
      """
      personas:
        to-update:
          description: Original description
          fragments:
            - original-fragment
      """
    When I run mlcm "persona update to-update --add-fragment new-fragment"
    Then the exit code should be 0
    When I run mlcm "persona show to-update"
    Then the exit code should be 0
    And the output should contain "new-fragment"

  Scenario: Update persona changes description
    Given a config file with:
      """
      personas:
        update-desc:
          description: OldDescription
          fragments:
            - some-fragment
      """
    When I run mlcm "persona update update-desc --description NewDescription"
    Then the exit code should be 0
    When I run mlcm "persona show update-desc"
    Then the exit code should be 0
    And the output should contain "NewDescription"

  Scenario: Update persona removes fragment
    Given a config file with:
      """
      personas:
        remove-frag:
          description: Has fragments
          fragments:
            - keep-fragment
            - remove-fragment
      """
    When I run mlcm "persona update remove-frag --remove-fragment remove-fragment"
    Then the exit code should be 0
    And the output should contain "Removed fragment"
    When I run mlcm "persona show remove-frag"
    Then the exit code should be 0
    And the output should contain "keep-fragment"
    And the output should not contain "remove-fragment"

  Scenario: Update persona adds generator
    Given a config file with:
      """
      personas:
        add-gen:
          description: Will add generator
          fragments:
            - some-fragment
      """
    When I run mlcm "persona update add-gen --add-generator my-generator"
    Then the exit code should be 0
    And the output should contain "Added generator"
    When I run mlcm "persona show add-gen"
    Then the exit code should be 0
    And the output should contain "my-generator"

  Scenario: Update persona removes generator
    Given a config file with:
      """
      personas:
        remove-gen:
          description: Has generators
          fragments:
            - some-fragment
          generators:
            - keep-generator
            - remove-generator
      """
    When I run mlcm "persona update remove-gen --remove-generator remove-generator"
    Then the exit code should be 0
    And the output should contain "Removed generator"
    When I run mlcm "persona show remove-gen"
    Then the exit code should be 0
    And the output should contain "keep-generator"
    And the output should not contain "remove-generator"

  Scenario: Update persona adds parent
    Given a config file with:
      """
      personas:
        base-persona:
          description: Base
          fragments:
            - base-fragment
        child-persona:
          description: Child
          fragments:
            - child-fragment
      """
    When I run mlcm "persona update child-persona --add-parent base-persona"
    Then the exit code should be 0
    And the output should contain "Added parent"
    When I run mlcm "persona show child-persona"
    Then the exit code should be 0
    And the output should contain "base-persona"

  Scenario: Update persona removes parent
    Given a config file with:
      """
      personas:
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
    When I run mlcm "persona update child --remove-parent base-one"
    Then the exit code should be 0
    And the output should contain "Removed parent"
    When I run mlcm "persona show child"
    Then the exit code should be 0
    And the output should contain "base-two"
    And the output should not contain "base-one"

  Scenario: Update nonexistent persona fails
    When I run mlcm "persona update nonexistent --add-fragment fragment"
    Then the exit code should be 1
    And the output should contain "not found"

  # ============================================================================
  # Persona Remove
  # ============================================================================

  Scenario: Remove persona deletes it
    Given a config file with:
      """
      personas:
        to-remove:
          description: Will be removed
          fragments:
            - fragment
        keep-this:
          description: Should remain
          fragments:
            - other
      """
    When I run mlcm "persona remove to-remove"
    Then the exit code should be 0
    When I run mlcm "persona show to-remove"
    Then the exit code should be 1
    When I run mlcm "persona show keep-this"
    Then the exit code should be 0

  Scenario: Remove nonexistent persona fails
    When I run mlcm "persona remove nonexistent"
    Then the exit code should be 1
    And the output should contain "not found"

  # ============================================================================
  # Persona with Generators
  # ============================================================================

  Scenario: Add persona with generators
    When I run mlcm "persona add with-gen -f fragment -g git-context"
    Then the exit code should be 0
    When I run mlcm "persona show with-gen"
    Then the exit code should be 0
    And the output should contain "git-context"

