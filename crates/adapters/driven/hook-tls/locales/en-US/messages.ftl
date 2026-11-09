# Messages for the crate's errors. Each id is an error's code, which begins with
# the crate's name; the variables are its args.

hook-tls-clock = the clock reads { $seconds } seconds since 1970, which is not a date a certificate can start at
hook-tls-clock-fix = set the system clock, then run again
hook-tls-name = { $name } is not a name a certificate can carry: { $ext_err_txt }
hook-tls-generate = could not make the run's listener certificate: { $ext_err_txt }
hook-tls-generate-fix = retry the run; if it persists the system's random source or ring is failing
