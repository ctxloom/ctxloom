//! A session as the daemon's client (slice 3): its project from its name
//! (OD-2), its reads, and its writes through the coordinator, keyed by one
//! id per call, so the retry on a new daemon is the same command (S8).

use std::sync::{Arc, Mutex};

use daemon_ports::{Called, CommandBook, DaemonApi, Daemons, Projection, Synced, Unreached};
use daemon_proto::v1;
use prost::Message;
use sessions_proto::v1 as wire;

use super::{urgency_of, Caller};
use crate::CallError;

const ME: &str = "projects/calm-quiet-heron/sessions/amber-quiet-heron";
const CHILD: &str = "projects/calm-quiet-heron/sessions/brisk-copper-moth";

/// What one test's daemons saw.
#[derive(Default)]
struct Seen {
    /// Each call, `<daemon>:<what>`.
    asked: Vec<String>,
    /// Each write's book.
    books: Vec<CommandBook>,
    /// Each write's projectors.
    projectors: Vec<Vec<String>>,
}

/// A daemon numbered `n`; `dying` ones cannot be reached for a write, after
/// they applied it; `projecting` ones answer the session-index projection.
#[derive(Clone)]
struct Fake {
    n: usize,
    seen: Arc<Mutex<Seen>>,
    dying: Arc<Mutex<Vec<usize>>>,
    projecting: bool,
}

impl Fake {
    fn ask(&self, what: &str) {
        self.seen
            .lock()
            .unwrap()
            .asked
            .push(format!("{}:{what}", self.n));
    }
}

impl DaemonApi for Fake {
    async fn get_daemon(&self, _: v1::GetDaemonRequest) -> Result<v1::Daemon, Called> {
        unreachable!()
    }

    async fn stop_daemon(&self, _: v1::StopDaemonRequest) -> Result<v1::Daemon, Called> {
        unreachable!("a session's call never stops a daemon (OD-3)")
    }

    async fn resolve_project(&self, _: v1::ResolveProjectRequest) -> Result<v1::Project, Called> {
        unreachable!("a session's project is its name's (OD-2)")
    }

    async fn get_project(&self, r: v1::GetProjectRequest) -> Result<v1::Project, Called> {
        self.ask(&format!("project {}", r.name));
        Ok(v1::Project {
            name: r.name,
            uid: "calm-otter".to_string(),
            ..v1::Project::default()
        })
    }

    async fn get_session(&self, r: v1::GetSessionRequest) -> Result<v1::Session, Called> {
        self.ask(&format!("session {}", r.name));
        assert!(r.env.is_some(), "the caller's environment is sent");
        Ok(v1::Session {
            name: r.name,
            owner: ME.to_string(),
            engine: "claude-code".to_string(),
            summary: "read".to_string(),
            ..v1::Session::default()
        })
    }

    async fn list_sessions(
        &self,
        r: v1::ListSessionsRequest,
    ) -> Result<v1::ListSessionsResponse, Called> {
        self.ask(&format!("list {}", r.parent));
        Ok(v1::ListSessionsResponse::default())
    }

    async fn search_sessions(
        &self,
        _: v1::SearchSessionsRequest,
    ) -> Result<v1::ListSessionsResponse, Called> {
        unreachable!()
    }

    async fn get_transcript(&self, r: v1::GetTranscriptRequest) -> Result<v1::Transcript, Called> {
        self.ask(&format!("transcript {}", r.name));
        Ok(v1::Transcript {
            name: r.name,
            ..v1::Transcript::default()
        })
    }

    async fn assemble_context(
        &self,
        r: v1::AssembleContextRequest,
    ) -> Result<v1::AssembledContext, Called> {
        self.ask(&format!(
            "assemble {} {} {}",
            r.name,
            r.fragments.join(","),
            r.tags.join(",")
        ));
        assert!(r.env.is_some(), "the caller's environment is sent");
        Ok(v1::AssembledContext {
            context: "CTX".to_string(),
            ..v1::AssembledContext::default()
        })
    }

    async fn handle_sync(
        &self,
        book: CommandBook,
        projectors: Vec<String>,
    ) -> Result<Synced, Called> {
        self.ask("write");
        let target = book
            .cover
            .as_ref()
            .and_then(|c| c.root.clone())
            .expect("a cover's root");
        {
            let mut seen = self.seen.lock().unwrap();
            seen.books.push(book);
            seen.projectors.push(projectors);
        }
        if self.dying.lock().unwrap().contains(&self.n) {
            return Err(Called::Unreachable {
                detail: "killed after the apply".to_string(),
            });
        }
        let projections = match self.projecting {
            true => vec![Projection {
                projector: "session-index".to_string(),
                projection: Some(
                    prost_types::Any::from_msg(&v1::Session {
                        name: CHILD.to_string(),
                        uid: uuid::Uuid::from_slice(&target.value)
                            .unwrap()
                            .hyphenated()
                            .to_string(),
                        summary: "projected".to_string(),
                        ..v1::Session::default()
                    })
                    .unwrap(),
                ),
                ..Projection::default()
            }],
            false => Vec::new(),
        };
        Ok(Synced {
            applied: true,
            projections,
        })
    }
}

