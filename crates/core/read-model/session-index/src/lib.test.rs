//! The session index on the projector port, caught up: each command runs
//! through angzarr-embedded's kernel over angzarr-ports' in-memory store,
//! and an index kept across commands catches up after each of them; a fresh
//! index catches up from the store, as every read of another process does.

use angzarr_embedded::angzarr_ports::memory::MemoryStore;
use angzarr_embedded::v1::{command_page, Command, CommandBook, CommandPage, Cover, Uuid};
use angzarr_embedded::{
    Any, CatchUp, EventStore, Kernel, OnRetryable, Payload, Purge, Registry, Sampled, StreamRef,
    Timestamp,
};
use sessions::{
    AdoptRotations, BindEngineSession, EndRecorded, MintSession, Origin, RecordEnd, Rotation,
    SessionAggregate, SessionCommand, SetKept, SESSION_DOMAIN,
};

use super::{SessionIndex, SessionIndexes, SessionRecord};

type Index = CatchUp<SessionIndexes>;

use angzarr_embedded::testing::block_on_ready as ready;

const FIRST: u8 = 1;
const SECOND: u8 = 2;

/// One project's store, and the clock its commands are sampled at.
struct Project {
    store: MemoryStore,
    registry: Registry,
    seconds: i64,
}

impl Project {
    fn new() -> Project {
        let mut registry = Registry::new();
        registry
            .aggregate::<SessionAggregate>(SessionAggregate::default)
            .unwrap();
        Project {
            store: MemoryStore::new(),
            registry,
            seconds: 100,
        }
    }

    /// Run `command` on the session stream `id`, then catch `index` up.
    fn run(&mut self, index: &mut Index, id: u8, command: impl Into<SessionCommand>) {
        self.run_book(index, book(id, command.into().to_any()));
    }

    /// Record a bind, a fact since the owner ruling of 2026-11-04 (S5 review
    /// item 8), as the hook delivers it.
    fn bound(&mut self, index: &mut Index, id: u8, bind: sessions::BindEngineSession) {
        self.run_book(index, sessions::bind_book(cover(id), &bind));
    }

    fn run_book(&mut self, index: &mut Index, book: CommandBook) {
        self.seconds += 1;
        let sampled = Sampled {
            now: Timestamp {
                seconds: self.seconds,
                nanos: 0,
            },
            command_id: Uuid::from_bytes([self.seconds as u8; 16]),
        };
        ready(
            Kernel::new(&self.registry)
                .on_retryable(OnRetryable {
                    max_retries: 0,
                    ..OnRetryable::default()
                })
                .handle::<SessionAggregate, _>(&self.store, book, &sampled),
        )
        .unwrap_or_else(|failure| panic!("{failure:?}"));
        self.catch_up(index);
    }

    /// Purge session stream `id`: what a store's purge does.
    fn purge(&self, id: u8) -> u64 {
        let cover = cover(id);
        let stream = StreamRef::try_from(&cover).unwrap();
        let head = ready(EventStore::head(&self.store, stream)).unwrap();
        ready(Purge::purge(&self.store, stream, head.next_sequence)).unwrap()
    }

    fn catch_up(&self, index: &mut Index) {
        let caught = ready(index.step(&self.store, self.registry.catalog())).unwrap();
        assert!(caught.failures().is_empty(), "{:?}", caught.failures());
    }

    /// An index built afresh from the store: another process's.
    fn rebuilt(&self) -> Index {
        let mut index = SessionIndex::catch_up();
        self.catch_up(&mut index);
        index
    }
}

fn cover(id: u8) -> Cover {
    Cover {
        domain: SESSION_DOMAIN.to_string(),
        root: Some(Uuid::from_bytes([id; 16])),
        edition: None,
    }
}

fn book(id: u8, payload: Any) -> CommandBook {
    CommandBook {
        cover: Some(cover(id)),
        page: Some(CommandPage {
            body: Some(command_page::Body::Command(Command {
                payload: Some(payload),
                handling: None,
            })),
            ..CommandPage::default()
        }),
    }
}

