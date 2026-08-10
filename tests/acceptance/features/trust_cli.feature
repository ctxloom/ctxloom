Feature: Trusting and untrusting, on the noun that owns the thing
  Trust is a posture toward something, so the verb lives on the noun that owns
  it. `bundle trust`/`bundle untrust` decide about ITEM CONTENT — one fragment,
  command, MCP server or hook at a time. `companion trust`/`untrust` decide
  which binaries may be EXECUTED. `remote trust`/`untrust` decide where signed
  content may be PUBLISHED. `signer trust`/`untrust` decide whose signature
  stands in for review at all.

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
  Scenario: A per-item acceptance and a rejection are recorded
    # Acceptance now countersigns content BYTES (a signing-key fingerprint),
    # not a hash-pair ledger entry — there is no "sha256:" content hash to
    # print. With no signing key configured the fixture takes the unsigned
    # degraded path (spec S9.5), which the CLI says so plainly.
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "guide" in bundle "demo" exists
    When I run "ctxloom bundle trust demo#fragments/guide"
    Then the command succeeds
    And the output contains "Approved demo#fragments/guide"
    And the output contains "UNSIGNED"
    And the approvals store holds an acceptance of "demo#fragments/guide" over the fragment's current bytes
    When I run "ctxloom bundle untrust demo#fragments/guide"
    Then the command succeeds
    And the output contains "Rejected demo#fragments/guide"
    And the output contains "rejected in form(s) raw"
    And the approvals store holds a rejection of "demo#fragments/guide": a sticky ref block and a content block over the same bytes
    And "demo#fragments/guide" is withheld from the agent, and the bundle's other fragment is not

  # Companion EXEC consent. Companions are DISCOVERED, not configured — the
  # shipped names plus anything called ctxloom-companion-* on $PATH — and
  # reading one's loadout means RUNNING it. ./node_modules/.bin is on $PATH in
  # a large share of JS projects, so without a gate an npm dependency nobody
  # chose could earn an exec at the next session start just by picking the
  # name. The proof below is the witness file the fake writes when it runs, not
  # an exit code: the failure mode being closed is the silent one.
  Scenario: A companion nobody confirmed is never executed, and says so
    Given an initialized ctxloom project
    And a discovered companion "ctxloom-companion-acme" is on PATH, never confirmed
    When I run "ctxloom doctor"
    Then the output contains "never confirmed for execution"
    And the companion "ctxloom-companion-acme" was never executed
    When I run "ctxloom companion trust ctxloom-companion-acme"
    Then the command succeeds
    And the output contains "ctxloom will run it"
    When I run "ctxloom doctor"
    Then the companion "ctxloom-companion-acme" was executed

  # Asserted by the acme entry's PRESENCE and ABSENCE rather than by an empty
  # listing: the scenario HOME legitimately starts with consent recorded for
  # whatever real companions this machine has installed (testenv grants those
  # so the suite behaves as it did before exec consent landed), and an
  # "is it empty" assertion would be an assertion about the developer's laptop.
  Scenario: Companion execution decisions are listable and revocable
    Given an initialized ctxloom project
    And a discovered companion "ctxloom-companion-acme" is on PATH, never confirmed
    When I run "ctxloom companion list"
    Then the command succeeds
    And the output does not contain "ctxloom-companion-acme"
    When I run "ctxloom companion trust ctxloom-companion-acme"
    Then the command succeeds
    When I run "ctxloom companion list"
    Then the command succeeds
    And the output contains "allowed"
    And the output contains "ctxloom-companion-acme"
    When I run "ctxloom companion untrust ctxloom-companion-acme"
    Then the command succeeds
    And the output contains "forgot 1 decision(s)"
    When I run "ctxloom companion list"
    Then the command succeeds
    And the output does not contain "ctxloom-companion-acme"

  # Publish destinations. The first publish to a given non-GitHub remote asks
  # once and records the answer; a session with nobody to ask REFUSES. That
  # refusal used to be a dead end for exactly the callers it fires for: the
  # only way to record a confirmation was to "run the same publish once from an
  # interactive terminal", which a CI runner and an agent host cannot do. These
  # three leaves are the provisioning path, and the assertions are on the
  # recorded DECISION each one produces — an empty listing and a listing that
  # never mentions the remote are the same pixels and very different facts, so
  # both the presence and the absence are asserted.
  Scenario: Publish destinations are listable, allowable and revocable without a terminal
    Given an initialized ctxloom project
    When I run "ctxloom remote trusted"
    Then the command succeeds
    And the output contains "no publish destinations recorded"
    When I run "ctxloom remote trust https://git.example.com/team/bundles"
    Then the command succeeds
    And the output contains "ctxloom will publish there without asking"
    When I run "ctxloom remote trusted"
    Then the command succeeds
    And the output contains "allowed"
    And the output contains "https://git.example.com/team/bundles"
    # One repository is ONE destination: the ssh spelling of the URL just
    # allowed must already be recorded, never asked about a second time.
    When I run "ctxloom remote untrust git@git.example.com:team/bundles.git"
    Then the command succeeds
    And the output contains "forgot 1 decision(s)"
    When I run "ctxloom remote trusted"
    Then the command succeeds
    And the output contains "no publish destinations recorded"
    # Undoing something nobody recorded reports zero rather than succeeding silently.
    When I run "ctxloom remote untrust https://git.example.com/team/bundles"
    Then the command succeeds
    And the output contains "forgot 0 decisions"

  # `signer trust` defaults to the committable PROJECT store (j001600_signing
  # proves the in-project cases). This is the edge that default has to
  # handle: run with no .ctxloom directory at all, there is no project store
  # to write. It must not fail — it falls back to the user store, and the
  # output names WHICH store it used and WHY, because silently writing
  # somewhere other than where the user expects is exactly the defect shape
  # this project keeps removing.
  Scenario: Trusting a signer outside a project falls back to the user store and says so
    Given an empty project directory
    When I run "ctxloom signer trust solo@example.com --key 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPu3qoOrcLwuHKdsczSsVcMrm+R6iPISwuP1K1/82kLr acme-fallback-test' --yes"
    Then the command succeeds
    And the output contains "no project"
    And the output contains "user store"
    And the home file ".ctxloom/allowed_signers" exists
    And the home file ".ctxloom/allowed_signers" contains "solo@example.com"
