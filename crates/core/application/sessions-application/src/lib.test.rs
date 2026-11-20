//! The sessions use cases, over fakes of the ports and angzarr-embedded's
//! kernel on an in-memory store. Each test names the Go scenario or test it
//! ports (session.feature, j001200, j001300, `operations`/`cli` tests,
//! release/0.7), or says it is design.

use angzarr_diagnostic::Diagnostic;
use session_ports::LockState;

use crate::fakes::{
    bound, captured, claim, ended, holder, id_text, minted, project, ready, FakeClock, FakeDelay,
    FakeFiles, FakeIdentity, FakeLiveness, FakeStores,
};
use crate::{
    AppError, BindRequest, Bound, Class, KeepRequest, ListQuery, Refusal, RemoveRequest,
    SearchQuery, Service, SessionService, ShowQuery, State, TranscriptQuery,
};

type Svc = Service<FakeIdentity, FakeStores, FakeFiles, FakeLiveness, FakeClock, FakeDelay>;

/// The fake clock reads from second 1_700_000_001: this is "now" (roughly) to
/// a test of retention.
const NOW: i64 = 1_700_000_000;
const HOUR: i64 = 3600;
const DAY: i64 = 24 * HOUR;
const HERE: &str = "jolly-mute-magma";
const ELSEWHERE: &str = "brisk-tall-oak";

fn service() -> Svc {
    Service::new(
        FakeIdentity {
            bound: Some(project(HERE)),
            every: vec![project(ELSEWHERE), project(HERE)],
            fails: None,
        },
        FakeStores::default(),
        FakeFiles::default(),
        FakeLiveness::default(),
        FakeClock::default(),
        FakeDelay::default(),
    )
}

/// The canonical transcript Go's fixture records: one user prompt.
fn transcript_of(harp: &str) -> String {
    format!(
        r#"{{"v":1,"harp":"{harp}","engine":"claude-code","seq":0,"ts":"2026-01-01T00:00:00Z","kind":"entry","entry":{{"type":"user","content":"Seeded prompt for {harp}."}}}}
"#
    )
}

impl Svc {
    /// A session of the project `project`, minted at `started`, with its
    /// transcript captured and its lock `lock` (Go's fixtures: "a finished
    /// session ... with a transcript").
    fn seed(&mut self, project: &str, id: u8, harp: &str, started: i64, lock: Option<LockState>) {
        let store = self.stores.stores.entry(project.to_string()).or_default();
        minted(store, id, harp, "/work/proj", started);
        // Its hooks delivered its first prompt (ruling 87a: the transcript is
        // the session's events); an older ctxloom's transcript file is left
        // on disk too, for a removal to name.
        captured(
            store,
            id,
            started + 1,
            "p0",
            &format!("Seeded prompt for {harp}."),
        );
        self.files
            .transcripts
            .insert(harp.to_string(), transcript_of(harp));
        if let Some(lock) = lock {
            self.liveness.0.insert(harp.to_string(), lock);
        }
    }

    fn store(&mut self, project: &str) -> &angzarr_embedded::angzarr_ports::memory::MemoryStore {
        self.stores.stores.get(project).expect("a store")
    }

    fn harps(&mut self, query: ListQuery) -> Vec<String> {
        ready(self.list(query))
            .unwrap()
            .into_iter()
            .map(|v| v.record.harp)
            .collect()
    }
}

fn all() -> ListQuery {
    ListQuery {
        all: true,
        ..ListQuery::default()
    }
}

fn state(state: State) -> ListQuery {
    ListQuery {
        all: true,
        state: Some(state),
        ..ListQuery::default()
    }
}

// ------------------------------------------------------------------ reading

// session.feature: "The bare noun lists what has been recorded".
#[test]
fn a_listing_holds_the_sessions_of_the_current_project() {
    let mut svc = service();
    svc.seed(HERE, 1, "amber-swift-owl", NOW - 2 * DAY, None);
    svc.seed(HERE, 2, "brisk-copper-moth", NOW - DAY, None);
    svc.seed(ELSEWHERE, 3, "calm-dusty-wren", NOW - DAY, None);
    assert_eq!(
        svc.harps(ListQuery::default()),
        ["brisk-copper-moth", "amber-swift-owl"],
        "this project's, most recently started first"
    );
}

// Design (queue-runner swap, unnamed-corsage): a read catches up fresh, so
// each of two consecutive reads sees a record written between them.
#[test]
fn each_of_two_consecutive_reads_sees_a_record_written_between_them() {
    let mut svc = service();
    svc.seed(HERE, 1, "amber-swift-owl", NOW - 2 * DAY, None);
    assert_eq!(svc.harps(ListQuery::default()), ["amber-swift-owl"]);
    svc.seed(HERE, 2, "brisk-copper-moth", NOW - DAY, None);
    assert_eq!(
        svc.harps(ListQuery::default()),
        ["brisk-copper-moth", "amber-swift-owl"],
        "the second read sees the record written after the first"
    );
    svc.seed(HERE, 3, "calm-dusty-wren", NOW, None);
    assert_eq!(
        svc.harps(ListQuery::default()),
        ["calm-dusty-wren", "brisk-copper-moth", "amber-swift-owl"]
    );
}

// session.feature: "--all reaches past the current project".
#[test]
fn all_reaches_past_the_current_project() {
    let mut svc = service();
    svc.seed(HERE, 1, "amber-swift-owl", NOW - 2 * DAY, None);
    svc.seed(ELSEWHERE, 3, "calm-dusty-wren", NOW - DAY, None);
    assert_eq!(svc.harps(all()), ["calm-dusty-wren", "amber-swift-owl"]);
}

// session.feature: "A fresh project has recorded nothing": an empty list, not
// an error and not nothing.
#[test]
fn a_fresh_project_has_no_sessions_and_a_read_creates_no_store() {
    let mut svc = service();
    assert_eq!(svc.harps(ListQuery::default()), Vec::<String>::new());
    assert_eq!(svc.harps(all()), Vec::<String>::new());
    assert!(
        svc.stores.opened.is_empty(),
        "reading the record touches nothing"
    );
}

