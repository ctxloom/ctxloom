@doc
Feature: A new engineer clones the repo and is already set up

  The day a new engineer joins, everything the team has standardized on should
  already be reaching their assistant — not because they followed a setup
  document, but because it travels with the project. Cloning IS the onboarding.
  If it does not work this way, every new hire's assistant behaves differently
  from everyone else's until someone notices and walks them through a checklist,
  which is exactly the drift ctxloom exists to remove.

  Two things must hold that are easy to get wrong. The new engineer must receive
  the SAME context as the rest of the team, not merely the newest thing the
  remotes happen to be serving that morning — otherwise "we all use the same
  standards" is a hope, not a fact. And their machine is not a clone of anyone
  else's: they will have some of the team's companion tools installed and not
  others, and the context that does not depend on the missing ones must still
  arrive intact.

  # NOTE ON TRUST: what Bob inherits by cloning is the TEAM tier — the project's
  # own committed context, first-party, no review. That is a different tier from
  # anything the project REFERENCES from elsewhere, which is still gated on Bob
  # trusting the publisher's key. Onboarding must not become a way to smuggle
  # untrusted content onto a fresh machine (see J3 for the trust story itself).

  Background:
    Given the team's project carries the context Carol has standardized on
    And Bob has just joined and has never used ctxloom before

  # LOCKED — the whole journey in one line: cloning IS the setup. No init
  # interview, no checklist, no copied prompts.
  Scenario: Bob clones the project and his assistant already has the team's context
    When Bob clones the project
    And Bob starts a session
    Then his assistant receives the team's standardized context
    And he was not asked to configure anything to get it

  # LOCKED — REPRODUCIBILITY, the point of the lockfile. Bob must get what the
  # TEAM pinned, not whatever the remote is serving today. Without this, "we all
  # share one standard" is untrue the moment an upstream moves.
  Scenario: Bob receives the versions the team pinned, not the latest ones
    Given the project pins the versions of the context it draws from elsewhere
    And an upstream has since published a newer version
    When Bob clones the project
    And Bob fetches the context the project draws on
    Then he receives the pinned versions, the same ones the rest of the team has
    And he does not receive the newer upstream version

  # LOCKED — the trust gate SURVIVES onboarding. A fresh machine must not be a
  # loophole: content the project references from a publisher Bob has not trusted
  # is HELD, exactly as if he had encountered it any other way. The team's OWN
  # context still flows — the two tiers are decided independently.
  Scenario: Content from a publisher Bob has not trusted is held, even on a fresh clone
    Given the project references a bundle published by Trent's company
    And Bob has not trusted the company key
    When Bob clones the project
    And Bob fetches the context the project draws on
    Then the company's content is held for his review
    But his assistant still receives the team's own context, because the project is first-party

  # LOCKED — and the gate opens the ordinary way, which shows the hold was about
  # trust and not about being new.
  Scenario: Once Bob trusts the company key, the held content reaches him
    Given the project references a bundle published by Trent's company
    And Bob has cloned the project and the company's content is held for his review
    When Bob trusts the company key
    And Bob starts a session
    Then his assistant receives the company's content

  # LOCKED — GRACEFUL DEGRADATION and its contrast in one place, the case a new
  # machine makes unavoidable: Bob will have some of the team's companion tools and
  # not others. Whatever does not depend on the missing companion must arrive and
  # nothing may fail — a setup that breaks on an absent optional tool is one no new
  # hire can complete. The companion-dependent guidance reaches him ONLY when the
  # companion is present, which is what makes the degradation meaningful rather than
  # silent loss. (A setup with the companion is Bob-on-a-fuller-machine, not a
  # different person — so the Background still holds for both rows.)
  Scenario Outline: Companion-dependent guidance reaches Bob only if he has the companion
    Given the team's context includes guidance for the "reprise" companion
    And the "reprise" companion is <presence> on Bob's machine
    When Bob clones the project
    And Bob starts a session
    Then his assistant receives the team's context that does not depend on reprise
    And the reprise-dependent guidance <reaches> his assistant
    And nothing fails because of the companion's presence or absence

    Examples:
      | presence      | reaches        |
      | installed     | reaches        |
      | not installed | does not reach |

  # LOCKED — hermetic materialization, over the same four-engine axis J5
  # proved (j5_multi_engine.feature's Outline A: claude-code, kiro,
  # antigravity, codex — reusing that engine-axis machinery, not re-deriving
  # it, see engineContextRelPath in steps_j5.go). Bob is precisely the person
  # most likely to be on a different engine from the rest of the team, so a
  # journey whose thesis is "cloning IS the onboarding" must prove this on
  # more than one engine, not just claude-code.
  #
  # codex IS a row now (taskloom lanky-plop/tiny-ooze): `profile materialize`
  # used to leave codex's context surface a silent no-op (keyed on
  # agent.SurfaceInputs.Fragments, which materialize never populates); codex's
  # context surface now ALSO writes AGENTS.md from agent.SurfaceInputs.Context,
  # which materialize does populate — the same fix J5's own materialization
  # outline proves.
  Scenario Outline: Bob's engine is not Alice's and the team's context still reaches him natively
    When Bob clones the project
    And Bob starts a session on <engine>
    Then his assistant receives the team's standardized context in <engine>'s own native surface

    Examples:
      | engine      |
      | claude-code |
      | kiro        |
      | antigravity |
      | codex       |

  # U3's axis-aware row (FLOWS-UNIFIED.md §3, finding class (b)): "what Bob
  # does NOT inherit on engine X". The outline above is the reassuring half —
  # four engines, same context, each in its own idiom. This is the other
  # half, and it is the one that bit somebody: Bob's deskmate uses opencode,
  # got the same bundle, and did NOT get the team's guardrails.
  #
  # The loss is STRUCTURAL, not a bug: internal/opencode's NewSurfaces
  # registers a context surface, a folded settings+MCP surface, commands and
  # skills — and no hook surface at all, because opencode has no
  # ctxloom-managed hook mechanism to write into. Three of the four things
  # the team shares do cross; the fourth cannot.
  #
  # Asserting the loss on the payload matters more here than usual, because
  # the deceiving signal is so strong: `profile materialize --backend
  # opencode` exits 0 and prints "wrote context / wrote settings / wrote
  # commands / wrote skills". Every line is true. Nothing is false. The
  # guardrail is simply not in the list, and no reader was ever going to
  # notice an absence in a success report.
  Scenario: Bob's opencode deskmate inherits the team's context and commands, but its hooks cannot follow
    Given Carol's team profile carries a shared fragment, command, MCP server, and hook
    When Alice materializes the team profile for opencode
    Then the materialized opencode context carries the shared fragment's marker, in its own native shape
    And the materialized opencode MCP configuration carries the shared server's command, in its own native shape
    And the materialized opencode command file carries the shared command's body, in its own native shape
    And no opencode surface anywhere in the materialized tree carries the shared hook's command

  # @wip — THE PRODUCT GAP, stated as the scenario that would prove it fixed.
  # The scenario above proves the loss is real; this one asks for the thing
  # that would have saved Bob's deskmate an afternoon: the materialize report
  # naming what it could not deliver. There is no per-engine capability-loss
  # report at materialize time today (FLOWS-UNIFIED.md §4 finding class (b),
  # boundary B7's axis face), so this cannot honestly go green and is not
  # pretended into one.
  #
  # It fails on the RIGHT thing: `profile materialize --backend opencode`
  # never mentions hooks in any form, so the assertion is not sensitive to
  # the eventual wording — only to whether the loss is reported at all.
  #
  # UNTAG CONDITION: when `profile materialize` reports the surfaces an
  # engine cannot carry, and the opencode run's report mentions the hook it
  # dropped. Until then this journey's honest state is "one silent per-engine
  # loss, discovered by accident rather than by tool".
  @wip
  Scenario: Materializing for an engine that cannot carry hooks says so
    Given Carol's team profile carries a shared fragment, command, MCP server, and hook
    When Alice materializes the team profile for opencode
    Then the materialize report names the hook it could not deliver to opencode
