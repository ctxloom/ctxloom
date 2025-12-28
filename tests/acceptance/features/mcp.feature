Feature: MCP server
  As an AI agent
  I want to interact with scm via MCP protocol
  So that I can access context fragments programmatically

  Background:
    Given a project with scm initialized

  # ============================================================================
  # Initialize
  # ============================================================================

  Scenario: MCP server responds to initialize
    When I send MCP initialize request
    Then the exit code should be 0
    And the MCP response should contain "protocolVersion"
    And the MCP response should contain "scm"

  # ============================================================================
  # Tools List
  # ============================================================================

  Scenario: MCP server lists available tools
    When I send MCP tools/list request
    Then the exit code should be 0
    And the MCP response should contain "list_fragments"
    And the MCP response should contain "get_fragment"
    And the MCP response should contain "list_profiles"
    And the MCP response should contain "get_profile"
    And the MCP response should contain "set_profile"
    And the MCP response should contain "assemble_context"
    And the MCP response should contain "list_prompts"
    And the MCP response should contain "get_prompt"

  # ============================================================================
  # List Fragments
  # ============================================================================

  Scenario: List fragments returns empty when none exist
    When I send MCP tools/call "list_fragments"
    Then the exit code should be 0
    And the MCP response should contain "count"
    And the MCP response should contain "0"

  Scenario: List fragments returns available fragments
    Given a fragment "test-frag" in the project with content:
      """
      tags:
        - testing
      content: |
        Test content.
      """
    When I send MCP tools/call "list_fragments"
    Then the exit code should be 0
    And the MCP response should contain "test-frag"
    And the MCP response should contain "testing"

  Scenario: List fragments filters by tag
    Given a fragment "tagged-frag" in the project with content:
      """
      tags:
        - special
      content: |
        Special content.
      """
    And a fragment "other-frag" in the project with content:
      """
      tags:
        - other
      content: |
        Other content.
      """
    When I send MCP tools/call "list_fragments" with:
      """
      {"tags": ["special"]}
      """
    Then the exit code should be 0
    And the MCP response should contain "tagged-frag"
    And the MCP response should not contain "other-frag"

  # ============================================================================
  # Get Fragment
  # ============================================================================

  Scenario: Get fragment returns content
    Given a fragment "get-test" in the project with content:
      """
      tags:
        - demo
      content: |
        This is the fragment content.
      """
    When I send MCP tools/call "get_fragment" with:
      """
      {"name": "get-test"}
      """
    Then the exit code should be 0
    And the MCP response should contain "This is the fragment content"
    And the MCP response should contain "demo"

  Scenario: Get nonexistent fragment returns error
    When I send MCP tools/call "get_fragment" with:
      """
      {"name": "nonexistent"}
      """
    Then the exit code should be 0
    And the MCP response should contain "not found"

  Scenario: Get fragment without name returns error
    When I send MCP tools/call "get_fragment" with:
      """
      {}
      """
    Then the exit code should be 0
    And the MCP response should contain "name is required"

  # ============================================================================
  # List Profiles
  # ============================================================================

  Scenario: List profiles returns empty when none defined
    When I send MCP tools/call "list_profiles"
    Then the exit code should be 0
    And the MCP response should contain "count"

  Scenario: List profiles returns defined profiles
    Given a config file with:
      """
      profiles:
        developer:
          description: Developer profile
          fragments:
            - code-style
      """
    When I send MCP tools/call "list_profiles"
    Then the exit code should be 0
    And the MCP response should contain "developer"
    And the MCP response should contain "Developer profile"

  # ============================================================================
  # Get Profile
  # ============================================================================

  Scenario: Get profile returns configuration
    Given a config file with:
      """
      profiles:
        detailed:
          description: A detailed profile
          fragments:
            - frag-one
            - frag-two
          variables:
            language: Go
      """
    When I send MCP tools/call "get_profile" with:
      """
      {"name": "detailed"}
      """
    Then the exit code should be 0
    And the MCP response should contain "A detailed profile"
    And the MCP response should contain "frag-one"
    And the MCP response should contain "frag-two"
    And the MCP response should contain "language"
    And the MCP response should contain "Go"

  Scenario: Get nonexistent profile returns error
    When I send MCP tools/call "get_profile" with:
      """
      {"name": "nonexistent"}
      """
    Then the exit code should be 0
    And the MCP response should contain "not found"

  # ============================================================================
  # Set Profile
  # ============================================================================

  Scenario: Set profile for session
    Given a config file with:
      """
      profiles:
        session-profile:
          description: Session test
          fragments:
            - test-frag
      """
    When I send MCP tools/call "set_profile" with:
      """
      {"name": "session-profile"}
      """
    Then the exit code should be 0
    And the MCP response should contain "session-profile"
    And the MCP response should contain "Session profile set"

  Scenario: Set nonexistent profile returns error
    When I send MCP tools/call "set_profile" with:
      """
      {"name": "nonexistent"}
      """
    Then the exit code should be 0
    And the MCP response should contain "not found"

  # ============================================================================
  # List Prompts
  # ============================================================================

  Scenario: List prompts returns empty when none exist
    When I send MCP tools/call "list_prompts"
    Then the exit code should be 0
    And the MCP response should contain "count"

  Scenario: List prompts returns available prompts
    Given a prompt "my-prompt" in the project with content:
      """
      description: My prompt
      content: |
        Prompt content here.
      """
    When I send MCP tools/call "list_prompts"
    Then the exit code should be 0
    And the MCP response should contain "my-prompt"

  # ============================================================================
  # Get Prompt
  # ============================================================================

  Scenario: Get prompt returns content
    Given a prompt "get-prompt" in the project with content:
      """
      description: Test prompt
      content: |
        This is the prompt content.
      """
    When I send MCP tools/call "get_prompt" with:
      """
      {"name": "get-prompt"}
      """
    Then the exit code should be 0
    And the MCP response should contain "This is the prompt content"

  Scenario: Get nonexistent prompt returns error
    When I send MCP tools/call "get_prompt" with:
      """
      {"name": "nonexistent"}
      """
    Then the exit code should be 0
    And the MCP response should contain "not found"

  # ============================================================================
  # Assemble Context
  # ============================================================================

  Scenario: Assemble context with fragment
    Given a fragment "assemble-frag" in the project with content:
      """
      tags:
        - assemble
      content: |
        Assembled fragment content.
      """
    When I send MCP tools/call "assemble_context" with:
      """
      {"fragments": ["assemble-frag"]}
      """
    Then the exit code should be 0
    And the MCP response should contain "Assembled fragment content"

  Scenario: Assemble context with profile
    Given a fragment "profile-frag" in the project with content:
      """
      tags:
        - profile
      content: |
        Profile fragment content.
      """
    And a config file with:
      """
      profiles:
        test-profile:
          description: Test
          fragments:
            - profile-frag
      """
    When I send MCP tools/call "assemble_context" with:
      """
      {"profile": "test-profile"}
      """
    Then the exit code should be 0
    And the MCP response should contain "Profile fragment content"

  Scenario: Assemble context with tags
    Given a fragment "tag-frag" in the project with content:
      """
      tags:
        - security
      content: |
        Security guidelines content.
      """
    When I send MCP tools/call "assemble_context" with:
      """
      {"tags": ["security"]}
      """
    Then the exit code should be 0
    And the MCP response should contain "Security guidelines content"

  Scenario: Assemble context with variable substitution
    Given a fragment "var-frag" in the project with content:
      """
      tags:
        - vars
      content: |
        The language is {{language}}.
      """
    And a config file with:
      """
      profiles:
        var-profile:
          description: Variables
          fragments:
            - var-frag
          variables:
            language: Python
      """
    When I send MCP tools/call "assemble_context" with:
      """
      {"profile": "var-profile"}
      """
    Then the exit code should be 0
    And the MCP response should contain "The language is Python"

  # ============================================================================
  # Error Handling
  # ============================================================================

  Scenario: Unknown tool returns error
    Given a config file with:
      """
      profiles: {}
      """
    When I send MCP tools/call "unknown_tool"
    Then the exit code should be 0
    And the MCP response should have error containing "Unknown tool"

