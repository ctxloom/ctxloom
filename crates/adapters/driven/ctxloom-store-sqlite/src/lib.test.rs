use std::path::Path;
use std::sync::Arc;

use file_system::{Fs, Rooted};

use session_ports::ProjectStores;

use super::Stores;

/// The one store's file (T14): every project's streams, under ctxloom's home.
const DB: &str = "/home/u/.ctxloom/ctxloom.db";

#[test]
fn test_the_one_store_is_ctxloom_db_in_the_homes_app_dir() {
    let stores = Stores::new(Arc::new(Rooted::temp().unwrap()), "/home/u");
    assert_eq!(stores.path(), Path::new(DB));
}

#[tokio::test]
async fn test_open_makes_the_app_dir_and_the_database_under_the_home() {
    let fs = Arc::new(Rooted::temp().unwrap());
    let stores = Stores::new(fs.clone(), "/home/u");
    stores.open().await.unwrap().close().await.unwrap();
    assert!(fs.exists(Path::new(DB)).unwrap());
    assert!(
        !fs.exists(Path::new("/home/u/.ctxloom/projects")).unwrap(),
        "no project directory is made for it"
    );
}

// ------------------------------------------------------- the ProjectStores port

fn project(id: &str) -> ports::Project {
    ports::Project {
        id: id.to_string(),
        root: None,
        store: std::path::PathBuf::from(format!("/home/u/.ctxloom/projects/{id}/state.db")),
    }
}

// Reading the record touches nothing (session.feature): a project with no
// database has none, and asking makes none.
#[tokio::test]
async fn test_open_existing_finds_no_store_and_makes_none() {
    let fs = Arc::new(Rooted::temp().unwrap());
    let mut stores = super::SqliteStores::new(fs.clone(), "/home/u");
    assert!(
        ProjectStores::open_existing(&mut stores, &project("jolly-mute-magma"))
            .await
            .unwrap()
            .is_none()
    );
    assert!(
        !fs.exists(Path::new("/home/u/.ctxloom")).unwrap(),
        "not even the app dir"
    );
}

#[tokio::test]
async fn test_open_makes_the_store_and_a_later_open_existing_finds_it() {
    let fs = Arc::new(Rooted::temp().unwrap());
    let mut stores = super::SqliteStores::new(fs.clone(), "/home/u");
    ProjectStores::open(&mut stores, &project("jolly-mute-magma"))
        .await
        .unwrap();
    assert!(fs.exists(Path::new(DB)).unwrap());
    // Another handle, as another process's read: the file is there.
    let mut reader = super::SqliteStores::new(fs, "/home/u");
    assert!(
        ProjectStores::open_existing(&mut reader, &project("jolly-mute-magma"))
            .await
            .unwrap()
            .is_some()
    );
}

// Every project's streams are in the one store (T14): a store opened for one
// project serves another's read, and only the one file is made.
#[tokio::test]
async fn test_every_project_is_served_by_the_one_store() {
    let fs = Arc::new(Rooted::temp().unwrap());
    let mut stores = super::SqliteStores::new(fs.clone(), "/home/u");
    ProjectStores::open(&mut stores, &project("jolly-mute-magma"))
        .await
        .unwrap();
    assert!(
        ProjectStores::open_existing(&mut stores, &project("brisk-tall-oak"))
            .await
            .unwrap()
            .is_some()
    );
    stores.close().await.unwrap();
    assert!(!fs.exists(&per_project("jolly-mute-magma")).unwrap());
    assert!(
        fs.exists(Path::new("/home/u/.ctxloom/projects/jolly-mute-magma"))
            .unwrap(),
        "a write lists its project, as taskloom's registry finds projects"
    );
    assert!(
        !fs.exists(Path::new("/home/u/.ctxloom/projects/brisk-tall-oak"))
            .unwrap(),
        "a read lists none"
    );
}

