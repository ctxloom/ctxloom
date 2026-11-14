# Messages for the crate's errors. Each id is an error's code, which begins
# with the crate's name; the variables are its args.

ctxloom-wiring-no-home = there is no home directory: { $variable } is not set, nor is CTXLOOM_HOME
ctxloom-wiring-bad-session = { $session } is not a session id: { $ext_err_txt }
ctxloom-wiring-no-runtime = cannot start a runtime to open the store: { $ext_err_txt }
ctxloom-wiring-store = cannot open the project store: { $error }
ctxloom-wiring-daemon-other-home = the user's daemon serves the ctxloom home { $daemon_home }, not this command's { $home }: there is one daemon per OS user
ctxloom-wiring-project-unnamed = the project at { $root } has no project id yet, and a dry run never makes one
ctxloom-wiring-project-unnamed-fix = run `ctxloom init` in { $root } to give the project its id
ctxloom-wiring-daemon-other-home-fix = run the command with the ctxloom home { $daemon_home }, or stop that daemon from it (`ctxloom daemon stop`) before using { $home }
ctxloom-wiring-seed = the test seed could not plant the session: { $ext_err_txt }
ctxloom-wiring-test-refused = the test command { $verb } refused, as it always does
ctxloom-wiring-test-refused-fix = nothing to fix: ask for the other test command
ctxloom-wiring-test-findings = { $count } fatal findings
