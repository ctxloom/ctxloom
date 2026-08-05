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

  # Phase 2 of tagma adoption: taskloom's built-in tag_schema default (see
  # internal/taskloom/config.DefaultTagSchema) declares triage:kind
  # arity=scalar, with NO .taskloom/config.yaml required — this fresh store
  # never writes one. Re-tagging that key with a different value collapses
  # the task down to the newest value, but the on-disk log still records
  # BOTH the retracting untag and the new tag: history is never rewritten.
  Scenario: Re-tagging a declared-scalar key collapses to the newest value, preserving history
    When taskloom adds a task "Triage this" with tags "triage:kind=defect"
    And taskloom tags it adding "triage:kind=capability"
    Then the folded task has text "Triage this", status "To Do", and tags "triage:kind=capability"
    And the on-disk task log records an untag of "triage:kind=defect" and a tag of "triage:kind=capability" for it

  # Re-tagging with the IDENTICAL value never displaces anything, so no
  # collapsing untag is ever emitted for it.
  Scenario: Re-tagging a declared-scalar key with the same value emits no collapsing untag
    When taskloom adds a task "Triage that" with tags "triage:kind=defect"
    And taskloom tags it adding "triage:kind=defect"
    Then the folded task has text "Triage that", status "To Do", and tags "triage:kind=defect"
    And the on-disk task log records no untag of "triage:kind=defect" for it

  # A key the built-in schema does NOT declare scalar (or any key the
  # project never mentions at all) is never collapsed: every value
  # accumulates exactly like a plain flat tag always has.
  Scenario: An undeclared tag key keeps every value, uncollapsed
    When taskloom adds a task "Track CWEs" with tags "triage:cwe=79"
    And taskloom tags it adding "triage:cwe=89"
    Then the folded task has text "Track CWEs", status "To Do", and tags "triage:cwe=79,triage:cwe=89"

  # The tagma namespace is reserved for tag_schema declarations in
  # .taskloom/config.yaml only — a task tag must never be able to write into
  # it, so injecting one fails loud rather than silently persisting.
  Scenario: A task tag in the reserved tagma namespace is rejected
    When taskloom adds a task "Should fail" with tags "tagma.arity:x=y" expecting failure
    Then the taskloom command fails with a nonzero exit
    And the taskloom command's error output mentions "reserved"

  # The agent-instruction-surface journey: an agent that has never read
  # taskloom's source discovers the tag surface exists and how to use it
  # purely from what `taskloom mcp` advertises over the wire — the server
  # Instructions blob and the registered tools' own descriptions/schemas.
  #
  # WHAT IS ASSERTED, and why it is not the tool names. This scenario used to
  # check that the instructions mentioned the words "tags", "tag_query" and
  # "task_tag", and that task_tag's description mentioned "add" and "remove".
  # An audit replaced the entire instructions blob with the literal string
  # "tags tag_query task_tag", and task_tag's description with "Tag a task.
  # Fields: add, remove." — deleting the tag grammar, the resource pointers,
  # the query grammar and the scalar-collapse warning — and the scenario
  # stayed green. Naming a tool is not documenting it.
  #
  # So the assertions now name the things an agent could not act correctly
  # without: the SHAPE a tag takes, WHERE the project's real vocabulary lives,
  # HOW a tag filter is written, and the one behavior that silently destroys
  # data if unknown (a scalar tag's value being retracted rather than added
  # alongside). Each is content the mutation removed.
  Scenario: The MCP surface documents tags without prior knowledge
    Given the agent connects to the taskloom MCP server
    Then the server instructions mention "filter by tag with tag_query"
    And the server instructions mention "(namespace:)key(=value)"
    And the server instructions mention "not flat labels"
    And the server instructions mention "postfix boolean expression"
    And the server instructions mention "taskloom://tag-schema"
    And the server instructions mention "taskloom://tag-vocabulary"
    And the server instructions mention "scalar (at-most-one-value)"
    And the tool "task_list" input schema mentions "tag_query"
    And the tool "task_add" input schema mentions "tags"
    And the tool "task_tag" description mentions "Add and/or remove tags on a task"
    And the tool "task_tag" description mentions "(namespace:)key(=value)"
    And the tool "task_tag" description mentions "add is applied before remove"
    And the tool "task_tag" description mentions "SILENTLY RETRACTS"
    And the tool "task_tag" description mentions "taskloom://tag-vocabulary"
    And the tool "task_tag" input schema mentions "add"
    And the tool "task_tag" input schema mentions "remove"
