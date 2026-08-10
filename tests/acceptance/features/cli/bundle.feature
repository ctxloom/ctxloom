@doc
Feature: bundle — the container authored content lives in, and everything that reads or moves one

  Covers: `ctxloom bundle list`, `bundle show`, `bundle view` (including the
  `#path` payload read), `bundle create`, `bundle edit`, `bundle remove` (and
  its `rm`/`del` aliases), `bundle export`, `bundle import`, `bundle push`, and
  the bare `ctxloom bundle` form.

  A bundle is a CONTAINER. One YAML file under `.ctxloom/content/bundles/`
  holds fragments, commands, MCP servers, skills and profiles together, and
  that file is the unit everything else in ctxloom addresses: a profile names a
  bundle, the lockfile pins a bundle, a publisher signs a bundle, and every
  item ref in the system is spelled `<bundle>#<kind>/<name>`. Nothing here
  reaches the network — authoring a bundle is a local file edit, and that is
  what makes every verb below safe to run offline.

  THE CONTAINER IS NOT THE CONTENTS, and the difference decides which command
  answers a question. `list` summarizes every bundle by count. `show` renders
  one bundle's structure — the sections, the tags, the distill markers — but
  never an item's body. `view` is the only verb that hands back BYTES, and the
  `#path` suffix is how it drills from the container into one item. A reader
  looking for a fragment's text who reaches for `show` gets a header and a
  70-character preview, which is the single most common wrong turn on this
  noun.

  SIX OF THIS NOUN'S LEAVES BELONG TO MECHANISMS BIGGER THAN THE CONTAINER,
  and are specified where that mechanism is, rather than duplicated here:

  | leaf                               | specified in                     |
  | bundle trust / reject / forget     | cli/content_decision.feature     |
  | bundle distill                     | content_distill.feature          |
  | bundle sign, bundle move           | j001600_signing.feature          |

  Each of those is a state machine or a ceremony that spans more than one noun
  — a decision that also governs `ctxloom review`, a distillation that also
  governs `fragment distill` and `command distill`, a signature that also
  governs `signer trust` — and splitting one across two files would leave
  neither able to assert the transition that matters.

  `bundle push` is the one leaf here with no hermetic fixture: it writes to a
  forge, and completeness_test.go's excludedLeaves says so. What IS hermetic is
  its refusal path, which never reaches the network, and that is what this file
  drives.

  Where content comes FROM is a different noun (cli/remote.feature); what this
  project has INSTALLED from there is a third (cli/deps.feature).

  Rule: A created bundle is a real file with real content in it

    `bundle create` seeds a skeleton — one example fragment, one example
    command — so a new bundle is editable rather than empty. Every assertion
    below reads the FILE, because "Created bundle: …" is printed from the
    argument and is equally true of a create that wrote zero bytes.

    Scenario: Creating a bundle writes a manifest carrying its seeded content
      Given an initialized ctxloom project
      When Alice starts a bundle for the team's shared guidance:
        """
        ctxloom bundle create demo -d acceptance-fixture-bundle
        """
      Then the command succeeds
      And the file ".ctxloom/content/bundles/demo.yaml" exists
      And the file ".ctxloom/content/bundles/demo.yaml" is valid YAML
      # The seeded bodies, not the name: a create that wrote only the scalar
      # header would satisfy a name check and leave an unusable skeleton.
      And the file ".ctxloom/content/bundles/demo.yaml" contains "# Example Fragment"
      And the file ".ctxloom/content/bundles/demo.yaml" contains "Example prompt content. Describe what this prompt does."
      And the file ".ctxloom/content/bundles/demo.yaml" contains "acceptance-fixture-bundle"
      When I run "ctxloom bundle list"
      Then the command succeeds
      And the output contains "demo"

    # The listing's whole job is the summary: name, version, and what is
    # inside. "the output contains demo" is satisfied by the name line alone,
    # which a renderer that lost every count would still print — so the counts
    # are asserted, and asserted again after the manifest changes underneath
    # them, so a hard-coded summary cannot pass twice.
    Scenario: The listing summarizes each bundle by version and by what it holds
      Given an initialized ctxloom project
      And a bundle "demo" exists
      When Alice asks what this project has authored:
        """
        ctxloom bundle list --no-companions
        """
      Then the command succeeds
      And the output contains "Installed bundles (1):"
      And the output contains "demo (v1.0.0)"
      And the output contains "Contains: 1 fragments, 1 commands"
      # Move the manifest and the summary has to move with it.
      When I run "ctxloom bundle edit demo --version 2.0.0 --add-fragment testing"
      Then the command succeeds
      When I run "ctxloom bundle list"
      Then the output contains "demo (v2.0.0)"
      And the output contains "Contains: 2 fragments, 1 commands"

    # The bare noun answers the question somebody typing it has, through the
    # same seam `ctxloom remote` and `ctxloom deps` use.
    Scenario: Bare bundle lists what is installed
      Given an initialized ctxloom project
      And a bundle "demo" exists
      When I run "ctxloom bundle --no-companions"
      Then the command succeeds
      And the output contains "Installed bundles (1):"
      And the output contains "demo (v1.0.0)"
      And the output does not contain "Available Commands:"

  Rule: Show renders the container's structure, never an item's body

    `show` is the map: which sections exist, how many entries each holds, what
    each entry is tagged, and whether it carries a distilled rendering. It
    deliberately stops at a first-line preview of a fragment — reading an
    item's bytes is `view`'s job, and the two are separated so a `show` cannot
    quietly become a way to dump a whole bundle's content.

    # "the output contains demo" is satisfied by the `Bundle: demo` header
    # alone, so a render that stopped right after the header — suppressing
    # every section, which IS the bundle's structure — passed. The sections
    # this scenario is named after are what it now reads.
    Scenario: Showing a bundle names every section it carries
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      When Alice looks at what the bundle is made of:
        """
        ctxloom bundle show demo
        """
      Then the command succeeds
      And the output contains "Bundle: demo"
      And the output contains "Description: acceptance fixture bundle"
      And the output contains "Fragments (2):"
      And the output contains "- example [example] (no_distill)"
      And the output contains "- testing"
      And the output contains "Commands (1):"
      # The command's own authored description, stored in the manifest and
      # printed from it — not a word the command line ever passed in.
      And the output contains "Example prompt"

    # THE ONE WRONG TURN THIS NOUN INVITES, pinned so it stays a documented
    # boundary rather than an accident. `show` takes a bundle NAME and nothing
    # else: the `#path` selector is `view`'s grammar, and passing one to `show`
    # is a lookup for a bundle whose name happens to contain a `#`, which
    # cannot exist. The refusal is the correct behaviour and the message names
    # the whole argument back, so a reader can see what was actually looked up.
    #
    # The positive case runs FIRST, in the same fixture, so "cannot be read
    # this way" is read off a bundle that demonstrably CAN be read.
    Scenario: Show does not take the item selector — that grammar belongs to view
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      When I run "ctxloom bundle show demo"
      Then the command succeeds
      And the output contains "Fragments (2):"
      When Alice tries to drill into an item the way view lets her:
        """
        ctxloom bundle show demo#fragments/testing
        """
      Then the command fails
      And the output contains "bundle not found: demo#fragments/testing"
      When I run "ctxloom bundle view demo#fragments/testing"
      Then the command succeeds
      And the output contains "FRAGMENT-BODY-testing"

  Rule: View hands back bytes, and `#path` drills from the container into one item

    Without a path, `view` dumps the bundle's stored YAML verbatim. With one,
    it returns exactly that item's content and nothing else — which is what
    makes it the payload read, and what makes an assertion on it capable of
    catching a renderer that emits structure without content.

    # `bundle view <name>` dumps the whole bundle YAML, and "testing" is the
    # fragment's YAML KEY — so re-marshalling through a struct that omits each
    # item's content left this green while view showed nothing of the fragment
    # it is named after. The marker exists only inside the fragment's stored
    # bytes.
    Scenario: Viewing a bundle dumps the manifest with its items' bodies in it
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      When Alice reads the bundle as it is stored:
        """
        ctxloom bundle view demo
        """
      Then the command succeeds
      And the output contains "testing"
      And the output contains "FRAGMENT-BODY-testing"
      And the output contains "Example prompt content. Describe what this prompt does."

    # Drilling in is a claim about what is left OUT as much as what comes back.
    # A `view` that ignored the selector and dumped the whole manifest would
    # satisfy every "contains" assertion here; the command body's absence is
    # what proves the selector narrowed anything, and the whole-bundle read
    # above it is what proves that body was there to be excluded.
    Scenario: A path selector returns one item's bytes and leaves the rest behind
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      When I run "ctxloom bundle view demo"
      Then the output contains "FRAGMENT-BODY-testing"
      And the output contains "Example prompt content. Describe what this prompt does."
      When Alice drills into the fragment alone:
        """
        ctxloom bundle view demo#fragments/testing
        """
      Then the command succeeds
      And the output contains "FRAGMENT-BODY-testing"
      And the output does not contain "Example prompt content. Describe what this prompt does."
      When Alice drills into the command instead:
        """
        ctxloom bundle view demo#commands/example
        """
      Then the command succeeds
      And the output contains "Example prompt content. Describe what this prompt does."
      And the output does not contain "FRAGMENT-BODY-testing"

    # The third selector kind, and the one whose payload is a structure rather
    # than prose: an MCP entry is re-marshalled to YAML under its own heading.
    # A server with no `command:` is not a server, so the command line is what
    # this asserts.
    Scenario: The selector reaches an MCP server's configuration too
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And I run "ctxloom bundle edit demo --add-mcp tree-sitter"
      When Alice reads the server the bundle declares:
        """
        ctxloom bundle view demo#mcp/tree-sitter
        """
      Then the command succeeds
      And the output contains "# MCP Server: tree-sitter"
      And the output contains "command: tree-sitter"

    # `--distilled` is a PREFERENCE, not a guarantee: an item with no distilled
    # rendering is returned raw rather than empty. That fallback is the whole
    # point — a distiller that has never run must not make content unreadable —
    # and it is easy to break into a silent empty read. The case where a
    # distilled form DOES exist is specified in content_distill.feature, which
    # owns the distiller fixture.
    Scenario: Asking for the distilled rendering of an item that has none falls back to the raw bytes
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      When I run "ctxloom bundle view demo#fragments/testing"
      Then the command succeeds
      And the output contains "FRAGMENT-BODY-testing"
      When Alice asks for the compressed rendering that was never made:
        """
        ctxloom bundle view demo#fragments/testing --distilled
        """
      Then the command succeeds
      And the output contains "FRAGMENT-BODY-testing"

    # A malformed selector must say what is wrong with it. Both refusals name
    # the vocabulary, because the everyday way to reach either is a typo, and
    # an error that only says "not found" sends the reader looking for a
    # missing item rather than a mistyped path.
    Scenario: A selector that is not `type/name`, or names a type that does not exist, is refused by name
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      When I run "ctxloom bundle view demo#fragments"
      Then the command fails
      And the output contains "invalid path format"
      When I run "ctxloom bundle view demo#widgets/testing"
      Then the command fails
      And the output contains "unknown item type: widgets"
      And the output contains "expected fragments, commands, mcp, or profiles"
      # And the well-formed spelling still works, so the refusals above are
      # refusals about the PATH rather than about this fixture's bundle.
      When I run "ctxloom bundle view demo#fragments/testing"
      Then the command succeeds
      And the output contains "FRAGMENT-BODY-testing"

  Rule: Editing a bundle changes the manifest, and a no-op edit says it changed nothing

    The flag form of `edit` is the scriptable one: it sets metadata and
    attaches or detaches items by name. Every attachment is asserted on the
    stored file, and every detachment is preceded by the attachment it undoes.

    Scenario: Metadata edits land in the file
      Given an initialized ctxloom project
      And a bundle "demo" exists
      # The description the bundle was created with, read first so the
      # "no longer there" assertion below is made against a fixture that
      # demonstrably had it.
      And the file ".ctxloom/content/bundles/demo.yaml" contains "acceptance fixture bundle"
      When Alice re-describes the bundle:
        """
        ctxloom bundle edit demo -d updated-desc
        """
      Then the command succeeds
      And the file ".ctxloom/content/bundles/demo.yaml" contains "updated-desc"
      And the file ".ctxloom/content/bundles/demo.yaml" does not contain "acceptance fixture bundle"

    # Attach then detach, in one fixture, on the payload. A `--remove-*` that
    # reported success and removed nothing passes any exit-code check, and a
    # `--add-*` that wrote an empty entry passes any name check — the
    # placeholder body `edit` actually writes is what distinguishes both.
    Scenario: Items are attached and detached by name, and the manifest follows
      Given an initialized ctxloom project
      And a bundle "demo" exists
      When Alice attaches a fragment, a command and a server:
        """
        ctxloom bundle edit demo --add-fragment coding-standards --add-prompt review --add-mcp tree-sitter --add-tag golang
        """
      Then the command succeeds
      And the file ".ctxloom/content/bundles/demo.yaml" contains "coding-standards"
      And the file ".ctxloom/content/bundles/demo.yaml" contains "Add prompt content here."
      And the file ".ctxloom/content/bundles/demo.yaml" contains "tree-sitter"
      And the file ".ctxloom/content/bundles/demo.yaml" contains "golang"
      When Alice detaches the two she did not want after all:
        """
        ctxloom bundle edit demo --remove-fragment coding-standards --remove-mcp tree-sitter --remove-tag golang
        """
      Then the command succeeds
      And the file ".ctxloom/content/bundles/demo.yaml" does not contain "coding-standards"
      And the file ".ctxloom/content/bundles/demo.yaml" does not contain "tree-sitter"
      And the file ".ctxloom/content/bundles/demo.yaml" does not contain "golang"
      # The command that was NOT detached is still there, so the removals above
      # took exactly what they named rather than emptying the manifest.
      And the file ".ctxloom/content/bundles/demo.yaml" contains "Add prompt content here."

    # An edit with no flags is a person who does not yet know what to type, and
    # the answer is help plus a plain statement that nothing happened — not a
    # silent exit 0 that reads as a successful edit.
    Scenario: An edit that was told to change nothing says so
      Given an initialized ctxloom project
      And a bundle "demo" exists
      When I run "ctxloom bundle edit demo"
      Then the command succeeds
      And the output contains "No changes made."
      And the file ".ctxloom/content/bundles/demo.yaml" contains "acceptance fixture bundle"

  Rule: A bundle travels as a file, and an import never clobbers without being told to

    Export copies the bundle as-is; import copies it back. "Observable" is the
    claim, and existence plus a name did not observe it: an export that
    marshalled only the scalar header and dropped the fragments:/commands: maps
    was green — the file existed, import reconstructed a bundle called demo,
    and `bundle list` printed it, with the entire payload lost in transit. Both
    ends now name a body.

    Scenario: Export, remove, and re-import a bundle
      Given an initialized ctxloom project
      And a bundle "demo" exists
      When Alice exports the bundle to share it:
        """
        ctxloom bundle export demo exported
        """
      Then the command succeeds
      And the file "exported/demo.yaml" contains "Example prompt content. Describe what this prompt does."
      And the file "exported/demo.yaml" contains "# Example Fragment"
      When I run "ctxloom bundle remove demo --yes"
      Then the command succeeds
      And the file ".ctxloom/content/bundles/demo.yaml" does not exist
      When Alice imports the copy back:
        """
        ctxloom bundle import exported/demo.yaml -f
        """
      Then the command succeeds
      When I run "ctxloom bundle list"
      Then the output contains "demo"
      And the file ".ctxloom/content/bundles/demo.yaml" contains "Example prompt content. Describe what this prompt does."
      And the file ".ctxloom/content/bundles/demo.yaml" contains "# Example Fragment"

    # `-o` writes to an exact file path instead of a directory. Same payload
    # claim, different destination shape — and the only place this flag is
    # exercised at all.
    Scenario: Export can name the destination file outright
      Given an initialized ctxloom project
      And a bundle "demo" exists
      When Alice exports to a path of her own choosing:
        """
        ctxloom bundle export demo -o handoff.yaml
        """
      Then the command succeeds
      And the file "handoff.yaml" contains "# Example Fragment"

    # THE DATA-LOSS GUARD. An import lands on a path that may already hold
    # somebody's work, so the refusal has to be proven by what SURVIVES it, not
    # by the error text: a guard that printed the message and overwrote anyway
    # would pass an exit-code check and still have eaten the local edit. The
    # forced retry that follows proves the guard was the only thing in the way,
    # so the survival above is not simply a broken import.
    Scenario: Importing over an existing bundle refuses, and the local edit survives
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And I run "ctxloom bundle export demo exported"
      And I run "ctxloom bundle edit demo -d LOCAL-EDIT-MARKER"
      When Alice imports the shared copy over her own:
        """
        ctxloom bundle import exported/demo.yaml
        """
      Then the command fails
      And the output contains "bundle already exists"
      And the output contains "use --force to overwrite"
      And the file ".ctxloom/content/bundles/demo.yaml" contains "LOCAL-EDIT-MARKER"
      When Alice decides she meant it:
        """
        ctxloom bundle import exported/demo.yaml --force
        """
      Then the command succeeds
      And the file ".ctxloom/content/bundles/demo.yaml" does not contain "LOCAL-EDIT-MARKER"
      And the file ".ctxloom/content/bundles/demo.yaml" contains "acceptance fixture bundle"

  Rule: Publishing carries a signature rather than deciding one on the spot

    A signature belongs to the bundle, not to the publish: `bundle sign` writes
    a detached `<name>.yaml.sig` sibling and `push` CARRIES it, so the key that
    signs never has to be on the machine that publishes. `--sign` is sugar for
    signing first; `--no-sign` means publish bare. Asking for both in one
    invocation is not a preference the command can resolve, and guessing either
    way would publish something the operator did not ask for.

    # The refusal happens before the bundle is even loaded, which is what makes
    # this the one part of `push` a hermetic fixture can reach: no remote, no
    # key, no network. Publishing itself needs a writable forge and is listed
    # in completeness_test.go's excludedLeaves for exactly that reason.
    Scenario: Being told both to sign and not to sign is refused rather than resolved
      Given an initialized ctxloom project
      And a bundle "demo" exists
      When Alice contradicts herself about signing:
        """
        ctxloom bundle push demo --sign --no-sign
        """
      Then the command fails
      And the output contains "--sign and --no-sign are mutually exclusive"

  Rule: An authored bundle is not an installed dependency

    A local bundle is authored here; a dependency is pinned in the lockfile
    from a remote. `deps hold` operates on the second kind, so pointing it at
    the first has nothing to freeze — and it says so plainly instead of
    recording a hold that would silently protect nothing.

    Scenario: Holding a local bundle reports it is not lockfile-tracked
      Given an initialized ctxloom project
      And a bundle "demo" exists
      When Alice tries to freeze a bundle she wrote herself:
        """
        ctxloom deps hold demo
        """
      Then the command succeeds
      And the output contains "nothing to hold"
      When I run "ctxloom deps unhold demo"
      Then the command succeeds

  Rule: Removal reports first and destroys only when told to

    `remove` is the destroyer, and its bare form is a preview: it names what
    would go — the bundle and every item it carries — states outright that
    nothing was removed, and prints the exact command that would apply it.

    # A guard that quietly destroyed anyway would still pass a scenario that
    # only checked exit code or the report text. The file-exists check is what
    # actually catches that, and the counts are what prove the report was built
    # from the manifest rather than from the argument.
    Scenario: Bare bundle remove reports what would go and destroys nothing
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      When Alice asks what removing the bundle would cost:
        """
        ctxloom bundle remove demo
        """
      Then the command succeeds
      And the output contains "Would remove bundle"
      And the output contains "2 fragment(s), 1 command(s)"
      And the output contains "Nothing was removed. Re-run with --yes to apply:"
      And the output contains "ctxloom bundle remove demo --yes"
      # The report side asserts the bundle still EXISTS — on disk and in the
      # listing both, because a remove that pruned the listing while leaving
      # the file (or the reverse) would satisfy exactly one of these.
      And the file ".ctxloom/content/bundles/demo.yaml" exists
      When I run "ctxloom bundle list"
      Then the output contains "demo"

    Scenario: Removing a bundle with --yes takes the file and the listing entry
      Given an initialized ctxloom project
      And a bundle "demo" exists
      When Alice applies the removal:
        """
        ctxloom bundle remove demo --yes
        """
      Then the command succeeds
      And the file ".ctxloom/content/bundles/demo.yaml" does not exist
      When I run "ctxloom bundle list --no-companions"
      Then the output contains "No bundles installed."

    # The aliases are not decoration: `rm` is what a person's fingers type, and
    # an alias that reached a different code path — a different default, a
    # missing preview — would be a destroyer with no guard on it. Both halves
    # are driven through the alias, in one fixture.
    Scenario: The rm alias previews and destroys exactly as remove does
      Given an initialized ctxloom project
      And a bundle "demo" exists
      When I run "ctxloom bundle rm demo"
      Then the command succeeds
      And the output contains "Nothing was removed"
      And the file ".ctxloom/content/bundles/demo.yaml" exists
      When I run "ctxloom bundle del demo --yes"
      Then the command succeeds
      And the file ".ctxloom/content/bundles/demo.yaml" does not exist
