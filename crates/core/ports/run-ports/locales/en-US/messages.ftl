# Messages for the crate's errors. Each id is an error's code, which begins with
# the crate's name; the variables are its args.

run-ports-token-malformed = a token is 64 hexadecimal characters, not { $length } characters
run-ports-engine-not-yet = the { $engine } engine has not built { $operation } yet
run-ports-records-bad-path = { $rel } is not a path inside a session's directory
run-ports-records-io = could not use { $rel } of session { $harp }: { $ext_err_txt }
