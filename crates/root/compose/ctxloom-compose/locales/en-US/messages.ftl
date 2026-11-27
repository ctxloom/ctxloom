# Messages for the crate's errors. Each id is an error's code, which begins
# with the crate's name; the variables are its args.

ctxloom-compose-no-home = there is no home directory: { $variable } is not set, nor is CTXLOOM_DAEMON_ROOT
ctxloom-compose-bad-session = { $session } is not a session id: { $ext_err_txt }
ctxloom-compose-output = the answer could not be written: { $ext_err_txt }
ctxloom-compose-no-runtime = cannot start a runtime to open the store: { $ext_err_txt }
ctxloom-compose-daemon-other-home = the user's daemon serves the ctxloom home { $daemon_home }, not this command's { $home }: there is one daemon per OS user
ctxloom-compose-project-unnamed = the project at { $root } has no project id yet, and a dry run never makes one
ctxloom-compose-project-unnamed-fix = run `ctxloom init` in { $root } to give the project its id
# A DRAFT: no project root outside git (the user's home is never taken for one).
ctxloom-compose-project-root-needed = no project root: { $cwd } has no .ctxloom of its own, is not in a git repository, and no root was named
ctxloom-compose-project-root-needed-fix = name the project root with `--root=<dir>` (or CTXLOOM_ROOT), `--root` alone for your home directory, or run `ctxloom init` in { $cwd } to make it one
# Step 10a U0: not drafted in the red spec (a failure of the machine, exit 32).
ctxloom-compose-checkout = the library checkout's name at { $path } could not be read or written: { $ext_err_txt }
# Step 10c U13 rework (a DRAFT): the scanner could not walk a library tree.
ctxloom-compose-scan = the library at { $path } could not be read: { $ext_err_txt }
ctxloom-compose-daemon-other-home-fix = run the command with the ctxloom home { $daemon_home }, or stop that daemon from it (`ctxloom daemon stop`) before using { $home }
# The daemon cluster's cap (owner ruling 2026-11-18). DRAFT wording, the
# owner's to approve: `daemon serve` that lost the single-instance lock.
ctxloom-compose-daemon-instance-held = another ctxloom daemon holds this account's single-instance lock { $lock }, so this one does not serve
ctxloom-compose-seed = the test seed could not plant the session: { $ext_err_txt }
ctxloom-compose-test-output = a test command's answer could not be written: { $ext_err_txt }
ctxloom-compose-test-refused = the test command { $verb } refused, as it always does
ctxloom-compose-test-refused-fix = nothing to fix: ask for the other test command
ctxloom-compose-test-findings = { $count } fatal findings
# s4-Q14 (H3): DRAFT wording, the owner's to approve. A command's flush of
# the home's holds could never deliver these (said once a session).
ctxloom-compose-held-set-aside = set aside { $count } held facts of { $session }, which will never be recorded: { $why }; they are kept beside the session's hold under ~/.ctxloom/held, and not delivered again