/// The daemons a caller reaches, in turn: the first, then (reached again)
/// the next.
struct Reached {
    next: Mutex<usize>,
    seen: Arc<Mutex<Seen>>,
    dying: Arc<Mutex<Vec<usize>>>,
    projecting: bool,
}

impl Daemons for Reached {
    type Api = Fake;

    async fn serving(&self) -> Result<Option<Fake>, Unreached> {
        unreachable!()
    }

    async fn reach(&self) -> Result<Fake, Unreached> {
        let mut next = self.next.lock().unwrap();
        *next += 1;
        Ok(Fake {
            n: *next,
            seen: Arc::clone(&self.seen),
            dying: Arc::clone(&self.dying),
            projecting: self.projecting,
        })
    }
}

struct World {
    seen: Arc<Mutex<Seen>>,
    dying: Arc<Mutex<Vec<usize>>>,
}

impl World {
    fn new() -> World {
        World {
            seen: Arc::default(),
            dying: Arc::default(),
        }
    }

    fn caller(&self, projecting: bool) -> Caller<Reached> {
        let ids = Arc::new(Mutex::new(0));
        Caller::new(
            Reached {
                next: Mutex::new(0),
                seen: Arc::clone(&self.seen),
                dying: Arc::clone(&self.dying),
                projecting,
            },
            ME,
            v1::EnvVars::default(),
            Box::new(move || {
                let mut n = ids.lock().unwrap();
                *n += 1;
                format!("id-{n}")
            }),
        )
        .unwrap()
    }

    fn asked(&self) -> Vec<String> {
        self.seen.lock().unwrap().asked.clone()
    }

    /// The commands written, in order, packed.
    fn commands(&self) -> Vec<prost_types::Any> {
        self.seen
            .lock()
            .unwrap()
            .books
            .iter()
            .map(|book| {
                let Some(angzarr_embedded::v1::command_page::Body::Command(command)) =
                    book.page.as_ref().and_then(|page| page.body.clone())
                else {
                    panic!("a command page")
                };
                command.payload.unwrap()
            })
            .collect()
    }

    fn command_ids(&self) -> Vec<angzarr_embedded::v1::Uuid> {
        self.seen
            .lock()
            .unwrap()
            .books
            .iter()
            .map(|book| book.page.as_ref().unwrap().command_id.clone().unwrap())
            .collect()
    }
}

#[test]
fn a_caller_is_a_sessions_resource_name_and_nothing_else() {
    let refused = Caller::new(
        Reached {
            next: Mutex::new(0),
            seen: Arc::default(),
            dying: Arc::default(),
            projecting: false,
        },
        "brisk-copper-moth",
        v1::EnvVars::default(),
        Box::new(String::new),
    );
    let Err(CallError::Refused(rejection)) = refused else {
        panic!("a bare harp is no caller");
    };
    assert_eq!(
        angzarr_diagnostic::Diagnostic::code(&rejection.report()),
        "daemon-api-name-invalid"
    );
}

#[test]
fn a_target_is_a_harp_of_the_callers_project_or_a_full_name() {
    let world = World::new();
    let caller = world.caller(false);
    assert_eq!(caller.session().to_string(), ME);
    assert_eq!(
        caller.target("brisk-copper-moth").unwrap().to_string(),
        CHILD
    );
    assert_eq!(
        caller
            .target("projects/far-otter/sessions/lone-wolf-pup")
            .unwrap()
            .to_string(),
        "projects/far-otter/sessions/lone-wolf-pup"
    );
    assert!(caller.target("projects/x").is_err());
    assert!(caller.target("").is_err());
}

#[test]
fn an_urgency_is_one_of_three_words() {
    assert_eq!(urgency_of("inform"), Some(sessions::Urgency::Inform));
    assert_eq!(urgency_of("wake"), Some(sessions::Urgency::Wake));
    assert_eq!(urgency_of("steer"), Some(sessions::Urgency::Steer));
    assert_eq!(urgency_of("shout"), None);
}

