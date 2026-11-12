# Messages for the crate's errors. Each id is a code, which begins with the
# crate's name; the variables are its args. An id ending -fix is the remedy of the one before.

session-cli-bad-state = --state { $value }: a session's state is one of { $states }
session-cli-runtime = could not start the runtime: { $ext_err_txt }
session-cli-output = could not write the answer: { $ext_err_txt }
session-cli-sweep-failed = { $count } sweep action(s) failed; the report says which