// A store held to write serves a write again without opening afresh, and a
// read-only one is replaced by one opened to write.
#[tokio::test]
async fn test_a_read_only_store_is_replaced_when_a_write_asks() {
    let fs = Arc::new(Rooted::temp().unwrap());
    let mut first = super::SqliteStores::new(fs.clone(), "/home/u");
    ProjectStores::open(&mut first, &project("jolly-mute-magma"))
        .await
        .unwrap();
    let mut stores = super::SqliteStores::new(fs, "/home/u");
    assert!(
        ProjectStores::open_existing(&mut stores, &project("jolly-mute-magma"))
            .await
            .unwrap()
            .is_some()
    );
    assert!(
        ProjectStores::open(&mut stores, &project("jolly-mute-magma"))
            .await
            .is_ok()
    );
}

// ---------------------------------------------------------------- characterization
//
// Where the one database is, what asking makes and what it does not.

use super::{OpenError, StoreOpenError};
use angzarr_diagnostic::Diagnostic;
use angzarr_embedded::angzarr_ports::Arg as PortArg;
use angzarr_embedded::StoreError;
use os::Mode;

#[test]
fn test_the_path_is_the_home_the_app_dir_and_the_file_name() {
    let stores = Stores::new(Arc::new(Rooted::temp().unwrap()), "/h");
    let path = stores.path();
    let parts: Vec<_> = path.iter().map(|p| p.to_str().unwrap()).collect();
    assert_eq!(parts, ["/", "h", ".ctxloom", "ctxloom.db"]);
}

#[test]
fn test_the_path_is_relative_to_a_relative_home() {
    let stores = Stores::new(Arc::new(Rooted::temp().unwrap()), "rel/home");
    assert_eq!(stores.path(), Path::new("rel/home/.ctxloom/ctxloom.db"));
}

#[tokio::test]
async fn test_open_twice_is_the_same_database_and_finds_it_the_second_time() {
    let fs = Arc::new(Rooted::temp().unwrap());
    let stores = Stores::new(fs.clone(), "/home/u");
    stores.open().await.unwrap().close().await.unwrap();
    stores.open().await.unwrap().close().await.unwrap();
    let found = stores.open_existing().await.unwrap().expect("there");
    found.close().await.unwrap();
}

#[tokio::test]
async fn test_open_existing_with_the_app_dir_but_no_database_finds_none_and_makes_none() {
    let fs = Arc::new(Rooted::temp().unwrap());
    fs.create_dir_all(Path::new("/home/u/.ctxloom"), Mode::from_bits(0o755))
        .unwrap();
    let stores = Stores::new(fs.clone(), "/home/u");
    assert!(stores.open_existing().await.unwrap().is_none());
    assert!(!fs.exists(Path::new(DB)).unwrap());
}

#[tokio::test]
async fn test_a_home_whose_app_dir_is_a_file_cannot_hold_a_store() {
    let fs = Arc::new(Rooted::temp().unwrap());
    fs.create_dir_all(Path::new("/home/u"), Mode::from_bits(0o755))
        .unwrap();
    fs.write_atomic(
        Path::new("/home/u/.ctxloom"),
        b"a file",
        file_system::Perms::Exact(Mode::from_bits(0o644)),
    )
    .unwrap();
    let stores = Stores::new(fs, "/home/u");
    let Err(StoreOpenError::Open(error @ OpenError::CreateDir { .. })) = stores.open().await else {
        panic!("the directory cannot be made");
    };
    let OpenError::CreateDir { path, .. } = &error else {
        unreachable!()
    };
    assert_eq!(path, "/home/u/.ctxloom");
    assert_eq!(error.code(), "sqlite-store-opener-create-dir");
}

#[tokio::test]
async fn test_the_port_places_the_store_under_the_home_not_by_the_projects_store_field() {
    // `Project.store` (taskloom's state.db) is ignored: ours is the one ctxloom.db
    let fs = Arc::new(Rooted::temp().unwrap());
    let mut stores = super::SqliteStores::new(fs.clone(), "/home/u");
    let mut elsewhere = project("p");
    elsewhere.store = std::path::PathBuf::from("/elsewhere/state.db");
    ProjectStores::open(&mut stores, &elsewhere).await.unwrap();
    stores.close().await.unwrap();
    assert!(fs.exists(Path::new(DB)).unwrap());
    assert!(!fs.exists(Path::new("/elsewhere")).unwrap());
}

