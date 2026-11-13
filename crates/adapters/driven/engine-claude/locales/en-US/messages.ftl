# Messages for the crate's notices. Each id is a notice's code, which begins
# with the crate's name; the variables are its args.

engine-claude-windows-token-only = on Windows this claude session is handed no login: none of CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN is set, and ctxloom shares no credentials file there. This is a workaround for claude's own credential handling: sessions that share one OAuth login each refresh it, and one session's token rotation can sign the others out, so on Windows claude is signed in only by a token from the environment, which no session rotates
engine-claude-windows-token-only-fix = run `claude setup-token` (it needs a Pro, Max, Team or Enterprise plan), then export CLAUDE_CODE_OAUTH_TOKEN with the token it prints (or export ANTHROPIC_API_KEY); until then, /login in the session signs in that session alone
