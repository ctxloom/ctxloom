# world: surfaces
# Ported from cli/deps.feature (rev f36e8dfb2)
# Ported from journeys/j001400_bundle_distribution.feature (rev f36e8dfb2)
# NOT ported: nothing.
# Split from cli/deps.feature (see its header): these Rules, their text unchanged.

@doc
Feature: deps — the verbs that move a pin: check, upgrade

  Covers `deps check` and `deps upgrade`, and the rule that only upgrade moves a pin. The verbs, the closure and the lockfile are
  described in cli/deps.feature; where content comes FROM is cli/remote.feature.

  Rule: Check reads, upgrade shows, and upgrade --yes writes

    `deps check` reaches the network and reports; it changes nothing, which is
    what makes it safe to run anywhere. `deps upgrade` re-resolves each
    profile's closure to the newest commit its constraint allows and shows what
    every move brings in; with --yes it writes the advance straight to the
    active lock — no staging, no approval.

    Scenario: Check reports an available advance and changes nothing
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
      And I run "ctxloom deps pull"
      And the remote "origin" advances its bundle
      When Alice asks what could be advanced:
        """
        ctxloom deps check --format text
        """
      Then the command succeeds
      And the output contains "ctxloom deps upgrade"
      When I run "ctxloom deps list"
      Then the output contains "demo"

    Scenario: Check on an empty closure has nothing to check
      Given an initialized ctxloom project
      When I run "ctxloom deps check --format text"
      Then the command succeeds
      And the output contains "nothing to check"

    Scenario: An upgrade advances the pin directly
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
      And I run "ctxloom deps pull"
      And the remote "origin" advances its bundle
      When Alice advances her pins to the newest commit:
        """
        ctxloom deps upgrade --yes --format text
        """
      Then the command succeeds
      And the output contains "Applied 1 pin(s)."

    # Upgrade rewrites the lock wholesale from the closure, so an entry the
    # project stopped composing disappears in that write. Unnamed, a removal
    # reads as a pin that never existed.
    Scenario: An upgrade names each dependency it drops from the lock
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And a git remote "other" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo --include other/demo"
      And I run "ctxloom deps pull"
      And I run "ctxloom profile modify dev --remove-bundle other/demo"
      When Alice advances her pins to the newest commit:
        """
        ctxloom deps upgrade --yes --format text
        """
      Then the command succeeds
      And the output contains "from the lockfile: nothing this project composes depends on it any more."

    # `deps pull` is offline by design: it cannot observe whether upstream has
    # moved, so it must not assert that it has. The entry was advanced one
    # command ago; telling the user to advance it again is advice the tool has
    # no evidence for.
    Scenario: A pull right after an upgrade does not tell the user to upgrade again
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
      And I run "ctxloom deps pull"
      And the remote "origin" advances its bundle
      And I run "ctxloom deps upgrade --yes"
      When Alice pulls immediately after advancing her pins:
        """
        ctxloom deps pull
        """
      Then the command succeeds
      And the output does not contain "ctxloom deps upgrade"

  Rule: Only upgrade moves a pin, and it shows the move before --yes applies it

    A pin is the decision to run what a bundle ships, so moving one is shown
    before it happens: every item added, removed or changed, what each hook and
    MCP server runs before and after, and the diff of every changed script. Env
    and header VALUES may be credentials, so they are shown only by name and a
    fingerprint of the value: a change is visible, the secret is not.
    Without --yes nothing is written; with it the closure is resolved again and
    what was actually applied is shown. A FIRST pin — the one `pull` creates —
    is shown the same way, with everything the bundle brings in. `pull` never
    moves an existing pin: a changed constraint is reported and waits for
    `upgrade`.

    Scenario: An upgrade without --yes shows the move and writes nothing
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
      And I run "ctxloom deps pull"
      And the remote "origin" ships an MCP server "srv" running "fixture-mcp-one" and a skill script printing "SCRIPT-ONE"
      When Alice asks what an upgrade would do:
        """
        ctxloom deps upgrade --format text
        """
      Then the command succeeds
      And the output contains "+ mcp srv"
      And the output contains "command: fixture-mcp-one"
      And the output contains "+ skill runner"
      And the output contains "+echo SCRIPT-ONE"
      And the output contains "1 pin(s) would move. Re-run with --yes to apply."
      When I run "ctxloom deps upgrade --format text"
      Then the output contains "1 pin(s) would move."

    Scenario: An upgrade with --yes applies the move and shows what it applied
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
      And I run "ctxloom deps pull"
      And the remote "origin" ships an MCP server "srv" running "fixture-mcp-one" and a skill script printing "SCRIPT-ONE"
      When Alice applies the upgrade:
        """
        ctxloom deps upgrade --yes --format text
        """
      Then the command succeeds
      And the output contains "+ mcp srv"
      And the output contains "Applied 1 pin(s)."
      When I run "ctxloom deps upgrade --format text"
      Then the output contains "Everything is up to date."

    Scenario: An upgrade shows what an MCP server and a script run before and after
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And the remote "origin" ships an MCP server "srv" running "fixture-mcp-one" and a skill script printing "SCRIPT-ONE"
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
      And I run "ctxloom deps pull"
      And the remote "origin" ships an MCP server "srv" running "fixture-mcp-two" and a skill script printing "SCRIPT-TWO"
      When Alice asks what an upgrade would change:
        """
        ctxloom deps upgrade --format text
        """
      Then the command succeeds
      And the output contains "~ mcp srv"
      And the output contains "command: fixture-mcp-one -> fixture-mcp-two"
      And the output contains "-echo SCRIPT-ONE"
      And the output contains "+echo SCRIPT-TWO"
      And the output contains "~ API_KEY: <"
      And the output does not contain "secret-SCRIPT-ONE"
      And the output does not contain "secret-SCRIPT-TWO"

    Scenario: A first pin shows everything the bundle brings in, executables included
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And the remote "origin" ships an MCP server "srv" running "fixture-mcp-one" and a skill script printing "SCRIPT-ONE"
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
      When Alice pulls a bundle for the first time:
        """
        ctxloom deps pull --format text
        """
      Then the command succeeds
      And the output contains "New pins, with everything each one brings in:"
      And the output contains "first pin"
      And the output contains "+ mcp srv"
      And the output contains "command: fixture-mcp-one"
      And the output contains "+ fragment demo-frag"
      And the output contains "+echo SCRIPT-ONE"
      And the output contains "API_KEY: <"
      And the output does not contain "secret-SCRIPT-ONE"

    Scenario: A pull never moves an existing pin, even when its constraint changed
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
      And I run "ctxloom deps pull"
      And the remote "origin" advances its bundle
      And I run "ctxloom profile modify dev --remove-bundle origin/demo --add-bundle origin/demo@main"
      When Alice pulls after changing the constraint:
        """
        ctxloom deps pull --force --format text
        """
      Then the command succeeds
      And the output contains "the manifest now asks for main; the pin stays at"
      And the output contains "ctxloom deps upgrade --yes"
      And the output does not contain "New pins"
      When I run "ctxloom deps upgrade --format text"
      Then the output contains "1 pin(s) would move."
