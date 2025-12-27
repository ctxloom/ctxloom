Feature: Shell completion
  As a user
  I want to generate shell completion scripts
  So that I can have tab completion for mlcm commands

  # ============================================================================
  # Bash Completion
  # ============================================================================

  Scenario: Generate bash completion script
    When I run mlcm "completion bash"
    Then the exit code should be 0
    And the output should contain "bash completion"
    And the output should contain "mlcm"

  # ============================================================================
  # Zsh Completion
  # ============================================================================

  Scenario: Generate zsh completion script
    When I run mlcm "completion zsh"
    Then the exit code should be 0
    And the output should contain "#compdef mlcm"

  # ============================================================================
  # Fish Completion
  # ============================================================================

  Scenario: Generate fish completion script
    When I run mlcm "completion fish"
    Then the exit code should be 0
    And the output should contain "fish"
    And the output should contain "mlcm"

  # ============================================================================
  # PowerShell Completion
  # ============================================================================

  Scenario: Generate powershell completion script
    When I run mlcm "completion powershell"
    Then the exit code should be 0
    And the output should contain "mlcm"
    And the output should contain "Register-ArgumentCompleter"

  # ============================================================================
  # Help Text
  # ============================================================================

  Scenario: Completion help shows available shells
    When I run mlcm "completion --help"
    Then the exit code should be 0
    And the output should contain "bash"
    And the output should contain "zsh"
    And the output should contain "fish"
    And the output should contain "powershell"

