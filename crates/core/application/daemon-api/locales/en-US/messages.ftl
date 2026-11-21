daemon-api-refused = the daemon refused with { $ext_err_txt }
daemon-api-failed = the daemon could not answer: { $ext_err_txt }
daemon-api-name-invalid = { $name } is not a resource name: { $reason }
daemon-api-session-not-found = no session is named { $name }
daemon-api-project-not-found = no project is named { $name }
daemon-api-run-not-found = the daemon hosts no run named { $name }
daemon-api-unimplemented = the daemon does not serve { $method } yet
daemon-api-transcript-empty = { $name } has no captured transcript yet
daemon-api-project-unknown = { $cwd } is in no ctxloom project
daemon-api-daemon-hosting = the daemon hosts { $runs } live run(s): { $harps }
daemon-api-daemon-hosting-fix = wait for them to end, or end them with it: ctxloom daemon stop --even-if-live
# OD-62 (2026-11-15): DRAFT wording, the owner's to approve (the successor
# version is gone).
daemon-api-draining = the serving daemon, ctxloom { $daemon }, is shutting down and takes no new call; it still hosts: { $harps }
daemon-api-client-version = the serving daemon is ctxloom { $daemon }, and this { $kind } client is ctxloom { $client }: one ctxloom version serves at a time
daemon-api-client-version-fix = stop the serving daemon, and the next command starts this version's: ctxloom daemon stop
daemon-api-draining-fix = run the command again once it has exited (the next daemon resumes each child it handed over); to stop what it hosts now: ctxloom shutdown now
# Owner ruling 2026-11-17: DRAFT wording, the owner's to approve.
daemon-api-home-gone = the daemon's home { $home } is gone, so it creates nothing there and is exiting
daemon-api-home-gone-fix = run the command again: it starts a fresh daemon
daemon-api-age-invalid = { $key } { $value } is neither an age nor a date
daemon-api-age-invalid-fix = give an age such as 90d, 1h30m or 1.5d, or a date such as 2026-01-01
daemon-api-etag-invalid = the etag { $etag } is not one a preview answered: an etag is the number of the change the preview saw, so nothing was written
daemon-api-etag-invalid-fix = preview the change again (the command without --yes), then apply what it shows
# step 10c U11: DRAFT wording, the owner's to approve.
daemon-api-plan-stale = what the preview showed has changed since (it was { $seen }, it is { $now } now), so nothing was applied
daemon-api-plan-stale-fix = look again: run the command without --yes, and answer what it shows now
# step 10c U11: DRAFT wording, the owner's to approve.
daemon-api-needs-unanswered = the apply did not answer what its preview asked ({ $needs }), so nothing was applied
daemon-api-needs-unanswered-fix = run the command without --yes, and answer what it asks
daemon-api-config-unreadable = the configuration of the project at { $root } does not read: { $ext_err_txt }
# OD-62 (2026-11-15): one ctxloom version runs at a time. DRAFT wording,
# the owner's to approve.
daemon-api-version-skew = the serving daemon is ctxloom { $daemon }, not this ctxloom { $own } (it hosts: { $harps })
daemon-api-version-skew-fix = stop it with `ctxloom daemon stop` (`--even-if-live` ends what it hosts), then run the command again
daemon-api-assembly-missing-fragment = no fragment is named { $name }
daemon-api-assembly-missing-tag = no fragment is tagged { $name }
daemon-api-launch-in-progress = another launch of { $harp } is being made now
daemon-api-launch-in-progress-fix = wait for it to finish, then look at the child: ctxloom session show { $harp }
# The terminal wake: DRAFT wording (2026-11-18), the owner's to approve.
daemon-api-wake-inform-only = { $harp } cannot be woken: no ctxloom MCP server is connected to its engine (a session run with --no-context has none), so it takes an inform alone, delivered at its next prompt
daemon-api-wake-inform-only-fix = send it as an inform: ctxloom session send { $harp } inform <text>
daemon-api-launch-lock = a child's launch lock could not be taken: { $ext_err_txt }
# OD-52: DRAFT wording, the owner's to approve.
daemon-api-start-up-incomplete = the daemon's start-up catch-up failed: { $why }; what it shows (sessions, transcripts, what it hosts) may be missing events until a later catch-up succeeds
daemon-api-start-up-incomplete-fix = the daemon tries the catch-up again each round, and this warning clears once one succeeds; the daemon's log (daemon/daemon.log under ctxloom's home) says why it failed
daemon-api-handing-over = the daemon, ctxloom { $daemon }, is shutting down: { $harps } will be handed over to the next daemon; a child busy in a turn has been steered to end it, and is handed over within { $seconds } seconds at the latest, its turn in flight cut; the next daemon resumes each in its own conversation
# Step 8 U4 (texts.md §5, D14): accepted verbatim (owner, 2026-11-16; reviewed before release).
# DRAFT (step 10e Stage 2a, `session restart`).
daemon-api-primary-not-hosted = { $harp } is a terminal session run by its own ctxloom process: the daemon does not host its engine, so it cannot restart it
daemon-api-primary-not-hosted-fix = end the session and resume it with `ctxloom run --session { $harp }`
daemon-api-session-not-hosted = { $name } is no session this daemon runs, so its ask was not recorded
daemon-api-approval-ambiguous = { $harp } has { $count } asks waiting ({ $asks }), so its name does not say which you mean; nothing was answered.
daemon-api-approval-ambiguous-fix = Name the ask itself: `ctxloom session approvals answer <ask> <option>`, with one of { $asks }.
daemon-api-approval-none-waiting = { $harp } has no ask waiting, so nothing was answered.
daemon-api-approval-none-waiting-fix = List what waits with `ctxloom session approvals`, and name the ask (`ask-...`) to answer one that is settled or another session's.
daemon-api-approval-rule-kept = { $rule } was written into { $path }, but { $ask_id } was already { $how }, so the ask was not answered; the rule stays, and every later launch carries it.
daemon-api-approval-rule-kept-fix = Take the rule out of { $path } if you did not mean to keep it.
daemon-api-turn-rate-limited = its last turn ran into its engine's rate limit, and nothing tries it again: once the limit has reset, resume it with `ctxloom session send { $harp } wake <text>`
daemon-api-turn-credential-refused = its engine refused its credential on its last turn
daemon-api-turn-engine-error = its engine failed its last turn
daemon-api-turn-interrupted = its last turn was interrupted
daemon-api-turn-runner-lost = its host was lost while its last turn was in flight
daemon-api-run-launch-failed-child = could not start the child's engine: { $detail }
daemon-api-run-launch-failed = could not start the session's engine: { $detail }
