# Messages for the crate's errors. Each id is an error's code, which begins with
# the crate's name; the variables are its args.

run-ports-token-malformed = a token is 64 hexadecimal characters, not { $length } characters
run-ports-engine-not-yet = the { $engine } engine has not built { $operation } yet
run-ports-engine-unknown-effort = the { $engine } engine takes no effort level "{ $level }"; it takes: { $known }
run-ports-engine-bad-permissions = the permissions document handed to the { $engine } engine is not JSON ({ $ext_err_txt }); nothing was launched
run-ports-engine-cannot-carry = the { $engine } engine cannot carry what the session's profiles offer: { $ext_err_txt }; nothing was launched
run-ports-engine-no-host-home = the { $engine } engine is set to use the host's own home (session_home: host), but this environment names no HOME
run-ports-engine-no-host-home-fix = set HOME, or give the engine a session home of its own (engines.{ $engine }.session_home: session)
run-ports-records-bad-path = { $rel } is not a path inside a session's directory
run-ports-records-io = could not use { $rel } of session { $harp }: { $ext_err_txt }
run-ports-bearer-malformed = a bearer is a run id, a dot and 64 hexadecimal characters, not { $length } characters

run-ports-endpoint-malformed = the listener endpoint { $endpoint } is not https://<host>:<port>
run-ports-endpoint-malformed-fix = give the endpoint as https://<host>:<port>
run-ports-endpoint-scheme = the listener endpoint scheme { $scheme } is not https
run-ports-endpoint-plaintext = the listener endpoint { $endpoint } is plaintext http, and the callback is TLS only
run-ports-endpoint-plaintext-fix = give the endpoint as https://<host>:<port>; ctxloom never falls back to http
run-ports-endpoint-port-zero = the listener endpoint { $endpoint } has port 0, which asks to bind a port and is not a place to dial
run-ports-endpoint-port-zero-fix = give the port the listener is bound to
run-ports-cert-missing = the listener certificate is not in the environment
run-ports-cert-missing-fix = run the hook under ctxloom's own run, which sets CTXLOOM_TLS_CERT; ctxloom never falls back to plaintext or to the system's roots
run-ports-cert-malformed = the listener certificate ({ $length } characters) is not base64 of a DER certificate
run-ports-cert-malformed-fix = restore CTXLOOM_TLS_CERT as the run set it; ctxloom never falls back to plaintext or to the system's roots
run-ports-bind-loopback-unreachable = a probe from inside the { $runtime } child found the host's loopback unreachable, so the hook listener has no narrow address to bind
run-ports-bind-loopback-unreachable-fix = run the child where the runtime reaches the host's loopback (see the runtime's host-alias settings), or use the host runtime; ctxloom will not bind a wider address
run-ports-bind-no-bridge-gateway = the { $runtime } child's bridge network has no gateway address to bind the hook listener on
run-ports-bind-no-bridge-gateway-fix = create the run's user-defined bridge network first, then retry
run-ports-bind-unspecified = the hook listener would bind { $addr }, an address on every interface
run-ports-bind-told-other-than-bound = the child would be told to dial { $told }, which is not the listener's address { $bind }, so its token would go to an interface the listener is not on
run-ports-child-not-started = could not start the child's engine { $program }: { $ext_err_txt }
run-ports-child-not-started-fix = check that { $program } is installed and on the PATH ctxloom runs it with (or set engines.<engine>.command)
run-ports-child-closed = the child has ended, or its engine has exited: it takes no more messages
run-ports-child-io = could not reach the child's engine: { $ext_err_txt }
run-ports-command-too-long = the engine's command is too long to start on { $os }: its { $measure } is { $length } where at most { $limit } fit, and the session's opening prompt is the one long part of it
run-ports-command-too-long-fix = start the session without the prompt and paste it in once it is open, or use `ctxloom run --one-shot`, which reads the prompt from stdin
run-ports-program-not-found = the engine was not started: { $program } was not found, looked for as { $names } in each directory of the engine's PATH ({ $dirs })
run-ports-program-not-found-fix = install { $program }, or put the directory that holds it on PATH, then run again
run-ports-program-not-found-no-path = the engine was not started: { $program } was not found, as the engine's PATH names no directory to look for { $names } in
run-ports-program-not-found-no-path-fix = set PATH to the directories your programs are in, then run again
run-ports-batch-arguments = { $program } is a batch file, which cannot be handed an argument holding a line break or a NUL (cmd.exe would end the command there), and the session's opening prompt holds one
run-ports-batch-arguments-fix = install claude's native build (claude.exe; `irm https://claude.ai/install.ps1 | iex` in PowerShell), or start the session without the opening prompt and paste it in once it is open
