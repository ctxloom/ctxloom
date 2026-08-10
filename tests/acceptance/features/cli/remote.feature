@doc
Feature: remote — registering the sources content comes from, and browsing them

  Covers: `ctxloom remote create`, `remote edit`, `remote list`, `remote show`, `remote
  default`, `remote remove`, and the bare `ctxloom remote` form.

  A remote is an ADDRESS. Registering one is local bookkeeping over
  `.ctxloom/remotes.yaml`: which repositories this project may draw shared
  bundles from, and which of them is the default. No fetch, no credential,
  nothing installed — which is what makes every verb here safe to run offline.

  A remote carries no trust either. Its content takes the review path whatever
  address it arrived from, and auto-trusting a publisher means trusting their
  signing KEY (`ctxloom signer trust`), which is verified over the bytes, never
  a URL, which is not. Registering a remote is also the consent to PUBLISH to
  it: `ctxloom bundle push` writes to a registered remote and asks for no
  second blessing, because naming the destination was the deliberate act.

  What this project has INSTALLED from these remotes is a different question
  and a different noun — see cli/deps.feature.

  This is the comprehensive per-noun spec: what the noun DOES, leaf by leaf,
  hermetically against a seeded file:// repository.

  Rule: The registry is local bookkeeping — no fetch happens

    Registering, defaulting, and removing a remote only ever touch
    `.ctxloom/remotes.yaml`. None of the three names, let alone reaches, a
    bundle — that is what makes them safe to run with no network and no
    credential.

    Scenario: Registering a remote records it and it is listed back
      Given an initialized ctxloom project
      When Alice registers a remote:
        """
        ctxloom remote create origin file:///tmp/acceptance-remote.git --forge git
        """
      Then the command succeeds
      And the file ".ctxloom/remotes.yaml" contains "origin"
      When I run "ctxloom remote list"
      Then the command succeeds
      And the output contains "origin"

    # The bare noun answers the question somebody typing it has, rather than
    # teaching them what they could have typed instead.
    Scenario: Bare remote lists the registry
      Given an initialized ctxloom project
      And I run "ctxloom remote create origin file:///tmp/acceptance-remote.git --forge git"
      When I run "ctxloom remote"
      Then the command succeeds
      And the output contains "origin"
      And the output does not contain "Available Commands:"

    Scenario: A remote can be made the default
      Given an initialized ctxloom project
      And I run "ctxloom remote create origin file:///tmp/acceptance-remote.git --forge git"
      When Alice sets the default remote:
        """
        ctxloom remote default origin
        """
      Then the command succeeds
      And the file ".ctxloom/remotes.yaml" contains "default: origin"

    # Bare `remove` is a preview: it must leave the remote registered. A guard
    # that quietly destroyed anyway would still pass a scenario that only
    # checked exit code — the follow-up `remote list` is what actually
    # catches that.
    Scenario: Bare remote remove reports and destroys nothing
      Given an initialized ctxloom project
      And I run "ctxloom remote create origin file:///tmp/acceptance-remote.git --forge git"
      When I run "ctxloom remote remove origin"
      Then the command succeeds
      And the output contains "Nothing was removed"
      And the output contains "--yes"
      When I run "ctxloom remote list"
      Then the output contains "origin"

    Scenario: Removing a remote takes it out of the registry
      Given an initialized ctxloom project
      And I run "ctxloom remote create origin file:///tmp/acceptance-remote.git --forge git"
      When Alice removes the remote:
        """
        ctxloom remote remove origin --yes
        """
      Then the command succeeds
      When I run "ctxloom remote list"
      Then the output does not contain "origin"

  Rule: A registered remote can be corrected in place

    An address changes — a repository moves host, a forge is misresolved, a
    name stops describing what it points at. Editing is the same local
    bookkeeping as registering: content already installed is untouched,
    because each lockfile entry records the URL it came from rather than
    naming a remote, and trust is unaffected because a remote never carried
    any.

    Scenario: Correcting a remote's URL records the new address and drops the old
      Given an initialized ctxloom project
      And I run "ctxloom remote create origin file:///tmp/acceptance-remote.git --forge git"
      When Alice corrects the remote's address:
        """
        ctxloom remote edit origin --url file:///tmp/acceptance-moved.git
        """
      Then the command succeeds
      And the file ".ctxloom/remotes.yaml" contains "acceptance-moved.git"
      And the file ".ctxloom/remotes.yaml" does not contain "acceptance-remote.git"

    # --forge rebinds the adapter a remote resolves through, independent of
    # its URL. Created bound to "git", then rebound to "github" — the new
    # label read back out of the registry is the only honest proof the
    # rebind landed rather than the create-time binding just sitting there.
    Scenario: Rebinding a remote's forge records the new binding
      Given an initialized ctxloom project
      And I run "ctxloom remote create origin file:///tmp/acceptance-remote.git --forge git"
      When Alice rebinds the remote to a different forge:
        """
        ctxloom remote edit origin --forge github
        """
      Then the command succeeds
      And the file ".ctxloom/remotes.yaml" contains "forge: github"

    # The rename must carry the default with it. The pointer is stored BY NAME,
    # so a rename that ignored it would leave `default:` naming a remote that
    # no longer exists — and every bare command reaching for a default would
    # find nothing, with the registry looking perfectly well-formed.
    Scenario: Renaming the default remote carries the default with it
      Given an initialized ctxloom project
      And I run "ctxloom remote create origin file:///tmp/acceptance-remote.git --forge git"
      And I run "ctxloom remote default origin"
      When Alice renames the remote:
        """
        ctxloom remote edit origin --name upstream
        """
      Then the command succeeds
      And the file ".ctxloom/remotes.yaml" contains "default: upstream"
      And the file ".ctxloom/remotes.yaml" does not contain "default: origin"
      When I run "ctxloom remote list"
      Then the output contains "upstream"
      And the output does not contain "origin"

    # An edit naming no field is a refusal, not a success. Reporting exit 0
    # over an untouched registry is this project's characteristic bug.
    Scenario: An edit that asks for nothing is refused
      Given an initialized ctxloom project
      And I run "ctxloom remote create origin file:///tmp/acceptance-remote.git --forge git"
      When I run "ctxloom remote edit origin"
      Then the command fails
      And the file ".ctxloom/remotes.yaml" contains "origin"

  Rule: A remote's catalog can be browsed without installing anything

    `remote show` and the equivalent MCP resource both read a remote's
    published bundles without touching any profile or lockfile — browsing is
    not installing.

    Scenario: Browsing a remote lists the bundles it publishes
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      When Alice browses what a remote publishes:
        """
        ctxloom remote show origin
        """
      Then the command succeeds
      And the output contains "@bundles/demo"

    Scenario: A remote's catalog is also readable over MCP
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      When the agent reads resource "ctxloom://remotes/origin/contents"
      Then the resource contains "demo"
