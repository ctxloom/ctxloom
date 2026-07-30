Feature: The coordination tools advertise a closed message-kind vocabulary

  An agent only knows what a mailbox message IS because the tool surface tells
  it. That used to be a free-text convention — put a `kind` key in the
  `structured` object, spelled however you like — and "however you like"
  included `approval_request`, the coordinator's own kind for an escalation
  WAITING ON A HUMAN DECISION. A child could name it, and the coordinator
  interpolated the name into the notice the receiving model saw: a forged
  approval prompt, assembled out of a string nobody validated.

  So `kind` is now a field with a closed vocabulary. The value an agent may
  send is enumerated in the tool's own schema, the coordinator's own kinds are
  refused from a sender, and there is nowhere left to write a kind that means
  something the vocabulary does not.

  This is a DELIBERATE BREAK to what `agent_recv` returns, taken before 1.0
  rather than carried forever: `kind` moved out of `structured` and onto the
  message. These scenarios are what makes that break visible — a silent
  reversion to the free-string convention, or an accidental widening of the
  vocabulary, comes back red here.

  # WHAT THIS FEATURE CAN AND CANNOT SEE (see steps_coordination_contract.go):
  # it reads the runner-terminated MCP surface — the proto-canonical one a real
  # harness gets through `ctxloom run` / `ctxloom acp`, enumerated over a real
  # MCP transport. That surface is the ONLY place a coordination tool's RESULT
  # shape is advertised, and no external MCP client in this suite can reach a
  # spawned session's per-cell runner socket. What these scenarios claim is
  # about the advertised CONTRACT. They deliberately do not claim anything
  # about a delivered message's runtime payload: that needs the runner
  # topology, and asserting it from here would be an overclaim.

  Background:
    Given the coordination tool surface a harness receives

  Scenario: agent_send advertises the kind field, not a kind convention buried in structured
    When I read the "agent_send" tool's input contract
    Then it advertises "MESSAGE_KIND_RESULT"
    And it advertises "MESSAGE_KIND_QUESTION"
    # The retired convention: `structured` no longer tells an agent to name the
    # kind inside it, and the old lowercase spellings are gone from the surface.
    And it does not advertise "envelope whose `kind` names the message kind"
    And it does not advertise "result | question | error"

  Scenario: agent_recv's result carries the kind on the message itself — the sanctioned break
    When I read the "agent_recv" tool's result contract
    Then it advertises "MESSAGE_KIND_APPROVAL_REQUEST"
    And it advertises "MESSAGE_KIND_EXITED"
    # The break, stated as the assertion: a recipient is told to read `kind`,
    # and is no longer told that `structured` carries it.
    And it advertises "read the `kind` field"
    And it does not advertise "`kind` names the message kind"

  # The vocabulary is CLOSED, and closed means enumerated: a value that is not
  # on this list cannot be named at all, and one that is added to the proto
  # without a decision shows up here rather than in a model's context.
  Scenario: the advertised vocabulary is exactly the declared one, on both tools
    When I read the "agent_send" tool's input contract
    Then the kind vocabulary it advertises is exactly:
      | MESSAGE_KIND_UNSPECIFIED      |
      | MESSAGE_KIND_MESSAGE          |
      | MESSAGE_KIND_RESULT           |
      | MESSAGE_KIND_ERROR            |
      | MESSAGE_KIND_QUESTION         |
      | MESSAGE_KIND_APPROVAL_REQUEST |
      | MESSAGE_KIND_USER_INJECTED    |
      | MESSAGE_KIND_USER_CONTROL     |
      | MESSAGE_KIND_EXITED           |
      | MESSAGE_KIND_STEER            |
    When I read the "agent_recv" tool's result contract
    Then the kind vocabulary it advertises is exactly:
      | MESSAGE_KIND_UNSPECIFIED      |
      | MESSAGE_KIND_MESSAGE          |
      | MESSAGE_KIND_RESULT           |
      | MESSAGE_KIND_ERROR            |
      | MESSAGE_KIND_QUESTION         |
      | MESSAGE_KIND_APPROVAL_REQUEST |
      | MESSAGE_KIND_USER_INJECTED    |
      | MESSAGE_KIND_USER_CONTROL     |
      | MESSAGE_KIND_EXITED           |
      | MESSAGE_KIND_STEER            |

  # A sender is told, in the schema it reads before choosing an argument, that
  # the coordinator's own kinds are not its to name AND that a wrong value is
  # REFUSED rather than accepted-and-ignored. An agent that learns either of
  # those only from a rejection has already spent a turn on it — and one that
  # believes an unknown kind passes through will keep sending it.
  Scenario: the surface states the rejection behaviour, not just the vocabulary
    When I read the "agent_send" tool's input contract
    Then it advertises "REQUIRED"
    And it advertises "CLOSED vocabulary of exactly four values"
    And it advertises "is REJECTED from a sender, not quietly downgraded"
    And it advertises "An unrecognised value is likewise REJECTED rather than passed through"
    # The four sender-legal values, named as such — `message` included, which
    # the retired free-text doc never listed.
    And it advertises "MESSAGE_KIND_MESSAGE (plain prose"
    And it advertises "MESSAGE_KIND_ERROR"

  # The tool DESCRIPTION carries it too, not only the per-field schema: some
  # harnesses surface the description and elide argument docs, and an agent that
  # sees only the description would otherwise learn nothing about the closure.
  Scenario: the tool description itself states the vocabulary is closed
    When I read the "agent_send" tool's description
    Then it advertises "`kind` is REQUIRED and its vocabulary is CLOSED"
    And it advertises "is REFUSED rather than accepted-and-ignored"

  # A recipient's guarantee is the mirror image of the sender's constraint: a
  # kind it reads is trustworthy as to PROVENANCE, because a sender could not
  # have set the reserved ones. That is the whole point of the split, and it is
  # what makes an approval prompt believable.
  Scenario: agent_recv tells a recipient which kinds a sender could not have set
    When I read the "agent_recv" tool's result contract
    Then it advertises "A SENDER could only have set one of"
    And it advertises "minted by the coordinator itself and are therefore trustworthy as to provenance"
    And it advertises "an unrecognised value is refused at ingress rather than delivered unclassified"
