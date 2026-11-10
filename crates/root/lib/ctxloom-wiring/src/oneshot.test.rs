use ports::{Project, ProjectIdentity};

use super::{output_root, run_env, sync_at_start, Told};
use process_env::EnvSnapshot;
use std::path::{Path, PathBuf};

fn project() -> Project {
    Project {
        id: "calm-otter".to_string(),
        root: Some("/work/proj".to_string()),
        store: "/h/.ctxloom/projects/calm-otter/ctxloom.db".into(),
    }
}

// The project is told once, before the store is opened, and the use case is
// then given the answer it already has.
#[test]
fn a_project_already_told_is_told_again_as_it_was() {
    let mut told = Told(project());
    assert_eq!(told.for_write(None).unwrap(), project());
    assert_eq!(told.for_read(None).unwrap(), Some(project()));
    assert_eq!(told.every_project().unwrap(), vec![project()]);
    assert_eq!(told.rootless_directory().unwrap(), None);
}

// Step 1b-ii, and the journey's promise: Alice leaves by deleting `.ctxloom`
// and `~/.ctxloom`, so a project that is told for a run is marked inside
// `.ctxloom` (the registry's marker setting), never at the work root.
mod told {
    use std::collections::VecDeque;
    use std::path::Path;
    use std::sync::Arc;

    use angzarr_diagnostic::Diagnostic;
    use file_system::{Fs, Perms, Rooted};
    use os::Mode;
    use ports::Names;
    use project_registry::Environment;

    use super::super::tell_project;

    struct Scripted(VecDeque<&'static str>);

    impl Names for Scripted {
        fn task_id(&mut self) -> String {
            unreachable!("a project is named with three words")
        }
        fn task_id_for(&mut self, _: &str, _: u64) -> String {
            unreachable!("a project is named with three words")
        }
        fn project_id(&mut self) -> String {
            self.0.pop_front().expect("a name was scripted").to_string()
        }
    }

    const PROJECT: &str = "/work/proj";

    fn world() -> (Arc<Rooted>, Environment) {
        let fs = Arc::new(Rooted::temp_with_cwd(Path::new(PROJECT)).unwrap());
        fs.create_dir_all(Path::new(PROJECT), Mode::from_bits(0o755))
            .unwrap();
        fs.create_dir_all(Path::new("/home/u"), Mode::from_bits(0o755))
            .unwrap();
        let env = Environment {
            home: Some("/home/u".into()),
            root: None,
            project_id: None,
        };
        (fs, env)
    }

    fn text(fs: &Rooted, path: &str) -> Option<String> {
        fs.read_to_string(Path::new(path)).ok()
    }

    fn mark(fs: &Rooted, path: &str, id: &str) {
        if let Some(dir) = Path::new(path).parent() {
            fs.create_dir_all(dir, Mode::from_bits(0o755)).unwrap();
        }
        fs.write_atomic(
            Path::new(path),
            format!("{id}\n").as_bytes(),
            Perms::Exact(Mode::from_bits(0o644)),
        )
        .unwrap();
    }

    #[test]
    fn a_new_project_is_named_and_marked_inside_ctxloom_alone() {
        let (fs, env) = world();
        let project = tell_project(fs.clone(), env, Scripted(["jolly-mute-magma"].into())).unwrap();
        assert_eq!(project.id, "jolly-mute-magma");
        assert_eq!(project.root.as_deref(), Some(PROJECT));
        assert_eq!(
            text(&fs, "/work/proj/.ctxloom/project-id").as_deref(),
            Some("jolly-mute-magma\n")
        );
        assert!(!fs.exists(Path::new("/work/proj/.project-id")).unwrap());
    }

    #[test]
    fn the_same_project_is_told_again_as_it_was() {
        let (fs, env) = world();
        tell_project(
            fs.clone(),
            env.clone(),
            Scripted(["jolly-mute-magma"].into()),
        )
        .unwrap();
        // No second name is scripted: none may be drawn.
        let again = tell_project(fs.clone(), env, Scripted(VecDeque::new())).unwrap();
        assert_eq!(again.id, "jolly-mute-magma");
        assert!(!fs.exists(Path::new("/work/proj/.project-id")).unwrap());
    }

    #[test]
    fn a_legacy_root_marker_keeps_its_id_and_is_adopted_into_ctxloom() {
        let (fs, env) = world();
        mark(&fs, "/work/proj/.project-id", "calm-otter");
        let project = tell_project(fs.clone(), env, Scripted(VecDeque::new())).unwrap();
        assert_eq!(project.id, "calm-otter");
        assert_eq!(
            text(&fs, "/work/proj/.ctxloom/project-id").as_deref(),
            Some("calm-otter\n")
        );
        // The root marker is left where it is.
        assert_eq!(
            text(&fs, "/work/proj/.project-id").as_deref(),
            Some("calm-otter\n")
        );
    }

    #[test]
    fn markers_that_disagree_are_refused_with_the_registrys_code() {
        let (fs, env) = world();
        mark(&fs, "/work/proj/.project-id", "calm-otter");
        mark(&fs, "/work/proj/.ctxloom/project-id", "bold-heron");
        let refused = tell_project(fs, env, Scripted(VecDeque::new())).unwrap_err();
        assert_eq!(refused.code(), "project-registry-marker-conflict");
    }
}

// A turn has half an hour: the limit the launcher is given.
#[test]
fn a_turn_has_half_an_hour() {
    assert_eq!(super::TURN_DEADLINE, std::time::Duration::from_secs(1800));
}

// The place the run is told is the path as it is spelled, whole.
#[test]
fn a_path_is_told_as_it_is_spelled() {
    assert_eq!(super::path_text(std::path::Path::new("/a/b c")), "/a/b c");
}

// The core the run use cases take serves its own catalog: the registered
// domains, which the sessions are among (a default catalog holds none).
#[tokio::test]
async fn the_core_serves_the_catalog_of_its_domains() {
    use run_application::Core;
    use std::sync::Arc;

    let fs = Arc::new(file_system::Rooted::temp().unwrap());
    let store = ctxloom_store_sqlite::Stores::new(fs, "/home/u")
        .open("calm-otter")
        .await
        .unwrap();
    let core = crate::InProcessCore::start(store).await;
    assert!(
        Core::catalog(&core)
            .domains()
            .any(|d| d == sessions::SESSION_DOMAIN),
        "the sessions domain is registered"
    );
}

// Owner ruling 2026-11-08: the engine is given ctxloom's home by name. The
// run's environment carries the home the wiring resolved under the variable,
// whatever the caller had (or had not) set, beside everything the caller had.
#[test]
fn the_run_environment_names_the_resolved_home() {
    let caller = EnvSnapshot::of([
        ("HOME", "/home/u"),
        ("PATH", "/bin"),
        ("CTXLOOM_HOME", "/old"),
    ]);
    let env = run_env(&caller, Path::new("/home/u"));
    assert_eq!(env.get("CTXLOOM_HOME").map(String::as_str), Some("/home/u"));
    assert_eq!(env.get("HOME").map(String::as_str), Some("/home/u"));
    assert_eq!(env.get("PATH").map(String::as_str), Some("/bin"));
}

// The second gap of the listener spike: the run waits for its engine in a
// BLOCKING call, so a task spawned on the run's runtime (the listener) must
// make progress while it waits. The test forces the interleaving: the
// "engine wait" is a blocking receive that only the spawned task can satisfy.
mod runtime_shape {
    use std::sync::mpsc;
    use std::time::Duration;

