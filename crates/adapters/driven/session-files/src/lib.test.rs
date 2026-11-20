//! A session's files. Go: `statHarpFile` (session_transcript.go),
//! `TranscriptActivity` (retention.go, retention_test.go), `PurgeSession`'s
//! survey and classes (session_purge.go), release/0.7.

use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::{Duration, SystemTime};

use file_system::{Fs, Open, Rooted};
use os::Mode;
use session_ports::{SessionFiles, Timestamp};

use super::SessionDirs;

/// `path`, absolute and spelled the unix way (`/home/u`), as this host
/// spells it (`C:\home\u` on Windows): what the adapter is handed and shows
/// back. A fixture's own write may stay unix-spelled, since the rooted
/// filesystem re-roots both spellings at one place.
fn host_path(path: &str) -> String {
    os::host().flavor().from_top(path)
}

/// The home Go's sessions are under.
fn home() -> String {
    host_path("/home/u")
}

/// A project directory: its sessions are `<project>/.ctxloom/sessions`.
fn project() -> String {
    host_path("/work/proj")
}

/// `bits` where the host keeps modes (unix); none on Windows, which keeps
/// none.
fn kept(bits: u32) -> Option<u32> {
    (os::host().flavor() == os::Flavor::Unix).then_some(bits)
}

/// Whether the host links to a file (unix's symlink); Windows' links are
/// directories' (a junction), which the product never makes to a login there
/// (it copies one: ruling 80), so the test that reads a login through a link
/// is skipped, and says so.
fn links_files() -> bool {
    if os::host().flavor() == os::Flavor::Unix {
        return true;
    }
    let test = std::thread::current();
    eprintln!(
        "SKIP {}: the host links directories only, and a login is a file",
        test.name().unwrap_or("a test")
    );
    false
}

/// Whether a look under a file fails otherwise than "not found" (unix's
/// ENOTDIR); Windows reports such a path as not found, so the test (or the
/// part of it) that tells the two apart is skipped, and says so: `SKIP
/// <test>: <why>`.
fn tells_a_path_through_a_file() -> bool {
    if os::host().flavor() == os::Flavor::Unix {
        return true;
    }
    let test = std::thread::current();
    eprintln!(
        "SKIP {} (a path through a file): the host reports it as not found",
        test.name().unwrap_or("a test")
    );
    false
}
const HARP: &str = "amber-swift-owl";

fn dirs() -> (Arc<Rooted>, SessionDirs) {
    let fs = Arc::new(Rooted::temp().unwrap());
    (fs.clone(), SessionDirs::new(fs, project()))
}

/// Write `body` at `rel` under `harp`'s directory.
fn write(fs: &Rooted, dirs: &SessionDirs, rel: &str, body: &str) -> PathBuf {
    let path = dirs.dir(HARP).join(rel);
    fs.create_dir_all(path.parent().unwrap(), Mode::from_bits(0o755))
        .unwrap();
    fs.write_atomic(
        &path,
        body.as_bytes(),
        file_system::Perms::Exact(Mode::from_bits(0o600)),
    )
    .unwrap();
    path
}

/// Set a file's mtime, on the real file the seam re-roots it to.
fn touch(fs: &Rooted, path: &Path, seconds: u64) {
    let file = fs.open(path, Open::Append(Mode::FILE)).unwrap();
    file.set_modified(SystemTime::UNIX_EPOCH + Duration::from_secs(seconds))
        .unwrap();
}

fn at(seconds: i64) -> Option<Timestamp> {
    Some(Timestamp { seconds, nanos: 0 })
}

// ------------------------------------------------------------------- places

// The lock is beside the session directory, never inside it.
#[test]
fn the_lock_is_beside_the_session_directory() {
    let (_, dirs) = dirs();
    assert_eq!(
        dirs.lock_path(HARP),
        Path::new(&host_path(
            "/work/proj/.ctxloom/sessions/amber-swift-owl.lock"
        ))
    );
}

// ----------------------------------------------------------- last activity

// Go: TestTranscriptActivity_IsTheNewestFileNotADirOrLink, and ..._IsTheConversationNotTheRecord:
// the record's write is not the session being worked on. A transcript file
// an older ctxloom left is not the conversation either (ruling 87a: the
// session's capture is in its events).
#[test]
fn the_last_activity_is_the_newest_file_of_the_native_history() {
    let (fs, mut dirs) = dirs();
    let old = write(&fs, &dirs, "native/old.jsonl", "a\n");
    let newer = write(&fs, &dirs, "native/history.jsonl", "b\n");
    let stale = write(&fs, &dirs, "transcripts/transcript.jsonl", "c\n");
    touch(&fs, &stale, 8_000);
    let record = write(&fs, &dirs, "keep", "");
    touch(&fs, &old, 1_000);
    touch(&fs, &newer, 2_000);
    touch(&fs, &record, 9_000);
    assert_eq!(dirs.last_active(HARP).unwrap(), at(2_000));
}

