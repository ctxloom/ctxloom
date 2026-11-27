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