    use super::super::run_runtime;

    /// Spawn a task that signals, then block the driving thread on a std
    /// channel, as `Running::finish` blocks on a process: whether the signal
    /// arrived while blocked.
    fn signalled_while_blocked(runtime: &tokio::runtime::Runtime, wait: Duration) -> bool {
        let (tx, rx) = mpsc::channel();
        runtime.block_on(async move {
            tokio::spawn(async move {
                let _ = tx.send(());
            });
            // Blocking, on the thread that drives the use case.
            rx.recv_timeout(wait).is_ok()
        })
    }

    #[test]
    fn a_task_on_the_run_runtime_progresses_while_the_blocking_wait_holds_the_driver() {
        let runtime = run_runtime().unwrap();
        // A generous bound: the task is runnable at once, so this passes as
        // soon as a worker runs it; it fails (never hangs) if none can.
        assert!(signalled_while_blocked(&runtime, Duration::from_secs(30)));
    }

    // The control that makes the test above mean something: on a
    // current-thread runtime the same interleaving starves, and for certain,
    // since the only thread is the blocked one.
    #[test]
    fn a_current_thread_runtime_starves_the_same_task() {
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .unwrap();
        assert!(!signalled_while_blocked(
            &runtime,
            Duration::from_millis(200)
        ));
    }
}

// The run's hook listener, wired over the real in-process core: served through
// the port the run use case holds, on a loopback port of the system's choosing
// that is open while it serves and closed once it is stopped; an unspecified
// address is refused with the listener's own code, which the run reports.
mod hooks {
    use std::net::{IpAddr, Ipv4Addr};
    use std::sync::Arc;

    use angzarr_diagnostic::Diagnostic;
    use run_ports::HookServer;

    use super::super::Hooks;