// ------------------------------------------------ a store of one project's own, from before (T17)
//
// S48: a project that still has `~/.ctxloom/projects/<id>/ctxloom.db` is
// refused, its fix naming the file to remove; nothing is imported from it,
// and it is left exactly as it was.

/// Where `project`'s own store was, before the one store.
fn per_project(project: &str) -> std::path::PathBuf {
    Path::new("/home/u/.ctxloom/projects")
        .join(project)
        .join("ctxloom.db")
}

fn old_store(fs: &Rooted, project: &str) -> Vec<u8> {
    let path = per_project(project);
    fs.create_dir_all(path.parent().unwrap(), Mode::from_bits(0o755))
        .unwrap();
    fs.write_atomic(
        &path,
        b"an old store",
        file_system::Perms::Exact(Mode::from_bits(0o644)),
    )
    .unwrap();
    fs.read(&path).unwrap()
}

#[test]
fn test_a_per_project_store_is_found_where_it_was_and_named_in_the_refusal() {
    let fs = Arc::new(Rooted::temp().unwrap());
    let before = old_store(&fs, "jolly-mute-magma");
    let stores = Stores::new(fs.clone(), "/home/u");
    let error = stores
        .refuse_per_project("jolly-mute-magma")
        .expect_err("refused");
    assert!(matches!(error, StoreOpenError::PerProjectStore { .. }));
    assert_eq!(error.code(), "ctxloom-store-sqlite-per-project-store");
    let shown = format!("{:?}", error.args());
    assert!(
        shown.contains("/home/u/.ctxloom/projects/jolly-mute-magma/ctxloom.db"),
        "{shown}"
    );
    assert!(stores.refuse_per_project("brisk-tall-oak").is_ok());
    assert_eq!(fs.read(&per_project("jolly-mute-magma")).unwrap(), before);
}

#[tokio::test]
async fn test_the_port_refuses_a_project_with_a_store_of_its_own_and_makes_nothing() {
    let fs = Arc::new(Rooted::temp().unwrap());
    let before = old_store(&fs, "jolly-mute-magma");
    let mut stores = super::SqliteStores::new(fs.clone(), "/home/u");
    let wanted = project("jolly-mute-magma");
    for error in [
        ProjectStores::open(&mut stores, &wanted).await.err(),
        ProjectStores::open_existing(&mut stores, &wanted)
            .await
            .err(),
    ] {
        let Some(StoreError::Unavailable(cause)) = error else {
            panic!("not refused: {error:?}");
        };
        assert_eq!(cause.code, "ctxloom-store-sqlite-per-project-store");
        assert_eq!(
            cause.args,
            [(
                "path",
                PortArg::Str(per_project("jolly-mute-magma").display().to_string())
            )]
        );
    }
    assert!(!fs.exists(Path::new(DB)).unwrap(), "the one store not made");
    assert_eq!(fs.read(&per_project("jolly-mute-magma")).unwrap(), before);
    // Another project, with none of its own, is served.
    ProjectStores::open(&mut stores, &project("brisk-tall-oak"))
        .await
        .unwrap();
    stores.close().await.unwrap();
}

/// An unsafe id names no per-project path: refused as before.
#[test]
fn test_an_unsafe_project_id_is_refused_with_a_coded_diagnostic() {
    let stores = Stores::new(Arc::new(Rooted::temp().unwrap()), "/h");
    let cases = [
        ("", "project-dir-id-empty"),
        ("/etc", "project-dir-id-absolute"),
        ("../x", "project-dir-id-separator"),
        ("a/b", "project-dir-id-separator"),
        ("..", "project-dir-id-relative"),
        ("a\0b", "project-dir-id-nul"),
    ];
    for (id, code) in cases {
        let error = stores.refuse_per_project(id).expect_err(id);
        assert_eq!(error.code(), code, "{id:?}");
        assert!(matches!(error, StoreOpenError::UnsafeProject(_)), "{id:?}");
    }
}