#[test]
fn a_session_with_no_conversation_has_no_activity() {
    let (fs, mut dirs) = dirs();
    assert_eq!(dirs.last_active(HARP).unwrap(), None, "no directory at all");
    write(&fs, &dirs, "keep", "");
    assert_eq!(dirs.last_active(HARP).unwrap(), None, "no native history");
}

// ------------------------------------------------------------------ survey

// Go: the purge survey: persisted members only; the ephemeral rows are never
// walked ("a purge runs zero git commands").
#[test]
fn the_survey_names_the_persisted_files_and_never_walks_the_ephemeral_members() {
    let (fs, dirs_) = dirs();
    let mut dirs = dirs_;
    write(&fs, &dirs, "transcripts/transcript.jsonl", "abc");
    write(&fs, &dirs, "transcripts/segments/one.jsonl", "de");
    write(&fs, &dirs, "native/projects/p/s.jsonl", "f");
    write(&fs, &dirs, "design-notes.md", "authored");
    write(&fs, &dirs, "work/checkout/wip.txt", "uncommitted");
    write(&fs, &dirs, "home/claude/settings.json", "{}");
    write(&fs, &dirs, "locks/x.lock", "");
    write(&fs, &dirs, "scratch/tmp", "");
    write(&fs, &dirs, "session.yaml", "Go's identity member");
    let got = dirs.survey(HARP).unwrap();
    let rels: Vec<(&str, u64)> = got.iter().map(|f| (f.rel.as_str(), f.bytes)).collect();
    assert_eq!(
        rels,
        [
            ("design-notes.md", 8),
            ("native/projects/p/s.jsonl", 1),
            ("transcripts/segments/one.jsonl", 2),
            ("transcripts/transcript.jsonl", 3),
        ]
    );
    assert_eq!(
        Path::new(&got[0].path),
        dirs.dir(HARP).join("design-notes.md"),
        "absolute, as the caller names it"
    );
}

#[test]
fn the_survey_of_a_session_with_no_directory_is_empty() {
    let (_, mut dirs) = dirs();
    assert_eq!(dirs.survey(HARP).unwrap(), Vec::new());
}

// ------------------------------------------------------- Go's session directories

use session_ports::GoSessions;

use super::GoDirs;

fn go() -> (Arc<Rooted>, GoDirs) {
    let fs = Arc::new(Rooted::temp().unwrap());
    (fs.clone(), GoDirs::new(fs, home()))
}

/// Go's `session.yaml` of `harp`, written.
fn sidecar(fs: &Rooted, harp: &str, body: &str) {
    let path = Path::new(&home())
        .join(".ctxloom/sessions")
        .join(harp)
        .join("session.yaml");
    fs.create_dir_all(path.parent().unwrap(), Mode::from_bits(0o755))
        .unwrap();
    fs.write_atomic(
        &path,
        body.as_bytes(),
        file_system::Perms::Exact(Mode::from_bits(0o600)),
    )
    .unwrap();
}

// Go: a root that does not exist yet reads as no sessions (first run).
#[test]
fn a_home_with_no_sessions_root_has_no_go_sessions() {
    let (_, mut go) = go();
    assert_eq!(go.go_sessions().unwrap(), Vec::new());
}

// Go: IsSessionDir, "THE predicate for 'this entry under the sessions root is
// a session'": a real directory, a usable harp, a regular sidecar.
#[test]
fn a_go_session_is_a_directory_with_a_sidecar_and_the_listing_is_by_harp() {
    let (fs, mut go) = go();
    sidecar(&fs, "brisk-copper-moth", "schema_version: 3\n");
    sidecar(&fs, "amber-swift-owl", "schema_version: 2\nkeep: true\n");
    let got = go.go_sessions().unwrap();
    let harps: Vec<&str> = got.iter().map(|s| s.harp.as_str()).collect();
    assert_eq!(harps, ["amber-swift-owl", "brisk-copper-moth"]);
    assert_eq!(
        got[0].sidecar.as_deref(),
        Ok("schema_version: 2\nkeep: true\n")
    );
    assert!(!got[0].keep_marker);
}

