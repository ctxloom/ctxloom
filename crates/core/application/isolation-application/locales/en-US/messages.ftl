# Messages for the crate's errors. Each id is a code, which begins with the
# crate's name; the variables are its args. An id ending -fix is the remedy of the one before.

isolation-application-no-session = no session named { $harp }
isolation-application-nothing-removed = ctxloom: removed no worktree for { $harp } ({ $spared } spared, { $skipped } skipped) - nothing was provably safe to remove
isolation-application-keeps-session = refusing to remove session { $harp }: { $spared } of its worktrees are spared and { $skipped } skipped, and a session is removed whole or not at all. Nothing was removed.
isolation-application-keeps-session-fix = commit or discard the work in each spared worktree, or wait for the session to end, then remove it again
isolation-application-unknown-backend = unknown backend "{ $backend }" (available: { $available })
isolation-application-unknown-runtime = unknown container runtime "{ $runtime }" ({ $known })
isolation-application-no-prune-runtime = container prune: no container runtime is available to prune
isolation-application-no-prune-runtime-fix = install and start docker or podman
isolation-application-no-build-runtime = container build: no container runtime is available to build with
isolation-application-no-build-runtime-fix = install and start docker or podman
isolation-application-devcontainer-exists = this project already has a devcontainer ({ $path }); edit it rather than scaffolding a new one - the agent image builds from it unless isolation_base says otherwise
isolation-application-not-ported = { $command } needs a live container runtime, and that part is not ported yet
