@doc
Feature: Isolation axes — where an agent's workspace and runtime actually land

  ctxloom's isolation seam has two independent axes: WORKSPACE (does the agent
  get its own git worktree, or share the live project dir?) and RUNTIME (does
  its engine process run on the host, or in a container?). This journey proves
  the workspace axis is not just a config knob that round-trips through
  `agent set`/`agent show` — a run REQUESTING a worktree actually lands its
  engine somewhere distinct from a run that doesn't, and two isolated runs
  never share that somewhere. It also proves the runtime axis's fail-loud
  contract: an explicitly-requested container that cannot launch here aborts
  the run rather than silently dropping the sandbox, unless the operator
  says --degraded.

  # WHAT THIS JOURNEY CAN AND CANNOT SEE (honesty, mirroring j6_delegation's
  # own note). The built-in mock backend is the only engine tests/acceptance
  # can drive hermetically, and it is a bare echo — it never spawns a real
  # engine binary. Two structural facts about the isolation seam follow from
  # that, discovered by running it, not merely read from source:
  #
  #   1. isolation.Prepare's Worktree policy never os.Chdir's the PLUGIN
  #      subprocess itself (`ctxloom llm serve mock` — see
  #      internal/lm/isolation/{none,worktree}.go's SpawnClient, both of
  #      which spawn via exec.Command with no Cmd.Dir). A REAL engine honors
  #      the workspace by having ITS OWN Execute spawn a grandchild process
  #      with Cmd.Dir = the resolved WorkDir; the mock never spawns a
  #      grandchild, so it cannot observe isolation via os.Getwd() — that
  #      value is identical across every workspace axis, confirmed live. The
  #      mock now also records req.WorkDir (internal/lm/backends/mock.go),
  #      the value isolation.Prepare actually resolved and threaded through
  #      RunOptions.WorkDir — THAT is the honest signal this journey reads.
  #   2. Per-engine config-home isolation (CLAUDE_CONFIG_DIR / CODEX_HOME /
  #      KIRO_HOME — internal/lm/isolation/auth.go's credentialSeedSpecs) is
  #      keyed by the REGISTERED backend name (claude-code/codex/kiro only);
  #      the built-in "mock" backend has no entry, so Worktree's Env()
  #      contributes nothing for it — hermetically true for every workspace
  #      axis. This journey therefore proves the WORKSPACE boundary itself
  #      (distinct worktree checkouts, no escape into the project tree), not
  #      per-engine config-home variable isolation — that needs a real
  #      registered-engine fixture, out of hermetic scope here.
  #
  # Credential SEEDING into an isolated config-home (grave-prize) is a
  # further, separate claim this journey does not make either way — see (2).
  #
  # The RUNTIME axis's real container LAUNCH boundary needs a live container
  # daemon plus a built agent image; that is out of hermetic scope and
  # deliberately deferred (no @container scenario exists in this file). What
  # IS hermetically provable — and is proven below — is the fail-loud
  # contract when a requested container can't launch: exit code 3 with a
  # ClassIsolation finding naming the escape hatch, or exit 0 on the host
  # under --degraded.

  Background:
    Given Alice has a git-backed project with a mock agent

  # The workspace axis is not merely a flag that parses — it changes WHERE
  # the run's engine actually executes. "none" (the default) shares the live
  # project dir; "worktree" gives the run a fresh checkout the ephemeral
  # session scratch owns, never under the project tree.
  Scenario Outline: The same run lands in the workspace its axis dictates
    When Alice runs the mock agent under workspace "<workspace>" with prompt "isolation-check"
    Then the run's workdir reflects the "<workspace>" workspace axis

    Examples:
      | workspace |
      | none      |
      | worktree  |

  # LOCKED — the core boundary claim: two isolated runs never share the
  # workspace they get. Genuinely different prompts (task-one / task-two) so
  # a one-value assertion can't accidentally pass twice.
  Scenario: Two isolated runs share no workspace between them
    When Alice runs the mock agent under workspace "worktree" with prompt "task-one"
    And Alice runs the mock agent under workspace "worktree" with prompt "task-two"
    Then the two runs' workdirs are distinct and neither is under the project directory

  # Non-escape: a worktree run's per-agent config edits are hidden from the
  # shared project tree via .git/info/exclude (worktree.go's
  # excludeConfigFromMerge), not left as new tracked/untracked files a
  # developer could accidentally merge back.
  Scenario: A worktree run leaves the project tree clean
    When Alice runs the mock agent under workspace "worktree" with prompt "cleanup-check"
    Then none of the per-agent worktree config artifacts appear in the project's git status
    And the shared git exclude file carries the ctxloom per-agent worktree config block

  # LOCKED — the runtime axis's fail-loud/degrade contract (isolation.go's
  # warnUnknownAxes / chainFor / prepareChain): an EXPLICITLY-requested
  # container that cannot actually launch here (no reachable daemon, absent
  # image, unresolvable auth — any reason within "container isolation was
  # requested but could not start") is a fatal ClassIsolation finding that
  # aborts the run (exit 3) rather than silently landing unsandboxed on the
  # host; --degraded downgrades that to a warned, working host run.
  Scenario Outline: Requesting a container with no runtime fails loud, or degrades under --degraded
    When Alice runs the container-bound agent with flags "<flags>"
    Then the run <outcome>

    Examples:
      | flags      | outcome                           |
      |            | aborts with an isolation finding  |
      | --degraded | runs on the host                  |