// Go: "A directory WITHOUT the sidecar is not a session. That is what `session
// remove` leaves behind on purpose, and what an engine-created directory
// nobody ever recorded looks like; neither is invented into a listing."
#[test]
fn a_directory_without_a_sidecar_is_not_a_session() {
    let (fs, mut go) = go();
    fs.create_dir_all(
        Path::new("/home/u/.ctxloom/sessions/forgotten-quiet-heron/transcripts"),
        Mode::from_bits(0o755),
    )
    .unwrap();
    assert_eq!(go.go_sessions().unwrap(), Vec::new());
}

// Go: the sessions root also holds the lock files beside the directories. A
// directory's name is not judged here: the port returns every one that
// carries a sidecar, and the import applies the harp rule.
#[test]
fn a_lock_file_is_not_a_session_and_a_name_is_not_judged() {
    let (fs, mut go) = go();
    fs.write_atomic(
        Path::new("/home/u/.ctxloom/sessions/amber-swift-owl.lock"),
        b"1234\n",
        file_system::Perms::Exact(Mode::from_bits(0o600)),
    )
    .unwrap_or_else(|_| {
        fs.create_dir_all(
            Path::new("/home/u/.ctxloom/sessions"),
            Mode::from_bits(0o755),
        )
        .unwrap();
    });
    // No harp (the rule refuses capitals and an underscore), and a name every
    // host can make a directory of.
    sidecar(&fs, "Not_A_Harp", "schema_version: 3\n");
    let got = go.go_sessions().unwrap();
    let harps: Vec<&str> = got.iter().map(|s| s.harp.as_str()).collect();
    assert_eq!(harps, ["Not_A_Harp"]);
}

// Go: "never a symlink, in either direction — a sweep that followed one would
// act on whatever it points at".
#[test]
fn a_symlinked_directory_and_a_symlinked_sidecar_are_not_sessions() {
    let (fs, mut go) = go();
    sidecar(&fs, "real-quiet-heron", "schema_version: 3\n");
    let sessions = Path::new("/home/u/.ctxloom/sessions");
    fs.symlink_dir(
        &sessions.join("real-quiet-heron"),
        &sessions.join("linked-quiet-heron"),
    )
    .unwrap();
    let dir = sessions.join("loose-quiet-heron");
    fs.create_dir_all(&dir, Mode::from_bits(0o755)).unwrap();
    fs.symlink_dir(
        &sessions.join("real-quiet-heron/session.yaml"),
        &dir.join("session.yaml"),
    )
    .unwrap();
    let harps: Vec<String> = go
        .go_sessions()
        .unwrap()
        .into_iter()
        .map(|s| s.harp)
        .collect();
    assert_eq!(harps, ["real-quiet-heron"]);
}

// The hand-placed `keep` file: Go's older mark, which the plan migrates
// (`KeptChanged` from the marker's presence).
#[test]
fn a_keep_marker_file_is_noted() {
    let (fs, mut go) = go();
    sidecar(&fs, "amber-swift-owl", "schema_version: 3\n");
    let marker = Path::new("/home/u/.ctxloom/sessions/amber-swift-owl/keep");
    fs.write_atomic(
        marker,
        b"",
        file_system::Perms::Exact(Mode::from_bits(0o600)),
    )
    .unwrap();
    assert!(go.go_sessions().unwrap()[0].keep_marker);
}

// Go: "one corrupt session must not hide every other": a sidecar that cannot
// be read is that session's answer, not the listing's failure.
#[test]
fn a_sidecar_that_cannot_be_read_does_not_hide_the_others() {
    let (fs, mut go) = go();
    sidecar(&fs, "amber-swift-owl", "schema_version: 3\n");
    let path = Path::new("/home/u/.ctxloom/sessions/brisk-copper-moth/session.yaml");
    fs.create_dir_all(path.parent().unwrap(), Mode::from_bits(0o755))
        .unwrap();
    fs.write_atomic(
        path,
        &[0xff, 0xfe, 0x00],
        file_system::Perms::Exact(Mode::from_bits(0o600)),
    )
    .unwrap();
    let got = go.go_sessions().unwrap();
    assert_eq!(got.len(), 2);
    assert!(got[0].sidecar.is_ok());
    assert!(got[1].sidecar.is_err());
}