#[tokio::test]
async fn test_the_port_refuses_an_unsafe_project_id_too() {
    let fs = Arc::new(Rooted::temp().unwrap());
    let mut stores = super::SqliteStores::new(fs.clone(), "/h");
    assert!(ProjectStores::open(&mut stores, &project("a/b"))
        .await
        .is_err());
    assert!(ProjectStores::open_existing(&mut stores, &project("a/b"))
        .await
        .is_err());
    assert!(!fs.exists(Path::new("/h")).unwrap());
}

// ------------------------------------------------ a store from before the embedded store
//
// D7 (the extraction plan): ctxloom inherits the opener's refusal: an earlier
// event store's file at `ctxloom.db` is refused, naming the backup to move it
// to, and left exactly as it was.

/// An earlier event store's file: one stream of two events under its rules,
/// its ledger only its first migration. Frozen in testdata.
const PRE_EMBEDDED: &[u8] = include_bytes!("../testdata/pre-embedded.db");

const DB_DIR: &str = "/home/u/.ctxloom";

fn earlier_store(fs: &Rooted) -> Vec<u8> {
    fs.create_dir_all(Path::new(DB_DIR), Mode::from_bits(0o755))
        .unwrap();
    let db = Path::new(DB);
    fs.write_atomic(
        db,
        PRE_EMBEDDED,
        file_system::Perms::Exact(Mode::from_bits(0o644)),
    )
    .unwrap();
    fs.read(db).unwrap()
}

fn refusal_args(error: &dyn Diagnostic) -> Vec<(String, String)> {
    assert_eq!(error.code(), "sqlite-store-opener-predates-embedded");
    error
        .args()
        .into_iter()
        .map(|(name, arg)| match arg {
            angzarr_diagnostic::Arg::Str(text) => (name.to_string(), text),
            other => panic!("{name}: {other:?}"),
        })
        .collect()
}

#[tokio::test]
async fn test_a_store_that_predates_the_embedded_store_is_refused_to_write_and_to_read_and_left_as_it_was(
) {
    let fs = Arc::new(Rooted::temp().unwrap());
    let before = earlier_store(&fs);
    let stores = Stores::new(fs.clone(), "/home/u");
    let expected = vec![
        ("path".to_string(), DB.to_string()),
        ("backup".to_string(), format!("{DB}.pre-embedded")),
    ];
    let error = stores.open().await.err().expect("refused");
    assert_eq!(refusal_args(&error), expected, "to write");
    let error = stores.open_existing().await.err().expect("refused");
    let StoreError::Unavailable(cause) = error else {
        panic!("not Unavailable: {error:?}");
    };
    assert_eq!(cause.code, "sqlite-store-opener-predates-embedded");
    assert_eq!(
        cause.args,
        [
            ("path", PortArg::Str(expected[0].1.clone())),
            ("backup", PortArg::Str(expected[1].1.clone())),
        ],
        "to read"
    );
    assert_eq!(
        fs.read(Path::new(DB)).unwrap(),
        before,
        "the file as it was: not migrated"
    );
}

#[tokio::test]
async fn test_the_port_refuses_a_store_that_predates_the_embedded_store_and_holds_nothing() {
    let fs = Arc::new(Rooted::temp().unwrap());
    earlier_store(&fs);
    let mut stores = super::SqliteStores::new(fs.clone(), "/home/u");
    let wanted = project("jolly-mute-magma");
    let error = ProjectStores::open(&mut stores, &wanted)
        .await
        .err()
        .expect("refused to write");
    let StoreError::Unavailable(cause) = &error else {
        panic!("not Unavailable: {error:?}");
    };
    assert_eq!(cause.code, "sqlite-store-opener-predates-embedded");
    let error = ProjectStores::open_existing(&mut stores, &wanted)
        .await
        .err()
        .expect("refused to read");
    let StoreError::Unavailable(cause) = &error else {
        panic!("not Unavailable: {error:?}");
    };
    assert_eq!(cause.code, "sqlite-store-opener-predates-embedded");
}

// ------------------------------------------------ closed, not dropped (ruling 131)
//
// A store's last close checkpoints its log into the file and removes the log
// and its index. Dropped, a store is closed on the driver's thread whenever
// that runs, which a process that exits first never lets it do; closed, it is
// over when the close returns.

