# world: base
# Ported from cli/init.feature (rev f36e8dfb2)
# NOT ported: the Rules tagged @pending: the first-init rule (it asks its
#   questions over a pty).
# Divergence from Go (owner rulings 110, 111): a first init seeds no remote and
#   pulls nothing; its default content is built in.
@doc
Feature: init — the setup interview, and what it does to a project that already has one

  Covers: `ctxloom init` and `ctxloom init prompt`.

  `init` is the command someone runs once. On a directory with no `.ctxloom` it
  scaffolds the project: its config, an empty remotes registry, and a default
  profile inheriting the developer profile ctxloom ships BUILT IN, so nothing
  is cloned or pulled and it works offline. Then — on a terminal — it hands
  the whole thing to your engine for one setup interview:
  companions, then profiles and content, then agents bound to them. It writes
  no engine file into the project: the interview, like every `ctxloom run`, is
  a session carrying ctxloom's hooks and MCP server in its own session home. It is a solo core verb
  with exactly one thing under it, `init prompt`, which re-emits that
  interview body for a shell or a script.

  After the interview ends cleanly, init offers to restart her into the configured
  session, as `[Y/n]`: a bare Enter accepts; n or no (any case) declines; a
  closed input (end of input, no one to answer) declines, as does anything else.

  WHAT THIS FILE COVERS, AND WHAT IT DELIBERATELY DOES NOT. A first-time init
  on a NEW `.ctxloom` reaches no network: the default content is built in
  (owner rulings 110, 111). The fresh-project scenarios here run off a
  terminal, so no question is asked and no session launched: they prove what
  the scaffold leaves and that its default content is there with nothing
  fetched. The pending fresh-project rule answers init's questions over a real
  pty with the setup session skipped (`--skip-launch`). The other scenarios
  run against a project that ALREADY has a `.ctxloom` directory, which takes
  init's already-exists branch: no questions, no scaffold. That branch is not
  a lesser case — it is what every re-run of `init` does, and it is where the
  flags that were once silently ignored live. The setup session itself is launched with a terminal stood in, against
  a stub engine, in j000200_setup.feature — on a new project, where init's one
  built question (where a session's outputs go, owner rulings 89, 106) is
  answered before the interview.

  The interactive interview needs a terminal too. Off one, init resolves the
  engine and returns without launching anything, which is precisely what makes
  these scenarios hermetic. Before that it checks for the token every agent
  authenticates with: the re-run scenarios export a fixture one, and the rule
  below covers what happens without it.

  Rule: Re-running init on an existing project changes nothing it did not ask to

    A second `init` is bookkeeping, not a rebuild. It says the directory is
    already there and leaves the project's configuration exactly as it found
    it.

    # THE CONFIG IS ASSERTED BY ITS BYTES, not by the exit code and not by the
    # message. `schema_version: 9` (Go 8) is the fixture's own content (it tracks
    # config.CurrentConfigVersion — bump both together) and `engine:` is a key
    # only the scaffold writes — so the pair says "your config survived" AND
    # "no scaffold ran over it", which a file-exists check cannot distinguish.
    #
    # The lockfile is no network assertion any more (a first init pulls
    # nothing, owner rulings 110, 111); it still says nothing was written
    # beside the config.
    Scenario: A second init reports the directory and leaves the configuration alone
      # substitution: the stamp asserted is this port's, 9 (a companion is registered with the command that runs it), where Go's is 8.
      Given an initialized ctxloom project
      And the file ".ctxloom/config.yaml" contains "schema_version: 9"
      And the environment variable "CLAUDE_CODE_OAUTH_TOKEN" is set to "sk-ant-oat01-acceptance-fixture-not-real"
      When Alice runs setup again on a project that already has it:
        """
        ctxloom init
        """
      Then the command succeeds
      And the output contains "ctxloom directory already exists"
      And the output does not contain "Seeded remote"
      And the file ".ctxloom/config.yaml" contains "schema_version: 9"
      And the file ".ctxloom/config.yaml" does not contain "engine:"
      And the file ".ctxloom/lock.yaml" does not exist

    # A REGRESSION WORTH ITS OWN SCENARIO. `--remote` and `--forge` used to be
    # read and then thrown away on this branch: the only consumer of them lived
    # inside the fresh-init arm, so `ctxloom init --remote <repo>` against an
    # existing `.ctxloom` printed "already exists", exited 0, and added zero
    # remotes. Nothing about that invocation looked like it had failed.
    #
    # Registering a remote records it in `.ctxloom/remotes.yaml` and tries a
    # clone, whose failure only warns (see cli/remote.feature), so this stays
    # offline; the registry file is the effect, and the listing is read back
    # because a write that landed in the wrong place would satisfy a
    # stdout-only assertion.
    Scenario: A personal remote named on the command line is registered, not discarded
      Given an initialized ctxloom project
      And the environment variable "CLAUDE_CODE_OAUTH_TOKEN" is set to "sk-ant-oat01-acceptance-fixture-not-real"
      When Alice adds her own content repository while re-running setup:
        """
        ctxloom init --remote file:///tmp/acceptance-remote.git --forge git
        """
      Then the command succeeds
      And the output contains "Added remote"
      And the file ".ctxloom/remotes.yaml" contains "personal"
      And the file ".ctxloom/remotes.yaml" contains "acceptance-remote"
      When I run "ctxloom remote list"
      Then the command succeeds
      And the output contains "personal"

  Rule: A first init's default content is built in, and nothing is fetched for it

    The bundles the seeded default profile inherits (`ai-developer` and the
    `ai-coding-general`, `testing` and `git` bundles its developer profile
    names) ship in the binary, under the read-only BUILT-IN origin
    `ctxloom/default` (owner rulings 110, 111). A first init seeds no remote,
    clones nothing, pulls nothing and writes nothing under `.ctxloom/cache`;
    the project's catalog lists the built-in bundles beside its own, and a
    run's plan resolves the seeded profile with nothing missing. The
    `ctxloom-default` repository stays an ordinary remote for whoever wants
    its other content.

    Scenario: A first init leaves a project whose default content is built in
      # new: owner rulings 110, 111 (hand-test defects 1 and 2: a fresh project's plan had its default content missing).
      Given an empty project directory
      And the environment variable "CLAUDE_CODE_OAUTH_TOKEN" is set to "sk-ant-oat01-acceptance-fixture-not-real"
      When Alice sets ctxloom up on a project, off a terminal:
        """
        ctxloom init
        """
      Then the command succeeds
      And the output does not contain "Pulled"
      And the file ".ctxloom/remotes.yaml" does not contain "ctxloom-default"
      And the file ".ctxloom/content/bundles/v2/project/profiles/default.yaml" contains "  - ai-developer#profiles/developer"
      And the file ".ctxloom/cache" does not exist
      And the file ".ctxloom/lock.yaml" does not exist
      When I run "ctxloom bundle list --format text"
      Then the command succeeds
      And the output contains "ai-developer (v1.0.0) [built-in ctxloom/default]"
      And the output contains "testing (v1.0.0) [built-in ctxloom/default]"
      When I run "ctxloom run --dry-run --format json hello"
      Then the command succeeds
      And the output contains "ctxloom+local:testing#fragments/tdd"
      And the output does not contain "not installed"
      And the file ".ctxloom/cache" does not exist

    # Read-only, and the refusal says how to change one: a project bundle of
    # its name overrides it whole, and an export is how to make one from it.
    Scenario: A built-in bundle refuses a write, and a project bundle of its name overrides it
      # new: owner rulings 110, 111.
      Given an initialized ctxloom project
      When Alice adds a fragment to a bundle ctxloom ships:
        """
        ctxloom fragment create testing house-rules
        """
      Then the command fails
      And the output contains "bundle \"testing\" is built into ctxloom (ctxloom/default) and is read-only"
      And the output contains "copy it into the project and change that copy: ctxloom bundle export testing "
      And the file ".ctxloom/content/bundles/v2/testing" does not exist
      When I run "ctxloom bundle export testing .ctxloom/content/bundles/v2"
      Then the command succeeds
      When I run "ctxloom fragment create testing house-rules"
      Then the command succeeds
      And the file ".ctxloom/content/bundles/v2/testing/fragments/house-rules.md" exists
      And the file ".ctxloom/content/bundles/v2/testing/fragments/tdd.md" exists
      When I run "ctxloom bundle list --format text"
      Then the command succeeds
      And the output does not contain "testing (v1.0.0) [built-in"
      And the output contains "git (v1.0.0) [built-in ctxloom/default]"

  Rule: Init never takes the agent token; without one it says how to create and export it, and carries on

    Every agent ctxloom launches authenticates with a long-lived token the
    human creates with the engine's own flow (claude: `claude setup-token`)
    and exports. ctxloom instructs; it never captures or stores the token.
    With none exported, init says so as a NOTICE and goes on: a `fix:` line in
    text, a notice in json, naming the steps in the engine's own wording, the
    one `ctxloom auth` and `ctxloom run` show. It never blocks, never asks for
    the token and never fails: init exits 0, off a terminal and on one, and
    the config is written whatever the token. Nobody is prompted off a
    terminal. (Owner ruling 2026-11-08, credentials: init guides her toward
    the token; the run is what refuses without one.)

    Scenario: Off a terminal with no token exported, init names how to create and export it and exits 0
      # substitution: Go exits 1 here; the owner's credentials ruling (2026-11-08) makes it a notice and exit 0.
      Given an initialized ctxloom project
      And the environment variable "CLAUDE_CODE_OAUTH_TOKEN" is set to ""
      When Alice re-runs setup with no agent token exported:
        """
        ctxloom init
        """
      Then the command succeeds
      And the output contains "claude setup-token"
      And the output contains "export CLAUDE_CODE_OAUTH_TOKEN"
      And the output contains "or store it in your secret manager"
      And the file ".ctxloom/config.yaml" exists

    # A credentials file serves the human's own primary session alone, so an
    # engine her agents use whose source is a file and no variable can
    # authenticate none of her child agents: init warns, in the words a
    # child's run of it is refused with, naming the variables to put in the
    # source and export, and carries on.
    Scenario: Init warns when the agents' engine authenticates with a file alone
      # new: owner ruling 64 (2026-11-10).
      Given an initialized ctxloom project
      And the environment variable "CLAUDE_CODE_OAUTH_TOKEN" is set to ""
      And the home already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        engines:
          claude-code:
            auth:
              file: ~/.claude/.credentials.json
        """
      When I run "ctxloom init"
      Then the command succeeds
      And the output contains "engines.claude-code.auth names only a file"
      And the output contains "CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_API_KEY"
      And the output contains "name one of those variables in engines.claude-code.auth.env and export it"

  Rule: A mistyped subcommand must not be mistaken for a bare init

    `init` is RUNNABLE as well as a namespace, and that combination made it the
    most destructive instance of the silent-namespace defect in the whole CLI.
    `ctxloom init prmopt` did not print help: it took the bare-init path,
    scaffolded `.ctxloom`, seeded remotes and cloned them, then exited 0 having
    ignored the argument entirely. Someone who mistyped one letter got a
    project set up around them and no indication that was not what they asked
    for.

    # The absence assertions are the point, so the same fixture proves it CAN
    # produce those files: `config create` scaffolds them right after, on the
    # identical empty directory. Without that, "no config.yaml" would be
    # equally consistent with the guard working and with the fixture never
    # being able to make one.
    Scenario: A mistyped subcommand fails and scaffolds nothing
      Given an empty project directory
      When Alice mistypes the one subcommand init has:
        """
        ctxloom init prmopt
        """
      Then the command fails
      And the output contains "unknown command"
      And the output contains "prmopt"
      And the file ".ctxloom/config.yaml" does not exist
      And the file ".ctxloom/remotes.yaml" does not exist
      When I run "ctxloom config create"
      Then the command succeeds
      And the file ".ctxloom/config.yaml" exists
      And the file ".ctxloom/remotes.yaml" exists

  Rule: The interview body is available on its own, for a shell or a script

    `init prompt` emits the same five-phase setup body `ctxloom init` hands the
    engine at bootstrap and `/ctxloom-init` loads in any ordinary session. It
    is a re-entry pointer, not a second copy: one body, reachable three ways,
    so they can never drift. Skipped the interview, or want to reconfigure? It
    is how you get back into it without re-running setup.

    # PHASE HEADINGS, NOT A WORD. Asserting a single token like "SCAN" is
    # satisfied by any fragment of the body that happens to survive a truncated
    # or partially-composed emit — and this command's failure mode is emitting
    # a prompt that is real but incomplete. Naming headings from opposite ends
    # of the body is what says the whole thing arrived.
    Scenario: Init prompt emits the whole setup interview body
      Given an initialized ctxloom project
      When Alice asks for the setup interview without launching one:
        """
        ctxloom init prompt
        """
      Then the command succeeds
      And the output contains "Phase 1 — Open, orient, scan"
      And the output contains "Phase 2 — Companions"
      And the output contains "Phase 4 — Agents"
      And the output contains "Phase 5 — Close"

  @pending
  Rule: A first init asks its questions at the terminal, and says what it chose for the rest

    Init's questions are asked in-process, on the user's own terminal, before
    any engine is involved. Two things are not asked: init says, in one line,
    that a delegation commits uncommitted work (the parent's off-branch before
    a child's worktree is carved from it, reaching the parent's branch only
    once the child has started; the child's on its own branch when its run
    ends), and,
    in another, which headless posture the default agent takes and how to
    change it. The project it leaves has a default agent whose headless runs
    accept file edits without asking.

    Init asks where a session's outputs go (`output.dir`), suggesting
    `ctxloom/` in the project and saying it is not gitignored, so what a
    session leaves there can be committed; a bare Enter takes the suggestion.
    This one question is BUILT (owner rulings 89, 106; j000200 scenario 2 and
    setup-cli's tests hold it); the rest of this Rule is not.

    Init also offers to add companions, by name. It seeds the offer with the
    companions ctxloom ships whose programs are on her machine, each one she
    can decline, and she can name others. Init looks for nothing beyond the
    companions it ships. Each accepted or named program is checked and
    registered in her home (`~/.ctxloom`), exactly as `companion add` would;
    a name whose program is not on her machine is refused, saying so, and the
    others are still added.

    # Hermetic: a stub `claude` makes claude-code the only installed engine
    # (so init announces it rather than asking), the https clone of the seeded
    # remote is refused, and --skip-launch stops before the auth probe and
    # the setup session. The stub records any execution, which is how "no
    # engine ran" is proven rather than assumed.
    Scenario: A first init writes the answers given at the terminal
      Given an empty project directory
      And claude-code is the only engine installed, and nothing reaches the network
      When Alice answers init's questions at a terminal, taking every recommendation
      Then init told her which engine it chose, "Using claude-code (only available engine)"
      And no engine ran during the interview
      And the file ".ctxloom/config.yaml" contains "type: claude-code"
      And the default agent's headless posture is "acceptEdits"
      And init's terminal output says "Delegation commits: before a delegated agent's worktree is carved"
      And init's terminal output says "ctxloom agent edit default --permissions <posture>"

    Scenario: A first init adds the companions she names, and refuses one she does not have
      # new: init offers to add companions by name.
      Given an empty project directory
      And claude-code is the only engine installed, and nothing reaches the network
      And the "ltk" program is on Alice's machine
      And the "reprise" program is not on Alice's machine
      When Alice runs "ctxloom init" at a terminal, answering:
        | question                | answer      |
        | Add companions by name? | ltk reprise |
      Then init told her it added the companion "ltk"
      And init told her it could not add "reprise" because its program is not on her machine
      And the companion "ltk" is registered in her home
      And the companion "reprise" is not registered
      And nothing in her project outside .ctxloom was written

    Scenario: A first init offers the shipped companions she has, and adds only those she accepts
      # new: init seeds its companion offer with ctxloom's shipped companions.
      Given an empty project directory
      And claude-code is the only engine installed, and nothing reaches the network
      And the shipped companions "ltk" and "reprise" are on Alice's machine
      When Alice runs "ctxloom init" at a terminal, answering:
        | question                   | answer |
        | Add the ltk companion?     | yes    |
        | Add the reprise companion? | no     |
      Then init offered her the companions "ltk" and "reprise"
      And the companion "ltk" is registered in her home
      And the companion "reprise" is not registered

    Scenario: A shipped companion whose program she does not have is not offered
      # new: init offers only the shipped companions on her machine.
      Given an empty project directory
      And claude-code is the only engine installed, and nothing reaches the network
      And the shipped companion "ltk" is on Alice's machine
      And the shipped companion "reprise" is not on Alice's machine
      When Alice runs init at a terminal, taking every recommendation:
        """
        ctxloom init
        """
      Then init offered her the companion "ltk"
      And init did not offer her the companion "reprise"
