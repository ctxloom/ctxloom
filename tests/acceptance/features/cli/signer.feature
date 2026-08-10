@doc
Feature: signer — whose signature stands in for your review

  Covers: `ctxloom signer list`, `signer show`, `signer trust`,
  `signer untrust`, and the bare `ctxloom signer` form.

  A signer is a PUBLIC KEY plus the namespaces it is trusted for. Trusting one
  is the single most consequential command in the product: everything that key
  ever publishes — text AND executables, now and in every future update —
  reaches your agent WITHOUT REVIEW until you untrust it. An approve/reject
  grant is narrower and no less serious: it delegates your review decisions to
  somebody else, permanently.

  THERE ARE THREE STORES, AND WHICH ONE A DECISION LANDS IN IS THE POINT.
  ctxloom's own EMBEDDED trust root is compiled into the binary and always
  listed. The PROJECT store (.ctxloom/allowed_signers) is committable — it is
  how a team distributes "trust our lead's approval key" to everyone who
  clones, without each colleague trusting the publisher individually. The USER
  store (~/.ctxloom/allowed_signers) follows one person across every project
  and travels with nobody else. `signer trust` writes the PROJECT store by
  default, because a trust root in the wrong store is invisible until a
  teammate's clone silently trusts nothing.

  Signer management is CLI-only, deliberately (ADR 0024): none of it is exposed
  over MCP. An agent that could name its own key as trusted could forge
  publisher trust for itself, which is exactly the capability the signature
  envelope exists to deny it.

  Trust in a piece of CONTENT is a different decision on a different noun
  (`ctxloom bundle trust`), and trust in a BINARY is another again — see
  cli/companion.feature. The narrative version of publishing and trusting is
  journeys/j001600_signing.feature and j001500_corporate_signed.feature, which
  assert what a PERSON sees, against a real ssh-agent.

  This is the comprehensive per-noun spec: what the noun DOES, leaf by leaf.

  Rule: The trust root is layered, and reading it touches nothing

    Every listing shows ctxloom's compiled-in root alongside whatever the
    project and the user have added, each labelled with the store it came from.
    A listing that collapsed them would hide half of what is actually trusted.

    Scenario: The listing names the compiled-in root even before anyone trusts anything
      Given an initialized ctxloom project
      When Alice asks whose signatures her project honours:
        """
        ctxloom signer list
        """
      Then the command succeeds
      And the output contains "embedded"
      And the output contains "not removable"

    # The bare noun answers the question somebody typing it has, rather than
    # teaching them what they could have typed instead.
    Scenario: Bare signer lists the trust root
      Given an initialized ctxloom project
      When I run "ctxloom signer"
      Then the command succeeds
      And the output contains "embedded"
      And the output does not contain "Available Commands:"

    # `show` narrows the listing to one principal. The absence half is asserted
    # only AFTER the presence half, in the same fixture: "no trusted signers"
    # for an unknown principal is worth nothing unless the same command has
    # just been seen to find one.
    Scenario: Show reports one principal's entries, and says so plainly when there are none
      Given an initialized ctxloom project
      And I run "ctxloom signer trust context@acme.example --key 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPu3qoOrcLwuHKdsczSsVcMrm+R6iPISwuP1K1/82kLr acceptance-signer' --yes"
      When Alice checks one publisher:
        """
        ctxloom signer show context@acme.example
        """
      Then the command succeeds
      And the output contains "context@acme.example"
      And the output contains "publish.v1.ctxloom.dev"
      And the output contains "SHA256:"
      When I run "ctxloom signer show nobody@acme.example"
      Then the command succeeds
      And the output contains "no trusted signers"

  Rule: Trusting writes the committable store by default

    A colleague who clones must inherit the team's trust root without ever
    having been told to type --project. `--user` is the per-machine escape
    hatch, for a decision that should follow one person rather than one repo.

    # THE POSITIVE CONTROL COMES FIRST, and deliberately. `--user` writes the
    # per-machine store where these very assertions can see it, so when the
    # default write below is checked NOT to have gone there, the check is a
    # live one over a file that exists and is read. Asserted the other way
    # round, "the user store does not hold it" would pass just as well against
    # a harness that could never observe a home file at all.
    Scenario: The default store is the one a teammate inherits by cloning
      Given an initialized ctxloom project
      When I run "ctxloom signer trust personal@example.com --key 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPu3qoOrcLwuHKdsczSsVcMrm+R6iPISwuP1K1/82kLr acceptance-signer' --user --yes"
      Then the command succeeds
      And the home file ".ctxloom/allowed_signers" exists
      And the home file ".ctxloom/allowed_signers" contains "personal@example.com"
      When Alice trusts her team's publishing key without naming a store:
        """
        ctxloom signer trust context@acme.example --key 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPu3qoOrcLwuHKdsczSsVcMrm+R6iPISwuP1K1/82kLr acceptance-signer' --yes
        """
      Then the command succeeds
      And the output contains "Trusted context@acme.example"
      And the output contains ".ctxloom/allowed_signers"
      And the file ".ctxloom/allowed_signers" contains "context@acme.example"
      And the file ".ctxloom/allowed_signers" contains "publish.v1.ctxloom.dev"
      # ...and it did NOT also land in the per-machine store, which exists and
      # is read by this assertion.
      And the home file ".ctxloom/allowed_signers" does not contain "context@acme.example"
      When I run "ctxloom signer list"
      Then the command succeeds
      And the output contains "project"
      And the output contains "user"

    # The namespace is part of what is trusted, not a footnote: a key trusted
    # to APPROVE is not a key trusted to PUBLISH, and a store that recorded the
    # broader grant would hand out a capability nobody granted.
    Scenario: A narrower grant records exactly the namespaces asked for
      Given an initialized ctxloom project
      When Alice delegates review — but not publishing — to her lead:
        """
        ctxloom signer trust lead@team.example --key 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPu3qoOrcLwuHKdsczSsVcMrm+R6iPISwuP1K1/82kLr acceptance-signer' --namespace approve,reject --yes
        """
      Then the command succeeds
      And the file ".ctxloom/allowed_signers" contains "approve.v1.ctxloom.dev"
      And the file ".ctxloom/allowed_signers" contains "reject.v1.ctxloom.dev"
      And the file ".ctxloom/allowed_signers" does not contain "publish.v1.ctxloom.dev"

    Scenario: A namespace nobody defines is refused rather than silently dropped
      Given an initialized ctxloom project
      When I run "ctxloom signer trust typo@team.example --key 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPu3qoOrcLwuHKdsczSsVcMrm+R6iPISwuP1K1/82kLr acceptance-signer' --namespace publsh --yes"
      Then the command fails
      And the output contains "unknown namespace"
      When I run "ctxloom signer show typo@team.example"
      Then the command succeeds
      And the output contains "no trusted signers"

    # THE EDGE THE DEFAULT HAS TO HANDLE. Run with no .ctxloom directory at
    # all, there is no project store to write. It must not fail — it falls back
    # to the user store, and the output names WHICH store it used and WHY,
    # because silently writing somewhere other than where the user expects is
    # exactly the defect shape this project keeps removing.
    Scenario: Trusting a signer outside a project falls back to the user store and says so
      Given an empty project directory
      When I run "ctxloom signer trust solo@example.com --key 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPu3qoOrcLwuHKdsczSsVcMrm+R6iPISwuP1K1/82kLr acme-fallback-test' --yes"
      Then the command succeeds
      And the output contains "no project"
      And the output contains "user store"
      And the home file ".ctxloom/allowed_signers" exists
      And the home file ".ctxloom/allowed_signers" contains "solo@example.com"

  Rule: Untrusting withdraws from the store it names, and only that one

    `signer untrust` means "I will review this myself from now on", not "deny":
    it removes entries, and rejects nothing the signer already published — that
    is `ctxloom bundle reject`'s job.

    Its default store is the USER one, which is the OPPOSITE of `signer
    trust`'s. Stated here as behaviour rather than argued about, because the
    asymmetry surprises people and a scenario that quietly assumed either
    default would be reporting on the wrong file.

    Scenario: A bare untrust looks in the user store, so a project trust survives it
      Given an initialized ctxloom project
      And I run "ctxloom signer trust context@acme.example --key 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPu3qoOrcLwuHKdsczSsVcMrm+R6iPISwuP1K1/82kLr acceptance-signer' --yes"
      And the file ".ctxloom/allowed_signers" contains "context@acme.example"
      When I run "ctxloom signer untrust context@acme.example"
      Then the command succeeds
      And the output contains "no entry for"
      # The report-side of a destroyer: the thing it did not remove still exists.
      And the file ".ctxloom/allowed_signers" contains "context@acme.example"
      When Alice withdraws trust from the store she actually wrote to:
        """
        ctxloom signer untrust context@acme.example --project
        """
      Then the command succeeds
      And the output contains "removed 1 entry"
      And the file ".ctxloom/allowed_signers" does not contain "context@acme.example"
      # Read back through the product's own lookup, not just the file: an
      # entry deleted from disk but still answered by `show` would be a trust
      # root that outlived its own removal.
      When I run "ctxloom signer show context@acme.example"
      Then the command succeeds
      And the output contains "no trusted signers"

    # Removing one principal must not take an unrelated one with it. Asserted
    # by the SURVIVOR, because a removal that emptied the whole store would
    # satisfy every assertion about what is gone.
    Scenario: Removing one principal leaves the others standing
      Given an initialized ctxloom project
      And I run "ctxloom signer trust context@acme.example --key 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPu3qoOrcLwuHKdsczSsVcMrm+R6iPISwuP1K1/82kLr acceptance-signer' --yes"
      And I run "ctxloom signer trust releases@acme.example --key 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPu3qoOrcLwuHKdsczSsVcMrm+R6iPISwuP1K1/82kLr acceptance-signer' --yes"
      When I run "ctxloom signer untrust context@acme.example --project"
      Then the command succeeds
      And the output contains "removed 1 entry"
      And the file ".ctxloom/allowed_signers" does not contain "context@acme.example"
      And the file ".ctxloom/allowed_signers" contains "releases@acme.example"
      When I run "ctxloom signer show releases@acme.example"
      Then the command succeeds
      And the output contains "releases@acme.example"

    # "I removed nothing" and "I removed it" must not render identically. A
    # principal nobody ever trusted is reported as absent, by name and by
    # store, rather than acknowledged with a success line that reads as a
    # deletion.
    Scenario: Untrusting a principal nobody trusted reports the absence, by store
      Given an initialized ctxloom project
      When I run "ctxloom signer untrust nobody@acme.example --project"
      Then the command succeeds
      And the output contains "no entry for nobody@acme.example"
      And the output contains "allowed_signers"
      And the output does not contain "removed 1 entry"
