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

const HOME: &str = "/home/u";
/// A project directory: its sessions are `<project>/.ctxloom/sessions`.
const PROJECT: &str = "/work/proj";
const HARP: &str = "amber-swift-owl";

fn dirs() -> (Arc<Rooted>, SessionDirs) {
    let fs = Arc::new(Rooted::temp().unwrap());
    (fs.clone(), SessionDirs::new(fs, PROJECT))
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

// ------------------------------------------------------------- the transcript

// Go: "transcripts/transcript.jsonl", D27.
#[test]
fn the_transcript_is_where_go_put_it() {
    let (_, dirs) = dirs();
    assert_eq!(
        dirs.transcript_path(HARP),
        Path::new("/work/proj/.ctxloom/sessions/amber-swift-owl/transcripts/transcript.jsonl")
    );
}

// session.feature: "A session with no captured transcript is still named":
// absence is a row, not an omission.
#[test]
fn a_session_with_no_transcript_has_none_and_no_error() {
    let (_, mut dirs) = dirs();
    assert_eq!(dirs.transcript(HARP).unwrap(), None);
    assert_eq!(dirs.read_transcript(HARP).unwrap(), None);
}

#[test]
fn a_captured_transcript_has_its_path_and_its_size() {
    let (fs, mut dirs) = dirs();
    let path = write(&fs, &dirs, "transcripts/transcript.jsonl", "{\"v\":1}\n");
    let got = dirs.transcript(HARP).unwrap().expect("captured");
    assert_eq!(Path::new(&got.path), path);
    assert_eq!(got.bytes, 8);
    assert_eq!(
        dirs.read_transcript(HARP).unwrap().as_deref(),
        Some("{\"v\":1}\n")
    );
}

// Go: "only a regular file that stats counts as present".
#[test]
fn a_directory_where_the_transcript_should_be_is_no_transcript() {
    let (fs, mut dirs) = dirs();
    fs.create_dir_all(&dirs.transcript_path(HARP), Mode::from_bits(0o755))
        .unwrap();
    assert_eq!(dirs.transcript(HARP).unwrap(), None);
    assert_eq!(dirs.read_transcript(HARP).unwrap(), None);
}

// ----------------------------------------------------------- last activity

// Go: TestTranscriptActivity_IsTheNewestFileNotADirOrLink, and ..._IsTheConversationNotTheRecord:
// the record's write is not the session being worked on.
#[test]
fn the_last_activity_is_the_newest_file_of_the_conversation() {
    let (fs, mut dirs) = dirs();
    let old = write(&fs, &dirs, "transcripts/transcript.jsonl", "a\n");
    let newer = write(&fs, &dirs, "native/history.jsonl", "b\n");
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
    assert_eq!(
        dirs.last_active(HARP).unwrap(),
        None,
        "no transcript, no native history"
    );
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

// ----------------------------------------------------------------- destroy

#[test]
fn destroy_removes_the_named_files_and_counts_the_bytes() {
    let (fs, mut dirs) = dirs();
    let transcript = write(&fs, &dirs, "transcripts/transcript.jsonl", "abc");
    let notes = write(&fs, &dirs, "design-notes.md", "authored");
    let freed = dirs
        .destroy(HARP, &["transcripts/transcript.jsonl".to_string()])
        .unwrap();
    assert_eq!(freed, 3);
    assert!(!fs.exists(&transcript).unwrap());
    assert!(fs.exists(&notes).unwrap(), "what was not named stays");
}

#[test]
fn a_file_already_gone_is_not_a_failure() {
    let (_, mut dirs) = dirs();
    assert_eq!(
        dirs.destroy(HARP, &["transcripts/transcript.jsonl".to_string()])
            .unwrap(),
        0
    );
}

// A path that leaves the session directory is refused, and nothing is removed.
#[test]
fn destroy_never_leaves_the_session_directory() {
    let (fs, mut dirs) = dirs();
    let outside = fs.create_dir_all(
        Path::new("/work/proj/.ctxloom/sessions/other"),
        Mode::from_bits(0o755),
    );
    outside.unwrap();
    let other = Path::new("/work/proj/.ctxloom/sessions/other/precious");
    fs.write_atomic(
        other,
        b"keep",
        file_system::Perms::Exact(Mode::from_bits(0o600)),
    )
    .unwrap();
    for rel in [
        "../other/precious",
        "/work/proj/.ctxloom/sessions/other/precious",
        "a/../../other/precious",
    ] {
        let error = dirs.destroy(HARP, &[rel.to_string()]).unwrap_err();
        assert_eq!(
            angzarr_diagnostic::Diagnostic::code(&error),
            "session-files-outside",
            "{rel}"
        );
    }
    assert!(fs.exists(other).unwrap());
    let _ = Open::Read;
}

// ------------------------------------------------------- Go's session directories

use session_ports::GoSessions;

use super::GoDirs;

fn go() -> (Arc<Rooted>, GoDirs) {
    let fs = Arc::new(Rooted::temp().unwrap());
    (fs.clone(), GoDirs::new(fs, HOME))
}

/// Go's `session.yaml` of `harp`, written.
fn sidecar(fs: &Rooted, harp: &str, body: &str) {
    let path = Path::new(HOME)
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
    sidecar(&fs, "has:colon", "schema_version: 3\n");
    let got = go.go_sessions().unwrap();
    let harps: Vec<&str> = got.iter().map(|s| s.harp.as_str()).collect();
    assert_eq!(harps, ["has:colon"]);
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

// ------------------------------------------------------------ the output folder

// session.feature: "--name renames the harp and moves its output folder": a
// move, so the folder is at exactly one of its two names.
#[test]
fn an_output_folder_moves_with_its_files_and_leaves_nothing_behind() {
    let (fs, mut dirs) = dirs();
    let old = Path::new("/home/u/Documents/ctxloom/p/amber-swift-owl");
    let new = Path::new("/home/u/Documents/ctxloom/p/bright-keen-hawk");
    fs.create_dir_all(&old.join("reports"), Mode::from_bits(0o755))
        .unwrap();
    fs.write_atomic(
        &old.join("essence.md"),
        b"kept",
        file_system::Perms::Exact(Mode::from_bits(0o600)),
    )
    .unwrap();
    let moved = dirs.move_output(&old.display().to_string(), &new.display().to_string());
    assert!(moved.unwrap());
    assert!(!fs.exists(old).unwrap());
    assert_eq!(fs.read_to_string(&new.join("essence.md")).unwrap(), "kept");
    assert!(fs.exists(&new.join("reports")).unwrap());
}

// Go: renamedOutputDir: a recorded folder never written moves nothing.
#[test]
fn a_folder_that_was_never_written_moves_nothing() {
    let (_, mut dirs) = dirs();
    let moved = dirs.move_output("/home/u/Documents/p/a", "/home/u/Documents/p/b");
    assert!(!moved.unwrap());
}

// Go: ErrOutputDirExists: "a rename whose folder cannot move is refused whole".
#[test]
fn a_move_onto_something_already_there_is_refused_and_moves_nothing() {
    let (fs, mut dirs) = dirs();
    let (old, new) = ("/home/u/Documents/p/a", "/home/u/Documents/p/b");
    fs.create_dir_all(Path::new(old), Mode::from_bits(0o755))
        .unwrap();
    fs.create_dir_all(Path::new(new), Mode::from_bits(0o755))
        .unwrap();
    let error = dirs.move_output(old, new).unwrap_err();
    assert_eq!(
        angzarr_diagnostic::Diagnostic::code(&error),
        "session-files-output-exists"
    );
    assert!(fs.exists(Path::new(old)).unwrap());
}

// Only a file is removed: a directory named is left, and is no failure.
#[test]
fn a_directory_named_to_destroy_is_left_alone() {
    let (fs, mut dirs) = dirs();
    write(&fs, &dirs, "transcripts/transcript.jsonl", "abc");
    assert_eq!(dirs.destroy(HARP, &["transcripts".to_string()]).unwrap(), 0);
    assert!(fs.exists(&dirs.transcript_path(HARP)).unwrap());
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
        "/work/proj/.ctxloom/sessions/amber-swift-owl"
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

const LOGIN: &str = "/home/u/.claude/.credentials.json";
const SECRET: &str = "not-a-real-credential";

/// The user's login file, with a body that must survive everything.
fn login(fs: &Rooted) {
    fs.create_dir_all(Path::new("/home/u/.claude"), Mode::from_bits(0o755))
        .unwrap();
    fs.write_atomic(
        Path::new(LOGIN),
        SECRET.as_bytes(),
        file_system::Perms::Exact(Mode::from_bits(0o600)),
    )
    .unwrap();
}

#[test]
fn a_login_file_is_present_by_its_metadata_and_a_directory_or_gap_is_not() {
    let (fs, dirs) = dirs();
    assert!(!dirs.present(LOGIN));
    login(&fs);
    assert!(dirs.present(LOGIN));
    assert!(!dirs.present("/home/u/.claude"));
}

#[test]
fn linking_makes_the_homes_file_a_link_to_the_login_and_says_so() {
    let (fs, mut dirs) = dirs();
    login(&fs);
    assert_eq!(
        dirs.state(HARP, "home/.credentials.json"),
        LinkState::Absent
    );
    dirs.link(HARP, "home/.credentials.json", LOGIN).unwrap();
    assert_eq!(
        dirs.state(HARP, "home/.credentials.json"),
        LinkState::LinkTo(LOGIN.to_string())
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
    dirs.link(HARP, "home/other.json", "/nowhere").unwrap();
    assert_eq!(
        dirs.state(HARP, "home/other.json"),
        LinkState::LinkTo("/nowhere".to_string())
    );
}

#[test]
fn linking_replaces_nothing() {
    let (fs, mut dirs) = dirs();
    login(&fs);
    write(&fs, &dirs, "home/.credentials.json", "mine");
    assert!(dirs.link(HARP, "home/.credentials.json", LOGIN).is_err());
    let path = dirs.dir(HARP).join("home/.credentials.json");
    assert_eq!(fs.read_to_string(&path).unwrap(), "mine");
}

// Ruling 80: where a link cannot be made (opencode on Windows), the login is
// copied whole, readable by its owner alone, and nothing is replaced.
#[test]
fn copying_makes_the_homes_file_a_private_copy_of_the_login_and_replaces_nothing() {
    let (fs, mut dirs) = dirs();
    login(&fs);
    dirs.copy(HARP, "home/xdg/data/opencode/auth.json", LOGIN)
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
        Some(0o600)
    );
    assert!(dirs
        .copy(HARP, "home/xdg/data/opencode/auth.json", LOGIN)
        .is_err());
    assert!(dirs.copy(HARP, "../escape", LOGIN).is_err());
    assert!(dirs
        .copy(HARP, "home/missing.json", "/home/u/none")
        .is_err());
}

#[test]
fn a_path_out_of_the_session_directory_is_refused() {
    let (fs, mut dirs) = dirs();
    login(&fs);
    assert!(dirs.link(HARP, "../escape", LOGIN).is_err());
    assert_eq!(dirs.state(HARP, "../escape"), LinkState::Other);
}

// Removing a session must never follow the link: the user's login is not the
// session's to destroy. The survey never walks the home, and a destroy that
// names the link itself, or the whole home, removes no file through it.
#[test]
fn removing_a_session_whose_home_holds_the_link_leaves_the_login_intact() {
    let (fs, mut dirs) = dirs();
    login(&fs);
    write(&fs, &dirs, "transcripts/transcript.jsonl", "hello");
    dirs.link(HARP, "home/.credentials.json", LOGIN).unwrap();
    let surveyed = dirs.survey(HARP).unwrap();
    assert!(
        surveyed.iter().all(|item| !item.rel.starts_with("home")),
        "{surveyed:?}"
    );
    let rels: Vec<String> = surveyed.iter().map(|item| item.rel.clone()).collect();
    dirs.destroy(HARP, &rels).unwrap();
    // Even a destroy that names the link is not a removal of its target.
    dirs.destroy(HARP, &["home/.credentials.json".to_string()])
        .unwrap();
    assert_eq!(fs.read_to_string(Path::new(LOGIN)).unwrap(), SECRET);
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
    assert_eq!(
        dirs.state(HARP, "blocked/.credentials.json"),
        LinkState::Other
    );
    // And a gap really is absent.
    assert_eq!(dirs.state(HARP, "home/never"), LinkState::Absent);
}
