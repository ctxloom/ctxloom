# Messages for the crate's errors. Each id is an error's code, which begins with
# the crate's name; the variables are its args.

run-application-session = the session could not be made: { $reason }
run-application-project = the project could not be identified: { $reason }
run-application-listener = the run's hook listener could not be started: { $reason }
run-application-listener-fix = free the loopback address, or let the run choose a port
run-application-lock = the run's lock could not be taken: { $reason }
run-application-launch = the run could not be launched: { $reason }
run-application-not-started = the engine was not started: { $detail }
run-application-rate-limited = the engine is rate limited: { $detail }
run-application-credential-refused = the engine refused the credential: { $detail }
run-application-engine-failed = the engine failed: { $detail }
run-application-interrupted = the turn was interrupted: { $detail }
run-application-empty-answer = the engine answered nothing
run-application-not-ended = the run { $run } did not end
run-application-stopped = the run was stopped by { $signal }: its engine was stopped and the run's end recorded
