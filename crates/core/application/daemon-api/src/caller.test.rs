//! A session as the daemon's client (slice 3): its project from its name
//! (OD-2), its reads, and its writes through the coordinator, keyed by one
//! id per call, so the retry on a new daemon is the same command (S8).

use std::sync::{Arc, Mutex};

use daemon_ports::{Called, CommandBook, DaemonApi, Daemons, Projection, Synced, Unreached};
use daemon_proto::v1;
use daemon_proto::Carries as _;
use prost::Message;
use sessions_proto::v1 as wire;

use super::{reason_of, urgency_of, Caller};
use crate::{ApiRejection, CallError, Rejection};

const ME: &str = "projects/calm-quiet-heron/sessions/amber-quiet-heron";
const CHILD: &str = "projects/calm-quiet-heron/sessions/brisk-copper-moth";

/// What one test's daemons saw.
/// What the tests' caller's requests carry of it: its client, whose version
/// the daemon admits.
fn context() -> v1::Caller {
    v1::Caller {
        env: Some(v1::EnvVars::default()),
        client: Some(v1::Client {
            kind: v1::client::Kind::Mcp as i32,
            program: "ctxloom".to_string(),
            program_dir: "/opt/ctxloom".to_string(),
            version: "v1.2.3-abc".to_string(),
        }),
        ..v1::Caller::default()
    }
}

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
    /// Whether every write is refused.
    refusing: Arc<Mutex<bool>>,
    closed: Arc<Mutex<bool>>,
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

    // Only a launch into another project resolves one (step 8 Q1): the
    // directory's, `bold-api-pine`.
    async fn resolve_project(&self, r: v1::ResolveProjectRequest) -> Result<v1::Project, Called> {
        self.ask(&format!("resolve {}", r.caller_cwd()));
        Ok(v1::Project {
            name: "projects/bold-api-pine".to_string(),
            uid: r.project_id,
            root: r.root,
            ..v1::Project::default()
        })
    }

    async fn get_trust(&self, r: v1::GetTrustRequest) -> Result<v1::Trust, Called> {
        self.ask(&format!("trust {}", r.name));
        Ok(v1::Trust {
            name: r.name,
            trusted: true,
            ..v1::Trust::default()
        })
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
        // A `lone-` harp is a primary (no owner); a `lone-awake-` one is
        // watched by its ctxloom MCP server (the terminal wake).
        let primary = r.name.contains("/sessions/lone-");
        let wakeable = r.name.contains("/sessions/lone-awake-");
        // A cascade's family (step 10c U11, D16): `tall-oak-crow` holds an
        // open child and a `shut-` (closed) one; its open child holds one
        // more, and names its owner back.
        let at = |harp: &str| format!("projects/calm-quiet-heron/sessions/{harp}");
        let children = match r.name.rsplit('/').next().unwrap_or_default() {
            "tall-oak-crow" => vec![at("olive-quiet-finch"), at("shut-tawny-wren")],
            "olive-quiet-finch" => vec![at("deep-grey-mole"), at("tall-oak-crow")],
            _ => Vec::new(),
        };
        let shut = r.name.contains("/sessions/shut-");
        Ok(v1::Session {
            children,
            owner: if primary {
                String::new()
            } else {
                ME.to_string()
            },
            wakeable,
            name: r.name,
            engine: "claude-code".to_string(),
            summary: "read".to_string(),
            closed: shut || *self.closed.lock().unwrap(),
            incarnation: 2,
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

    async fn delete_session(
        &self,
        r: v1::DeleteSessionRequest,
    ) -> Result<v1::DeleteSessionResponse, Called> {
        assert_eq!(
            r.caller_client(),
            context().client.as_ref(),
            "the caller's working context is sent"
        );
        self.ask(&format!(
            "delete {} {} {} {} {}",
            r.name,
            r.request_id,
            r.validate_only,
            r.etag,
            r.answers.len()
        ));
        Ok(v1::DeleteSessionResponse::default())
    }

    async fn create_fragment(
        &self,
        r: v1::CreateFragmentRequest,
    ) -> Result<v1::CreateFragmentResponse, Called> {
        assert_eq!(
            r.caller_client(),
            context().client.as_ref(),
            "the caller's working context is sent"
        );
        self.ask(&format!(
            "fragment {} {} {} {}",
            r.parent, r.fragment_id, r.request_id, r.validate_only
        ));
        Ok(v1::CreateFragmentResponse::default())
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
        assert_eq!(
            r.caller_client(),
            context().client.as_ref(),
            "the caller's working context is sent"
        );
        Ok(v1::AssembledContext {
            context: "CTX".to_string(),
            ..v1::AssembledContext::default()
        })
    }

    async fn create_hand_off(&self, r: v1::CreateHandOffRequest) -> Result<v1::HandOff, Called> {
        self.ask(&format!(
            "hand-off {} {} {} {} {}{}",
            r.parent,
            r.hand_off_id,
            r.owner,
            r.engine,
            r.agent,
            match r.project.is_empty() {
                true => String::new(),
                false => format!(" into {}", r.project),
            }
        ));
        assert_eq!(
            r.caller_client(),
            context().client.as_ref(),
            "the caller's working context is sent"
        );
        // The engine asked, else the agent's: here, opencode.
        let engine = match r.engine.is_empty() {
            true => "opencode".to_string(),
            false => r.engine.clone(),
        };
        Ok(v1::HandOff {
            name: format!("{}/handOffs/{}", r.parent, r.hand_off_id),
            engine,
            project: r.project,
        })
    }

    async fn delete_hand_off(&self, r: v1::DeleteHandOffRequest) -> Result<v1::HandOff, Called> {
        self.ask(&format!("withdraw {}", r.name));
        Ok(v1::HandOff {
            name: r.name,
            ..v1::HandOff::default()
        })
    }

    async fn wait_approval(
        &self,
        r: v1::WaitApprovalRequest,
        until: daemon_ports::Until,
    ) -> Result<v1::Approval, Called> {
        self.ask(&format!("wait {} until {}", r.name, until.seconds));
        if self.dying.lock().unwrap().contains(&self.n) {
            return Err(Called::Unreachable {
                detail: "killed while it waited".to_string(),
            });
        }
        Ok(v1::Approval {
            name: r.name,
            state: "expired".to_string(),
            ..v1::Approval::default()
        })
    }

    // A write no projection is awaited for (a run's RequestStop): applied.
    async fn handle(&self, book: CommandBook) -> Result<bool, Called> {
        self.ask("handle");
        self.seen.lock().unwrap().books.push(book);
        Ok(true)
    }

    async fn watch_session(
        &self,
        r: v1::WatchSessionRequest,
    ) -> Result<Box<dyn daemon_ports::SessionSignals>, Called> {
        self.ask(&format!("watch {}", r.name));
        Ok(Box::new(OneSignal(Some(v1::SessionSignal {
            wakes: vec!["m1".to_string()],
            outstanding_wake: String::new(),
        }))))
    }

    async fn handle_sync(
        &self,
        book: CommandBook,
        projectors: Vec<String>,
    ) -> Result<Synced, Called> {
        self.ask("write");
        if *self.refusing.lock().unwrap() {
            return Err(Called::Refused {
                code: 9,
                refusal: prost_types::Any::from_msg(&wire::DelegationLimit { limit: 4, open: 4 })
                    .unwrap(),
            });
        }
        // The coordinator's refusal of a command names its type alone, the
        // object dropped: a send whose text is "not mine" is refused so.
        if let Some(angzarr_embedded::v1::command_page::Body::Command(command)) =
            book.page.as_ref().and_then(|page| page.body.clone())
        {
            let packed = command.payload.unwrap_or_default();
            if packed
                .type_url
                .ends_with("/ctxloom.sessions.v1.SendMessage")
                && wire::SendMessage::decode(packed.value.as_slice())
                    .unwrap()
                    .text
                    == "not mine"
            {
                return Err(Called::Refused {
                    code: 9,
                    refusal: prost_types::Any {
                        type_url: "type.googleapis.com/ctxloom.sessions.v1.NotOwner".to_string(),
                        value: Vec::new(),
                    },
                });
            }
        }
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
    refusing: Arc<Mutex<bool>>,
    closed: Arc<Mutex<bool>>,
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
            refusing: Arc::clone(&self.refusing),
            closed: Arc::clone(&self.closed),
        })
    }
}

