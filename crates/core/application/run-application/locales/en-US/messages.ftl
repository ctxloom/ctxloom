# Messages for the crate's errors. Each id is an error's code, which begins with
# the crate's name; the variables are its args.

run-application-no-prompt = nothing to run: give a prompt as arguments, with --prompt, with -r <saved command>, or on standard input
run-application-no-prompt-fix = ask the model something, e.g. ctxloom run --one-shot "what does this project expect of me?"
run-application-unknown-engine = no engine named { $name } can be driven here; this build drives: { $known }
run-application-unknown-engine-fix = name one of them with --engine, or leave --engine off for the default agent's
run-application-session = the session could not be made: { $reason }
run-application-project = the project could not be identified: { $reason }
run-application-launch = the run could not be launched: { $reason }
run-application-not-started = the engine was not started: { $detail }
run-application-rate-limited = the engine is rate limited: { $detail }
run-application-credential-refused = the engine refused the credential: { $detail }
run-application-engine-failed = the engine failed: { $detail }
run-application-interrupted = the turn was interrupted: { $detail }
run-application-empty-answer = the engine answered nothing
run-application-not-ended = the run { $run } did not end
