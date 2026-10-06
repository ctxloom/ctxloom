@doc
Feature: companion — which binaries on your machine ctxloom may execute

  Covers: `ctxloom companion list`, `companion show`, `companion allow`,
  `companion forget`, and the bare `ctxloom companion` form.

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

  THE GATE IS YOUR ALLOW. A companion is executed when your own
  ~/.ctxloom/companion_allow.yaml holds a record for its resolved path AND the
  SHA-256 of its bytes. `ctxloom companion allow <path|name> --yes` writes that
  record; without --yes it shows what it would record and writes nothing.
  Nothing else admits a companion: not its name, not where it sits, not a
  prompt. Two refusals are distinguished and neither is silent — a path nobody
  allowed, and a path allowed for different bytes (a rebuild, an upgrade, a
  swap), which names the old and the new hash so you can tell which.

  The record is per user and per machine. No project file can carry one, so a
  repository you clone cannot arrive with its binaries pre-allowed.

  Deliberately NO MCP tools for any of this: handing the agent the ability to
  allow the binaries that run alongside it defeats the property the gate
  exists to provide.

  This is the comprehensive per-noun spec: what the noun DOES, leaf by leaf.

  Rule: A companion nobody allowed is never executed

    The assertion that matters is not the exit code and not the warning — it
    is WHICH BINARIES ACTUALLY RAN. The fake companion in these fixtures
    appends to a witness file every time it is invoked, so "was never
    executed" is read off the filesystem rather than inferred from a missing
    line of output. An admission gate is exactly the kind of change that
    passes every exit-code assertion while quietly doing nothing — or quietly
    doing everything.

    Scenario: An unallowed companion is skipped, said so, and runs once allowed
      Given an initialized ctxloom project
      And a discovered companion "ctxloom-companion-acme" is on PATH, not allowed
      When I run "ctxloom doctor"
      Then the output contains "is not allowed to run"
      And the output contains "ctxloom companion allow"
      And the companion "ctxloom-companion-acme" was never executed
      When I run "ctxloom companion allow ctxloom-companion-acme --yes"
      And I run "ctxloom doctor"
      Then the companion "ctxloom-companion-acme" was executed

    # A rebuild is not the same refusal as "never allowed": the path IS
    # allowed, for other bytes. Reporting it as not-allowed would hide that
    # the binary a human chose has changed under them.
    Scenario: A companion rebuilt after it was allowed is refused, naming both hashes
      Given an initialized ctxloom project
      And a discovered companion "ctxloom-companion-acme" is on PATH, not allowed
      And the companion "ctxloom-companion-acme" is allowed, then rebuilt
      When I run "ctxloom doctor"
      Then the output contains "hash changed"
      And the output names the allowed and the current hash of "ctxloom-companion-acme"
      And the companion "ctxloom-companion-acme" was never executed

  Rule: Allowing and forgetting preview by default, and apply only with --yes

    A bare `allow` or `forget` reports what it would record or remove and
    writes nothing. That makes the hash a human is agreeing to visible before
    they agree to it.

    Scenario: Allow without --yes shows the hash and records nothing
      Given an initialized ctxloom project
      And a discovered companion "ctxloom-companion-acme" is on PATH, not allowed
      When I run "ctxloom companion allow ctxloom-companion-acme"
      Then the command succeeds
      And the output contains "sha256:"
      And the output contains "Re-run with --yes"
      When I run "ctxloom doctor"
      Then the companion "ctxloom-companion-acme" was never executed
      When I run "ctxloom companion allow ctxloom-companion-acme --yes"
      Then the output contains "Allowed."

    Scenario: Re-allowing a rebuilt companion shows the hash change before recording it
      Given an initialized ctxloom project
      And a discovered companion "ctxloom-companion-acme" is on PATH, not allowed
      And the companion "ctxloom-companion-acme" is allowed, then rebuilt
      When I run "ctxloom companion allow ctxloom-companion-acme"
      Then the command succeeds
      And the output contains "hash changed:"
      And the output names the allowed and the current hash of "ctxloom-companion-acme"

    Scenario: Forget previews, then withdraws the allow with --yes
      Given an initialized ctxloom project
      And a discovered companion "ctxloom-companion-acme" is on PATH, not allowed
      And the companion "ctxloom-companion-acme" is allowed
      When I run "ctxloom companion forget ctxloom-companion-acme"
      Then the command succeeds
      And the output contains "Nothing was removed"
      When I run "ctxloom companion forget ctxloom-companion-acme --yes"
      And I run "ctxloom doctor"
      Then the output contains "is not allowed to run"
      And the companion "ctxloom-companion-acme" was never executed

  Rule: The verdict is inspectable one binary at a time

    `companion show` runs the EXACT SAME decision the real probes consult
    (companions.AdmitCompanions), so its answer can never disagree with what
    actually happens at session start. Merely LOOKING never executes anything:
    a reporting command that ran a foreign binary to describe it would be the
    exec this noun exists to gate.

    Scenario Outline: Show answers whether ctxloom would execute one binary, and why
      Given an initialized ctxloom project
      And a discovered companion "ctxloom-companion-acme" is on PATH, not allowed
      When Alice asks whether one binary would run:
        """
        ctxloom companion show ctxloom-companion-acme <flags>
        """
      Then the command succeeds
      And the output reports "allowed" as "<not allowed yet>"
      And the output reports "reason" as "<because not allowed>"
      When the companion "ctxloom-companion-acme" is allowed
      And I run "ctxloom companion show ctxloom-companion-acme <flags>"
      Then the command succeeds
      And the output reports "allowed" as "<now allowed>"
      And the output reports "reason" as "<because allowed>"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | not allowed yet | because not allowed | now allowed | because allowed |
        |               | false           | not-allowed         | true        | allowed         |
        | --format json | false           | not-allowed         | true        | allowed         |
        | --format text | not-allowed     | not-allowed         | allowed     | allowed         |

    # "not installed" and "found but refused" are different facts about the
    # machine, and collapsing them into one silence is the shape this whole
    # noun exists to avoid.
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
    each binary on disk and your allow records together.

    Scenario: Bare companion reports the verdict for what is on PATH
      Given an initialized ctxloom project
      And a discovered companion "ctxloom-companion-acme" is on PATH, not allowed
      When I run "ctxloom companion"
      Then the command succeeds
      And the output contains "ctxloom-companion-acme"
      And the output contains "not-allowed"
      And the output does not contain "Available Commands:"
      And the companion "ctxloom-companion-acme" was never executed