struct World {
    seen: Arc<Mutex<Seen>>,
    dying: Arc<Mutex<Vec<usize>>>,
    refusing: Arc<Mutex<bool>>,
    /// Whether every session the daemon has is closed.
    closed: Arc<Mutex<bool>>,
}

impl World {
    fn new() -> World {
        World {
            seen: Arc::default(),
            dying: Arc::default(),
            refusing: Arc::default(),
            closed: Arc::default(),
        }
    }

    fn reached(&self, projecting: bool) -> Reached {
        Reached {
            next: Mutex::new(0),
            seen: Arc::clone(&self.seen),
            dying: Arc::clone(&self.dying),
            projecting,
            refusing: Arc::clone(&self.refusing),
            closed: Arc::clone(&self.closed),
        }
    }

    /// The USER in the caller's project.
    fn user(&self, projecting: bool) -> Caller<Reached> {
        let ids = Arc::new(Mutex::new(0));
        Caller::user(
            self.reached(projecting),
            "projects/calm-quiet-heron",
            context(),
            Box::new(move || {
                let mut n = ids.lock().unwrap();
                *n += 1;
                format!("id-{n}")
            }),
        )
        .unwrap()
    }

    fn caller(&self, projecting: bool) -> Caller<Reached> {
        let ids = Arc::new(Mutex::new(0));
        Caller::new(
            Reached {
                next: Mutex::new(0),
                seen: Arc::clone(&self.seen),
                dying: Arc::clone(&self.dying),
                projecting,
                refusing: Arc::clone(&self.refusing),
                closed: Arc::clone(&self.closed),
            },
            ME,
            context(),
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
            refusing: Arc::default(),
            closed: Arc::default(),
        },
        "brisk-copper-moth",
        context(),
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
    assert_eq!(caller.session().unwrap().to_string(), ME);
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
        sessions_application::urgencies_of(true, "claude-code", false).len()
    );
    assert_eq!(world.command_ids(), [sessions::command_id("id-1")]);
    assert_eq!(
        world.seen.lock().unwrap().projectors,
        [vec!["session-index".to_string()]]
    );
}

