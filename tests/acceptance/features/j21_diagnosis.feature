@doc
Feature: The day the assistant goes blind

  Monday morning. "The assistant knew our deploy process on Friday. It doesn't
  today." And the worst part of the report is the second half: it is still
  reaching HER assistant and not his. Nothing errored. Nothing is red. Content
  simply stopped arriving, and the person who has to find out why has eight
  hops to search and no idea which one dropped it.

  This journey is a binary search over the delivery pipeline, and the product's
  bar is stated as a rule: EVERY stage boundary either names its inspector, or
  it is a defect. Content travels authored -> packaged -> attested ->
  distributed -> admitted -> composed -> delivered -> ingested. One scenario per
  boundary. Each plants the cause at exactly that hop and then asks the
  inspector that owns it to say so out loud. A boundary whose inspector cannot
  name the cause is not a missing test — it is the reason a Monday like this one
  takes a day instead of a minute.

  Two boundaries have no inspector at all, and their scenarios are here to stay
  red until they do: an edited-but-never-re-signed bundle is withheld with
  nothing anywhere naming "unsigned" as the reason, and NOTHING in the product
  can tell a user whether the engine ever read the file it was handed. The
  second is the one this whole journey ends on, because it is the question every
  real diagnosis session ends on too.

  # NOTE ON SCOPE. J18 owns the PRODUCTION of signatures; J3 and J7 own the
  # ADVERSARY (tamper, retraction, revocation). This journey owns neither.
  # Signing appears here only as a way to PLANT a cause, and every assertion is
  # about what an INSPECTOR reports. Nothing below re-proves that a tampered
  # bundle is detected or that `bundle sign` writes bytes.
  #
  # NOTE ON ASSERTIONS. No INSPECTOR's exit code is asserted here. An inspector
  # that exits 0 while naming nothing is precisely the failure under test, so
  # "the command succeeds" would assert the bug. Every Then reads a payload:
  # the bytes of the assembled context, or the inspector's own words naming a
  # specific bundle, fragment, or file.
  #
  # THE ONE EXCEPTION, added 2026-08-05 and not a softening of that rule: B2's
  # refusal row asserts that `remote upgrade` exits 2. That is not "the command
  # succeeded", it is the OPPOSITE — the code is the only thing an unattended
  # sync tells a script, and 0 there would make a refusal indistinguishable from
  # a round with nothing to do. It sits beside four payload assertions that
  # cover what the human is told; neither substitutes for the other.
  #
  # NOTE ON TAGS. Every scenario in this file is @wip, including the ones
  # believed to pass today — this file is a to-do list to be walked one scenario
  # at a time, and a scenario that arrived green would be indistinguishable from
  # one nobody had looked at. Each carries its own untag condition.

  Background:
    Given Alice's team ships its deploy process as ctxloom content

  # ---- B1: authored -> packaged -----------------------------------------
  # Silent-loss mode: the text exists, in no bundle. Inspector per the boundary
  # table: `search` / `bundle show`. Verdict OK — so this scenario is the
  # control that proves the walk's first hop really does answer.
  #
  # UNTAGGED 2026-08-05, confirmed to pass as written AND to bite. Mutation:
  # deleting `fmt.Fprintln(w, "No results found.")` from cli.runUnifiedSearch's
  # zero-result branch turns this red on the "search printed NOTHING at all"
  # assertion — which is the half that matters, since "no such item" and "the
  # search never ran" must not look the same. (A first attempt that suppressed
  # printUnifiedResults' "Results (N):" header did NOT bite: the search here
  # genuinely returns zero results and never reaches that branch.)
  Scenario: Nothing packaged the deploy process, and the search says so
    Given the deploy process exists only as a loose file in Alice's repo, in no bundle
    When I run "ctxloom search deploy"
    Then the search results name no packaged item carrying the deploy process
    And her assistant does not receive the deploy guidance

  # ---- B2: packaged -> attested -----------------------------------------
  # THE REAL CAUSE of this journey's Monday. Carol edited the bundle Friday and
  # never re-signed it, so the newest commit carries bytes the signature beside
  # them does not cover. This first scenario owns the whole attestation
  # boundary: what the sync does with that content, what it leaves in the
  # lockfile, and what it tells Alice.
  #
  # WHAT THE PRODUCT DOES, and it is a DECIDED behaviour rather than an
  # observed one (taskloom unearned-cornea, decided 2026-08-05 by the human):
  # `remote upgrade` REFUSES to advance the pin. The lockfile keeps the last
  # commit whose signature verified, Alice goes on being served that content,
  # and she is told — by name, with the pin she is being kept at.
  #
  # THE PARAGRAPH THAT USED TO SIT HERE WAS FALSE, and is worth recording
  # because the way it became false is the trap this journey is about. It said
  # the consumer "silently keeps serving the SUPERSEDED copy", and it was
  # written from a scenario that NEVER ADVANCED A PIN: "Alice syncs on Monday"
  # ran only `remote update` (a dry check ending in "Run with --apply") and
  # `remote pull` (which says in as many words "Pull never moves an existing
  # pin"). The lockfile never left Friday's commit, so the attestation boundary
  # was never reached and the green proved nothing — two checks confirmed it,
  # making the fixture re-sign properly changed no outcome, and gutting
  # signing.VerifyPublisher so an invalid signature verifies changed no outcome
  # either.
  #
  # Once the step also ran `remote upgrade`, the pin really advanced, and the
  # answer was the OPPOSITE of that paragraph: she was left with NOTHING. The
  # revision was withheld as tampered and the superseded copy went out of reach
  # with the pin that had named it — silent capability loss at exactly the
  # moment a signature stops verifying. That is the behaviour the decision
  # replaced. She keeps serving the old copy now because upgrade refuses to
  # move, not because anything falls back.
  #
  # THE ROW BITES, verified 2026-08-05 by three mutations, one per claim:
  #
  #   - neutering the refusal branch in operations.UpgradeDependencies (so a
  #     failed verifyAdvance writes the proposed entry anyway) turns the PIN
  #     assertion red, naming both SHAs;
  #   - deleting cli.runRemoteUpgrade's call to reportRefusedAdvances — the pin
  #     still holds, and nobody is told — turns the MESSAGE assertion red;
  #   - putting the old "…withheld until reviewed: ctxloom review" line back as
  #     the remedy turns the LAST assertion red, quoting what `review --list`
  #     actually answers.
  #
  # The first mutation reports only the pin: godog stops a scenario at its first
  # failed step, so the three later Thens are skipped rather than separately
  # red. That is why they are mutated separately.
  #
  # The five Thens are separate on purpose, because they are five different
  # ways this can be wrong. The pin can move. The pin can hold while the
  # content stops arriving anyway. Everything can be right and nobody told, a
  # silence indistinguishable from "already up to date". The HUMAN can be told
  # while the SCRIPT is not — an unattended sync that refuses and exits 0 is,
  # to the cron job that ran it, the same as one with nothing to do, so exit 2
  # is asserted on its own (cli.exitCodeRefused, docs/cli-ux-principles.md §7;
  # decided in taskloom monstrous-speech). And the sync can name
  # a remedy that does not exist: it used to say "Changed content from
  # untrusted sources is withheld until reviewed: ctxloom review", and
  # `ctxloom review` answered "Nothing is pending review." — content withheld
  # as tampered is deliberately never offered for review, so the remedy sent
  # Alice in a circle. The last Then re-proves that dead end is still a dead
  # end and holds the sync's own words to it.
  Scenario: An edited, never-re-signed runbook is refused, and the verified pin is kept
    Given Carol published the signed runbook, and Alice's assistant receives its deploy guidance
    When Carol edits the runbook on Friday and never re-signs it
    And Alice syncs on Monday
    Then her assistant never receives the revised deploy guidance
    And the runbook's pin did not advance, and the lockfile still holds the commit whose signature verified
    And her assistant is still served the content at that pin
    And the sync told her the runbook cannot be verified, naming the pin it kept
    And the sync exited with the code for "did some of this deliberately not happen"
    And the remedy it named is not the review queue, which has nothing to offer her

  # THE TRAP, asserted deliberately. Alice's first instinct is `review --list`,
  # and it shows nothing — not because nothing is wrong, but because "unsigned"
  # is a DIFFERENT STATE from "pending" (docs/trust-model.md, "Item states").
  # This scenario exists to pin that: the most obvious inspector is silent here
  # BY DESIGN, and any diagnosis that stops at it stops in the wrong place.
  #
  # UNTAGGED 2026-08-05, after the sync above was fixed to actually advance the
  # pin and this row's assertion was fixed to read the right stream. The
  # product's behaviour was correct throughout and was never changed.
  #
  # Before the pin advanced, `review --list` had nothing to be silent ABOUT, so
  # the green here proved nothing. After it, the pending list still prints
  # exactly "Nothing is pending review." — unsigned really is not pending, and
  # the trap this scenario documents is real — but the step was reading
  # w.env.LastOutput(), which MERGES stderr, and the withhold advisory on
  # stderr names the bundle by its full remote URL, "deploy-runbook" included.
  # The assertion therefore tripped on the WARNING and reported the opposite of
  # what the product did. It now reads the list's own stdout, with an
  # empty-stdout guard so that is not a free pass.
  #
  # Two mutations, both red: removing the `!v.Reason.NeedsReview()` filter from
  # operations.reviewEnumerator.classify (so withheld items are listed as
  # pending) trips the naming assertion, and deleting "Nothing is pending
  # review." from cli.renderReviewList trips the silence guard.
  #
  # The scenario's own escape clause still stands: if the pending list ITSELF
  # starts naming the runbook, that is good news, not a regression — it means
  # B2 grew an inspector, and the scenario below is the one that should then be
  # green.
  Scenario: The pending-review list is silent, because unsigned is not pending
    Given Carol published the signed runbook, and Alice's assistant receives its deploy guidance
    And Carol edits the runbook on Friday and never re-signs it
    And Alice syncs on Monday
    When I run "ctxloom review --list"
    Then the pending-review list does not name the runbook at all
    And her assistant never receives the revised deploy guidance

  # B2's DEFECT, red and expected to stay red. Boundary-table verdict: PARTIAL —
  # nothing NAMES "unsigned". Today Alice finds this by diffing lockfiles by
  # hand. The step probes every inspector the boundary table nominates (doctor,
  # review --list, bundle list, bundle show, trust signer list) and fails with
  # all five outputs quoted, so the red scenario is itself the evidence of what
  # each surface says instead.
  #
  # UNTAG WHEN: any ctxloom inspector names the withheld bundle AND the word
  # "unsigned" in one report. FLOWS-UNIFIED §5.5 puts this in doctor's trust
  # checks; any surface satisfying the assertion is fine. This is one of the two
  # sharpest diagnosis gaps in the product.
  #
  # ITS PREMISE MOVED, RE-MEASURED 2026-08-05 against the refusal above, and
  # this paragraph replaces one that is no longer true.
  #
  # While upgrade still advanced the pin, `doctor` came within a case fold of
  # closing this row: DOCTOR-CHECK-CONTENT-TRUST-n4 warned "1 remote bundle(s)
  # are UNSIGNED to this machine, so their content is withheld …
  # @bundles/deploy-runbook", which names the bundle and the reason in one
  # report, and only "UNSIGNED" vs the step's "unsigned" kept it red.
  #
  # Now that upgrade REFUSES the advance, that warning is gone: nothing is
  # withheld at the kept pin, and n4 reports "[ok] every remote bundle's
  # content is attributable to a publisher this machine trusts" — which is
  # true. The runbook did not stop arriving; only the REVISION did, and the
  # thing that names that is the sync's own refusal message (asserted by the
  # first scenario above), not an after-the-fact inspector.
  #
  # So this row is asking a question whose subject has changed, and answering
  # it means deciding what "why did the newer copy not arrive?" should be
  # ANSWERABLE BY, minutes or days after the sync that refused it — a
  # persisted refusal doctor could read, most likely. That is a design
  # question, not a case fold. It stays @wip, with its untag condition
  # unchanged: some inspector names the bundle and why its revision is not
  # here.
  @wip
  Scenario: An inspector names the unsigned runbook as the reason it is withheld
    Given Carol published the signed runbook, and Alice's assistant receives its deploy guidance
    And Carol edits the runbook on Friday and never re-signs it
    And Alice syncs on Monday
    When Alice asks ctxloom why the runbook stopped arriving
    Then some inspector names the runbook as withheld because it is unsigned

  # ---- B3: attested -> distributed --------------------------------------
  # Silent-loss mode: published, never pulled — or, as here, deliberately frozen
  # and then forgotten about. A hold is a decision someone made on purpose; a
  # hold nobody can see is indistinguishable from a broken pull. Inspector:
  # `bundle list`. Verdict OK.
  #
  # WAS RED, and was a finding against B3's "OK" verdict: `ctxloom bundle hold`
  # froze the bundle correctly — the payload half always passed — but
  # `ctxloom bundle list` rendered the runbook as an ordinary entry at "(v1.0.0)"
  # and said NOTHING about the hold. A deliberate freeze and a broken pull were
  # the same two lines of output, and the only way to tell them apart was
  # diffing lockfiles by hand.
  #
  # FIXED 2026-08-04: BundleInfo carries Held/Retracted, stamped from the
  # lockfile entry by operations.stampLockState (the loader reads bundle CONTENT
  # and knows nothing about pins, so the join can only happen there), and the
  # listing renders "[held]" / "[retracted]" on the name line. Retraction was
  # equally invisible and is a worse silence — the content is still installed and
  # still being served while its publisher has said not to use it — so it is
  # rendered here too, with the publisher's stated reason.
  #
  # The payload half is asserted separately, so a listing that says "held" while
  # actually delivering the new bytes could never pass.
  Scenario: The runbook is frozen at an older version, and the listing names the hold
    Given Carol published the signed runbook, and Alice's assistant receives its deploy guidance
    And Carol publishes a newer signed runbook while Alice's copy is held
    And Alice syncs on Monday
    When I run "ctxloom bundle list"
    Then the installed-bundle listing names the runbook as held
    And her assistant still receives the older deploy guidance and not the newer

  # ---- B4: distributed -> admitted --------------------------------------
  # Silent-loss mode: it arrived and is waiting on her own review. Inspector:
  # `review --list`. Verdict OK — trust is per-consumer, and content sitting in
  # review is the system working, provided the list actually names the item.
  #
  # UNTAGGED 2026-08-05, confirmed to pass as written AND to bite. Mutation:
  # reducing cli.renderReviewList's per-item line from
  # `"  %-8s %s/%s\n", it.Status, it.Kind, it.Name` to the status alone turns
  # this red — the assertion names the bundle AND the fragment inside it, so a
  # pending list that says only "1 item(s) pending" cannot satisfy it.
  Scenario: The runbook is waiting on her review, and the pending list names it
    Given Carol published the signed runbook from a key Alice has never reviewed
    When I run "ctxloom review --list"
    Then the pending list names the deploy fragment of the runbook
    And her assistant does not receive the deploy guidance

  # B4's second half, and the one likely to be red. "Pending" and "pending
  # because I do not trust who signed it" are different diagnoses with different
  # fixes — the first wants `ctxloom review`, the second wants `trust signer
  # create`. A pending list that renders them identically sends Alice to the
  # wrong command. The assertion names the actual key fingerprint, so a generic
  # "review these items" banner cannot satisfy it.
  #
  # MEASURED: RED. The pending list renders exactly one line of item state —
  # "new      fragments/deploy-process" — and carries no signer information of
  # any kind: not the fingerprint, not the principal, not whether the key is
  # trusted. Content signed by a stranger and content signed by the team's own
  # publisher are typographically identical in the surface whose whole job is
  # deciding whether to admit them.
  #
  # UNTAGGED 2026-08-04, condition met: the pending list now renders three
  # distinct publisher states — unsigned, signed by an untrusted key (with that
  # key's fingerprint), signed by a trusted key — and names a DIFFERENT next
  # command for each, which is the part that closes the diagnosis gap: an
  # untrusted signer sends Alice to `trust signer create`, not to `ctxloom
  # review`. The fingerprint is display-only and comes from
  # signing.SignatureKeyFingerprint, which reads the key out of the signature
  # blob and is never a trust input; VerifyPublisher is unchanged and still
  # collapses "unsigned" and "untrusted" for the GATE, which is correct — the
  # collapse was only ever wrong for the human. Related open bug: `trust accept`
  # silently no-ops when the user holds a signing key (task remote-prefix, and
  # j18_signing.feature's own @wip scenario) — that bug lives one hop further
  # on and is NOT restated here.
  Scenario: The pending list says whether the signer is one she trusts
    Given Carol published the signed runbook from a key Alice has never reviewed
    When I run "ctxloom review --list"
    Then the pending list says the runbook's signer is one she does not trust, naming the key
    And her assistant does not receive the deploy guidance

  # ---- B5: admitted -> composed -----------------------------------------
  # Silent-loss mode: the item is installed and admitted and belongs to no
  # profile the agent composes. Inspectors: `profile show`, `agent show`.
  # Verdict OK.
  #
  # UNTAGGED 2026-08-05, confirmed to pass as written — but only after one of
  # its three Thens was STRENGTHENED, because it was tautological.
  #
  # "the agent listing names the profile it composes" asserted that "default"
  # appeared somewhere in `agent show default`'s output. The agent in this
  # fixture is itself NAMED "default", so the assertion was satisfied by the
  # echoed argument: deleting the Profiles bullet list from renderAgentShow
  # outright left it green. The step now reads the rendered "Profiles:" section
  # and the "- default" bullet under it, and that same deletion turns it red.
  #
  # The other two Thens bite as written. `profile show` rendering nothing at
  # all turns the profile-listing Then red on its explicit no-output guard; and
  # composing the runbook into the profile after all — the one product state
  # this scenario says is absent — turns it red on the runbook being named.
  # Same command, two fixture states, two different answers, which is what
  # makes the negative assertion mean something.
  Scenario: The runbook is admitted but in no profile, and profile show says so
    Given the runbook is installed and admitted, but composed into no profile
    When I run "ctxloom profile show default"
    Then the profile listing does not name the runbook among its bundles
    When I run "ctxloom agent show default"
    Then the agent listing names the profile it composes
    And her assistant does not receive the deploy guidance

  # B5's THIRD nominated inspector, and a finding. The boundary table lists
  # `run --dry-run` as an inspector for "is my content actually composed?" —
  # the question a user reaches for it to answer. The flag exists (`-n`), but
  # its own help says "Show command that would be executed", which is a
  # different question: it inspects the INVOCATION, not the CONTEXT. If it does
  # not render the composed context, then B5 has two working inspectors rather
  # than three, and the boundary table's third entry is aspirational.
  #
  # MEASURED: this one PASSES. `ctxloom run -n -p default` does render the
  # composed context, deploy guidance and all — so B5 genuinely has three
  # working inspectors and FLOWS-UNIFIED's U5 arc is wrong where it lists
  # "`run --dry-run` (composed?) — absent". Correct that document; do not
  # correct this scenario. It is kept precisely because it is the hop that
  # works, and a diagnosis walk with no green hops teaches nothing about which
  # ones are broken.
  #
  # UNTAGGED 2026-08-05, confirmed to pass as written AND to bite. Mutation:
  # making runState.emitDryRun print "(no context)" unconditionally instead of
  # st.ctxResult.Context turns this red — which is precisely the world the
  # scenario's own comment describes, a --dry-run that inspects the INVOCATION
  # and not the CONTEXT.
  Scenario: The dry run shows her the context that would be sent, not just the command line
    Given the runbook is composed into Alice's profile
    When I run "ctxloom run -n -p default"
    Then the dry run shows her the deploy guidance that would be composed

  # ---- B6: composed -> delivered ----------------------------------------
  # Silent-loss mode: hooks not installed, or a materialized surface that has
  # gone stale under content that moved on. Inspectors: `manage status`,
  # `doctor`. Verdict OK in the boundary table — this scenario tests the
  # staleness half specifically, which is the half a user actually hits.
  #
  # MEASURED: RED, and a finding against B6's "OK" verdict. `manage status`
  # reports the project path, whether MCP auto-registration and the statusline
  # are on, one "not configured" line per engine, and the companion binaries it
  # found. It never mentions a materialized surface at all — so it cannot
  # report one as stale, fresh, or missing. B6's inspector reports on WIRING,
  # not on DELIVERY, and the boundary table credits it with a hop it does not
  # actually watch.
  #
  # The fixture proves the divergence is real before the inspector ever runs:
  # it asserts the surface on disk holds last week's bytes and NOT this week's.
  # So the red here is a product answer, not a harness artifact.
  #
  # UNTAG WHEN: `manage status` (or doctor) reports a materialized surface whose
  # content no longer matches what the profile composes.
  @wip
  Scenario: The composed context moved on and the engine's file did not, and the wiring report says so
    Given the runbook is composed into Alice's profile
    And the engine's own surface on disk still holds last week's copy
    When I run "ctxloom manage status"
    Then the wiring report names the materialized surface as stale

  # ---- B7: delivered -> ingested ----------------------------------------
  # THE DEFECT. Boundary-table verdict: NONE / DEFECT, and both predecessor
  # documents converged on it independently. Every inspector above can be green
  # and the assistant can still be blind, because nothing in the product can
  # tell a user whether the engine READ the file it was handed — a vendor
  # changing its surface format, a config key moving, an engine silently
  # ignoring a path, all look identical from here. J5's live table proves
  # ingestion in CI; it is not a tool a user can run.
  #
  # This is the question every real diagnosis session ends on, and it is the one
  # ctxloom cannot answer. The step probes doctor, manage status, agent show and
  # session list, and fails quoting all four, so the red scenario documents
  # exactly what the product says instead of an answer.
  #
  # UNTAG WHEN: any surface reports, for the configured engine, the file it
  # actually read and whether the delivered content was in it. Until then this
  # stays red — deliberately, permanently, and visibly.
  @wip
  Scenario: Something tells her whether the engine ever read the file it was given
    Given every earlier inspector is green and the deploy guidance is materialized into the engine's own surface
    When Alice asks ctxloom what her engine actually ingested
    Then ctxloom names the surface her engine read and confirms the deploy guidance was in it

  # ---- M5: the two-machine symptom --------------------------------------
  # Where the journey OPENED: "it is reaching her assistant and not his." That
  # is a comparison, and every diagnostic ctxloom has is single-machine. The
  # user has the answer sitting in front of her in two files and no way to ask
  # the tool which one differs and where. Recorded as miss M5 in
  # FLOWS-UNIFIED §4 finding class (b).
  #
  # The fixture makes the divergence genuinely two-sided: Bob's delivered
  # context is materialized and captured OUTSIDE the project before Alice's
  # profile is changed, so this is two real deliveries being compared, not one
  # file read twice.
  #
  # UNTAG WHEN: a surface compares two delivered contexts and names what differs.
  # Shape deliberately unspecified — the scenario probes three plausible
  # spellings and asserts only that SOMETHING answers, so whichever design wins
  # can satisfy it without this file having pre-decided the flag.
  @wip
  Scenario: His assistant has the guidance and hers does not, and something compares the two
    Given Bob's checkout of the same project delivers the deploy guidance and Alice's does not
    When Alice asks ctxloom to compare her delivered context with Bob's
    Then ctxloom reports the deploy guidance as present for Bob and absent for her
