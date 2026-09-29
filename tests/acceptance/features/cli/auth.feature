@doc
Feature: auth — the credentials ctxloom-launched engines authenticate with

  Covers: `ctxloom auth set`, `auth status`, `auth mint`.

  An agent declares HOW its engine authenticates (`auth:` on its binding);
  this noun holds the credential for the modes ctxloom stores — a minted token,
  or a key you already have — owner-only under your home, never in a config
  file or a repository.

  Everything here runs against the scenario's own temporary home, so the
  credential written is a fixture string and nothing real is created or read.

  WHAT THIS FILE CANNOT ASSERT, AND SAYS SO. `auth mint` runs the ENGINE's own
  interactive flow (claude: `claude setup-token`, which opens a browser). No
  hermetic scenario can complete that flow, and none should: it would mint a
  real credential. What is specifiable is its refusal off a terminal — the
  shape every delegated or unattended run takes — which is also the case that
  must never hang waiting on a prompt nobody will answer.

  Rule: A credential is stored from stdin, and only ever reported, never shown

    # THE EFFECT IS THE STORED FILE, read back through the command that owns
    # it. A `set` that printed its confirmation and wrote nothing would pass an
    # output-only check; `auth status` reading "stored" off the real file does
    # not. The secret itself must appear in neither command's output: that is
    # the half of the contract a leak would break silently.
    Scenario: A key piped to set is stored owner-only, and status reports it without revealing it
      Given an initialized ctxloom project
      When I run "ctxloom auth set --engine claude-code --mode api-key --format text" with input:
        """
        sk-acceptance-fixture-not-a-real-key
        """
      Then the command succeeds
      And the output contains "stored the claude-code api-key credential"
      And the output does not contain "sk-acceptance-fixture-not-a-real-key"
      And the home file ".ctxloom/auth/claude-code.api-key" contains "sk-acceptance-fixture-not-a-real-key"
      When I run "ctxloom auth status --format json"
      Then the command succeeds
      And the output does not contain "sk-acceptance-fixture-not-a-real-key"
      And the output reports "[mode=api-key].stored" as "true"
      And the output reports "[mode=token].stored" as "false"

    Scenario: With nothing stored, status says so for every mode it could hold
      Given an initialized ctxloom project
      When I run "ctxloom auth status --format text"
      Then the command succeeds
      And the output contains "claude-code api-key: none stored at"
      And the output contains "claude-code token: none stored at"

    # A mode that keeps no credential (the human's own login) has nothing to
    # store. Accepting the input anyway would write a file no run ever reads.
    Scenario: A mode that stores nothing is refused, and nothing is written
      Given an initialized ctxloom project
      When I run "ctxloom auth set --engine claude-code --mode login" with input:
        """
        sk-acceptance-fixture-not-a-real-key
        """
      Then the command fails
      And the output contains "stores no credential"
      And exactly 0 home files match ".ctxloom/auth/*"

  Rule: mint runs the engine's own flow, and refuses where there is no terminal to run it on

    Scenario: Minting without a terminal is refused rather than left waiting on a prompt
      Given an initialized ctxloom project
      When I run "ctxloom auth mint --engine claude-code --mode token"
      Then the command fails
      And the output contains "needs a terminal on stdin"
      And exactly 0 home files match ".ctxloom/auth/*"