/// The urgencies the one send in `world` gave as its evidence.
fn evidence(world: &World) -> Vec<i32> {
    let commands = world.commands();
    let [packed] = commands.as_slice() else {
        panic!("one send: {commands:?}");
    };
    wire::SendMessage::decode(packed.value.as_slice())
        .unwrap()
        .supported
}

// The terminal wake: a send's evidence is the target as the daemon has it,
// so a primary whose ctxloom MCP server watches it takes a wake, and one
// nobody watches an inform alone.
#[tokio::test]
async fn a_sends_evidence_offers_a_wake_to_a_primary_only_while_it_is_watched() {
    let awake = World::new();
    awake
        .caller(true)
        .send("lone-awake-heron", sessions::Urgency::Inform, "hi")
        .await
        .unwrap();
    assert_eq!(
        evidence(&awake),
        [wire::Urgency::Inform as i32, wire::Urgency::Wake as i32]
    );
    let asleep = World::new();
    asleep
        .caller(true)
        .send("lone-asleep-heron", sessions::Urgency::Inform, "hi")
        .await
        .unwrap();
    assert_eq!(evidence(&asleep), [wire::Urgency::Inform as i32]);
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
        format!("1:session {CHILD}"),
        "1:project projects/calm-quiet-heron".to_string(),
        "1:write".to_string(),
        format!("2:session {CHILD}"),
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

// S19's `session stop` (OD-57): the target's CURRENT run is asked to stop,
// keyed by that run, so a second stop of it is the first's duplicate; the
// session itself is not written (it stays open).
#[tokio::test]
async fn a_stop_asks_the_targets_current_run_to_stop_keyed_by_that_run() {
    let world = World::new();
    let caller = world.user(true);
    let written = caller.stop("brisk-copper-moth").await.unwrap();
    assert!(written.applied);
    let child = sessions::SessionName::parse(CHILD).unwrap();
    let commands = world.commands();
    let [packed] = commands.as_slice() else {
        panic!("one stop: {commands:?}");
    };
    assert!(
        packed.type_url.ends_with("/ctxloom.runs.v1.RequestStop"),
        "{packed:?}"
    );
    let cover = world.seen.lock().unwrap().books[0].cover.clone().unwrap();
    assert_eq!(cover.domain, runs::RUN_DOMAIN);
    assert_eq!(cover.root, Some(runs::run_root(&child.uid(), 2)));
    assert_eq!(
        world.command_ids(),
        [sessions::command_id(&format!("stop:{}:2", child.uid()))]
    );
}

// Step 10e Stage 2a, `session restart`: the operator asks the session, which
// decides (the domain's `RequestRestart`, reason operator, the note), keyed by
// the incarnation it asks of, so a replay is the first's duplicate and a
// restart of the next incarnation is a new one.
#[tokio::test]
async fn a_restart_asks_the_session_keyed_by_the_incarnation_it_asks_of() {
    let world = World::new();
    let caller = world.user(true);
    let written = caller
        .restart("brisk-copper-moth", "context is stale")
        .await
        .unwrap();
    assert!(written.applied);
    let child = sessions::SessionName::parse(CHILD).unwrap();
    assert_eq!(written.id, child.uid());
    let commands = world.commands();
    let [packed] = commands.as_slice() else {
        panic!("one restart: {commands:?}");
    };
    assert!(
        packed
            .type_url
            .ends_with("/ctxloom.sessions.v1.RequestRestart"),
        "{packed:?}"
    );
    let asked = wire::RequestRestart::decode(packed.value.as_slice()).unwrap();
    assert_eq!(asked.reason, wire::RestartReason::Operator as i32);
    assert_eq!(asked.note, "context is stale");
    let cover = world.seen.lock().unwrap().books[0].cover.clone().unwrap();
    assert_eq!(cover.domain, sessions::SESSION_DOMAIN);
    assert_eq!(
        world.command_ids(),
        [sessions::command_id(&format!(
            "restart:{}:2:operator",
            child.uid()
        ))]
    );
}

// A terminal session's engine is held by its own `ctxloom run`, not the
// daemon: refused before anything is written.
#[tokio::test]
async fn a_restart_of_a_primary_is_refused_and_writes_nothing() {
    let world = World::new();
    let caller = world.user(true);
    let refused = caller.restart("lone-awake-heron", "").await.unwrap_err();
    assert!(
        matches!(
            &refused,
            CallError::Refused(Rejection::Api(ApiRejection::PrimaryNotHosted { harp }))
                if harp == "lone-awake-heron"
        ),
        "{refused:?}"
    );
    assert!(world.commands().is_empty());
}

// A refused name says why: the harp grammar's reason, or its shape.
#[test]
fn a_refused_names_reason_is_the_one_it_was_refused_for() {
    let malformed = sessions::SessionRejection::NameMalformed {
        name: "projects/a:b".to_string(),
        reason: "separator".to_string(),
    };
    assert_eq!(reason_of(&malformed), "separator");
    let invalid = sessions::SessionRejection::HarpInvalid {
        harp: String::new(),
        reason: "empty".to_string(),
    };
    assert_eq!(reason_of(&invalid), "empty");
    assert_eq!(reason_of(&sessions::SessionRejection::NoSession), "shape");
}

// Slice 4 U3 (S2, D25): the user's writes name no requester, a close is
// recorded as hers, and a message is keyed by the id she gave, so the same
// id again is one message.
#[tokio::test]
async fn the_users_writes_name_no_requester_and_keep_the_id_she_gave() {
    let world = World::new();
    let caller = world.user(true);
    assert!(caller.session().is_none());
    let written = caller
        .send_as(
            "brisk-copper-moth",
            sessions::Urgency::Wake,
            "check the tests",
            Some("m1"),
        )
        .await
        .unwrap();
    assert_eq!(written.id, "m1");
    assert!(written.applied);
    let closed = caller.close("brisk-copper-moth").await.unwrap();
    assert!(closed.applied);
    let commands = world.commands();
    let [sent, close] = commands.as_slice() else {
        panic!("a send and a close: {commands:?}");
    };
    let sent = wire::SendMessage::decode(sent.value.as_slice()).unwrap();
    assert_eq!(sent.requester, None);
    let close = wire::CloseSession::decode(close.value.as_slice()).unwrap();
    assert_eq!(close.requester, None);
    assert_eq!(close.cause, "closed by the user");
    assert_eq!(world.command_ids()[0], sessions::command_id("m1"));
    assert!(
        !world.asked().iter().any(|asked| asked.contains("project ")),
        "the user names no requester, so no project is asked: {:?}",
        world.asked()
    );
}

// Slice 4 U3 (T6, S10): a launch hands the daemon its environment first,
// then sends the owner's LaunchChild, keyed by the owner and the child; the
// answer is the child as the daemon has it.
#[tokio::test]
async fn a_launch_is_the_hand_off_then_the_owners_launch_child() {
    let world = World::new();
    let caller = world.user(false);
    let written = caller
        .launch(&super::Launch {
            owner: Some("amber-quiet-heron".to_string()),
            harp: Some("brisk-copper-moth".to_string()),
            task: "review the change".to_string(),
            agent: "dev".to_string(),
            ..super::Launch::default()
        })
        .await
        .unwrap();
    let child = sessions::SessionName::parse(CHILD).unwrap();
    assert_eq!(written.id, child.uid());
    assert_eq!(written.session.name, CHILD);
    let asked = world.asked();
    assert_eq!(
        asked[0],
        format!(
            "1:hand-off projects/calm-quiet-heron {} amber-quiet-heron  dev",
            child.uid()
        )
    );
    assert!(asked.contains(&"1:write".to_string()), "{asked:?}");
    let commands = world.commands();
    let [launched] = commands.as_slice() else {
        panic!("one launch: {commands:?}");
    };
    assert!(launched
        .type_url
        .ends_with("/ctxloom.sessions.v1.LaunchChild"));
    let launched = wire::LaunchChild::decode(launched.value.as_slice()).unwrap();
    assert_eq!(launched.child_session, child.uid());
    assert_eq!(launched.harp, "brisk-copper-moth");
    assert_eq!(launched.prompt, "review the change");
    assert_eq!(launched.requester, None);
    assert_eq!(launched.delegation_limit, super::DELEGATION_LIMIT);
    // Its owner's project: the same project, its trust unread (step 8).
    let trust = launched.trust.expect("a trust");
    assert!(trust.same_project);
    assert!(!trust.trusted);
    assert_eq!(launched.child_project_id, "calm-otter");
    // Ruling 4: none asked, the engine the daemon planned it on (its
    // agent's).
    assert_eq!(launched.engine, "opencode");
    let owner =
        sessions::SessionName::parse("projects/calm-quiet-heron/sessions/amber-quiet-heron")
            .unwrap();
    assert_eq!(
        world.command_ids(),
        [sessions::command_id(&format!(
            "launch:{}:{}",
            owner.uid(),
            child.uid()
        ))]
    );
}

// Step 8 Q1, D8: a launch into another project registers it (the
// caller's resolution of the directory), hands off into it, and names the
// child there: its project's id and root, and the daemon's word on the
// target's trust.
#[tokio::test]
async fn a_launch_into_another_project_names_the_child_there_with_its_trust() {
    let world = World::new();
    let resolved = Arc::new(Mutex::new(Vec::new()));
    let seen = Arc::clone(&resolved);
    let caller = world.user(false).resolving(Arc::new(move |dir: &str| {
        seen.lock().unwrap().push(dir.to_string());
        Ok(v1::ResolveProjectRequest {
            caller: Some(v1::Caller {
                cwd: "/work/api".to_string(),
                ..v1::Caller::default()
            }),
            project_id: "api-otter".to_string(),
            root: "/work/api".to_string(),
            ..v1::ResolveProjectRequest::default()
        })
    }));
    let written = caller
        .launch(&super::Launch {
            owner: Some("amber-quiet-heron".to_string()),
            harp: Some("brisk-copper-moth".to_string()),
            project: Some("../api".to_string()),
            ..super::Launch::default()
        })
        .await
        .unwrap();
    assert_eq!(*resolved.lock().unwrap(), ["../api"]);
    let child =
        sessions::SessionName::parse("projects/bold-api-pine/sessions/brisk-copper-moth").unwrap();
    assert_eq!(written.id, child.uid());
    let asked = world.asked();
    assert_eq!(asked[0], "1:resolve /work/api");
    assert!(
        asked.contains(&format!(
            "2:hand-off projects/calm-quiet-heron {} amber-quiet-heron   into projects/bold-api-pine",
            child.uid()
        )),
        "{asked:?}"
    );
    assert!(
        asked.contains(&"2:trust projects/bold-api-pine/trust".to_string()),
        "{asked:?}"
    );
    let commands = world.commands();
    let [launched] = commands.as_slice() else {
        panic!("one launch: {commands:?}");
    };
    let launched = wire::LaunchChild::decode(launched.value.as_slice()).unwrap();
    assert_eq!(launched.child_project_id, "api-otter");
    assert_eq!(launched.child_root, "/work/api");
    assert_eq!(launched.child_session, child.uid());
    let trust = launched.trust.expect("a trust");
    assert!(!trust.same_project);
    assert!(trust.trusted);
}

// A caller that resolves no directory refuses a launch into another
// project, as a directory in no project is refused, and asks nothing.
#[tokio::test]
async fn a_launch_into_another_project_with_no_resolution_is_refused() {
    let world = World::new();
    let refused = world
        .user(false)
        .launch(&super::Launch {
            owner: Some("amber-quiet-heron".to_string()),
            harp: Some("brisk-copper-moth".to_string()),
            project: Some("/nowhere".to_string()),
            ..super::Launch::default()
        })
        .await;
    let Err(CallError::Refused(crate::Rejection::Api(crate::ApiRejection::ProjectUnknown { cwd }))) =
        refused
    else {
        panic!("refused: {refused:?}");
    };
    assert_eq!(cwd, "/nowhere");
    assert!(world.asked().is_empty(), "{:?}", world.asked());
}

// Step 8 Q7: a chosen name of the ask form is refused before anything is
// asked of the daemon, in the sessions domain's words.
#[tokio::test]
async fn a_launch_naming_its_child_as_an_ask_is_refused_and_asks_nothing() {
    let world = World::new();
    let refused = world
        .user(false)
        .launch(&super::Launch {
            owner: Some("amber-quiet-heron".to_string()),
            harp: Some("ask-tidy-wren".to_string()),
            ..super::Launch::default()
        })
        .await;
    let Err(CallError::Refused(crate::Rejection::Sessions(
        sessions::SessionRejection::HarpInvalid { harp, reason },
    ))) = refused
    else {
        panic!("refused: {refused:?}");
    };
    assert_eq!(harp, "ask-tidy-wren");
    assert_eq!(reason, sessions::RESERVED_REASON);
    assert!(world.asked().is_empty(), "{:?}", world.asked());
}

// A refused LaunchChild takes its hand-off back: a refused launch leaves
// nothing.
#[tokio::test]
async fn a_refused_launch_takes_its_hand_off_back() {
    let world = World::new();
    *world.refusing.lock().unwrap() = true;
    let caller = world.user(false);
    let refused = caller
        .launch(&super::Launch {
            owner: Some("amber-quiet-heron".to_string()),
            harp: Some("brisk-copper-moth".to_string()),
            ..super::Launch::default()
        })
        .await;
    let Err(CallError::Refused(crate::Rejection::Sessions(rejection))) = refused else {
        panic!("refused in the sessions domain's words: {refused:?}");
    };
    assert_eq!(
        rejection,
        sessions::SessionRejection::DelegationLimit { limit: 4, open: 4 }
    );
    let child = sessions::SessionName::parse(CHILD).unwrap();
    assert!(
        world.asked().contains(&format!(
            "1:withdraw projects/calm-quiet-heron/handOffs/{}",
            child.uid()
        )),
        "{:?}",
        world.asked()
    );
}

// A session's own launch is its child's (the owner itself, its requester),
// its harp minted where none is named.
#[tokio::test]
async fn a_sessions_launch_is_of_its_own_child_named_when_unnamed() {
    let world = World::new();
    let caller = world
        .caller(false)
        .naming(Box::new(|| "brisk-copper-moth".to_string()));
    let written = caller.launch(&super::Launch::default()).await.unwrap();
    let child = sessions::SessionName::parse(CHILD).unwrap();
    assert_eq!(written.id, child.uid());
    let commands = world.commands();
    let launched = wire::LaunchChild::decode(commands[0].value.as_slice()).unwrap();
    let me = sessions::SessionName::parse(ME).unwrap();
    assert_eq!(
        launched.requester,
        Some(wire::SessionRef {
            project_id: "calm-otter".to_string(),
            session: me.uid(),
        })
    );
}

// A message, a clear and a close to a closed session: the first two are
// refused in the domain's words, naming it, and nothing is written; a
// close of one says it was closed already.
#[tokio::test]
async fn a_closed_session_takes_no_message_or_clear_and_its_close_says_so() {
    let world = World::new();
    *world.closed.lock().unwrap() = true;
    let caller = world.user(false);
    let refused = caller
        .send_as("brisk-copper-moth", sessions::Urgency::Wake, "x", None)
        .await;
    assert_eq!(
        refused,
        Err(CallError::Refused(crate::Rejection::Sessions(
            sessions::SessionRejection::SessionClosed {
                harp: "brisk-copper-moth".to_string()
            }
        )))
    );
    assert!(caller.clear("brisk-copper-moth").await.is_err());
    assert!(world.commands().is_empty(), "nothing written");
    let closed = caller.close("brisk-copper-moth").await.unwrap();
    assert!(!closed.applied, "closed already");
}

/// Launch locks in memory: each taken and let go recorded in `seen`'s
/// asks; one held refuses a second taker.
#[derive(Clone, Default)]
struct Locks {
    held: Arc<Mutex<std::collections::BTreeSet<String>>>,
    seen: Arc<Mutex<Seen>>,
}

/// A held launch lock: let go, and said, when dropped.
struct Holding {
    name: String,
    locks: Locks,
}

impl Drop for Holding {
    fn drop(&mut self) {
        self.locks.held.lock().unwrap().remove(&self.name);
        self.locks
            .seen
            .lock()
            .unwrap()
            .asked
            .push(format!("let go {}", self.name));
    }
}

impl run_ports::RunLocks for Locks {
    fn hold_run(&self, run: &str) -> Result<run_ports::HeldLock, run_ports::LockError> {
        if !self.held.lock().unwrap().insert(run.to_string()) {
            return Err(run_ports::LockError::Held(angzarr_diagnostic::Report::of(
                &crate::ApiRejection::LaunchInProgress {
                    harp: run.to_string(),
                },
            )));
        }
        self.seen.lock().unwrap().asked.push(format!("hold {run}"));
        Ok(run_ports::HeldLock::new(
            run.to_string(),
            Holding {
                name: run.to_string(),
                locks: self.clone(),
            },
        ))
    }

    fn probe(&self, _: &str) -> run_ports::LockProbe {
        unreachable!("a launch only holds")
    }

    fn session(&self, _: &str, _: &str) -> run_ports::LockProbe {
        unreachable!("a launch only holds")
    }
}

fn a_launch() -> super::Launch {
    super::Launch {
        owner: Some("amber-quiet-heron".to_string()),
        harp: Some("brisk-copper-moth".to_string()),
        ..super::Launch::default()
    }
}

// T6 (owner ruling on U3): a launch holds its launch lock from before its
// `CreateHandOff` to after its `LaunchChild`, and lets it go then.
#[tokio::test]
async fn a_launch_holds_its_lock_across_its_hand_off_and_its_launch_child() {
    let world = World::new();
    let locks = Locks {
        seen: Arc::clone(&world.seen),
        ..Locks::default()
    };
    let caller = world.user(false).holding(Arc::new(locks.clone()));
    caller.launch(&a_launch()).await.unwrap();
    let child = sessions::SessionName::parse(CHILD).unwrap();
    let name = super::launch_lock_name(&child.uid());
    let asked = world.asked();
    let at = |what: &str| {
        asked
            .iter()
            .position(|a| a.starts_with(what))
            .unwrap_or_else(|| panic!("{what}: {asked:?}"))
    };
    assert!(at(&format!("hold {name}")) < at("1:hand-off"), "{asked:?}");
    assert!(at("1:write") < at(&format!("let go {name}")), "{asked:?}");
    assert!(locks.held.lock().unwrap().is_empty(), "let go");
}

// T6, the race of two launches of one child: while one is made, a second
// is refused at once, before anything is asked of the daemon.
#[tokio::test]
async fn a_second_launch_of_a_child_while_the_first_is_made_is_refused() {
    let world = World::new();
    let locks = Locks {
        seen: Arc::clone(&world.seen),
        ..Locks::default()
    };
    let child = sessions::SessionName::parse(CHILD).unwrap();
    let first =
        run_ports::RunLocks::hold_run(&locks, &super::launch_lock_name(&child.uid())).unwrap();
    let caller = world.user(false).holding(Arc::new(locks.clone()));
    let refused = caller.launch(&a_launch()).await;
    assert_eq!(
        refused,
        Err(CallError::Refused(crate::Rejection::Api(
            crate::ApiRejection::LaunchInProgress {
                harp: "brisk-copper-moth".to_string()
            }
        )))
    );
    assert!(
        !world.asked().iter().any(|a| a.contains("hand-off")),
        "nothing asked: {:?}",
        world.asked()
    );
    drop(first);
    caller.launch(&a_launch()).await.unwrap();
}

// U5: a wait retried on a new daemon (the first died while it waited) ends
// at the same instant as the first would have, never a second span later.
#[tokio::test]
async fn a_wait_retried_on_a_new_daemon_ends_when_the_first_would_have() {
    let w = World::new();
    w.dying.lock().unwrap().push(1);
    let caller = w.caller(false);
    let name = "projects/calm-quiet-heron/approvals/ask-tidy-wren";
    let until = daemon_ports::Until {
        seconds: 1_360,
        nanos: 0,
    };
    let waited = caller.wait_approval(name, until).await.unwrap();
    assert_eq!(waited.state, "expired");
    assert_eq!(
        w.asked(),
        [
            format!("1:wait {name} until 1360"),
            format!("2:wait {name} until 1360"),
        ]
    );
}

// Step 8 Q1: a `project` that resolves to the owner's own project is the
// same project: its trust is unread, as with no project named.
#[tokio::test]
async fn a_launch_into_its_owners_own_project_is_the_same_project() {
    let world = World::new();
    let caller = world.user(false).resolving(Arc::new(|_: &str| {
        Ok(v1::ResolveProjectRequest {
            caller: Some(v1::Caller {
                cwd: "/work/app".to_string(),
                ..v1::Caller::default()
            }),
            project_id: "calm-otter".to_string(),
            root: "/work/app".to_string(),
            ..v1::ResolveProjectRequest::default()
        })
    }));
    caller
        .launch(&super::Launch {
            owner: Some("amber-quiet-heron".to_string()),
            harp: Some("brisk-copper-moth".to_string()),
            project: Some(".".to_string()),
            ..super::Launch::default()
        })
        .await
        .unwrap();
    let asked = world.asked();
    assert!(
        !asked.iter().any(|a| a.contains(":trust ")),
        "its trust is unread: {asked:?}"
    );
    let commands = world.commands();
    let [launched] = commands.as_slice() else {
        panic!("one launch: {commands:?}");
    };
    let launched = wire::LaunchChild::decode(launched.value.as_slice()).unwrap();
    assert_eq!(launched.child_project_id, "calm-otter");
    let trust = launched.trust.expect("a trust");
    assert!(trust.same_project);
    assert!(!trust.trusted);
}

/// A notice as its code, its args by name and its text.
type Said = (String, Vec<(String, String)>, String);

// What an assembly did not find is said as the MCP tool's notices: each
// missing fragment, then each missing tag, its name the arg.
#[test]
fn an_assemblys_missing_fragments_and_tags_are_its_notices() {
    let assembled = v1::AssembledContext {
        missing_fragments: vec!["gone".to_string()],
        missing_tags: vec!["nope".to_string()],
        ..v1::AssembledContext::default()
    };
    let said: Vec<Said> = super::missing_notices(&assembled)
        .into_iter()
        .map(|n| {
            let args = n.args.into_iter().map(|a| (a.name, a.value)).collect();
            (n.code, args, n.text)
        })
        .collect();
    let arg = |value: &str| vec![("name".to_string(), value.to_string())];
    assert_eq!(
        said,
        [
            (
                "daemon-api-assembly-missing-fragment".to_string(),
                arg("gone"),
                "no fragment is named gone".to_string()
            ),
            (
                "daemon-api-assembly-missing-tag".to_string(),
                arg("nope"),
                "no fragment is tagged nope".to_string()
            ),
        ]
    );
}

/// A watch that gives one signal, then ends.
struct OneSignal(Option<v1::SessionSignal>);

impl daemon_ports::SessionSignals for OneSignal {
    fn next(&mut self) -> daemon_ports::NextSignal<'_> {
        let signal = self.0.take().map(Ok);
        Box::pin(async move { signal })
    }
}

