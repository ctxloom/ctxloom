Feature: LLM backends
  llm lists the available backends and manages the default, and — like its
  `agent` sibling — creates, edits and removes labeled engine configs
  (`ctxloom llm create/edit/remove`).

  Scenario: List built-in backends
    Given an initialized ctxloom project
    When I run "ctxloom llm list"
    Then the command succeeds
    And the output contains "claude-code"

  # Asserting only the exit code let a show path that returned an EMPTY default
  # pass. The default a project with no llm block resolves to is a fact worth
  # stating, and it is the effect the command exists to report.
  Scenario: Show the default backend
    Given an initialized ctxloom project
    When I run "ctxloom llm default"
    Then the command succeeds
    And the output contains "claude-code"

  Scenario: Set the default backend
    Given an initialized ctxloom project
    When I run "ctxloom llm default antigravity"
    Then the command succeeds
    When I run "ctxloom llm default"
    Then the output contains "antigravity"

  # `agent create --engine <label>` draws from exactly this vocabulary — this
  # is the CRUD that lets a team manage it, not just enumerate it.
  Scenario: Create, edit and remove a labeled LLM engine config
    Given an initialized ctxloom project
    When I run "ctxloom llm create big --type codex --model o1"
    Then the command succeeds
    And the output contains "Created llm"
    And the output contains "codex"
    And the output contains "o1"
    When I run "ctxloom llm list"
    Then the output contains "big"
    # create refuses a label that already exists.
    When I run "ctxloom llm create big --type codex"
    Then the command fails
    And the output contains "already exists"
    # edit changes only the field named, leaving the rest (type: codex) intact.
    When I run "ctxloom llm edit big --model o1-pro"
    Then the command succeeds
    And the output contains "Updated llm"
    And the output contains "o1-pro"
    And the output contains "codex"
    # remove reports and destroys nothing without --yes.
    When I run "ctxloom llm remove big"
    Then the command succeeds
    And the output contains "Nothing was removed"
    And the output contains "--yes"
    When I run "ctxloom llm list"
    Then the output contains "big"
    When I run "ctxloom llm remove big --yes"
    Then the command succeeds
    And the output contains "Removed llm"
    When I run "ctxloom llm list"
    Then the output does not contain "big"

  # CREDENTIALS ARE WITHHELD (a security posture, not a nicety): --env-file
  # is the ONLY way to set llm.configs.<label>.env, so a secret never
  # travels through argv — and the value it sets is written to the
  # PER-MACHINE user config, never the committed project file (a
  # credential-scoped fact, like `signer trust`'s project/user split is a
  # trust-scoped one), and never echoed back by any command's own output.
  Scenario: A credential set via --env-file never appears in ANY output, and never lands in the committed project file
    Given an initialized ctxloom project
    And the project already has the file "openai.env":
      """
      OPENAI_API_KEY=sk-test-should-never-be-printed-anywhere
      """
    When I run "ctxloom llm create big --type codex --env-file openai.env"
    Then the command succeeds
    And the output contains "OPENAI_API_KEY"
    And the output does not contain "sk-test-should-never-be-printed-anywhere"
    When I run "ctxloom llm list"
    Then the output does not contain "sk-test-should-never-be-printed-anywhere"
    And the file ".ctxloom/config.yaml" does not contain "sk-test-should-never-be-printed-anywhere"
    And the home file ".ctxloom/config.yaml" contains "sk-test-should-never-be-printed-anywhere"