/// What is beside the one database: its log and its index, where left.
fn beside(fs: &Rooted) -> Vec<&'static str> {
    let dir = Path::new(DB_DIR);
    ["ctxloom.db-wal", "ctxloom.db-shm"]
        .into_iter()
        .filter(|name| fs.exists(&dir.join(name)).unwrap())
        .collect()
}

#[tokio::test]
async fn test_a_closed_store_leaves_nothing_beside_its_file_and_reopens_whole() {
    let fs = Arc::new(Rooted::temp().unwrap());
    let mut stores = super::SqliteStores::new(fs.clone(), "/home/u");
    // Opened to write: made and migrated, the migrations' commits in the log.
    ProjectStores::open(&mut stores, &project("p"))
        .await
        .unwrap();
    stores.close().await.unwrap();
    assert_eq!(beside(&fs), Vec::<&str>::new(), "closed: nothing beside");
    // What was written is in the file: it opens, read-only, as a store.
    let reopened = Stores::new(fs.clone(), "/home/u")
        .open_existing()
        .await
        .unwrap()
        .expect("the file is a store");
    reopened.close().await.unwrap();
    // Closing what holds nothing is nothing.
    stores.close().await.unwrap();
}

#[tokio::test]
async fn test_another_project_s_open_reuses_the_one_store_held() {
    let fs = Arc::new(Rooted::temp().unwrap());
    let mut stores = super::SqliteStores::new(fs.clone(), "/home/u");
    ProjectStores::open(&mut stores, &project("a"))
        .await
        .unwrap();
    fs.remove_file(Path::new(DB)).unwrap();
    ProjectStores::open(&mut stores, &project("b"))
        .await
        .unwrap();
    assert!(
        !fs.exists(Path::new(DB)).unwrap(),
        "reused: not reopened, so no file was made again"
    );
    stores.close().await.unwrap();
}

#[tokio::test]
async fn test_a_store_held_to_read_is_closed_before_it_is_reopened_to_write() {
    let fs = Arc::new(Rooted::temp().unwrap());
    Stores::new(fs.clone(), "/home/u")
        .open()
        .await
        .unwrap()
        .close()
        .await
        .unwrap();
    let mut stores = super::SqliteStores::new(fs.clone(), "/home/u");
    assert!(ProjectStores::open_existing(&mut stores, &project("p"))
        .await
        .unwrap()
        .is_some());
    ProjectStores::open(&mut stores, &project("p"))
        .await
        .unwrap();
    // The write's close is the file's last only if the read's was over
    // first: a read connection still open would keep the log.
    stores.close().await.unwrap();
    assert_eq!(beside(&fs), Vec::<&str>::new());
}

// ------------------------------------------------ a held store is reused, not reopened
//
// A second open is served by the held store: nothing is closed and nothing
// opened afresh. Seen from the file: once the database is unlinked under the
// held connection, a reuse leaves it gone (the connection keeps its inode),
// where a close and reopen would make the file again (to write) or find none
// (to read).

#[tokio::test]
async fn test_a_second_open_to_write_reuses_the_held_store() {
    let fs = Arc::new(Rooted::temp().unwrap());
    let mut stores = super::SqliteStores::new(fs.clone(), "/home/u");
    ProjectStores::open(&mut stores, &project("p"))
        .await
        .unwrap();
    fs.remove_file(Path::new(DB)).unwrap();
    ProjectStores::open(&mut stores, &project("p"))
        .await
        .unwrap();
    assert!(
        !fs.exists(Path::new(DB)).unwrap(),
        "reused: not reopened, so no file was made again"
    );
    stores.close().await.unwrap();
}

#[tokio::test]
async fn test_a_read_reuses_the_held_store() {
    let fs = Arc::new(Rooted::temp().unwrap());
    let mut stores = super::SqliteStores::new(fs.clone(), "/home/u");
    ProjectStores::open(&mut stores, &project("p"))
        .await
        .unwrap();
    fs.remove_file(Path::new(DB)).unwrap();
    assert!(
        ProjectStores::open_existing(&mut stores, &project("q"))
            .await
            .unwrap()
            .is_some(),
        "reused: a reopen would find no file"
    );
    stores.close().await.unwrap();
}
