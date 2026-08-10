@doc
Feature: container — the images isolated agents run in, and the questions you can ask without one

  Covers: `ctxloom container check`, `container scaffold`, `container tooling
  list`, the bare `ctxloom container tooling` form, and the bare `ctxloom
  container` namespace itself.

  An agent with `runtime: container` does not run on your machine — it runs
  inside a per-backend IMAGE, and this noun owns that image. The image builds in
  two stages: a shared BASE (the distro plus the coding-agent tool layer) and
  the engine's AGENT stage (the client CLI plus the running ctxloom binary)
  layered on top. `build` produces it, `scaffold` makes the base stage yours to
  edit, `tooling list` collects what installed content says it needs in there,
  and `check` tells you whether any of it can work here at all.

  WHAT THIS FILE CANNOT ASSERT, AND SAYS SO RATHER THAN PRETENDING. `ctxloom
  container build` needs a real container runtime, network pulls of each
  engine's official installer, and minutes of wall clock. It is listed in
  completeness_test.go's excludedLeaves for exactly that reason, and no
  scenario here drives it; the containerized run path is exercised by
  j002400_container.feature, behind a tag the default suite skips.
  What IS specifiable without a runtime is everything around it: the read-only
  diagnosis, the file scaffold, the trust-gated collection, and the namespace's
  own dispatch. `container provenance` is hidden plumbing (the digest the
  ahead-of-time build recipes stamp) and is not part of the public surface.

  Rule: The namespace answers what it holds, and refuses what it does not

    `container` has no safe read-only view to default to — `check` probes a
    runtime, `build` writes an image — so the bare form prints help, which is
    still a real answer to "what does this noun do". A MISSPELLED verb is the
    case that matters: a namespace that printed its own help on a typo and
    exited 0 is this project's characteristic silent no-op wearing cobra's
    dispatch as a disguise.

    Scenario: The bare noun lists what the namespace holds
      Given an initialized ctxloom project
      When I run "ctxloom container"
      Then the command succeeds
      And the output contains "Available Commands:"
      And the output contains "scaffold"
      And the output contains "tooling"

    Scenario: A misspelled verb fails instead of printing help
      Given an initialized ctxloom project
      When Alice mistypes the verb she wanted:
        """
        ctxloom container buidl
        """
      Then the command fails
      And the output contains "unknown command"
      And the output contains "buidl"

    # Help is prose, and no encoding carries prose. A caller that asked for
    # json to parse and got help text back — with a 0 exit saying all was well
    # — is the same silent no-op one layer up.
    Scenario: A machine-readable format aimed at the namespace itself is refused
      Given an initialized ctxloom project
      When I run "ctxloom --format json container"
      Then the command fails
      And the output contains "help is not a payload"

  Rule: Check reports; it never builds, changes or blocks anything

    `container check` answers whether `runtime: container` agents can launch
    here: whether THIS process is already inside a container, which runtime is
    reachable, whether the backend's image is present, and whether the
    runtime's daemon shares this filesystem (the docker-outside-of-docker
    detector, where bind mounts silently resolve against the wrong filesystem
    and launches hang).

    No probe outcome ever fails the command — read the report, not the exit
    code. A USAGE error still is one.

    # DIAGNOSTIC-ONLY is a claim about what the command does NOT do, and no
    # assertion on its own stdout can see it writing a file somewhere else: the
    # static "Container capability" header prints either way. The read-only
    # half is asserted where it actually lives — the project tree, byte for
    # byte.
    #
    # The report's CONTENT is asserted by the lines that are always present
    # whatever this machine has installed. Whether a runtime is reachable is a
    # fact about the developer's laptop; that the report answers the question
    # at all is a fact about ctxloom.
    Scenario: The capability check is diagnostic-only
      Given an initialized ctxloom project
      And I record the project tree
      When Alice asks whether containerized agents could run here:
        """
        ctxloom container check claude-code
        """
      Then the command succeeds
      And the output contains "Container capability (backend: claude-code)"
      And the output contains "in a container:"
      And the output contains "shared fs:"
      And the project tree is unchanged

    # Named backend vs. resolved default are two different code paths: with no
    # argument the project's configured default is resolved, and a report that
    # named no engine at all would describe nothing while still looking like a
    # report.
    Scenario: With no backend named, the check reports on the project's default
      Given an initialized ctxloom project
      When I run "ctxloom container check"
      Then the command succeeds
      And the output contains "Container capability (backend:"
      And the output does not contain "(unresolved)"
      And the output contains "shared fs:"

    Scenario: An unknown backend is a usage error, and the real ones are named
      Given an initialized ctxloom project
      When Alice asks about an engine that does not exist:
        """
        ctxloom container check totally-bogus-engine
        """
      Then the command fails
      And the output contains "unknown backend"
      And the output contains "totally-bogus-engine"
      And the output contains "claude-code"

  Rule: Scaffold hands you the base stage, and never overwrites your edits

    `container scaffold` materializes the embedded default base Containerfile
    as an editable local file and wires `isolation_base_containerfile` so every
    locally-built image — the on-the-fly build included — layers on it. It is
    content-identical to what the default build already used, so nothing about
    the resulting image changes until you edit it.

    It is idempotent and WIP-SAFE, which is the claim worth pinning: a file
    already at the target is ADOPTED, never clobbered. `--force` is the
    explicit opt-in to overwriting.

    Scenario: Scaffolding writes the base Containerfile and wires it into config
      Given an initialized ctxloom project
      When Alice takes ownership of the base image stage:
        """
        ctxloom container scaffold
        """
      Then the command succeeds
      And the file ".ctxloom/base.Containerfile" contains "FROM"
      And the file ".ctxloom/config.yaml" contains "isolation_base_containerfile"

    # THE DESTROYER'S TWO SIDES, in one fixture. The adopting run must leave
    # the user's bytes exactly where they were — asserted by the marker
    # SURVIVING and by the embedded default's "FROM" being ABSENT, because a
    # clobber that happened to keep the file non-empty would satisfy a
    # file-exists check. Only then does --force get to prove it really does
    # replace the content it was asked to replace.
    Scenario: An existing base Containerfile is adopted, and only --force replaces it
      Given an initialized ctxloom project
      And the project already has the file ".ctxloom/base.Containerfile":
        """
        MY-OWN-BASE-EDITS: hand-written, must survive a scaffold.
        """
      When Alice scaffolds over work she already did:
        """
        ctxloom container scaffold
        """
      Then the command succeeds
      And the file ".ctxloom/base.Containerfile" contains "MY-OWN-BASE-EDITS"
      And the file ".ctxloom/base.Containerfile" does not contain "FROM"
      And the file ".ctxloom/config.yaml" contains "isolation_base_containerfile"
      When Alice deliberately discards them:
        """
        ctxloom container scaffold --force
        """
      Then the command succeeds
      And the file ".ctxloom/base.Containerfile" contains "FROM"
      And the file ".ctxloom/base.Containerfile" does not contain "MY-OWN-BASE-EDITS"

    # --path is a bare flag carrying untrusted user input into a filesystem
    # write. The positive control is the first scenario in this Rule, which
    # proves the same fixture DOES produce `.ctxloom/base.Containerfile` — so
    # its absence here means the write was refused, not that scaffolding never
    # writes anything. Nothing may be wired into config either: a refusal that
    # still recorded the path would point every later build at a file that was
    # never created.
    Scenario: A path escaping the project root is refused and nothing is written
      Given an initialized ctxloom project
      When Alice asks for the base file outside the project:
        """
        ctxloom container scaffold --path ../escaped.Containerfile
        """
      Then the command fails
      And the output contains "escapes the project root"
      And the file ".ctxloom/base.Containerfile" does not exist
      And the file ".ctxloom/config.yaml" does not contain "isolation_base_containerfile"

  Rule: Tooling collection is trust-gated, and never applies anything itself

    A bundle declares the tools its content needs inside the agent image as a
    well-known `tooling` command. `container tooling list` collects those
    declarations from TRUSTED bundles and emits them with instructions for the
    LLM: locate or scaffold the base Containerfile, propose the additions as a
    diff, get the user's explicit approval per change, then rebuild.

    Collection goes through the same trust gate as any other content —
    declarations from unreviewed bundles are withheld — and nothing is ever
    written here. The edit is the LLM's, gated by the user.

    # ABSENCE SATISFIED ABSENCE. With nothing declared anywhere, "none
    # reported" was equally consistent with the trust gate working and with
    # collection being broken outright — a render that dropped EVERY
    # declaration, trusted or not, left this green. So the fixture declares
    # tooling twice: once from a bundle whose declaration has been rejected
    # (must be withheld, and the summary line is then a fact about the GATE),
    # and once from a bundle that is trusted (must come through — the positive
    # control that makes "none reported" mean something).
    #
    # DECIDED 2026-08-08 (taskloom vivacious-overlook), NOT YET IMPLEMENTED:
    # `container tooling` will gain a section naming what was withheld BY
    # BUNDLE AND ITEM REF ("shady#commands/tooling") — never the
    # publisher-authored declaration body. A ref is a ctxloom-controlled
    # identifier; the body is attacker-controlled text, and rendering it to an
    # operator's terminal is a confirmed hazard (taskloom delicious-goatskin:
    # publisher content reaches the terminal with no sanitiser, measured).
    #
    # So the "does not contain TOOLING-DECL-SHADY" assertion below becomes MORE
    # load-bearing when that lands, not less: it is what pins that adding the
    # ref section did not start leaking the body.
    Scenario: An untrusted declaration is withheld, and a trusted one comes through
      Given an initialized ctxloom project
      And a bundle "shady" declaring container tooling "TOOLING-DECL-SHADY"
      And I run "ctxloom bundle reject shady#commands/tooling"
      When Alice collects what her installed content needs in the image:
        """
        ctxloom container tooling list
        """
      Then the command succeeds
      And the output contains "No trusted bundles declare container tooling"
      And the output contains "Untrusted declarations are withheld"
      And the output does not contain "TOOLING-DECL-SHADY"
      Given a bundle "tooled" declaring container tooling "TOOLING-DECL-TOOLED"
      When Alice collects again now that a trusted bundle declares tooling:
        """
        ctxloom container tooling list
        """
      Then the command succeeds
      And the output contains "TOOLING-DECL-TOOLED"
      And the output does not contain "TOOLING-DECL-SHADY"
      And the output does not contain "No trusted bundles declare container tooling"

    # `tooling` is a sub-noun carrying the spine verb `list` rather than a bare
    # leaf, so it composes with the rest of the CLI's noun-verb shape — and its
    # bare form still lists, through the same seam `ctxloom remote` uses, so
    # nothing a caller already types stops working.
    Scenario: Bare container tooling lists the declarations
      Given an initialized ctxloom project
      And a bundle "tooled" declaring container tooling "TOOLING-DECL-TOOLED"
      When I run "ctxloom container tooling"
      Then the command succeeds
      And the output contains "TOOLING-DECL-TOOLED"
      And the output does not contain "Available Commands:"

    # The instructions and the declarations are two different fields, and a
    # caller parsing this needs both: emitting only the preamble would read as
    # a working response and carry no content to apply.
    Scenario: The machine-readable form carries the instructions and the declarations
      Given an initialized ctxloom project
      And a bundle "tooled" declaring container tooling "TOOLING-DECL-TOOLED"
      When I run "ctxloom --format json container tooling list"
      Then the command succeeds
      And the output is valid JSON
      And the output contains "instructions"
      And the output contains "TOOLING-DECL-TOOLED"