// Design: a directory in no project has no sessions to list.
#[test]
fn a_directory_in_no_project_lists_nothing() {
    let mut svc = service();
    svc.identity.bound = None;
    assert_eq!(svc.harps(ListQuery::default()), Vec::<String>::new());
}

// session.feature: "The listing takes each session's state from its lock, not
// its end stamp": held is live, free is ended, no lock file is unknown, and a
// live session's end is blank rather than a stamp from its previous run.
#[test]
fn the_state_is_the_locks_and_a_live_session_has_no_end() {
    let mut svc = service();
    svc.seed(
        HERE,
        1,
        "amber-swift-owl",
        NOW - 3 * DAY,
        Some(LockState::Held),
    );
    svc.seed(
        HERE,
        2,
        "brisk-copper-moth",
        NOW - 2 * DAY,
        Some(LockState::Free),
    );
    svc.seed(HERE, 3, "calm-dusty-wren", NOW - DAY, None);
    for (id, harp) in [
        (1, "amber-swift-owl"),
        (2, "brisk-copper-moth"),
        (3, "calm-dusty-wren"),
    ] {
        let _ = harp;
        ended(svc.store(HERE), id, NOW - HOUR, NOW - 2 * HOUR);
    }
    let views = ready(svc.list(all())).unwrap();
    let by = |harp: &str| {
        views
            .iter()
            .find(|v| v.record.harp == harp)
            .unwrap()
            .clone()
    };
    assert_eq!(by("amber-swift-owl").state, State::Live);
    assert_eq!(
        by("amber-swift-owl").end(),
        None,
        "the record's stamp is the LAST run's"
    );
    assert_eq!(by("brisk-copper-moth").state, State::Ended);
    assert!(by("brisk-copper-moth").end().is_some());
    assert_eq!(by("calm-dusty-wren").state, State::Unknown);
}

// session.feature: "--state keeps only the sessions in that state" (list).
#[test]
fn state_keeps_only_the_sessions_in_that_state() {
    let mut svc = service();
    svc.seed(
        HERE,
        1,
        "amber-swift-owl",
        NOW - 3 * DAY,
        Some(LockState::Held),
    );
    svc.seed(
        HERE,
        2,
        "brisk-copper-moth",
        NOW - 2 * DAY,
        Some(LockState::Free),
    );
    svc.seed(HERE, 3, "calm-dusty-wren", NOW - DAY, None);
    assert_eq!(svc.harps(state(State::Live)), ["amber-swift-owl"]);
    assert_eq!(svc.harps(state(State::Ended)), ["brisk-copper-moth"]);
    assert_eq!(svc.harps(state(State::Unknown)), ["calm-dusty-wren"]);
}

// session.feature: "Showing a session prints its record, titled by its first
// prompt": the listing carries the title.
#[test]
fn a_session_is_titled_by_the_first_prompt_of_its_transcript() {
    let mut svc = service();
    svc.seed(HERE, 1, "amber-swift-owl", NOW - DAY, None);
    let views = ready(svc.list(all())).unwrap();
    assert_eq!(
        views[0].title.as_deref(),
        Some("Seeded prompt for amber-swift-owl.")
    );
    assert_eq!(views[0].summary(), "Seeded prompt for amber-swift-owl.");
    assert!(views[0].captured.is_some(), "a transcript");
}

// j001200: a session with no captured transcript has no title and no
// transcript, and is still listed.
#[test]
fn a_session_with_no_transcript_is_listed_with_no_title() {
    let mut svc = service();
    svc.seed_captured(1, "amber-swift-owl", NOW - DAY, &[]);
    let views = ready(svc.list(all())).unwrap();
    assert_eq!(views[0].title, None);
    assert_eq!(views[0].captured, None);
    assert_eq!(views[0].summary(), "(no summary)");
}

// j001200: "A phrase from the question she asked finds the session that
// answered it", beside an unrelated session the search could have returned.
#[test]
fn a_search_finds_the_session_whose_title_has_the_words() {
    let mut svc = service();
    svc.seed_captured(
        1,
        "amber-quiet-heron",
        NOW - 3 * DAY,
        &["J001200-TITLE-WORKTREE-QUESTION why this layout"],
    );
    svc.seed(HERE, 2, "brisk-copper-moth", NOW - 2 * DAY, None);
    let found = ready(svc.search(SearchQuery {
        list: all(),
        words: vec!["J001200-TITLE-WORKTREE-QUESTION".to_string()],
    }))
    .unwrap();
    let harps: Vec<&str> = found.iter().map(|v| v.record.harp.as_str()).collect();
    assert_eq!(harps, ["amber-quiet-heron"]);
}

// j001200: "A query that matches nothing says so": an empty result, which the
// edge renders; the use case does not fail.
#[test]
fn a_search_that_matches_nothing_is_an_empty_result() {
    let mut svc = service();
    svc.seed(HERE, 1, "amber-quiet-heron", NOW - 3 * DAY, None);
    let found = ready(svc.search(SearchQuery {
        list: all(),
        words: vec!["J001200-MATCHES-ABSOLUTELY-NOTHING".to_string()],
    }))
    .unwrap();
    assert!(found.is_empty());
}

// session.feature: "--state keeps only the sessions in that state" (search).
#[test]
fn a_search_is_narrowed_by_state_as_a_listing_is() {
    let mut svc = service();
    svc.seed(
        HERE,
        1,
        "amber-swift-owl",
        NOW - 3 * DAY,
        Some(LockState::Held),
    );
    svc.seed(
        HERE,
        2,
        "brisk-copper-moth",
        NOW - 2 * DAY,
        Some(LockState::Free),
    );
    let found = ready(svc.search(SearchQuery {
        list: state(State::Ended),
        words: vec!["seeded".to_string()],
    }))
    .unwrap();
    let harps: Vec<&str> = found.iter().map(|v| v.record.harp.as_str()).collect();
    assert_eq!(harps, ["brisk-copper-moth"]);
}

