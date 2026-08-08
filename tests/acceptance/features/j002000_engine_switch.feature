@doc
Feature: Engine switch day

  A pricing change, a policy change, a vendor doing something nobody liked —
  and the team is moving to a different engine. This is the day ctxloom's whole
  premise gets tested, because the premise is that your context is yours: the
  fragments, the profiles, the trust decisions and the history belong to the
  team, not to whichever assistant happened to be fashionable when they were
  written.

  On paper it is one command. `agent edit dev --engine <new>` swaps the engine
  under the binding; profiles are engine-neutral by construction, so the same
  content composes for the new engine's own native surfaces; the canonical
  transcript keeps the history readable; and `session backfill` can still
  rescue whatever the abandoned vendor's private store holds.

  In practice Alice discovers the pieces exist and the FLOW does not. No
  migration story narrates the order, nothing verifies the result, and the one
  thing she most wants to know — what does the new engine NOT do that the old
  one did — has no surface at all. Nothing here is structural for the
  mainstream engines, which is exactly the finding: the portability story is
  sellable and untold.

  # NOTE ON SCOPE. J000400 (j000400_multi_engine.feature) is the axis's REFERENCE
  # feature — one profile, many engines, a matrix row per engine — and it is
  # deliberately not a journey. This file does not turn it into one. What it
  # asserts is what a matrix cannot express: that the swap is a SEQUENCE with a
  # before and an after, and that the context surviving it is the SAME context
  # rather than merely a valid one.
  #
  # NOTE ON THE CENTRAL ASSERTION. The comparison is between two real
  # materializations, captured on either side of the swap, compared as bytes.
  # A migration that delivers different-but-valid content is worse than one
  # that fails, because the team's assistant quietly knows something different
  # on Monday and nobody is told what.
  #
  # NOTE ON TAGS. Each scenario carries its own note: an untagged one records
  # the mutation that proves it can still fail, a @wip one the product surface
  # that does not exist yet.

  Background:
    Given Alice's team has standardised on one engine and is moving to another

  # THE MIGRATION ITSELF, as a sequence rather than a capability. The swap is
  # asserted on the file ON DISK and on what `agent show` reports — the two can
  # disagree, and a binding that changed in config while the inspector still
  # reports the old engine is a migration nobody can verify.
  #
  # UNTAGGED: confirmed to pass as written, and confirmed to BITE. Dropping
  # `entry.Engine = orKeep(...)` from operations.SetAgent — so `agent edit
  # --engine` reports "Updated agent" and writes the old engine back — turns
  # this red on the on-disk assertion, which is the failure the scenario names.
  Scenario: The engine changes under the binding and the context does not
    Given the team's guidance reaches the old engine's own surface
    When Alice swaps the engine under the binding
    Then the binding on disk names the new engine, and so does what ctxloom reports
    And the same guidance reaches the new engine's own surface, unchanged

  # A VALIDATION GAP, at the exact moment it does the most damage — and the
  # MEASURED shape of it is more interesting than the reported one. The prior
  # finding was "`manage install --engine <unknown>` exits 0". Against an
  # already-initialized project it does not: it exits non-zero, saying
  # ".ctxloom already exists, and the engine is only recorded while scaffolding
  # it", and points at `ctxloom llm default`.
  #
  # That refusal is INCIDENTAL. It rejects the invocation for a reason that has
  # nothing to do with the engine name, so the one thing it never tells a user
  # typing an engine name they have never typed before is that they typed it
  # wrong. Feed it a perfectly valid engine and you get the same message. On
  # migration day, when every engine name in play is unfamiliar, the error that
  # fires is about directory state.
  #
  # UNTAGGED: `manage install --engine` now validates the argument BEFORE the
  # already-exists check gets a chance to fire, so a typo is diagnosed as a
  # typo even against an already-initialized project. The check lives in
  # cli.checkEngineKnown (internal/cli/manage.go) and runs ahead of
  # checkInstallEngineApplies. Its roster is backends.List(), not
  # operations.AvailableLLMNames — unlike `agent edit --engine`, this flag
  # ends up as the TYPE of a real `{type: engine}` LM config entry
  # (operations.engineRegistry/fallbackRegistry) when scaffolding, so it needs
  # no project config and applies identically whether or not .ctxloom exists
  # yet, the same set InitializeProject already enforces via backends.Exists
  # for the fresh-scaffold path.
  Scenario: A typo in the engine name is caught, not reported as success
    When I run "ctxloom manage install --engine bogus-engine"
    Then ctxloom refuses and names the engines it knows

  # The same validation question one layer up, at the binding. The two commands
  # take the same kind of argument on the same day and are asserted separately,
  # because they are separate code paths and a fix to one says nothing about
  # the other.
  #
  # MEASURED: RED, and worse than the command above. `ctxloom agent edit dev
  # --engine bogus-engine` exits 0 and WRITES the nonexistent engine into the
  # binding. Nothing validates the name at the moment it becomes the team's
  # configuration, so the failure surfaces later, somewhere else, as whatever a
  # missing engine happens to look like downstream.
  #
  # UNTAGGED: `agent edit --engine` now validates its argument and names the
  # engines it knows. The check lives in operations.SetAgent — `agent create`
  # and `agent edit`'s single shared body, so both verbs are covered — and
  # rejects before the write, so a typo'd edit cannot corrupt a live binding.
  # Membership is operations.AvailableLLMNames (registered backends UNION the
  # config's declared labels), the same set `llm default` accepts and offers,
  # because an agent's engine is a LABEL and `--engine claude-fast` is one of
  # this command's own documented examples.
  Scenario: Binding an agent to an engine that does not exist is refused
    When I run "ctxloom agent edit dev --engine bogus-engine"
    Then ctxloom refuses and names the engines it knows

  # THE PORTABILITY PAYOFF, and the reason owning a canonical transcript is
  # worth its cost. History that dies with the engine that produced it is
  # history rented from a vendor. The assertion is deliberately cheap and
  # deliberately present: it is the property most likely to be broken by an
  # unrelated change and least likely to be noticed, because nobody looks for
  # old sessions until they need one.
  #
  # UNTAGGED, but only after the assertion was made capable of failing. As
  # written it searched the COMBINED output for the harp, and Manager.Reconcile
  # warns "session <harp> dropped from the index" when it reaps one — so the
  # message announcing the history was deleted satisfied the check for the
  # history surviving. Measured: with operations.isUnrecoverable inverted for
  # distilled entries the listing returned zero rows and the scenario stayed
  # green. It now reads the ROWS out of `session list --all --format json`
  # stdout, and that same mutation turns it red.
  Scenario: History recorded under the old engine survives the move
    Given a session was recorded under the old engine before the switch
    When Alice swaps the engine under the binding
    Then the sessions recorded under the old engine are still listed after the switch

  # THE MISSING FLOW, stated as the question a migrating team actually asks.
  # Not "did it work" — they can see that — but "what did I just give up?"
  # The concrete, verified loss on this exact engine pair: codex has no
  # session_end hook. A team moving from claude-code to codex loses a hook
  # event they may well have built on, and no surface anywhere mentions it,
  # before or after the switch. They will find out when something stops
  # happening.
  #
  # This is the same shape as U3's missing per-engine capability-loss report,
  # arriving through a different door: there, a new hire silently inherits less
  # than his deskmate; here, a whole team silently loses something it had.
  #
  # UNTAGGED (trusting-ambiguity): `agent show <name>` now reports it —
  # operations.CapabilityLoss wraps the SAME backends.UncarriedSurfaces read
  # `profile materialize` already performed (whiny-exclusive), against the
  # resolved agent's ACTUAL backend and profiles rather than a materialize
  # target. codex declaring only a whole-mechanism noHooksReason (empty for
  # codex — it has hooks) could not have caught this: the gap was PER-EVENT,
  # so agentDescriptor gained unsupportedHookKinds (codex: session_end →
  # codex.NoSessionEndReason, the SAME string addUnifiedHooks' route already
  # used to warn at write time) and UncarriedSurfaces now checks it too.
  Scenario: Nothing tells her what the team just gave up
    When Alice swaps the engine under the binding
    And Alice asks ctxloom what the switch changed and what to verify
    Then ctxloom names what the new engine cannot do that the old one could
