Feature: Manage the project harness
  manage installs, inspects, and removes ctxloom's integration with a project:
  the .ctxloom scaffold, backend hooks/statusline, MCP registration, and the
  .gitignore entries that keep ctxloom's private state out of source control.

  # Payload, not existence — the same rule the per-engine matrix below states
  # for codex and kiro, which the default engine was exempt from. An empty
  # settings file exists just as convincingly as a wired one, and the
  # surviving mutation was narrower than a wholesale gutting (j19 catches
  # that): emit the MCP server KEY with an empty body and every engine
  # launched from the project gets a server entry with no command, while
  # `exists` and even j19's contains-"ctxloom" both stay green. So this names
  # the SessionStart hook that actually delivers context, and two fields from
  # inside the MCP server's body rather than its key.
  Scenario: Install wires ctxloom into an empty project
    Given an empty project directory
    When I run "ctxloom manage install --engine claude-code"
    Then the command succeeds
    And the file ".ctxloom/config.yaml" exists
    And the file ".claude/settings.json" contains "SessionStart"
    And the file ".claude/settings.json" contains "hook inject-context"
    And the file ".mcp.json" contains "ctxloom-auto"
    And the file ".mcp.json" contains "${CLAUDE_PROJECT_DIR}"
    And the file ".gitignore" contains "ctxloom"

  # The engine NAME appears in both "claude-code: not configured" and
  # "claude-code: hooks=true ...", so asserting it said nothing about whether
  # anything was wired: an install that wrote nothing and reported success left
  # this green while ten other scenarios went red. What "wired" means is the
  # per-surface flags.
  Scenario: Status reports the wired harness
    Given an initialized ctxloom project
    When I run "ctxloom manage hooks install"
    Then the command succeeds
    When I run "ctxloom manage status"
    Then the command succeeds
    And the output matches "claude-code: hooks=true[^\n]*mcp=true"

  Scenario: Uninstall strips the harness but keeps .ctxloom
    Given an initialized ctxloom project
    And I run "ctxloom manage hooks install"
    When I run "ctxloom manage uninstall"
    Then the command succeeds
    And the file ".ctxloom/config.yaml" exists
    And the file ".claude/settings.json" does not contain "ctxloom hook"

  Scenario: Hooks can be installed, inspected, and actually removed
    Given an initialized ctxloom project
    When I run "ctxloom manage hooks install"
    Then the command succeeds
    And the file ".claude/settings.json" contains "ctxloom hook"
    When I run "ctxloom manage hooks status"
    Then the command succeeds
    When I run "ctxloom manage hooks uninstall"
    Then the command succeeds
    And the file ".claude/settings.json" does not contain "ctxloom hook"

  Scenario: Re-applying hooks does not duplicate them
    Given an initialized ctxloom project
    When I run "ctxloom manage hooks install"
    Then the command succeeds
    When I run "ctxloom manage hooks install"
    Then the command succeeds
    And the file ".claude/settings.json" contains "ctxloom hook session-bind" exactly 1 times

  # Hooks merge across sources by pure APPEND, and each bundle's own `order:`
  # sequences only its own hooks within an event. Nothing states the result, so
  # before this leaf the order a user actually got was invisible. The fixture
  # makes DECLARATION ORDER DISAGREE with `order:` on purpose: "second" is
  # written first in the YAML and ordered last, so a build that ignored the field
  # would print them the other way round.
  Scenario: Listing hooks shows the resolved order and where each hook came from
    Given an initialized ctxloom project
    And the project already has the file ".ctxloom/content/bundles/hooked.yaml":
      """
      name: hooked
      description: ships two pre_tool hooks whose order contradicts their position
      hooks:
        pre_tool:
          - type: command
            command: echo HOOKLIST-SECOND
            order: 200
          - type: command
            command: echo HOOKLIST-FIRST
            order: 100
      """
    And a profile "hooky" with bundle "hooked"
    When I run "ctxloom manage hooks list --event pre_tool --profile hooky"
    Then the command succeeds
    And the output contains "HOOKLIST-FIRST"
    And the output contains "HOOKLIST-SECOND"
    And the output matches "HOOKLIST-FIRST[\s\S]*HOOKLIST-SECOND"
    And the output contains "bundle hooked"

  # Provenance used to be three coarse labels, and one of them — "local" —
  # covered both config.yaml's own hooks: block and every inline profile's,
  # because the merge appended into a settings-shaped value and discarded the
  # source as it went. A user told "local" still had to search two blocks of one
  # file. Assembly now resolves into a model that keeps what the merge knows, so
  # each hook names ONE place to go.
  Scenario: The hook list names the specific place each hook was declared
    Given an initialized ctxloom project
    And the project already has the file ".ctxloom/config.yaml":
      """
      version: 4
      editor:
        command: "true"
      hooks:
        unified:
          pre_tool:
            - type: command
              command: echo PROV-FROM-CONFIG
      profiles:
        definitions:
          inline-prov:
            hooks:
              unified:
                pre_tool:
                  - type: command
                    command: echo PROV-FROM-INLINE-PROFILE
      """
    When I run "ctxloom manage hooks list --event pre_tool --profile inline-prov"
    Then the command succeeds
    And the output matches "PROV-FROM-CONFIG\s+\[config\]"
    And the output matches "PROV-FROM-INLINE-PROFILE\s+\[profile-inline inline-prov\]"
    And the output does not contain "[local]"
    # The machine form carries the same specific kind, plus the position the
    # MERGE gave each hook — so a later reordering is visible as a move rather
    # than as an unexplained final number.
    When I run "ctxloom manage hooks list --event pre_tool --profile inline-prov --format json"
    Then the command succeeds
    And the output contains "profile-inline"
    And the output contains "declared"

  # "Machine-readable" was asserted with two event names the HUMAN listing
  # prints too, so a command that ignored --format json and rendered text
  # passed the machine-readable scenario. Parsing is the claim.
  Scenario: The hook list has a machine-readable form
    Given an initialized ctxloom project
    When I run "ctxloom manage hooks list --format json"
    Then the command succeeds
    And the output is valid JSON
    And the JSON output array "events" contains an object whose "event" is "post_file_edit"
    And the JSON output array "events" contains an object whose "event" is "session_start"
    And every object in the JSON output array "events" has a non-empty "event"

  # A typo must not resolve to "nothing runs on that event" — a confident wrong
  # answer from an inspect command is worse than no inspect command.
  Scenario: An unknown event name is refused rather than answered empty
    Given an initialized ctxloom project
    When I run "ctxloom manage hooks list --event pre_toll"
    Then the command fails
    And the output contains "pre_toll"

  Scenario: Gitignore install excludes ctxloom's private state
    Given an initialized ctxloom project
    When I run "ctxloom manage gitignore install"
    Then the command succeeds
    And the file ".gitignore" contains ".ctxloom/ephemeral/"

  Scenario: Config init scaffolds a config in a bare project
    Given an empty project directory
    When I run "ctxloom config init --engine claude-code"
    Then the command succeeds
    And the file ".ctxloom/config.yaml" exists

  Scenario: Statusline can be disabled and re-enabled
    Given an initialized ctxloom project
    When I run "ctxloom manage statusline uninstall"
    Then the command succeeds
    When I run "ctxloom manage hooks install"
    Then the command succeeds
    And the file ".claude/settings.json" does not contain "hook hud"
    When I run "ctxloom manage statusline install"
    Then the command succeeds
    When I run "ctxloom manage hooks install"
    Then the command succeeds
    And the file ".claude/settings.json" contains "hook hud"

  # The dirty-tree-commit acknowledgement moved out of config.yaml into its
  # own gitignored state-store record (internal/config/layerscope: the value
  # records a prior HUMAN authorization, not configuration, and is
  # ScopeNever — no config layer may set it). `manage dirty-tree-ack` is the
  # scriptable counterpart to `ctxloom init`'s interview question, the only
  # other writer.
  Scenario: The dirty-tree-commit acknowledgement can be granted and revoked
    Given an initialized ctxloom project
    Then the file ".ctxloom/state/dirty_tree_commit_ack.yaml" does not exist
    When I run "ctxloom manage dirty-tree-ack grant"
    Then the command succeeds
    And the output contains "granted"
    And the file ".ctxloom/state/dirty_tree_commit_ack.yaml" exists
    And the file ".ctxloom/state/dirty_tree_commit_ack.yaml" contains "approved: true"
    When I run "ctxloom manage dirty-tree-ack revoke"
    Then the command succeeds
    And the output contains "revoked"
    And the file ".ctxloom/state/dirty_tree_commit_ack.yaml" contains "approved: false"

  # A rewrite of settings.json must only ADD ctxloom's own keys: whatever the
  # user wrote by hand comes back byte-for-byte, including numbers no float64
  # can hold exactly. The failure this pins reported success and altered the
  # file, so the assertion is on the persisted digits.
  Scenario: Installing hooks preserves the exact numbers the user wrote by hand
    Given an initialized ctxloom project
    And the project already has the file ".claude/settings.json":
      """
      {
        "awkwardNumber": 1234567890123456789,
        "nested": { "id": -9223372036854775808 },
        "permissions": { "allow": ["Read"], "quota": 18446744073709551615 }
      }
      """
    When I run "ctxloom manage hooks install"
    Then the command succeeds
    And the file ".claude/settings.json" contains "ctxloom hook"
    And the file ".claude/settings.json" contains "1234567890123456789"
    And the file ".claude/settings.json" contains "-9223372036854775808"
    And the file ".claude/settings.json" contains "18446744073709551615"
    And the file ".claude/settings.json" contains "Read"

  # ctxloom is the wrong party to decide the fate of a setting it just failed to
  # read: the write is refused and the user's file is left exactly as it was.
  Scenario: A statusline ctxloom cannot read refuses the write instead of replacing it
    Given an initialized ctxloom project
    And the project already has the file ".claude/settings.json":
      """
      {
        "env": { "A": "b" },
        "statusLine": { "type": "command", "command": { "exec": "ccusage" } }
      }
      """
    When I run "ctxloom manage hooks install"
    Then the output contains "refusing to write settings.json"
    And the file ".claude/settings.json" contains "ccusage"
    And the file ".claude/settings.json" does not contain "ctxloom hook"

  # THE PER-ENGINE MATRIX. Everything above installs against claude-code, and a
  # substring check on the leaf is satisfied by that one engine forever — so
  # codex's and kiro's install paths could break without a single red test.
  # They are written as separate scenarios rather than one Scenario Outline
  # deliberately: the completeness gate reads the corpus as TEXT and looks for
  # a literal `I run "ctxloom manage install --engine codex"`, which an
  # Examples table never produces. It would report these leaves uncovered no
  # matter how thoroughly an Outline exercised them.
  #
  # Each asserts the engine's OWN surfaces, because that is what differs: the
  # three engines share the .ctxloom scaffold and agree about nothing else.
  # Payload, not existence — an empty settings file exists just as convincingly
  # as a wired one.

  # codex is configured through a TOML file plus a repo-root AGENTS.md, and its
  # SessionStart hook is what delivers context. Asserting the hook line proves
  # the wiring, not merely that a config file was created.
  Scenario: Install wires codex through its own config surfaces
    Given an empty project directory
    When I run "ctxloom manage install --engine codex"
    Then the command succeeds
    And the file ".ctxloom/config.yaml" exists
    And the file ".codex/config.toml" contains "[mcp_servers.ctxloom]"
    And the file ".codex/config.toml" contains "hook inject-context"
    And the file "AGENTS.md" exists
    And the file ".gitignore" contains "ctxloom"

  # kiro takes JSON, and registers ctxloom as an MCP server plus an agent
  # definition. Its skills land as directories rather than a single file, which
  # is the shape j10 already proves elsewhere; here the claim is only that
  # install reached kiro's surfaces at all.
  Scenario: Install wires kiro through its own config surfaces
    Given an empty project directory
    When I run "ctxloom manage install --engine kiro"
    Then the command succeeds
    And the file ".ctxloom/config.yaml" exists
    And the file ".kiro/settings/mcp.json" contains "ctxloom"
    And the file ".kiro/agents/ctxloom.json" exists
    And the file ".gitignore" contains "ctxloom"

  # `config init --engine` writes no engine files at all — it selects the
  # engine INSIDE config.yaml. Probed on a real run: the codex and kiro
  # invocations produce byte-identical trees apart from that one file, so the
  # config's contents are the only place the flag is observable and asserting
  # a file exists would prove nothing about which engine was asked for.
  Scenario: Config init selects codex as the project's engine
    Given an empty project directory
    When I run "ctxloom config init --engine codex"
    Then the command succeeds
    And the file ".ctxloom/config.yaml" contains "engine: codex"
    And the file ".ctxloom/config.yaml" contains "type: codex"
    And the file ".ctxloom/config.yaml" contains "primary: codex"

  Scenario: Config init selects kiro as the project's engine
    Given an empty project directory
    When I run "ctxloom config init --engine kiro"
    Then the command succeeds
    And the file ".ctxloom/config.yaml" contains "engine: kiro"
    And the file ".ctxloom/config.yaml" contains "type: kiro"
    And the file ".ctxloom/config.yaml" contains "primary: kiro"