fn mint(harp: &str) -> MintSession {
    MintSession {
        harp: harp.to_string(),
        project_id: "calm-otter".to_string(),
        project_dir: "/work/proj".to_string(),
        output_dir: format!("/home/Documents/ctxloom/proj/{harp}"),
        engine: "claude-code".to_string(),
        origin: Origin::Session,
        started_at: None,
        depth: 0,
        owner: None,
    }
}

fn bind(id: &str, path: &str) -> BindEngineSession {
    BindEngineSession {
        native_id: id.to_string(),
        transcript_path: path.to_string(),
        engine_version: None,
    }
}

fn records(index: &Index) -> Vec<SessionRecord> {
    index.projector().index().records()
}

fn harps(index: &Index) -> Vec<String> {
    records(index).into_iter().map(|r| r.harp).collect()
}

fn id_text(id: u8) -> String {
    let hex = format!("{id:02x}").repeat(16);
    format!(
        "{}-{}-{}-{}-{}",
        &hex[0..8],
        &hex[8..12],
        &hex[12..16],
        &hex[16..20],
        &hex[20..32]
    )
}

// Go: ListAll / ListForProject hold every session in the store.
#[test]
fn the_index_holds_every_session_minted() {
    let mut project = Project::new();
    let mut index = SessionIndex::catch_up();
    project.run(&mut index, FIRST, mint("amber-swift-owl"));
    project.run(&mut index, SECOND, mint("brisk-copper-moth"));
    let mut got = harps(&index);
    got.sort();
    assert_eq!(got, ["amber-swift-owl", "brisk-copper-moth"]);
}

// A record is the session as its events leave it (Go: Entry).
#[test]
fn a_record_is_the_session_its_events_fold() {
    let mut project = Project::new();
    let mut index = SessionIndex::catch_up();
    project.run(&mut index, FIRST, mint("amber-swift-owl"));
    project.bound(&mut index, FIRST, bind("id-1", "/t1"));
    project.bound(&mut index, FIRST, bind("id-2", "/t2"));
    project.run(&mut index, FIRST, SetKept { kept: true });
    project.run(
        &mut index,
        FIRST,
        RecordEnd {
            at: Timestamp {
                seconds: 900,
                nanos: 0,
            },
        },
    );
    let [record] = records(&index).try_into().unwrap();
    assert_eq!(record.id, id_text(FIRST));
    assert_eq!(record.harp, "amber-swift-owl");
    assert_eq!(record.project_id, "calm-otter");
    assert_eq!(record.project_dir, "/work/proj");
    assert_eq!(record.engine, "claude-code");
    assert_eq!(record.origin, Some(Origin::Session));
    assert_eq!(record.native_id, "id-2");
    assert_eq!(record.transcript_path, "/t2");
    assert_eq!(record.rotations.len(), 1);
    assert_eq!(record.rotations[0].native_id, "id-1");
    assert!(record.kept);
    assert_eq!(
        record.started_at,
        Timestamp {
            seconds: 101,
            nanos: 0
        }
    );
    assert_eq!(
        record.ended_at,
        Some(Timestamp {
            seconds: 900,
            nanos: 0
        })
    );
}

// S55 (owner, 2026-11-12, slice 2): a title is the record's; the harp and
// the places keyed by it stay as they were.
#[test]
fn a_titled_session_keeps_its_harp_and_carries_its_title() {
    let mut project = Project::new();
    let mut index = SessionIndex::catch_up();
    project.run(&mut index, FIRST, mint("amber-swift-owl"));
    let before = index.projector().index().find("amber-swift-owl").unwrap();
    project.run(
        &mut index,
        FIRST,
        sessions::SetSessionTitle {
            title: "the auth refactor".to_string(),
        },
    );
    assert_eq!(harps(&index), ["amber-swift-owl"]);
    let after = index.projector().index().find("amber-swift-owl").unwrap();
    assert_eq!(after.title, "the auth refactor");
    assert_eq!(after.output_dir, before.output_dir);
}