/// The one command `world` sent: its type's name and its body.
fn the_command(world: &World) -> prost_types::Any {
    let commands = world.commands();
    let [packed] = commands.as_slice() else {
        panic!("one command: {commands:?}");
    };
    packed.clone()
}

// The terminal wake: a session's MCP server watches its OWN session.
#[tokio::test]
async fn a_session_watches_its_own_session() {
    let world = World::new();
    let mut signals = world.caller(false).watch_own().await.unwrap();
    assert_eq!(signals.next().await.unwrap().unwrap().wakes, ["m1"]);
    assert!(world
        .seen
        .lock()
        .unwrap()
        .asked
        .iter()
        .any(|asked| asked.ends_with(&format!("watch {ME}"))));
}

// Its wake's issue and disarm are its own stream's commands, keyed by its
// session and the nonce, so a redelivery is one wake.
#[tokio::test]
async fn a_wake_is_issued_and_disarmed_on_the_sessions_own_stream() {
    let me = sessions::SessionName::parse(ME).unwrap();
    let issued = World::new();
    issued
        .caller(false)
        .issue_wake("0123456789abcdef")
        .await
        .unwrap();
    let packed = the_command(&issued);
    assert!(
        packed.type_url.ends_with("/ctxloom.sessions.v1.IssueWake"),
        "{packed:?}"
    );
    assert_eq!(
        wire::IssueWake::decode(packed.value.as_slice())
            .unwrap()
            .nonce,
        "0123456789abcdef"
    );
    assert_eq!(
        issued.command_ids(),
        [sessions::command_id(&format!(
            "wake:{}:0123456789abcdef",
            me.uid()
        ))]
    );
    let disarmed = World::new();
    disarmed
        .caller(false)
        .disarm_wake("0123456789abcdef")
        .await
        .unwrap();
    let packed = the_command(&disarmed);
    assert!(
        packed.type_url.ends_with("/ctxloom.sessions.v1.DisarmWake"),
        "{packed:?}"
    );
    assert_eq!(
        disarmed.command_ids(),
        [sessions::command_id(&format!(
            "disarm:{}:0123456789abcdef",
            me.uid()
        ))]
    );
}

