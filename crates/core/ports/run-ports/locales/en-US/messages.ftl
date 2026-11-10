# Messages for the crate's errors. Each id is an error's code, which begins with
# the crate's name; the variables are its args.

run-ports-token-malformed = a token is 64 hexadecimal characters, not { $length } characters
run-ports-engine-not-yet = the { $engine } engine has not built { $operation } yet
run-ports-engine-unknown-effort = the { $engine } engine takes no effort level "{ $level }"; it takes: { $known }
run-ports-records-bad-path = { $rel } is not a path inside a session's directory
run-ports-records-io = could not use { $rel } of session { $harp }: { $ext_err_txt }
run-ports-bearer-malformed = a bearer is a run id, a dot and 64 hexadecimal characters, not { $length } characters

run-ports-endpoint-malformed = the listener endpoint { $endpoint } is not http://<host>:<port>
run-ports-endpoint-malformed-fix = give the endpoint as http://<host>:<port> (https:// when the listener has a certificate)
run-ports-endpoint-scheme = the listener endpoint scheme { $scheme } is not http or https
run-ports-bind-loopback-unreachable = a probe from inside the { $runtime } child found the host's loopback unreachable, so the hook listener has no narrow address to bind
run-ports-bind-loopback-unreachable-fix = run the child where the runtime reaches the host's loopback (see the runtime's host-alias settings), or use the host runtime; ctxloom will not bind a wider address
run-ports-bind-no-bridge-gateway = the { $runtime } child's bridge network has no gateway address to bind the hook listener on
run-ports-bind-no-bridge-gateway-fix = create the run's user-defined bridge network first, then retry
run-ports-bind-unspecified = the hook listener would bind { $addr }, an address on every interface
run-ports-bind-told-other-than-bound = the child would be told to dial { $told }, which is not the listener's address { $bind }, so its token would go to an interface the listener is not on
