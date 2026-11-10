# Development against the sibling checkouts: a generated, gitignored
# .cargo/config.toml [patch] that points the whole git dependency family at
# its path references. Nothing path-based is ever committed.

# Generate .cargo/config.toml: a [patch."<git url>"] per git source in Cargo.lock.
[group('dev')]
[doc("Patch every git dependency to its sibling checkout: dev-local [root] (default ~/workspace)")]
[no-exit-message]
dev-local root="":
    @python3 -I {{TOP}}/scripts/dev-local.py --project {{TOP}} {{root}}

# Remove the generated .cargo/config.toml and restore Cargo.lock.
[group('dev')]
[doc("Undo dev-local: remove the generated config, restore Cargo.lock")]
[no-exit-message]
dev-local-off:
    @{{TOP}}/scripts/dev-local-off.sh {{TOP}}