// A hook's delivery: the messages it gave its engine, and the wake its
// prompt was, keyed by both, so a redelivered hook is one delivery.
#[tokio::test]
async fn a_delivery_names_its_messages_and_wake_on_the_sessions_own_stream() {
    let me = sessions::SessionName::parse(ME).unwrap();
    let world = World::new();
    let ids = vec!["m1".to_string(), "m2".to_string()];
    world
        .caller(false)
        .deliver(&ids, "0123456789abcdef")
        .await
        .unwrap();
    let packed = the_command(&world);
    assert!(
        packed
            .type_url
            .ends_with("/ctxloom.sessions.v1.DeliverMessages"),
        "{packed:?}"
    );
    let delivered = wire::DeliverMessages::decode(packed.value.as_slice()).unwrap();
    assert_eq!(delivered.message_ids, ids);
    assert_eq!(delivered.wake_nonce, "0123456789abcdef");
    assert_eq!(
        world.command_ids(),
        [sessions::command_id(&format!(
            "deliver:{}:0123456789abcdef:m1,m2",
            me.uid()
        ))]
    );
}

// The terminal wake: a wake to a primary nobody watches (no ctxloom MCP
// server connected, as with `--no-context`) is refused before it is sent,
// the sender told it takes an inform; an inform goes as ever.
#[tokio::test]
async fn a_wake_to_a_primary_nobody_watches_is_refused_in_the_senders_words() {
    let world = World::new();
    let refused = world
        .caller(true)
        .send("lone-asleep-heron", sessions::Urgency::Wake, "wake up")
        .await
        .unwrap_err();
    let CallError::Refused(crate::refusal::Rejection::Api(
        crate::refusal::ApiRejection::WakeInformOnly { harp },
    )) = &refused
    else {
        panic!("{refused:?}");
    };
    assert_eq!(harp, "lone-asleep-heron");
    assert!(world.commands().is_empty(), "nothing sent");
    let awake = World::new();
    awake
        .caller(true)
        .send("lone-awake-heron", sessions::Urgency::Wake, "wake up")
        .await
        .unwrap();
    assert_eq!(awake.commands().len(), 1);
}

