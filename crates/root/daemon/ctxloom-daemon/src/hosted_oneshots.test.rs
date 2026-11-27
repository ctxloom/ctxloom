//! The daemon's hosted one-shots over its real core.

#[allow(
    clippy::disallowed_methods,
    reason = "a test kernel or store of its own"
)]
#[test]
fn the_core_serves_the_catalog_of_its_domains() {
    use run_application::Core;
    use std::sync::Arc;

    let runtime = tokio::runtime::Builder::new_multi_thread()
        .worker_threads(1)
        .enable_all()
        .build()
        .unwrap();
    let fs = Arc::new(file_system::Rooted::temp().unwrap());
    let core = runtime.block_on(async {
        let store = ctxloom_store_sqlite::Stores::new(fs, "/home/u")
            .open()
            .await
            .unwrap();
        crate::InProcessCore::start(store).await
    });
    assert!(
        Core::catalog(&core)
            .domains()
            .any(|d| d == sessions::SESSION_DOMAIN),
        "the sessions domain is registered"
    );
    // Closed, not dropped (ruling 131), as a run's end closes it.
    runtime.block_on(core.close()).unwrap();
}
// Owner, 2026-11-19: every run's launcher waits for a turn on a parked
// blocking thread, never an async worker (`equipped`, which `runs_over`
// builds each launcher through).
#[allow(
    clippy::disallowed_methods,
    reason = "a test kernel or store of its own"
)]
#[test]
fn every_runs_launcher_parks_its_turns() {
    use std::sync::Arc;

    let runtime = tokio::runtime::Builder::new_multi_thread()
        .worker_threads(1)
        .enable_all()
        .build()
        .unwrap();
    let dir = ctxloom_compose::fixtures::root();
    let fs = ctxloom_compose::fixtures::fs();
    let core = runtime.block_on(async {
        let store = ctxloom_store_sqlite::Stores::new(Arc::clone(&fs), dir.root().join("home"))
            .open()
            .await
            .unwrap();
        crate::InProcessCore::start(store).await
    });
    let bare = run_launcher::Launcher::new(
        core.edge(),
        host_launcher::HostLauncher::default(),
        session_files::SessionDirs::new(Arc::clone(&fs), dir.root().to_path_buf()),
        Vec::new(),
        Arc::new(run_launcher::Orders::new()),
        run_ports::Limits::new(std::time::Duration::from_secs(1)),
    );
    assert!(!bare.parks_turns(), "a new launcher parks nothing");
    let locks: Arc<dyn run_ports::RunLocks> =
        Arc::new(liveness::RunLockFiles::new(Arc::clone(&fs), dir.root()));
    let workspaces = Arc::new(worktree_git::GitWorkspaces::new(
        Arc::clone(&fs),
        dir.root().to_path_buf(),
        None,
        Arc::new(::process_env::EnvSnapshot::of(Vec::<(&str, &str)>::new())),
    ));
    let equipped = ctxloom_compose::oneshot::equipped(bare, (&locks, workspaces, "linux"));
    assert!(equipped.parks_turns());
    drop(equipped);
    runtime.block_on(core.close()).unwrap();
}
