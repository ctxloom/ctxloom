Feature: taskloom's tag surface is real across the process boundary

  taskloom's tag-query grammar (RPN and/or/not), fold, and handler I/O are
  already covered at the unit/op/handler tiers — see pkg/tagquery,
  internal/shared/tasks, and internal/shared/tasks/operations. This journey
  does NOT re-prove any of that. It proves the seam those tiers cannot see:
  that the REAL `taskloom` binary, run as a separate OS process against the
  same on-disk store `ctxloom` and its MCP tools would use, actually behaves
  the way an agent (or a human at the CLI) experiences it — exact tag-query
  result sets, a malformed query failing loud rather than degrading silently,
  an append-only log across a real task lifecycle, and an MCP surface that
  documents tags without the caller needing prior knowledge of the grammar.

  Background:
    Given a fresh taskloom store

  # LOCKED — the core tag-query journey: and/or/not/implicit-AND each single
  # out the exact expected task set, and a malformed query (an operator
  # popping an empty stack) fails loud with a nonzero exit and an error
  # naming the problem, never a silently empty or unfiltered list.
  Scenario: Tag queries select exact task sets, and a malformed query fails loud
    Given taskloom has tasks:
      | name    | tags           |
      | alpha   | urgent,release |
      | bravo   | urgent         |
      | charlie | release        |
      | delta   |                |
    When taskloom lists tag query "urgent/release/and"
    Then the tag query result is exactly "alpha"
    When taskloom lists tag query "urgent/release/or"
    Then the tag query result is exactly "alpha,bravo,charlie"
    When taskloom lists tag query "urgent/not"
    Then the tag query result is exactly "charlie,delta"
    When taskloom lists tag query "urgent/release"
    Then the tag query result is exactly "alpha"
    When taskloom lists tag query "and" expecting failure
    Then the taskloom command fails with a nonzero exit
    And the taskloom command's error output mentions "operand"

  # LOCKED — the append-only journey: a full task lifecycle (add, tag, untag,
  # status, edit) proves the on-disk JSONL is append-only end to end — every
  # line written by an earlier operation survives byte-identical under every
  # later one, the file strictly grows by one line per operation, and the
  # folded state a fresh `list` process reports matches what the lifecycle
  # should have produced.
  Scenario: The on-disk task log is append-only across a full task lifecycle
    When taskloom adds a task "Ship the release" with tags "urgent,release"
    And taskloom tags it adding "blocked"
    And taskloom untags it removing "urgent"
    And taskloom sets its status to "In Progress"
    And taskloom edits its text to "Ship the v2 release"
    Then the on-disk task log grew by exactly one line after each operation
    And every previously written line is byte-identical across all snapshots
    And the folded task has text "Ship the v2 release", status "In Progress", and tags "blocked,release"
    And an independent re-fold reproduces the same task state

  # The agent-instruction-surface journey: an agent that has never read
  # taskloom's source discovers the tag surface exists and how to use it
  # purely from what `taskloom mcp` advertises over the wire — the server
  # Instructions blob and the registered tools' own descriptions/schemas.
  Scenario: The MCP surface documents tags without prior knowledge
    Given the agent connects to the taskloom MCP server
    Then the server instructions mention "tags"
    And the server instructions mention "tag_query"
    And the server instructions mention "task_tag"
    And the tool "task_list" input schema mentions "tag_query"
    And the tool "task_add" input schema mentions "tags"
    And the tool "task_tag" description mentions "add"
    And the tool "task_tag" description mentions "remove"
    And the tool "task_tag" input schema mentions "add"
    And the tool "task_tag" input schema mentions "remove"
