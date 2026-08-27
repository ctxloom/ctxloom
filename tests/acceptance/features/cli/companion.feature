@doc
Feature: companion — which binaries on your machine ctxloom may execute

  Covers: `ctxloom companion list`, `companion show`, `companion trust`,
  `companion untrust`, and the bare `ctxloom companion` form.

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
  name-squatted an auto-exec convention in a directory already on it. So a
  binary ctxloom has not run before is put to a human once, and the answer is
  recorded against the binary's absolute path AND its SHA-256: replace the
  file and you are asked again. A non-interactive session — an agent, CI, any
  piped invocation — is never prompted; the unconfirmed companion is skipped
  with a warning. These four leaves are how that decision is made, inspected
  and undone from a script or after the fact.

  Decisions live in ~/.ctxloom/companion_consent.yaml and have deliberately no
  committable project counterpart: a repo you cloned must not be able to
  arrive carrying pre-approved binaries.

  THE ONE EXEMPTION, and the one place in the whole trust model where a
  missing record does not deny: a first-party name resolving from the
  directory the running ctxloom itself lives in. It is pinned to LOCATION, not
  name — the name list is three guessable strings, so a name-only exemption
  would be the same hole in a smaller costume. Someone who can write to the
  directory holding the running ctxloom can replace ctxloom itself, so the
  gate would be defending a position they have already taken; meanwhile a
  prompt on every `just install` is what trains people to approve without
  reading. See docs/trust-model.md.

  Deliberately NO MCP tools for any of this, matching every other trust
  surface: handing the agent the ability to approve the binaries that run
  alongside it defeats the property the consent exists to provide.

  Trust in a signing KEY is a different decision on a different noun — see
  cli/signer.feature.

  This is the comprehensive per-noun spec: what the noun DOES, leaf by leaf.

  Rule: A companion nobody confirmed is never executed

    The assertion that matters is not the exit code and not the warning — it
    is WHICH BINARIES ACTUALLY RAN. The fake companion in these fixtures
    appends to a witness file every time it is invoked, so "was never
    executed" is read off the filesystem rather than inferred from a missing
    line of output. A consent gate is exactly the kind of change that passes
    every exit-code assertion while quietly doing nothing — or quietly doing
    everything.

    Scenario Outline: An unconfirmed companion is skipped, said so, and only runs once trusted
      Given an initialized ctxloom project
      And a discovered companion "ctxloom-companion-acme" is on PATH, never confirmed
      When I run "ctxloom doctor"
      Then the output contains "never confirmed for execution"
      And the companion "ctxloom-companion-acme" was never executed
      When Alice decides this binary may run:
        """
        ctxloom companion trust ctxloom-companion-acme <flags>
        """
      Then the command succeeds
      And the output reports "allowed" as "<ctxloom will run it>"
      When I run "ctxloom doctor"
      Then the companion "ctxloom-companion-acme" was executed

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | ctxloom will run it |
        |               | true                  |
        | --format json | true                  |
        | --format text | ctxloom will run it   |

  Rule: The decision is inspectable one binary at a time, and revocable

    `companion show` runs the EXACT SAME decision cascade the real probes
    consult (config.AdmitCompanions), so its answer can never disagree with
    what actually happens at session start. Merely LOOKING never prompts: a
    reporting command that could conjure a security question would be a
    question asked at a moment nobody chose.

    # Asserted by the acme entry's PRESENCE and ABSENCE rather than by an
    # empty listing: this scenario's HOME legitimately starts with consent
    # recorded for whatever real companions this machine has installed, and an
    # "is it empty" assertion would be an assertion about the developer's
    # laptop.
    Scenario Outline: Execution decisions are listable and revocable
      Given an initialized ctxloom project
      And a discovered companion "ctxloom-companion-acme" is on PATH, never confirmed
      When I run "ctxloom companion list <flags>"
      Then the command succeeds
      And the output does not contain "ctxloom-companion-acme"
      When I run "ctxloom companion trust ctxloom-companion-acme"
      Then the command succeeds
      When Alice reviews what she has allowed to run:
        """
        ctxloom companion list <flags>
        """
      Then the command succeeds
      And the output reports "[bin=ctxloom-companion-acme].allowed" as "<the decision is allowed>"
      When Alice takes the decision back:
        """
        ctxloom companion untrust ctxloom-companion-acme <flags>
        """
      Then the command succeeds
      And the output reports "forgot" as "<confirms one decision forgotten>"
      When I run "ctxloom companion list <flags>"
      Then the command succeeds
      And the output does not contain "ctxloom-companion-acme"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | the decision is allowed | confirms one decision forgotten |
        |               | true                     | 1                                 |
        | --format json | true                     | 1                                 |
        | --format text | allowed                  | forgot 1 decision(s)              |

    # The bare noun answers the question somebody typing it has, rather than
    # teaching them what they could have typed instead — and reading the
    # record executes nothing, which is why the bare form can be a listing at
    # all.
    Scenario: Bare companion lists the recorded decisions
      Given an initialized ctxloom project
      And a discovered companion "ctxloom-companion-acme" is on PATH, never confirmed
      And I run "ctxloom companion trust ctxloom-companion-acme"
      When I run "ctxloom companion"
      Then the command succeeds
      And the output contains "ctxloom-companion-acme"
      And the output does not contain "Available Commands:"

    Scenario Outline: Show answers whether ctxloom would execute one binary, and why
      Given an initialized ctxloom project
      And a discovered companion "ctxloom-companion-acme" is on PATH, never confirmed
      When Alice asks whether one binary would run:
        """
        ctxloom companion show ctxloom-companion-acme <flags>
        """
      Then the command succeeds
      And the output reports "allowed" as "<not allowed yet>"
      And the output reports "reason" as "<because unconfirmed>"
      When I run "ctxloom companion trust ctxloom-companion-acme"
      Then the command succeeds
      When I run "ctxloom companion show ctxloom-companion-acme <flags>"
      Then the command succeeds
      And the output reports "allowed" as "<now allowed>"
      And the output reports "reason" as "<because consented>"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | not allowed yet | because unconfirmed | now allowed | because consented |
        |               | false             | unconfirmed           | true          | consented            |
        | --format json | false             | unconfirmed           | true          | consented            |
        | --format text | DENIED            | unconfirmed           | allowed       | consented            |

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
        |               | not-installed                 |
        | --format json | not-installed                 |
        | --format text | not found                     |

  Rule: First-party binaries are exempt by PROVENANCE, never by name

    ltk, taskloom and reprise are executed with no recorded decision at all —
    but only when they resolve from the directory the running ctxloom binary
    itself lives in, which is where every install shape puts them together
    (`just install` → ~/go/bin, a Homebrew prefix, the devcontainer image,
    $GOBIN). That keeps routine rebuilds silent, which is the entire purpose:
    a prompt that fires on every reinstall trains reflex approval.

    A first-party NAME found anywhere else is a third-party binary that picked
    a familiar name, and goes through the gate like any other.

    # BOTH HALVES, IN ONE FIXTURE, because either alone proves nothing. Two
    # byte-identical fake companions are installed under two of the three
    # first-party names: one BESIDE the ctxloom being run, one somewhere else
    # on $PATH. Nothing is trusted anywhere in this scenario, so the only
    # thing that can separate their fates is where they resolved from.
    #
    # And the verdicts are read twice over — once from `companion show`'s
    # cascade, and once from the witness file after a real probe, which is the
    # only assertion that can tell an exemption that ADMITS from one that
    # merely says it would.
    Scenario Outline: The same binary is exempt beside ctxloom and refused anywhere else
      Given an initialized ctxloom project
      And ctxloom is installed beside a first-party companion "ltk"
      And a discovered companion "taskloom" is on PATH, never confirmed
      When Alice asks about the one that ships beside ctxloom:
        """
        ctxloom companion show ltk <flags>
        """
      Then the command succeeds
      And the output reports "allowed" as "<beside ctxloom, exempt>"
      And the output reports "reason" as "<because first-party>"
      When Alice asks about the same binary under a first-party name elsewhere:
        """
        ctxloom companion show taskloom <flags>
        """
      Then the command succeeds
      And the output reports "allowed" as "<elsewhere, refused>"
      And the output reports "reason" as "<because unconfirmed>"
      When I run "ctxloom doctor"
      Then the companion "ltk" was executed
      And the companion "taskloom" was never executed

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | beside ctxloom, exempt | because first-party | elsewhere, refused | because unconfirmed |
        |               | true                     | first-party            | false                 | unconfirmed            |
        | --format json | true                     | first-party            | false                 | unconfirmed            |
        | --format text | allowed                  | first-party            | DENIED                | unconfirmed            |

    # THE EXEMPTION IS NOT A RECORD, so there is nothing for `untrust` to
    # take away — and the command says exactly that instead of reporting a
    # revocation it did not perform. Worth pinning, because "I untrusted it
    # and it still runs" is the reading a user will otherwise arrive at, and
    # the honest answer is that the decision was never record-based.
    #
    # The decline arm of the cascade (a recorded "no", checked AHEAD of this
    # exemption and hash-blind so it survives a rebuild) is deliberately not
    # exercised here: `companion untrust` FORGETS a decision, and no CLI leaf
    # records a refusal at all — the only door to one is answering "no" at the
    # interactive prompt. That gap is a finding about the surface, not
    # something to paper over with a scenario that asserts the wrong verb.
    Scenario Outline: Untrusting an exempt companion revokes nothing, and says so
      Given an initialized ctxloom project
      And ctxloom is installed beside a first-party companion "ltk"
      And I run "ctxloom companion show ltk"
      And the output contains "first-party"
      When Alice tries to take back a decision she never made:
        """
        ctxloom companion untrust ltk <flags>
        """
      Then the command succeeds
      And the output reports "forgot" as "<nothing was forgotten>"
      When I run "ctxloom companion show ltk <flags>"
      Then the command succeeds
      And the output reports "allowed" as "<still exempt>"
      And the output reports "reason" as "<still first-party>"
      When I run "ctxloom doctor"
      Then the companion "ltk" was executed

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | nothing was forgotten | still exempt | still first-party |
        |               | 0                       | true          | first-party          |
        | --format json | 0                       | true          | first-party          |
        | --format text | forgot 0 decisions      | allowed       | first-party          |