#[test]
fn show_answers_one_session_with_its_record() {
    let mut svc = service();
    svc.seed(HERE, 1, "amber-swift-owl", NOW - DAY, Some(LockState::Free));
    bound(
        svc.store(HERE),
        1,
        NOW - DAY + 10,
        "native-1",
        "/vendor/t.jsonl",
    );
    let view = ready(svc.show(ShowQuery {
        harp: "amber-swift-owl".to_string(),
        ..ShowQuery::default()
    }))
    .unwrap();
    assert_eq!(view.record.engine, "claude-code");
    assert_eq!(view.record.native_id, "native-1");
    assert_eq!(view.state, State::Ended);
    assert_eq!(
        view.title.as_deref(),
        Some("Seeded prompt for amber-swift-owl.")
    );
}

// Go: GetSession is global: a harp in another project is still found.
#[test]
fn show_finds_a_session_of_another_project() {
    let mut svc = service();
    svc.seed(ELSEWHERE, 3, "calm-dusty-wren", NOW - DAY, None);
    let view = ready(svc.show(ShowQuery {
        harp: "calm-dusty-wren".to_string(),
        ..ShowQuery::default()
    }))
    .unwrap();
    assert_eq!(view.record.harp, "calm-dusty-wren");
}

// session.feature: "Editing a session nobody recorded fails" / "The harp is a
// positional, and an unknown one fails": an unknown harp is an error naming it.
#[test]
fn an_unknown_harp_is_an_error_naming_it() {
    let mut svc = service();
    let error = ready(svc.show(ShowQuery {
        harp: "no-such-harp".to_string(),
        ..ShowQuery::default()
    }))
    .unwrap_err();
    assert_eq!(
        error,
        AppError::NoSession {
            harp: "no-such-harp".to_string()
        }
    );
    assert_eq!(error.args()[0].0, "harp");
}

// ---------------------------------------------------------------- transcripts

#[test]
fn the_transcript_listing_of_one_session_is_that_session() {
    let mut svc = service();
    svc.seed(HERE, 1, "amber-swift-owl", NOW - 2 * DAY, None);
    svc.seed(HERE, 2, "brisk-copper-moth", NOW - DAY, None);
    let rows = ready(svc.transcripts(TranscriptQuery {
        harp: Some("amber-swift-owl".to_string()),
        ..TranscriptQuery::default()
    }))
    .unwrap();
    assert_eq!(rows.len(), 1);
    assert_eq!(rows[0].harp, "amber-swift-owl");
}

// session.feature: "The harp is a positional, and an unknown one fails": an
// empty listing would read as "this session has nothing".
#[test]
fn the_transcript_listing_of_an_unknown_session_fails() {
    let mut svc = service();
    let error = ready(svc.transcripts(TranscriptQuery {
        harp: Some("no-such-harp".to_string()),
        ..TranscriptQuery::default()
    }))
    .unwrap_err();
    assert!(matches!(error, AppError::NoSession { .. }));
}

// Go: errNoCapturedTranscript.
#[test]
fn a_transcript_never_captured_is_an_error_not_an_empty_text() {
    let mut svc = service();
    svc.seed_captured(1, "amber-swift-owl", NOW - DAY, &[]);
    assert_eq!(
        ready(svc.transcript("amber-swift-owl")).unwrap_err(),
        AppError::NoCapturedTranscript {
            harp: "amber-swift-owl".to_string()
        }
    );
    assert!(matches!(
        ready(svc.transcript("no-such-harp")).unwrap_err(),
        AppError::NoSession { .. }
    ));
}

// ----------------------------------------------------------------------- keep

fn keep(harp: &str, keep: bool) -> KeepRequest {
    KeepRequest {
        harp: harp.to_string(),
        keep,
        ..KeepRequest::default()
    }
}

// session.feature: "A kept transcript is refused at any age, and the listing
// says it is kept": keep marks it, the listing shows it, --undo releases it.
#[test]
fn keep_marks_the_session_and_undo_releases_it() {
    let mut svc = service();
    svc.seed(
        HERE,
        1,
        "amber-swift-owl",
        NOW - 40 * DAY,
        Some(LockState::Free),
    );
    let kept = ready(svc.keep(keep("amber-swift-owl", true))).unwrap();
    assert!((kept.kept, kept.changed) == (true, true));
    let views = ready(svc.list(all())).unwrap();
    assert!(views[0].record.kept);
    assert!(
        views[0].summary().contains("kept"),
        "the badge rides the row"
    );
    let undone = ready(svc.keep(keep("amber-swift-owl", false))).unwrap();
    assert!((undone.kept, undone.changed) == (false, true));
    assert!(!ready(svc.list(all())).unwrap()[0].record.kept);
}

// Go: SetKeep is idempotent: an unchanged mark records nothing.
#[test]
fn keeping_a_kept_session_changes_nothing() {
    let mut svc = service();
    svc.seed(HERE, 1, "amber-swift-owl", NOW - 40 * DAY, None);
    ready(svc.keep(keep("amber-swift-owl", true))).unwrap();
    let again = ready(svc.keep(keep("amber-swift-owl", true))).unwrap();
    assert!(!again.changed);
    assert!(again.kept);
}

// Go: SetKeep on a harp with no sidecar is ErrNotFound.
#[test]
fn keeping_an_unknown_session_fails_naming_it() {
    let mut svc = service();
    assert_eq!(
        ready(svc.keep(keep("no-such-harp", true))).unwrap_err(),
        AppError::NoSession {
            harp: "no-such-harp".to_string()
        }
    );
}

// ---------------------------------------------------------------------- remove

fn remove(harp: &str, yes: bool) -> RemoveRequest {
    RemoveRequest {
        harp: harp.to_string(),
        yes,
        ..RemoveRequest::default()
    }
}

/// A finished session whose transcript retention has passed (j001300): its
/// lock free, its end 60 days ago, nothing worked on since.
fn finished(svc: &mut Svc, id: u8, harp: &str) {
    svc.seed(HERE, id, harp, NOW - 70 * DAY, Some(LockState::Free));
    ended(svc.store(HERE), id, NOW - 70 * DAY + 10, NOW - 60 * DAY);
    svc.files.last_active.insert(
        harp.to_string(),
        crate::Timestamp {
            seconds: NOW - 60 * DAY,
            nanos: 0,
        },
    );
}