// Reading Go's directories writes nothing (the importer leaves them exactly as
// they are).
#[test]
fn reading_writes_nothing() {
    let (fs, mut go) = go();
    sidecar(&fs, "amber-swift-owl", "schema_version: 3\n");
    let before = dirs_tree(&fs);
    go.go_sessions().unwrap();
    assert_eq!(dirs_tree(&fs), before);
}

fn dirs_tree(fs: &Rooted) -> Vec<String> {
    let mut seen = Vec::new();
    let mut stack = vec![PathBuf::from("/home/u")];
    while let Some(dir) = stack.pop() {
        if let Ok(entries) = fs.read_dir(&dir) {
            for entry in entries {
                let path = dir.join(&entry.name);
                seen.push(path.display().to_string());
                stack.push(path);
            }
        }
    }
    seen.sort();
    seen
}

// ------------------------------------------------------- the records port
// Step 1b-ii: the driver writes the session's directory from the events,
// through this port.

#[test]
fn a_records_write_makes_the_directories_above_it() {
    use run_ports::SessionRecords;
    let (fs, mut dirs) = dirs();
    SessionRecords::write(&mut dirs, HARP, "home/ctxloom-context.md", b"CTX").unwrap();
    assert_eq!(
        fs.read(&dirs.dir(HARP).join("home/ctxloom-context.md"))
            .unwrap(),
        b"CTX"
    );
    assert_eq!(
        SessionRecords::dir(&dirs, HARP),
        host_path("/work/proj/.ctxloom/sessions/amber-swift-owl")
    );
}

#[test]
fn a_records_write_replaces_what_was_there() {
    use run_ports::SessionRecords;
    let (_, mut dirs) = dirs();
    SessionRecords::write(&mut dirs, HARP, "metadata.json", b"first").unwrap();
    SessionRecords::write(&mut dirs, HARP, "metadata.json", b"2nd").unwrap();
    assert_eq!(
        SessionRecords::read(&mut dirs, HARP, "metadata.json").unwrap(),
        Some(b"2nd".to_vec())
    );
}

#[test]
fn a_records_append_creates_the_file_then_adds_to_its_end() {
    use run_ports::SessionRecords;
    let (_, mut dirs) = dirs();
    SessionRecords::append(&mut dirs, HARP, "records/runs.jsonl", b"a\n").unwrap();
    SessionRecords::append(&mut dirs, HARP, "records/runs.jsonl", b"b\n").unwrap();
    assert_eq!(
        SessionRecords::read(&mut dirs, HARP, "records/runs.jsonl").unwrap(),
        Some(b"a\nb\n".to_vec())
    );
}

#[test]
fn a_records_read_of_a_file_that_is_not_there_is_none() {
    use run_ports::SessionRecords;
    let (_, mut dirs) = dirs();
    assert_eq!(
        SessionRecords::read(&mut dirs, HARP, "metadata.json").unwrap(),
        None
    );
}

#[test]
fn a_records_path_that_leaves_the_directory_writes_nothing() {
    use run_ports::{RecordsError, SessionRecords};
    let (fs, mut dirs) = dirs();
    for rel in ["../escape", "/abs", "a//b", ""] {
        let bad = SessionRecords::write(&mut dirs, HARP, rel, b"x");
        assert!(matches!(bad, Err(RecordsError::BadPath { .. })), "{rel}");
        let bad = SessionRecords::append(&mut dirs, HARP, rel, b"x");
        assert!(matches!(bad, Err(RecordsError::BadPath { .. })), "{rel}");
        let bad = SessionRecords::read(&mut dirs, HARP, rel);
        assert!(matches!(bad, Err(RecordsError::BadPath { .. })), "{rel}");
    }
    assert!(!fs.exists(&dirs.dir("..").join("escape")).unwrap_or(false));
}

#[test]
fn a_harp_that_is_not_one_name_is_refused_too() {
    use run_ports::{RecordsError, SessionRecords};
    let (_, mut dirs) = dirs();
    let bad = SessionRecords::write(&mut dirs, "../other", "a", b"x");
    assert!(matches!(bad, Err(RecordsError::BadPath { .. })));
}

#[test]
fn a_records_failure_names_the_file_and_the_systems_own_text() {
    use run_ports::{RecordsError, SessionRecords};
    let (fs, mut dirs) = dirs();
    // A file where a directory must go.
    write(&fs, &dirs, "home", "in the way");
    let bad = SessionRecords::write(&mut dirs, HARP, "home/x", b"x").unwrap_err();
    match bad {
        RecordsError::Io { harp, rel, .. } => {
            assert_eq!((harp.as_str(), rel.as_str()), (HARP, "home/x"));
        }
        other => panic!("{other:?}"),
    }
}

