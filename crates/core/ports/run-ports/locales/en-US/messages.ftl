# Messages for the crate's errors. Each id is an error's code, which begins with
# the crate's name; the variables are its args.

run-ports-token-malformed = a token is 64 hexadecimal characters, not { $length } characters
run-ports-engine-not-yet = the { $engine } engine has not built { $operation } yet
run-ports-records-bad-path = { $rel } is not a path inside a session's directory
run-ports-records-io = could not use { $rel } of session { $harp }: { $ext_err_txt }
run-ports-endpoint-too-long = the run's listener socket path is { $length } bytes, over the { $limit } a unix socket takes: { $path }
run-ports-endpoint-too-long-fix = set XDG_RUNTIME_DIR to a shorter directory, or run ctxloom from a shorter home
run-ports-endpoint-run-too-short = a run's root of { $length } bytes is too short to name its listener socket
run-ports-bearer-malformed = a bearer is a run id, a dot and 64 hexadecimal characters, not { $length } characters
run-ports-endpoint-no-directory = the run's listener socket has nowhere to live: XDG_RUNTIME_DIR is unset and ctxloom's home is unknown
run-ports-endpoint-no-directory-fix = set XDG_RUNTIME_DIR, or CTXLOOM_HOME to an absolute path