// session.feature: "Delete reports before it removes": the plan, applied
// false, and everything still there.
#[test]
fn a_removal_reports_before_it_removes() {
    let mut svc = service();
    finished(&mut svc, 1, "amber-swift-owl");
    let removal = ready(svc.remove(remove("amber-swift-owl", false))).unwrap();
    assert!(!removal.applied);
    assert_eq!(removal.refusal, None);
    let destroy: Vec<&str> = removal
        .files
        .destroy
        .iter()
        .map(|i| i.rel.as_str())
        .collect();
    assert_eq!(destroy, ["transcripts/transcript.jsonl"]);
    assert_eq!(removal.files.bytes_freed, 0, "nothing freed on a report");
    assert!(svc.files.destroyed_sessions.is_empty());
    assert!(svc.files.transcripts.contains_key("amber-swift-owl"));
    assert_eq!(svc.harps(all()), ["amber-swift-owl"], "still listed");
}

// Owner ruling 98: `session remove --yes` DESTROYS the session: its
// authored files go with its native history (the whole directory), and it is
// listed no more.
#[test]
fn a_removal_destroys_the_whole_directory_authored_files_too() {
    let mut svc = service();
    finished(&mut svc, 1, "amber-swift-owl");
    svc.files.others.insert(
        "amber-swift-owl".to_string(),
        vec![("design-notes.md".to_string(), 12)],
    );
    let removal = ready(svc.remove(remove("amber-swift-owl", true))).unwrap();
    assert!(removal.applied);
    assert_eq!(removal.refusal, None);
    let destroyed: Vec<&str> = removal
        .files
        .destroy
        .iter()
        .map(|i| i.rel.as_str())
        .collect();
    assert!(destroyed.contains(&"design-notes.md"), "{destroyed:?}");
    assert_eq!(svc.files.destroyed_sessions, ["amber-swift-owl"]);
    let session_root = uuid::Uuid::from_bytes([1; 16]);
    assert_eq!(head(&mut svc, sessions::SESSION_DOMAIN, session_root), 0);
    assert_eq!(svc.harps(all()), Vec::<String>::new(), "no longer listed");
}

// A removal's plan names each file's class: the engine's native history and
// a transcript an older ctxloom left are machine-written, everything else is
// a person's.
#[test]
fn a_removal_plan_names_the_machine_written_and_the_authored_files() {
    let mut svc = service();
    finished(&mut svc, 1, "amber-swift-owl");
    svc.files.others.insert(
        "amber-swift-owl".to_string(),
        vec![
            ("native/history.jsonl".to_string(), 3),
            ("notes.md".to_string(), 4),
        ],
    );
    let removal = ready(svc.remove(remove("amber-swift-owl", false))).unwrap();
    let classes: Vec<(&str, Class)> = removal
        .files
        .destroy
        .iter()
        .map(|i| (i.rel.as_str(), i.class))
        .collect();
    assert_eq!(
        classes,
        [
            ("native/history.jsonl", Class::Machine),
            ("notes.md", Class::Authored),
            ("transcripts/transcript.jsonl", Class::Machine),
        ]
    );
}

// Owner ruling 104: `Removal::authored` is what the warning names: exactly
// the files a person wrote, in the plan's order, never the engine's native
// history or a transcript an older ctxloom left; none when there are none.
#[test]
fn a_removal_names_exactly_its_authored_files() {
    let mut svc = service();
    finished(&mut svc, 1, "amber-swift-owl");
    svc.files.others.insert(
        "amber-swift-owl".to_string(),
        vec![
            ("design-notes.md".to_string(), 4),
            ("native/history.jsonl".to_string(), 3),
            ("plans/next.md".to_string(), 5),
        ],
    );
    let removal = ready(svc.remove(remove("amber-swift-owl", false))).unwrap();
    assert_eq!(removal.authored(), ["design-notes.md", "plans/next.md"]);

    let mut machine_only = service();
    finished(&mut machine_only, 1, "brisk-copper-moth");
    machine_only.files.others.insert(
        "brisk-copper-moth".to_string(),
        vec![("native/history.jsonl".to_string(), 3)],
    );
    let removal = ready(machine_only.remove(remove("brisk-copper-moth", false))).unwrap();
    assert_eq!(
        removal.files.destroy.len(),
        2,
        "native history and transcript"
    );
    assert!(removal.authored().is_empty(), "{:?}", removal.authored());
}

/// The head of the stream `domain`/`root` in `project`'s store.
fn head(svc: &mut Svc, domain: &str, root: uuid::Uuid) -> u64 {
    use angzarr_embedded::EventStore;
    let stream = angzarr_embedded::StreamRef::main(domain, root);
    ready(EventStore::head(svc.store(HERE), stream))
        .unwrap()
        .next_sequence
}

/// A run stream of session `id`'s `incarnation`, with one event, as the run
/// core would leave it.
fn a_run_of(svc: &mut Svc, id: u8, incarnation: u32) -> uuid::Uuid {
    use angzarr_embedded::{EventStore, Write};
    let root = runs::run_root(&id_text(id), incarnation);
    let root = uuid::Uuid::from_slice(&root.value).unwrap();
    let page = angzarr_embedded::v1::EventPage {
        sequence: 0,
        body: Some(angzarr_embedded::v1::event_page::Body::Event(
            angzarr_embedded::Any {
                type_url: "type.googleapis.com/ctxloom.runs.v1.RunExited".to_string(),
                value: Vec::new(),
            },
        )),
        meta: None,
    };
    let stream = angzarr_embedded::StreamRef::main(runs::RUN_DOMAIN, root);
    ready(EventStore::write(
        svc.store(HERE),
        Write::append(stream, 0, std::slice::from_ref(&page)),
    ))
    .unwrap();
    root
}

/// `session remove <harp> --yes`: the destroy (owner ruling 98).
fn destroy(harp: &str) -> RemoveRequest {
    remove(harp, true)
}

