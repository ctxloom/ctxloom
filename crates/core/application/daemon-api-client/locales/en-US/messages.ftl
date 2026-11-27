# OD-62 (2026-11-15): one ctxloom version runs at a time. DRAFT wording,
# the owner's to approve.
daemon-api-client-version-skew = the serving daemon is ctxloom { $daemon }, not this ctxloom { $own } (it hosts: { $harps })
daemon-api-client-version-skew-fix = stop it with `ctxloom daemon stop` (`--even-if-live` ends what it hosts), then run the command again
daemon-api-client-launch-lock = a child's launch lock could not be taken: { $ext_err_txt }
