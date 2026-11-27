# Messages for the crate's errors. Each id is an error's code, which begins
# with the crate's name; the variables are its args.

ctxloom-wiring-no-home = there is no home directory: { $variable } is not set, nor is CTXLOOM_DAEMON_ROOT
ctxloom-wiring-bad-session = { $session } is not a session id: { $ext_err_txt }
ctxloom-wiring-no-runtime = cannot start a runtime to open the store: { $ext_err_txt }
ctxloom-wiring-daemon-other-home = the user's daemon serves the ctxloom home { $daemon_home }, not this command's { $home }: there is one daemon per OS user
ctxloom-wiring-project-unnamed = the project at { $root } has no project id yet, and a dry run never makes one
ctxloom-wiring-project-unnamed-fix = run `ctxloom init` in { $root } to give the project its id
# A DRAFT: no project root outside git (the user's home is never taken for one).
ctxloom-wiring-project-root-needed = no project root: { $cwd } has no .ctxloom of its own, is not in a git repository, and no root was named
ctxloom-wiring-project-root-needed-fix = name the project root with `--root=<dir>` (or CTXLOOM_ROOT), `--root` alone for your home directory, or run `ctxloom init` in { $cwd } to make it one
# Step 10a U0: not drafted in the red spec (a failure of the machine, exit 32).
ctxloom-wiring-checkout = the library checkout's name at { $path } could not be read or written: { $ext_err_txt }
# Step 10c U13 rework (a DRAFT): the scanner could not walk a library tree.
ctxloom-wiring-scan = the library at { $path } could not be read: { $ext_err_txt }
ctxloom-wiring-daemon-other-home-fix = run the command with the ctxloom home { $daemon_home }, or stop that daemon from it (`ctxloom daemon stop`) before using { $home }
# The daemon cluster's cap (owner ruling 2026-11-18). DRAFT wording, the
# owner's to approve: `daemon serve` that lost the single-instance lock.
ctxloom-wiring-daemon-instance-held = another ctxloom daemon holds this account's single-instance lock { $lock }, so this one does not serve
ctxloom-wiring-seed = the test seed could not plant the session: { $ext_err_txt }
ctxloom-wiring-test-refused = the test command { $verb } refused, as it always does
ctxloom-wiring-test-refused-fix = nothing to fix: ask for the other test command
ctxloom-wiring-test-findings = { $count } fatal findings
# s4-Q14 (H3): DRAFT wording, the owner's to approve. A command's flush of
# the home's holds could never deliver these (said once a session).
ctxloom-wiring-held-set-aside = set aside { $count } held facts of { $session }, which will never be recorded: { $why }; they are kept beside the session's hold under ~/.ctxloom/held, and not delivered again
# Owner, 2026-11-15 (OD-62): `ctxloom shutdown`. DRAFT wording, the owner's
# to approve.
ctxloom-wiring-shutdown-bad-time = { $value } is not a time to drain within: { $ext_err_txt }
ctxloom-wiring-shutdown-bad-time-fix = give a duration such as 15m, 1h30m or 90s, or `now` to stop everything at once
ctxloom-wiring-shutdown-incomplete = ctxloom still runs after it was asked to stop: the daemon { $daemon }; frontends { $frontends }
ctxloom-wiring-shutdown-incomplete-fix = stop each one named yourself (`kill <pid>` asks it to stop), then run `ctxloom shutdown now` again
# Owner ruling 2026-11-16 ("flush before install"). DRAFT wording, the
# owner's to approve.
ctxloom-wiring-shutdown-held = held facts no daemon has taken are left: { $holds }; a ctxloom of another version may not read them, so the shutdown is not done
ctxloom-wiring-shutdown-held-fix = run `ctxloom session list` with the ctxloom that held them (its daemon takes them on its way in), then `ctxloom shutdown` again; `ctxloom doctor` names what is still held