// OD-2: the reads are under the project the caller's name names.
#[tokio::test]
async fn the_reads_ask_in_the_callers_own_project() {
    let world = World::new();
    let caller = world.caller(false);
    caller.list_sessions(false).await.unwrap();
    caller.list_sessions(true).await.unwrap();
    let shown = caller.get_session("brisk-copper-moth").await.unwrap();
    assert_eq!(shown.name, CHILD);
    let transcript = caller.get_transcript("brisk-copper-moth").await.unwrap();
    assert_eq!(transcript.name, format!("{CHILD}/transcript"));
    let assembled = caller
        .assemble(vec!["demo#fragments/go".to_string()], vec!["t".to_string()])
        .await
        .unwrap();
    assert_eq!(assembled.context, "CTX");
    let expected = [
        "1:list projects/calm-quiet-heron".to_string(),
        "2:list projects/-".to_string(),
        format!("3:session {CHILD}"),
        format!("4:transcript {CHILD}/transcript"),
        "5:assemble projects/calm-quiet-heron demo#fragments/go t".to_string(),
    ];
    assert_eq!(world.asked(), expected);
}

// Slice 3 step 4 and s3-Q4: a send is the domain's SendMessage to the
// child's stream, its requester the caller, its evidence from the child as
// the daemon has it, keyed by the id the caller minted; answered with the
// index's projection.
#[tokio::test]
async fn a_send_is_the_domains_command_answered_with_the_projection() {
    let world = World::new();
    let caller = world.caller(true);
    let written = caller
        .send(
            "brisk-copper-moth",
            sessions::Urgency::Wake,
            "check the tests",
        )
        .await
        .unwrap();
    assert_eq!(written.id, "id-1");
    assert_eq!(written.session.summary, "projected");
    let child = sessions::SessionName::parse(CHILD).unwrap();
    assert_eq!(written.session.uid, child.uid());
    let commands = world.commands();
    let [packed] = commands.as_slice() else {
        panic!("one send: {commands:?}");
    };
    assert!(
        packed
            .type_url
            .ends_with("/ctxloom.sessions.v1.SendMessage"),
        "{packed:?}"
    );
    let sent = wire::SendMessage::decode(packed.value.as_slice()).unwrap();
    assert_eq!(sent.message_id, "id-1");
    assert_eq!(sent.text, "check the tests");
    assert_eq!(sent.urgency, wire::Urgency::Wake as i32);
    let me = sessions::SessionName::parse(ME).unwrap();
    assert_eq!(
        sent.requester,
        Some(wire::SessionRef {
            project_id: "calm-otter".to_string(),
            session: me.uid(),
        })
    );
    assert_eq!(
        sent.supported.len(),
        sessions_application::urgencies_of(true, "claude-code").len()
    );
    assert_eq!(world.command_ids(), [sessions::command_id("id-1")]);
    assert_eq!(
        world.seen.lock().unwrap().projectors,
        [vec!["session-index".to_string()]]
    );
}

// S8: a daemon killed after it applied the write is reached again, and the
// SAME command (its id minted once) is sent to the new one; no projection
// back (a duplicate), the answer is GetSession's.
#[tokio::test]
async fn a_write_whose_daemon_dies_is_sent_once_more_under_the_same_id() {
    let world = World::new();
    world.dying.lock().unwrap().push(1);
    let caller = world.caller(false);
    let written = caller.close("brisk-copper-moth").await.unwrap();
    assert_eq!(written.session.summary, "read", "GetSession's answer");
    let child = sessions::SessionName::parse(CHILD).unwrap();
    assert_eq!(written.id, child.uid());
    let key = format!("close:{}", child.uid());
    assert_eq!(
        world.command_ids(),
        [sessions::command_id(&key), sessions::command_id(&key)]
    );
    let expected = [
        "1:project projects/calm-quiet-heron".to_string(),
        "1:write".to_string(),
        "2:project projects/calm-quiet-heron".to_string(),
        "2:write".to_string(),
        format!("2:session {CHILD}"),
    ];
    assert_eq!(world.asked(), expected);
    for packed in world.commands() {
        assert!(packed
            .type_url
            .ends_with("/ctxloom.sessions.v1.CloseSession"));
        let close = wire::CloseSession::decode(packed.value.as_slice()).unwrap();
        assert!(close.requester.is_some(), "{close:?}");
    }
}

#[tokio::test]
async fn a_clear_is_keyed_by_the_id_minted_for_it() {
    let world = World::new();
    let caller = world.caller(true);
    let written = caller.clear("brisk-copper-moth").await.unwrap();
    assert_eq!(written.id, "id-1");
    let commands = world.commands();
    let [packed] = commands.as_slice() else {
        panic!("one clear");
    };
    let clear = wire::ClearChild::decode(packed.value.as_slice()).unwrap();
    assert_eq!(clear.clear_id, "id-1");
    assert_eq!(world.command_ids(), [sessions::command_id("id-1")]);
}