// Go: TestFindBySessionID_ResolvesCurrentBinding / _ResolvesRotatedAwayID /
// _UnknownIDReturnsNil.
#[test]
fn a_native_id_resolves_through_the_current_binding_and_the_lineage() {
    let mut project = Project::new();
    let mut index = SessionIndex::catch_up();
    project.run(&mut index, FIRST, mint("amber-swift-owl"));
    project.bound(&mut index, FIRST, bind("id-1", "/t1"));
    project.bound(&mut index, FIRST, bind("id-2", "/t2"));
    project.run(&mut index, SECOND, mint("brisk-copper-moth"));
    let index = index.projector().index();
    assert_eq!(
        index.find_by_native_id("id-2").unwrap().harp,
        "amber-swift-owl"
    );
    assert_eq!(
        index.find_by_native_id("id-1").unwrap().harp,
        "amber-swift-owl",
        "rotated away, still named by an old transcript"
    );
    assert!(index.find_by_native_id("nope").is_none());
    assert!(
        index.find_by_native_id("").is_none(),
        "no id names no session"
    );
}

#[test]
fn an_adopted_rotation_is_in_the_lineage() {
    let mut project = Project::new();
    let mut index = SessionIndex::catch_up();
    project.run(&mut index, FIRST, mint("amber-swift-owl"));
    project.run(
        &mut index,
        FIRST,
        AdoptRotations {
            rotations: vec![Rotation {
                native_id: "orphan-one".to_string(),
                transcript_path: "/o".to_string(),
                rotated_at: Some(Timestamp {
                    seconds: 5,
                    nanos: 0,
                }),
            }],
        },
    );
    let index = index.projector().index();
    assert_eq!(
        index.find_by_native_id("orphan-one").unwrap().harp,
        "amber-swift-owl"
    );
}

// Design: Go lists most recently WORKED first; the index knows only when each
// session started, so it orders by that (a tie by harp); a listing that reads
// the files may order by activity itself.
#[test]
fn the_list_is_most_recently_started_first() {
    let mut project = Project::new();
    let mut index = SessionIndex::catch_up();
    project.run(&mut index, FIRST, mint("amber-swift-owl"));
    project.run(&mut index, SECOND, mint("brisk-copper-moth"));
    assert_eq!(harps(&index), ["brisk-copper-moth", "amber-swift-owl"]);
}

// Another process's index catches up from the store and holds the same.
#[test]
fn a_fresh_index_catches_up_from_the_store() {
    let mut project = Project::new();
    let mut index = SessionIndex::catch_up();
    project.run(&mut index, FIRST, mint("amber-swift-owl"));
    project.bound(&mut index, FIRST, bind("id-1", "/t1"));
    assert_eq!(records(&project.rebuilt()), records(&index));
}

// A stream purged whole leaves the index, as the projector port hands it: a
// book with no event at 0.
#[test]
fn a_session_stream_purged_leaves_the_index() {
    let mut project = Project::new();
    let mut index = SessionIndex::catch_up();
    project.run(&mut index, FIRST, mint("amber-swift-owl"));
    project.run(&mut index, SECOND, mint("brisk-copper-moth"));
    project.purge(FIRST);
    project.catch_up(&mut index);
    assert_eq!(harps(&index), ["brisk-copper-moth"]);
}

// An index of another domain's events is no index of sessions: it folds only
// the sessions domain (the claim streams are not sessions).
#[test]
fn a_catch_up_reads_only_the_sessions_domain() {
    let _: Index = SessionIndex::catch_up();
    let _ = EndRecorded {
        at: Timestamp::default(),
    };
}

// The projector's name is the index's.
#[test]
fn the_projector_is_named_for_the_index() {
    use angzarr_embedded::ProjectorHandler;
    assert_eq!(SessionIndexes::default().name(), "session-index");
}

// A stable id nobody holds is none.
#[test]
fn an_id_nobody_holds_finds_no_session() {
    let mut project = Project::new();
    let mut index = SessionIndex::catch_up();
    project.run(&mut index, FIRST, mint("amber-swift-owl"));
    let index = index.projector().index();
    assert!(index.find_by_id(&id_text(SECOND)).is_none());
    assert_eq!(
        index.find_by_id(&id_text(FIRST)).unwrap().harp,
        "amber-swift-owl"
    );
}

