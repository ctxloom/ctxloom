Feature: Context assembly
  As a user
  I want to run scm with fragments
  So that context is correctly sent to the language model

  Background:
    Given a project with scm initialized
    And a mock LM is configured

  Scenario: Run with a single fragment
    Given a fragment "test-fragment" in the project with content:
      """
      tags:
        - testing
      content: |
        This is test content.
        It should be sent to the LM.
      """
    And the mock LM will respond with:
      """
      LM processed your request.
      """
    When I run scm "run -f test-fragment --print test prompt"
    Then the exit code should be 0
    And the LM should have received context containing "This is test content"
    And the LM should have received context containing "test prompt"

  Scenario: Run with multiple fragments
    Given a fragment "fragment-one" in the project with content:
      """
      tags:
        - first
      content: |
        Content from fragment one.
      """
    And a fragment "fragment-two" in the project with content:
      """
      tags:
        - second
      content: |
        Content from fragment two.
      """
    And the mock LM will respond with:
      """
      Response from mock LM.
      """
    When I run scm "run -f fragment-one -f fragment-two --print combined test"
    Then the exit code should be 0
    And the LM should have received context containing "Content from fragment one"
    And the LM should have received context containing "Content from fragment two"

  # ============================================================================
  # Variable Substitution (Mustache Templating)
  # ============================================================================

  Scenario: Simple variable substitution in fragment
    Given a fragment "var-fragment" in the project with content:
      """
      tags:
        - variables
      content: |
        The language is {{language}}.
      """
    And a config file with:
      """
      lm:
        default_plugin: claude-code
        plugins:
          claude-code:
            binary_path: "{{MOCK_LM_PATH}}"
      personas:
        lang-persona:
          description: Language persona
          fragments:
            - var-fragment
          variables:
            language: Python
      """
    And the mock LM will respond with:
      """
      OK
      """
    When I run scm "run -p lang-persona --print test"
    Then the exit code should be 0
    And the LM should have received context containing "The language is Python"

  Scenario: Multiple variables in single fragment
    Given a fragment "multi-var" in the project with content:
      """
      tags:
        - vars
      content: |
        Project: {{project_name}}
        Language: {{language}}
        Version: {{version}}
      """
    And a config file with:
      """
      lm:
        default_plugin: claude-code
        plugins:
          claude-code:
            binary_path: "{{MOCK_LM_PATH}}"
      personas:
        multi-var-persona:
          description: Multiple variables
          fragments:
            - multi-var
          variables:
            project_name: MyApp
            language: Go
            version: "1.21"
      """
    And the mock LM will respond with:
      """
      OK
      """
    When I run scm "run -p multi-var-persona --print test"
    Then the exit code should be 0
    And the LM should have received context containing "Project: MyApp"
    And the LM should have received context containing "Language: Go"
    And the LM should have received context containing "Version: 1.21"

  Scenario: Variables shared across multiple fragments
    Given a fragment "frag-one" in the project with content:
      """
      tags:
        - shared
      content: |
        Fragment one uses {{shared_var}}.
      """
    And a fragment "frag-two" in the project with content:
      """
      tags:
        - shared
      content: |
        Fragment two also uses {{shared_var}}.
      """
    And a config file with:
      """
      lm:
        default_plugin: claude-code
        plugins:
          claude-code:
            binary_path: "{{MOCK_LM_PATH}}"
      personas:
        shared-persona:
          description: Shared variables
          fragments:
            - frag-one
            - frag-two
          variables:
            shared_var: CommonValue
      """
    And the mock LM will respond with:
      """
      OK
      """
    When I run scm "run -p shared-persona --print test"
    Then the exit code should be 0
    And the LM should have received context containing "Fragment one uses CommonValue"
    And the LM should have received context containing "Fragment two also uses CommonValue"

  Scenario: Variables inherited from parent persona
    Given a fragment "child-frag" in the project with content:
      """
      tags:
        - child
      content: |
        Using {{inherited_var}} from parent.
      """
    And a config file with:
      """
      lm:
        default_plugin: claude-code
        plugins:
          claude-code:
            binary_path: "{{MOCK_LM_PATH}}"
      personas:
        parent-persona:
          description: Parent with variables
          fragments: []
          variables:
            inherited_var: ParentValue
        child-persona:
          description: Child inheriting variables
          parents:
            - parent-persona
          fragments:
            - child-frag
      """
    And the mock LM will respond with:
      """
      OK
      """
    When I run scm "run -p child-persona --print test"
    Then the exit code should be 0
    And the LM should have received context containing "Using ParentValue from parent"

  Scenario: Child persona overrides parent variables
    Given a fragment "override-frag" in the project with content:
      """
      tags:
        - override
      content: |
        Value is {{override_me}}.
      """
    And a config file with:
      """
      lm:
        default_plugin: claude-code
        plugins:
          claude-code:
            binary_path: "{{MOCK_LM_PATH}}"
      personas:
        base:
          description: Base with default
          fragments: []
          variables:
            override_me: BaseValue
        derived:
          description: Derived with override
          parents:
            - base
          fragments:
            - override-frag
          variables:
            override_me: DerivedValue
      """
    And the mock LM will respond with:
      """
      OK
      """
    When I run scm "run -p derived --print test"
    Then the exit code should be 0
    And the LM should have received context containing "Value is DerivedValue"

  Scenario: Unsubstituted variable is replaced with empty string
    Given a fragment "missing-var" in the project with content:
      """
      tags:
        - missing
      content: |
        This uses {{undefined_variable}} here.
      """
    And the mock LM will respond with:
      """
      OK
      """
    When I run scm "run -f missing-var --print test"
    Then the exit code should be 0
    And the LM should have received context containing "This uses  here"

  # ============================================================================
  # Fragment Ordering and Assembly
  # ============================================================================

  Scenario: Fragments are assembled in specified order
    Given a fragment "first" in the project with content:
      """
      tags:
        - order
      content: |
        FIRST_MARKER
      """
    And a fragment "second" in the project with content:
      """
      tags:
        - order
      content: |
        SECOND_MARKER
      """
    And a fragment "third" in the project with content:
      """
      tags:
        - order
      content: |
        THIRD_MARKER
      """
    And the mock LM will respond with:
      """
      OK
      """
    When I run scm "run -f first -f second -f third --print test"
    Then the exit code should be 0
    And the LM should have received context containing "FIRST_MARKER"
    And the LM should have received context containing "SECOND_MARKER"
    And the LM should have received context containing "THIRD_MARKER"

  Scenario: Persona fragments combined with additional fragments
    Given a fragment "persona-frag" in the project with content:
      """
      tags:
        - persona
      content: |
        From persona.
      """
    And a fragment "extra-frag" in the project with content:
      """
      tags:
        - extra
      content: |
        Extra fragment added.
      """
    And a config file with:
      """
      lm:
        default_plugin: claude-code
        plugins:
          claude-code:
            binary_path: "{{MOCK_LM_PATH}}"
      personas:
        base-persona:
          description: Base
          fragments:
            - persona-frag
      """
    And the mock LM will respond with:
      """
      OK
      """
    When I run scm "run -p base-persona -f extra-frag --print test"
    Then the exit code should be 0
    And the LM should have received context containing "From persona"
    And the LM should have received context containing "Extra fragment added"

  Scenario: Tag-selected fragments with variable substitution
    Given a fragment "tagged-var" in the project with content:
      """
      tags:
        - code
        - golang
      content: |
        Write {{language}} code following best practices.
      """
    And a fragment "untagged" in the project with content:
      """
      tags:
        - other
      content: |
        This should not be included.
      """
    And a config file with:
      """
      lm:
        default_plugin: claude-code
        plugins:
          claude-code:
            binary_path: "{{MOCK_LM_PATH}}"
      personas:
        go-dev:
          description: Go developer
          tags:
            - golang
          variables:
            language: Go
      """
    And the mock LM will respond with:
      """
      OK
      """
    When I run scm "run -p go-dev --print test"
    Then the exit code should be 0
    And the LM should have received context containing "Write Go code following best practices"
    And the LM should have received context not containing "This should not be included"

  # ============================================================================
  # Generator Integration
  # ============================================================================

  Scenario: Generator output included in context
    Given a config file with:
      """
      lm:
        default_plugin: claude-code
        plugins:
          claude-code:
            binary_path: "{{MOCK_LM_PATH}}"
      generators:
        test-gen:
          description: Test generator
          command: printf
          args:
            - "content: |\n  GENERATED_CONTEXT_MARKER\n"
      personas:
        gen-persona:
          description: Persona with generator
          fragments: []
          generators:
            - test-gen
      """
    And the mock LM will respond with:
      """
      OK
      """
    When I run scm "run -p gen-persona --print test"
    Then the exit code should be 0
    And the LM should have received context containing "GENERATED_CONTEXT_MARKER"

  Scenario: Multiple generators combined in context
    Given a config file with:
      """
      lm:
        default_plugin: claude-code
        plugins:
          claude-code:
            binary_path: "{{MOCK_LM_PATH}}"
      generators:
        gen-one:
          description: First generator
          command: printf
          args:
            - "content: |\n  FIRST_GEN_OUTPUT\n"
        gen-two:
          description: Second generator
          command: printf
          args:
            - "content: |\n  SECOND_GEN_OUTPUT\n"
      personas:
        multi-gen:
          description: Multiple generators
          fragments: []
          generators:
            - gen-one
            - gen-two
      """
    And the mock LM will respond with:
      """
      OK
      """
    When I run scm "run -p multi-gen --print test"
    Then the exit code should be 0
    And the LM should have received context containing "FIRST_GEN_OUTPUT"
    And the LM should have received context containing "SECOND_GEN_OUTPUT"

  Scenario: Generator and fragments combined in context
    Given a fragment "static-frag" in the project with content:
      """
      tags:
        - static
      content: |
        STATIC_FRAGMENT_CONTENT
      """
    And a config file with:
      """
      lm:
        default_plugin: claude-code
        plugins:
          claude-code:
            binary_path: "{{MOCK_LM_PATH}}"
      generators:
        dynamic-gen:
          description: Dynamic generator
          command: printf
          args:
            - "content: |\n  DYNAMIC_GEN_CONTENT\n"
      personas:
        combined:
          description: Fragments and generators
          fragments:
            - static-frag
          generators:
            - dynamic-gen
      """
    And the mock LM will respond with:
      """
      OK
      """
    When I run scm "run -p combined --print test"
    Then the exit code should be 0
    And the LM should have received context containing "STATIC_FRAGMENT_CONTENT"
    And the LM should have received context containing "DYNAMIC_GEN_CONTENT"