// Owner rulings 94, 94a-c, 98: `session remove` DESTROYS the session: every event
// of its stream and of every run of it (UUIDv5(session, 1..n)) is purged from
// the store, and its directory and lock are removed. It is listed no more.
// The name stays claimed, never reused.
#[test]
fn a_destroy_purges_the_session_and_every_run_of_it_and_its_directory() {
    let mut svc = service();
    finished(&mut svc, 1, "amber-swift-owl");
    claim(svc.store(HERE), HERE, "amber-swift-owl", &id_text(1)).unwrap();
    let store = svc.stores.stores.get(HERE).unwrap();
    captured(store, 1, NOW - 70 * DAY + 20, "p1", "a prompt to destroy");
    let run = a_run_of(&mut svc, 1, 1);
    let session_root = uuid::Uuid::from_bytes([1; 16]);
    assert!(head(&mut svc, sessions::SESSION_DOMAIN, session_root) > 0);
    let events = head(&mut svc, sessions::SESSION_DOMAIN, session_root)
        + head(&mut svc, runs::RUN_DOMAIN, run);
    let destroyed = ready(svc.remove(destroy("amber-swift-owl"))).unwrap();
    assert!(destroyed.applied);
    assert_eq!(
        destroyed.events_destroyed, events,
        "every event of the session and its run, counted"
    );
    assert_eq!(head(&mut svc, sessions::SESSION_DOMAIN, session_root), 0);
    assert_eq!(head(&mut svc, runs::RUN_DOMAIN, run), 0, "its run is gone");
    assert_eq!(svc.files.destroyed_sessions, ["amber-swift-owl"]);
    assert_eq!(svc.harps(all()), Vec::<String>::new(), "listed no more");
    assert_eq!(
        holder(svc.store(HERE), HERE, "amber-swift-owl"),
        id_text(1),
        "the name stays claimed (94c)"
    );
}

// Ruling 94d: a hook fact delivered again after its session was destroyed
// (its command id forgotten with the stream) finds no session, and records
// nothing.
#[test]
fn a_hook_fact_redelivered_after_a_destroy_records_nothing() {
    let mut svc = service();
    finished(&mut svc, 1, "amber-swift-owl");
    let store = svc.stores.stores.get(HERE).unwrap();
    captured(store, 1, NOW - 70 * DAY + 20, "p1", "a prompt to destroy");
    ready(svc.remove(destroy("amber-swift-owl"))).unwrap();
    let store = svc.stores.stores.get(HERE).unwrap();
    captured(store, 1, NOW, "p1", "a prompt to destroy");
    let session_root = uuid::Uuid::from_bytes([1; 16]);
    assert_eq!(head(&mut svc, sessions::SESSION_DOMAIN, session_root), 0);
    assert_eq!(svc.harps(all()), Vec::<String>::new());
}

// Ruling 94b: a destroy reported (no --yes) changes nothing: the streams and
// the directory stay.
#[test]
fn a_destroy_reported_and_not_applied_changes_nothing() {
    let mut svc = service();
    finished(&mut svc, 1, "amber-swift-owl");
    let report = ready(svc.remove(RemoveRequest {
        yes: false,
        ..destroy("amber-swift-owl")
    }))
    .unwrap();
    assert!(!report.applied);
    let session_root = uuid::Uuid::from_bytes([1; 16]);
    assert!(head(&mut svc, sessions::SESSION_DOMAIN, session_root) > 0);
    assert!(svc.files.destroyed_sessions.is_empty());
}

// session.feature: "A session running again is refused by the transcript's own
// destroyer": the refusal names the command that was run, `session remove`.
#[test]
fn the_refusal_names_the_destroyer_that_was_run() {
    let mut svc = service();
    svc.seed(
        HERE,
        1,
        "amber-swift-owl",
        NOW - 70 * DAY,
        Some(LockState::Held),
    );
    match ready(svc.remove(remove("amber-swift-owl", true)))
        .unwrap()
        .refusal
    {
        Some(Refusal::NotProvenDead { verb, reason, .. }) => {
            assert_eq!(verb, "session remove");
            assert!(reason.contains("owner is alive"), "{reason}");
        }
        other => panic!("{other:?}"),
    }
}

// session.feature: "A transcript within its retention is refused by every
// destroyer" (session remove): refused, nothing destroyed, the plan shown.
#[test]
fn a_removal_within_the_retention_is_refused() {
    let mut svc = service();
    svc.seed(
        HERE,
        1,
        "brisk-copper-moth",
        NOW - 2 * HOUR,
        Some(LockState::Free),
    );
    ended(svc.store(HERE), 1, NOW - HOUR, NOW - HOUR);
    svc.files.last_active.insert(
        "brisk-copper-moth".to_string(),
        crate::Timestamp {
            seconds: NOW - HOUR,
            nanos: 0,
        },
    );
    let removal = ready(svc.remove(remove("brisk-copper-moth", true))).unwrap();
    assert!(matches!(removal.refusal, Some(Refusal::Retained { .. })));
    assert!(!removal.applied);
    assert!(svc.files.destroyed_sessions.is_empty());
    assert_eq!(svc.harps(all()), ["brisk-copper-moth"]);
    assert_eq!(removal.files.destroy.len(), 1, "the plan is still shown");
}

// session.feature: "A kept transcript is refused at any age".
#[test]
fn a_removal_of_a_kept_session_is_refused_at_any_age() {
    let mut svc = service();
    finished(&mut svc, 1, "amber-swift-owl");
    ready(svc.keep(keep("amber-swift-owl", true))).unwrap();
    let removal = ready(svc.remove(remove("amber-swift-owl", true))).unwrap();
    assert_eq!(
        removal.refusal,
        Some(Refusal::Kept {
            harp: "amber-swift-owl".to_string()
        })
    );
    assert!(svc.files.destroyed_sessions.is_empty());
    ready(svc.keep(keep("amber-swift-owl", false))).unwrap();
    let removal = ready(svc.remove(remove("amber-swift-owl", true))).unwrap();
    assert!(removal.applied, "released, it goes");
}

