# world: base
# Ported from cli/doctor.feature
# Ported from journeys/j001300_closeout.feature (rev f36e8dfb2): authored files in a session's directory, the last Rule, tagged @pending
# NOT ported: the Rule tagged @pending (a doctor check Rust lacks); every other rule is ported, the credentials rule
#   (DOCTOR-CHECK-CREDENTIALS-m5) included. Plan overrides: the trust-store
#   half of HOOKS-TRUST-d4 is dead since ADR 0037 and the marker is kept for
#   the hooks half; run-end and runner/MCP-state checks are S6's; the lockstep
#   check against the installed taskloom is unruled (D15/D3b).
@doc
Feature: doctor — the deterministic diagnosis, and the one finding that fails it

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

  READ THE REPORT; THE EXIT CODE ONLY SAYS WHETHER A RUN WOULD BE REFUSED.
  A `warn` is advisory and arrives with exit 0: a caller that branches on `$?`
  alone will call a project healthy while the report in front of it carries
  warnings, so most scenarios here assert the REPORT, and the ones that drive
  a genuinely broken project assert `the command succeeds` deliberately. A
  `fail` row is different: it is a finding that guarantees `ctxloom run`
  refuses (today, an agent credential no run can authenticate with), and it
  makes doctor exit 20, run's status for the refusal it guarantees, after the
  whole report is written. A USAGE error (a --format this build cannot render)
  exits 10.

  EVERY CHECK NEEDS BOTH HALVES. A check that fired on every project would be
  indistinguishable from one that works and would teach a reader to skip the
  line. So the two checks specified in detail below — the .gitignore posture
  and the MCP invocation — each get the state that makes them fire AND the
  state that keeps them quiet.

  Background:
    Given the environment variable "CLAUDE_CODE_OAUTH_TOKEN" is set to "sk-ant-oat01-acceptance-fixture-not-real"

  Rule: A credential no run can authenticate with is a fail row, and fails doctor

    Scenario: A missing agent credential is a fail row with run's fix, and exit 3
      # substitution: exit codes per rulings 149, 152, 153 (10 failed, 20 refused, 21 fatal findings; Go's 1, 2, 3); a fail row is run's refusal, 20.
      Given an initialized ctxloom project
      And the environment variable "CLAUDE_CODE_OAUTH_TOKEN" is set to ""
      When Alice asks for the diagnosis in a form a script can parse:
        """
        ctxloom --format json doctor
        """
      Then the command exits with code 20
      And the output is valid JSON
      And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-CREDENTIALS-m5" and whose "status" is "fail"
      And the output contains "claude setup-token"

    # A file alone serves her own primary session, so the row is a warning,
    # said in the words a child's run of it is refused with: one wording
    # everywhere (init, auth status, doctor, run).
    Scenario: A credential source that is a file alone is a warn row in the refusal's words
      # new: owner rulings 53 and 81 (2026-11-10).
      Given an initialized ctxloom project
      And the home already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        engines:
          claude-code:
            auth:
              file: ~/.claude/.credentials.json
        """
      When Alice asks for the diagnosis in a form a script can parse:
        """
        ctxloom --format json doctor
        """
      Then the command succeeds
      And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-CREDENTIALS-m5" and whose "status" is "warn"
      And the output contains "engines.claude-code.auth names only a file, which serves the primary interactive session alone"
      And the output contains "name one of those variables in engines.claude-code.auth.env and export it"

  Rule: The report names every check, in one shared marker vocabulary

    @json-default
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
    of whether anything has been configured: binaries on PATH and git
    identity. init's PRIME and the setup
    skill's phase 1 run in this mode, because a full report on a brand-new
    project is a wall of expected-missing state that would alarm a user at the
    very start of the setup about to configure it.

    # THE NARROWING IS THE CLAIM, so it is asserted by what must be ABSENT —
    # and the positive control runs first, in the same fixture, so the missing
    # agents line means "scoped out" rather than "this project has no agents
    # check at all".
    @json-default
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

    # The identity is the one git commits with in the project, asked of git in
    # her own environment (owner ruling 112's, which a session home is seeded
    # with): her HOME's configuration, not ctxloom's home's, which
    # CTXLOOM_HOME may put elsewhere.
    Scenario: The git identity is hers, read where git reads it, wherever ctxloom's home is
      # new: the owner's first use, under CTXLOOM_HOME, was told git had no
      # identity because doctor read <ctxloom home>/.gitconfig
      Given an initialized ctxloom project
      And the home already has the file ".gitconfig":
        """
        [user]
          name = Alice
          email = alice@example.com
        """
      And ctxloom's home is "ctxloom-home" in her home
      When I run "ctxloom doctor --deps --all --format text"
      Then the command succeeds
      And the output contains "git is on the PATH and has an identity"
      And the output does not contain "has no identity"

    Scenario: A .gitconfig in ctxloom's home is no identity of hers
      # new: the converse; git never reads ctxloom's home
      Given an initialized ctxloom project
      And ctxloom's home is "ctxloom-home" in her home
      And the home already has the file "ctxloom-home/.gitconfig":
        """
        [user]
          name = Nobody
          email = nobody@example.com
        """
      When I run "ctxloom doctor --deps --format text"
      Then the command succeeds
      And the output contains "git is there but has no identity"

  Rule: A finding is reported at full volume and still exits 0

    A blanket `.ctxloom` ignore rule predates version-controlled content moving
    INTO `.ctxloom/`. Left in place it silently un-tracks the project's own
    content: `git add` reports nothing, and a content repository publishes an
    empty tree while every consumer's bundle refs fail to resolve. Nothing else
    in the system can see it, which is exactly the kind of thing doctor is for.

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
      # substitution: every private-state pattern is listed, since a project that leaves any of them to git is now a finding of its own (hand-test 2026-11-10).
      Given an initialized ctxloom project
      And the project already has the file ".gitignore":
        """
        .ctxloom/cache/
        .ctxloom/sessions/
        .ctxloom/project-id
        .ctxloom/state/
        .ctxloom/*.lock
        """
      When I run "ctxloom --format json doctor"
      Then the command succeeds
      And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-GITIGNORE-f6" and whose "status" is "ok"

    Scenario: A blanket rule spelled as a glob is the same finding
      # new: `.ctxloom/*` hides the project's content as surely as `.ctxloom/`, and doctor names the command that replaces it.
      Given an initialized ctxloom project
      And the project already has the file ".gitignore":
        """
        .ctxloom/*
        """
      When I run "ctxloom doctor"
      Then the command succeeds
      And the output contains "ctxloom manage gitignore install"
      When I run "ctxloom --format json doctor"
      Then the command succeeds
      And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-GITIGNORE-f6" and whose "status" is "warn"

    # The other half of the posture: ctxloom's private state (session homes
    # holding an engine's login state, the cache, the project id) must be
    # out of git. The rules live in the nested `.ctxloom/.gitignore` init
    # writes; a project without them, and without the same rules in its root
    # `.gitignore`, shows its sessions to `git status`.
    Scenario: Private state git can see is reported as a warning
      # new: hand-test 2026-11-10 (doctor said ok while `git status` listed .ctxloom/sessions/).
      Given an initialized ctxloom project
      When I run "ctxloom --format json doctor"
      Then the command succeeds
      And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-GITIGNORE-f6" and whose "status" is "warn"
      And the output contains "/sessions"
      And the output contains "ctxloom manage gitignore install"
      When I run "ctxloom manage gitignore install"
      Then the command succeeds
      When I run "ctxloom --format json doctor"
      Then the command succeeds
      And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-GITIGNORE-f6" and whose "status" is "ok"

  Rule: A config every run refuses is a fail row, and fails doctor

    Doctor reads the config as a run does. A key the schema does not know is
    refused by every run, so doctor says so in the run's own words, as a
    `fail` row, and exits 20.

    Scenario: An unknown config key is a fail row in the run's words, and exit 20
      # new: hand-test 2026-11-10 (doctor said ".ctxloom and its config.yaml read" of a config every run refused).
      Given an initialized ctxloom project
      And the project already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        sync:
          auto_sync: true
        """
      When I run "ctxloom --format json doctor"
      Then the command exits with code 20
      And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-SETUP-MARKER-e5" and whose "status" is "fail"
      And the output contains "unknown key `sync.auto_sync`"
      And the output contains "remove or rename the key in config.yaml"

  Rule: The one broken state nothing else can see

    A settings file that LAUNCHES ctxloom as an MCP server is invisible
    everywhere else: the entry is PRESENT, so every wiring check reports it
    healthy, and the engine starts fine. ctxloom ships no stdio MCP server —
    its tools are served by the running session's endpoint, injected into the
    session's own registry at start — so what fails is silent: the client
    waits on a handshake that never arrives, the session comes up with none
    of ctxloom's tools, and nothing says why. Reading what the entry launches
    is the only way to tell a rendered endpoint from that one.

    Scenario: Doctor names a settings file whose ctxloom entry launches a server that does not exist
      Given an initialized ctxloom project
      And the project already has the file ".mcp.json":
        """
        {
          "mcpServers": {
            "ctxloom": {"command": "/usr/local/bin/ctxloom", "args": ["mcp", "serve"]}
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
      # Written by hand, so this machine holds no record of it: a release
      # could not remove it, and the remedy says to delete it instead.
      And the output contains "entry from"
      And the output contains "by hand"
      And the output does not contain "ctxloom materialize --release --surface mcp"

    # The paired negative: a report that warned on every project would be
    # indistinguishable from one that works, and would teach a user to skip the
    # line. A registry that names ctxloom's endpoint by URL — a session's own
    # rendering — launches nothing and is healthy.
    Scenario: Doctor stays quiet about a settings file that names the session's endpoint
      Given an initialized ctxloom project
      And the project already has the file ".mcp.json":
        """
        {
          "mcpServers": {
            "ctxloom": {"type": "http", "url": "http://127.0.0.1:4242/mcp", "headers": {"Authorization": "Bearer x"}}
          }
        }
        """
      When I run "ctxloom --format json doctor"
      Then the command succeeds
      And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-MCP-INVOCATION-g7" and whose "status" is "ok"

    # The user's ~/.claude.json is hers: read in her HOME, never in ctxloom's
    # home, which CTXLOOM_HOME may put elsewhere.
    Scenario: The user's own ~/.claude.json is read in her HOME, wherever ctxloom's home is
      # new: the two-homes defect of the owner's first use (doctor read the
      # user's files under CTXLOOM_HOME)
      Given an initialized ctxloom project
      And ctxloom's home is "ctxloom-home" in her home
      And the home already has the file ".claude.json":
        """
        {"mcpServers": {"ctxloom": {"command": "ctxloom", "args": ["mcp", "serve"]}}}
        """
      And the home already has the file "ctxloom-home/.claude.json":
        """
        {"mcpServers": {"ctxloom": {"command": "decoy-ctxloom", "args": ["mcp", "serve"]}}}
        """
      When I run "ctxloom --format json doctor"
      Then the command succeeds
      And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-MCP-INVOCATION-g7" and whose "status" is "warn"
      And the output contains "~/.claude.json"

    Scenario: A ~/.claude.json in ctxloom's home is no file of hers
      # new: the converse
      Given an initialized ctxloom project
      And ctxloom's home is "ctxloom-home" in her home
      And the home already has the file "ctxloom-home/.claude.json":
        """
        {"mcpServers": {"ctxloom": {"command": "ctxloom", "args": ["mcp", "serve"]}}}
        """
      When I run "ctxloom --format json doctor"
      Then the command succeeds
      And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-MCP-INVOCATION-g7" and whose "status" is "ok"

  Rule: A hook entry an older ctxloom left behind is found, and --fix removes only it

    `ctxloom materialize` (and its `--release`) takes out only the entries
    this machine's ctxloom recorded writing. An entry an older release wrote, or one committed with
    the file from another machine, stays, and the engine runs it at every
    session start, where it fails: this ctxloom has no such subcommand. The
    rule is general rather than a list of retired names: any entry running a
    `ctxloom hook` subcommand the binary does not have is ctxloom's own
    leftover, in the project's settings and in the user's.

    Owner ruling 162: Go ctxloom and this one coexist, and both write hook
    entries into the user's shared `~/.claude/settings.json`. The one form
    this ctxloom writes is `ctxloom hook <engine> execute <Event>` (the
    Rust-era entry); every other `ctxloom hook ...` entry is Go-era, and may
    be a LIVE hook of a Go ctxloom still installed (`ctxloom hook
    stamp-plan`). So the home config's `legacy.go` decides: `present` (the
    default) reports each Go-era entry as an `info` row, left alone, and
    `--fix` never touches it; `retired` makes a Go-era entry stale, and
    `--fix` removes it. A Rust-era entry is managed the same either way.
    These scenarios name Go-era entries stale, so they retire Go first.

    Scenario: Doctor names a stale ctxloom hook entry and --fix removes exactly it
      Given an initialized ctxloom project
      And the home already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        legacy:
          go: retired
        """
      And the project already has the file ".claude/settings.json":
        """
        {
          "permissions": {"allow": ["Bash(ls)"]},
          "hooks": {
            "SessionStart": [
              {"matcher": "", "hooks": [
                {"type": "command", "command": "'ctxloom' hook inject-context --part 1 --of 2 abc123"},
                {"type": "command", "command": "ctxloom hook claude-code execute SessionStart"}
              ]}
            ],
            "Stop": [
              {"hooks": [{"type": "command", "command": "my-tool hook inject-context"}]}
            ]
          }
        }
        """
      And the home already has the file ".claude/settings.json":
        """
        {"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "ctxloom hook inject-context abc123"}]}]}}
        """
      When Alice checks a project whose engine still runs a hook this ctxloom removed:
        """
        ctxloom doctor
        """
      Then the command succeeds
      And the output contains "DOCTOR-CHECK-STALE-HOOKS-n5"
      And the output contains "SessionStart runs `ctxloom hook inject-context`"
      And the output contains "ctxloom doctor --fix"
      When I run "ctxloom doctor --fix"
      Then the command succeeds
      And the file ".claude/settings.json" registers no SessionStart hook whose command contains "inject-context"
      And the file ".claude/settings.json" registers a SessionStart hook whose command contains "hook claude-code execute SessionStart"
      And the file ".claude/settings.json" contains "my-tool hook inject-context" exactly 1 times
      And the file ".claude/settings.json" contains "Bash(ls)" exactly 1 times
      And the home file ".claude/settings.json" does not contain "inject-context"
      When I run "ctxloom --format json doctor"
      Then the command succeeds
      And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-STALE-HOOKS-n5" and whose "status" is "ok"

    # The paired negative: a live subcommand, and a foreign program that merely
    # spells the same words, are not ctxloom's leftovers.
    Scenario: A live ctxloom hook and a foreign command keep the check quiet
      Given an initialized ctxloom project
      And the project already has the file ".claude/settings.json":
        """
        {"hooks": {"SessionStart": [{"hooks": [
          {"type": "command", "command": "ctxloom hook claude-code execute SessionStart"},
          {"type": "command", "command": "my-tool hook inject-context"}
        ]}]}}
        """
      When I run "ctxloom --format json doctor"
      Then the command succeeds
      And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-STALE-HOOKS-n5" and whose "status" is "ok"

    Scenario: The user's own ~/.claude/settings.json is read and fixed in her HOME, wherever ctxloom's home is
      # new: the two-homes defect of the owner's first use (doctor read and
      # fixed the user's files under CTXLOOM_HOME)
      Given an initialized ctxloom project
      And ctxloom's home is "ctxloom-home" in her home
      And the home already has the file "ctxloom-home/.ctxloom/config.yaml":
        """
        schema_version: 9
        legacy:
          go: retired
        """
      And the home already has the file ".claude/settings.json":
        """
        {"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "ctxloom hook inject-context abc123"}]}]}}
        """
      When I run "ctxloom --format json doctor"
      Then the command succeeds
      And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-STALE-HOOKS-n5" and whose "status" is "warn"
      When I run "ctxloom doctor --fix"
      Then the command succeeds
      And the home file ".claude/settings.json" does not contain "inject-context"

    Scenario: A ~/.claude/settings.json in ctxloom's home is no file of hers
      # new: the converse; --fix leaves it as it is
      Given an initialized ctxloom project
      And ctxloom's home is "ctxloom-home" in her home
      And the home already has the file "ctxloom-home/.claude/settings.json":
        """
        {"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "ctxloom hook inject-context abc123"}]}]}}
        """
      When I run "ctxloom --format json doctor"
      Then the command succeeds
      And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-STALE-HOOKS-n5" and whose "status" is "ok"
      When I run "ctxloom doctor --fix"
      Then the command succeeds
      And the home file "ctxloom-home/.claude/settings.json" contains "inject-context"

  Rule: A Go-era hook is Go ctxloom's while legacy.go says it is present

    # new: owner ruling 162. Go ctxloom's live hook in the user's shared
    # settings is not this ctxloom's leftover; deleting it breaks Go.
    Scenario: A Go-era hook entry is reported and --fix leaves it while Go ctxloom is present
      Given an initialized ctxloom project
      And the home already has the file ".claude/settings.json":
        """
        {"hooks": {
          "PostToolUse": [{"matcher": "Edit|Write", "hooks": [{"type": "command", "command": "ctxloom hook stamp-plan"}]}],
          "SessionStart": [{"hooks": [{"type": "command", "command": "ctxloom hook claude-code execute SessionStart"}]}]
        }}
        """
      When Alice checks her setup while Go ctxloom still runs beside this one:
        """
        ctxloom doctor --all
        """
      Then the command succeeds
      And the output contains "DOCTOR-CHECK-GO-ERA-HOOK-n6"
      And the output contains "Go-era hook, left alone"
      And the output contains "PostToolUse runs `ctxloom hook stamp-plan` (~/.claude/settings.json)"
      When I run "ctxloom --format json doctor"
      Then the command succeeds
      And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-GO-ERA-HOOK-n6" and whose "status" is "info"
      And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-STALE-HOOKS-n5" and whose "status" is "ok"
      When I run "ctxloom doctor --fix"
      Then the command succeeds
      And the home file ".claude/settings.json" contains "ctxloom hook stamp-plan"
      And the home file ".claude/settings.json" contains "ctxloom hook claude-code execute SessionStart"

    Scenario: Once Go ctxloom is retired, a Go-era hook entry is stale and --fix removes it
      Given an initialized ctxloom project
      And the home already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        legacy:
          go: retired
        """
      And the home already has the file ".claude/settings.json":
        """
        {"hooks": {
          "PostToolUse": [{"matcher": "Edit|Write", "hooks": [{"type": "command", "command": "ctxloom hook stamp-plan"}]}],
          "SessionStart": [{"hooks": [{"type": "command", "command": "ctxloom hook claude-code execute SessionStart"}]}]
        }}
        """
      When I run "ctxloom --format json doctor"
      Then the command succeeds
      And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-STALE-HOOKS-n5" and whose "status" is "warn"
      And the output does not contain "DOCTOR-CHECK-GO-ERA-HOOK-n6"
      When I run "ctxloom doctor --fix"
      Then the command succeeds
      And the home file ".claude/settings.json" does not contain "stamp-plan"
      And the home file ".claude/settings.json" contains "ctxloom hook claude-code execute SessionStart"

    Scenario Outline: A Rust-era hook entry is managed alike whatever legacy.go says
      Given an initialized ctxloom project
      And the home already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        legacy:
          go: <legacy>
        """
      And the home already has the file ".claude/settings.json":
        """
        {"hooks": {
          "SessionStart": [{"hooks": [{"type": "command", "command": "ctxloom hook claude-code execute SessionStart"}]}],
          "Stop": [{"hooks": [{"type": "command", "command": "ctxloom", "args": ["hook", "claude-code", "execute", "Stop"]}]}]
        }}
        """
      When I run "ctxloom --format json doctor"
      Then the command succeeds
      And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-STALE-HOOKS-n5" and whose "status" is "ok"
      And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-HOOKS-TRUST-d4" and whose "status" is "ok"
      And the output does not contain "DOCTOR-CHECK-GO-ERA-HOOK-n6"
      When I run "ctxloom doctor --fix"
      Then the command succeeds
      And the home file ".claude/settings.json" contains "ctxloom hook claude-code execute SessionStart"
      And the home file ".claude/settings.json" contains '"args": ["hook", "claude-code", "execute", "Stop"]'

      Examples:
        | legacy  |
        | present |
        | retired |

  Rule: legacy.go is a word of the home config alone

    Whether Go ctxloom still runs is a fact of this machine, not of a
    checked-out repository: a project's config that says it is refused (a
    project could otherwise retire Go and have `doctor --fix` delete the
    user's live Go hooks). Its values are words, never a boolean.

    # new: owner ruling 162
    Scenario: The home config says legacy.go, and a run reads it
      Given an initialized ctxloom project
      And the home already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        legacy:
          go: present
        """
      When I run "ctxloom engine list"
      Then the command succeeds

    Scenario: A project's config cannot say legacy.go
      Given an initialized ctxloom project
      And the project already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        legacy:
          go: retired
        """
      When I run "ctxloom engine list"
      Then the command exits with code 20
      And the output contains "legacy.go"
      And the output contains "home config"

    Scenario Outline: legacy.go takes present or retired, nothing else
      Given an initialized ctxloom project
      And the home already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        legacy:
          go: <value>
        """
      When I run "ctxloom engine list"
      Then the command exits with code 20
      And the output contains "legacy.go"
      And the output contains '"present", "retired"'

      Examples:
        | value |
        | true  |
        | yes   |
        | gone  |

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
    @json-default
    Scenario: The delivered-versus-ingested boundary is stated, not claimed as checked
      Given an initialized ctxloom project
      When I run "ctxloom --format json doctor"
      Then the command succeeds
      And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-INGESTION-q7" and whose "status" is "info"
      When I run "ctxloom doctor"
      Then the command succeeds
      And the output contains "a process ctxloom does not own"

  @pending
  Rule: Authored files in a session's directory are named, with where they belong

    A session's directory holds machine state only, row by row; readable
    outputs belong in the session's output dir. A file a person or an agent
    wrote at the directory's top level is classified by no row: on the host it
    sits where nobody looks, and a containerized agent writing there loses it
    when the container exits. Doctor warns about each one and names the output
    dir it belongs in.

    Scenario: Authored artifacts in the unclassified middle of a harp directory are flagged
      # substitution: the project Given is the scenario's own, not the journey's Background.
      Given an initialized ctxloom project
      And a finished session "amber-quiet-heron" carrying design notes nobody filed
      When I run "ctxloom doctor"
      Then the checks warn that the design notes sit in the harp directory's unclassified top level
