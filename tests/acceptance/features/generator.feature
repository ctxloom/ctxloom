Feature: Generator management
  As a user
  I want to manage context generators
  So that I can include dynamic context in AI interactions

  Background:
    Given a project with scm initialized

  # ============================================================================
  # Generator List
  # ============================================================================

  Scenario: List generators when none exist
    When I run scm "generator list"
    Then the exit code should be 0
    And the output should contain "No generators"

  Scenario: List generators shows defined generators
    Given a config file with:
      """
      generators:
        my-gen:
          description: My test generator
          command: echo test
      """
    When I run scm "generator list"
    Then the exit code should be 0
    And the output should contain "my-gen"

  Scenario: List generators shows multiple generators
    Given a config file with:
      """
      generators:
        gen-one:
          description: First generator
          command: echo one
        gen-two:
          description: Second generator
          command: echo two
      """
    When I run scm "generator list"
    Then the exit code should be 0
    And the output should contain "gen-one"
    And the output should contain "gen-two"

  # ============================================================================
  # Generator Show
  # ============================================================================

  Scenario: Show generator displays details
    Given a config file with:
      """
      generators:
        detailed:
          description: A detailed generator
          command: /usr/bin/my-generator
          args:
            - --verbose
            - --format=json
      """
    When I run scm "generator show detailed"
    Then the exit code should be 0
    And the output should contain "A detailed generator"
    And the output should contain "/usr/bin/my-generator"

  Scenario: Show nonexistent generator fails
    When I run scm "generator show nonexistent"
    Then the exit code should be 1
    And the output should contain "not found"

  # ============================================================================
  # Generator Add
  # ============================================================================

  Scenario: Add generator with command
    When I run scm "generator add my-gen -c echo"
    Then the exit code should be 0
    And the output should contain "Added generator"
    When I run scm "generator show my-gen"
    Then the exit code should be 0
    And the output should contain "echo"

  Scenario: Add generator with description
    When I run scm "generator add described -c echo -d TestGenerator"
    Then the exit code should be 0
    When I run scm "generator show described"
    Then the exit code should be 0
    And the output should contain "TestGenerator"

  Scenario: Add generator with arguments
    When I run scm "generator add with-args -c echo -a hello -a world"
    Then the exit code should be 0
    When I run scm "generator show with-args"
    Then the exit code should be 0
    And the output should contain "echo"
    And the output should contain "hello"
    And the output should contain "world"

  Scenario: Run generator with arguments
    Given a config file with:
      """
      generators:
        echo-args:
          description: Echo with args
          command: echo
          args:
            - arg1
            - arg2
      """
    When I run scm "generator run echo-args"
    Then the exit code should be 0
    And the output should contain "arg1"
    And the output should contain "arg2"

  Scenario: Add duplicate generator fails
    Given a config file with:
      """
      generators:
        existing:
          description: Existing generator
          command: echo existing
      """
    When I run scm "generator add existing -c echo"
    Then the exit code should be 1
    And the output should contain "already exists"

  # ============================================================================
  # Generator Remove
  # ============================================================================

  Scenario: Remove generator deletes it
    Given a config file with:
      """
      generators:
        to-remove:
          description: Will be removed
          command: echo remove
        keep-this:
          description: Should remain
          command: echo keep
      """
    When I run scm "generator remove to-remove"
    Then the exit code should be 0
    When I run scm "generator show to-remove"
    Then the exit code should be 1
    When I run scm "generator show keep-this"
    Then the exit code should be 0

  Scenario: Remove nonexistent generator fails
    When I run scm "generator remove nonexistent"
    Then the exit code should be 1
    And the output should contain "not found"

  # ============================================================================
  # Generator Run
  # ============================================================================

  Scenario: Run generator executes command
    Given a config file with:
      """
      generators:
        echo-gen:
          description: Echo generator
          command: echo
          args:
            - "Hello from generator"
      """
    When I run scm "generator run echo-gen"
    Then the exit code should be 0
    And the output should contain "Hello from generator"

  Scenario: Run nonexistent generator fails
    When I run scm "generator run nonexistent"
    Then the exit code should be 1
    And the output should contain "not found"

  # ============================================================================
  # Generator Alias
  # ============================================================================

  Scenario: Use gen alias for generator
    Given a config file with:
      """
      generators:
        alias-test:
          description: Alias test
          command: echo alias
      """
    When I run scm "gen list"
    Then the exit code should be 0
    And the output should contain "alias-test"