// session.feature: "A session the lock cannot prove finished is refused": a
// free lock proves the owner dead; a held lock, or no lock at all, refuses.
#[test]
fn a_removal_of_a_session_the_lock_cannot_prove_dead_is_refused() {
    for (lock, name) in [(Some(LockState::Held), "held"), (None, "no lock")] {
        let mut svc = service();
        svc.seed(HERE, 1, "amber-swift-owl", NOW - 70 * DAY, lock);
        ended(svc.store(HERE), 1, NOW - 70 * DAY + 10, NOW - 60 * DAY);
        let removal = ready(svc.remove(remove("amber-swift-owl", true))).unwrap();
        assert!(
            matches!(removal.refusal, Some(Refusal::NotProvenDead { .. })),
            "{name}: {:?}",
            removal.refusal
        );
        assert!(svc.files.destroyed_sessions.is_empty(), "{name}");
    }
}

// session.feature: "--even-if-live sweeps it anyway".
#[test]
fn even_if_live_permits_a_session_whose_lock_proves_nothing() {
    let mut svc = service();
    svc.seed(HERE, 1, "amber-swift-owl", NOW - 70 * DAY, None);
    ended(svc.store(HERE), 1, NOW - 70 * DAY + 10, NOW - 60 * DAY);
    let removal = ready(svc.remove(RemoveRequest {
        yes: true,
        even_if_live: true,
        ..remove("amber-swift-owl", true)
    }))
    .unwrap();
    assert!(removal.applied);
    assert_eq!(svc.harps(all()), Vec::<String>::new());
}

// Go: PurgeSession checks the lock first, then the retention.
#[test]
fn the_lock_is_asked_before_the_retention() {
    let mut svc = service();
    svc.seed(HERE, 1, "amber-swift-owl", NOW - 2 * HOUR, None);
    ended(svc.store(HERE), 1, NOW - HOUR, NOW - HOUR);
    let removal = ready(svc.remove(remove("amber-swift-owl", true))).unwrap();
    assert!(matches!(
        removal.refusal,
        Some(Refusal::NotProvenDead { .. })
    ));
}

// Go: TestRetention_OneShotIsReclaimableAtAnyAgeUnlessKept.
#[test]
fn an_internal_one_shot_is_reclaimable_at_any_age() {
    let mut svc = service();
    let store = svc.stores.stores.entry(HERE.to_string()).or_default();
    crate::fakes::run(
        store,
        1,
        NOW - 2 * HOUR,
        sessions::MintSession {
            harp: "gray-oneshot-wisp".to_string(),
            project_id: HERE.to_string(),
            project_dir: "/work/proj".to_string(),
            output_dir: String::new(),
            engine: "claude-code".to_string(),
            origin: sessions::Origin::Oneshot,
            started_at: None,
            depth: 0,
            workspace: None,
        },
    );
    ended(store, 1, NOW - HOUR, NOW - HOUR);
    svc.files.transcripts.insert(
        "gray-oneshot-wisp".to_string(),
        transcript_of("gray-oneshot-wisp"),
    );
    svc.liveness
        .0
        .insert("gray-oneshot-wisp".to_string(), LockState::Free);
    let removal = ready(svc.remove(remove("gray-oneshot-wisp", true))).unwrap();
    assert!(removal.applied, "{:?}", removal.refusal);
}

// Ruling 94b: a destroy purges the streams BEFORE it removes the directory,
// so a failure removing the directory leaves a session nothing lists (its
// harp never reused), not a listed session that lost its files.
#[test]
fn the_streams_go_before_the_directory() {
    let mut svc = service();
    finished(&mut svc, 1, "amber-swift-owl");
    svc.files.fail_destroy = true;
    let error = ready(svc.remove(remove("amber-swift-owl", true))).unwrap_err();
    assert!(matches!(error, AppError::Files(_)), "{error:?}");
    let session_root = uuid::Uuid::from_bytes([1; 16]);
    assert_eq!(head(&mut svc, sessions::SESSION_DOMAIN, session_root), 0);
    assert_eq!(svc.harps(all()), Vec::<String>::new(), "listed no more");
}

#[test]
fn a_removal_of_an_unknown_session_fails_naming_it() {
    let mut svc = service();
    assert_eq!(
        ready(svc.remove(remove("no-such-harp", true))).unwrap_err(),
        AppError::NoSession {
            harp: "no-such-harp".to_string()
        }
    );
}

// ------------------------------------------------------------------------ bind

fn bind(harp: &str, native: &str, transcript: &str) -> BindRequest {
    BindRequest {
        harp: harp.to_string(),
        native_id: native.to_string(),
        transcript_path: transcript.to_string(),
        ..BindRequest::default()
    }
}

// session_hooks.feature: "Binding a session records the harp both in the
// transcript and in the index": the index binds the harp to the session.
#[test]
fn a_bind_records_the_engines_session_on_the_harp() {
    let mut svc = service();
    svc.seed(HERE, 1, "brisk-copper-moth", NOW - DAY, None);
    let bound = ready(svc.bind(bind(
        "brisk-copper-moth",
        "vendor-session-42",
        "/tmp/vendor/transcript.jsonl",
    )))
    .unwrap();
    assert!(matches!(bound, Bound::Recorded { .. }));
    let view = ready(svc.show(ShowQuery {
        harp: "brisk-copper-moth".to_string(),
        ..ShowQuery::default()
    }))
    .unwrap();
    assert_eq!(view.record.native_id, "vendor-session-42");
    assert_eq!(view.record.transcript_path, "/tmp/vendor/transcript.jsonl");
}

// Go: TestBindSession_Idempotent, _RotationRepointsTheBinding.
#[test]
fn a_repeat_bind_changes_nothing_and_a_new_session_rotates() {
    let mut svc = service();
    svc.seed(HERE, 1, "brisk-copper-moth", NOW - DAY, None);
    ready(svc.bind(bind("brisk-copper-moth", "first", "/t1"))).unwrap();
    assert_eq!(
        ready(svc.bind(bind("brisk-copper-moth", "first", "/t1"))).unwrap(),
        Bound::Unchanged
    );
    assert!(matches!(
        ready(svc.bind(bind("brisk-copper-moth", "second", "/t2"))).unwrap(),
        Bound::Recorded { .. }
    ));
    let view = ready(svc.show(ShowQuery {
        harp: "brisk-copper-moth".to_string(),
        ..ShowQuery::default()
    }))
    .unwrap();
    assert_eq!(view.record.native_id, "second");
    assert_eq!(view.record.rotations.len(), 1);
    assert_eq!(view.record.rotations[0].native_id, "first");
}

