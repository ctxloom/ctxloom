# Messages for the crate's errors. Each id is a code, which begins with the
# crate's name; the variables are its args. An id ending -fix is the remedy of the one before.

session-cli-bad-state = --state { $value }: a session's state is one of { $states }
session-cli-runtime = could not start the runtime: { $ext_err_txt }
session-cli-output = could not write the answer: { $ext_err_txt }
session-cli-sweep-failed = { $count } sweep action(s) failed; the report says which
# `ctxloom top` (DRAFT: every text of it is a draft for the owner).
session-cli-top-unknown-sort-column = --sort { $value }: top sorts by one of { $columns }
session-cli-top-tree-without-session = --tree needs a session to root it: ctxloom top <session> --tree
session-cli-top-all-with-session = --all cannot be given with a session to watch: a session names one project's
session-cli-top-needs-terminal = --format text draws a full screen, and this output is not a terminal
session-cli-top-needs-terminal-fix = Use --format json for a stream of JSON lines.
session-cli-top-session-unknown = { $name }: this project has no such session
session-cli-already-closed = { $harp } was already closed: the close changed nothing

# What `session approvals` and `trust` say in text (step 8, texts.md §6):
# accepted verbatim (owner, 2026-11-16; reviewed before release).
session-cli-approvals-label-allow-once = Allow once
session-cli-approvals-label-allow-session = Allow { $rule } for the rest of this session
session-cli-approvals-label-allow-always = Always allow { $rule }: writes it into agents.{ $agent }.engines.{ $engine }.permissions (previewed first)
session-cli-approvals-label-deny = Deny
session-cli-approvals-none-waiting = no ask is waiting
session-cli-approvals-asks = { $child } asks to run { $tool }: { $input }
session-cli-approvals-ask-under = ask { $ask }, under { $root }
session-cli-approvals-ask-under-until = ask { $ask }, under { $root }, denied at { $when } unless answered
session-cli-approvals-answer-with = answer: ctxloom session approvals answer { $ask } <option> ["<message>"]
session-cli-approvals-always-would = { $ask }: allow-always would write { $rule } into { $path }:
session-cli-approvals-always-apply = To write it and allow the ask: { $apply }
session-cli-approvals-always-question = Write { $rule } into { $path }, and allow it?
session-cli-approvals-answered = { $ask }: answered { $option }
session-cli-approvals-answered-written = { $ask }: answered { $option }, and { $rule } written into { $path }
session-cli-trust-trusted = { $name }: trusted
session-cli-trust-not-trusted = { $name }: not trusted
session-cli-trust-already-trusted = { $name }: already trusted
session-cli-trust-no-longer-trusted = { $name }: no longer trusted
session-cli-trust-nothing = This project's own config says nothing that reaches past it.
session-cli-trust-allows = Trusting it allows this project's own:
session-cli-trust-apply = To trust it: ctxloom trust --yes
session-cli-trust-undo-apply = To take your trust back: ctxloom trust --undo --yes
session-cli-trust-question = Trust this project, allowing what is shown above?
session-cli-trust-undo-question = Take back your trust in this project?
session-cli-trust-none = No project is trusted.
session-cli-trust-by = { $name }  by { $actor }
session-cli-trust-by-since = { $name }  by { $actor } since { $when }
