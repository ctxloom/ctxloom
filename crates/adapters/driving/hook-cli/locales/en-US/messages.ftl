# Messages for the crate's errors and warnings. Each id is a code, which begins
# with the crate's name; the variables are its args.

hook-cli-unknown-engine = the hook engine "{ $engine }" is not one whose hooks ctxloom reads (known: { $known })
hook-cli-unknown-engine-fix = name one of: { $known }
hook-cli-no-usable-harp = SessionStart hook: no usable harp (the harp of CTXLOOM_SESSION: "{ $harp }"): this session's transcript carries no harp self-id marker and cannot be attributed to a harp by content
hook-cli-not-recorded = the hook could not record { $what }: { $why } (the engine was answered as usual)
# s4-Q14 (H2): DRAFT wording, the owner's to approve (H-Q10).
hook-cli-held = held { $what } for { $harp }: { $why }; the session's next hook delivers it once a ctxloom daemon of this version or newer serves (the engine was answered as usual)
hook-cli-held-fix = ctxloom daemon status
hook-cli-held-behind = held { $what } for { $harp } behind the facts this session already holds, so its facts are recorded in the order they happened (the engine was answered as usual)
