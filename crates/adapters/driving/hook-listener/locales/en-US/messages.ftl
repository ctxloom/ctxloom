# Messages for the crate's errors. Each id is an error's code, which begins with
# the crate's name; the variables are its args.

hook-listener-bind = the run's hook listener could not be started at { $endpoint }: { $cause }
hook-listener-bind-fix = free the address, or let the run choose a port
hook-listener-stop = the run's hook listener could not be stopped cleanly at { $endpoint }: { $cause }
hook-listener-stop-timed-out = the run's hook listener at { $endpoint } still had calls open after { $grace_ms } ms and was aborted
hook-listener-bind-unspecified = the run's hook listener was asked to bind { $addr }, an address on every interface
hook-listener-bind-unspecified-fix = bind the loopback address, or the one interface the child reaches
hook-listener-bind-not-reported = the run's hook listener bound for { $addr } but reported no TCP address