// A stream purged and minted again on the same root starts afresh: the new
// session replaces the old, as the projector hands it from sequence 0.
#[test]
fn a_stream_purged_and_minted_again_starts_afresh() {
    let mut project = Project::new();
    let mut index = SessionIndex::catch_up();
    project.run(&mut index, FIRST, mint("amber-swift-owl"));
    project.bound(&mut index, FIRST, bind("id-1", "/t1"));
    project.purge(FIRST);
    project.run(&mut index, FIRST, mint("bright-keen-hawk"));
    let [record] = records(&index).try_into().unwrap();
    assert_eq!(record.harp, "bright-keen-hawk");
    assert_eq!(record.native_id, "", "nothing of the old stream is left");
}

// An event the index cannot read is the projector's bug, naming the type.
#[test]
fn an_event_the_index_cannot_read_is_a_bug_naming_it() {
    let any = Any {
        type_url: "/ctxloom.sessions.v1.Nope".to_string(),
        value: Vec::new(),
    };
    let error = sessions::SessionEvent::from_any(&any).unwrap_err();
    let coded = held_projection::bug(&error);
    assert!(format!("{coded:?}").contains("Nope"), "{coded:?}");
}

// A framework meta-event on a session stream is no session event: it is not
// folded, and not the projector's bug.
#[test]
fn a_meta_event_is_not_folded() {
    use angzarr_embedded::v1::{event_page, EventBook, EventPage, MetaEvent};
    let mut index = SessionIndex::new();
    let meta = EventBook {
        cover: Some(cover(FIRST)),
        pages: vec![EventPage {
            sequence: 0,
            body: Some(event_page::Body::MetaEvent(MetaEvent::default())),
            meta: None,
        }],
        next_sequence: 1,
        ..EventBook::default()
    };
    index.project(&meta).unwrap();
}

// ------------------------------------------------- the transcript (ruling 87a)

/// A capture fact on session stream `id`, as a hook delivers it: keyed by its
/// external id, so a redelivery is a DUPLICATE.
fn fact(id: u8, payload: Any, external_id: &str) -> CommandBook {
    use angzarr_embedded::v1::{Fact, FactOrigin};
    CommandBook {
        cover: Some(cover(id)),
        page: Some(CommandPage {
            body: Some(command_page::Body::Fact(Fact {
                payload: Some(payload),
                origin: Some(FactOrigin {
                    external_id: external_id.to_string(),
                    description: String::new(),
                }),
            })),
            ..CommandPage::default()
        }),
    }
}

fn wire<M: angzarr_embedded::markup::DomainMessage>(message: &M) -> Any {
    Any::from_msg(&message.to_wire()).unwrap()
}

fn prompt(id: u8, prompt_id: &str, text: &str) -> CommandBook {
    let payload = wire(&sessions::RecordPromptSubmitted {
        prompt_id: prompt_id.to_string(),
        prompt: text.to_string(),
    });
    fact(id, payload, &format!("hook:prompt:{id}:{prompt_id}"))
}

fn words(id: u8, prompt_id: &str, index: u32, text: &str) -> CommandBook {
    let payload = wire(&sessions::RecordAssistantText {
        prompt_id: prompt_id.to_string(),
        turn_id: String::new(),
        message_id: "m1".to_string(),
        index,
        is_final: false,
        text: text.to_string(),
    });
    fact(id, payload, &format!("hook:text:{id}:m1:{index}"))
}

fn turn_end(id: u8, prompt_id: &str) -> CommandBook {
    let payload = wire(&sessions::RecordTurnEnded {
        prompt_id: prompt_id.to_string(),
    });
    fact(id, payload, &format!("hook:turn-end:{id}:{prompt_id}"))
}

fn lines(text: &str) -> Vec<serde_json::Value> {
    text.lines()
        .map(|line| serde_json::from_str(line).unwrap())
        .collect()
}

