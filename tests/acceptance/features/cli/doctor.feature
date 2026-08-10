@doc
Feature: doctor — the deterministic diagnosis, and why its exit code is not the verdict

  Covers: `ctxloom doctor`, its `--deps` scoping, and its machine-readable
  form.

  `doctor` is the one command whose whole output is the answer. It runs
  ctxloom's deterministic setup checks — the .ctxloom marker and config
  validity, required binaries on PATH, whether every configured agent resolves,
  hooks and MCP registration per backend, the trust store, companion detection,
  local-only state a fresh clone cannot know it lacks — and prints one line per
  check, each prefixed with a DOCTOR-CHECK-* marker. That marker vocabulary is
  shared with the "ctxloom-doctor" Agent Skill, so a human and an LLM reading
  either surface see one language.

  READ THE REPORT, DON'T GREP THE EXIT CODE. This is the design decision the
  whole file exists to pin. No check outcome ever fails the command: a `warn`
  IS doctor's fail-loud signal, and it arrives with exit 0. A caller that
  branches on `$?` will call a project healthy while the report in front of it
  says otherwise — so every scenario here asserts the REPORT, and the two that
  drive a genuinely broken project assert `the command succeeds` deliberately,
  as the specified behaviour rather than an oversight. A USAGE error (a
  --format this build cannot render) is still an error.

  EVERY CHECK NEEDS BOTH HALVES. A check that fired on every project would be
  indistinguishable from one that works and would teach a reader to skip the
  line. So the two checks specified in detail below — the .gitignore posture
  and the MCP invocation — each get the state that makes them fire AND the
  state that keeps them quiet.

  Rule: The report names every check, in one shared marker vocabulary

    Scenario: Doctor reports every check with the shared DOCTOR-CHECK-* marker vocabulary
      Given an initialized ctxloom project
      When Alice asks ctxloom to check its own setup:
        """
        ctxloom doctor
        """
      Then the command succeeds
      And the output contains "DOCTOR-CHECK-DEPS-a1"
      And the output contains "DOCTOR-CHECK-AGENTS-b2"
      And the output contains "DOCTOR-CHECK-VERSION-c3"
      And the output contains "DOCTOR-CHECK-HOOKS-TRUST-d4"

    # Every marker this could substring-match is in the TEXT rendering too (the
    # scenario above asserts the same one there), so nothing here said the
    # output was json: a doctor that rendered human text under --format json
    # passed. Parse it, and assert the decoded structure the text form cannot
    # produce.
    Scenario: Doctor's machine-readable form carries the same structured checks
      Given an initialized ctxloom project
      When Alice asks for the diagnosis in a form a script can parse:
        """
        ctxloom --format json doctor
        """
      Then the command succeeds
      And the output is valid JSON
      And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-DEPS-a1"
      And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-AGENTS-b2"
      And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-VERSION-c3"
      And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-HOOKS-TRUST-d4"
      And every object in the JSON output array "checks" has a non-empty "status"
      And every object in the JSON output array "checks" has a non-empty "detail"

    # A diagnosis is what you run when things are broken, so a broken config is
    # the state it must survive rather than the state it may refuse. It reports
    # and keeps going.
    Scenario: A config that will not parse does not stop the diagnosis
      Given an initialized ctxloom project
      And a malformed ctxloom config
      When Alice runs the diagnosis on a project whose config is broken:
        """
        ctxloom doctor
        """
      Then the command succeeds
      And the output contains "DOCTOR-CHECK-SETUP-MARKER-e5"
      And the output contains "DOCTOR-CHECK-DEPS-a1"

  Rule: --deps narrows to machine capability, for a project not yet set up

    `--deps` scopes the report to the probes that are true-or-false regardless
    of whether anything has been configured: binaries on PATH, signing-key
    readiness, git identity, the ACP adapter. init's PRIME and the setup
    skill's phase 1 run in this mode, because a full report on a brand-new
    project is a wall of expected-missing state that would alarm a user at the
    very start of the setup about to configure it.

    # THE NARROWING IS THE CLAIM, so it is asserted by what must be ABSENT —
    # and the positive control runs first, in the same fixture, so the missing
    # agents line means "scoped out" rather than "this project has no agents
    # check at all".
    Scenario: The scoped report keeps the capability probes and drops the setup checks
      Given an initialized ctxloom project
      When I run "ctxloom doctor"
      Then the command succeeds
      And the output contains "DOCTOR-CHECK-AGENTS-b2"
      And the output contains "DOCTOR-CHECK-HOOKS-TRUST-d4"
      When Alice asks only whether this machine has what ctxloom needs:
        """
        ctxloom doctor --deps
        """
      Then the command succeeds
      And the output contains "DOCTOR-CHECK-DEPS-a1"
      And the output does not contain "DOCTOR-CHECK-AGENTS-b2"
      And the output does not contain "DOCTOR-CHECK-HOOKS-TRUST-d4"

  Rule: A finding is reported at full volume and still exits 0

    A blanket `.ctxloom` ignore rule predates version-controlled content moving
    INTO `.ctxloom/`. Left in place it silently un-tracks the project's own
    content: `git add` reports nothing, and a content repository publishes an
    empty tree while every consumer's bundle refs fail to resolve. Nothing else
    in the system can see it, which is exactly the kind of thing doctor is for.

    # THE EXIT-CODE CLAIM, made concrete. This project is genuinely broken in a
    # way that will cost someone an afternoon, and the command still succeeds.
    # That is specified, not tolerated — asserting a failure here would be
    # asserting the opposite of the design. The report is where the alarm
    # lives, so the report is what is asserted: the marker, the status decoded
    # from the structured form, and the command that fixes it.
    Scenario: A blanket ignore rule is reported as a warning, with exit 0
      Given an initialized ctxloom project
      And the project already has the file ".gitignore":
        """
        .ctxloom/
        """
      When Alice checks a project whose ignore rule hides its own content:
        """
        ctxloom doctor
        """
      Then the command succeeds
      And the output contains "DOCTOR-CHECK-GITIGNORE-f6"
      And the output contains "blanket"
      And the output contains "ctxloom manage gitignore install"
      When I run "ctxloom --format json doctor"
      Then the command succeeds
      And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-GITIGNORE-f6" and whose "status" is "warn"

    # THE PAIRED NEGATIVE, and the one that keeps the check honest. The
    # granular `.ctxloom/<subdir>/` patterns are the CURRENT spelling and must
    # never be mistaken for the superseded blanket one — a detector that warned
    # on both would fire on every correctly-configured project.
    Scenario: The current granular ignore patterns keep the check quiet
      Given an initialized ctxloom project
      And the project already has the file ".gitignore":
        """
        .ctxloom/cache/
        .ctxloom/sessions/
        """
      When I run "ctxloom --format json doctor"
      Then the command succeeds
      And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-GITIGNORE-f6" and whose "status" is "ok"

  Rule: The one broken state nothing else can see

    A settings file naming a ctxloom invocation that does not speak MCP is
    invisible everywhere else: the entry is PRESENT, so every wiring check
    reports it healthy, and the engine starts fine. What fails is silent — the
    client waits on a handshake that never arrives, the session comes up with
    none of ctxloom's tools, and nothing says why. Reading the argv is the only
    way to tell a working entry from that one.

    Scenario: Doctor names a settings file whose ctxloom entry cannot speak the protocol
      Given an initialized ctxloom project
      And the project already has the file ".mcp.json":
        """
        {
          "mcpServers": {
            "ctxloom": {"command": "/usr/local/bin/ctxloom", "args": ["mcp"]}
          }
        }
        """
      When Alice checks a project whose MCP entry will never handshake:
        """
        ctxloom doctor
        """
      Then the command succeeds
      And the output contains "DOCTOR-CHECK-MCP-INVOCATION-g7"
      And the output contains ".mcp.json"
      And the output contains "ctxloom init"

    # The paired negative: a report that warned on every project would be
    # indistinguishable from one that works, and would teach a user to skip the
    # line.
    Scenario: Doctor stays quiet about a settings file that names the protocol server
      Given an initialized ctxloom project
      And the project already has the file ".mcp.json":
        """
        {
          "mcpServers": {
            "ctxloom": {"command": "/usr/local/bin/ctxloom", "args": ["mcp", "serve"]}
          }
        }
        """
      When I run "ctxloom --format json doctor"
      Then the command succeeds
      And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-MCP-INVOCATION-g7" and whose "status" is "ok"

  Rule: The report ends on a stated limit rather than going quiet

    Every check above can be green — the context assembled, the surface
    written, hooks and MCP registered — and ctxloom still has no way to know
    whether the vendor engine actually READ what landed on disk. That read
    happens inside a process ctxloom does not own.

    # It reports "info", never "ok": "ok" is a verdict, and there is nothing
    # left on ctxloom's side of this boundary to reach a verdict about. A
    # report that simply went quiet here could be mistaken for "checked and
    # confirmed read", which is the misreading this line exists to prevent —
    # so the STATUS is asserted, not just the marker's presence.
    Scenario: The delivered-versus-ingested boundary is stated, not claimed as checked
      Given an initialized ctxloom project
      When I run "ctxloom --format json doctor"
      Then the command succeeds
      And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-INGESTION-q7" and whose "status" is "info"
      When I run "ctxloom doctor"
      Then the command succeeds
      And the output contains "a process ctxloom does not own"
