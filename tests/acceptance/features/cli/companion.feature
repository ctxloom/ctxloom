@doc
Feature: companion — which binaries on your machine ctxloom may execute

  Covers: `ctxloom companion list`, `companion show`, and the bare
  `ctxloom companion` form.

  A companion is a program that CONTRIBUTES context — the shipped ltk,
  taskloom and reprise, plus anything named `ctxloom-companion-*`. Companions
  are DISCOVERED, not configured: ctxloom scans $PATH for those names. And
  reading what a companion contributes means RUNNING it, which is why this
  noun exists at all.

  WHY A GATE. `./node_modules/.bin` is on $PATH in a large share of JavaScript
  projects, and an npm package — including a transitive dependency nobody
  chose — can ship a binary under any name. Shipping
  `ctxloom-companion-anything` once earned an exec at the next session start
  with no user action at all. That attacker never controlled $PATH; they
  name-squatted an auto-exec convention in a directory already on it.

  THE GATE IS A SIGNATURE. A companion is executed when a detached
  `<binary>.sig` beside it verifies, in the namespace
  `companion.v1.ctxloom.dev`, against a key in your allowed_signers. Nothing
  else admits one: no recorded consent, no exemption for a familiar name, no
  prompt. Three refusals are distinguished and none of them is silent — no
  signature, a signature that does not cover those bytes, and a signer you
  have not authorized to say "this may run here".

  THERE IS NO COMMAND TO APPROVE OR REFUSE ONE, and none is needed. To stop
  ctxloom running a companion, take away what admits it: delete its `.sig`, or
  rename the binary so discovery no longer finds it. Both are ordinary file
  operations that need no record kept in step with them.

  WHY NOT A RECORDED DECISION, which is what this used to be: consent was
  keyed to the binary's path AND its SHA-256, so every rebuild voided it and
  asked again — and a prompt that fires on every `just install` is what trains
  people to approve without reading. A signature survives a rebuild by the
  same publisher, and travels with the binary rather than depending on where
  it landed.

  Deliberately NO MCP tools for any of this, matching every other trust
  surface: handing the agent the ability to admit the binaries that run
  alongside it defeats the property the gate exists to provide.

  Trust in a signing KEY is a different decision on a different noun — see
  cli/signer.feature.

  This is the comprehensive per-noun spec: what the noun DOES, leaf by leaf.

  Rule: A companion nobody vouched for is never executed

    The assertion that matters is not the exit code and not the warning — it
    is WHICH BINARIES ACTUALLY RAN. The fake companion in these fixtures
    appends to a witness file every time it is invoked, so "was never
    executed" is read off the filesystem rather than inferred from a missing
    line of output. An admission gate is exactly the kind of change that
    passes every exit-code assertion while quietly doing nothing — or quietly
    doing everything.

    Scenario: An unsigned companion is skipped, said so, and runs once signed
      Given an initialized ctxloom project
      And a discovered companion "ctxloom-companion-acme" is on PATH, unsigned
      When I run "ctxloom doctor"
      Then the output contains "no signature beside it"
      And the companion "ctxloom-companion-acme" was never executed
      When the companion "ctxloom-companion-acme" is signed by a publisher this project trusts
      And I run "ctxloom doctor"
      Then the companion "ctxloom-companion-acme" was executed

    # The two failing signatures are SEPARATE outcomes, and this pins that
    # they are reported as such. Collapsing either into "unsigned" would tell
    # a reader nobody vouched for the binary when in fact somebody did — a
    # stranger, or the right publisher for different bytes. Those call for
    # different actions.
    Scenario: A signature from a stranger is refused, and named as its own refusal
      Given an initialized ctxloom project
      And a discovered companion "ctxloom-companion-acme" is on PATH, unsigned
      And the companion "ctxloom-companion-acme" is signed by a key this project does not trust
      When I run "ctxloom doctor"
      Then the output contains "signed by a key you do not trust"
      And the companion "ctxloom-companion-acme" was never executed

    Scenario: A companion edited after signing is refused, and does not read as unsigned
      Given an initialized ctxloom project
      And a discovered companion "ctxloom-companion-acme" is on PATH, unsigned
      And the companion "ctxloom-companion-acme" is edited after it was signed
      When I run "ctxloom doctor"
      Then the output contains "does not cover these bytes"
      And the companion "ctxloom-companion-acme" was never executed

  Rule: The verdict is inspectable one binary at a time

    `companion show` runs the EXACT SAME decision cascade the real probes
    consult (config.AdmitCompanions), so its answer can never disagree with
    what actually happens at session start. Merely LOOKING never executes
    anything: a reporting command that ran a foreign binary to describe it
    would be the exec this noun exists to gate.

    Scenario Outline: Show answers whether ctxloom would execute one binary, and why
      Given an initialized ctxloom project
      And a discovered companion "ctxloom-companion-acme" is on PATH, unsigned
      When Alice asks whether one binary would run:
        """
        ctxloom companion show ctxloom-companion-acme <flags>
        """
      Then the command succeeds
      And the output reports "allowed" as "<not allowed yet>"
      And the output reports "reason" as "<because unsigned>"
      When the companion "ctxloom-companion-acme" is signed by a publisher this project trusts
      And I run "ctxloom companion show ctxloom-companion-acme <flags>"
      Then the command succeeds
      And the output reports "allowed" as "<now allowed>"
      And the output reports "reason" as "<because signed>"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | not allowed yet | because unsigned | now allowed | because signed |
        |               | false           | unsigned         | true        | signed         |
        | --format json | false           | unsigned         | true        | signed         |
        | --format text | DENIED          | unsigned         | allowed     | signed         |

    # "not installed" and "found but refused" are different facts about the
    # machine, and collapsing them into one silence is the shape this whole
    # noun exists to avoid. The positive case above ran first in this same
    # file; here the name simply resolves to nothing.
    Scenario Outline: A name that resolves to nothing says so, rather than reporting a refusal
      Given an initialized ctxloom project
      When I run "ctxloom companion show ctxloom-companion-nowhere <flags>"
      Then the command succeeds
      And the output reports "reason" as "<not installed, not refused>"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | not installed, not refused |
        |               | not-installed              |
        | --format json | not-installed              |
        | --format text | not found                  |

  Rule: The bare noun reports the live verdict for everything discovered

    `companion list` reports what WOULD happen on the next run, derived from
    each binary's signature — not a record of what someone once agreed to.
    There is no stored decision left to print, and the answer now lives with
    the file, which is what makes the listing current by construction.

    Scenario: Bare companion reports the verdict for what is on PATH
      Given an initialized ctxloom project
      And a discovered companion "ctxloom-companion-acme" is on PATH, unsigned
      When I run "ctxloom companion"
      Then the command succeeds
      And the output contains "ctxloom-companion-acme"
      And the output contains "unsigned"
      And the output does not contain "Available Commands:"
      And the companion "ctxloom-companion-acme" was never executed