// Ruling 87a: the transcript is read from the session's events, in D27's
// envelope (`v, harp, session_id, engine, seq, ts, kind`), each capture event
// a line, `seq` counting them, the instant the page's.
#[test]
fn a_sessions_transcript_is_folded_from_its_capture_events() {
    let mut project = Project::new();
    let mut index = SessionIndex::catch_up();
    project.run(&mut index, FIRST, mint("amber-swift-owl"));
    project.run_book(&mut index, prompt(FIRST, "p1", "Fix the build"));
    project.run_book(&mut index, words(FIRST, "p1", 0, "Done."));
    project.run_book(&mut index, turn_end(FIRST, "p1"));
    let text = index
        .projector()
        .index()
        .transcript(&id_text(FIRST))
        .expect("a transcript");
    let got = lines(&text);
    let kinds: Vec<&str> = got.iter().map(|l| l["kind"].as_str().unwrap()).collect();
    assert_eq!(kinds, ["entry", "assistant_delta", "turn_end"]);
    assert_eq!(got[0]["entry"]["type"], "user");
    assert_eq!(got[0]["entry"]["content"], "Fix the build");
    assert_eq!(got[1]["delta"]["text"], "Done.");
    for (n, line) in got.iter().enumerate() {
        assert_eq!(line["v"], 1);
        assert_eq!(line["seq"], n);
        assert_eq!(line["harp"], "amber-swift-owl");
        assert_eq!(line["session_id"], id_text(FIRST));
        assert_eq!(line["engine"], "claude-code");
    }
    // The mint was sampled at 101, the prompt at 102 (the Project's clock).
    assert_eq!(got[0]["ts"], "1970-01-01T00:01:42Z");
    assert!(text.ends_with('\n'), "a line each, newline ended");
}

// Ruling 87a: a session no hook has delivered for holds no transcript (the
// read says it was never captured), and another session's lines are not its.
#[test]
fn a_session_with_no_capture_holds_no_transcript_and_none_of_anothers() {
    let mut project = Project::new();
    let mut index = SessionIndex::catch_up();
    project.run(&mut index, FIRST, mint("amber-swift-owl"));
    project.run(&mut index, SECOND, mint("brisk-copper-moth"));
    project.run_book(&mut index, prompt(SECOND, "p1", "only the second's"));
    let held = index.projector().index();
    assert_eq!(held.transcript(&id_text(FIRST)), None);
    let second = held.transcript(&id_text(SECOND)).expect("the second's");
    assert_eq!(lines(&second).len(), 1);
}

// Ruling 87a: the transcript is LIVE: what the hooks delivered so far is
// read, by an index kept across deliveries (continuing its `seq`) and by one
// caught up afresh (another terminal's read), alike; a redelivery adds no line.
#[test]
fn the_transcript_holds_what_was_delivered_so_far_and_a_redelivery_adds_nothing() {
    let mut project = Project::new();
    let mut kept = SessionIndex::catch_up();
    project.run(&mut kept, FIRST, mint("amber-swift-owl"));
    project.run_book(&mut kept, prompt(FIRST, "p1", "first"));
    let before = kept.projector().index().transcript(&id_text(FIRST));
    assert_eq!(before.as_deref().map(|t| lines(t).len()), Some(1));
    project.run_book(&mut kept, words(FIRST, "p1", 0, "partial"));
    project.run_book(&mut kept, words(FIRST, "p1", 0, "partial"));
    let after = kept
        .projector()
        .index()
        .transcript(&id_text(FIRST))
        .unwrap();
    let got = lines(&after);
    assert_eq!(got.len(), 2, "the redelivery is a DUPLICATE: {got:#?}");
    assert_eq!(got[1]["seq"], 1);
    let fresh = project.rebuilt();
    assert_eq!(
        fresh.projector().index().transcript(&id_text(FIRST)),
        Some(after)
    );
}

