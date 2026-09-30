@doc
Feature: auth — how ctxloom-launched engines authenticate

  Covers: `ctxloom auth status`.

  An agent declares HOW its engine authenticates (`auth:` on its binding).
  ctxloom never collects, stores or mints a credential: the human mints a
  token with the engine's own flow (claude: `claude setup-token`) and exports
  it — or keeps it in a secret manager that exports it — and ctxloom reads it
  from the environment it is launched in. `auth status` reports, per engine
  and mode, whether that credential is present, never its value.

  Everything here runs against the scenario's own temporary home and
  environment, so the credential exported is a fixture string and nothing
  real is read.

  Rule: status reports whether each credential is in the environment, never its value

    # THE SECRET MUST APPEAR IN NO OUTPUT: a status that echoed the value it
    # found would leak the credential into logs and transcripts, silently.
    Scenario: An exported token is reported present without revealing it
      Given an initialized ctxloom project
      And the environment variable "CLAUDE_CODE_OAUTH_TOKEN" is set to "sk-ant-oat01-acceptance-fixture-not-real"
      And the environment variable "ANTHROPIC_API_KEY" is set to ""
      When I run "ctxloom auth status --format json"
      Then the command succeeds
      And the output does not contain "sk-ant-oat01-acceptance-fixture-not-real"
      And the output reports "[mode=token].present" as "true"
      And the output reports "[mode=api-key].present" as "false"

    Scenario: With nothing exported, status names how to supply each credential
      Given an initialized ctxloom project
      And the environment variable "CLAUDE_CODE_OAUTH_TOKEN" is set to ""
      And the environment variable "ANTHROPIC_API_KEY" is set to ""
      When I run "ctxloom auth status --format text"
      Then the command succeeds
      And the output contains "claude-code token: missing — run `claude setup-token` and export CLAUDE_CODE_OAUTH_TOKEN"
      And the output contains "claude-code api-key: missing — export ANTHROPIC_API_KEY"

  Rule: ctxloom takes no credential

    # Anthropic does not allow a third party to collect, store or intermediate
    # Claude.ai credentials, so there is no command that hands ctxloom one,
    # and nothing is ever written under the home for one.
    Scenario: There is no command that stores a credential
      Given an initialized ctxloom project
      When I run "ctxloom auth set --engine claude-code --mode token" with input:
        """
        sk-ant-oat01-acceptance-fixture-not-real
        """
      Then the command fails
      And the output does not contain "sk-ant-oat01-acceptance-fixture-not-real"
      And exactly 0 home files match ".ctxloom/auth/*"
