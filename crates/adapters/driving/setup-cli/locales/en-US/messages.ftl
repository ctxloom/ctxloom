# Messages for the crate's faults. Each id is a code, which begins with the
# crate's name; the variables are its args.

setup-cli-output = could not write the answer: { $ext_err_txt }
setup-cli-no-such-command = { $noun } is not a setup command
setup-cli-token-not-exported = delegated agents need a token, and none is exported ({ $variables })
setup-cli-token-not-exported-fix = run `{ $command }` (it needs a Pro, Max, Team or Enterprise plan, and the token lasts a year), then export { $export } with it, or store it in your secret manager
