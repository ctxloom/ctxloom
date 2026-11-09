# Messages for the crate's errors. Each id is an error's code, which begins with
# the crate's name; the variables are its args.

hook-listener-bind = the run's hook listener could not be started at { $endpoint }: { $cause }
hook-listener-bind-fix = the socket's directory must be yours with mode 0700, and no other run may be serving the same socket
hook-listener-stop = the run's hook listener could not be stopped cleanly at { $endpoint }: { $cause }
