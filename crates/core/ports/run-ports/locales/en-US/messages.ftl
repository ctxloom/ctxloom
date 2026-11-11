# Messages for the crate's errors. Each id is an error's code, which begins with
# the crate's name; the variables are its args.

run-ports-token-malformed = a token is 64 hexadecimal characters, not { $length } characters
run-ports-engine-not-yet = the { $engine } engine has not built { $operation } yet
run-ports-engine-unknown-effort = the { $engine } engine takes no effort level "{ $level }"; it takes: { $known }
run-ports-engine-unknown-permission-mode = the { $engine } engine takes no permission mode "{ $mode }"; it takes: { $known }
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