// Step 10c U11, D16 (S40): a close that takes open children with it asks
// to confirm, naming each it takes, nearest first, and none closed already;
// a close that takes none asks nothing.
#[tokio::test]
async fn a_close_that_takes_children_asks_to_confirm_naming_each() {
    let world = World::new();
    let needs = world
        .caller(false)
        .close_needs("tall-oak-crow")
        .await
        .unwrap();
    let asked: Vec<(&str, &str)> = needs
        .iter()
        .filter_map(|need| match &need.kind {
            Some(v1::need::Kind::Confirm(c)) if !need.optional => {
                Some((need.id.as_str(), c.text.as_str()))
            }
            _ => None,
        })
        .collect();
    assert_eq!(
        asked,
        [(
            "confirm",
            "Close \"tall-oak-crow\" and, with it, \"olive-quiet-finch\", \"deep-grey-mole\"?"
        )]
    );
    assert!(world
        .caller(false)
        .close_needs("deep-grey-mole")
        .await
        .unwrap()
        .is_empty());
}

// The test build's tools' calls (step 10c U11): a removal and a fragment's
// creation, each of this caller's project, under an id minted for it.
#[tokio::test]
async fn a_removal_and_a_fragment_are_asked_in_the_callers_project() {
    let world = World::new();
    let caller = world.caller(false);
    let apply = v1::DeleteSessionRequest {
        etag: "e-1".to_string(),
        answers: vec![v1::Answer::default()],
        ..v1::DeleteSessionRequest::default()
    };
    caller
        .delete_session("ended-amber-owl", apply)
        .await
        .unwrap();
    let asked = v1::CreateFragmentRequest {
        fragment_id: "brief".to_string(),
        validate_only: true,
        ..v1::CreateFragmentRequest::default()
    };
    caller.create_fragment("notes", asked).await.unwrap();
    assert_eq!(
        world.asked(),
        [
            "1:delete projects/calm-quiet-heron/sessions/ended-amber-owl id-1 false e-1 1",
            "2:fragment projects/calm-quiet-heron/bundles/notes brief id-2 true"
        ]
    );
}

