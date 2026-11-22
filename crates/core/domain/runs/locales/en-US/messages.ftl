# Messages for the crate's errors. Each id is an error's code, which begins with
# the crate's name; the variables are its args.

runs-already-enqueued = this run was already enqueued
runs-no-run = there is no such run
runs-turn-in-flight = turn { $turn } is still in flight
runs-ended = this run has ended
runs-not-its-stream = this stream is not the run's own
runs-no-credential = a child agent run of { $engine } needs a credential in the environment (any one of: { $variables }), and none is set
runs-no-credential-fix = run `claude setup-token` (it needs a Pro, Max, Team or Enterprise plan and makes a one-year token), then export CLAUDE_CODE_OAUTH_TOKEN with it, or store it in your secret manager
runs-file-only-credential = a child agent run of { $engine } needs a credential in the environment (any one of: { $variables }), and engines.{ $engine }.auth names only a file, which serves the primary interactive session alone
runs-file-only-credential-fix = name one of those variables in engines.{ $engine }.auth.env and export it (for claude-code, `claude setup-token` makes CLAUDE_CODE_OAUTH_TOKEN); the file keeps serving the primary session
runs-no-prompt = nothing to run: give a prompt as arguments, with --prompt, with -r <saved command>, or on standard input
runs-no-prompt-fix = ask the model something, e.g. ctxloom run --one-shot "what does this project expect of me?"
runs-unknown-engine = no engine named { $name } can be driven here; this build drives: { $known }
runs-unknown-engine-fix = name one of them with --engine, or leave --engine off for the default agent's
runs-launch-refused = the run was not started: { $count } of its checks failed
runs-untrusted-project-settings = This project's own config sets { $paths }, which reach past the project, and you have not trusted it, so nothing was launched.
runs-untrusted-project-settings-fix = Look at what trusting the project allows with `ctxloom trust`, then say yes (off a terminal, `ctxloom trust --yes`); or take those settings out of the project's config.
# DRAFT (owner ruling 2026-11-21): a fatal finding of a launch's plan, said as
# the library said it, its remedy the fix line.
runs-fatal-finding = { $text }
runs-fatal-finding-fix = { $remedy }
runs-fatal-finding-said = { $text }
