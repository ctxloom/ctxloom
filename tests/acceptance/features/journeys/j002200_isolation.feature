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
  #      internal/adapters/isolation/{none,worktree}.go's SpawnClient, both of
  #      which spawn via exec.Command with no Cmd.Dir). A REAL engine honors
  #      the workspace by having ITS OWN Execute spawn a grandchild process
  #      with Cmd.Dir = the resolved WorkDir; the mock never spawns a
  #      grandchild, so it cannot observe isolation via os.Getwd() — that
  #      value is identical across every workspace axis, confirmed live. The
  #      mock now also records req.WorkDir (internal/lm/backends/mock.go),
  #      the value isolation.Prepare actually resolved and threaded through
  #      RunOptions.WorkDir — THAT is the honest signal this journey reads.
  #   2. Per-engine config-home isolation (CLAUDE_CONFIG_DIR) is decided off
  #      the agent binding's engine_home, for every cell alike
  #      (operations.ResolveInTreeAgentHome), and the built-in "mock" backend
  #      declares no relocatable home at all — hermetically true for every
  #      workspace axis. This journey therefore proves the WORKSPACE boundary
  #      itself (distinct worktree checkouts, no escape into the project
  #      tree), not per-engine config-home variable isolation — that needs a
  #      real registered-engine fixture, out of hermetic scope here.
  #
  # Credential SEEDING into an isolated config-home (grave-prize) is a
  # further, separate claim this journey does not make either way — see (2).
  #
  # UPDATE (isolation-matrix task): (2)'s gap is now filled, below, WITHOUT
  # abandoning the mock's hermetic guarantee. A real registered backend name
  # drives isolation.Prepare
  # exactly as a live run would — but PATH is rebuilt from scratch to a
  # scratch dir plus /usr/bin:/bin, so the literal binary a backend execs
  # resolves ONLY to a
  # recording spy script this suite writes, NEVER to a real installed engine
  # — no live credential, no network call, ever, in any scenario in this
  # file. The spy dumps its OWN os.Environ() (exactly what a real engine
  # process receives — internal/core/agent/base.go's BuildEnv) plus a `cat`
  # of whatever credential file its own env points it at, captured from
  # INSIDE the spawned process — the per-agent scratch config-home does not
  # survive past the run (Cleanup removes it unconditionally), so this is the
  # only vantage point from which the seeded byte content is observable at
  # all. See j002200_isolation.doc.md for the rendered matrix this proves and does
  # not prove, and steps_j002200_isolation_matrix.go's own package doc for why
  # an engine driven over a stateful handshake rather than a plain oneshot
  # exec gets the fail-loud/warn CONTRACT below but not the exact spawned-env
  # payload — that half stays pinned at the Go level (auth_test.go).
  #
  # The RUNTIME axis's real container LAUNCH boundary needs a live container
  # daemon plus a built agent image; that is out of hermetic scope. What IS
  # hermetically provable — and is proven below — is the fail-loud contract when
  # a requested container can't launch: exit code 3 with a ClassIsolation
  # finding naming the escape hatch, or exit 0 on the host under --degraded.
  #
  # ONE real-launch scenario DOES live here now, tagged @container (excluded
  # from the default run, gated by `just test-acceptance-container`, self-skips
  # where no runtime is reachable): "A containerized engine's write reaches the
  # host through the same read-write bind mount…", below. It demonstrates the
  # SHARED-IDENTITY property of ctxloom's read-write host bind mount — the exact
  # mechanism the container claude credential now relies on (unripe-juiciness:
  # the credential is the REAL ~/.claude/.credentials.json mounted rw, not a
  # copy). See that scenario's own note for what the @container lane can and
  # cannot express about the credential specifically.

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

  # LOCKED — the runtime axis's REFUSAL contract: an EXPLICITLY-requested
  # container that cannot launch here is a NON-DEGRADABLE ClassIsolation
  # finding that aborts the run (exit 3) rather than landing unsandboxed on the
  # host. --degraded does NOT downgrade it, and this row is what proves that.
  #
  # INVERTED 2026-09-15 by the degradation audit (obstinate-judiciary). The
  # --degraded row used to expect a warned, working HOST run, and the human
  # ruled that break deliberately: "it BREAKS currently-documented behaviour —
  # an explicitly requested container falling back to the host under
  # --degraded. That break is the point, not a side effect." One flag cannot
  # mean both "I accept a thinner context" and "I accept no sandbox".
  #
  # The two rows now differ only in the FLAG, not the outcome, and that is the
  # assertion: the flag must make no difference here. Deliberately kept as two
  # rows rather than collapsed to one — a single row could not tell "--degraded
  # is ignored" from "--degraded was never passed".
  #
  # WHAT THIS SCENARIO ACTUALLY REACHES: isolation.chainFor, via the
  # NO-REACHABLE-DAEMON reason only. It does NOT reach isolation.prepareChain's
  # downgrade branch, and an earlier version of this comment claimed it did.
  # With no runtime, chainFor never puts a container tier in the chain, so
  # prepareChain's `IsContainerPolicyName(p.Name()) && !IsContainerPolicyName(next)`
  # never evaluates — measured, by mutation: BOTH operands of that condition can
  # be replaced with `true` and this suite stays green, as can `continue` ->
  # `break` on the line below it.
  #
  # So the other reasons named in prepareChain's own comment — absent image,
  # shared-fs probe, unresolvable auth, i.e. a REACHABLE runtime whose
  # PrepareWorkspace fails — are UNCOVERED here. Covering them needs a scenario
  # that reaches a reachable-runtime-but-failing-prepare state; see
  # uninvited-maternity. Do not read this scenario as proof of that path.
  Scenario Outline: Requesting a container with no runtime REFUSES, and --degraded does not rescue it
    When Alice runs the container-bound agent with flags "<flags>"
    Then the run aborts with an isolation finding

    Examples:
      | flags      |
      |            |
      | --degraded |

  # LOCKED — isolation.prepareChain's container-to-host DOWNGRADE: the sibling
  # gate the row above deliberately EXCLUDES. There a container is never
  # selected (no runtime); here one IS selected and then fails to START.
  #
  # Reaching it needs both halves at once, which is why it did not exist before:
  # the runtime must report itself reachable (so chainFor puts a container
  # policy in the chain) AND the image must be unproducible. The DEFAULT image
  # name is content-addressed, so it is absent but BUILDABLE and ctxloom builds
  # it — measured. An isolation_images override is "run AS-IS and never built",
  # so it is absent AND unbuildable, and isolation.Container.ensureImage errors.
  #
  # What this pins is the security half of the contract: prepareChain's
  # `IsContainerPolicyName(p.Name()) && !IsContainerPolicyName(next)` guards the
  # finding that says the session is NOT sandboxed. Both operands of that
  # condition could be replaced with `true` with the suite staying green.
  # INVERTED 2026-09-15 with its sibling above, and for the same ruling. The
  # --degraded row expected a host run; a selected-then-unstartable container
  # is now refused in both modes.
  #
  # It keeps its OWN outcome step ("aborts at the container START gate"), which
  # asserts prepareChain's finding and asserts the runtime-gate finding is
  # ABSENT. Both scenarios now exit 3, so that per-gate wording is the only
  # thing still telling these two failure modes apart — collapsing them onto
  # one shared step would silently let either gate satisfy both rows, which is
  # a confusion this feature has already paid for once.
  Scenario Outline: A container that was selected and cannot start REFUSES, and --degraded does not rescue it
    When Alice runs a container-bound agent whose image cannot be produced, with flags "<flags>"
    Then the run aborts at the container START gate

    Examples:
      | flags      |
      |            |
      | --degraded |

  # THE BOUNDARY THAT WAS ACCEPTED AND THEN LOST, which is a different fault
  # from every gate above and the only one that is NOT degradable.
  #
  # The rows above cover a container that could never be BUILT or SELECTED.
  # They too now refuse in both modes (the degradation audit removed the host
  # fallback), so this row is no longer distinguished by BEING fatal under
  # --degraded — it is distinguished by WHERE the boundary is lost: there the
  # container never existed, here it was accepted and then died. Here the
  # runtime is reachable, the ownership
  # matches, the image is present, and the daemon ACCEPTS the run — then the
  # container never reaches running. The session was already told it had a
  # boundary. Falling back now would run on the host something that believes
  # it is sandboxed, so the launch itself is the harm and --degraded must NOT
  # rescue it: both rows below expect exit 3.
  #
  # This is also the only row that exercises the transport-start gate. The
  # finding is raised in startContainerInteractive and gated in startTransport,
  # and DELETING THAT GATE leaves every unit test green — the finding is simply
  # recorded and checked by nothing, so the run warns and exits 1. That is the
  # characteristic defect of this codebase (a green-looking no-op) reproduced
  # inside the fix meant to prevent it, and this row is what kills it.
  #
  # The engine-never-ran assertion is not decoration: an abort that still
  # launched the engine would satisfy an exit-code check alone while doing the
  # exact thing the finding claims to have prevented.
  Scenario Outline: A container accepted by the daemon that never runs is fatal in BOTH modes
    When Alice runs a container-bound agent whose container dies at the daemon, with flags "<flags>"
    Then the run aborts because the container never reached running state

    Examples:
      | flags      |
      |            |
      | --degraded |

  # ===========================================================================
  # THE CONTAINER CREDENTIAL AXIS — real-home mount, not a copy (unripe-juiciness).
  #
  # WHY THIS MATTERS. claude's OAuth refresh token is SINGLE-USE and ROTATING:
  # whenever any holder refreshes, the provider mints a replacement and
  # invalidates the spent one. A COPY of ~/.claude/.credentials.json that ever
  # refreshes therefore rotates the live token out from under every other holder
  # — INCLUDING the host login. So ctxloom mounts the container's claude
  # credential as the REAL host file, read-write, with no copy: the container's
  # refresh lands in the one file the host also holds, and nothing desyncs. (The
  # two HOST axes — worktree, in-tree instance — do the opposite, copying an
  # access-token-ONLY credential; see the matrix scenarios below and
  # docs/architecture/engines/isolation.md.)
  #
  # WHAT THE @container LANE CAN AND CANNOT EXPRESS. The @container lane launches
  # the built-in MOCK in a real container; the mock authenticates against NO
  # vendor, so it carries no claude credential mount to observe, and there is no
  # CLI surface that prints the resolved container mount plan. So this scenario
  # does NOT read the credential file's bytes. What it DOES prove — live, against
  # a real daemon — is the SHARED-IDENTITY property the credential mount depends
  # on: a write made from INSIDE the container reaches the HOST at the same path,
  # because ctxloom's read-write bind mount is one file on both sides, not a
  # copy. The credential-SPECIFIC facts are pinned where they are observable: the
  # mount SOURCE = real ~/.claude/.credentials.json, rw, refresh token PRESENT is
  # a hermetic Go test (internal/adapters/isolation/auth_test.go's
  # TestClaudeCredentialMounts_PresentAndAbsent), and a real claude refreshing in
  # place is the @live isolation probe (@claude-code @container). This scenario
  # is the runtime-lane half; those two are the credential half.
  # ===========================================================================
  @container @reach-back @R5
  Scenario: A containerized engine's write reaches the host through the same read-write bind mount claude's real-credential mount uses
    When Alice runs the container-bound agent in a real container
    Then the engine's in-container write is the same file the host holds

  # ===========================================================================
  # PER-ENGINE CONFIG-HOME ISOLATION MATRIX — fills the gap the journey's own
  # top note used to flag as out of hermetic scope. Every scenario below
  # drives a REAL registered backend name through isolation.Prepare via a
  # PATH-sandboxed recording spy (see the top-of-file UPDATE note) — never a
  # real engine binary, never a live credential, never a network call. See
  # j002200_isolation.doc.md for the rendered outcome matrix (isolated / LEAKS /
  # not executed) these scenarios and their Go-level siblings together prove.
  # ===========================================================================

  # workspace "none" for ALICE'S OWN SESSION is the baseline every other row in
  # the matrix is measured against: it shares the live project dir AND the
  # engine's shared global config — by design, not a bug — so NONE of the
  # config-home machinery below (seeding, gating, curated HOME, findings) fires
  # at all. Asserting this explicitly is what lets a later "worktree" cell's
  # finding read as isolation actually engaging, rather than the isolation
  # machinery just always firing regardless of which axis was requested.
  #
  # Alice types `ctxloom run` and names no agent; the project's default agent
  # is bound for her, but the session is hers, and — in this fixture — the
  # bound agent's engine_home is UNDECLARED, which resolves to the real host
  # home by default (engine_home is strictly opt-in: see the sibling
  # "An in-tree AGENT run gets a ctxloom-controlled config home" scenario,
  # which is the positive control for every absence asserted here — it uses
  # the SAME fixture with the ONE addition of an explicit
  # `engine_home: session` declaration on the binding, and gets the opposite
  # outcome). The two must be read together, or "nothing was created"
  # degenerates into "nothing happens in this fixture at all".
  #
  # The baseline is asserted on PAYLOAD, not on the absence of a warning: the
  # engine really runs (exit 0, the spy process leaves its recording), it runs
  # in the LIVE project dir, not one of its config-home variables points into a
  # per-agent scratch tree, and no ctxloom-controlled home was created in the
  # project. An earlier version checked only that two phrases were missing from
  # the output, which an audit showed is equally satisfied by a run in which the
  # engine never launched at all.
  # ADJUSTED, ruled 2026-09-21: engine_home session by default; the seed
  # carries no refresh token. A run naming no agent gets the session home
  # too — the real home is reached only by a binding's explicit, unsafe
  # `engine_home: host`. The engine still runs in the live project dir; what
  # moved is its config home. The "touches no isolation mechanism" reading
  # of this scenario is now pinned by the explicit-host scenario below.
  Scenario Outline: workspace "none" runs Alice's own bare session in its session home, in the live project dir
    Given Alice has a git-backed project
    And Alice has whatever host credentials "<engine>" needs to authenticate
    When Alice runs "<engine>" under workspace "none" as her own session, naming no agent
    Then the run reports no isolation finding
    And the spy "<engine>" process's "<var>" env var points at this session's config-home instance
    And the spy "<engine>" process ran in the live project dir

    Examples:
      | engine      | var               |
      | claude-code | CLAUDE_CONFIG_DIR |

  # THE OTHER HALF, and the positive control the scenario above depends on: the
  # SAME project, the SAME none axis, the SAME engine — but Alice names an
  # agent WHOSE BINDING DECLARES engine_home: session, so the run gets a
  # controlled home instead of hers. A ctxloom agent that opts in this way is
  # not entitled to her memory, plugins, personal MCP registrations, global
  # agents or steering, and must not write its session state into them, so it is
  # given a PER-SESSION config-home INSTANCE under her ctxloom home
  # (`~/.ctxloom/sessions/<harp>/home/<leaf>`) instead — created at session start,
  # copied into one way from her real home, and disposable.
  #
  # THE DECLARATION RESTATES THE DEFAULT (ruled 2026-09-21): the sibling
  # "undeclared engine_home" scenario below uses this exact fixture MINUS the
  # declaration and proves the SAME outcome; only an explicit `engine_home:
  # host` — the unsafe selection — keeps the real home.
  #
  # This is asserted on PAYLOAD from INSIDE the spawned engine process, not on
  # ctxloom's own say-so: the spy dumps the config-home variable it was really
  # handed, and `cat`s the credential file that variable points at. Both halves
  # matter — a variable naming a home the engine cannot authenticate against is
  # worse than no relocation at all, and asserting only the variable would pass
  # in exactly that world.
  Scenario Outline: An in-tree AGENT run gets a per-session config-home instance instead of Alice's own home
    Given Alice has a git-backed project
    And Alice has whatever host credentials "<engine>" needs to authenticate
    And Alice's agent declares engine_home "session"
    When Alice runs the isolated "<engine>" agent under workspace "none"
    Then the run reports no isolation finding
    And the spy "<engine>" process's "<var>" env var points at this session's config-home instance
    And Alice's own "<engine>" home directory was never created by the run

    Examples:
      | engine      | var               |
      | claude-code | CLAUDE_CONFIG_DIR |

  # UNDECLARED engine_home — ADJUSTED, ruled 2026-09-21: engine_home session
  # by default; the seed carries no refresh token. This is the SAME fixture
  # as the scenario above with ONE difference: the "iso" binding never
  # declares engine_home at all — and gets the session home all the same.
  # Against a tree whose parser defaulted to host this scenario fails: the
  # spy would report no CLAUDE_CONFIG_DIR at all.
  Scenario Outline: An in-tree AGENT run with an undeclared engine_home gets the session home too
    Given Alice has a git-backed project
    And Alice has whatever host credentials "<engine>" needs to authenticate
    When Alice runs the isolated "<engine>" agent under workspace "none"
    Then the run reports no isolation finding
    And the spy "<engine>" process's "<var>" env var points at this session's config-home instance

    Examples:
      | engine      | var               |
      | claude-code | CLAUDE_CONFIG_DIR |

  # THE UNSAFE SELECTION. engine_home: host is the ONE way a run keeps the
  # real host home (ruled 2026-09-21): a binding that NAMES "host" gets
  # exactly what it asked for, and the launch names the selection unsafe. A
  # bug that ignored a declared "host" value (treating every resolved agent
  # binding as session regardless of what it declared) would make this
  # scenario red while the undeclared one stayed green, since only THIS one
  # exercises the declared-host branch.
  Scenario Outline: An in-tree AGENT run that declares engine_home: host keeps Alice's own real home
    Given Alice has a git-backed project
    And Alice has whatever host credentials "<engine>" needs to authenticate
    And Alice's agent declares engine_home "host"
    When Alice runs the isolated "<engine>" agent under workspace "none"
    Then the run reports no isolation finding
    And no ctxloom-controlled config home exists for "<engine>" in the project

    Examples:
      | engine      |
      | claude-code |

  # The credential half of the "gets a ctxloom-controlled config home" scenario
  # above, claude-code only. ADJUSTED, ruled 2026-09-21: engine_home session
  # by default; the seed carries no refresh token. The seed is claude's own
  # precedent: the engine reads its access token out of the controlled home
  # and authenticates, but the single-use ROTATING refresh token is withheld
  # — a copy that refreshes consumes the host's grant and revokes the
  # human's own login (three children died 401 on 2026-09-20/21 while the
  # host token rotated). Alice's own `~/.claude/.credentials.json` is READ,
  # never rewritten, and still carries its refresh token in full.
  #
  # The instance-side assertions are read from INSIDE the running engine: the
  # spy dumps what it was handed while it runs. The instance itself is an
  # Ephemeral member of the session (paths.HarpMembers) and is taken by the
  # ONE reaper (sessions.Reap) once the session ages out — there is no
  # session-end remover beside it. The last line pins that disposal end to
  # end: a reap bounded so the just-ended session counts as aged takes the
  # instance, credential copy and all, and this is the only place outside Go
  # tests that proves the reaper reaches it.
  Scenario: An in-tree AGENT run's instance credential authenticates without the refresh token, and the host's own copy is untouched
    Given Alice has a git-backed project
    And Alice has a "claude-code" credential fixture on the host
    And Alice's agent declares engine_home "session"
    When Alice runs the isolated "claude-code" agent under workspace "none"
    Then the isolated "claude-code" credential carries the access token and no refresh token
    And the host "claude-code" credential file was never modified
    And the copied "claude-code" credential was owner-only inside the run
    And the "claude-code" config-home instance is reaped once the session has aged out

  # THE AMBIENT COPY-IN'S OTHER HALF (D4): claude's instance `.claude.json` is
  # GENERATED by ctxloom, not copied. Alice's onboarding answer crosses by name
  # so an agent session does not re-onboard; the workspace-trust answer is
  # generated for the directory the run works in, so a headless run is not left
  # silently untrusted; her account identity (oauthAccount) crosses because
  # claude's own session seeding copies it beside the credential (ADJUSTED,
  # ruled 2026-09-21: engine_home session by default; the seed carries no
  # refresh token); and NOTHING else crosses — not her own mcpServers
  # registrations (and the secrets in their env blocks), not her per-project
  # prompt history, and not her standing "skip every permission prompt"
  # answer, which belongs to her own interactive session and not to an agent.
  #
  # The fixture deliberately carries all of them so the negative assertions
  # have something real to fail on: a scenario that proves "nothing leaked"
  # against a config with nothing in it proves nothing at all.
  Scenario: An in-tree AGENT run generates claude's instance config without carrying Alice's own registrations or history
    Given Alice has a git-backed project
    And Alice has a "claude-code" credential fixture on the host
    And Alice has a personal claude config carrying her own MCP servers
    And Alice's agent declares engine_home "session"
    When Alice runs the isolated "claude-code" agent under workspace "none"
    Then the run reports no isolation finding
    And the instance's claude config carries the generated trust answer and the account identity, and none of Alice's own registrations or history
    And the host "claude-code" credential file was never modified

  # The fail-loud half. With no env token and no host credential to seed,
  # ctxloom refuses rather than pointing claude at a controlled home it
  # cannot authenticate against — the same ClassIsolation mechanism the
  # worktree axis uses, never degradable to the real home. The failure names
  # the three remedies (ADJUSTED, ruled 2026-09-21: engine_home session by
  # default; the seed carries no refresh token), and it happens BEFORE any
  # engine is spawned.
  Scenario: An in-tree AGENT run refuses a controlled home it cannot authenticate
    Given Alice has a git-backed project
    And Alice has no "claude-code" credentials or API key on the host
    And Alice's agent declares engine_home "session"
    When Alice runs the isolated "claude-code" agent under workspace "none"
    Then the run aborts with an isolation finding naming "no CLAUDE_CODE_OAUTH_TOKEN, no ANTHROPIC_API_KEY, no host ~/.claude/.credentials.json"
    And the output contains "engine_home: host"

  # LOCKED — the safety net grave-prize exists to guarantee: a run that
  # declared engine_home: session, on the WORKTREE cell this time, for an
  # engine that DOES relocate credentials with its config-home var refuses to
  # start rather than silently handing the engine an empty, logged-out
  # config-home. The home is orthogonal to the worktree: the SAME resolver
  # and the SAME refusal the in-tree scenario above pins, reached from a
  # different cell. This is provable without any engine binary at all: the
  # finding fires, and the run aborts, BEFORE isolation.Prepare ever tries to
  # spawn one.
  Scenario Outline: A worktree run refuses to start an engine it cannot authenticate, rather than silently sharing the host's global credentials
    Given Alice has a git-backed project
    And Alice has no "<engine>" credentials or API key on the host
    And Alice's agent declares engine_home "session"
    When Alice runs the isolated "<engine>" agent under workspace "worktree"
    Then the run aborts with an isolation finding naming "<needle>"

    Examples:
      | engine      | needle                                                                                   |
      | claude-code | no CLAUDE_CODE_OAUTH_TOKEN, no ANTHROPIC_API_KEY, no host ~/.claude/.credentials.json    |

  # The bypass half of the SAME gate: an API key riding the environment is
  # its own proof of intent to authenticate that way (auth.go's
  # resolveEnvOrMountAuth precedence), so seeding is skipped and the run is
  # never blocked on a missing host credential file.
  #
  # "PROCEED" is the load-bearing word in this scenario's title, so it is what
  # gets asserted: the run exits 0, the engine really launches, and the
  # config-home variable it is handed points at this session's instance — the
  # controlled home the binding asked for is still in place, only the
  # credential gate stood down. Asserting merely that no finding was printed
  # used to let this pass under a mutation that stopped any engine from
  # launching, and under one that collapsed isolation to nothing at all; the
  # degrade warning's wording matched neither needle.
  Scenario Outline: The same engines proceed without any isolation finding once their API key rides the environment
    Given Alice has a git-backed project
    And Alice has no "<engine>" credentials on the host
    And Alice has set the "<engine>" API key in the environment
    And Alice's agent declares engine_home "session"
    When Alice runs the isolated "<engine>" agent under workspace "worktree"
    Then the run reports no isolation finding
    And the spy "<engine>" process's "<var>" env var points at this session's config-home instance

    Examples:
      | engine      | var               |
      | claude-code | CLAUDE_CONFIG_DIR |

  # LOCKED — the ISOLATED case, positively proven, but only ONE HALF of the
  # claim its own name suggests. What this scenario actually proves: ctxloom's
  # OWN bookkeeping — the env var it sets, the byte-for-byte copy it seeds,
  # the host original it leaves untouched — is correct. The spy is
  # cooperative BY CONSTRUCTION (see the file-level UPDATE note above): it
  # dumps whatever env it was handed and cats whatever file that env points
  # at, so this scenario is INCAPABLE of going red if a real vendor engine
  # read CLAUDE_CONFIG_DIR and then wrote somewhere else anyway —
  # that engine would never run here at all. A prior version of this comment
  # claimed otherwise ("this is the exact claim that would go RED the moment
  # a vendor engine... stopped honoring the var"); that claim was false and
  # has been corrected. The vendor half of the claim — does a REAL engine
  # binary actually honor the variable it was handed, credentials and all —
  # is proven live, against real engine binaries and real credentials, by
  # tests/acceptance/features/probes/isolation_probe.feature (`just isolation-probe
  # <engine> worktree`). The two layers are complementary, not redundant:
  # this one is fast, hermetic, and catches a ctxloom-side regression in CI on
  # every commit; the probe is slow, costs a real paid call, and is the one
  # that catches a vendor-side regression a spy can never see.
  # claude's seed withholds the refresh token (ADJUSTED, ruled 2026-09-21:
  # engine_home session by default; the seed carries no refresh token): the
  # SAME resolver and the SAME CopyAmbient seed as the in-tree cell — the
  # home is orthogonal to the worktree — so the isolated copy authenticates
  # but cannot rotate the host's single-use refresh token, and the host's
  # own file keeps its refresh token in full.
  Scenario: A worktree claude run's isolated config-home credential authenticates without the refresh token, and never touches the host's own copy
    Given Alice has a git-backed project
    And Alice has a "claude-code" credential fixture on the host
    And Alice's agent declares engine_home "session"
    When Alice runs the isolated "claude-code" agent under workspace "worktree"
    Then the spy "claude-code" process's "CLAUDE_CONFIG_DIR" env var points at this session's config-home instance
    And the isolated "claude-code" credential carries the access token and no refresh token
    And the host "claude-code" credential file was never modified

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
