@doc
Feature: The container command surface — can a run's engine actually land in a container here?

  ctxloom's runtime axis can put an agent's ENGINE process inside a container
  (agents with `runtime: container`, or the project `runtime:` default) instead
  of on the host, so a risky or untrusted task runs against a fresh HOME, a
  scoped filesystem, and read-only credential mounts rather than the operator's
  live environment. The `ctxloom container` surface is how that capability is
  inspected and provisioned WITHOUT running a task: `container check` diagnoses
  whether containerized agents could launch here at all, and `container build`
  provisions the per-backend agent image they would run in — a two-stage image
  (a shared BASE tool layer plus the engine's AGENT stage, so a rebuilt image
  never needs a new ctxloom release; internal/cli/container_cmd.go:40-78).
  Without this surface an operator has no way to answer "will `runtime:
  container` actually work on this host?" short of launching a run and watching
  it abort — the whole point of the diagnostic seam (container_cmd.go:256-274)
  is to answer that ahead of time, and to fail LOUD on a misconfigured request
  rather than silently degrading a requested sandbox onto the bare host.

  # WHAT THIS JOURNEY CAN AND CANNOT SEE (honesty, mirroring j9_isolation's own
  # runtime-axis note). tests/acceptance drives a `ctxloom` SUBPROCESS: every
  # assertion here observes what the real command prints or the exit class it
  # returns, against the REAL host environment — there is no injected Diagnose
  # (containerDiagnose at container_cmd.go:136 is an in-process test seam this
  # external harness never reaches). Two consequences follow, and they bound
  # every scenario below:
  #
  #   1. The real container LAUNCH/BUILD boundary is OUT of hermetic scope. A
  #      genuine `container build` needs a reachable docker/podman daemon
  #      (isolation.runtimeReachable, runtime.go:532; Docker/Podman.Available at
  #      runtime.go:290/348), and a genuine containerized run additionally needs
  #      a built agent image plus the shared-filesystem probe to pass
  #      (diagnose.go:90). None of that is hermetic — the daemon's presence and
  #      the fs-sharing verdict are properties of the CI machine, not of
  #      ctxloom. So the ONE scenario that actually builds an image and lands an
  #      engine in a container is tagged @container: default-excluded (like
  #      @live), and it self-skips when SelectRuntime("") returns Host{}
  #      (runtime.go:648-668) — no reachable runtime, nothing to prove, clean
  #      skip. A green hermetic run of THIS file therefore does NOT mean
  #      "containers proven"; it means the DIAGNOSTIC and VALIDATION surface
  #      around them is proven. The launch itself is deferred, never faked.
  #
  #   2. `container check`'s payload is only PARTLY environment-independent.
  #      Which runtime is named, whether the image is present, and the shared-fs
  #      verdict all depend on the host (diagnose.go:46-86), so this journey
  #      does NOT assert those values hermetically. What IS invariant on every
  #      host — and is asserted below — is that the command is diagnostic-only
  #      (always exit 0, container_cmd.go:272) and renders ALL of its capability
  #      axes: the in-container line, the runtime line, and the shared-fs line
  #      (renderContainerCheck, container_cmd.go:298-325). That skeleton is the
  #      honest hermetic claim: the report answers every axis, whatever the
  #      answers are here.
  #
  # DELIBERATELY NOT RESTATED (covered elsewhere; this journey narrates the
  # persona arc around them, it does not duplicate their assertions):
  #   - j9_isolation.feature: the fail-loud/degrade RUN gate — an explicitly
  #     requested container that cannot launch aborts with a ClassIsolation
  #     finding (exit 3) unless --degraded runs it on the host. That is the
  #     runtime axis's behavior at RUN time; this file is the command surface
  #     that lets an operator SEE the same fact before running.
  #   - agent.feature: the `--runtime container` round-trip through agent
  #     set/show (agent.feature:11-17), the `container check claude-code`
  #     "Container capability" header (agent.feature:42-46), `container
  #     scaffold`, and `tooling`. This file reads the DEEPER check payload and
  #     the build/backend-validation surface those scenarios do not touch.

  Background:
    Given an initialized ctxloom project

  # Trent (platform-security) has to decide whether to mandate `runtime:
  # container` across the fleet, so before anyone runs anything he asks the
  # capability surface directly. The claim is not a bare exit code: it is that
  # the report is genuinely diagnostic (it changes nothing, always succeeds)
  # AND that it answers every capability axis — in-container, runtime, shared
  # filesystem — whatever the answers happen to be on this host. Asserting the
  # AXIS LABELS (not their values) is the honest hermetic reading: the values
  # are host-dependent (see honesty note 2), the four-axis skeleton is not.
  Scenario: The capability check answers every axis and changes nothing
    When I run "ctxloom container check codex"
    Then the command succeeds
    And the output contains "Container capability"
    And the output contains "in a container:"
    And the output contains "runtime:"
    And the output contains "shared fs:"

  # A requested backend that does not exist is a fail-loud finding, not a
  # best-effort diagnosis of nothing (container_cmd.go:280-283). The payload
  # claim is the finding TEXT — it names the bad backend AND lists the real
  # registered ones — so an operator who fat-fingered a name is handed the
  # correct set rather than a silent empty report.
  Scenario: Checking an unknown backend fails loud and names the real backends
    When I run "ctxloom container check not-a-backend"
    Then the command fails
    And the output contains "unknown backend"
    And the output contains "claude-code"

  # The build surface validates its backend argument BEFORE it ever reaches for
  # a daemon (container_cmd.go:84-88, ahead of isolation.BuildAgentImage at
  # :125). So this fail-loud is hermetic even though a real build is not: an
  # unknown backend is rejected with the available set with no runtime touched
  # at all — the honest slice of the build surface a machine without docker can
  # still prove.
  Scenario: Building an unknown backend fails loud before any daemon is touched
    When I run "ctxloom container build not-a-backend"
    Then the command fails
    And the output contains "unknown backend"
    And the output contains "claude-code"

  # LOCKED — the real boundary, DEFERRED behind @container (default-excluded;
  # self-skips when no runtime is reachable, the inverse of @live's
  # credential gate — see honesty note 1). This is the only scenario that
  # actually provisions the two-stage agent image (container_cmd.go:40-78,
  # imagebuild.go:262) and lands an engine INSIDE a container, then confirms via
  # the check payload that the image is now present and the daemon shares this
  # filesystem (diagnose.go:70-94). Everything above proves the surface AROUND
  # this; only a host with a reachable docker/podman daemon can prove THIS, so
  # a hermetic run skips it rather than faking a container boundary.
  #
  # DEFERRAL (draft): @container is not yet in the default tag-exclusion set
  # (acceptance_test.go:63 excludes @live/@network/@future/@wip) — wiring this
  # journey must add ~@container there and give the scenario a reachability
  # gate step that no-ops the body toward a clean skip when SelectRuntime("")
  # is Host{}, exactly as steps_live.go gates @live on credential presence.
  @container
  Scenario: A built image lets an engine run in a container, and check confirms it
    Given a container runtime is reachable on this host
    When I run "ctxloom container build claude-code"
    Then the command succeeds
    And the output contains "Built"
    When Alice runs a claude-code agent with runtime "container" and prompt "sandbox-check"
    Then the run's engine executed inside a container, not on the host
    When I run "ctxloom container check claude-code"
    Then the command succeeds
    And the output contains "agent image:"
    And the output contains "present"
    And the output contains "shared fs:       ok"
