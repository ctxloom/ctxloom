@doc
Feature: content decisions — ctxloom review, bundle trust, bundle reject, bundle forget

  COMMAND SURFACES COVERED HERE: `ctxloom review` (and `review --list`), and
  the three per-item verbs `ctxloom bundle trust`, `ctxloom bundle reject` and
  `ctxloom bundle forget`. They are four commands under two different nouns,
  and they belong in one file because they are ONE MECHANISM: a state machine
  with three states — approved, rejected, undecided — that decides what a
  third-party bundle's content is allowed to reach the agent.

  `review` is the porcelain a person uses; the three verbs are the plumbing a
  script uses. Testing them together is what pins that they AGREE: both write
  through the same mutation path, so a decision made either way must produce
  the same effect, and a decision cleared either way must clear the same
  records.

  One verb per state, each named for what it writes:

  | verb                 | records      | the item is                     |
  | bundle trust <ref>   | an approval  | delivered                       |
  | bundle reject <ref>  | a rejection  | withheld everywhere, always     |
  | bundle forget <ref>  | nothing      | undecided again, awaiting review |

  `forget` is the inverse of BOTH. Rejection is not the inverse of approval: it
  is sticky, it survives the content changing under the ref, and it beats a
  trusted publisher and a project's own content alike. Someone reaching for it
  to undo an approval does something stronger than they meant, which is why the
  second verb is named `reject` and why the third one exists at all.

  WHAT IS ASSERTED IS THE EFFECT — content delivered, content withheld, or an
  item back in the review queue — and never the command's echo of its own
  argument. "Approved demo#fragments/guide" is true of a command that recorded
  nothing; neutering the countersignature store's write paths until it recorded
  NOTHING AT ALL once left a whole file of scenarios green. So each decision is
  read back out of the materialized context, out of the pending list, or out of
  the store by the same lookup the exposure gate performs.

  EVERY TRANSITION IS PROVEN IN BOTH DIRECTIONS, in one fixture. A scenario
  that only checks the delivered path passes against a system that admits
  everything, which is the most dangerous failure there is and looks green; a
  scenario that only checks the withheld path is satisfied by a fixture where
  the content never existed. So each scenario below establishes the OPPOSITE
  state first and asserts it, which is what makes the assertion that follows
  capable of failing.

  The interactive walk's own keystrokes ([t]rust / [r]eject / [s]kip, [T]/[R]
  for the rest of a bundle) are pinned by unit tests — TestParseReviewChoice
  and TestRunReviewWalk_* in internal/cli — because `ctxloom review` degrades
  to the pending table off a TTY and cannot be driven interactively from here.
  What this file covers of the porcelain is that table and its agreement with
  the plumbing.

  See docs/trust-model.md for the model these commands operate.

  Rule: A third-party item is withheld until it is decided, and the decision decides the delivery

    @remote
    Scenario: Pending items are listed, withheld, and delivered once trusted
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --bundle origin/demo"
      And I run "ctxloom deps pull"
      # The pinned third-party bundle's items are born pending: review names them…
      When I run "ctxloom review --list"
      Then the command succeeds
      And the output contains "fragments/demo-frag"
      And the output contains "commands/demo-skill"
      And the output contains "new"
      # …and the content gate withholds them from the materialized context.
      When I run "ctxloom profile materialize dev --target out"
      Then the command succeeds
      And the file "out/CLAUDE.md" does not contain "Demo fragment content"
      # Trusting through the plumbing records the same approval the porcelain
      # writes: the item leaves the pending set and reaches the agent.
      When I accept the pending item "demo#fragments/demo-frag" from remote "origin"
      Then the command succeeds
      And the output contains "Approved"
      When I run "ctxloom review --list"
      Then the command succeeds
      And the output does not contain "fragments/demo-frag"
      And the output contains "commands/demo-skill"
      When I run "ctxloom profile materialize dev --target out"
      Then the command succeeds
      And the file "out/CLAUDE.md" contains "Demo fragment content"

    @remote
    Scenario: A rejected item stays withheld and leaves the pending set
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --bundle origin/demo"
      And I run "ctxloom deps pull"
      When I reject the pending item "demo#fragments/demo-frag" from remote "origin"
      Then the command succeeds
      And the output contains "Rejected"
      When I run "ctxloom review --list"
      Then the command succeeds
      And the output does not contain "fragments/demo-frag"
      When I run "ctxloom profile materialize dev --target out"
      Then the command succeeds
      And the file "out/CLAUDE.md" does not contain "Demo fragment content"

    Scenario: Nothing pending reads as a clean slate
      Given an initialized ctxloom project
      When I run "ctxloom review --list"
      Then the command succeeds
      And the output contains "Nothing is pending review."

  Rule: A decision can be withdrawn, and withdrawing it is not the opposite decision

    # THE CASE THE THIRD VERB EXISTS FOR, and the one most easily missed: a
    # REJECTION, not an approval. An approval invalidates itself the moment the
    # bytes move, so it half-expires on its own; a ref-level rejection is
    # sticky by design and nothing else on this surface can lift one.
    #
    # The final trust is the proof. A rejection beats an approval outright, so
    # if any part of it had survived the forget, the content would still be
    # missing from CLAUDE.md at the end — the delivered assertion cannot pass
    # over a half-cleared rejection.
    @remote
    Scenario: A rejection is withdrawn, and the item can then be decided the other way
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --bundle origin/demo"
      And I run "ctxloom deps pull"
      # Establish the rejection, and that it is really in force.
      When I reject the pending item "demo#fragments/demo-frag" from remote "origin"
      Then the command succeeds
      When I run "ctxloom review --list"
      Then the output does not contain "fragments/demo-frag"
      # Withdrawing it returns the item to the review queue — undecided, not
      # approved: still withheld, but a human is asked about it again.
      When I clear the decision on the pending item "demo#fragments/demo-frag" from remote "origin"
      Then the command succeeds
      And the output contains "Forgot"
      When I run "ctxloom review --list"
      Then the command succeeds
      And the output contains "fragments/demo-frag"
      When I run "ctxloom profile materialize dev --target out"
      Then the command succeeds
      And the file "out/CLAUDE.md" does not contain "Demo fragment content"
      # And the rejection is gone rather than hidden: an approval now delivers,
      # which it could not do while any component of a rejection stood.
      When I accept the pending item "demo#fragments/demo-frag" from remote "origin"
      Then the command succeeds
      When I run "ctxloom profile materialize dev --target out"
      Then the command succeeds
      And the file "out/CLAUDE.md" contains "Demo fragment content"

    # The other decided state. Here the delivered assertion comes FIRST, so the
    # "no longer delivered" assertion at the end is read off a fixture that
    # demonstrably delivered a moment earlier rather than one where the content
    # never arrived.
    @remote
    Scenario: An approval is withdrawn and the item goes back to awaiting review
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --bundle origin/demo"
      And I run "ctxloom deps pull"
      When I accept the pending item "demo#fragments/demo-frag" from remote "origin"
      Then the command succeeds
      When I run "ctxloom profile materialize dev --target out"
      Then the command succeeds
      And the file "out/CLAUDE.md" contains "Demo fragment content"
      When I clear the decision on the pending item "demo#fragments/demo-frag" from remote "origin"
      Then the command succeeds
      And the output contains "Forgot"
      When I run "ctxloom profile materialize dev --target out"
      Then the command succeeds
      And the file "out/CLAUDE.md" does not contain "Demo fragment content"
      When I run "ctxloom review --list"
      Then the command succeeds
      And the output contains "fragments/demo-frag"

    # Clearing nothing must not read as clearing something. A mistyped ref is
    # the everyday way to reach this, and "Forgot <ref>" over it would confirm
    # the typo as a success.
    @remote
    Scenario: Withdrawing a decision nobody made says so
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --bundle origin/demo"
      And I run "ctxloom deps pull"
      When I clear the decision on the pending item "demo#fragments/demo-frag" from remote "origin"
      Then the command succeeds
      And the output contains "No decision was recorded"
      And the output does not contain "Forgot"

  Rule: The plumbing writes real records, and clearing them really removes them

    # These read the STORE, by the same lookup the exposure gate performs,
    # because the printed line is the argument echoed back and is true of a
    # command that wrote nothing.
    #
    # The bundle here is PROJECT-AUTHORED, and local content is auto-allowed
    # ahead of any review — so the fragment reads "accepted" before anyone
    # accepts anything, and asserting delivery on the approval half would be
    # tautological. The approval's one observable consequence is the
    # countersignature. The REJECTION beats that local allowance, so it is
    # asserted on both the record and the delivered state, in both directions.
    Scenario: An approval and a rejection are recorded, and forgetting removes them
      # Decisions countersign content BYTES. With no signing key configured the
      # fixture takes the unsigned degraded path, which the CLI says plainly.
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "guide" in bundle "demo" exists
      When I run "ctxloom bundle trust demo#fragments/guide"
      Then the command succeeds
      And the output contains "Approved demo#fragments/guide"
      And the output contains "UNSIGNED"
      And the approvals store holds an acceptance of "demo#fragments/guide" over the fragment's current bytes
      # Forgetting takes the approval back off the record.
      When I run "ctxloom bundle forget demo#fragments/guide"
      Then the command succeeds
      And the output contains "Forgot demo#fragments/guide"
      And the approvals store holds no decision about "demo#fragments/guide"
      # A rejection writes both components, and beats the local allowance.
      When I run "ctxloom bundle reject demo#fragments/guide"
      Then the command succeeds
      And the output contains "Rejected demo#fragments/guide"
      And the output contains "rejected in form(s) raw"
      And the approvals store holds a rejection of "demo#fragments/guide": a sticky ref block and a content block over the same bytes
      And "demo#fragments/guide" is withheld from the agent, and the bundle's other fragment is not
      # Forgetting takes BOTH components back off the record, and the
      # first-party allowance applies again.
      When I run "ctxloom bundle forget demo#fragments/guide"
      Then the command succeeds
      And the approvals store holds no decision about "demo#fragments/guide"
      And "demo#fragments/guide" reaches the agent again, alongside the bundle's other fragment
