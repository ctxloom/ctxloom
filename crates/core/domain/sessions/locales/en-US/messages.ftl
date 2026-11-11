# Messages for the crate's errors. Each id is an error's code, which begins with
# the crate's name; the variables are its args.

sessions-session-exists = session { $harp } already exists
sessions-no-session = there is no such session
sessions-harp-invalid = { $harp } cannot be a session name: { $reason }
sessions-harp-too-long = { $harp } is { $length } characters, the limit for a session name is { $limit }
sessions-names-taken = the session name { $harp } is taken
sessions-no-claim = nobody holds that session name
sessions-not-holder = this session does not hold the name { $harp }
sessions-child-cannot-stop = a child (depth { $depth }) cannot order a stop
sessions-session-live = session { $harp } is running, and a rename would move its directory from under its engine
sessions-session-live-fix = wait for the session to end, then rename it
sessions-session-unknown = there is no session named { $harp }
sessions-session-unknown-fix = list the sessions with `ctxloom session list`
sessions-session-destroyed = session { $harp } was removed, and cannot be resumed
sessions-session-destroyed-fix = start a new session with `ctxloom run`
sessions-session-running = session { $harp } is running, and cannot be resumed until it ends
sessions-session-running-fix = return to the session where it runs, or wait for it to end
sessions-no-native-session = session { $harp } has no engine session to resume: its engine never named one
sessions-no-native-session-fix = start a new session with `ctxloom run`
sessions-engine-mismatch = session { $harp } runs on { $engine }, and cannot be resumed on { $asked }
sessions-engine-mismatch-fix = resume it without `--engine`, or with `--engine { $engine }`
