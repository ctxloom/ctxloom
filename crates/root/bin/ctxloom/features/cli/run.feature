# world: base
# Ported from cli/run.feature (rev f36e8dfb2)
# Ported from journeys/j001200_recall.feature (rev f36e8dfb2): the Rule on resuming a session
# Ported from journeys/j002200_isolation.feature (rev f36e8dfb2): the last three Rules
# NOT ported: the scenarios tagged @pending (the mock engine's recorded input,
#   one-shot, dry-run, tag and agent selection, deny_tools, the opencode TUI):
#   they need `run` to launch an engine. Plan override: clap's usage error says
#   "unexpected argument" where cobra said "unknown flag"; the step maps one to
#   the other (plan 8.2).
@doc
Feature: run — assembling a project's context and handing it to an engine

  `run` is the core operation, and the only one whose whole job is to put
  something in front of a model. It resolves which context this invocation
  gets — a profile, loose fragments, a tag selection, or a named agent binding
  — assembles it, and launches the configured engine with that context and a
  prompt.

  Surfaces covered here:

    ctxloom run [prompt...]              (interactive: a session)
    ctxloom run --one-shot [prompt...]   (one turn, then exit)
    ctxloom run --dry-run                (render what would be sent; send nothing)
    ctxloom run --profile/-p <name>
    ctxloom run --tag/-t <tag>
    ctxloom run --command/-r <name>      (a saved command as the prompt)
    ctxloom run --agent <name>
    ctxloom run --session <harp>         (resume a session, owner ruling 115)

  A RUN IS EITHER A SESSION OR A SINGLE TURN, and `--one-shot` is how a caller
  says which. The flag names the MODE, not its output: every mode prints, and
  what actually differs is that a one-shot takes one turn and exits. Bare
  `ctxloom run` is the session — interactive and terminal-owning; the
  journeys drive it over a pty (j000200_setup.feature), and so does the one
  opencode scenario at the end, whose scripted engine records what it was
  launched with and exits.

  A RUN ASKED TO STOP STOPS ITS ENGINE FIRST (ruling 114). For the whole run
  ctxloom hears SIGHUP, SIGINT and SIGTERM, so none ends it by its default
  and leaves the engine working with no one listening. A one-shot stops on
  any of the three: the turn's process tree is asked to stop (SIGTERM),
  given five seconds, then killed. The session stops on SIGHUP or SIGTERM:
  its engine (the direct child) is killed; a SIGINT is the terminal's Ctrl-C,
  which reaches the engine itself, and ctxloom lets it pass. Either way the
  run's end is recorded as for any engine that exited, and the command exits
  with 128 plus the signal's number (129, 130, 143), the error
  `run-application-stopped` naming the signal. j001000 proves the one-shot's
  SIGTERM end to end.

  EVERY OTHER SCENARIO HERE DRIVES THE MOCK ENGINE, never a real one. That is not a
  compromise — it is what makes the full path OBSERVABLE. The mock records the
  setup payload that actually crossed the launch wire, so a scenario can assert
  what the model RECEIVED rather than that the command exited 0. `the mock
  recorded input contains` is the strongest assertion available on this
  surface, and the reason the deny_tools regression below is catchable at all:
  a per-hop test cannot see a field silently dropped crossing the proto wire,
  because the converter and its mirror struct read as a matched pair.

  Rule: The mode has one spelling, and asking for two at once is a refusal

    A caller reaching for a mode by a name it does not have must not get a run
    they did not ask for. An unknown flag is an error, and that error is what
    makes the real spelling discoverable; an alias quietly accepted alongside
    it would leave two names for one mode forever — in help, in scripts, and in
    every future reader's head.

    Scenario: A flag that is not the mode's name is refused rather than guessed at
      Given an initialized ctxloom project
      When Alice reaches for the mode by a name it does not have:
        """
        ctxloom run --print hello
        """
      Then the command fails
      And the output contains "unknown flag"

    # The help says what each spelling does: bare is the session, and the
    # one-shot and the dry run are the flags (hand-test 2026-11-10: it still
    # said this build takes only a one-shot or a dry run).
    Scenario: The help names the interactive session beside the one-shot and the dry run
      # new: the help of a bare run that is interactive.
      Given an initialized ctxloom project
      When I run "ctxloom run -h"
      Then the command succeeds
      And the output contains "a bare run is an interactive session"
      And the output contains "--one-shot takes one turn"
      And the output contains "--dry-run previews"
      And the output does not contain "this build"

    # `--agent` names a BINDING that already carries its own composed profiles,
    # engine and runtime, so combining it with the flags that assemble context
    # by hand is a caller asking for two different answers to the same
    # question.
    @pending
    Scenario: An agent binding and a hand-assembled profile cannot both decide the context
      Given an initialized ctxloom project
      And a profile "dev" exists
      When Alice names both a binding and a profile:
        """
        ctxloom run --agent developer --profile dev hello
        """
      Then the command fails
      And the output contains "none of the others can be"

  @pending
  Rule: A one-shot takes exactly one turn, and refuses to take an empty one

    Scenario: Run hands the assembled context to the model and returns its reply
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      And a profile "dev" with bundle "demo"
      And the mock LLM responds "MOCK-REPLY"
      When Alice asks a question with her project's context attached:
        """
        ctxloom run --one-shot --profile dev unicorn-prompt
        """
      Then the command succeeds
      And the output contains "MOCK-REPLY"
      And the mock recorded input contains "FRAGMENT-BODY-testing"
      And the run's session record carries no MCP endpoint

    # THE CHARACTERISTIC BUG, in its purest form. A one-shot gets exactly one
    # turn, so an empty prompt asks nothing at all: the run would exit 0, print
    # a reply to a question nobody posed, and `ctxloom run --one-shot > out.txt`
    # would leave a file whose emptiness looks like the model's answer. It is a
    # refusal instead, and the message names every door a prompt could have
    # arrived through so the caller knows which one they forgot.
    Scenario: A one-shot with no prompt from any door refuses to run
      Given an initialized ctxloom project
      And the mock LLM responds "MOCK-REPLY-NEVER-ASKED"
      When Alice starts a one-shot without asking anything:
        """
        ctxloom run --one-shot
        """
      Then the command fails
      And the output contains "nothing to run"
      And the output does not contain "MOCK-REPLY-NEVER-ASKED"

    # EVERY RUN MINTS A HARP, and the mint is the first thing that can fail.
    # A run that cannot be named has no session dir, no coordinator, no place
    # for its transcript, and no address a child could reach back to — so a
    # run that shrugged past the failed mint would spawn an engine whose every
    # later phase fails for a cause three phases behind it. The refusal is
    # here, at the mint, before any engine process exists. What the binary
    # can show is WHERE the failure was reported from: a run that reached a
    # plugin relays that plugin's failure, a run refused at the mint has no
    # plugin to relay. (The mock's record cannot testify here — a runner that
    # was spawned and then refused inside setup leaves no record either; the
    # cli package's unit test observes the spawn seam itself.)
    Scenario: A run whose session cannot be minted refuses before spawning the engine
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      And a profile "dev" with bundle "demo"
      And the mock LLM responds "MOCK-REPLY-UNNAMED-RUN"
      And the session store cannot be opened
      When Alice starts a one-shot that has nowhere to record its session:
        """
        ctxloom run --one-shot --profile dev unicorn-prompt
        """
      Then the command fails
      And the output contains "session"
      And the output does not contain "AI plugin failed"

    # ONE-SHOT AS A UNIVERSAL REDUCER: with no prompt on the command line, a
    # one-shot reads it from piped stdin, so anything a shell can produce can
    # be summarized, classified or rewritten with the project's own context
    # attached. The assertion is on what CROSSED THE WIRE — the piped text
    # reaching the model — because stdout carrying the mock's canned reply
    # would look identical whether the pipe was read or silently discarded.
    Scenario: A one-shot reads its prompt from piped input
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      And a profile "dev" with bundle "demo"
      And the mock LLM responds "MOCK-REPLY"
      When I run "ctxloom run --one-shot --profile dev" with input:
        """
        PIPED-PROMPT-summarize-this
        """
      Then the command succeeds
      And the output contains "MOCK-REPLY"
      And the mock recorded input contains "PIPED-PROMPT-summarize-this"

    # A saved command is content, stored in a bundle and resolved by name, so
    # `-r` is how a team's reviewed prompt becomes the thing that runs. The
    # BODY has to reach the model: a run that resolved the command, dropped its
    # content and launched with an empty prompt would still exit 0.
    Scenario: A saved command becomes the prompt
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a command "review" in bundle "demo" exists
      And a profile "dev" with bundle "demo"
      And the mock LLM responds "MOCK-REPLY"
      When Alice runs the review prompt her team saved:
        """
        ctxloom run --one-shot --profile dev -r review
        """
      Then the command succeeds
      And the output contains "MOCK-REPLY"
      And the mock recorded input contains "COMMAND-BODY-review"

  @pending
  Rule: A dry run renders what would be sent, and sends nothing

    `--dry-run` is the inspection surface: it stops before anything stateful or
    interactive happens — no session-index write, no coordinator, no task
    seeding, no isolation, no engine launch — and prints the composed context.

    # "the output contains dev" was satisfied by the dry-run header, which
    # names the profile whether or not assembly produced anything: a run that
    # loaded zero fragments and assembled an empty context passed. What a dry
    # run is FOR is showing what would be sent, so the fragment's REF and its
    # BODY are both asserted.
    #
    # The mock is configured and its reply must NOT appear. That is the half
    # that says "sends nothing": without it, a dry run that rendered the
    # context AND launched the engine anyway would pass every other assertion
    # here.
    Scenario: A dry run shows the composed context without launching a model
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      And a profile "dev" with bundle "demo"
      And the mock LLM responds "MOCK-REPLY-NEVER-SENT"
      When Alice checks what would be sent before sending it:
        """
        ctxloom run --dry-run --profile dev hello
        """
      Then the command succeeds
      And the output contains "dev"
      And the output contains "demo#fragments/testing"
      And the output contains "FRAGMENT-BODY-testing"
      And the output does not contain "MOCK-REPLY-NEVER-SENT"

    # U082-F03 regression: an explicit -t selection matching zero fragments
    # previously assembled Context: "" with a nil error and NO warning at all —
    # `ctxloom run -t <tag-that-matches-nothing>` exited 0 having delivered no
    # context whatsoever, indistinguishable from a working, empty-by-design
    # session. No per-hop test caught it: AssembleContext's own unit tests
    # asserted the (correct) empty Context string and stopped there, never
    # asking whether the CALLER could tell that apart from "nothing was asked
    # for". This asserts the CLI's own observable behavior — a non-zero exit
    # naming the tag — not just that assembly returned successfully.
    Scenario: An explicit tag selection matching nothing fails loudly, not silently
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      When Alice selects by a tag nothing carries:
        """
        ctxloom run --dry-run -t no-such-tag-anywhere hello
        """
      Then the command fails
      And the output contains "no-such-tag-anywhere"

  @pending
  Rule: What the profile declares reaches the launched backend

    A profile carries more than fragments: tool denials, permission posture,
    the engine it prefers. None of that is worth anything if it is lost between
    the command line and the process that runs.

    # deny_tools must survive every hop from the command line to the launched
    # engine. A per-hop test cannot see a field dropped between two hops whose
    # converter and mirror struct read as a matched pair; only tracing the
    # payload tip-to-tail (argv -> profile resolution -> the launch carrier ->
    # the runner's managed payload) does. This asserts the WIRE PAYLOAD the backend actually
    # received, not just that the command exited 0.
    Scenario: A tool denial configured on a profile reaches the launched backend
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a profile "guarded" with bundle "demo" and deny_tools "WebFetch"
      And the mock LLM responds "MOCK-REPLY"
      When Alice runs under a profile that forbids a tool:
        """
        ctxloom run --one-shot --profile guarded unicorn-prompt
        """
      Then the command succeeds
      And the mock recorded input contains "WebFetch"

  @pending
  Rule: What the launch learned about its own environment reaches the agent

    A launch computes findings about the ground the agent is about to stand
    on — a config key it does not know and therefore IGNORED, an isolation axis
    that degraded, a companion it expected and withheld. Until now those went
    to stderr, which the human may not be watching and the agent never sees;
    an agent whose config half-loaded then works confidently on the wrong
    premise and reports success. So the findings ride INTO the started agent's
    context, as doctor's own rows, and a flag opts out for the scripted runs
    where that is measured waste.

    # Both scenarios assert on the mock's RECORD of what crossed the launch
    # wire, never on the CLI's own stderr: the claim is that the AGENT received
    # the finding, and stderr is exactly the surface this rule exists to stop
    # relying on. Degraded mode is what lets a config finding reach a launch
    # at all — in strict mode the startup gate aborts on it first, which is
    # the other, already-covered half of the same doctrine. It is spelled as
    # the environment switch rather than the flag because the engine is a
    # child ctxloom process with a startup gate of its own, and only the
    # environment reaches it.
    Scenario: A config finding the launch proceeded past is delivered into the agent's context
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      And a profile "dev" with bundle "demo"
      And the project already has the file ".ctxloom/config.yaml":
        """
        schema_version: 8
        runt1me: host
        """
      And the mock LLM responds "MOCK-REPLY"
      And the environment variable "CTXLOOM_DEGRADED" is set to "1"
      When I run "ctxloom run --one-shot --profile dev unicorn-prompt"
      Then the command succeeds
      And the mock recorded input contains "FRAGMENT-BODY-testing"
      And the mock recorded input contains "DOCTOR-CHECK-STARTUP-FINDINGS"
      And the mock recorded input contains "unknown key `runt1me`"

    # A profile that VETOES a server with exclude_mcp means it, and the items
    # linked to that server — a hook among them — are withheld with it. The
    # agent is still told, since an item it cannot see otherwise reads as one
    # never authored; but a withhold is not a fault, so this launch runs in
    # STRICT mode (no CTXLOOM_DEGRADED) and must start, with the withhold listed.
    Scenario: A server vetoed with exclude_mcp withholds its linked items, and a strict launch starts and lists them
      Given an initialized ctxloom project
      And the project already has the bundle "linked":
        """
        version: "1.0"
        mcp:
          think:
            command: think-server
            tags: [ctxloom:link_id=think]
        fragments:
          guide:
            content: "LINKED-GUIDE"
            tags: [ctxloom:link_id=think]
          plain:
            content: "PLAIN-FRAGMENT"
        hooks:
          session_start:
            - command: think-warmup
              tags: [ctxloom:link_id=think]
        """
      And the project already has the file ".ctxloom/content/bundles/v2/project/profiles/vetoed.yaml":
        """
        description: vetoes the think server
        bundles:
          - linked
        exclude_mcp:
          - think
        """
      And the mock LLM responds "MOCK-REPLY"
      When I run "ctxloom run --one-shot --profile vetoed unicorn-prompt"
      Then the command succeeds
      And the mock recorded input contains "PLAIN-FRAGMENT"
      And the mock recorded input does not contain "LINKED-GUIDE"
      And the mock recorded input contains "DOCTOR-CHECK-WITHHELD-ITEMS"
      And the mock recorded input contains "linked#fragments/guide"
      And the mock recorded input contains "hook ctxloom+local:linked#hooks/session_start/0"
      And the mock recorded input contains "which this run was not granted"

    # The opt-out, paired with the positive scenario above and with the
    # assembled context still present in the record — so an absent finding is
    # evidence of the flag, not of a launch that delivered nothing.
    Scenario: --no-startup-findings withholds the findings and nothing else
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      And a profile "dev" with bundle "demo"
      And the project already has the file ".ctxloom/config.yaml":
        """
        schema_version: 8
        runt1me: host
        """
      And the mock LLM responds "MOCK-REPLY"
      And the environment variable "CTXLOOM_DEGRADED" is set to "1"
      When I run "ctxloom run --one-shot --no-startup-findings --profile dev unicorn-prompt"
      Then the command succeeds
      And the mock recorded input contains "FRAGMENT-BODY-testing"
      And the mock recorded input does not contain "DOCTOR-CHECK-STARTUP-FINDINGS"
      And the mock recorded input does not contain "unknown key `runt1me`"

  @pending
  Rule: An opencode label runs opencode's own TUI on ctxloom's terminal

    # A SCRIPTED opencode (the label's binary_path) stands in for the real
    # CLI: no model, no network. It records what it was handed and exits, so
    # the record proves the interactive launch end to end — the TUI's argv
    # (the prompt a flag, not stdin), a terminal on both ends, the assembled
    # guidance in the AGENTS.md delivered to its config dir, Alice's own
    # tui.json carried in beside it (the relocated config would otherwise
    # drop her theme), the snapshot default a TUI's /undo needs, and the
    # bridge plugin that carries her session's hooks, mail and wake.
    @opencode
    Scenario: An opencode label's run is opencode's TUI on a terminal, with the guidance and Alice's TUI preferences
      Given Alice has an opencode label served by a scripted opencode TUI, and a fragment of guidance
      And Alice keeps her own opencode TUI preferences
      When Alice runs the opencode label interactively with the prompt "hello tui"
      Then the scripted opencode TUI ran on a terminal with the prompt as a flag, the guidance and Alice's preferences in its config dir
      And the bridge plugin in its config dir fires ctxloom's session-start and mail-drain for opencode, and the TUI is told its wake socket

  # Owner ruling 115 (2026-11-10): `run --session <harp>` resumes a session,
  # a one-shot's or the terminal's: its engine is handed its own session
  # (claude's `--resume <id>`, opencode's `--session <id>`), in the same
  # session home, as the session's next incarnation on the same stream (a run
  # of its own, UUIDv5(session, incarnation)). A session that is running (its
  # lock held, or its last run not ended), one whose engine never named its
  # own session, one `session remove` destroyed, a name no session holds, and
  # another engine than the session's are each refused, before anything is
  # launched.
  Rule: run --session resumes a session in its engine's own session, as its next incarnation

    Scenario: Another turn on a session resumes the engine's own session in the same session
      # new: owner ruling 115 (2026-11-10).
      Given Alice has a project that has never used ctxloom
      And Alice has set ctxloom up in her project
      And Alice has started her assistant through ctxloom
      When Alice takes another turn on that session with "and then?"
      Then the command succeeds
      And the reply names the same session
      And the assistant was resumed on its own session "00000000-0000-4000-8000-0000000000aa"

    Scenario: A name no session holds is refused, and nothing is launched
      # new: owner ruling 115 (2026-11-10).
      Given Alice has a project that has never used ctxloom
      And Alice has set ctxloom up in her project
      When Alice resumes a session that does not exist:
        """
        ctxloom run --one-shot "again" --session no-such-heron
        """
      Then the command exits with code 20
      And the refusal is "sessions-session-unknown"
      And no assistant was launched

  # Superseded by ruling 115 above (resume is the engine's own session, not a
  # context fold); kept pending as Go's design.
  @pending
  Rule: Resuming a session folds in its recorded conversation, and leaves the record as it was

    `run --session <harp>` reads the session's recorded conversation, the one
    ctxloom keeps whatever engine held it, and folds it into the run's
    context. The fixture's session carries one marker in its title and
    another only in its conversation, so "found by its title" and "resumed
    with its conversation" are told apart.

    Scenario: Resuming the March session puts that conversation back in front of a model
      # substitution: the journey's March fixture is a Given of the scenario, not a Background.
      Given a design question everyone remembers deciding and nobody remembers why
      When I run "ctxloom run --session amber-quiet-heron --dry-run -p default"
      Then the assembled context carries the conversation she had in March

    # A resume that consumed, truncated or rewrote what it read would fail the
    # second person to ask. The record is read back by its bytes.
    Scenario: Recalling a session does not consume it
      # substitution: the journey's March fixture is a Given of the scenario, not a Background.
      Given a design question everyone remembers deciding and nobody remembers why
      When I run "ctxloom run --session amber-quiet-heron --dry-run -p default"
      Then the canonical transcript she recalled from is still on disk, untouched

  @pending
  Rule: A worktree run hides its per-agent config from the project's git

    # A worktree run's per-agent config edits are kept out of the shared
    # project tree through .git/info/exclude, not left as new files a
    # developer could merge back by accident.
    Scenario: A worktree run leaves the project tree clean
      # substitution: the journey's Background is the scenario's first Given
      Given Alice has a git-backed project with a mock agent
      When Alice runs the mock agent under workspace "worktree" with prompt "cleanup-check"
      Then none of the per-agent worktree config artifacts appear in the project's git status
      And the shared git exclude file carries the ctxloom per-agent worktree config block

  @pending
  Rule: Every gate that loses a requested container refuses the run, --degraded or not

    The fixture's agent asks for a container. One flag cannot mean both "I
    accept a thinner context" and "I accept no sandbox", so --degraded never
    rescues a lost container: each outline's two rows differ only in the flag,
    and a single row could not tell "--degraded is ignored" from "--degraded
    was never passed". Each gate keeps its own outcome step, which asserts its
    own finding and the other gates' absence, since every row exits 21 and the
    wording is all that tells the gates apart.

    # A dry run does not stop at the refusal: it renders the plan it could
    # compute, lists the finding with its fix, and exits as the run would,
    # whatever the format. Both rows read the probed environment off the plan
    # (runtime unavailable, reach unknown), never the host the refused run
    # does not fall back to.
    Scenario Outline: Previewing a container with no runtime shows the plan, then refuses as the run would
      # substitution: the journey's Background is the scenario's first Given
      Given Alice has a git-backed project with a mock agent
      When Alice previews the container-bound agent with flags "<flags>"
      Then the run aborts with an isolation finding
      And the preview rendered its plan and the refusal's fix

      Examples:
        | flags         |
        |               |
        | --format json |

    # The START gate: the runtime is reachable, so a container is selected,
    # and the image cannot be produced (an override image is run as-is and
    # never built). The finding says the session is NOT sandboxed.
    Scenario Outline: A container that was selected and cannot start REFUSES, and --degraded does not rescue it
      # substitution: the journey's Background is the scenario's first Given
      Given Alice has a git-backed project with a mock agent
      When Alice runs a container-bound agent whose image cannot be produced, with flags "<flags>"
      Then the run aborts at the container START gate

      Examples:
        | flags      |
        |            |
        | --degraded |

    # The same gate with both axes asked for: the container tier is the
    # worktree-backed one, and the tier below it is a plain host worktree,
    # which looks like isolation and is the degrade most tempting to accept.
    # A check that recognised only the live-dir container would pass the row
    # above and let this run land on the host.
    Scenario Outline: A containerized worktree that cannot start REFUSES rather than keeping the worktree on the host
      # substitution: the journey's Background is the scenario's first Given
      Given Alice has a git-backed project with a mock agent
      When Alice runs a container-bound agent whose image cannot be produced, with flags "--workspace worktree <flags>"
      Then the run aborts at the container START gate

      Examples:
        | flags      |
        |            |
        | --degraded |

    # The transport-start gate: the daemon accepts the run and the container
    # never reaches running. The session was already told it had a boundary,
    # so falling back would run on the host something that believes it is
    # sandboxed. The outcome step also asserts the engine never ran: an abort
    # that still launched it would pass on the exit code alone.
    Scenario Outline: A container accepted by the daemon that never runs is fatal in BOTH modes
      # substitution: the journey's Background is the scenario's first Given
      Given Alice has a git-backed project with a mock agent
      When Alice runs a container-bound agent whose container dies at the daemon, with flags "<flags>"
      Then the run aborts because the container never reached running state

      Examples:
        | flags      |
        |            |
        | --degraded |

  @pending
  Rule: A run whose engine cannot authenticate, or cannot load its MCP servers, is refused before the engine starts

    # session_home: host leaves claude's MCP servers in the project's own
    # .mcp.json, which claude loads only in a repository Alice trusted in her
    # own claude: in one she never trusted, the run is refused.
    Scenario: An in-tree AGENT run that declares session_home: host in a project Alice never trusted is refused
      # new: Go's scenario of this name says engine_home; the vocabulary ruling (2026-11-06) renamed the setting to session_home, so the scenario carries the new name
      Given Alice has a git-backed project
      And Alice has whatever credentials "claude-code" needs to authenticate
      And Alice has not trusted the project in her own claude
      And Alice's agent declares session_home "host"
      When Alice runs the isolated "claude-code" agent under workspace "none"
      Then the run is refused because an untrusted repository's session cannot load the project's MCP file

    # A login session needs the login: with no claude login on the host, it
    # is refused before any engine is spawned, naming what to do. The host and
    # the container refuse alike.
    Scenario: Alice's login session with no login on the host is refused, naming auth token
      # substitution: the agent declares session_home where Go's step says engine_home (vocabulary ruling, 2026-11-06)
      Given Alice has a git-backed project
      And Alice has no "claude-code" credentials or API key on the host
      And Alice has no "claude-code" credentials on the host
      And Alice's agent declares session_home "session"
      And Alice's own session authenticates with "login"
      When Alice runs the isolated "claude-code" agent under workspace "none"
      Then the run is refused without any isolation finding, naming "auth: token"

  Rule: A run holds its session's lock for as long as it runs, so its session reads live

    The lock is `.ctxloom/sessions/<harp>.lock`, beside the session's
    directory: an exclusive file lock the run takes once its session is
    minted and lets go after its engine exits. Another process probes it
    without holding it: held is live, free is ended. So while a session runs,
    ctxloom will not remove it without `--even-if-live`. A harp never changes
    (S55): a rename sets the session's title, live or ended, and its directory
    and lock stay under its harp.

    Scenario: While her session runs another terminal sees it live, titles it, and cannot remove it
      # new: concurrent-primaries defects 1 and 2, with a stub engine on PATH
      # that runs the other terminal's commands itself, while it runs.
      Given Alice has a project that has never used ctxloom
      And Alice has set ctxloom up in her project
      When Alice runs her assistant interactively, and while it runs another terminal lists, renames and removes its session:
        """
        ctxloom run
        """
      Then while it ran, the listing showed its session as live
      And while it ran, renaming its session set its title and moved nothing
      And while it ran, removing its session was refused because its owner is alive
      And after it ended, its session is listed as ended under its own name
      And after it ended, its session's directory and lock are still under its harp

  # A CHILD agent (a one-shot is one) authenticates only with a credential in
  # its environment: the token the human mints with `claude setup-token` and
  # exports, or an API key. ctxloom never captures or stores one, and a child
  # is never given a shared login file. With none exported the run is refused
  # before any engine is spawned, naming how to mint and export one. (Owner
  # ruling 2026-11-08, credentials: an exported API key IS accepted; Go's
  # scenario here, "an exported API key does not stand in", is superseded.)
  Rule: A child run needs a credential in its environment, and either the token or an API key is one

    Scenario: A child run with no credential at all is refused, naming claude setup-token
      #   and exit codes per rulings 149, 152, 153 (10 failed, 20 refused, 21 fatal findings; Go's 1, 2, 3).
      # substitution: Go refused with "auth: token" for a token session; the refusal is the typed `runs-no-credential` and its fix names the token flow.
      Given Alice has a project that has never used ctxloom
      And Alice has set ctxloom up in her project
      And Alice has exported no credential
      When Alice runs a child agent with no credential:
        """
        ctxloom run --one-shot "hello"
        """
      Then the command exits with code 20
      And the refusal is "runs-no-credential"
      And the refusal holds no list of refusals
      And the refusal's fix says "claude setup-token"
      When Alice asks again in text, which carries the fix:
        """
        ctxloom run --one-shot "hello" --format text
        """
      Then the command exits with code 20
      And the output contains "claude setup-token"
      And no assistant was launched

    Scenario Outline: A child run with <variable> exported, and no other credential, runs
      # new: the owner's credentials ruling accepts an exported API key as a child's credential.
      Given Alice has a project that has never used ctxloom
      And Alice has set ctxloom up in her project
      And Alice has exported only the credential "<variable>"
      When Alice runs a child agent on that credential:
        """
        ctxloom run --one-shot "hello"
        """
      Then the command succeeds
      And an assistant was launched

      Examples:
        | variable                |
        | CLAUDE_CODE_OAUTH_TOKEN |
        | ANTHROPIC_API_KEY       |

    # A credentials file serves the human's own primary session alone (ruling
    # 8), so an engine whose source names a file and no variable can
    # authenticate no child: the run is refused before anything starts,
    # naming the variables to put in the source and export, even with one of
    # them exported (the source does not name it, so the child would not get
    # it). Her interactive session is unchanged.
    Scenario: A child run of an engine whose credential source is a file alone is refused
      # new: owner ruling 53 (2026-11-09).
      Given Alice has a project that has never used ctxloom
      And Alice has set ctxloom up in her project
      And Alice has exported only the credential "CLAUDE_CODE_OAUTH_TOKEN"
      And the home already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        engines:
          claude-code:
            auth:
              file: ~/.claude/.credentials.json
        """
      When Alice runs a child agent on a file-only credential source:
        """
        ctxloom run --one-shot "hello"
        """
      Then the command exits with code 20
      And the refusal is "runs-file-only-credential"
      And the refusal's fix says "engines.claude-code.auth.env"
      And the output contains "CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_API_KEY"
      And no assistant was launched

    # opencode's login mirrors claude's: a child of an opencode source that
    # is a file alone is refused the same way, naming opencode's built-in
    # variables: its providers' keys (rulings 69 and 79).
    Scenario: A child opencode run whose credential source is a file alone is refused
      # new: owner rulings 53, 69 and 79 (2026-11-10).
      Given Alice has a project that has never used ctxloom
      And Alice has set ctxloom up in her project
      And the home already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        engines:
          opencode:
            auth:
              file: ~/.local/share/opencode/auth.json
        """
      When Alice runs a child opencode agent on a file-only credential source:
        """
        ctxloom run --engine opencode --one-shot "hello"
        """
      Then the command exits with code 20
      And the refusal is "runs-file-only-credential"
      And the refusal's fix says "engines.opencode.auth.env"
      And the output contains "ANTHROPIC_API_KEY, OPENAI_API_KEY"
      And the output does not contain "CTXLOOM_OPENCODE_API_KEY"

  Rule: A CTXLOOM_PERMIT_<NAME> variable reaches the agent as <NAME>

    An engine's environment is curated: its allowlist, the engine block's
    `env`, the credential source, and blocks of its own (opencode never passes
    an `OPENCODE_*` variable of hers). `CTXLOOM_PERMIT_<NAME>=value` in the
    environment she runs ctxloom in is the escape hatch: the agent is given
    `<NAME>=value`, a child's turn and her interactive session alike, whatever
    the curation says. The permit itself is not passed on, and a value is
    never shown. A permit can never replace one of the run's own `CTXLOOM_*`
    variables, nor anything else ctxloom sets for the engine (the session home
    it relocates the engine into, its HOME, which is that session home (owner
    ruling 112), and the PATH it builds for the hooks): it is ignored, and the
    run says so. An explicit permit for
    `ANTHROPIC_AUTH_TOKEN` gives a child the primary's token (without one it
    never reaches a child): such a child holds her token, no credential of
    its own.

    Scenario: A permit gives her interactive opencode session a variable opencode's block withholds
      # new: owner ruling 78 (2026-11-10).
      Given Alice has a project that has never used ctxloom
      And Alice has set ctxloom up in her project
      And the environment variable "CTXLOOM_PERMIT_OPENCODE_API_KEY" is set to "oc-fixture-not-real"
      When Alice runs her assistant on opencode interactively, and it reports its environment:
        """
        ctxloom run --engine opencode
        """
      Then opencode's environment held "OPENCODE_API_KEY" as "oc-fixture-not-real", and no CTXLOOM_PERMIT_ variable
      And the output does not contain "oc-fixture-not-real"

    Scenario: A permit for one of the run's own variables is ignored, and the run says so
      # new: owner ruling 78 (2026-11-10).
      Given Alice has a project that has never used ctxloom
      And Alice has set ctxloom up in her project
      And the environment variable "CTXLOOM_PERMIT_CTXLOOM_BEARER" is set to "forged-bearer-not-real"
      When Alice runs her assistant on opencode interactively, and it reports its environment:
        """
        ctxloom run --engine opencode
        """
      Then the command succeeds
      And the output contains "CTXLOOM_PERMIT_CTXLOOM_BEARER"
      And the output contains "never replaced"
      And the output does not contain "forged-bearer-not-real"
      And opencode's "CTXLOOM_BEARER" was the run's own, not "forged-bearer-not-real"

    # A permit never replaces what ctxloom itself sets for the engine: the
    # session home it relocates the engine into, and the PATH it builds for
    # the hooks (owner ruling 78b). Each is ignored, and said.
    Scenario: A permit for a name ctxloom sets for opencode is ignored, and the run says so
      # new: owner ruling 78b (2026-11-10).
      Given Alice has a project that has never used ctxloom
      And Alice has set ctxloom up in her project
      And the environment variable "CTXLOOM_PERMIT_HOME" is set to "/evil-home"
      And the environment variable "CTXLOOM_PERMIT_XDG_DATA_HOME" is set to "/evil-data"
      And the environment variable "CTXLOOM_PERMIT_OPENCODE_CONFIG_DIR" is set to "/evil-config"
      And the environment variable "CTXLOOM_PERMIT_PATH" is set to "/evil"
      And the environment variable "CTXLOOM_PERMIT_OPENCODE_API_KEY" is set to "oc-fixture-not-real"
      When Alice runs her assistant on opencode interactively, and it reports its environment:
        """
        ctxloom run --engine opencode
        """
      Then opencode's HOME, XDG_DATA_HOME and OPENCODE_CONFIG_DIR were its session home's, and its PATH led with ctxloom's directory
      And opencode's environment held "OPENCODE_API_KEY" as "oc-fixture-not-real", and no CTXLOOM_PERMIT_ variable
      And the output contains "CTXLOOM_PERMIT_HOME was not passed on"
      And the output contains "CTXLOOM_PERMIT_XDG_DATA_HOME was not passed on"
      And the output contains "CTXLOOM_PERMIT_OPENCODE_CONFIG_DIR was not passed on"
      And the output contains "CTXLOOM_PERMIT_PATH was not passed on"

    Scenario: A permit for a name ctxloom sets for claude is ignored in a child's turn
      # new: owner ruling 78b (2026-11-10).
      Given Alice has a project that has never used ctxloom
      And Alice has set ctxloom up in her project
      And Alice has exported only the credential "CLAUDE_CODE_OAUTH_TOKEN"
      And the environment variable "CTXLOOM_PERMIT_CLAUDE_CONFIG_DIR" is set to "/evil-config"
      And the environment variable "CTXLOOM_PERMIT_HOME" is set to "/evil-home"
      When Alice runs a child agent with permits for what ctxloom sets:
        """
        ctxloom run --one-shot "hello"
        """
      Then the command succeeds
      And the assistant's "CLAUDE_CONFIG_DIR" was ctxloom's, not "/evil-config"
      And the assistant's "HOME" was ctxloom's, not "/evil-home"
      And the assistant's HOME was its session home
      And the output contains "CTXLOOM_PERMIT_CLAUDE_CONFIG_DIR was not passed on"
      And the output contains "CTXLOOM_PERMIT_HOME was not passed on"

    Scenario: An explicit permit gives a child the primary's token
      # new: owner ruling 78a (2026-11-10).
      Given Alice has a project that has never used ctxloom
      And Alice has set ctxloom up in her project
      And Alice has exported only the credential "CLAUDE_CODE_OAUTH_TOKEN"
      And the environment variable "CTXLOOM_PERMIT_ANTHROPIC_AUTH_TOKEN" is set to "primary-token-not-real"
      When Alice runs a child agent with a permit for the primary's token:
        """
        ctxloom run --one-shot "hello"
        """
      Then the command succeeds
      And the assistant's environment held "ANTHROPIC_AUTH_TOKEN" as "primary-token-not-real"

    Scenario: Without a permit the primary's token never reaches a child, even when the config names it
      # new: owner rulings 15 and 78a (2026-11-10).
      Given Alice has a project that has never used ctxloom
      And Alice has set ctxloom up in her project
      And Alice has exported only the credential "CLAUDE_CODE_OAUTH_TOKEN"
      And the environment variable "ANTHROPIC_AUTH_TOKEN" is set to "primary-token-not-real"
      And the home already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        engines:
          claude-code:
            env: [ANTHROPIC_AUTH_TOKEN]
        """
      When Alice runs a child agent with no permit for the primary's token:
        """
        ctxloom run --one-shot "hello"
        """
      Then the command succeeds
      And the assistant's environment did not hold "ANTHROPIC_AUTH_TOKEN"

  Rule: Alice's interactive opencode session shares her opencode login by a link, never a copy

    opencode keeps its login in `auth.json` in its data directory
    (`$XDG_DATA_HOME/opencode`, else `~/.local/share/opencode`), and an OAuth
    provider's refresh writes the rotated token back to it. Her interactive
    session's relocated data directory is given a link to her own file, so a
    refresh there is a refresh of her login; a copy's would invalidate it. A
    child is never given the file.

    Scenario: Her interactive opencode session's data directory links her opencode login
      # new: owner ruling 69 (2026-11-10), with a stub opencode on PATH that
      # reports what its data directory holds.
      Given Alice has a project that has never used ctxloom
      And Alice has set ctxloom up in her project
      And the home already has the file ".local/share/opencode/auth.json":
        """
        {"fixture": "not a real login"}
        """
      When Alice runs her assistant on opencode interactively, and it reports its login:
        """
        ctxloom run --engine opencode
        """
      Then the command succeeds
      And opencode's data directory held a link to her opencode login, not a copy

  @pending
  Rule: --engine runs the engine it names, with that engine's defaults

    A run says what runs in one of two ways: `--agent <name>` runs an agent,
    and `--engine <engine>` runs that engine with its engine defaults, with
    no agent named. An engine ctxloom does not have is refused by name, and
    nothing runs.

    Scenario: A run naming an engine runs that engine with its defaults
      # new: run takes --engine.
      Given an initialized ctxloom project
      And the mock LLM responds "MOCK-REPLY"
      When Alice runs the mock engine by name:
        """
        ctxloom run --one-shot --engine mock hello
        """
      Then the command succeeds
      And the run was on the mock engine, with its engine defaults

    Scenario: A run naming an engine ctxloom does not have is refused, naming it
      # new: run refuses an unknown engine.
      Given an initialized ctxloom project
      When Alice names an engine ctxloom does not have:
        """
        ctxloom run --one-shot --engine bogus-engine hello
        """
      Then the command fails
      And the output contains "bogus-engine"
      And no model was asked anything

  Rule: A refused run says every check it failed at once, and leaves nothing behind

    Before a run starts, it is checked: the plan's fatal findings, a one-shot's
    prompt, an engine this build can drive, and a child's credential. A run
    that fails more than one says them all in one refusal, `runs-launch-refused`,
    in that order (each the refusal it would be alone: its code, args, message
    and fix), so she fixes them together instead of one per attempt. One
    failure is its own refusal, exactly as before. The exit is 20, or 21 when
    the plan's fatal findings are among the failures (they are said as
    warnings beside the refusal, as a run past them always said them). An
    engine nobody drives is still planned, so the plan and the prompt are
    judged; its credential cannot be. A refused run claims no name and makes
    no session: nothing of it is listed, and nothing is left in the project.

    Scenario: A one-shot with no prompt and no credential is refused for both at once
      # new: owner rulings 73, 73a, 73b.
      Given Alice has a project that has never used ctxloom
      And Alice has set ctxloom up in her project
      And Alice has exported no credential
      When Alice starts a child agent with nothing to ask and no credential:
        """
        ctxloom run --one-shot
        """
      Then the command exits with code 20
      And the refusal is "runs-launch-refused"
      And the refusals are "runs-no-prompt, runs-no-credential"
      And each refusal carries its own message and fix
      And no assistant was launched
      And the file ".ctxloom/sessions" does not exist
      When Alice asks again in text:
        """
        ctxloom run --one-shot --format text
        """
      Then the command exits with code 20
      And the output contains "nothing to run"
      And the output contains "claude setup-token"

    Scenario: An engine nobody drives is still planned, so a missing prompt is said beside it
      # new: owner rulings 73c, 73d.
      Given Alice has a project that has never used ctxloom
      And Alice has set ctxloom up in her project
      And Alice has exported no credential
      When Alice names an engine ctxloom does not have, with nothing to ask:
        """
        ctxloom run --one-shot --engine bogus-engine
        """
      Then the command exits with code 20
      And the refusal is "runs-launch-refused"
      And the refusals are "runs-no-prompt, runs-unknown-engine"
      And the output contains "bogus-engine"
      And no assistant was launched

    Scenario: A fatal finding beside a refused credential is exit 21, and both are said
      # new: owner rulings 73a, 73c.
      Given Alice has a project that has never used ctxloom
      And Alice has set ctxloom up in her project
      And Alice has exported no credential
      And the project already has the file ".ctxloom/content/bundles/v2/project/profiles/broken.yaml":
        """
        description: references a missing bundle
        bundles:
          - does-not-exist
        """
      When Alice runs a child agent on a broken profile with no credential:
        """
        ctxloom run --one-shot --profile broken hello
        """
      Then the command exits with code 21
      And the refusal is "runs-launch-refused"
      And the refusals are "runs-no-credential"
      And the output contains "does-not-exist"
      And no assistant was launched
      And the file ".ctxloom/sessions" does not exist

  # Owner rulings 89 and 106: a session's outputs (its plans, its reports)
  # belong to the project, not to a folder in her home. `output.dir` names
  # where they go: relative to the project root unless absolute, `ctxloom/`
  # when the config names nowhere. It is NOT gitignored: what a session leaves
  # there can be committed. Each session's outputs are `<output.dir>/<harp>/`,
  # and a harp never changes (S55), so neither does that folder.
  Rule: A session's outputs go under the project's output directory

    Scenario: A session's outputs go under ctxloom/ in her project
      # new: owner rulings 89, 106.
      Given Alice has a project that has never used ctxloom
      And Alice has set ctxloom up in her project
      When Alice asks her assistant one thing:
        """
        ctxloom run --one-shot --format json hello
        """
      Then the command succeeds
      And the session that answered keeps its outputs in "ctxloom" of her project

    Scenario: output.dir names where a session's outputs go
      # new: owner ruling 106.
      Given Alice has a project that has never used ctxloom
      And Alice has set ctxloom up in her project
      When Alice asks her assistant one thing, her outputs kept elsewhere:
        """
        ctxloom --config-set output.dir=notes/sessions run --one-shot --format json hello
        """
      Then the command succeeds
      And the session that answered keeps its outputs in "notes/sessions" of her project

  # A run tells its session when its incarnation ended (ruling 85), however
  # it ended; that end is when the session's last run ended, so the listing's
  # END and `show`'s ended_at say it once the lock is free (hand-test
  # 2026-11-10: both were blank for every session that had ended).
  Rule: A session whose run ended says when it ended

    Scenario: A one-shot's session lists when it ended
      # new: the session's end is its incarnation's end (ruling 85).
      Given Alice has a project that has never used ctxloom
      And Alice has set ctxloom up in her project
      When Alice asks her assistant one thing:
        """
        ctxloom run --one-shot --format json hello
        """
      Then the command succeeds
      And the session that answered is ended and says when it ended

  # Owner rulings 133-139 (2026-11-11): permissions are per engine, in the
  # engine's own words. ctxloom has no posture of its own any more: the
  # engine-neutral `permissions.approver`, `approval_timeout`, `sandbox` and
  # `network` are gone (ruling 134, unknown keys, refused like any other), and
  # with them the launch's check of a posture against its runtime
  # (`runs-unenforced-posture`).
  Rule: The engine-neutral posture keys are gone

    Scenario Outline: A neutral posture key is an unknown key, at the project and on an agent
      # new: owner ruling 134 (hard migrate, no alias)
      Given Alice has a project that has never used ctxloom
      And Alice has set ctxloom up in her project
      When Alice runs a child agent with a posture key ctxloom no longer has:
        """
        ctxloom --config-set <path>=<value> run --one-shot hello
        """
      Then the command exits with code 20
      And the output contains "unknown key `<unknown>"
      And no assistant was launched

      Examples:
        | path                               | value     | unknown                    |
        | permissions.network                | none      | permissions                |
        | permissions.sandbox                | read-only | permissions                |
        | permissions.approver               | human     | permissions                |
        | permissions.approval_timeout       | 90s       | permissions                |
        | agents.default.permissions.network | open      | agents.default.permissions |

  # Owner rulings 135, 137, 145: an agent's `agents.<n>.engines.claude-code.
  # permissions` IS claude's own permissions document, copied verbatim and not
  # checked by ctxloom. It is the `permissions` object of the user-scope
  # settings of the session's own claude config dir, a file of its own: never
  # the `--settings` file that holds ctxloom's hooks, because claude refuses a
  # settings file whole for one invalid value, hooks and all (ruling 145). No
  # `--permission-mode` is passed (ruling 137 retires ruling 116's flag):
  # claude's mode is the document's `defaultMode`. An agent with no block runs
  # on ctxloom's default document for claude, `{defaultMode: acceptEdits}`; a
  # block she writes replaces it whole.
  Rule: The agent's claude permissions reach claude verbatim, in a settings file apart from ctxloom's hooks

    Scenario: A child's turn on the default agent runs on ctxloom's default document
      # new: owner rulings 133, 137 (2026-11-11)
      Given Alice has a project that has never used ctxloom
      And Alice has set ctxloom up in her project
      When Alice runs a child agent on her default agent:
        """
        ctxloom run --one-shot hello
        """
      Then the command succeeds
      And the assistant's settings hold the permissions '{"defaultMode": "acceptEdits"}'
      And the assistant's settings still register ctxloom's hooks
      And the assistant was launched without "--permission-mode"

    Scenario: Her own claude settings are the session's, but for its permissions
      # new: owner ruling 169 (2026-11-12)
      Given Alice has a project that has never used ctxloom
      And Alice has set ctxloom up in her project
      And Alice's own claude settings are:
        """
        {"attribution": {"commit": "", "pr": ""}, "model": "opus", "permissions": {"allow": ["Bash(*)"]}}
        """
      When Alice runs a child agent on her default agent:
        """
        ctxloom run --one-shot hello
        """
      Then the command succeeds
      And the assistant's settings hold the permissions '{"defaultMode": "acceptEdits"}'
      And the assistant's settings still register ctxloom's hooks

    Scenario: Her session runs on the document she wrote, as she wrote it
      # new: owner rulings 135, 137 (2026-11-11)
      Given Alice has a project that has never used ctxloom
      And Alice has set ctxloom up in her project
      When Alice starts her session in plan mode, with two rules of claude's:
        """
        ctxloom --config-set agents.default.engines.claude-code.permissions.defaultMode=plan --config-set agents.default.engines.claude-code.permissions.allow=Read,Grep run
        """
      Then the command succeeds
      And the assistant's settings hold the permissions '{"defaultMode": "plan", "allow": ["Read", "Grep"]}'
      And the assistant's settings still register ctxloom's hooks
      And the assistant was launched without "--permission-mode"

    Scenario: A word ctxloom does not know is passed on unchecked
      # new: owner ruling 135 (a typo reaches the engine, the accepted cost);
      # this replaces ruling 125's refusal of a mode ctxloom did not have.
      Given Alice has a project that has never used ctxloom
      And Alice has set ctxloom up in her project
      When Alice runs a child agent with the mode key ctxloom used to have:
        """
        ctxloom --config-set agents.default.engines.claude-code.permissions.mode=auto run --one-shot hello
        """
      Then the command succeeds
      And the assistant's settings hold the permissions '{"mode": "auto"}'
      And the assistant was launched without "--permission-mode"

    # Owner T21 (2026-11-11): the settings file sits in the session's own
    # directory, where the agent's file tools reach it, so it is regenerated
    # from her config at every launch (a fresh session, a child's turn, a
    # resume): an agent's edit of its own permissions never survives into a
    # launch.
    Scenario: An agent's edit of its own settings does not survive into the next launch
      # new: owner T21 (2026-11-11)
      Given Alice has a project that has never used ctxloom
      And Alice has set ctxloom up in her project
      And Alice has started her assistant through ctxloom
      And her assistant loosens its own permissions in the settings it was handed
      When Alice takes another turn on that session with "and now?"
      Then the command succeeds
      And the assistant's settings hold the permissions '{"defaultMode": "acceptEdits"}'
      And the assistant's settings still register ctxloom's hooks
