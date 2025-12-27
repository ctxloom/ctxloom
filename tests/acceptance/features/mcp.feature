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
    And the MCP response should contain "list_personas"
    And the MCP response should contain "get_persona"
    And the MCP response should contain "set_persona"
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
  # List Personas
  # ============================================================================

  Scenario: List personas returns empty when none defined
    When I send MCP tools/call "list_personas"
    Then the exit code should be 0
    And the MCP response should contain "count"

  Scenario: List personas returns defined personas
    Given a config file with:
      """
      personas:
        developer:
          description: Developer persona
          fragments:
            - code-style
      """
    When I send MCP tools/call "list_personas"
    Then the exit code should be 0
    And the MCP response should contain "developer"
    And the MCP response should contain "Developer persona"

  # ============================================================================
  # Get Persona
  # ============================================================================

  Scenario: Get persona returns configuration
    Given a config file with:
      """
      personas:
        detailed:
          description: A detailed persona
          fragments:
            - frag-one
            - frag-two
          variables:
            language: Go
      """
    When I send MCP tools/call "get_persona" with:
      """
      {"name": "detailed"}
      """
    Then the exit code should be 0
    And the MCP response should contain "A detailed persona"
    And the MCP response should contain "frag-one"
    And the MCP response should contain "frag-two"
    And the MCP response should contain "language"
    And the MCP response should contain "Go"

  Scenario: Get nonexistent persona returns error
    When I send MCP tools/call "get_persona" with:
      """
      {"name": "nonexistent"}
      """
    Then the exit code should be 0
    And the MCP response should contain "not found"

  # ============================================================================
  # Set Persona
  # ============================================================================

  Scenario: Set persona for session
    Given a config file with:
      """
      personas:
        session-persona:
          description: Session test
          fragments:
            - test-frag
      """
    When I send MCP tools/call "set_persona" with:
      """
      {"name": "session-persona"}
      """
    Then the exit code should be 0
    And the MCP response should contain "session-persona"
    And the MCP response should contain "Session persona set"

  Scenario: Set nonexistent persona returns error
    When I send MCP tools/call "set_persona" with:
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

  Scenario: Assemble context with persona
    Given a fragment "persona-frag" in the project with content:
      """
      tags:
        - persona
      content: |
        Persona fragment content.
      """
    And a config file with:
      """
      personas:
        test-persona:
          description: Test
          fragments:
            - persona-frag
      """
    When I send MCP tools/call "assemble_context" with:
      """
      {"persona": "test-persona"}
      """
    Then the exit code should be 0
    And the MCP response should contain "Persona fragment content"

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
      personas:
        var-persona:
          description: Variables
          fragments:
            - var-frag
          variables:
            language: Python
      """
    When I send MCP tools/call "assemble_context" with:
      """
      {"persona": "var-persona"}
      """
    Then the exit code should be 0
    And the MCP response should contain "The language is Python"

  # ============================================================================
  # Error Handling
  # ============================================================================

  Scenario: Unknown tool returns error
    Given a config file with:
      """
      personas: {}
      """
    When I send MCP tools/call "unknown_tool"
    Then the exit code should be 0
    And the MCP response should have error containing "Unknown tool"

