//! The sessions use cases, over fakes of the ports and angzarr-embedded's
//! kernel on an in-memory store. Each test names the Go scenario or test it
//! ports (session.feature, j001200, j001300, `operations`/`cli` tests,
//! release/0.7), or says it is design.

use angzarr_diagnostic::Diagnostic;
use exit_code::{Exit, ExitClass};
use session_ports::LockState;

use crate::fakes::{
    bound, captured, claim, ended, holder, id_text, minted, minted_in, project, ready, FakeClock,
    FakeDelay, FakeFiles, FakeIdentity, FakeLiveness, FakeStores,
};
use crate::{
    AppError, BindRequest, Bound, Class, KeepRequest, ListQuery, Refusal, RemoveRequest,
    RenameRequest, SearchQuery, Service, SessionService, ShowQuery, State, TranscriptQuery,
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
        let key = self.stores.key(project);
        let store = self.stores.stores.entry(key).or_default();
        minted_in(store, project, id, harp, "/work/proj", started);
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

// T14: every project's sessions are in the one store, and a listing holds the
// current project's alone; --all reaches the others, each found in its own.
#[test]
fn the_one_store_lists_each_projects_sessions_under_its_own_project() {
    let mut svc = service();
    svc.stores.one = true;
    svc.seed(HERE, 1, "amber-swift-owl", NOW - 2 * DAY, None);
    svc.seed(ELSEWHERE, 3, "calm-dusty-wren", NOW - DAY, None);
    assert_eq!(svc.stores.stores.len(), 1, "one store");
    assert_eq!(svc.harps(ListQuery::default()), ["amber-swift-owl"]);
    assert_eq!(svc.harps(all()), ["calm-dusty-wren", "amber-swift-owl"]);
    let shown = ready(svc.show(ShowQuery {
        project: Some(ELSEWHERE.to_string()),
        harp: "calm-dusty-wren".to_string(),
    }))
    .unwrap();
    assert_eq!(shown.record.project_id, ELSEWHERE);
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
            owner: None,
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

// ---------------------------------------------------------------------- rename

// plan 3.1 / D5 Q1: a rename claims the new name, records it, and frees the
// old one; the output folder moves beside itself, to the new name; the stable
// id never changes.
#[test]
fn a_rename_moves_the_claim_frees_the_old_name_and_moves_the_output_folder() {
    let mut svc = service();
    svc.seed(HERE, 1, "amber-swift-owl", NOW - DAY, None);
    let old_out = "/home/Documents/ctxloom/proj/amber-swift-owl";
    let new_out = "/home/Documents/ctxloom/proj/bright-keen-hawk";
    svc.files.outputs.insert(old_out.to_string());
    claim(svc.store(HERE), HERE, "amber-swift-owl", &id_text(1)).unwrap();
    let renamed = ready(svc.rename(rename("amber-swift-owl", "bright-keen-hawk"))).unwrap();
    assert_eq!(
        (
            renamed.from.as_str(),
            renamed.to.as_str(),
            renamed.output_dir.as_str()
        ),
        ("amber-swift-owl", "bright-keen-hawk", new_out)
    );
    assert_eq!(
        svc.files.moves,
        [(old_out.to_string(), new_out.to_string())]
    );
    let views = ready(svc.list(all())).unwrap();
    assert_eq!(views.len(), 1);
    assert_eq!(views[0].record.harp, "bright-keen-hawk");
    assert_eq!(views[0].record.output_dir, new_out);
    assert_eq!(
        views[0].record.id,
        id_text(1),
        "the stable id never changes"
    );
    let store = svc.store(HERE);
    assert_eq!(holder(store, HERE, "bright-keen-hawk"), id_text(1));
    assert_eq!(
        holder(store, HERE, "amber-swift-owl"),
        "",
        "the old name is free"
    );
    assert!(
        claim(store, HERE, "amber-swift-owl", &id_text(2)).is_ok(),
        "and another may take it"
    );
}

fn rename(from: &str, to: &str) -> RenameRequest {
    RenameRequest {
        harp: from.to_string(),
        to: to.to_string(),
        ..RenameRequest::default()
    }
}

// D5 Q1: a name another session holds is refused, and nothing moves.
#[test]
fn a_rename_onto_a_name_another_session_holds_is_refused_and_changes_nothing() {
    let mut svc = service();
    svc.seed(HERE, 1, "amber-swift-owl", NOW - DAY, None);
    svc.seed(HERE, 2, "brisk-copper-moth", NOW - DAY, None);
    claim(svc.store(HERE), HERE, "amber-swift-owl", &id_text(1)).unwrap();
    claim(svc.store(HERE), HERE, "brisk-copper-moth", &id_text(2)).unwrap();
    let error = ready(svc.rename(rename("amber-swift-owl", "brisk-copper-moth"))).unwrap_err();
    assert_eq!(error.code(), "sessions-names-taken");
    assert_eq!(error.exit(), Exit::REFUSED);
    assert!(svc.files.moves.is_empty());
    assert_eq!(holder(svc.store(HERE), HERE, "amber-swift-owl"), id_text(1));
    assert_eq!(
        holder(svc.store(HERE), HERE, "brisk-copper-moth"),
        id_text(2)
    );
    let mut harps = svc.harps(all());
    harps.sort();
    assert_eq!(harps, ["amber-swift-owl", "brisk-copper-moth"]);
}

// Go: Rename onto its own name collides with itself ("name already in use").
#[test]
fn a_rename_to_the_name_it_has_is_refused() {
    let mut svc = service();
    svc.seed(HERE, 1, "amber-swift-owl", NOW - DAY, None);
    claim(svc.store(HERE), HERE, "amber-swift-owl", &id_text(1)).unwrap();
    let error = ready(svc.rename(rename("amber-swift-owl", "amber-swift-owl"))).unwrap_err();
    assert_eq!(error.code(), "sessions-names-taken");
    assert_eq!(
        holder(svc.store(HERE), HERE, "amber-swift-owl"),
        id_text(1),
        "still held"
    );
}

// Go: TestRename_RefusesUnsafeNewName / ErrNameTooLong, before anything is
// claimed: a refused rename leaves no claim behind.
#[test]
fn a_rename_to_a_name_no_session_may_have_claims_nothing() {
    let mut svc = service();
    svc.seed(HERE, 1, "amber-swift-owl", NOW - DAY, None);
    for bad in ["../../etc", "a/b", " lead", &"x".repeat(25)] {
        let error = ready(svc.rename(rename("amber-swift-owl", bad))).unwrap_err();
        assert_eq!(error.exit(), Exit::REFUSED, "{bad:?}");
        assert_eq!(holder(svc.store(HERE), HERE, bad), "", "{bad:?}");
    }
    assert!(svc.files.moves.is_empty());
    assert_eq!(svc.harps(all()), ["amber-swift-owl"]);
}

// Go: "A refusal leaves everything where it was: each step is undone if a
// later one fails": a folder that cannot move gives the new name back.
#[test]
fn a_folder_that_cannot_move_refuses_the_rename_whole() {
    let mut svc = service();
    svc.seed(HERE, 1, "amber-swift-owl", NOW - DAY, None);
    claim(svc.store(HERE), HERE, "amber-swift-owl", &id_text(1)).unwrap();
    svc.files.fail_move = true;
    let error = ready(svc.rename(rename("amber-swift-owl", "bright-keen-hawk"))).unwrap_err();
    assert!(matches!(error, AppError::Files(_)), "{error:?}");
    assert_eq!(
        holder(svc.store(HERE), HERE, "bright-keen-hawk"),
        "",
        "the new name is given back"
    );
    assert_eq!(holder(svc.store(HERE), HERE, "amber-swift-owl"), id_text(1));
    assert_eq!(svc.harps(all()), ["amber-swift-owl"]);
}

// A session minted before claims existed holds no claim on its name: a rename
// still works, and frees nothing it never held.
#[test]
fn a_session_with_no_claim_on_its_name_can_still_be_renamed() {
    let mut svc = service();
    svc.seed(HERE, 1, "amber-swift-owl", NOW - DAY, None);
    ready(svc.rename(rename("amber-swift-owl", "bright-keen-hawk"))).unwrap();
    assert_eq!(svc.harps(all()), ["bright-keen-hawk"]);
    assert_eq!(
        holder(svc.store(HERE), HERE, "bright-keen-hawk"),
        id_text(1)
    );
}

// plan 3.1: "a forgotten session's name stays claimed", so the harp is never
// reused.
#[test]
fn a_removed_sessions_name_stays_claimed() {
    let mut svc = service();
    finished(&mut svc, 1, "amber-swift-owl");
    claim(svc.store(HERE), HERE, "amber-swift-owl", &id_text(1)).unwrap();
    assert!(
        ready(svc.remove(remove("amber-swift-owl", true)))
            .unwrap()
            .applied
    );
    assert_eq!(svc.harps(all()), Vec::<String>::new());
    assert_eq!(holder(svc.store(HERE), HERE, "amber-swift-owl"), id_text(1));
    assert!(claim(svc.store(HERE), HERE, "amber-swift-owl", &id_text(2)).is_err());
}

// session.feature: "Editing a session nobody recorded fails": the harp is named,
// whatever else is unavailable.
#[test]
fn a_rename_of_a_session_nobody_recorded_fails_naming_it() {
    let mut svc = service();
    assert_eq!(
        ready(svc.rename(RenameRequest {
            harp: "no-such-harp".to_string(),
            to: "bright-keen-hawk".to_string(),
            ..RenameRequest::default()
        }))
        .unwrap_err(),
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

// A session that records no output folder has none to move.
#[test]
fn a_session_with_no_output_folder_renames_without_moving_one() {
    let mut svc = service();
    let store = svc.stores.stores.entry(HERE.to_string()).or_default();
    crate::fakes::run(
        store,
        1,
        NOW - DAY,
        sessions::MintSession {
            harp: "amber-swift-owl".to_string(),
            project_id: HERE.to_string(),
            project_dir: "/work/proj".to_string(),
            output_dir: String::new(),
            engine: "claude-code".to_string(),
            origin: sessions::Origin::Session,
            started_at: None,
            depth: 0,
            owner: None,
        },
    );
    let renamed = ready(svc.rename(rename("amber-swift-owl", "bright-keen-hawk"))).unwrap();
    assert_eq!(renamed.output_dir, "");
    assert!(svc.files.moves.is_empty());
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

// ------------------------------------------ rename and liveness (defect 2)

impl Svc {
    /// `harp`'s session directory exists, and its name is claimed by it.
    fn with_directory(&mut self, id: u8, harp: &str) {
        self.files.sessions.insert(harp.to_string());
        claim(self.store(HERE), HERE, harp, &id_text(id)).unwrap();
    }
}

// Defect 2: a LIVE session (its lock held) is not renamed: its engine works in
// its directory. Refused with a typed refusal whose fix says to wait for the
// session's end; no name is claimed, nothing is moved, nothing is recorded.
#[test]
fn a_live_session_is_not_renamed_and_nothing_is_claimed_or_moved() {
    let mut svc = service();
    svc.seed(HERE, 1, "amber-swift-owl", NOW - DAY, Some(LockState::Held));
    svc.with_directory(1, "amber-swift-owl");
    let old_out = "/home/Documents/ctxloom/proj/amber-swift-owl";
    svc.files.outputs.insert(old_out.to_string());
    let error = ready(svc.rename(rename("amber-swift-owl", "bright-keen-hawk"))).unwrap_err();
    // PROPOSAL: the refusal `SessionLive`, code `sessions-session-live`.
    assert_eq!(error.code(), "sessions-session-live");
    assert!(svc.files.moves.is_empty(), "no output folder moved");
    assert!(svc.files.session_moves.is_empty(), "no directory moved");
    assert_eq!(holder(svc.store(HERE), HERE, "bright-keen-hawk"), "");
    assert_eq!(holder(svc.store(HERE), HERE, "amber-swift-owl"), id_text(1));
    assert_eq!(svc.harps(all()), ["amber-swift-owl"]);
}

// Defect 2: a rename of an ENDED session moves its session directory (and its
// lock, beside it) to the new name, as well as its output folder: no
// directory is left under the old name for a later session to claim.
#[test]
fn an_ended_sessions_rename_moves_its_session_directory_too() {
    let mut svc = service();
    svc.seed(HERE, 1, "amber-swift-owl", NOW - DAY, Some(LockState::Free));
    svc.with_directory(1, "amber-swift-owl");
    ready(svc.rename(rename("amber-swift-owl", "bright-keen-hawk"))).unwrap();
    assert_eq!(
        svc.files.session_moves,
        [(
            "amber-swift-owl".to_string(),
            "bright-keen-hawk".to_string()
        )]
    );
    assert!(svc.files.sessions.contains("bright-keen-hawk"));
    assert!(!svc.files.sessions.contains("amber-swift-owl"));
}

// Defect 2: a session directory that cannot move refuses the rename whole:
// the new name is released, the output folder moved back, the old name kept.
#[test]
fn a_session_directory_that_cannot_move_refuses_the_rename_whole() {
    let mut svc = service();
    svc.seed(HERE, 1, "amber-swift-owl", NOW - DAY, Some(LockState::Free));
    svc.with_directory(1, "amber-swift-owl");
    let old_out = "/home/Documents/ctxloom/proj/amber-swift-owl";
    svc.files.outputs.insert(old_out.to_string());
    svc.files.fail_session_move = true;
    let error = ready(svc.rename(rename("amber-swift-owl", "bright-keen-hawk")));
    assert!(error.is_err(), "the rename is refused");
    assert_eq!(
        svc.files.session_moves.len(),
        1,
        "the directory's move was asked for"
    );
    assert!(svc.files.outputs.contains(old_out), "the folder moved back");
    assert_eq!(holder(svc.store(HERE), HERE, "bright-keen-hawk"), "");
    assert_eq!(holder(svc.store(HERE), HERE, "amber-swift-owl"), id_text(1));
    assert_eq!(svc.harps(all()), ["amber-swift-owl"]);
}

// PROPOSAL (defect 2): a lock that proves nothing (a session from before runs
// held their lock) does not block a rename; only a held one does.
#[test]
fn a_session_whose_lock_proves_nothing_is_renamed() {
    let mut svc = service();
    svc.seed(
        HERE,
        1,
        "amber-swift-owl",
        NOW - DAY,
        Some(LockState::Unproven),
    );
    svc.with_directory(1, "amber-swift-owl");
    ready(svc.rename(rename("amber-swift-owl", "bright-keen-hawk"))).unwrap();
    assert_eq!(svc.harps(all()), ["bright-keen-hawk"]);
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

// ------------------------------------------------- the daemon's model (D28)

fn at_session(project: &str, id: u8) -> sessions::SessionRef {
    sessions::SessionRef {
        project_id: project.to_string(),
        session: id_text(id),
    }
}

/// Session `owner` (of HERE) launches session `child` of `child_project`,
/// which is minted for it, finished as `finished` leaves one (each command at
/// its own second: the fake keys a command by its second).
fn launched_child(svc: &mut Svc, owner: u8, child: u8, child_project: &str, harp: &str) {
    let store = svc.stores.stores.get(HERE).unwrap();
    crate::fakes::run(
        store,
        owner,
        NOW - 70 * DAY + 30 + i64::from(child) * 3,
        sessions::LaunchChild {
            child_project_id: child_project.to_string(),
            child_session: id_text(child),
            child_root: format!("/work/{child_project}"),
            harp: harp.to_string(),
            engine: "claude-code".to_string(),
            trust: sessions::TargetTrust {
                same_project: child_project == HERE,
                trusted: true,
            },
            delegation_limit: 4,
            requester: None,
            prompt: String::new(),
        },
    );
    let store = svc
        .stores
        .stores
        .entry(child_project.to_string())
        .or_default();
    crate::fakes::run(
        store,
        child,
        NOW - 70 * DAY + 31 + i64::from(child) * 3,
        sessions::MintSession {
            harp: harp.to_string(),
            project_id: child_project.to_string(),
            project_dir: format!("/work/{child_project}"),
            output_dir: String::new(),
            engine: "claude-code".to_string(),
            origin: sessions::Origin::Agent,
            started_at: None,
            depth: 1,
            owner: Some(at_session(HERE, owner)),
        },
    );
    ended(
        store,
        child,
        NOW - 70 * DAY + 32 + i64::from(child) * 3,
        NOW - 60 * DAY,
    );
    svc.liveness.0.insert(harp.to_string(), LockState::Free);
    svc.files.last_active.insert(
        harp.to_string(),
        crate::Timestamp {
            seconds: NOW - 60 * DAY,
            nanos: 0,
        },
    );
}

fn record_of(svc: &mut Svc, harp: &str) -> session_index::SessionRecord {
    ready(svc.list(all()))
        .unwrap()
        .into_iter()
        .find(|view| view.record.harp == harp)
        .unwrap_or_else(|| panic!("{harp} is listed"))
        .record
}

// Rulings 94, 98, D28 K3/M3: a purge emits no event, so a destroy closes each
// child its session holds BEFORE it purges, in the child's own store (D19).
#[test]
fn a_destroy_closes_the_children_of_the_session_first() {
    let mut svc = service();
    finished(&mut svc, 1, "amber-swift-owl");
    launched_child(&mut svc, 1, 2, HERE, "kid-near");
    launched_child(&mut svc, 1, 3, ELSEWHERE, "kid-far");
    assert_eq!(record_of(&mut svc, "amber-swift-owl").children.len(), 2);
    let destroyed = ready(svc.remove(destroy("amber-swift-owl"))).unwrap();
    assert!(destroyed.applied);
    for kid in ["kid-near", "kid-far"] {
        let record = record_of(&mut svc, kid);
        assert!(record.closed, "{kid} is closed");
    }
    // A redelivered destroy step (its keyed command again) records nothing.
    let again = session_policies::destroy(
        &at_session(HERE, 1),
        None,
        &[sessions::Child {
            session: Some(at_session(HERE, 2)),
            root: String::new(),
        }],
    );
    let before = head(
        &mut svc,
        sessions::SESSION_DOMAIN,
        uuid::Uuid::from_bytes([2; 16]),
    );
    crate::fakes::run_keyed(svc.store(HERE), NOW, again.close[0].book());
    assert_eq!(
        head(
            &mut svc,
            sessions::SESSION_DOMAIN,
            uuid::Uuid::from_bytes([2; 16])
        ),
        before,
        "a duplicate"
    );
}

// M3: a destroyed child is forgotten by its owner before its purge.
#[test]
fn a_destroyed_child_is_forgotten_by_its_owner() {
    let mut svc = service();
    finished(&mut svc, 1, "amber-swift-owl");
    launched_child(&mut svc, 1, 2, HERE, "kid-near");
    ready(svc.remove(destroy("kid-near"))).unwrap();
    assert!(record_of(&mut svc, "amber-swift-owl").children.is_empty());
    assert!(!record_of(&mut svc, "amber-swift-owl").closed);
}

// A destroy that is only reported sends nothing to the children.
#[test]
fn a_destroy_reported_and_not_applied_closes_no_child() {
    let mut svc = service();
    finished(&mut svc, 1, "amber-swift-owl");
    launched_child(&mut svc, 1, 2, HERE, "kid-near");
    ready(svc.remove(remove("amber-swift-owl", false))).unwrap();
    assert!(!record_of(&mut svc, "kid-near").closed);
}

// D30: `session show` lists the messages the session was sent that no turn
// has carried; an inform to an idle session stays there, visibly.
#[test]
fn show_lists_the_pending_messages() {
    let mut svc = service();
    svc.seed(HERE, 1, "amber-swift-owl", NOW - DAY, None);
    let store = svc.stores.stores.get(HERE).unwrap();
    crate::fakes::run(
        store,
        1,
        NOW - DAY + 10,
        sessions::SendMessage {
            message_id: "m1".to_string(),
            urgency: sessions::Urgency::Inform,
            text: "when you are next on".to_string(),
            requester: None,
            supported: vec![sessions::Urgency::Inform],
        },
    );
    let shown = ready(svc.show(ShowQuery {
        project: None,
        harp: "amber-swift-owl".to_string(),
    }))
    .unwrap();
    assert_eq!(
        shown.pending,
        vec![session_index::PendingMessage {
            message_id: "m1".to_string(),
            urgency: sessions::Urgency::Inform,
            text: "when you are next on".to_string(),
            sender: None,
        }]
    );
    let listed = ready(svc.list(all())).unwrap();
    assert!(listed[0].pending.is_empty(), "a listing does not fill it");
}

// ------------------------------------------ the orchestrator's commands (D25)

fn send(harp: &str, id: &str, urgency: sessions::Urgency, text: &str) -> crate::SendRequest {
    crate::SendRequest {
        project: None,
        harp: harp.to_string(),
        message_id: id.to_string(),
        urgency,
        text: text.to_string(),
    }
}

fn close(harp: &str) -> crate::CloseRequest {
    crate::CloseRequest {
        project: None,
        harp: harp.to_string(),
        cause: "done".to_string(),
    }
}

// D12: the user sends a message; its id is its key, so the same id again
// records nothing; `show` lists it pending (D30).
#[test]
fn the_user_sends_a_session_a_message_once_by_its_id() {
    let mut svc = service();
    svc.seed(HERE, 1, "amber-swift-owl", NOW - DAY, None);
    let sent = ready(svc.send(send(
        "amber-swift-owl",
        "m1",
        sessions::Urgency::Inform,
        "fyi",
    )))
    .unwrap();
    assert_eq!(
        (sent.harp.as_str(), sent.id.as_str(), sent.recorded),
        ("amber-swift-owl", "m1", true)
    );
    let again = ready(svc.send(send(
        "amber-swift-owl",
        "m1",
        sessions::Urgency::Inform,
        "fyi",
    )))
    .unwrap();
    assert!(!again.recorded, "a duplicate");
    let view = ready(svc.show(ShowQuery {
        project: None,
        harp: "amber-swift-owl".to_string(),
    }))
    .unwrap();
    assert_eq!(view.pending.len(), 1);
    assert_eq!(view.pending[0].text, "fyi");
    let minted =
        ready(svc.send(send("amber-swift-owl", "", sessions::Urgency::Inform, "x"))).unwrap();
    assert!(minted.recorded);
    assert_ne!(minted.id, "", "an id is minted when none is given");
}

// S-c: a primary takes no wake yet, so a wake to it is refused on the
// evidence; a send to no session is NoSession.
#[test]
fn a_wake_to_a_primary_and_a_send_to_nobody_are_refused() {
    let mut svc = service();
    svc.seed(HERE, 1, "amber-swift-owl", NOW - DAY, None);
    let refused =
        ready(svc.send(send("amber-swift-owl", "m1", sessions::Urgency::Wake, "go"))).unwrap_err();
    assert_eq!(
        refused.code(),
        "sessions-urgency-unsupported",
        "{refused:?}"
    );
    let nobody = ready(svc.send(send(
        "nobody-here-now",
        "m1",
        sessions::Urgency::Inform,
        "x",
    )))
    .unwrap_err();
    assert!(matches!(nobody, AppError::NoSession { .. }), "{nobody:?}");
}

// K1: the user closes a session once; a closed session takes no message.
#[test]
fn the_user_closes_a_session_and_it_takes_no_more_messages() {
    let mut svc = service();
    svc.seed(HERE, 1, "amber-swift-owl", NOW - DAY, None);
    assert!(ready(svc.close(close("amber-swift-owl"))).unwrap().recorded);
    assert!(
        !ready(svc.close(close("amber-swift-owl"))).unwrap().recorded,
        "a second close records nothing"
    );
    let refused = ready(svc.send(send(
        "amber-swift-owl",
        "m1",
        sessions::Urgency::Inform,
        "x",
    )))
    .unwrap_err();
    assert_eq!(refused.code(), "sessions-session-closed");
}

// K2: the user launches a child of an owner; the owner holds it.
#[test]
fn the_user_launches_a_child_of_an_owner() {
    let mut svc = service();
    svc.seed(HERE, 1, "amber-swift-owl", NOW - DAY, None);
    let launched = ready(svc.launch_child(crate::LaunchRequest {
        project: None,
        owner: "amber-swift-owl".to_string(),
        harp: "lone-wolf-pup".to_string(),
        engine: "claude-code".to_string(),
        prompt: "port the parser".to_string(),
        delegation_limit: 4,
        session: String::new(),
    }))
    .unwrap();
    assert!(launched.recorded);
    assert_eq!(launched.harp, "amber-swift-owl");
    let view = ready(svc.show(ShowQuery {
        project: None,
        harp: "amber-swift-owl".to_string(),
    }))
    .unwrap();
    assert_eq!(view.record.children.len(), 1);
    assert_eq!(
        view.record.children[0].session.as_ref().unwrap().session,
        launched.id
    );
    assert_eq!(view.record.children[0].root, "/work/proj");
    let limited = ready(svc.launch_child(crate::LaunchRequest {
        project: None,
        owner: "amber-swift-owl".to_string(),
        harp: "lone-wolf-cub".to_string(),
        engine: "claude-code".to_string(),
        prompt: String::new(),
        delegation_limit: 1,
        session: String::new(),
    }))
    .unwrap_err();
    assert_eq!(limited.code(), "sessions-delegation-limit");
}

// D-N45: the caller may name the child's stable id (what it hands the daemon
// for the child is keyed by it); it is the child the owner holds.
#[test]
fn a_launch_takes_the_childs_id_the_caller_names() {
    let mut svc = service();
    svc.seed(HERE, 1, "amber-swift-owl", NOW - DAY, None);
    let named = "0b0b0b0b-0b0b-4b0b-8b0b-0b0b0b0b0b0b";
    let launched = ready(svc.launch_child(crate::LaunchRequest {
        project: None,
        owner: "amber-swift-owl".to_string(),
        harp: "lone-wolf-pup".to_string(),
        engine: "claude-code".to_string(),
        prompt: String::new(),
        delegation_limit: 4,
        session: named.to_string(),
    }))
    .unwrap();
    assert_eq!(launched.id, named);
    let view = ready(svc.show(ShowQuery {
        project: None,
        harp: "amber-swift-owl".to_string(),
    }))
    .unwrap();
    assert_eq!(
        view.record.children[0].session.as_ref().unwrap().session,
        named
    );
}

// M1: the user asks a session to clear; the clear is recorded by its id.
#[test]
fn the_user_asks_a_session_to_clear() {
    let mut svc = service();
    svc.seed(HERE, 1, "amber-swift-owl", NOW - DAY, None);
    let cleared = ready(svc.clear(crate::ClearRequest {
        project: None,
        harp: "amber-swift-owl".to_string(),
        clear_id: "c1".to_string(),
    }))
    .unwrap();
    assert_eq!((cleared.id.as_str(), cleared.recorded), ("c1", true));
    let minted = ready(svc.clear(crate::ClearRequest {
        project: None,
        harp: "amber-swift-owl".to_string(),
        clear_id: String::new(),
    }))
    .unwrap();
    assert_ne!(minted.id, "");
}

#[test]
fn the_urgencies_a_send_gives_as_evidence() {
    use sessions::Urgency::{Inform, Steer, Wake};
    assert_eq!(
        crate::urgencies_of(true, "claude-code"),
        [Inform, Wake, Steer]
    );
    assert_eq!(crate::urgencies_of(false, "claude-code"), [Inform]);
    assert_eq!(crate::urgencies_of(true, "opencode"), [Inform]);
}

// ------------------------------------------------------------------ a project's harp (S54)

fn name_request(harps: &[&str]) -> crate::NameProjectRequest {
    crate::NameProjectRequest {
        project: None,
        harps: harps.iter().map(|h| h.to_string()).collect(),
    }
}

// S54: a project is named once, with the first harp tried, and keeps it: a
// second naming claims nothing and names nothing.
#[test]
fn a_project_is_named_once_and_keeps_its_harp() {
    let mut svc = service();
    let named = ready(svc.name_project(name_request(&["calm-quiet-heron"]))).unwrap();
    assert_eq!(
        named,
        crate::ProjectHarp {
            project_id: HERE.to_string(),
            harp: "calm-quiet-heron".to_string(),
            named: true,
        }
    );
    let again = ready(svc.name_project(name_request(&["brisk-tall-fern"]))).unwrap();
    assert_eq!(again.harp, "calm-quiet-heron", "permanent");
    assert!(!again.named);
}

// S54: "its claim unique per user": a harp another project holds is passed
// over for the next; with none left, the taken refusal is the answer.
#[test]
fn a_harp_another_project_holds_is_passed_over_for_the_next() {
    let mut svc = service();
    svc.stores.one = true;
    ready(svc.name_project(crate::NameProjectRequest {
        project: Some(ELSEWHERE.to_string()),
        harps: vec!["calm-quiet-heron".to_string()],
    }))
    .unwrap();
    let named =
        ready(svc.name_project(name_request(&["calm-quiet-heron", "brisk-tall-fern"]))).unwrap();
    assert_eq!(named.harp, "brisk-tall-fern");
    assert!(named.named);
    // A third project, through the same service (its clock's command ids
    // run on: a fresh fake clock would repeat the first claim's id, a
    // DUPLICATE on that stream).
    svc.identity.bound = Some(project("calm-otter"));
    let error = ready(svc.name_project(name_request(&["calm-quiet-heron"]))).unwrap_err();
    assert_eq!(error.code(), "sessions-project-name-taken");
    assert_eq!(error.exit(), Exit::REFUSED);
}

// Nothing to try is a failure of the caller's, not a refusal.
#[test]
fn a_project_given_no_harp_to_try_is_not_named() {
    let mut svc = service();
    let error = ready(svc.name_project(name_request(&[]))).unwrap_err();
    assert_eq!(error.code(), "sessions-application-no-project-harp");
}