// The coordinator names a refused command's refusal by its type alone (the
// status carries no object), so a NotOwner reaches the client with no
// requester and reads "session  is not this session's owner". The requester
// is the caller itself, which knows its own id: the refusal says it.
#[tokio::test]
async fn a_not_owner_refusal_the_coordinator_named_by_type_alone_names_its_requester() {
    let world = World::new();
    let caller = world.caller(true);
    let refused = caller
        .send("brisk-copper-moth", sessions::Urgency::Wake, "not mine")
        .await;
    let me = sessions::SessionName::parse(ME).unwrap();
    let Err(CallError::Refused(crate::Rejection::Sessions(rejection))) = refused else {
        panic!("refused in the sessions domain's words: {refused:?}");
    };
    assert_eq!(
        rejection,
        sessions::SessionRejection::NotOwner {
            requester: me.uid()
        }
    );
}

// Only an empty NotOwner is told its requester: one that carries it, any
// other refusal, any other call, and the user (who names none) are as they
// came.
#[test]
fn only_an_empty_not_owner_is_told_its_requester() {
    let world = World::new();
    let caller = world.caller(true);
    let refused = |any| Called::Refused {
        code: 9,
        refusal: any,
    };
    let filled = refused(
        prost_types::Any::from_msg(&wire::NotOwner {
            requester: "theirs".to_string(),
        })
        .unwrap(),
    );
    assert_eq!(caller.naming_requester(filled.clone()), filled);
    let other = refused(prost_types::Any {
        type_url: "type.googleapis.com/ctxloom.sessions.v1.SessionIsClosed".to_string(),
        value: Vec::new(),
    });
    assert_eq!(caller.naming_requester(other.clone()), other);
    let down = Called::Unreachable {
        detail: "gone".to_string(),
    };
    assert_eq!(caller.naming_requester(down.clone()), down);
    let bare = refused(prost_types::Any {
        type_url: "type.googleapis.com/ctxloom.sessions.v1.NotOwner".to_string(),
        value: Vec::new(),
    });
    assert_eq!(world.user(false).naming_requester(bare.clone()), bare);
}
