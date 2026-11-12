# Messages for the crate's errors. Each id is an error's code, which begins with
# the crate's name; the variables are its args.

sessions-session-exists = session { $harp } already exists
sessions-no-session = there is no such session
sessions-harp-invalid = { $harp } cannot be a session name: { $reason }
sessions-harp-too-long = { $harp } is { $length } characters, the limit for a session name is { $limit }
sessions-name-malformed = { $name } is not a resource name: { $reason }
sessions-names-taken = the session name { $harp } is taken
sessions-project-name-taken = the project name { $harp } is another project's
sessions-no-claim = nobody holds that session name
sessions-not-holder = this session does not hold the name { $harp }
sessions-child-cannot-stop = a child (depth { $depth }) cannot order a stop
sessions-session-live = session { $harp } is running, and a rename would move its directory from under its engine
sessions-session-live-fix = wait for the session to end, then rename it
sessions-not-owner = session { $requester } is not this session's owner, and only its owner may do that
sessions-session-closed = session { $harp } is closed: it takes no message, clear or new run
sessions-owner-closed = session { $harp } is closed, and a closed session launches no child
sessions-no-open-incarnation = session { $harp } has no run open to orchestrate a child
sessions-no-open-incarnation-fix = launch the child from the session's running agent
sessions-delegation-limit = this session holds { $open } open children, and its limit is { $limit }
sessions-delegation-limit-fix = close a child first, or raise the limit
sessions-untrusted-target = { $root } is another repository that has not passed the trust check
sessions-untrusted-target-fix = review and trust that repository's configuration first
sessions-urgency-unsupported = the session's engine cannot take a message of urgency { $urgency }
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
