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

  # FUTURE — deferred, NOT in the green run. Bob's engine may not be Alice's, and
  # the team's context must land in whichever engine's native surface he uses.
  # Held back because only claude-code is live-verified today; the other engines
  # are unverified by their own source comments, and asserting them here would be
  # claiming coverage we do not have. This becomes a Scenario Outline over the
  # engine table when J5 verifies them.
  # Scenario: Bob's engine is not Alice's and the team's context reaches him natively
  #   Given Bob uses a different engine from the rest of the team
  #   When Bob clones the project
  #   And Bob starts a session
  #   Then his assistant receives the team's standardized context in his engine's own surface
