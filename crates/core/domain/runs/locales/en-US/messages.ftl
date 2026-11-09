# Messages for the crate's errors. Each id is an error's code, which begins with
# the crate's name; the variables are its args.

runs-already-enqueued = this run was already enqueued
runs-no-run = there is no such run
runs-turn-in-flight = turn { $turn } is still in flight
runs-ended = this run has ended
runs-credential-hash-invalid = a credential hash is a 32-byte SHA-256 digest, not { $length } bytes
runs-not-its-stream = this stream is not the run's own
runs-credentials-not-distinct = the runner's, the model's and the hook's credentials must all differ
runs-no-credential = a child agent run of { $engine } needs a credential in the environment (any one of: { $variables }), and none is set
runs-no-credential-fix = run `claude setup-token` (it needs a Pro, Max, Team or Enterprise plan and makes a one-year token), then export CLAUDE_CODE_OAUTH_TOKEN with it, or store it in your secret manager