// Only a file that is not there reads as none: a path that cannot be read
// for any other reason is the error, with the file and the system's words.
#[test]
fn a_records_read_that_fails_otherwise_than_by_absence_is_the_error() {
    if !tells_a_path_through_a_file() {
        return;
    }
    use run_ports::{RecordsError, SessionRecords};
    let (fs, mut dirs) = dirs();
    // A file where a directory must be: reading under it is not "not found".
    write(&fs, &dirs, "home", "in the way");
    let bad = SessionRecords::read(&mut dirs, HARP, "home/x").unwrap_err();
    match bad {
        RecordsError::Io { harp, rel, .. } => {
            assert_eq!((harp.as_str(), rel.as_str()), (HARP, "home/x"));
        }
        other => panic!("{other:?}"),
    }
}

// ------------------------------------------------------------ the login link

use run_ports::{LinkState, LoginLinks};

/// The user's login file, absolute on every host: a link names it as given.
fn login_file() -> String {
    host_path("/home/u/.claude/.credentials.json")
}
const SECRET: &str = "not-a-real-credential";

/// The user's login file, with a body that must survive everything.
fn login(fs: &Rooted) {
    fs.create_dir_all(Path::new("/home/u/.claude"), Mode::from_bits(0o755))
        .unwrap();
    fs.write_atomic(
        Path::new(&login_file()),
        SECRET.as_bytes(),
        file_system::Perms::Exact(Mode::from_bits(0o600)),
    )
    .unwrap();
}

#[test]
fn a_login_file_is_present_by_its_metadata_and_a_directory_or_gap_is_not() {
    let (fs, dirs) = dirs();
    assert!(!dirs.present(&login_file()));
    login(&fs);
    assert!(dirs.present(&login_file()));
    assert!(!dirs.present("/home/u/.claude"));
}

#[test]
fn linking_makes_the_homes_file_a_link_to_the_login_and_says_so() {
    if !links_files() {
        return;
    }
    let (fs, mut dirs) = dirs();
    login(&fs);
    assert_eq!(
        dirs.state(HARP, "home/.credentials.json"),
        LinkState::Absent
    );
    dirs.link(HARP, "home/.credentials.json", &login_file())
        .unwrap();
    assert_eq!(
        dirs.state(HARP, "home/.credentials.json"),
        LinkState::LinkTo(login_file())
    );
    // Read through the link it is the login; ctxloom read nothing to say so.
    let linked = dirs.dir(HARP).join("home/.credentials.json");
    assert_eq!(fs.read_to_string(&linked).unwrap(), SECRET);
}

#[test]
fn a_plain_file_is_other_and_a_dangling_link_is_still_a_link() {
    let (fs, mut dirs) = dirs();
    write(&fs, &dirs, "home/.credentials.json", "mine");
    assert_eq!(dirs.state(HARP, "home/.credentials.json"), LinkState::Other);
    dirs.link(HARP, "home/other.json", &host_path("/nowhere"))
        .unwrap();
    assert_eq!(
        dirs.state(HARP, "home/other.json"),
        LinkState::LinkTo(host_path("/nowhere"))
    );
}

#[test]
fn linking_replaces_nothing() {
    let (fs, mut dirs) = dirs();
    login(&fs);
    write(&fs, &dirs, "home/.credentials.json", "mine");
    assert!(dirs
        .link(HARP, "home/.credentials.json", &login_file())
        .is_err());
    let path = dirs.dir(HARP).join("home/.credentials.json");
    assert_eq!(fs.read_to_string(&path).unwrap(), "mine");
}

// Ruling 80: where a link cannot be made (opencode on Windows), the login is
// copied whole, readable by its owner alone, and nothing is replaced.
#[test]
fn copying_makes_the_homes_file_a_private_copy_of_the_login_and_replaces_nothing() {
    let (fs, mut dirs) = dirs();
    login(&fs);
    dirs.copy(HARP, "home/xdg/data/opencode/auth.json", &login_file())
        .unwrap();
    let copied = dirs.dir(HARP).join("home/xdg/data/opencode/auth.json");
    assert_eq!(fs.read_to_string(&copied).unwrap(), SECRET);
    assert_eq!(
        dirs.state(HARP, "home/xdg/data/opencode/auth.json"),
        LinkState::Other,
        "a copy is no link"
    );
    assert_eq!(
        fs.metadata(&copied).unwrap().mode.map(|m| m.bits() & 0o777),
        kept(0o600)
    );
    assert!(dirs
        .copy(HARP, "home/xdg/data/opencode/auth.json", &login_file())
        .is_err());
    assert!(dirs.copy(HARP, "../escape", &login_file()).is_err());
    assert!(dirs
        .copy(HARP, "home/missing.json", "/home/u/none")
        .is_err());
}

