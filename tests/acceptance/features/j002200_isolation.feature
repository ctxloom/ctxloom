@doc
Feature: Bounding what the agent can reach, even with permissions bypassed

  Priya runs an agent with its permission prompts turned off. She is not being
  reckless — approving every file read one dialog at a time is unusable at the
  pace the work actually happens, and her employer's policy is not "prompt the
  human more", it is "the assistant must not be able to reach what it has no
  business reaching." Those two facts together are the whole problem: the
  moment the prompts are off, the engine's own guardrails are not what is
  protecting the rest of her disk. Something else has to be.

  Asking the engine nicely does not work. Vendor CLIs treat an environment
  variable as a suggestion — one of them writes to a global path regardless of
  what HOME says, and this suite asserts that leak as a known fact elsewhere.
  So a boundary an engine has to cooperate with is not a boundary. It has to
  be a property of where the process actually runs.

  ctxloom's answer is two independent axes: WORKSPACE (its own git worktree,
  or the live project directory) and RUNTIME (a host process, or a container).
  This journey is Priya establishing that they are real. That a run REQUESTING
  a worktree lands its engine somewhere genuinely distinct, and two isolated
  runs never share that somewhere — not that the setting round-trips through
  `agent show`, which would prove only that ctxloom can remember a preference.
  And that when she asks for a container and this machine cannot give her one,
  the run ABORTS rather than quietly proceeding unsandboxed. A sandbox that
  silently degrades is worse than none, because she would have stopped.
  Dropping to the host is available, but only when she says --degraded and
  therefore knows.

  # WHAT THIS JOURNEY CAN AND CANNOT SEE (honesty, mirroring j002100_delegation's
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
  # UPDATE (isolation-matrix task): (2)'s gap is now filled, below, WITHOUT
  # abandoning the mock's hermetic guarantee. A real registered backend name
  # (claude-code/codex/kiro/opencode) drives isolation.Prepare
  # exactly as a live run would — but PATH is rebuilt from scratch to a
  # scratch dir plus /usr/bin:/bin, so the literal binary a backend execs
  # ("claude"/"codex"/"kiro-cli"/"opencode") resolves ONLY to a
  # recording spy script this suite writes, NEVER to a real installed engine
  # — no live credential, no network call, ever, in any scenario in this
  # file. The spy dumps its OWN os.Environ() (exactly what a real engine
  # process receives — internal/shared/agent/base.go's BuildEnv) plus a `cat`
  # of whatever credential file its own env points it at, captured from
  # INSIDE the spawned process — the per-agent scratch config-home does not
  # survive past the run (Cleanup removes it unconditionally), so this is the
  # only vantage point from which the seeded byte content is observable at
  # all. See j002200_isolation.doc.md for the rendered matrix this proves and does
  # not prove, and steps_j002200_isolation_matrix.go's own package doc for why
  # opencode (a stateful ACP handshake, not a plain oneshot exec) gets the
  # fail-loud/warn CONTRACT below but not the exact spawned-env payload —
  # that half stays pinned at the Go level (auth_test.go).
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

  # ===========================================================================
  # PER-ENGINE CONFIG-HOME ISOLATION MATRIX — fills the gap the journey's own
  # top note used to flag as out of hermetic scope. Every scenario below
  # drives a REAL registered backend name through isolation.Prepare via a
  # PATH-sandboxed recording spy (see the top-of-file UPDATE note) — never a
  # real engine binary, never a live credential, never a network call. See
  # j002200_isolation.doc.md for the rendered outcome matrix (isolated / LEAKS /
  # not executed) these scenarios and their Go-level siblings together prove.
  # ===========================================================================

  # workspace "none" is the baseline every other row in the matrix is
  # measured against: it shares the live project dir AND the engine's shared
  # global config — by design, not a bug — so NONE of the config-home
  # machinery below (seeding, gating, curated HOME, findings) fires at all.
  # Asserting this explicitly is what lets a later "worktree" cell's finding
  # read as isolation actually engaging, rather than the isolation machinery
  # just always firing regardless of which axis was requested.
  #
  # The baseline is asserted on PAYLOAD, not on the absence of a warning: the
  # engine really runs (exit 0, the spy process leaves its recording), it runs
  # in the LIVE project dir, and not one of its config-home variables points
  # into a per-agent scratch tree. An earlier version checked only that two
  # phrases were missing from the output, which an audit showed is equally
  # satisfied by a run in which the engine never launched at all.
  #
  # This table lists TWO engines, not four, and the two absentees are the
  # honest part. codex and opencode each have their own scenario below,
  # because for them the sentence above is not true as written — and a table
  # row that quietly asserts less than its scenario's title claims is how the
  # audit's finding happened in the first place.
  Scenario Outline: workspace "none" never touches any engine's config-home isolation at all
    Given Alice has a git-backed project
    When Alice runs the isolated "<engine>" agent under workspace "none"
    Then the run touches no isolation mechanism at all

    Examples:
      | engine      |
      | claude-code |
      | kiro        |

  # codex is the documented EXCEPTION to the baseline above, and it belongs in
  # the feature file rather than hidden in a table row that asserts around it.
  # codex relocates CODEX_HOME to <WorkDir>/.codex on EVERY axis including
  # none — its own in-tree fallback, not ctxloom's isolation policy (see
  # internal/lm/isolation.SeedCodexHome's doc and internal/codex/backend.go's
  # resolveCodexProjectDir). That relocated home starts empty, so ctxloom
  # seeds the host's credentials into it, and with neither OPENAI_API_KEY nor
  # ~/.codex/auth.json there is nothing to seed. ctxloom then refuses rather
  # than launching a codex that would silently 401.
  #
  # So "workspace none touches no config-home machinery" is FALSE for codex,
  # and this scenario says so out loud. Note the failure is a plain backend
  # error (exit 1), NOT a ClassIsolation finding (exit 3) — ctxloom's own
  # isolation gates genuinely stayed out of the way, which is the half of the
  # baseline claim that does hold here.
  Scenario: workspace "none" still meets codex's own CODEX_HOME relocation, which needs credentials of its own
    Given Alice has a git-backed project
    And Alice has no "codex" credentials or API key on the host
    When Alice runs the isolated "codex" agent under workspace "none"
    Then the run fails without any isolation finding, naming "no OPENAI_API_KEY and no host ~/.codex/auth.json credentials"

  # opencode's baseline row, split out for a HARNESS reason rather than a
  # product one: opencode is driven over ACP, a stateful JSON-RPC handshake,
  # and this file's spy is a dumb recorder that dumps its env and exits — so
  # the handshake never completes and no exit code or spy recording is
  # available to assert on. What the row can still prove, and does, is that
  # every isolation gate was passed and the run reached an actual engine
  # spawn from the PATH-sandboxed spy directory.
  Scenario: workspace "none" leaves opencode's own launch untouched, right up to the engine spawn
    Given Alice has a git-backed project
    When Alice runs the isolated "opencode" agent under workspace "none"
    Then the run proceeds past every isolation gate to spawn the engine itself

  # LOCKED — the safety net grave-prize exists to guarantee: an isolated
  # worktree run for an engine that DOES relocate credentials with its
  # config-home var (claude/codex/opencode all HonoursVarForCreds=true —
  # auth.go) refuses to start rather than silently handing the engine an
  # empty, logged-out config-home. This is provable without any engine binary
  # at all: the finding fires, and the run aborts, BEFORE isolation.Prepare
  # ever tries to spawn one.
  Scenario Outline: A worktree run refuses to start an engine it cannot authenticate, rather than silently sharing the host's global credentials
    Given Alice has a git-backed project
    And Alice has no "<engine>" credentials or API key on the host
    When Alice runs the isolated "<engine>" agent under workspace "worktree"
    Then the run aborts with an isolation finding naming "<needle>"

    Examples:
      | engine      | needle                                                              |
      | claude-code | no ANTHROPIC_API_KEY and no host claude credentials                |
      | codex       | no OPENAI_API_KEY and no host codex credentials                    |
      | opencode    | no OPENROUTER_API_KEY and no host opencode credentials             |

  # The bypass half of the SAME gate: an API key riding the environment is
  # its own proof of intent to authenticate that way (auth.go's
  # resolveEnvOrMountAuth precedence), so seeding is skipped and the run is
  # never blocked on a missing host credential file.
  #
  # "PROCEED" is the load-bearing word in this scenario's title, so it is what
  # gets asserted: the run exits 0, the engine really launches, and the
  # config-home variable it is handed points at a per-agent scratch tree — the
  # isolation this axis was asked for is still in place, only the credential
  # gate stood down. Asserting merely that no finding was printed used to let
  # this pass under a mutation that stopped any engine from launching, and
  # under one that collapsed isolation to nothing at all; the degrade warning's
  # wording matched neither needle. opencode's row moved to its own scenario
  # below for the ACP reason described there.
  Scenario Outline: The same engines proceed without any isolation finding once their API key rides the environment
    Given Alice has a git-backed project
    And Alice has no "<engine>" credentials on the host
    And Alice has set the "<engine>" API key in the environment
    When Alice runs the isolated "<engine>" agent under workspace "worktree"
    Then the run reports no isolation finding
    And the spy "<engine>" process's "<var>" env var points to an isolated per-agent directory, not the host's own

    Examples:
      | engine      | var               |
      | claude-code | CLAUDE_CONFIG_DIR |
      | codex       | CODEX_HOME        |

  # opencode's row of the same claim. Its ACP launch cannot reach this file's
  # dumb spy (see the workspace-"none" opencode scenario above and the
  # file-level note in steps_j002200_isolation_matrix.go), so the payload it can
  # prove is that the credential gate stood down and the run went all the way
  # to spawning opencode from the sandboxed PATH. The spawned-env payload
  # proper — opencode's XDG_DATA_HOME/opencode nesting — is pinned at the Go
  # level by internal/lm/isolation/auth_test.go's
  # TestHostCredentialSeed_OpencodeSeedsAuthJsonUnderXdgDataOpencode.
  Scenario: opencode proceeds past the credential gate too once OPENROUTER_API_KEY rides the environment
    Given Alice has a git-backed project
    And Alice has no "opencode" credentials on the host
    And Alice has set the "opencode" API key in the environment
    When Alice runs the isolated "opencode" agent under workspace "worktree"
    Then the run proceeds past every isolation gate to spawn the engine itself

  # LOCKED — the ISOLATED case, positively proven, but only ONE HALF of the
  # claim its own name suggests. What this scenario actually proves: ctxloom's
  # OWN bookkeeping — the env var it sets, the byte-for-byte copy it seeds,
  # the host original it leaves untouched — is correct. The spy is
  # cooperative BY CONSTRUCTION (see the file-level UPDATE note above): it
  # dumps whatever env it was handed and cats whatever file that env points
  # at, so this scenario is INCAPABLE of going red if a real vendor engine
  # read CLAUDE_CONFIG_DIR/CODEX_HOME and then wrote somewhere else anyway —
  # that engine would never run here at all. A prior version of this comment
  # claimed otherwise ("this is the exact claim that would go RED the moment
  # a vendor engine... stopped honoring the var"); that claim was false and
  # has been corrected. The vendor half of the claim — does a REAL engine
  # binary actually honor the variable it was handed, credentials and all —
  # is proven live, against real engine binaries and real credentials, by
  # tests/acceptance/features/isolation_probe.feature (`just isolation-probe
  # <engine> worktree`). The two layers are complementary, not redundant:
  # this one is fast, hermetic, and catches a ctxloom-side regression in CI on
  # every commit; the probe is slow, costs a real paid call, and is the one
  # that would have caught kiro's actual credential-store leak (legal-hula) —
  # discovered by running kiro live, not by any spy.
  Scenario Outline: A worktree run copies the host credential into the isolated config-home verbatim, and never touches the host's own copy
    Given Alice has a git-backed project
    And Alice has a "<engine>" credential fixture on the host
    When Alice runs the isolated "<engine>" agent under workspace "worktree"
    Then the spy "<engine>" process's "<var>" env var points to an isolated per-agent directory, not the host's own
    And the isolated "<engine>" credential matches the host fixture byte-for-byte
    And the host "<engine>" credential file was never modified

    Examples:
      | engine      | var               |
      | claude-code | CLAUDE_CONFIG_DIR |
      | codex       | CODEX_HOME        |

  # LOCKED — kiro's PARTIAL isolation, and this IS the leak: subscription auth
  # lives in a GLOBAL sqlite under $XDG_DATA_HOME that KIRO_HOME does not
  # touch (auth.go's resolveKiroContainerAuth doc, verified live against
  # kiro-cli 2.12.1). Isolating XDG_DATA_HOME anyway with nothing to
  # authenticate a fresh store would silently strand the agent logged out —
  # so ctxloom refuses instead, the SAME ClassIsolation mechanism as the
  # claude/codex/opencode no-credential case above. The finding text itself
  # names the mechanism: the credential store would NOT relocate. This is the
  # positive leak assertion — it fails the moment kiro's credential store
  # becomes genuinely KIRO_HOME-scoped (a vendor fix ctxloom would then need
  # to widen HonoursVarForCreds to match), not merely an absent check.
  Scenario: A worktree run for kiro isolates only session state without an API key, and refuses to silently share the global credential store
    Given Alice has a git-backed project
    When Alice runs the isolated "kiro" agent under workspace "worktree"
    Then the run aborts with an isolation finding naming "isolating XDG_DATA_HOME would relocate kiro's credential store"

  # The other half of kiro's story: KIRO_API_KEY genuinely authenticates a
  # FRESH per-agent XDG_DATA_HOME headlessly (live-verified against kiro-cli
  # 2.12.1, auth.go's own doc) — so once it is present, isolation widens to
  # cover the credential store too, not just session state.
  Scenario: A worktree run for kiro isolates its credential store too once KIRO_API_KEY authenticates a fresh one
    Given Alice has a git-backed project
    And Alice has set the "kiro" API key in the environment
    When Alice runs the isolated "kiro" agent under workspace "worktree"
    Then the run reports no isolation finding
    And the spy "kiro" process's "KIRO_HOME" env var points to an isolated per-agent directory, not the host's own
    And the spy "kiro" process's "XDG_DATA_HOME" env var points to an isolated per-agent directory, not the host's own

  # ARGV/STDIN VISIBILITY (U161-F01) — the spy previously dumped only its own
  # environment; it never emitted "$@" and never read stdin, so every argv
  # defect (a flag whose value is dropped, a variadic flag swallowing the
  # prompt, a missing required flag, a mutually exclusive pair emitted
  # together) was structurally invisible to this suite, in the one place
  # that could have seen it — the actual argv/stdin a real engine binary
  # receives. This scenario puts claude-code's per-engine argv/stdin payload
  # under acceptance coverage for the first time.
  Scenario: The spy captures the real argv and stdin a live engine binary would receive
    Given Alice has a git-backed project
    And Alice has set the "claude-code" API key in the environment
    When Alice runs the isolated "claude-code" agent under workspace "worktree"
    Then the spy "claude-code" process's ARGV contains "--print"
    And the spy "claude-code" process's STDIN contains "hello"
