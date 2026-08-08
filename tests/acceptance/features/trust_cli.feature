Feature: Trust posture CLI
  Per-item acceptances (`trust accept`) and rejections (`trust reject`) manage
  what content the agent sees. The retired whole-bundle postures (`bundle
  trust`/`untrust`, `remote trust`/`untrust`) are DELETED commands, not
  deprecation stubs — trust is now keyed to a publisher signing key, not a
  bundle or remote (see docs/trust-model.md, docs/trust-simplify-plan.md).

  # RECORDED is the claim in the title, and until the audit (irate-catfish, F2)
  # nothing here checked it: every assertion was an exit code plus a substring
  # of the output, and "Approved demo#fragments/guide" is the argument echoed
  # back. Neutering countersign.Store's write paths until the store recorded
  # NOTHING AT ALL left all four scenarios on this page green — the exact
  # product failure j16_signing.feature's @wip scenario describes in prose
  # ("exit 0, a success message naming the key, and no effect, in the flagship
  # trust command"). So each decision is now read back out of the store by the
  # same lookup the trust gate performs, and the rejection is read out of the
  # delivered state as well.
  #
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
    When I run "ctxloom trust accept demo#fragments/guide"
    Then the command succeeds
    And the output contains "Approved demo#fragments/guide"
    And the output contains "UNSIGNED"
    And the approvals store holds an acceptance of "demo#fragments/guide" over the fragment's current bytes
    When I run "ctxloom trust reject demo#fragments/guide"
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
    When I run "ctxloom trust companion allow ctxloom-companion-acme"
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
    When I run "ctxloom trust companion list"
    Then the command succeeds
    And the output does not contain "ctxloom-companion-acme"
    When I run "ctxloom trust companion allow ctxloom-companion-acme"
    Then the command succeeds
    When I run "ctxloom trust companion list"
    Then the command succeeds
    And the output contains "allowed"
    And the output contains "ctxloom-companion-acme"
    When I run "ctxloom trust companion forget ctxloom-companion-acme"
    Then the command succeeds
    And the output contains "forgot 1 decision(s)"
    When I run "ctxloom trust companion list"
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
    When I run "ctxloom trust publish list"
    Then the command succeeds
    And the output contains "no publish destinations recorded"
    When I run "ctxloom trust publish allow https://git.example.com/team/bundles"
    Then the command succeeds
    And the output contains "ctxloom will publish there without asking"
    When I run "ctxloom trust publish list"
    Then the command succeeds
    And the output contains "allowed"
    And the output contains "https://git.example.com/team/bundles"
    # One repository is ONE destination: the ssh spelling of the URL just
    # allowed must already be recorded, never asked about a second time.
    When I run "ctxloom trust publish forget git@git.example.com:team/bundles.git"
    Then the command succeeds
    And the output contains "forgot 1 decision(s)"
    When I run "ctxloom trust publish list"
    Then the command succeeds
    And the output contains "no publish destinations recorded"
    # Undoing something nobody recorded reports zero rather than succeeding silently.
    When I run "ctxloom trust publish forget https://git.example.com/team/bundles"
    Then the command succeeds
    And the output contains "forgot 0 decisions"
