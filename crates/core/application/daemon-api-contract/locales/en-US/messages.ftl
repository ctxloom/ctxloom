daemon-api-contract-refused = the daemon refused with { $ext_err_txt }
daemon-api-contract-failed = the daemon could not answer: { $ext_err_txt }
daemon-api-contract-name-invalid = { $name } is not a resource name: { $reason }
daemon-api-contract-session-not-found = no session is named { $name }
daemon-api-contract-project-not-found = no project is named { $name }
daemon-api-contract-run-not-found = the daemon hosts no run named { $name }
daemon-api-contract-unimplemented = the daemon does not serve { $method } yet
daemon-api-contract-transcript-empty = { $name } has no captured transcript yet
daemon-api-contract-per-project-store = this project still has a store of its own, { $path }, from before ctxloom kept every project's sessions in one store; it is not read, and nothing is imported from it
daemon-api-contract-per-project-store-fix = remove { $path } (its sessions' history is not carried over), then run the command again
daemon-api-contract-project-unknown = { $cwd } is in no ctxloom project
daemon-api-contract-daemon-hosting = the daemon hosts { $runs } live run(s): { $harps }
daemon-api-contract-daemon-hosting-fix = wait for them to end, or end them with it: ctxloom daemon stop --even-if-live
# OD-62 (2026-11-15): DRAFT wording, the owner's to approve (the successor
# version is gone).
daemon-api-contract-draining = the serving daemon, ctxloom { $daemon }, is shutting down and takes no new call; it still hosts: { $harps }
daemon-api-contract-client-version = the serving daemon is ctxloom { $daemon }, and this { $kind } client is ctxloom { $client }: one ctxloom version serves at a time
daemon-api-contract-client-version-fix = stop the serving daemon, and the next command starts this version's: ctxloom daemon stop
daemon-api-contract-draining-fix = run the command again once it has exited (the next daemon resumes each child it handed over); to stop what it hosts now: ctxloom shutdown now
# Owner ruling 2026-11-17: DRAFT wording, the owner's to approve.
daemon-api-contract-home-gone = the daemon's home { $home } is gone, so it creates nothing there and is exiting
daemon-api-contract-home-gone-fix = run the command again: it starts a fresh daemon
daemon-api-contract-age-invalid = { $key } { $value } is neither an age nor a date
daemon-api-contract-age-invalid-fix = give an age such as 90d, 1h30m or 1.5d, or a date such as 2026-01-01
daemon-api-contract-etag-invalid = the etag { $etag } is not one a preview answered: an etag is the number of the change the preview saw, so nothing was written
daemon-api-contract-etag-invalid-fix = preview the change again (the command without --yes), then apply what it shows
# step 10c U11: DRAFT wording, the owner's to approve.
daemon-api-contract-plan-stale = what the preview showed has changed since (it was { $seen }, it is { $now } now), so nothing was applied
daemon-api-contract-plan-stale-fix = look again: run the command without --yes, and answer what it shows now
# step 10c U11: DRAFT wording, the owner's to approve.
daemon-api-contract-needs-unanswered = the apply did not answer what its preview asked ({ $needs }), so nothing was applied
daemon-api-contract-needs-unanswered-fix = run the command without --yes, and answer what it asks
daemon-api-contract-config-unreadable = the configuration of the project at { $root } does not read: { $ext_err_txt }
daemon-api-contract-launch-in-progress = another launch of { $harp } is being made now
daemon-api-contract-launch-in-progress-fix = wait for it to finish, then look at the child: ctxloom session show { $harp }
# The terminal wake: DRAFT wording (2026-11-18), the owner's to approve.
daemon-api-contract-wake-inform-only = { $harp } cannot be woken: no ctxloom MCP server is connected to its engine (a session run with --no-context has none), so it takes an inform alone, delivered at its next prompt
daemon-api-contract-wake-inform-only-fix = send it as an inform: ctxloom session send { $harp } inform <text>
# Step 8 U4 (texts.md §5, D14): accepted verbatim (owner, 2026-11-16; reviewed before release).
# DRAFT (step 10e Stage 2a, `session restart`).
daemon-api-contract-primary-not-hosted = { $harp } is a terminal session run by its own ctxloom process: the daemon does not host its engine, so it can neither restart nor stop it
daemon-api-contract-primary-not-hosted-fix = end the session and resume it with `ctxloom run --session { $harp }`
# DRAFT (dogfood fix 2, 2026-11-21): a stop or restart of a session whose run has ended.
daemon-api-contract-run-ended = { $harp } has no run open: its last run has ended, so there is nothing to stop or restart
daemon-api-contract-run-ended-fix = resume it with `ctxloom run --session { $harp }`
daemon-api-contract-session-not-hosted = { $name } is no session this daemon runs, so its ask was not recorded
daemon-api-contract-approval-ambiguous = { $harp } has { $count } asks waiting ({ $asks }), so its name does not say which you mean; nothing was answered.
daemon-api-contract-approval-ambiguous-fix = Name the ask itself: `ctxloom session approvals answer <ask> <option>`, with one of { $asks }.
daemon-api-contract-approval-none-waiting = { $harp } has no ask waiting, so nothing was answered.
daemon-api-contract-approval-none-waiting-fix = List what waits with `ctxloom session approvals`, and name the ask (`ask-...`) to answer one that is settled or another session's.
daemon-api-contract-approval-rule-kept = { $rule } was written into { $path }, but { $ask_id } was already { $how }, so the ask was not answered; the rule stays, and every later launch carries it.
daemon-api-contract-approval-rule-kept-fix = Take the rule out of { $path } if you did not mean to keep it.
