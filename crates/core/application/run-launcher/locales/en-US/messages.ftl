# Messages for the crate's errors. Each id is an error's code, which begins with
# the crate's name; the variables are its args.

run-launcher-catch-up = the runs owed a runner could not be read from the store: { $ext_err_txt }
run-launcher-record = a record of the session could not be written: { $ext_err_txt }
run-launcher-fact = the launch could not be recorded: { $reason }
run-launcher-bad-session = { $session } is not a session id
run-launcher-no-spec = the run { $run } was enqueued with no launch spec recorded, so it cannot be launched
run-launcher-unreadable = the launch spec of the run { $run } cannot be read: { $reason }
# Step 10c U13 (texts.md §1, a DRAFT): the framing of a live session's
# re-assembled context, handed to its engine at its next event.
run-launcher-context-redelivered = ctxloom: your context changed while this session ran ({ $changed }). What follows replaces the context you were given when the session started; where the two differ, follow this one.
# Step 10c U13 rework (a DRAFT, owner 2026-11-18): a re-delivery over the
# hook's 10,000-character budget gives only the fragments that changed.
run-launcher-context-changed-only = ctxloom: your context changed while this session ran ({ $changed }). It is too long to give whole here, so what follows is only the fragments that changed, each whole; the rest of the context you were given stands. Where these differ from what you were given, follow these.
# Step 10c U13 rework (a DRAFT): even its changed fragments are over the budget.
run-launcher-context-too-long = ctxloom: your context changed while this session ran ({ $changed }), and even the fragments that changed are too long to give here. Run `ctxloom context assemble` to read your context as it is now; where it differs from what you were given, follow it.
