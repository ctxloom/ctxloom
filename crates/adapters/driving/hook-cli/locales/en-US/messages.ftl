# Messages for the crate's errors and warnings. Each id is a code, which begins
# with the crate's name; the variables are its args.

hook-cli-unknown-engine = the hook engine "{ $engine }" is not one whose hooks ctxloom reads (known: { $known })
hook-cli-unknown-engine-fix = name one of: { $known }
hook-cli-no-usable-harp = SessionStart hook: no usable harp (CTXLOOM_SESSION_HARP="{ $harp }"): this session's transcript carries no harp self-id marker and cannot be attributed to a harp by content
hook-cli-not-recorded = the hook could not record { $what }: { $why } (the engine was answered as usual)
