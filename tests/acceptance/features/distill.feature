Feature: Distill command
  As a user
  I want to create distilled versions of fragments
  So that I can reduce token usage while preserving meaning

  Background:
    Given a project with scm initialized
    And a mock LM is configured

  # ============================================================================
  # Basic Distill Operations
  # ============================================================================

  Scenario: Distill creates distilled version of fragment
    Given a fragment "to-distill" in the project with content:
      """
      tags:
        - distill
      content: |
        This is a verbose fragment with lots of unnecessary words.
        It contains detailed explanations that could be shortened.
      """
    And the mock LM will respond with:
      """
      Concise version of the fragment.
      """
    When I run scm "distill -f to-distill"
    Then the exit code should be 0
    And the output should contain "Distilling"
    And the output should contain "to-distill"
    And the output should contain "OK"

  Scenario: Distill multiple fragments
    Given a fragment "multi-one" in the project with content:
      """
      tags:
        - multi
      content: |
        First fragment content.
      """
    And a fragment "multi-two" in the project with content:
      """
      tags:
        - multi
      content: |
        Second fragment content.
      """
    And the mock LM will respond with:
      """
      Distilled content.
      """
    When I run scm "distill -f multi-one -f multi-two"
    Then the exit code should be 0
    And the output should contain "multi-one"
    And the output should contain "multi-two"

  # ============================================================================
  # No Distill Flag
  # ============================================================================

  Scenario: Distill skips fragments with no_distill flag
    Given a fragment "protected" in the project with content:
      """
      tags:
        - protected
      no_distill: true
      content: |
        This content must not be distilled.
      """
    When I run scm "distill -f protected"
    Then the exit code should be 0
    And the output should contain "Skipping protected (no_distill)"

  # ============================================================================
  # Distill by Profile
  # ============================================================================

  Scenario: Distill fragments by profile
    Given a fragment "profile-frag" in the project with content:
      """
      tags:
        - code
      content: |
        Profile fragment content.
      """
    And a config file with:
      """
      lm:
        plugins:
          claude-code:
            default: true
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
      Distilled.
      """
    When I run scm "distill -p test-profile"
    Then the exit code should be 0
    And the output should contain "profile-frag"

  # ============================================================================
  # Distill Prompts
  # ============================================================================

  Scenario: Distill prompt
    Given a prompt "to-distill" in the project with content:
      """
      description: A verbose prompt
      content: |
        This is a verbose prompt with lots of words.
      """
    And the mock LM will respond with:
      """
      Concise prompt.
      """
    When I run scm "distill -r to-distill"
    Then the exit code should be 0
    And the output should contain "to-distill"

  # ============================================================================
  # Force Overwrite
  # ============================================================================

  Scenario: Distill with force re-distills existing
    Given a fragment "force-test" in the project with content:
      """
      tags:
        - force
      content: |
        Original content.
      distilled: |
        Previously distilled.
      content_hash: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
      """
    And the mock LM will respond with:
      """
      Second distillation.
      """
    When I run scm "distill -f force-test --force"
    Then the exit code should be 0
    And the output should contain "Re-distilling force-test"
    And the output should contain "OK"

  # ============================================================================
  # Dry Run Mode
  # ============================================================================

  Scenario: Dry run shows what would be distilled
    Given a fragment "dry-test" in the project with content:
      """
      tags:
        - dry
      content: |
        Content to distill.
      """
    When I run scm "distill -f dry-test --dry-run"
    Then the exit code should be 0
    And the output should contain "Would distill: dry-test"
    And the output should contain "Dry run complete"

  # ============================================================================
  # Error Handling
  # ============================================================================

  Scenario: Distill nonexistent fragment fails
    When I run scm "distill -f nonexistent"
    Then the exit code should be 1
    And the output should contain "not found: nonexistent"

  # ============================================================================
  # Distill Clean
  # ============================================================================

  Scenario: Distill clean removes distilled content
    Given a fragment "to-clean" in the project with content:
      """
      tags:
        - clean
      content: |
        Original content.
      distilled: |
        Distilled content.
      content_hash: "abc123"
      distilled_by: "test"
      """
    When I run scm "distill clean"
    Then the exit code should be 0
    And the output should contain "Cleaned: to-clean"

  Scenario: Distill clean dry run shows what would be cleaned
    Given a fragment "dry-clean" in the project with content:
      """
      tags:
        - clean
      content: |
        Original content.
      distilled: |
        Distilled content.
      """
    When I run scm "distill clean --dry-run"
    Then the exit code should be 0
    And the output should contain "Would clean: dry-clean"

  # ============================================================================
  # Flags
  # ============================================================================

  Scenario: Distill with prompts-only skips fragments
    Given a fragment "frag" in the project with content:
      """
      tags:
        - skip
      content: |
        Fragment content.
      """
    And a prompt "prompt" in the project with content:
      """
      description: A prompt
      content: |
        Prompt content.
      """
    And the mock LM will respond with:
      """
      Distilled.
      """
    When I run scm "distill --prompts-only"
    Then the exit code should be 0
    And the output should contain "prompts"
    And the output should not contain "fragments"

  Scenario: Distill with skip-prompts skips prompts
    Given a fragment "frag" in the project with content:
      """
      tags:
        - distill
      content: |
        Fragment content.
      """
    And a prompt "prompt" in the project with content:
      """
      description: A prompt
      content: |
        Prompt content.
      """
    And the mock LM will respond with:
      """
      Distilled.
      """
    When I run scm "distill --skip-prompts"
    Then the exit code should be 0
    And the output should contain "fragments"
    And the output should not contain "prompts"

  # ============================================================================
  # Filter Isolation (flags should not cross-distill)
  # ============================================================================

  Scenario: Distill with -P only distills prompts not fragments
    Given a fragment "isolated-frag" in the project with content:
      """
      tags:
        - isolated
      content: |
        Fragment that should not be distilled.
      """
    And a prompt "isolated-prompt" in the project with content:
      """
      description: Isolated prompt
      content: |
        Prompt content.
      """
    And the mock LM will respond with:
      """
      Distilled.
      """
    When I run scm "distill -r isolated-prompt"
    Then the exit code should be 0
    And the output should contain "1 prompts"
    And the output should not contain "fragments"
    And the output should contain "isolated-prompt"

  Scenario: Distill with -f only distills fragments not prompts
    Given a fragment "frag-only" in the project with content:
      """
      tags:
        - fragonly
      content: |
        Fragment content.
      """
    And a prompt "prompt-untouched" in the project with content:
      """
      description: Untouched prompt
      content: |
        Prompt content.
      """
    And the mock LM will respond with:
      """
      Distilled.
      """
    When I run scm "distill -f frag-only"
    Then the exit code should be 0
    And the output should contain "1 fragments"
    And the output should not contain "prompts"
    And the output should contain "frag-only"

  Scenario: Distill with -p only distills profile fragments not prompts
    Given a fragment "profile-only-frag" in the project with content:
      """
      tags:
        - profileonly
      content: |
        Profile fragment content.
      """
    And a prompt "profile-untouched" in the project with content:
      """
      description: Untouched prompt
      content: |
        Prompt content.
      """
    And a config file with:
      """
      lm:
        plugins:
          claude-code:
            default: true
            binary_path: "{{MOCK_LM_PATH}}"
      profiles:
        isolated-profile:
          description: Isolated test profile
          fragments:
            - profile-only-frag
      """
    And the mock LM will respond with:
      """
      Distilled.
      """
    When I run scm "distill -p isolated-profile"
    Then the exit code should be 0
    And the output should contain "1 fragments"
    And the output should not contain "prompts"
    And the output should contain "profile-only-frag"

  # ============================================================================
  # Subdirectory Fragments
  # ============================================================================

  Scenario: Distill fragment in subdirectory
    Given a fragment "lang/python" in the project with content:
      """
      tags:
        - python
      content: |
        Python coding guidelines from subdirectory.
      """
    And the mock LM will respond with:
      """
      Distilled Python guidelines.
      """
    When I run scm "distill -f lang/python"
    Then the exit code should be 0
    And the output should contain "lang/python"
    And the output should contain "OK"