// Owner ruling 94b: a destroyed session's stream is purged whole, and with it
// its transcript: nothing of it is read again.
#[test]
fn a_destroyed_sessions_transcript_is_gone_with_its_stream() {
    let mut project = Project::new();
    let mut index = SessionIndex::catch_up();
    project.run(&mut index, FIRST, mint("amber-swift-owl"));
    project.run_book(&mut index, prompt(FIRST, "p1", "to be destroyed"));
    assert!(index
        .projector()
        .index()
        .transcript(&id_text(FIRST))
        .is_some());
    project.purge(FIRST);
    project.catch_up(&mut index);
    assert_eq!(index.projector().index().transcript(&id_text(FIRST)), None);
    assert_eq!(
        project
            .rebuilt()
            .projector()
            .index()
            .transcript(&id_text(FIRST)),
        None
    );
}

// Ruling 87a: a session's last activity is its newest capture's instant.
#[test]
fn a_records_last_capture_is_its_newest_capture_events_instant() {
    let mut project = Project::new();
    let mut index = SessionIndex::catch_up();
    project.run(&mut index, FIRST, mint("amber-swift-owl"));
    assert_eq!(records(&index)[0].last_captured, None);
    project.run_book(&mut index, prompt(FIRST, "p1", "first"));
    project.run_book(&mut index, words(FIRST, "p1", 0, "then"));
    let last = records(&index)[0].last_captured.expect("captured");
    assert_eq!(last.seconds, project.seconds);
}

// ------------------------------------------------------------ owner link

// D26, K2: the record carries the owner a child was minted for, its close,
// and the children an owner holds (the owner-to-children view, per owner
// stream): launched minus ended.
#[test]
fn a_record_carries_its_owner_its_close_and_the_children_it_holds() {
    let owner = sessions::SessionRef {
        project_id: "calm-otter".to_string(),
        session: id_text(FIRST),
    };
    let child = |n: u8| sessions::SessionRef {
        project_id: "calm-otter".to_string(),
        session: id_text(n),
    };
    let launch = |n: u8| sessions::LaunchChild {
        child_project_id: "calm-otter".to_string(),
        child_session: id_text(n),
        child_root: format!("/w/{n}"),
        harp: format!("kid-{n}"),
        engine: "claude-code".to_string(),
        trust: sessions::TargetTrust {
            same_project: true,
            trusted: false,
        },
        delegation_limit: 4,
        requester: None,
        prompt: String::new(),
    };
    let mut project = Project::new();
    let mut index = SessionIndex::catch_up();
    project.run(&mut index, FIRST, mint("owner-owl"));
    project.run(&mut index, FIRST, launch(SECOND));
    project.run(&mut index, FIRST, launch(3));
    project.run(
        &mut index,
        FIRST,
        sessions::EndChild {
            child: Some(child(3)),
            cause: "closed".to_string(),
        },
    );
    project.run(
        &mut index,
        SECOND,
        MintSession {
            owner: Some(owner.clone()),
            depth: 1,
            ..mint("kid-2")
        },
    );
    project.run(
        &mut index,
        SECOND,
        sessions::CloseSession {
            cause: "done".to_string(),
            requester: None,
        },
    );
    let index = project.rebuilt();
    let found = |harp: &str| index.projector().index().find(harp).unwrap();
    let held = found("owner-owl");
    assert_eq!(held.owner, None);
    assert!(!held.closed);
    assert_eq!(
        held.children,
        vec![sessions::Child {
            session: Some(child(SECOND)),
            root: "/w/2".to_string(),
        }]
    );
    let kid = found("kid-2");
    assert_eq!(kid.owner, Some(owner));
    assert!(kid.closed);
    assert!(kid.children.is_empty());
}

