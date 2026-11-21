# world: base
# Ported from cli/mcp_tools.feature (rev f36e8dfb2)
#   rewritten for the daemon's tools (owner, 2026-11-18, "Go-era MCP
#   specs") as DRAFT text, accepted under the standing ruling and reviewed
#   in full before release. Go's `assemble_context` is
#   `context_assemble` (S46), whose `bundles` argument loads the premised
#   fragments the agent chose from its premise index (D24).
# NOT ported: the approval hook scenario (`ctxloom hook permission`): an
#   engine's permission ask is routed through the approval prompt of
#   ctxloom's MCP server (cli/mcp_serve.feature, cli/session_approvals.feature);
#   the read of `ctxloom://fragments` in "Assemble context from a
#   fragment the session's package carries" (there are no MCP resources:
#   one gRPC call per tool, tools only, the roadmap's step 9), and its
#   refused `profile` ask (the tool's arguments are `bundles` and `tags`
#   alone, S46 and D24: no profile is an argument to refuse); "Search
#   content over MCP" and "Search the installable library degrades
#   gracefully with no remotes" (no search tool is among S46's: search is
#   the CLI's, `ctxloom search`); "A deferred task's trigger verdict reaches
#   the caller, attributed to it" and "With nothing deferred,
#   evaluate_triggers judges nothing" (no task tool is among S46's: Deferred
#   tasks are taskloom's, the 2026-11-06 feature analysis's "move to
#   taskloom", as stamp-plan was, D17).

Feature: MCP tools
  The agent drives ctxloom through the tools of ctxloom's MCP server, each a
  call to the user's daemon. This one loads the conditional guidance the
  agent chose from its premise index.

  Every tool here can answer successfully while delivering nothing, so the
  scenario names a marker that exists ONLY in its own fixture, and asserts
  that marker in the result. An envelope with the right fields is not
  evidence.

  # A fragment with a premise is held back from the launch and named in the
  # premise index; the agent loads the ones it chose by those names. What it
  # names is what it gets: the marker inside the chosen fragment's body is
  # in the context, and the marker of a fragment it did not name is not, so
  # an empty assembly, or one that loaded everything, cannot pass.
  Scenario: The agent loads the premised fragment it chose, and only that
    # substitution: Go's "Assemble context from a fragment the session's package carries": Go's `assemble_context` is `context_assemble` (S46), its `bundles` the premise index's names (D24), from a fixture's own markers
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And the project already has the file ".ctxloom/content/bundles/v2/demo/fragments/golang.md":
      """
      ---
      description: when editing Go code
      ---
      GO-RULES-ONLY-IN-THIS-FIXTURE
      """
    And the project already has the file ".ctxloom/content/bundles/v2/demo/fragments/release.md":
      """
      ---
      description: when cutting a release
      ---
      RELEASE-RULES-ONLY-IN-THIS-FIXTURE
      """
    And a session "amber-quiet-heron" that is still running
    When the engine of "amber-quiet-heron" calls "context_assemble" with the fragments "demo#fragments/golang"
    Then the tool answered with a context holding "GO-RULES-ONLY-IN-THIS-FIXTURE"
    And the assembled context does not hold "RELEASE-RULES-ONLY-IN-THIS-FIXTURE"
