daemon-api-assembly-missing-fragment = no fragment is named { $name }
daemon-api-assembly-missing-tag = no fragment is tagged { $name }
# OD-52: DRAFT wording, the owner's to approve.
daemon-api-start-up-incomplete = the daemon's start-up catch-up failed: { $why }; what it shows (sessions, transcripts, what it hosts) may be missing events until a later catch-up succeeds
daemon-api-start-up-incomplete-fix = the daemon tries the catch-up again each round, and this warning clears once one succeeds; the daemon's log (daemon/daemon.log under ctxloom's home) says why it failed
daemon-api-handing-over = the daemon, ctxloom { $daemon }, is shutting down: { $harps } will be handed over to the next daemon; a child busy in a turn has been steered to end it, and is handed over within { $seconds } seconds at the latest, its turn in flight cut; the next daemon resumes each in its own conversation
daemon-api-turn-rate-limited = its last turn ran into its engine's rate limit, and nothing tries it again: once the limit has reset, resume it with `ctxloom session send { $harp } wake <text>`
daemon-api-turn-credential-refused = its engine refused its credential on its last turn
daemon-api-turn-engine-error = its engine failed its last turn
daemon-api-turn-interrupted = its last turn was interrupted
daemon-api-turn-runner-lost = its host was lost while its last turn was in flight
daemon-api-run-launch-failed-child = could not start the child's engine: { $detail }
daemon-api-run-launch-failed = could not start the session's engine: { $detail }
