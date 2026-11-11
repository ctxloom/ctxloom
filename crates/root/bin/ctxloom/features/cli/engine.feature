# world: base
# NOT ported: nothing. The `engine` noun is new to this port (docs/plans/ctxloom-features.md, "Names":
#   a read-only `ctxloom engine` noun shows each engine's effective settings); Go has no such file.
# RED (awaiting review): committed red on port/engines-schema. The shapes asserted here are the
#   proposal's (engines-schema.proposal.md); the owner reviews the names before any is made to pass.
@doc
Feature: engine — the programs ctxloom drives, and the settings they share

  Covers: `ctxloom engine list`, `engine show <engine>`, the `engines:` block of
  .ctxloom/config.yaml, and the refusal of the retired `llm:` keys.

  An ENGINE is only the program: claude-code, opencode, mock. The set is fixed;
  nobody creates one, and an engine has many agents. What an engine is configured
  with that every agent of it shares lives in the `engines.<engine>` block:

    command        the program to run (default: the engine's own name's binary)
    session_home   whose engine configuration a run gets: session | host
    auth           where its credential comes from: a SOURCE, never a secret
    runtime        the default runtime an agent of it runs in
    env            the host environment names an agent of it is given

  Model, effort and permissions are NOT in that block. They are an agent's
  settings, in its block for the engine (`agents.<n>.engines.<e>`), because
  two agents on one engine rightly differ in them (owner rulings 40-44,
  2026-11-09; 136). The model and effort reach claude as its flags (`--model`,
  `--effort`), unset being no flag; opencode is given the model alone. The
  permissions are the engine's OWN document, copied verbatim and not checked
  by ctxloom (owner rulings 133-139): claude's `permissions` object of the
  settings it is handed, opencode's `permission` object of its config. They
  are written in the config by hand (`agent create --model --effort` writes
  the rest; there is no `--permissions`, ruling 138).

  Every value `engine show` prints carries the layer that said it: `agent`,
  `engine`, `project` or `default`. A value nobody set is printed as the
  default it falls to, never omitted, so "what will this run use" has one answer.

  `engine` is READ-ONLY. The block is written in the config file itself
  (`ctxloom config edit`).

  Rule: engine list names the fixed set, each with the settings it will use

    # new: the noun is new.
    Scenario Outline: A project that configures nothing lists the engines on their defaults
      # new: the engine noun is new to this port
      Given an initialized ctxloom project
      When Alice asks which engines ctxloom can drive:
        """
        ctxloom engine list <flags>
        """
      Then the command succeeds
      And the output reports "[name=claude-code].command.value" as "claude"
      And the output reports "[name=claude-code].command.source" as "default"
      And the output reports "[name=claude-code].runtime.value" as "host"
      And the output reports "[name=claude-code].runtime.source" as "default"
      And the output reports "[name=opencode].command.value" as "opencode"

      @json-default
      Examples: no --format at all takes the derived default off a terminal; an explicit one wins
        | flags         |
        |               |
        | --format json |

    Scenario: The text rendering says the same, with the source beside each value
      # new: the text arm of the listing
      Given an initialized ctxloom project
      When I run "ctxloom engine list --format text"
      Then the command succeeds
      And the output contains "claude-code"
      And the output contains "opencode"
      And the output contains "runtime: host (default)"

    Scenario: A setting the project wrote is reported with the engine as its source
      # new: the source is what makes the listing an answer
      Given an initialized ctxloom project
      And the home already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        engines:
          claude-code:
            session_home: host
        """
      And the project already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        engines:
          claude-code:
            runtime: container-rootless
        """
      When I run "ctxloom engine list --format json"
      Then the command succeeds
      And the output reports "[name=claude-code].runtime.value" as "container-rootless"
      And the output reports "[name=claude-code].runtime.source" as "engine"
      And the output reports "[name=claude-code].session_home.value" as "host"
      And the output reports "[name=claude-code].session_home.source" as "engine"
      And the output reports "[name=opencode].runtime.source" as "default"

    Scenario: Bare engine lists the engines
      # new: bare noun answers, as every noun does
      Given an initialized ctxloom project
      When I run "ctxloom engine --format json"
      Then the command succeeds
      And the output reports "[name=claude-code].command.value" as "claude"

  Rule: engine show reports one engine's settings, and what its agents resolve to

    Scenario: Each shared setting carries the layer that said it
      # new: value plus source for every key of the block
      Given an initialized ctxloom project
      And the home already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        engines:
          claude-code:
            command: /opt/claude/bin/claude
        """
      And the project already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        runtime: host
        engines:
          claude-code:
            env: [HTTPS_PROXY]
        """
      When I run "ctxloom engine show claude-code --format json"
      Then the command succeeds
      And the output reports "name" as "claude-code"
      And the output reports "command.value" as "/opt/claude/bin/claude"
      And the output reports "command.source" as "engine"
      And the output reports "session_home.value" as "session"
      And the output reports "session_home.source" as "default"
      And the output reports "runtime.value" as "host"
      And the output reports "runtime.source" as "project"
      And the output reports "env.value" containing "HTTPS_PROXY"
      And the output reports "env.source" as "engine"

    Scenario: A project's config cannot say which program or whose home a run uses
      # new: ruling 46, a checked-out repository must not choose the program ctxloom runs
      Given an initialized ctxloom project
      And the project already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        engines:
          claude-code:
            command: /tmp/not-the-claude-you-meant
            session_home: host
        """
      When I run "ctxloom engine show claude-code --format json"
      Then the command succeeds
      And the output reports "command.value" as "claude"
      And the output reports "command.source" as "default"
      And the output reports "session_home.value" as "session"
      And the output contains "a project's config cannot set it"

    Scenario: An agent's own settings are listed with their sources, unset ones as the engine's own default
      # new: model and effort unset pass no flag; claude's permissions have a
      # ctxloom default document (owner ruling 137)
      Given an initialized ctxloom project
      And the project already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        agents:
          reviewer:
            engine: claude-code
            engines:
              claude-code:
                model: haiku
                effort: low
          plain:
            engine: claude-code
        """
      When I run "ctxloom engine show claude-code --format json"
      Then the command succeeds
      And the output reports "agents[name=reviewer].model.value" as "haiku"
      And the output reports "agents[name=reviewer].model.source" as "agent"
      And the output reports "agents[name=reviewer].effort.value" as "low"
      And the output reports "agents[name=plain].model.source" as "default"
      And the output reports "agents[name=plain].permissions.value.defaultMode" as "acceptEdits"
      And the output reports "agents[name=plain].permissions.source" as "default"
      When I run "ctxloom engine show claude-code --format text"
      Then the command succeeds
      And the output contains "reviewer"
      And the output contains "model: haiku (agent)"
      And the output contains "model: engine default, no flag passed (default)"
      And the output contains "permissions: {defaultMode: acceptEdits} (default)"
      And the output contains "passed to claude-code verbatim; ctxloom does not check it"

    # An agent's model, effort and permission mode are per engine (ruling 57):
    # an engine lists the agents with a block for it too, since they run on it
    # under `run --engine`, each value from that block, else the engine's.
    Scenario: An engine lists an agent with a block for it, with that block's values
      # new: owner ruling 57 (2026-11-09)
      Given an initialized ctxloom project
      And the project already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        agents:
          reviewer:
            engine: claude-code
            engines:
              claude-code:
                model: haiku
              opencode:
                model: opencode/big-pickle
        """
      When I run "ctxloom engine show opencode --format json"
      Then the command succeeds
      And the output reports "agents[name=reviewer].model.value" as "opencode/big-pickle"
      And the output reports "agents[name=reviewer].model.source" as "agent"
      And the output reports "agents[name=reviewer].effort.source" as "default"

    Scenario: An agent's runtime that replaces the engine's is shown as an override, and a weaker one says so
      # new: the isolation guard is never silent
      Given an initialized ctxloom project
      And the project already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        engines:
          claude-code:
            runtime: container-rootless
        agents:
          loose:
            engine: claude-code
            runtime: host
          other:
            engine: claude-code
            runtime: container-rootful
          kept:
            engine: claude-code
        """
      When I run "ctxloom engine show claude-code --format json"
      Then the command succeeds
      And the output reports "agents[name=loose].runtime.value" as "host"
      And the output reports "agents[name=loose].runtime.source" as "agent"
      And the output reports "agents[name=loose].runtime.overrides" as "container-rootless"
      And the output reports "agents[name=loose].runtime.change" as "weakens"
      And the output reports "agents[name=other].runtime.change" as "ownership-swap"
      And the output reports "agents[name=kept].runtime.value" as "container-rootless"
      And the output reports "agents[name=kept].runtime.source" as "engine"
      When I run "ctxloom engine show claude-code --format text"
      Then the command succeeds
      And the output contains "runtime: host (agent; replaces engine container-rootless: weakens isolation)"
      And the output contains "runtime: container-rootful (agent; replaces engine container-rootless: ownership differs, not interchangeable)"

    Scenario: Showing an engine ctxloom does not drive is refused, naming the engines it does
      # new: a typo is answered with the set
      Given an initialized ctxloom project
      When I run "ctxloom engine show gpt-9"
      Then the command fails
      And the output contains "gpt-9"
      And the output contains "claude-code"
      And the output contains "opencode"

    Scenario: engine has no verb that writes
      # new: read-only (ruling 43)
      Given an initialized ctxloom project
      When I run "ctxloom engine create claude-code"
      Then the command fails
      And the file ".ctxloom/config.yaml" does not contain "engines:"

  Rule: auth is a source, and status reads it without revealing it

    Scenario: The built-in source for claude-code is either of its two variables
      # new: replaces the implicit policy
      Given an initialized ctxloom project
      And the environment variable "CLAUDE_CODE_OAUTH_TOKEN" is set to ""
      And the environment variable "ANTHROPIC_API_KEY" is set to "sk-ant-api03-acceptance-fixture-not-real"
      When I run "ctxloom engine show claude-code --format json"
      Then the command succeeds
      And the output does not contain "sk-ant-api03-acceptance-fixture-not-real"
      And the output reports "auth.source" as "default"
      And the output reports "auth.value.env" containing "CLAUDE_CODE_OAUTH_TOKEN"
      And the output reports "auth.value.env" containing "ANTHROPIC_API_KEY"
      And the output reports "auth.state" as "present"
      And the output reports "auth.via" as "ANTHROPIC_API_KEY"

    Scenario: A source the project names is the one read
      # new: the variable is the project's to name; the value is never in the file
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
      When I run "ctxloom engine show claude-code --format json"
      Then the command succeeds
      And the output reports "auth.source" as "engine"
      And the output reports "auth.state" as "missing"
      Given the environment variable "TEAM_CLAUDE_TOKEN" is set to "team-token-acceptance-fixture-not-real"
      And I run "ctxloom engine show claude-code --format json"
      Then the command succeeds
      And the output reports "auth.state" as "present"
      And the output reports "auth.via" as "TEAM_CLAUDE_TOKEN"
      And the output does not contain "team-token-acceptance-fixture-not-real"

    Scenario: auth none says no credential is needed
      # new: an engine that takes none
      Given an initialized ctxloom project
      And the project already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        engines:
          claude-code:
            auth: none
        """
      When I run "ctxloom engine show claude-code --format json"
      Then the command succeeds
      And the output reports "auth.state" as "not-needed"

    Scenario: A secret written where a variable name belongs is refused, and not repeated
      # new: config is committed; a key that invites a secret is how one is pushed
      Given an initialized ctxloom project
      And the project already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        engines:
          claude-code:
            auth:
              env: [sk-ant-oat01-acceptance-fixture-not-real]
        """
      When I run "ctxloom engine list"
      Then the command exits with code 20
      And the output contains "engines.claude-code.auth.env"
      And the output does not contain "sk-ant-oat01-acceptance-fixture-not-real"

  Rule: the engines block is a section of the configuration, and llm is not

    Scenario: config show engines renders the block and nothing above it
      # new: sections are the top-level keys
      Given an initialized ctxloom project
      And the project already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        engines:
          claude-code:
            runtime: container-rootless
        """
      When I run "ctxloom config show engines --format json"
      Then the command succeeds
      And the output reports "claude-code.runtime" as "container-rootless"
      And the output does not contain "schema_version"

    Scenario: The default configuration no longer carries an llm registry
      # new: the shipped claude-fast/big registry is retired with the llm noun
      Given an initialized ctxloom project
      When I run "ctxloom config show --format json"
      Then the command succeeds
      And the output does not contain "claude-fast"
      And the output does not contain "\"llm\""

  Rule: the retired llm keys are refused like any unknown key

    A prerelease breaks without migrating: no schema_version moves, nothing is
    rewritten, and no fallback reader reads the old key. `llm:` is simply a key
    the strict decode does not know (exit 20), with the ordinary unknown-key
    diagnostic and no message of its own.

    Scenario: A config still carrying the llm block is refused as an unknown key
      # new: owner rulings 23 and 41
      Given an initialized ctxloom project
      And the project already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        llm:
          configs:
            big:
              type: claude-code
              model: opus
          defaults:
            primary: big
        """
      When I run "ctxloom agent list --format text"
      Then the command exits with code 20
      And the output contains "unknown key `llm`"
      And the file ".ctxloom/config.yaml" contains "llm:"
      And the file ".ctxloom/config.yaml" contains "schema_version: 9"

    Scenario: An agent still naming an llm is refused, naming the key
      # new: the agent label
      Given an initialized ctxloom project
      And the project already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        agents:
          developer:
            llm: claude-code
            profiles: [dev]
        """
      When I run "ctxloom agent list --format text"
      Then the command exits with code 20
      And the output contains "unknown key `agents.developer.llm`"

    Scenario: An agent still carrying engine_home is refused as an unknown key
      # new: ruling 44(b), the home moved to the engine
      Given an initialized ctxloom project
      And the project already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        agents:
          developer:
            engine: claude-code
            engine_home: session
        """
      When I run "ctxloom agent list --format text"
      Then the command exits with code 20
      And the output contains "unknown key `agents.developer.engine_home`"

    Scenario Outline: A model, effort or permissions in the engine block is refused as an unknown key
      # new: ruling 40, these three are the agent's (136: permissions too)
      Given an initialized ctxloom project
      And the project already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        engines:
          claude-code:
            <key>: <value>
        """
      When I run "ctxloom engine list --format text"
      Then the command exits with code 20
      And the output contains "unknown key `engines.claude-code.<key>`"

      Examples:
        | key         | value |
        | model       | opus  |
        | effort      | high  |
        | permissions | plan  |

    Scenario: The llm noun is gone
      # new: ruling 43 track 3
      Given an initialized ctxloom project
      When I run "ctxloom llm list"
      Then the command fails
      And the output does not contain "Available LLMs"
      And the output contains "llm"

    Scenario: --llm is gone from the agent
      # new: --engine names a program, --llm names nothing
      Given an initialized ctxloom project
      When I run "ctxloom agent create developer --llm claude-code --profiles dev"
      Then the command fails
      And the output contains "unknown flag"
      And the output contains "--llm"
      And the file ".ctxloom/config.yaml" does not contain "developer"

    Scenario: --llm is gone from run
      # new: run names its engine with --engine
      Given an initialized ctxloom project
      When I run "ctxloom run --dry-run --llm claude-code hello"
      Then the command fails
      And the output contains "unknown flag"
      And the output contains "--llm"
