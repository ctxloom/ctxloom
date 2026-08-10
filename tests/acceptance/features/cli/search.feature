@doc
Feature: search — finding content by name, tag and type, here and upstream

  Covers: `ctxloom search`, in full. It is a SOLO VERB — there are no
  subcommands under it — and everything it does is expressed by one optional
  query argument and four flags: `--tag`, `--type`, `--local` and `--remote`.

  Search answers "what have I got, and what could I have?" in one question.
  Local content is fragments, commands, skills, profiles and MCP servers
  already installed here; remote content is the bundles a configured remote
  publishes and this project has NOT installed. Both halves run against the
  same query and come back in one answer, because the distinction a person
  cares about is what the content IS, not which side of the pull it sits on.

  THREE NARROWINGS, AND EACH IS A CLAIM ABOUT WHAT IS LEFT OUT:

  | flag      | narrows by | leaves out                                     |
  | --type    | kind       | every other kind                               |
  | --tag     | tag        | everything untagged with it — FRAGMENTS ONLY   |
  | --local   | source     | remote repositories                            |
  | --remote  | source     | this project's installed content               |

  A narrowing is untestable by asserting the match appears: a search that
  ignored the flag entirely still returns it. So every scenario below seeds
  something the same query WOULD match, and asserts that the narrowing
  excluded it — with the un-narrowed run in the same fixture, so "excluded" is
  proven capable of failing.

  `--tag` alone is fragment-scoped, and deliberately so: without a query, the
  name/description matchers behind commands, profiles and MCP servers are
  satisfied by everything, so a tag-only search over all kinds would return
  the whole library under the guise of a filter.

  A SEARCH WITH NOTHING TO LOOK FOR IS REFUSED rather than answered. An empty
  query matches everything, and dumping the library in response to a mistyped
  command is the failure mode this refusal exists for.

  Rule: A search must be told what to look for

    Scenario: A search with neither a query nor a tag is refused, not answered with everything
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      # Asked properly, it answers — so the refusal below is a refusal rather
      # than a search that never worked in this fixture.
      When I run "ctxloom search --local testing"
      Then the command succeeds
      And the output contains "Results (1):"
      When Alice runs the search having typed no query at all:
        """
        ctxloom search --local
        """
      Then the command fails
      And the output contains "please provide a search query or tags"
      And the output does not contain "Results ("

    Scenario: A --type nobody recognizes is refused, and the message names the vocabulary
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      When I run "ctxloom search --local --type fragment testing"
      Then the command succeeds
      And the output contains "Results (1):"
      When I run "ctxloom search --local --type widget testing"
      Then the command fails
      And the output contains "unknown type"
      And the output contains "valid: fragment, command, skill, profile, bundle, mcp_server"

  Rule: A name search reaches every kind of local content at once

    One query, every local kind, grouped by kind in the answer. The fixture
    seeds a fragment and a command whose names the same query matches, so the
    result proves the search crossed kinds rather than stopping at the first
    one it knows about.

    Scenario: One query finds matching content of more than one kind
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      And a command "testing-review" in bundle "demo" exists
      When Alice looks for everything about testing:
        """
        ctxloom search --local testing
        """
      Then the command succeeds
      And the output contains "Results (2):"
      And the output contains "Fragments:"
      And the output contains "- testing"
      And the output contains "Commands:"
      And the output contains "- testing-review"

  Rule: --type narrows to one kind, and that is a claim about exclusion

    # RESTRICT is a claim about what is left OUT, and asserting only that the
    # match appears says nothing about it: a search that ignored --type
    # entirely still returns the fragment. The fixture seeds a command whose
    # name the same query matches, so the restriction has something to exclude
    # and the result count states the narrowing outright.
    Scenario: Restricting to fragments drops the command the same query matched
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      And a command "testing-review" in bundle "demo" exists
      When I run "ctxloom search --local testing"
      Then the command succeeds
      And the output contains "Results (2):"
      And the output contains "testing-review"
      When Alice narrows the same search to fragments:
        """
        ctxloom search --local --type fragment testing
        """
      Then the command succeeds
      And the output contains "Results (1):"
      And the output contains "- testing"
      And the output does not contain "testing-review"
      And the output does not contain "Commands:"

    # The mirror of the row above, so the flag is shown to select rather than
    # merely to survive one value: the same query, narrowed the other way,
    # keeps the command and drops the fragment.
    Scenario: Restricting to commands drops the fragment the same query matched
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      And a command "testing-review" in bundle "demo" exists
      When Alice narrows the same search to commands:
        """
        ctxloom search --local --type command testing
        """
      Then the command succeeds
      And the output contains "Results (1):"
      And the output contains "- testing-review"
      And the output does not contain "Fragments:"

    # Profiles are searchable content too — a composition is something a
    # person looks for by name as readily as a fragment is.
    Scenario: Restricting to profiles finds a composition and nothing else
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "dev-notes" in bundle "demo" exists
      And a profile "dev" with bundle "demo"
      When I run "ctxloom search --local dev"
      Then the command succeeds
      And the output contains "Results (2):"
      And the output contains "- dev-notes"
      When Alice narrows the same search to profiles:
        """
        ctxloom search --local --type profile dev
        """
      Then the command succeeds
      And the output contains "Results (1):"
      And the output contains "Profiles:"
      And the output contains "- dev"
      And the output does not contain "dev-notes"

  Rule: A tag search is answered from the tags, not from the name

    `--tag` matches what an author LABELLED, which is the whole point: content
    named nothing like the query is still found, and content named exactly
    like it is still left out. The fixture's `example` fragment carries the
    tag; `testing` carries none, and is findable by name in the same fixture —
    which is what makes its absence from the tag search mean something.

    Scenario: A tag search returns what carries the tag and leaves out what does not
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      When I run "ctxloom search --local testing"
      Then the command succeeds
      And the output contains "- testing"
      When Alice searches by the label instead of the name:
        """
        ctxloom search --local --tag example
        """
      Then the command succeeds
      And the output contains "Results (1):"
      And the output contains "- example [example]"
      And the output does not contain "- testing"

  Rule: --local and --remote choose the source, and browsing is not installing

    A remote's catalog is readable without pulling anything: the remote half
    of a search reports bundles this project has NOT installed, so it can
    answer "should I install this?" before the lockfile has any opinion. The
    two scopes are proven against each other in one fixture — a local match
    and a remote match under the same query, each of which must disappear when
    the other scope is asked for alone.

    @remote
    Scenario: One query answers from both sources, and each flag narrows it to one
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And a bundle "notes" exists
      And a fragment "demo-local-note" in bundle "notes" exists
      When Alice asks what she has and what she could have:
        """
        ctxloom search demo
        """
      Then the command succeeds
      And the output contains "- demo-local-note"
      And the output contains "Remote:"
      When Alice asks only about what is already installed:
        """
        ctxloom search --local demo
        """
      Then the command succeeds
      And the output contains "- demo-local-note"
      And the output does not contain "Remote:"
      When Alice asks only about what upstream publishes:
        """
        ctxloom search --remote demo
        """
      Then the command succeeds
      And the output contains "Remote:"
      And the output contains "demo"
      And the output does not contain "demo-local-note"

    # `--type bundle` is the one kind that exists ONLY upstream: a bundle is
    # how content is distributed, so narrowing to it narrows to the remote
    # scope without anyone naming a scope. The local match in the same fixture
    # is what proves that narrowing happened.
    @remote
    Scenario: Narrowing to bundles searches upstream alone
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And a bundle "notes" exists
      And a fragment "demo-local-note" in bundle "notes" exists
      When I run "ctxloom search demo"
      Then the command succeeds
      And the output contains "- demo-local-note"
      When Alice narrows the search to distributable bundles:
        """
        ctxloom search --type bundle demo
        """
      Then the command succeeds
      And the output contains "Remote:"
      And the output does not contain "- demo-local-note"

  Rule: The machine-readable answer carries the results, not a re-rendering of the text

    A `--format json` scenario that only substring-matches is not testing the
    flag at all: every marker it looks for is present in the human rendering
    too, so a command that quietly stopped emitting JSON stays green. This
    reads DECODED structure, which the text form cannot satisfy at any price.

    Scenario: The same search answered as JSON carries each result as an object
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      And a command "testing-review" in bundle "demo" exists
      # The human form first, in the same fixture, so the JSON run below is
      # compared against a search that demonstrably found something.
      When I run "ctxloom search --local testing"
      Then the command succeeds
      And the output contains "Results (2):"
      When Alice asks for the same answer in a form a script can read:
        """
        ctxloom search --local testing --format json
        """
      Then the command succeeds
      And the output is valid JSON
      And the JSON output array "local" contains an object whose "name" is "testing" and whose "type" is "fragment"
      And the JSON output array "local" contains an object whose "name" is "testing-review" and whose "type" is "command"
      And every object in the JSON output array "local" has a non-empty "name"
      # The text rendering is REPLACED, not wrapped: a JSON payload with the
      # human table concatenated onto it is unparseable by the caller that
      # asked for it.
      And the output does not contain "Results ("
