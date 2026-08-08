Feature: The container runtime axis — can an engine actually land in a container here, and did it?

  ctxloom's runtime axis puts an agent's ENGINE process inside a container
  (`runtime: container`) instead of on the host, so a risky or untrusted task
  runs against a fresh HOME, a scoped filesystem, and read-only credential
  mounts rather than the operator's live environment. Containers are the axis
  that makes isolation a PROPERTY of the runtime rather than a request the
  engine may ignore — some vendor CLIs write credentials to a global path no
  matter what environment variable you hand them — which is exactly why it is
  the axis that must be proven by observation rather than assumed.

  `ctxloom container check` is how an operator answers "will `runtime:
  container` work on this host?" without launching anything, and `container
  build` provisions the per-backend agent image a run would use. Without that
  diagnostic surface the only way to find out is to start a run and watch it
  abort.

  # WHAT THIS JOURNEY CAN AND CANNOT SEE. tests/acceptance drives a `ctxloom`
  # SUBPROCESS, so every assertion observes what the real command printed, the
  # exit class it returned, or a file it left behind — never an injected seam.
  # Two consequences bound the scenarios below.
  #
  #   1. `container check`'s payload is only PARTLY environment-independent.
  #      Which runtime is named, whether the image is present, and the
  #      shared-filesystem verdict are all properties of the machine, not of
  #      ctxloom, so this journey does not assert those VALUES. What is
  #      invariant on every host — and is asserted — is that the command is
  #      diagnostic-only (always exit 0, it changes nothing) and that it
  #      renders every capability axis it claims to answer, whatever the
  #      answers are here. That skeleton is the honest hermetic claim.
  #
  #   2. The real launch is NOT hermetic: it needs a reachable docker/podman
  #      daemon and an agent image. So the one scenario that actually lands an
  #      engine in a container is tagged @container, excluded from the default
  #      run, and has its own gate — `just test-acceptance-container`. A green
  #      default run of this file therefore does NOT mean "containers proven";
  #      it means the surface AROUND them is proven. The launch is deferred to
  #      a gate that really performs it, never faked here.
  #
  # NOT TAGGED @doc, deliberately: the first three scenarios drive the generic
  # `I run "ctxloom ..."` / `the output contains` steps, which the living-docs
  # generator cannot narrate (taskloom sore-stew). Tagging it would produce a
  # documentation page that says nothing rather than no page at all.
  #
  # DELIBERATELY NOT RESTATED (covered elsewhere): j002200_isolation.feature owns
  # the fail-loud/degrade contract for a requested container that CANNOT launch
  # — and self-skips where one can, so on a docker-having machine THIS file is
  # the only executable evidence for the runtime axis. agent.feature owns the
  # `--runtime container` round-trip through agent set/show and the `container
  # check` header line.

  Background:
    Given an initialized ctxloom project

  # Trent (platform-security) is deciding whether to mandate `runtime:
  # container` across the fleet, so before running anything he asks the
  # capability surface directly. The claim is not a bare exit code: it is that
  # the report is genuinely diagnostic — it changes nothing and always succeeds
  # — AND that it answers every axis it names, whatever the answers are here.
  Scenario: The capability check answers every axis and changes nothing
    When I run "ctxloom container check codex"
    Then the command succeeds
    And the output contains "Container capability"
    And the output contains "in a container:"
    And the output contains "runtime:"
    And the output contains "shared fs:"

  # A backend that does not exist is a fail-loud finding, not a best-effort
  # diagnosis of nothing. The payload claim is the finding TEXT: it names the
  # backend that was rejected AND lists the real registered ones, so an
  # operator who fat-fingered a name is handed the correct set rather than an
  # empty report they might read as "no capability here".
  Scenario: Checking an unknown backend fails loud and names the real backends
    When I run "ctxloom container check not-a-backend"
    Then the command fails
    And the output contains "unknown backend"
    And the output contains "not-a-backend"
    And the output contains "claude-code"

  # The build surface validates its backend argument BEFORE it reaches for a
  # daemon, which is what makes this row hermetic even though a real build is
  # not: the request is rejected with the available set and no runtime is
  # touched at all.
  Scenario: Building an unknown backend fails loud before any daemon is touched
    When I run "ctxloom container build not-a-backend"
    Then the command fails
    And the output contains "unknown backend"
    And the output contains "not-a-backend"
    And the output contains "claude-code"

  # THE REAL BOUNDARY. Everything above proves the surface around the launch;
  # this proves the launch. It is DIFFERENTIAL by construction — the same agent
  # runs twice, once per runtime — because an absolute reading cannot be
  # trusted: when the test harness itself runs inside a devcontainer, "am I in
  # a container" is true on BOTH sides, and a scenario asserting only that
  # would pass with no container ever launched. Running the host leg first
  # establishes what this machine looks like, so the container leg's claim is
  # "somewhere this host is not" rather than "somewhere containerish".
  #
  # The evidence is the engine's own record file, written from wherever the
  # engine actually ran and reaching the host through the workspace mount. Exit
  # code cannot serve: a containerized run and a run that silently never
  # containerized both exit 0 and both echo the prompt.
  @container
  Scenario: A containerized run puts the engine somewhere this host is not
    When Alice runs the mock agent with runtime "host"
    Then the engine left its record, and it ran on this host
    When Alice runs the mock agent with runtime "container"
    Then the engine left its record, and it ran inside a container, not on this host
