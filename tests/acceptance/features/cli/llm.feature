@doc
Feature: llm — the named engine configurations, and the credentials they hold

  Covers: `ctxloom llm list`, `llm create`, `llm edit`, `llm remove`,
  `llm default`, and the bare `ctxloom llm` form.

  An LLM entry is a LABEL for an engine configuration: a backend type
  (claude-code, codex, kiro, ...), a model string, a launch-time permission
  posture, and — the part that makes this noun different from every other one
  in the CLI — an `env` block holding API credentials. Agents reference these
  labels by name (`ctxloom agent create dev --llm big`), so this is the
  vocabulary cli/agent.feature draws on: a team manages it here, once, instead
  of every binding restating a model string.

  CREDENTIALS ARE WITHHELD, AND THAT IS A SECURITY POSTURE, NOT A NICETY.
  Three separate rules make it up, and each is exercised below:

    1. A secret never travels through ARGV. There is no --env flag; `--env-file`
       (a path, or "-" for stdin) is the only way to set an env block, because
       an argv value reaches shell history, the process table, and any CI log
       that captures a command line.
    2. A secret never lands in a COMMITTED file. `llm.configs.*.env` is
       machine-scoped: the values go to ~/.ctxloom/config.yaml, never the
       project's .ctxloom/config.yaml, which somebody will push.
    3. A secret is never ECHOED. Every surface reports which KEYS are declared
       and never what they are set to.

  Which engine a given agent binds is the other noun — see cli/agent.feature.

  This is the comprehensive per-noun spec: what the noun DOES, leaf by leaf,
  hermetically against a seeded project.

  Rule: The vocabulary is enumerable before anything is configured

    A project that has declared no engine config still has engines — the
    registered backends. `llm list` answers with the union of those and any
    labels config.yaml declares, which is exactly the set `agent create
    --engine` and `llm default` accept, so a name offered here is never
    rejected there.

    # ONE BODY, ONCE PER ENCODING. Off a terminal ctxloom derives JSON, so the
    # no-flag row exercises the DEFAULT every script receives; the explicit
    # rows prove a typed --format wins in both directions. The structured
    # rows address each backend BY PATH rather than by a substring that would
    # also match across field boundaries.
    Scenario Outline: The listing names the built-in backends and marks the default
      Given an initialized ctxloom project
      When Alice asks which engines she can bind:
        """
        ctxloom llm list <flags>
        """
      Then the command succeeds
      And the output reports "[label=claude-code].default" as "<claude-code is the default>"
      And the output reports "[label=codex].label" as "<names codex too>"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | claude-code is the default | names codex too |
        |               | true                        | codex            |
        | --format json | true                        | codex            |
        | --format text | (default)                   | codex            |

    # The bare noun answers the question somebody typing it has, rather than
    # teaching them what they could have typed instead.
    Scenario: Bare llm lists the engines
      Given an initialized ctxloom project
      When I run "ctxloom llm"
      Then the command succeeds
      And the output contains "claude-code"
      And the output does not contain "Available Commands:"

  Rule: Every row says whether the team wrote it or ctxloom supplied it

    That union is two very different kinds of thing. A LABEL is something a
    team authored in config.yaml and maintains; a BUILT-IN is a bare engine
    name ctxloom fills in for a project that configured none. Printed
    identically they read as one list of decisions somebody made, and acting on
    that misreading is exactly how `llm remove claude-code` came to report
    success on a project that had never written an llm.configs line. So the
    listing marks BOTH kinds — not one, leaving the other bare, which a reader
    would take for missing information rather than an answer.

    Scenario Outline: A label the team wrote is told apart from an engine ctxloom supplied
      Given an initialized ctxloom project
      And I run "ctxloom llm create big --type codex --model o1"
      And I run "ctxloom llm default codex"
      When Alice asks which of these engines her team actually configured:
        """
        ctxloom llm list <flags>
        """
      Then the command succeeds
      # One listing, both kinds. Asserting either marker alone would pass just
      # as happily against a render that stamped every row the same way.
      And the output reports "[label=big].authored" as "<big is configured>"
      And the output reports "[label=claude-code].authored" as "<claude-code is built-in>"
      # The origin marker does not displace the default marker: a row can be
      # the fallback engine everything resolves to and still be one nobody
      # configured.
      And the output reports "[label=codex].default" as "<codex is the default>"
      And the output reports "[label=codex].authored" as "<and still built-in>"
      # The marker is not decoration — it predicts what the label can DO. A
      # name ctxloom supplied has no entry to take away, and remove says so
      # rather than reporting a deletion that never happened.
      When I run "ctxloom llm remove claude-code"
      Then the command fails
      And the output contains "not defined in config.yaml"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | big is configured | claude-code is built-in | codex is the default | and still built-in |
        |               | true               | false                    | true                  | false                |
        | --format json | true               | false                    | true                  | false                |
        | --format text | [configured]       | [built-in]               | (default)             | [built-in]           |

  Rule: The default is a label, and only a label the project knows

    Asserting only the exit code once let a show path that returned an EMPTY
    default pass. What a project with no llm block resolves to is a fact worth
    stating, and it is the effect the command exists to report.

    Scenario: A project with no llm block still reports the default it resolves to
      Given an initialized ctxloom project
      When I run "ctxloom llm default"
      Then the command succeeds
      And the output contains "claude-code"

    # Read back through the OTHER surface on purpose: `llm default` reporting
    # what `llm default` just set proves the two calls agree with each other,
    # not that anything was written. The listing's default marker is derived
    # from stored config, so it can only move if the write landed.
    Scenario Outline: Setting the default moves the marker in the listing
      Given an initialized ctxloom project
      When Alice makes codex the engine everything falls back to:
        """
        ctxloom llm default codex <flags>
        """
      Then the command succeeds
      And the output reports "status" as "<confirms the change>"
      When I run "ctxloom llm default <flags>"
      Then the output reports "default" as "<reads back codex>"
      When I run "ctxloom llm list <flags>"
      Then the output reports "[label=codex].default" as "<codex is now marked default>"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | confirms the change       | reads back codex | codex is now marked default |
        |               | set                        | codex             | true                          |
        | --format json | set                        | codex             | true                          |
        | --format text | Default LLM set to: codex | codex             | codex (default)               |

    # A default nobody can resolve is a project that fails at run time with no
    # clue why. The refusal names the set it would have accepted, which is the
    # same set `llm list` prints.
    Scenario: An unknown label is refused, and the old default stands
      Given an initialized ctxloom project
      When I run "ctxloom llm default nosuchengine"
      Then the command fails
      And the output contains "unknown LLM"
      And the output contains "claude-code"
      When I run "ctxloom llm default"
      Then the command succeeds
      And the output contains "claude-code"

  Rule: create and edit are NOT an upsert, and edit merges per field

    The same split `agent` has, for the same reason and against the same
    shared body: `create` refuses a label that already resolves, `edit` refuses
    one that does not. "Already resolves" is the MERGED view — a registered
    backend name counts, so `llm create claude-code` is refused (it would
    shadow the built-in) while `llm edit claude-code` is accepted (that is how
    a built-in becomes an explicit entry).

    Scenario Outline: A labeled engine config is created and read back out of the project file
      Given an initialized ctxloom project
      When Alice names an engine configuration her team can bind by label:
        """
        ctxloom llm create big --type codex --model o1 <flags>
        """
      Then the command succeeds
      And the output reports "type" as "<names the backend>"
      And the output reports "model" as "<names the model>"
      # The EFFECT: the confirmation is rendered from the request and would
      # print identically over a save that never happened.
      And the file ".ctxloom/config.yaml" contains "big"
      And the file ".ctxloom/config.yaml" contains "o1"
      When I run "ctxloom llm list <flags>"
      Then the output reports "[label=big].label" as "<the label round-trips>"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | names the backend | names the model | the label round-trips |
        |               | codex              | o1               | big                    |
        | --format json | codex              | o1               | big                    |
        | --format text | codex              | o1               | big                    |

    Scenario: Create refuses a label that already resolves, including a built-in backend
      Given an initialized ctxloom project
      And I run "ctxloom llm create big --type codex --model o1"
      When I run "ctxloom llm create big --type codex"
      Then the command fails
      And the output contains "already exists"
      And the output contains "ctxloom llm edit big"
      # The built-in half of "already resolves": shadowing claude-code with a
      # config entry that answers to the same name is the ambiguity the guard
      # exists to refuse.
      When I run "ctxloom llm create claude-code --type codex"
      Then the command fails
      And the output contains "already exists"
      # Neither refusal wrote: the original entry is intact.
      When I run "ctxloom llm list"
      Then the output contains "big"
      And the file ".ctxloom/config.yaml" contains "o1"

    Scenario: Edit refuses a label nothing defines, and creates nothing
      Given an initialized ctxloom project
      When I run "ctxloom llm edit nosuchlabel --model o1-pro"
      Then the command fails
      And the output contains "no llm named"
      And the output contains "ctxloom llm create nosuchlabel"
      When I run "ctxloom llm list"
      Then the command succeeds
      And the output does not contain "nosuchlabel"

    # Every field the invocation did not name survives. A merge that applied
    # the model and reset the type would leave a config pointing the new model
    # at the wrong backend — and would satisfy a model-only assertion.
    Scenario Outline: An edit naming only --model leaves the backend type intact
      Given an initialized ctxloom project
      And I run "ctxloom llm create big --type codex --model o1"
      When Alice moves one label onto a newer model:
        """
        ctxloom llm edit big --model o1-pro <flags>
        """
      Then the command succeeds
      And the output reports "model" as "<the new model>"
      And the output reports "type" as "<the backend survives unmerged>"
      And the file ".ctxloom/config.yaml" contains "o1-pro"
      And the file ".ctxloom/config.yaml" contains "codex"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | the new model | the backend survives unmerged |
        |               | o1-pro         | codex                          |
        | --format json | o1-pro         | codex                          |
        | --format text | o1-pro         | codex                          |

    # --permissions is the launch-time posture, a project-file field distinct
    # from the credential rule below (which is about the machine-scoped env
    # block). Read back out of the project config, not the echo.
    Scenario Outline: Setting the permission posture records it in the project config
      Given an initialized ctxloom project
      And I run "ctxloom llm create big --type codex --model o1"
      When Alice sets the posture this label launches with:
        """
        ctxloom llm edit big --permissions bypass <flags>
        """
      Then the command succeeds
      And the output reports "permissions" as "<the posture>"
      And the file ".ctxloom/config.yaml" contains "permissions: bypass"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | the posture |
        |               | bypass       |
        | --format json | bypass       |
        | --format text | bypass       |

  Rule: Removing reports before it destroys, and never reaches the credential

    A bare `remove` prints what would go and removes nothing, naming the
    invocation that would — the posture every destroyer in this CLI shares.
    `--yes` takes the entry out of the PROJECT file only: the credential
    recorded for that label lives in the per-machine config, may still be in
    use by another project binding the same label, and is not this command's
    to delete.

    Scenario Outline: Bare remove reports and destroys nothing
      Given an initialized ctxloom project
      And I run "ctxloom llm create big --type codex --model o1"
      When I run "ctxloom llm remove big <flags>"
      Then the command succeeds
      And the output reports "applied" as "<nothing was applied>"
      And the output reports "apply" as "<the command to actually do it>"
      When I run "ctxloom llm list <flags>"
      Then the output reports "[label=big].label" as "<the entry survives>"
      And the file ".ctxloom/config.yaml" contains "big"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | nothing was applied  | the command to actually do it | the entry survives |
        |               | false                 | ctxloom llm remove big --yes  | big                  |
        | --format json | false                 | ctxloom llm remove big --yes  | big                  |
        | --format text | Nothing was removed   | ctxloom llm remove big --yes  | big                  |

    Scenario Outline: --yes takes the entry out of the project file
      Given an initialized ctxloom project
      And I run "ctxloom llm create big --type codex --model o1"
      And the file ".ctxloom/config.yaml" contains "big"
      When Alice retires a label her team no longer binds:
        """
        ctxloom llm remove big --yes <flags>
        """
      Then the command succeeds
      And the output reports "status" as "<confirms the removal>"
      And the file ".ctxloom/config.yaml" does not contain "big"
      When I run "ctxloom llm list <flags>"
      Then the output does not contain "big"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | confirms the removal |
        |               | removed                |
        | --format json | removed                |
        | --format text | Removed llm            |

    # A built-in backend is not an entry in config.yaml, so there is nothing
    # here to remove — and saying "removed" would be the silent no-op this
    # project is named for.
    Scenario: Removing a label config.yaml never declared is refused
      Given an initialized ctxloom project
      When I run "ctxloom llm remove claude-code"
      Then the command fails
      And the output contains "not defined in config.yaml"
      When I run "ctxloom llm list"
      Then the output contains "claude-code"

  Rule: CREDENTIALS ARE WITHHELD — presence is reported, the value never is

    THE SCENARIOS THIS WHOLE FILE IS FOR. A withholding assertion is the
    easiest kind in the world to write vacuously: "the output does not contain
    the secret" passes just as happily against a command that never read the
    file, never parsed a key, and stored nothing at all. So each scenario below
    proves the POSITIVE FIRST, twice over — the key NAME is echoed (the file
    really was read and parsed) and the VALUE is on disk in the per-machine
    config (it really was stored) — and only then asserts the absences. With
    both positives in place, a secret missing from the output can only mean it
    was withheld.

    Scenario: A credential set from a file is stored, its key reported, and its value withheld everywhere
      Given an initialized ctxloom project
      And the project already has the file "openai.env":
        """
        OPENAI_API_KEY=sk-acceptance-MUST-NEVER-BE-PRINTED
        """
      When Alice records the credential her codex label needs:
        """
        ctxloom llm create big --type codex --env-file openai.env
        """
      Then the command succeeds
      # POSITIVE 1 — the env file was really read and its key parsed out.
      And the output contains "OPENAI_API_KEY"
      # POSITIVE 2 — the value was really stored, in the per-machine config.
      And the home file ".ctxloom/config.yaml" contains "sk-acceptance-MUST-NEVER-BE-PRINTED"
      # ...so these absences are load-bearing rather than vacuous.
      And the output does not contain "sk-acceptance-MUST-NEVER-BE-PRINTED"
      And the file ".ctxloom/config.yaml" does not contain "sk-acceptance-MUST-NEVER-BE-PRINTED"
      When I run "ctxloom llm list"
      Then the command succeeds
      And the output contains "big"
      And the output does not contain "sk-acceptance-MUST-NEVER-BE-PRINTED"

    # "-" IS THE POINT OF THE FLAG. A credential piped in never appears in
    # argv at all, so it cannot reach shell history, the process table, or a CI
    # log that records the command line — and the same invocation proves the
    # merge (the type and model it did not name survive) in one act.
    Scenario: A credential piped on stdin never touches argv, and the unnamed fields survive
      Given an initialized ctxloom project
      And I run "ctxloom llm create big --type codex --model o1"
      When I run "ctxloom llm edit big --env-file -" with input:
        """
        ANTHROPIC_API_KEY=sk-stdin-MUST-NEVER-BE-PRINTED
        """
      Then the command succeeds
      # POSITIVE 1 — stdin was really read and the key parsed out of it.
      And the output contains "ANTHROPIC_API_KEY"
      # POSITIVE 2 — the value was really stored, per-machine.
      And the home file ".ctxloom/config.yaml" contains "sk-stdin-MUST-NEVER-BE-PRINTED"
      # ...and now the withholding, plus the per-field merge in the same act.
      And the output does not contain "sk-stdin-MUST-NEVER-BE-PRINTED"
      And the output contains "codex"
      And the output contains "o1"
      And the file ".ctxloom/config.yaml" does not contain "sk-stdin-MUST-NEVER-BE-PRINTED"

    # There is no --env flag, and that absence is a design decision rather than
    # an omission: a flag that took a value would put the secret in argv, which
    # is the one place this rule exists to keep it out of.
    Scenario: There is no way to put a credential on the command line
      Given an initialized ctxloom project
      And I run "ctxloom llm create big --type codex --model o1"
      When I run "ctxloom llm edit big --env ANTHROPIC_API_KEY=sk-on-the-command-line"
      Then the command fails
      And the output contains "unknown flag"

    # The credential is MACHINE-scoped and outlives the project entry that
    # referenced it, because another project on this box may bind the same
    # label. Deleting it here would be a destroyer reaching past what it was
    # asked to destroy.
    Scenario: Removing the project entry leaves the per-machine credential alone
      Given an initialized ctxloom project
      And the project already has the file "openai.env":
        """
        OPENAI_API_KEY=sk-survives-the-removal
        """
      And I run "ctxloom llm create big --type codex --env-file openai.env"
      And the home file ".ctxloom/config.yaml" contains "sk-survives-the-removal"
      When Alice removes the label from her project:
        """
        ctxloom llm remove big --yes
        """
      Then the command succeeds
      And the file ".ctxloom/config.yaml" does not contain "big"
      And the home file ".ctxloom/config.yaml" contains "sk-survives-the-removal"