    async fn hooks() -> Hooks<impl run_ports::Edge + 'static> {
        let store = {
            let home = Arc::new(file_system::Rooted::temp().unwrap());
            ctxloom_store_sqlite::Stores::new(home, "/home/u")
                .open("calm-otter")
                .await
                .unwrap()
        };
        let core = crate::InProcessCore::start(store).await;
        Hooks { edge: core.edge() }
    }

    /// The run's real certificate, minted by the wiring's own mint.
    fn identity() -> run_ports::HookCertificate {
        use run_ports::CertMint;
        hook_tls::Minter::new(hook_tls::SystemClock)
            .mint(&run_ports::CertNames::new(
                vec![IpAddr::V4(Ipv4Addr::LOCALHOST)],
                Vec::new(),
            ))
            .unwrap()
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn the_run_use_case_serves_and_stops_the_listener_through_the_port() {
        let hooks = hooks().await;
        let serving = hooks
            .serve_tcp(IpAddr::V4(Ipv4Addr::LOCALHOST), &identity())
            .await
            .expect("serves");
        let port = serving.port();
        assert_ne!(port, 0, "the port the system gave is reported");
        assert!(
            tokio::net::TcpStream::connect((Ipv4Addr::LOCALHOST, port))
                .await
                .is_ok(),
            "bound when serve_tcp returns"
        );
        serving.stop().await;
        assert!(
            tokio::net::TcpStream::connect((Ipv4Addr::LOCALHOST, port))
                .await
                .is_err(),
            "closed on stop"
        );
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn two_runs_get_two_ports() {
        let hooks = hooks().await;
        let a = hooks
            .serve_tcp(IpAddr::V4(Ipv4Addr::LOCALHOST), &identity())
            .await
            .unwrap();
        let b = hooks
            .serve_tcp(IpAddr::V4(Ipv4Addr::LOCALHOST), &identity())
            .await
            .unwrap();
        assert_ne!(a.port(), b.port());
        a.stop().await;
        b.stop().await;
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn an_unspecified_address_is_refused_with_the_listeners_code() {
        let hooks = hooks().await;
        let refused = hooks
            .serve_tcp(IpAddr::V4(Ipv4Addr::UNSPECIFIED), &identity())
            .await
            .err()
            .expect("refused");
        assert_eq!(refused.code(), "hook-listener-bind-unspecified");
    }
}

// Owner rulings 89, 106: a session's outputs go under `output.dir`, relative
// to the project root unless absolute; a trailing slash says nothing more.
#[test]
fn the_output_root_is_the_output_directory_against_the_project_root() {
    let project = Path::new("/work/proj");
    assert_eq!(
        output_root(project, "ctxloom/"),
        PathBuf::from("/work/proj/ctxloom")
    );
    assert_eq!(
        output_root(project, "notes/sessions"),
        PathBuf::from("/work/proj/notes/sessions")
    );
    assert_eq!(output_root(project, "/var/out/"), PathBuf::from("/var/out"));
}

// Owner rulings 59, 60: `sync.on_run: missing` (the default) installs, at a
// run's start, what the profiles reference and is not installed; `none`
// pulls nothing. A pull that fails (offline) is said and the run goes on with
// what is installed; one that installed nothing says nothing.
#[test]
fn a_run_installs_the_missing_dependencies_at_its_start_unless_on_run_is_none() {
    use library_application::OnRun;
    use std::cell::Cell;
    let asked = Cell::new(0);
    let pull = |answer: Result<u64, String>| {
        let asked = &asked;
        move || {
            asked.set(asked.get() + 1);
            answer
        }
    };
    assert!(sync_at_start(OnRun::None, pull(Ok(3))).is_empty());
    assert_eq!(asked.get(), 0, "none pulls nothing");
    assert!(sync_at_start(OnRun::Missing, pull(Ok(0))).is_empty());
    assert_eq!(asked.get(), 1);
    let said = |notices: Vec<library_application::Notice>| -> Vec<(String, String)> {
        notices
            .into_iter()
            .map(|n| match n {
                library_application::Notice::Finding { text, remedy } => (text, remedy),
                other => panic!("{other:?}"),
            })
            .collect()
    };
    assert_eq!(
        said(sync_at_start(OnRun::Missing, pull(Ok(2)))),
        [(
            "installed 2 missing dependencies at the start of the run (sync.on_run: missing)"
                .to_string(),
            String::new()
        )]
    );
    assert_eq!(
        said(sync_at_start(
            OnRun::Missing,
            pull(Err("1 could not be installed".to_string()))
        )),
        [(
            "could not install the missing dependencies at the start of the run: 1 could not be installed; the run goes on with what is installed".to_string(),
            "run `ctxloom deps pull` once the remotes are reachable, or set `sync.on_run: none` to skip this".to_string()
        )]
    );
}