#[test]
fn a_path_out_of_the_session_directory_is_refused() {
    let (fs, mut dirs) = dirs();
    login(&fs);
    assert!(dirs.link(HARP, "../escape", &login_file()).is_err());
    assert_eq!(dirs.state(HARP, "../escape"), LinkState::Other);
}

// Removing a session must never follow the link: the user's login is not the
// session's to destroy. The survey never walks the home, and the destroy of
// the whole directory removes the link, never what it names.
#[test]
fn removing_a_session_whose_home_holds_the_link_leaves_the_login_intact() {
    let (fs, mut dirs) = dirs();
    login(&fs);
    write(&fs, &dirs, "transcripts/transcript.jsonl", "hello");
    dirs.link(HARP, "home/.credentials.json", &login_file())
        .unwrap();
    let surveyed = dirs.survey(HARP).unwrap();
    assert!(
        surveyed.iter().all(|item| !item.rel.starts_with("home")),
        "{surveyed:?}"
    );
    dirs.destroy_session(HARP).unwrap();
    assert!(!fs.exists(&dirs.dir(HARP)).unwrap());
    assert_eq!(fs.read_to_string(Path::new(&login_file())).unwrap(), SECRET);
}

#[test]
fn a_directory_at_the_link_path_is_other_and_a_failed_look_is_not_absent() {
    let (fs, dirs) = dirs();
    fs.create_dir_all(
        &dirs.dir(HARP).join("home/.credentials.json"),
        Mode::from_bits(0o755),
    )
    .unwrap();
    assert_eq!(dirs.state(HARP, "home/.credentials.json"), LinkState::Other);
    // `home` is a file, so nothing under it can be looked at: that is a
    // failure to find out (not a directory), never "absent".
    write(&fs, &dirs, "blocked", "x");
    if tells_a_path_through_a_file() {
        assert_eq!(
            dirs.state(HARP, "blocked/.credentials.json"),
            LinkState::Other
        );
    }
    // And a gap really is absent.
    assert_eq!(dirs.state(HARP, "home/never"), LinkState::Absent);
}

// Defect 3: a harp whose directory exists is taken, whatever the store says.
#[test]
fn a_records_harp_exists_once_its_directory_does() {
    use run_ports::SessionRecords;
    let (fs, mut dirs) = dirs();
    assert!(!SessionRecords::exists(&mut dirs, HARP));
    fs.create_dir_all(&dirs.dir(HARP), Mode::from_bits(0o755))
        .unwrap();
    assert!(SessionRecords::exists(&mut dirs, HARP));
    assert!(!SessionRecords::exists(&mut dirs, "brisk-copper-moth"));
}

// Ruling 94b: a destroy removes the session's directory whole (the home too)
// and its lock, and counts what it freed; a session with none is no failure.
#[test]
fn a_destroy_removes_the_whole_directory_and_its_lock() {
    let (fs, mut dirs) = dirs();
    write(&fs, &dirs, "home/settings.json", "{}");
    write(&fs, &dirs, "native/history.jsonl", "abc");
    fs.write_atomic(
        &dirs.lock_path(HARP),
        b"",
        file_system::Perms::Exact(Mode::from_bits(0o600)),
    )
    .unwrap();
    assert_eq!(dirs.destroy_session(HARP).unwrap(), 5);
    assert!(!fs.exists(&dirs.dir(HARP)).unwrap());
    assert!(!fs.exists(&dirs.lock_path(HARP)).unwrap());
    assert_eq!(
        dirs.destroy_session(HARP).unwrap(),
        0,
        "again: nothing there"
    );
}

// Ruling 94b: a lock that is not there is no failure of a destroy; one that
// cannot be removed (a directory in its place) is.
#[test]
fn a_lock_that_cannot_be_removed_is_the_destroys_error() {
    let (fs, mut dirs) = dirs();
    write(&fs, &dirs, "home/settings.json", "{}");
    fs.create_dir_all(&dirs.lock_path(HARP).join("inside"), Mode::from_bits(0o755))
        .unwrap();
    assert!(dirs.destroy_session(HARP).is_err());
}
