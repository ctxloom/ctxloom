# world: base
# Ported from cli/auth.feature
# NOT ported: nothing. All three scenarios. Rust has two engines with an environment
#   credential (claude-code's token, opencode's optional provider keys: rulings 69, 79); a
#   claude login shared in place has no row, as in Go.
@doc
Feature: auth — how ctxloom-launched engines authenticate

  Covers: `ctxloom auth status`.

  Every agent ctxloom spawns authenticates with ONE credential: a token the
  human mints with the engine's own flow (claude: `claude setup-token`) and
  exports — or keeps in a secret manager that exports it. Only the human's
  own session may share their login instead (the top-level `auth: login`).
  ctxloom never collects, stores or mints a credential: it reads the token
  from the environment it is launched in. `auth status` reports, per engine,
  whether that credential is present, never its value.

  Everything here runs against the scenario's own temporary home and
  environment, so the credential exported is a fixture string and nothing
  real is read.

  Rule: status reports whether each credential is in the environment, never its value

    # THE SECRET MUST APPEAR IN NO OUTPUT: a status that echoed the value it
    # found would leak the credential into logs and transcripts, silently.
    # Rows are per engine and mode, so the claim is read off claude-code's own
    # row: other engines report their own credentials (opencode's is an
    # endpoint API key). A second claude-code row (an api-key mode) would make
    # the selector ambiguous and fail it, which keeps claude token-only.
    Scenario: An exported token is reported present without revealing it
      Given an initialized ctxloom project
      And the environment variable "CLAUDE_CODE_OAUTH_TOKEN" is set to "sk-ant-oat01-acceptance-fixture-not-real"
      And the environment variable "ANTHROPIC_API_KEY" is set to ""
      When I run "ctxloom auth status --format json"
      Then the command succeeds
      And the output does not contain "sk-ant-oat01-acceptance-fixture-not-real"
      And the output reports "[engine=claude-code].mode" as "token"
      And the output reports "[engine=claude-code].present" as "true"

    Scenario: With nothing exported, status names how to mint and export the token
      Given an initialized ctxloom project
      And the environment variable "CLAUDE_CODE_OAUTH_TOKEN" is set to ""
      And the environment variable "ANTHROPIC_API_KEY" is set to ""
      When I run "ctxloom auth status --format text"
      Then the command succeeds
      And the output contains "claude-code token: missing — run `claude setup-token` and export CLAUDE_CODE_OAUTH_TOKEN"

    # opencode's built-in keys are its providers' and optional (rulings 69
    # and 79): with none exported it runs keyless on its free models. Status
    # says so, as doctor and `engine show` do (hand-test 2026-11-10: status
    # had no opencode row, doctor called it present, `engine show` missing).
    Scenario: With no opencode key exported, status says opencode needs none
      # new: rulings 69 and 79.
      Given an initialized ctxloom project
      And the environment variable "ANTHROPIC_API_KEY" is set to ""
      And the environment variable "OPENAI_API_KEY" is set to ""
      When I run "ctxloom auth status --format text"
      Then the command succeeds
      And the output contains "opencode token: not needed: none of ANTHROPIC_API_KEY, OPENAI_API_KEY is exported, so opencode runs keyless on its free models"
      When I run "ctxloom engine show opencode --format json"
      Then the command succeeds
      And the output reports "auth.state" as "NOT_NEEDED"

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

  Rule: status reads the engine's configured source (engines.<engine>.auth), not a fixed variable

    # RED (awaiting review). The source is where the credential comes from, a
    # variable NAME or a file, never a value; any one of a source's variables
    # authenticates. This also fixes status (and doctor) requiring
    # CLAUDE_CODE_OAUTH_TOKEN alone when an API key authenticates a run.
    Scenario: An API key alone is reported present, naming the variable
      # new: the built-in source for claude-code is either variable
      Given an initialized ctxloom project
      And the environment variable "CLAUDE_CODE_OAUTH_TOKEN" is set to ""
      And the environment variable "ANTHROPIC_API_KEY" is set to "sk-ant-api03-acceptance-fixture-not-real"
      When I run "ctxloom auth status --format json"
      Then the command succeeds
      And the output does not contain "sk-ant-api03-acceptance-fixture-not-real"
      And the output reports "[engine=claude-code].present" as "true"
      And the output reports "[engine=claude-code].via" as "ANTHROPIC_API_KEY"

    Scenario: A source the project names replaces the built-in one
      # new: engines.claude-code.auth.env
      Given an initialized ctxloom project
      And the project already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        engines:
          claude-code:
            auth:
              env: [TEAM_CLAUDE_TOKEN]
        """
      And the environment variable "CLAUDE_CODE_OAUTH_TOKEN" is set to "sk-ant-oat01-acceptance-fixture-not-real"
      And the environment variable "TEAM_CLAUDE_TOKEN" is set to ""
      When I run "ctxloom auth status --format text"
      Then the command succeeds
      And the output contains "claude-code token: missing"
      And the output contains "TEAM_CLAUDE_TOKEN"
      And the output does not contain "sk-ant-oat01-acceptance-fixture-not-real"

    Scenario: A source that is a file alone serves the primary session only
      # new: owner ruling 53 (2026-11-09). A file is the human's own session's;
      # a child agent needs a variable, so the row says who it serves.
      Given an initialized ctxloom project
      And the home already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        engines:
          claude-code:
            auth:
              file: ~/.claude/.credentials.json
        """
      And the environment variable "CLAUDE_CODE_OAUTH_TOKEN" is set to "sk-ant-oat01-acceptance-fixture-not-real"
      When I run "ctxloom auth status --format json"
      Then the command succeeds
      And the output does not contain "sk-ant-oat01-acceptance-fixture-not-real"
      And the output reports "[engine=claude-code].mode" as "file"
      And the output reports "[engine=claude-code].usable_by" as "primary"
      And the output reports "[engine=claude-code].via" as "~/.claude/.credentials.json"
      # owner ruling 65 (2026-11-10): nothing looks at the file, so the row
      # says nothing of whether it is there. (opencode's own row, optional
      # and keyless, does say it is not present: rulings 69, 79.)
      And the output reports no "[engine=claude-code].present"
      # owner ruling 81: in the words a child's run of it is refused with.
      And the output reports "[engine=claude-code].message" as "a child agent run of claude-code needs a credential in the environment (any one of: CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_API_KEY), and engines.claude-code.auth names only a file, which serves the primary interactive session alone"
      And the output reports "[engine=claude-code].remedy" as "name one of those variables in engines.claude-code.auth.env and export it (for claude-code, `claude setup-token` makes CLAUDE_CODE_OAUTH_TOKEN); the file keeps serving the primary session"
