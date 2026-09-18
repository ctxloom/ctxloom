Feature: Trusting and untrusting ITEM CONTENT, on the noun that owns it
  Trust is a posture toward something, so the verb lives on the noun that owns
  it. `bundle trust`/`bundle reject` decide about ITEM CONTENT — one fragment,
  command, MCP server or hook at a time — and that is what this file covers.
  The sibling decisions live with their own nouns: which BINARIES may be
  executed (cli/companion.feature), whose SIGNATURE stands in for review at all
  (cli/signer.feature), and where signed content may be PUBLISHED
  (cli/remote.feature). See the note at the foot of this file.

  Each is proven in BOTH directions, because one direction alone proves
  nothing: a scenario that only checks the trusted path passes against a system
  that admits everything, which is the most dangerous failure there is and
  looks like a green suite. So each decision below is exercised trusted (the
  thing happens) and untrusted (it does not), in the same fixture, and the
  refusal is checked to say so out loud.

  What is asserted is the EFFECT, not the echo. "Approved demo#fragments/guide"
  is the argument printed back; it is true of a command that recorded nothing.
  Neutering countersign.Store's write paths until the store recorded NOTHING AT
  ALL once left every scenario here green — the exact product failure
  j001600_signing.feature describes in prose ("exit 0, a success message naming
  the key, and no effect, in the flagship trust command"). So each decision is
  read back out of the store by the same lookup the trust gate performs, and
  the refusals are read out of the delivered state.

  See docs/trust-model.md for the model these commands operate.

  # The acceptance is asserted on the RECORD only, and deliberately: this
  # bundle is project-authored, and local content is auto-allowed ahead of any
  # review, so the fragment reads "accepted" before anyone accepts anything —
  # a state assertion there would be tautological a second time. The rejection
  # beats that local allowance, so it is asserted both ways, including that it
  # left the bundle's OTHER fragment alone.
  # Tabled by format: both `bundle trust` and `bundle reject` are wired to
  # emit(), so off a terminal (which this harness always is) the no-flag row
  # now gets the JSON result object the operations layer returns, not the
  # prose lines the old assertion checked unconditionally. The store-effect
  # steps (approvals store / withheld-from-agent) are unaffected by
  # --format — they read the record ctxloom wrote, not the CLI's rendering
  # of it — so they stay as they were, run once per row under whichever
  # <flags> that row names.
  Scenario Outline: A per-item acceptance and a rejection are recorded
    # Acceptance now countersigns content BYTES (a signing-key fingerprint),
    # not a hash-pair ledger entry — there is no "sha256:" content hash to
    # print. With no signing key configured the fixture takes the unsigned
    # degraded path (spec S9.5), which the CLI says so plainly.
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "guide" in bundle "demo" exists
    When I run "ctxloom <flags> bundle trust demo#fragments/guide"
    Then the command succeeds
    And the output reports "ref" as "<names the trusted item>"
    And the output reports "unsigned" as "<shown as unsigned>"
    And the approvals store holds an acceptance of "demo#fragments/guide" over the fragment's current bytes
    When I run "ctxloom <flags> bundle reject demo#fragments/guide"
    Then the command succeeds
    And the output reports "ref" as "<names the rejected item>"
    And the output reports "content_forms" containing "<names the form>"
    And the approvals store holds a rejection of "demo#fragments/guide": a sticky ref block and a content block over the same bytes
    And "demo#fragments/guide" is withheld from the agent, and the bundle's other fragment is not

    Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
      | flags         | names the trusted item          | shown as unsigned | names the rejected item          | names the form            |
      |               | demo#fragments/guide             | true               | demo#fragments/guide              | raw                        |
      | --format json | demo#fragments/guide             | true               | demo#fragments/guide              | raw                        |
      | --format text | Approved demo#fragments/guide    | UNSIGNED           | Rejected demo#fragments/guide     | rejected in form(s) raw   |

  # THE OTHER THREE DECISIONS MOVED, and each to the noun that owns it, now
  # that every noun has a comprehensive spec of its own:
  #
  #   `companion trust`/`untrust`  → cli/companion.feature, which keeps both
  #     directions (the witness file proving an unconfirmed binary never ran,
  #     and that a trusted one does) and adds the provenance exemption.
  #   `signer trust`/`untrust`     → cli/signer.feature, including the
  #     outside-a-project fallback to the user store that used to live here.
  #   Publish destinations have no verb at all, and that is the model rather
  #     than a gap: registering a remote IS the consent to publish there.
  #     `remote create` names a URL deliberately and `bundle push` honors it
  #     without asking a second time (cli/remote.feature). The ledger that once
  #     recorded a separate blessing is gone.
  #
  # What stays here is item-content trust, which is a decision about BYTES
  # inside a bundle rather than about a key, a binary or an address — and the
  # both-directions discipline above, which every one of those files inherits.
