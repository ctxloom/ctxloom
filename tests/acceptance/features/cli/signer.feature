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

  EVERY SCENARIO THAT READS OUTPUT IS DRIVEN ONCE PER FORMAT. Off a terminal
  the resolved format is the structured one, so a scenario that only ever
  asserted prose was asserting a rendering no scripted caller receives — and
  rewriting it to speak only JSON would have deleted the human renderer's only
  coverage. Each Outline therefore runs its body three times: with no --format
  flag at all (the derived default, and the row that matters most), with an
  explicit --format json, and with an explicit --format text. The structured
  rows address the payload BY PATH so a structurally-valid but empty result
  fails; the text row keeps the exact prose the scenario always asserted.

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

    # The embedded row is addressed BY ITS STORE rather than by index, so the
    # assertion keeps meaning when a project or user entry sorts ahead of it.
    Scenario Outline: The listing names the compiled-in root even before anyone trusts anything
      Given an initialized ctxloom project
      When Alice asks whose signatures her project honours:
        """
        ctxloom signer list <flags>
        """
      Then the command succeeds
      And the output reports "$[Source=embedded].Path" as "<names the compiled-in root>"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | names the compiled-in root |
        |               | (compiled-in)              |
        | --format json | (compiled-in)              |
        | --format text | (embedded, not removable)  |

    # The bare noun answers the question somebody typing it has, rather than
    # teaching them what they could have typed instead. The help-banner check
    # holds in every encoding — cobra's usage block is prose in all of them.
    Scenario Outline: Bare signer lists the trust root
      Given an initialized ctxloom project
      When I run "ctxloom signer <flags>"
      Then the command succeeds
      And the output reports "$[Source=embedded].Path" as "<names the compiled-in root>"
      And the output does not contain "Available Commands:"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | names the compiled-in root |
        |               | (compiled-in)              |
        | --format json | (compiled-in)              |
        | --format text | (embedded, not removable)  |

    # `show` narrows the listing to one principal. The absence half is asserted
    # only AFTER the presence half, in the same fixture: "no trusted signers"
    # for an unknown principal is worth nothing unless the same command has
    # just been seen to find one.
    #
    # The KEY ITSELF is asserted, not merely that some fingerprint was printed.
    # The text renderer computes an SHA256 fingerprint that exists nowhere in
    # the payload, so the structured rows pin the public key they both describe
    # — a stronger claim than "SHA256: appeared", and the same one.
    Scenario Outline: Show reports one principal's entries, and says so plainly when there are none
      Given an initialized ctxloom project
      And I run "ctxloom signer trust context@acme.example --key 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPu3qoOrcLwuHKdsczSsVcMrm+R6iPISwuP1K1/82kLr acceptance-signer' --yes"
      When Alice checks one publisher:
        """
        ctxloom signer show context@acme.example <flags>
        """
      Then the command succeeds
      And the output reports "$[Source=project].Entry.Principals" containing "<the publisher>"
      And the output reports "$[Source=project].Entry.Namespaces" containing "<the namespace granted>"
      And the output reports "$[Source=project].Entry.PublicKey" as "<the key it trusts>"
      When I run "ctxloom signer show nobody@acme.example <flags>"
      Then the command succeeds
      And the output reports "$" as empty, saying "no trusted signers"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | the publisher        | the namespace granted  | the key it trusts                            |
        |               | context@acme.example | publish.v1.ctxloom.dev | +7eqg6twvC4cp2xzNKxVwyub5HqI8hLC4/UrX/zaQus= |
        | --format json | context@acme.example | publish.v1.ctxloom.dev | +7eqg6twvC4cp2xzNKxVwyub5HqI8hLC4/UrX/zaQus= |
        | --format text | context@acme.example | publish.v1.ctxloom.dev | SHA256:                                      |

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
    #
    # Fallback:false is the structured statement of "wrote the committable
    # store": the only way this call reaches the user store is the fallback
    # path, which says so. The two store files below settle it on disk.
    Scenario Outline: The default store is the one a teammate inherits by cloning
      Given an initialized ctxloom project
      When I run "ctxloom signer trust personal@example.com --key 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPu3qoOrcLwuHKdsczSsVcMrm+R6iPISwuP1K1/82kLr acceptance-signer' --user --yes"
      Then the command succeeds
      And the home file ".ctxloom/allowed_signers" exists
      And the home file ".ctxloom/allowed_signers" contains "personal@example.com"
      When Alice trusts her team's publishing key without naming a store:
        """
        ctxloom signer trust context@acme.example --key 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPu3qoOrcLwuHKdsczSsVcMrm+R6iPISwuP1K1/82kLr acceptance-signer' --yes <flags>
        """
      Then the command succeeds
      And the output reports "Fallback" as "<wrote the committable store>"
      And the output reports "Path" matching "<names the store it wrote>"
      And the file ".ctxloom/allowed_signers" contains "context@acme.example"
      And the file ".ctxloom/allowed_signers" contains "publish.v1.ctxloom.dev"
      # ...and it did NOT also land in the per-machine store, which exists and
      # is read by this assertion.
      And the home file ".ctxloom/allowed_signers" does not contain "context@acme.example"
      # The listing then places each key in the store it actually went to,
      # which is a stronger claim than "both store names appeared somewhere".
      When I run "ctxloom signer list <flags>"
      Then the command succeeds
      And the output reports "$[Source=project].Entry.Principals" containing "<the team's key>"
      And the output reports "$[Source=user].Entry.Principals" containing "<her own key>"
      And the output contains "project"
      And the output contains "user"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | wrote the committable store  | names the store it wrote   | the team's key       | her own key          |
        |               | false                        | \.ctxloom/allowed_signers$ | context@acme.example | personal@example.com |
        | --format json | false                        | \.ctxloom/allowed_signers$ | context@acme.example | personal@example.com |
        | --format text | Trusted context@acme.example | .ctxloom/allowed_signers   | context@acme.example | personal@example.com |

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

    # The refusal itself is an ERROR, which is prose in every encoding — a
    # command that fails never reaches the renderer — so only the read-back is
    # tabled. The read-back is the assertion that matters: a namespace refused
    # on the way in must also be absent on the way out.
    Scenario Outline: A namespace nobody defines is refused rather than silently dropped
      Given an initialized ctxloom project
      When I run "ctxloom signer trust typo@team.example --key 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPu3qoOrcLwuHKdsczSsVcMrm+R6iPISwuP1K1/82kLr acceptance-signer' --namespace publsh --yes"
      Then the command fails
      And the output contains "unknown namespace"
      When I run "ctxloom signer show typo@team.example <flags>"
      Then the command succeeds
      And the output reports "$" as empty, saying "no trusted signers"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         |
        |               |
        | --format json |
        | --format text |

    # THE EDGE THE DEFAULT HAS TO HANDLE. Run with no .ctxloom directory at
    # all, there is no project store to write. It must not fail — it falls back
    # to the user store, and the output names WHICH store it used and WHY,
    # because silently writing somewhere other than where the user expects is
    # exactly the defect shape this project keeps removing.
    Scenario Outline: Trusting a signer outside a project falls back to the user store and says so
      Given an empty project directory
      When I run "ctxloom signer trust solo@example.com --key 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPu3qoOrcLwuHKdsczSsVcMrm+R6iPISwuP1K1/82kLr acme-fallback-test' --yes <flags>"
      Then the command succeeds
      And the output reports "Fallback" as "<fell back>"
      And the output reports "FallbackReason" matching "<why it fell back>"
      And the home file ".ctxloom/allowed_signers" exists
      And the home file ".ctxloom/allowed_signers" contains "solo@example.com"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | fell back  | why it fell back                             |
        |               | true       | no project .*falling back to your user store |
        | --format json | true       | no project .*falling back to your user store |
        | --format text | no project | user store                                   |

  Rule: Untrusting withdraws from the store it names, and only that one

    `signer untrust` means "I will review this myself from now on", not "deny":
    it removes entries, and rejects nothing the signer already published — that
    is `ctxloom bundle reject`'s job.

    Its default store is the USER one, which is the OPPOSITE of `signer
    trust`'s. Stated here as behaviour rather than argued about, because the
    asymmetry surprises people and a scenario that quietly assumed either
    default would be reporting on the wrong file.

    # Removed is the count of lines actually deleted, so 0 and 1 are the whole
    # difference between "I looked in the wrong store" and "I withdrew a grant"
    # — a distinction the success exit code cannot make.
    Scenario Outline: A bare untrust looks in the user store, so a project trust survives it
      Given an initialized ctxloom project
      And I run "ctxloom signer trust context@acme.example --key 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPu3qoOrcLwuHKdsczSsVcMrm+R6iPISwuP1K1/82kLr acceptance-signer' --yes"
      And the file ".ctxloom/allowed_signers" contains "context@acme.example"
      When I run "ctxloom signer untrust context@acme.example <flags>"
      Then the command succeeds
      And the output reports "Removed" as "<nothing removed>"
      # The report-side of a destroyer: the thing it did not remove still exists.
      And the file ".ctxloom/allowed_signers" contains "context@acme.example"
      When Alice withdraws trust from the store she actually wrote to:
        """
        ctxloom signer untrust context@acme.example --project <flags>
        """
      Then the command succeeds
      And the output reports "Removed" as "<one entry gone>"
      And the file ".ctxloom/allowed_signers" does not contain "context@acme.example"
      # Read back through the product's own lookup, not just the file: an
      # entry deleted from disk but still answered by `show` would be a trust
      # root that outlived its own removal.
      When I run "ctxloom signer show context@acme.example <flags>"
      Then the command succeeds
      And the output reports "$" as empty, saying "no trusted signers"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | nothing removed | one entry gone  |
        |               | 0               | 1               |
        | --format json | 0               | 1               |
        | --format text | no entry for    | removed 1 entry |

    # Removing one principal must not take an unrelated one with it. Asserted
    # by the SURVIVOR, because a removal that emptied the whole store would
    # satisfy every assertion about what is gone.
    Scenario Outline: Removing one principal leaves the others standing
      Given an initialized ctxloom project
      And I run "ctxloom signer trust context@acme.example --key 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPu3qoOrcLwuHKdsczSsVcMrm+R6iPISwuP1K1/82kLr acceptance-signer' --yes"
      And I run "ctxloom signer trust releases@acme.example --key 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPu3qoOrcLwuHKdsczSsVcMrm+R6iPISwuP1K1/82kLr acceptance-signer' --yes"
      When I run "ctxloom signer untrust context@acme.example --project <flags>"
      Then the command succeeds
      And the output reports "Removed" as "<one entry gone>"
      And the file ".ctxloom/allowed_signers" does not contain "context@acme.example"
      And the file ".ctxloom/allowed_signers" contains "releases@acme.example"
      When I run "ctxloom signer show releases@acme.example <flags>"
      Then the command succeeds
      And the output reports "$[Source=project].Entry.Principals" containing "<the survivor>"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | one entry gone  | the survivor          |
        |               | 1               | releases@acme.example |
        | --format json | 1               | releases@acme.example |
        | --format text | removed 1 entry | releases@acme.example |

    # "I removed nothing" and "I removed it" must not render identically. A
    # principal nobody ever trusted is reported as absent, by name and by
    # store, rather than acknowledged with a success line that reads as a
    # deletion. The negative assertion holds in every encoding: no payload
    # carries that sentence either.
    Scenario Outline: Untrusting a principal nobody trusted reports the absence, by store
      Given an initialized ctxloom project
      When I run "ctxloom signer untrust nobody@acme.example --project <flags>"
      Then the command succeeds
      And the output reports "Removed" as "<nothing removed>"
      And the output reports "Path" matching "<names the store it looked in>"
      And the output does not contain "removed 1 entry"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | nothing removed                  | names the store it looked in |
        |               | 0                                | allowed_signers$             |
        | --format json | 0                                | allowed_signers$             |
        | --format text | no entry for nobody@acme.example | allowed_signers              |