// Go: operations.BindSession "no-ops a harp that is absent from the index": a
// hook never fails an engine's startup over it.
#[test]
fn a_bind_of_a_harp_nobody_recorded_is_not_a_failure() {
    let mut svc = service();
    assert_eq!(
        ready(svc.bind(bind("no-such-harp", "id", "/t"))).unwrap(),
        Bound::NoSuchSession
    );
    assert!(svc.stores.opened.is_empty(), "and creates nothing");
}

// ----------------------------------------------------------------- title (S55)

fn title(harp: &str, text: &str) -> crate::TitleRequest {
    crate::TitleRequest {
        harp: harp.to_string(),
        title: text.to_string(),
        ..crate::TitleRequest::default()
    }
}

// S55 (owner, 2026-11-12, slice 2): a rename sets the title. The harp, the
// stable id, the output folder, the directory and the lock stay as they were,
// and no name is claimed or freed.
#[test]
fn a_title_is_set_and_shown_and_nothing_moves_or_is_claimed() {
    let mut svc = service();
    svc.seed(HERE, 1, "amber-swift-owl", NOW - DAY, Some(LockState::Free));
    svc.with_directory(1, "amber-swift-owl");
    let titled = ready(svc.set_title(title("amber-swift-owl", "the auth refactor"))).unwrap();
    assert_eq!(
        (titled.harp.as_str(), titled.title.as_str(), titled.changed),
        ("amber-swift-owl", "the auth refactor", true)
    );
    let views = ready(svc.list(all())).unwrap();
    assert_eq!(views[0].record.harp, "amber-swift-owl");
    assert_eq!(views[0].record.id, id_text(1));
    assert_eq!(views[0].record.title, "the auth refactor");
    assert_eq!(views[0].title.as_deref(), Some("the auth refactor"));
    assert!(
        svc.files.sessions.contains("amber-swift-owl"),
        "its directory is where it was"
    );
    assert_eq!(holder(svc.store(HERE), HERE, "amber-swift-owl"), id_text(1));
    let again = ready(svc.set_title(title("amber-swift-owl", "the auth refactor"))).unwrap();
    assert!(!again.changed, "the title held records nothing");
}

// A live session is titled too: nothing it works in moves.
#[test]
fn a_live_session_is_titled() {
    let mut svc = service();
    svc.seed(HERE, 1, "amber-swift-owl", NOW - DAY, Some(LockState::Held));
    let titled = ready(svc.set_title(title("amber-swift-owl", "still going"))).unwrap();
    assert!(titled.changed);
}

#[test]
fn a_title_for_a_session_nobody_recorded_fails_naming_it() {
    let mut svc = service();
    assert_eq!(
        ready(svc.set_title(title("no-such-harp", "x"))).unwrap_err(),
        AppError::NoSession {
            harp: "no-such-harp".to_string()
        }
    );
}

// ---------------------------------------------------------------------- errors

#[test]
fn a_project_that_cannot_be_resolved_is_an_identity_failure() {
    let mut svc = service();
    svc.identity.fails = Some(angzarr_diagnostic::Report::new("x-no-home"));
    let error = ready(svc.list(ListQuery::default())).unwrap_err();
    assert!(matches!(error, AppError::Identity(_)), "{error:?}");
    assert_eq!(error.code(), "x-no-home");
}

// The refusal for a lock says which: held means the owner is running.
#[test]
fn a_held_lock_and_a_missing_one_are_refused_for_different_reasons() {
    let reason = |lock: Option<LockState>| {
        let mut svc = service();
        svc.seed(HERE, 1, "amber-swift-owl", NOW - 70 * DAY, lock);
        match ready(svc.remove(remove("amber-swift-owl", true)))
            .unwrap()
            .refusal
        {
            Some(Refusal::NotProvenDead { reason, .. }) => reason,
            other => panic!("{other:?}"),
        }
    };
    let held = reason(Some(LockState::Held));
    let none = reason(None);
    assert!(held.contains("running"), "{held}");
    assert!(none.contains("no lock"), "{none}");
}

// The sample dead letter names its component and reason (a driving adapter's
// tests say it).
#[test]
fn the_sample_dead_letter_names_its_component_and_reason() {
    let line = crate::dead_letter_line(&crate::dead_letter_sample("sessions").letter);
    assert!(
        line.contains("sessions") && line.contains("ROUTER_REASON_UNHANDLED_HANDLER_ERROR"),
        "{line}"
    );
}

// ------------------------------------- the transcript from the store (87a)

impl Svc {
    /// A session minted at `started` whose hooks delivered `prompts`, one a
    /// minute after it, and NO transcript file (owner ruling 87a).
    fn seed_captured(&mut self, id: u8, harp: &str, started: i64, prompts: &[&str]) {
        let store = self.stores.stores.entry(HERE.to_string()).or_default();
        minted(store, id, harp, "/work/proj", started);
        for (n, prompt) in prompts.iter().enumerate() {
            captured(
                store,
                id,
                started + 60 * (n as i64 + 1),
                &format!("p{n}"),
                prompt,
            );
        }
        self.files.transcripts.remove(harp);
    }
}

fn kinds_and_contents(text: &str) -> Vec<(String, String)> {
    text.lines()
        .map(|line| {
            let line: serde_json::Value = serde_json::from_str(line).unwrap();
            (
                line["kind"].as_str().unwrap_or_default().to_string(),
                line["entry"]["content"]
                    .as_str()
                    .unwrap_or_default()
                    .to_string(),
            )
        })
        .collect()
}

// Ruling 87a: `session transcript show` answers from the session's events in
// the store; there is no transcript file to read.
#[test]
fn a_transcript_is_shown_from_the_sessions_events_with_no_file() {
    let mut svc = service();
    svc.seed_captured(1, "amber-swift-owl", NOW - DAY, &["Fix the build"]);
    let text = ready(svc.transcript("amber-swift-owl")).unwrap();
    assert_eq!(
        kinds_and_contents(&text),
        [("entry".to_string(), "Fix the build".to_string())]
    );
}