// S32: registered in the kernel, the index is what a HandleSync waits for,
// and it answers the command's session as its resource message, named by
// its project's harp and its own (S42, S54); a catch-up of the same index
// after it folds nothing twice.
#[test]
fn handle_sync_answers_the_session_as_its_resource_named_by_both_harps() {
    use prost::Message as _;
    let index = SessionIndexes::default();
    let mut registry = Registry::new();
    registry
        .aggregate::<SessionAggregate>(SessionAggregate::default)
        .unwrap();
    registry
        .aggregate::<sessions::ProjectAggregate>(sessions::ProjectAggregate::default)
        .unwrap();
    registry
        .projector_handler(&super::SOURCES, index.clone())
        .unwrap();
    let store = MemoryStore::new();
    let sampled = |n: u8| Sampled {
        now: Timestamp {
            seconds: 100 + i64::from(n),
            nanos: 0,
        },
        command_id: Uuid::from_bytes([n; 16]),
    };
    let named = sessions::ProjectCommand::from(sessions::NameProject {
        project_id: "calm-otter".to_string(),
        harp: "calm-quiet-heron".to_string(),
    });
    let project = Cover {
        domain: sessions::PROJECTS_DOMAIN.to_string(),
        root: Some(sessions::project_root("calm-otter")),
        edition: None,
    };
    let kernel = Kernel::new(&registry);
    ready(kernel.handle_any(
        &store,
        angzarr_embedded::CommandBookExt::command_of(project, &named),
        &sampled(1),
    ))
    .unwrap();
    let synced = ready(kernel.handle_sync(
        &store,
        book(
            FIRST,
            SessionCommand::from(mint("amber-swift-owl")).to_any(),
        ),
        &["session-index".to_string()],
        &sampled(2),
    ))
    .unwrap_or_else(|failure| panic!("{failure:?}"));
    let [projection] = synced.projections.as_slice() else {
        panic!("one projection: {:?}", synced.projections);
    };
    assert_eq!(projection.projector, "session-index");
    let any = projection.projection.as_ref().expect("a view");
    let session = daemon_proto::v1::Session::decode(any.value.as_slice()).unwrap();
    assert_eq!(
        session.name,
        "projects/calm-quiet-heron/sessions/amber-swift-owl"
    );
    assert_eq!(session.uid, id_text(FIRST));
    assert_eq!(session.engine, "claude-code");
    let mut caught = SessionIndex::caught_up(index.clone());
    ready(caught.step(&store, registry.catalog())).unwrap();
    assert_eq!(index.index().records().len(), 1, "folded once");
}

/// The projects' stream of calm-otter, naming it `harp`, as the store hands
/// it; `pages` none for a gone stream, else its one event.
fn project_book(harp: Option<&str>, next_sequence: u64) -> angzarr_embedded::v1::EventBook {
    use angzarr_embedded::v1::{event_page, EventBook, EventPage};
    let pages = harp
        .map(|harp| {
            vec![EventPage {
                sequence: 0,
                body: Some(event_page::Body::Event(
                    sessions::ProjectEvent::ProjectNamed(sessions::ProjectNamed {
                        project_id: "calm-otter".to_string(),
                        harp: harp.to_string(),
                    })
                    .to_any(),
                )),
                meta: None,
            }]
        })
        .unwrap_or_default();
    EventBook {
        cover: Some(Cover {
            domain: sessions::PROJECTS_DOMAIN.to_string(),
            root: Some(sessions::project_root("calm-otter")),
            edition: None,
        }),
        pages,
        snapshot: None,
        next_sequence,
    }
}

fn hand(index: &SessionIndexes, book: angzarr_embedded::v1::EventBook) {
    use angzarr_embedded::angzarr_router::hpb::ProjectRequest;
    use angzarr_embedded::ProjectorHandler;
    ready(index.dispatch(&ProjectRequest { events: Some(book) })).unwrap();
}

