# world: base
# NOT ported: nothing; new (P3, owner rulings 2026-11-25).

@doc
Feature: launch spec — what a plan decided is on the event that records it, so no launch reads another component's memory

  RECORD THE DECISION'S OUTPUTS (owner, 2026-11-25: "the events are what was
  actually decided by the aggregate; we don't even persist commands and
  facts"). A run's plan decides its model and effort, its session home, how
  long an ask of it waits, the command that runs its engine, the credential
  sources it names, the environment it gives the engine, the context it hands
  it, its opening prompt, its surfaces (hooks, statusline, denied tools,
  commands, skills, MCP servers), the git identity of its session home and
  the client's program. All of it is the `spec` of its `RunEnqueued`, and a
  launch acts on the record: nothing is handed to the launcher through the
  daemon's memory, a file, or a callback, so a process on another machine can
  launch what was enqueued.

  WHAT AN EARLIER EVENT SAID IS NOT SAID AGAIN. The engine and the workspace
  axis are the session's mint's, the agent the loadout's, the harp, the native
  session id, the project root and the output folder the session's.

  A SECRET RIDES A MESSAGE ONLY SEALED (angzarr-sealed): the environment's
  variable NAMES are on the event and each VALUE sealed to the daemon's key;
  an MCP server's headers and environment likewise. No event of any stream
  holds a credential in the clear.

  The record is read with the test build's `ctxloom test record` (every
  stream, each event's fields as JSON) and `ctxloom test library-events`.

  Rule: A run's enqueue records what its plan decided

    Scenario: A one-shot's enqueue records its context, its prompt, its settings and its environment by name
      # new: P3 item 1.
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      And a profile "dev" with bundle "demo"
      And Alice has exported her assistant's token
      And the mock LLM responds "MOCK-REPLY"
      When Alice asks a question with her project's context attached:
        """
        ctxloom run --one-shot --profile dev unicorn-prompt
        """
      Then the command succeeds
      And the record's RunEnqueued event says "spec.prompt" is "unicorn-prompt"
      And the record's RunEnqueued event says "spec.context" contains "FRAGMENT-BODY-testing"
      And the record's RunEnqueued event says "spec.session_home" is "session"
      And the record's RunEnqueued event names the environment variable "CLAUDE_CODE_OAUTH_TOKEN"
      And the record holds "sk-ant-oat01-acceptance-fixture-not-real" in no event, in the clear

    Scenario: A run with a profile records its grant and the context its profile loads
      # new: P3 items 1 and 4: the grant was left unset for the human's own session; the terminal session's own enqueue is the run-application unit test `a_terminal_sessions_enqueue_carries_its_plan_and_its_opening_prompt`.
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      And a profile "dev" with bundle "demo"
      And Alice has exported her assistant's token
      And the mock LLM responds "MOCK-REPLY"
      When Alice asks a question with her project's context attached:
        """
        ctxloom run --one-shot --profile dev "what does this project expect of me?"
        """
      Then the command succeeds
      And the record's RunEnqueued event says "grant.mcp_servers.0" is "ctxloom"
      And the record's RunEnqueued event says "spec.session_home" is "session"
      And the record's RunEnqueued event says "spec.context" contains "FRAGMENT-BODY-testing"

    Scenario: A hosted child's enqueue records its plan, and no hand-off file holds its launch inputs
      # new: P3 item 2: the hand-off file, the daemon's environment cache and its client programs are gone.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" that is open and idle
      Then the record's RunEnqueued event says "spec.session_home" is "session"
      And the record's RunEnqueued event names the environment variable "PATH"
      And no hand-off file is kept for "brisk-copper-moth"

  Rule: A turn's answer is a fact of its run's stream

    Scenario: A one-shot's reply is on its run's stream, where a caller that is gone can read it
      # new: P3 item 3: the answer rode in memory from the launcher to its caller.
      Given an initialized ctxloom project
      And the mock LLM responds "MOCK-REPLY"
      When I run "ctxloom run --one-shot unicorn-prompt"
      Then the command succeeds
      And the record's TurnCompletedRecorded event says "answer" is "MOCK-REPLY"

  Rule: The library's files are in its stream, once, and a secret inside one is sealed

    Scenario: A file's bytes are recorded when its content is first seen, and not again by a read that sees nothing new
      # new: P3 item 6, reversing "nothing holds a file's text" (owner, 2026-11-25).
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      When I run "ctxloom bundle list"
      And I run "ctxloom bundle list"
      Then the library's record holds the bytes of "fragments/testing.md", containing "FRAGMENT-BODY-testing"
      And the library's record holds the bytes of "fragments/testing.md" once

    Scenario: A change is recorded as the new bytes, beside the old
      # new: P3 item 6.
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      When I run "ctxloom bundle list"
      And the file ".ctxloom/content/bundles/v2/demo/fragments/testing.md" is changed by hand to:
        """
        EDITED-BY-HAND
        """
      And I run "ctxloom bundle list"
      Then the library's record holds the bytes of "fragments/testing.md", containing "EDITED-BY-HAND"

    Scenario: An MCP server's secret is sealed in the record, never dropped or refused
      # new: P3 item 6 (owner: secrets in recorded files are sealed).
      Given an initialized ctxloom project
      And a bundle "demo" exists
      When the file ".ctxloom/content/bundles/v2/demo/mcp/docs.yaml" is changed by hand to:
        """
        url: https://docs.example/mcp
        headers:
          Authorization: Bearer SECRET-HEADER-VALUE
        """
      And I run "ctxloom bundle list"
      Then the library's record holds "mcp/docs.yaml" with 1 sealed value and not "SECRET-HEADER-VALUE" in the clear

  Rule: The user's own engine settings are a stream of their own, read by a launch from the stream

    Scenario: Her claude settings are recorded when they change, secrets sealed, and a launch names the sequence it read
      # new: P3 item 7.
      Given an initialized ctxloom project
      And Alice has exported her assistant's token
      And Alice's claude settings, in her own home, say:
        """
        {"model": "opus", "env": {"MY_TOKEN": "SECRET-SETTINGS-VALUE"}}
        """
      And the mock LLM responds "MOCK-REPLY"
      When I run "ctxloom run --one-shot unicorn-prompt"
      Then the command succeeds
      And the record holds a "user-settings" stream whose event says "kind" is "CLAUDE_SETTINGS"
      And the record's RunEnqueued event names the user settings it read
      And the record holds "SECRET-SETTINGS-VALUE" in no event, in the clear

  Rule: A project's secrets are sealed with that project's own key, from its .env once it is trusted

    Background:
      # new: owner 2026-11-27 (threefold-canyon): one seal key per project, in its root `.env`.
      Given an initialized ctxloom project
      And Alice has exported her assistant's token
      And the mock LLM responds "MOCK-REPLY"

    Scenario: A project whose .env holds a key is refused, naming the project and its file, until it is trusted
      # new: the seal key is the project's own (owner 2026-11-27).
      Given the project already has the file ".env":
        """
        CTXLOOM_SEAL_KEY=0101010101010101010101010101010101010101010101010101010101010101
        """
      When I run "ctxloom run --one-shot unicorn-prompt"
      Then the command exits with code 48
      And the output contains "run-ports-sealing-no-key"
      And the output contains ".env"

    Scenario: A trusted project's launch is sealed with the key its .env holds
      # new: the seal key is the project's own (owner 2026-11-27).
      Given the project already has the file ".env":
        """
        CTXLOOM_SEAL_KEY=0101010101010101010101010101010101010101010101010101010101010101
        """
      And I run "ctxloom trust --yes"
      When I run "ctxloom run --one-shot unicorn-prompt"
      Then the command succeeds
      And the record holds "sk-ant-oat01-acceptance-fixture-not-real" in no event, in the clear

    Scenario: A key in the .env that is no key is refused, naming the project and its file, never the value
      # new: the seal key is the project's own (owner 2026-11-27).
      Given the project already has the file ".env":
        """
        CTXLOOM_SEAL_KEY=not-a-key-at-all
        """
      And I run "ctxloom trust --yes"
      When I run "ctxloom run --one-shot unicorn-prompt"
      Then the command exits with code 48
      And the output contains "run-ports-sealing-bad-key"
      And the output does not contain "not-a-key-at-all"