// Ruling 87a: a file left in a session's directory (a transcript an older
// ctxloom wrote) is not what is shown: the events are.
#[test]
fn a_transcript_file_left_on_disk_is_not_what_is_shown() {
    let mut svc = service();
    svc.seed_captured(1, "amber-swift-owl", NOW - DAY, &["from the events"]);
    svc.files.transcripts.insert(
        "amber-swift-owl".to_string(),
        transcript_of("amber-swift-owl"),
    );
    let text = ready(svc.transcript("amber-swift-owl")).unwrap();
    assert!(text.contains("from the events"), "{text}");
    assert!(!text.contains("Seeded prompt"), "{text}");
}

// Ruling 87a: a live session (its lock held) shows what its hooks have
// delivered so far, and the next read sees what was delivered since.
#[test]
fn a_running_session_shows_what_its_hooks_delivered_so_far() {
    let mut svc = service();
    svc.seed_captured(1, "amber-swift-owl", NOW - DAY, &["first"]);
    svc.liveness
        .0
        .insert("amber-swift-owl".to_string(), LockState::Held);
    let so_far = ready(svc.transcript("amber-swift-owl")).unwrap();
    assert_eq!(kinds_and_contents(&so_far).len(), 1);
    let store = svc.stores.stores.get(HERE).unwrap();
    captured(store, 1, NOW - DAY + 3600, "p9", "second");
    let since = ready(svc.transcript("amber-swift-owl")).unwrap();
    let got = kinds_and_contents(&since);
    assert_eq!(got.len(), 2, "{since}");
    assert_eq!(got[1].1, "second");
}

// Ruling 87a: the listing's title is the first prompt the session's events
// hold, and its size is the transcript's as read from them; no file is named.
#[test]
fn a_listing_is_titled_and_sized_from_the_sessions_events() {
    let mut svc = service();
    svc.seed_captured(
        1,
        "amber-swift-owl",
        NOW - DAY,
        &["Fix the build", "and then"],
    );
    let views = ready(svc.list(all())).unwrap();
    assert_eq!(views[0].title.as_deref(), Some("Fix the build"));
    let text = ready(svc.transcript("amber-swift-owl")).unwrap();
    let captured = views[0].captured.as_ref().expect("captured");
    assert_eq!(captured.bytes, text.len() as u64);
    let found = ready(svc.search(SearchQuery {
        list: all(),
        words: vec!["build".to_string()],
    }))
    .unwrap();
    assert_eq!(found.len(), 1, "a search matches the title from the events");
}

// Ruling 87a: `session transcript list` says a session's transcript is
// captured, and its size, from the events, and names no path.
#[test]
fn the_transcript_listing_reads_the_events_and_names_no_file() {
    let mut svc = service();
    svc.seed_captured(1, "amber-swift-owl", NOW - 2 * DAY, &["asked"]);
    svc.seed_captured(2, "brisk-copper-moth", NOW - DAY, &[]);
    let text = ready(svc.transcript("amber-swift-owl")).unwrap();
    let rows = ready(svc.transcripts(TranscriptQuery {
        all: true,
        ..TranscriptQuery::default()
    }))
    .unwrap();
    let got: Vec<(&str, bool, u64)> = rows
        .iter()
        .map(|r| (r.harp.as_str(), r.captured, r.bytes))
        .collect();
    assert_eq!(
        got,
        [
            ("brisk-copper-moth", false, 0),
            ("amber-swift-owl", true, text.len() as u64),
        ]
    );
}

// ------------------------------------------------------- session directories

impl Svc {
    /// `harp`'s session directory exists, and its name is claimed by it.
    fn with_directory(&mut self, id: u8, harp: &str) {
        self.files.sessions.insert(harp.to_string());
        claim(self.store(HERE), HERE, harp, &id_text(id)).unwrap();
    }
}

/// A stream of `n` events in a store that answers its next `conflicts`
/// purges with a conflict (another writer moved it in between), and the
/// stream.
fn conflicting(
    n: u64,
    conflicts: usize,
) -> (
    angzarr_embedded::testing::FaultyStore<angzarr_embedded::angzarr_ports::memory::MemoryStore>,
    uuid::Uuid,
) {
    use angzarr_embedded::{EventStore, Write};
    let store = angzarr_embedded::testing::FaultyStore::new(
        angzarr_embedded::angzarr_ports::memory::MemoryStore::new(),
    );
    let root = uuid::Uuid::from_bytes([9; 16]);
    let pages: Vec<_> = (0..n)
        .map(|sequence| angzarr_embedded::v1::EventPage {
            sequence,
            body: Some(angzarr_embedded::v1::event_page::Body::Event(
                angzarr_embedded::Any {
                    type_url: "type.googleapis.com/ctxloom.runs.v1.RunExited".to_string(),
                    value: Vec::new(),
                },
            )),
            meta: None,
        })
        .collect();
    let stream = angzarr_embedded::StreamRef::main(runs::RUN_DOMAIN, root);
    ready(EventStore::write(
        store.inner(),
        Write::append(stream, 0, &pages),
    ))
    .unwrap();
    store.fail(std::iter::repeat_n(
        angzarr_embedded::testing::Fault::Conflict,
        conflicts,
    ));
    (store, root)
}

// Ruling 94b: a destroy's purge is guarded by the head just read; a conflict
// (another writer moved the stream) is read again and tried again, three
// times, and then is the destroy's failure, never a purge of a stream it has
// not read.
#[test]
fn a_purge_that_conflicts_is_tried_again_three_times_then_fails() {
    let (store, root) = conflicting(3, 3);
    let stream = angzarr_embedded::StreamRef::main(runs::RUN_DOMAIN, root);
    assert_eq!(
        ready(crate::write::purge(&store, stream)).unwrap(),
        3,
        "three conflicts, then purged"
    );
    let (store, root) = conflicting(2, 4);
    let stream = angzarr_embedded::StreamRef::main(runs::RUN_DOMAIN, root);
    assert!(
        ready(crate::write::purge(&store, stream)).is_err(),
        "a fourth conflict is the failure"
    );
}