// S32, S42: the session as its resource message: named by its project's harp
// and its own, its summary its title, its times, kept and closed, its
// incarnation, its owner and its children by their names.
#[test]
fn a_session_is_its_resource_with_its_owner_and_children_named() {
    let mut project = Project::new();
    let mut index = SessionIndex::catch_up();
    project.run(&mut index, FIRST, mint("boss"));
    project.run(
        &mut index,
        FIRST,
        sessions::LaunchChild {
            child_project_id: "calm-otter".to_string(),
            child_session: id_text(SECOND),
            child_root: "/work/proj".to_string(),
            harp: "kid".to_string(),
            engine: "claude-code".to_string(),
            trust: sessions::TargetTrust {
                same_project: true,
                trusted: true,
            },
            delegation_limit: 1,
            requester: None,
            prompt: String::new(),
        },
    );
    project.run(
        &mut index,
        SECOND,
        MintSession {
            depth: 1,
            owner: Some(sessions::SessionRef {
                project_id: "calm-otter".to_string(),
                session: id_text(FIRST),
            }),
            ..mint("kid")
        },
    );
    project.run_book(&mut index, prompt(SECOND, "p1", "Fix the build"));
    project.run(&mut index, SECOND, SetKept { kept: true });
    project.run(
        &mut index,
        SECOND,
        RecordEnd {
            at: Timestamp {
                seconds: 900,
                nanos: 0,
            },
        },
    );
    project.run(
        &mut index,
        SECOND,
        sessions::CloseSession {
            cause: "done".to_string(),
            requester: None,
        },
    );
    hand(index.projector(), project_book(Some("calm-quiet-heron"), 1));
    let held = index.projector().index();
    let child = held.resource(&held.find_by_id(&id_text(SECOND)).unwrap());
    assert_eq!(child.name, "projects/calm-quiet-heron/sessions/kid");
    assert_eq!(child.summary, "Fix the build");
    assert!(child.start_time.is_some());
    assert_eq!(child.end_time.map(|t| t.seconds), Some(900));
    assert!(child.kept);
    assert!(child.closed);
    assert_eq!(child.incarnation, 1);
    assert_eq!(child.owner, "projects/calm-quiet-heron/sessions/boss");
    let owner = held.resource(&held.find_by_id(&id_text(FIRST)).unwrap());
    assert_eq!(owner.children, ["projects/calm-quiet-heron/sessions/kid"]);
    assert!(!owner.kept && !owner.closed);
}

// A project stream gone (purged: no page at 0) takes its harp with it, and
// a session's name is empty again; a book with no page that is not at 0
// changes nothing.
#[test]
fn a_gone_project_stream_takes_its_harp_with_it() {
    let mut index = SessionIndex::new();
    let record = SessionRecord {
        project_id: "calm-otter".to_string(),
        harp: "amber-swift-owl".to_string(),
        ..SessionRecord::default()
    };
    index
        .project(&project_book(Some("calm-quiet-heron"), 1))
        .unwrap();
    index.project(&project_book(None, 1)).unwrap();
    assert_eq!(
        index.name_of(&record),
        "projects/calm-quiet-heron/sessions/amber-swift-owl"
    );
    index.project(&project_book(None, 0)).unwrap();
    assert_eq!(index.name_of(&record), "");
}

/// A fill that records each gap it is asked and reads nothing.
#[derive(Default)]
struct Recording(std::sync::Mutex<Vec<(u64, u64)>>);

impl held_projection::Fill for Recording {
    fn pages<'a>(
        &'a self,
        _: &'a angzarr_embedded::v1::Cover,
        from: u64,
        to: u64,
    ) -> angzarr_embedded::angzarr_ports::BoxFuture<
        'a,
        Result<Vec<angzarr_embedded::v1::EventPage>, angzarr_embedded::StoreError>,
    > {
        self.0.lock().unwrap().push((from, to));
        Box::pin(async { Ok(Vec::new()) })
    }
}

// S32: a model made filled reads a gap through its fill: a session's book
// that starts past where the model is asks for the pages before it.
#[test]
fn a_filled_model_reads_a_gap_through_its_fill() {
    use angzarr_embedded::angzarr_router::hpb::ProjectRequest;
    use angzarr_embedded::ProjectorHandler;
    let fill = std::sync::Arc::new(Recording::default());
    let model = crate::SessionIndexes::filled(fill.clone());
    let late = angzarr_embedded::v1::EventBook {
        cover: Some(angzarr_embedded::v1::Cover {
            domain: sessions::SESSION_DOMAIN.to_string(),
            root: Some(angzarr_embedded::v1::Uuid::from_bytes([7; 16])),
            edition: None,
        }),
        pages: vec![angzarr_embedded::v1::EventPage {
            sequence: 2,
            body: None,
            meta: None,
        }],
        snapshot: None,
        next_sequence: 3,
    };
    angzarr_embedded::testing::block_on_ready(
        model.dispatch(&ProjectRequest { events: Some(late) }),
    )
    .unwrap();
    assert_eq!(*fill.0.lock().unwrap(), [(0, 2)]);
}
