Feature: Shell completion
  As a user
  I want to generate shell completion scripts
  So that I can have tab completion for scm commands

  # ============================================================================
  # Bash Completion
  # ============================================================================

  Scenario: Generate bash completion script
    When I run scm "completion bash"
    Then the exit code should be 0
    And the output should contain "bash completion"
    And the output should contain "scm"

  # ============================================================================
  # Zsh Completion
  # ============================================================================

  Scenario: Generate zsh completion script
    When I run scm "completion zsh"
    Then the exit code should be 0
    And the output should contain "#compdef scm"

  # ============================================================================
  # Fish Completion
  # ============================================================================

  Scenario: Generate fish completion script
    When I run scm "completion fish"
    Then the exit code should be 0
    And the output should contain "fish"
    And the output should contain "scm"

  # ============================================================================
  # PowerShell Completion
  # ============================================================================

  Scenario: Generate powershell completion script
    When I run scm "completion powershell"
    Then the exit code should be 0
    And the output should contain "scm"
    And the output should contain "Register-ArgumentCompleter"

  # ============================================================================
  # Help Text
  # ============================================================================

  Scenario: Completion help shows available shells
    When I run scm "completion --help"
    Then the exit code should be 0
    And the output should contain "bash"
    And the output should contain "zsh"
    And the output should contain "fish"
    And the output should contain "powershell"

