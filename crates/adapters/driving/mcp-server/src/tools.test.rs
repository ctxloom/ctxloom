//! The tools as an engine lists them, and their arguments as read.

use serde_json::json;

use super::{json_text, text, texts, tools, ToolArgs, TOOL_NAMES};

fn args(value: serde_json::Value) -> rmcp::model::JsonObject {
    match value {
        serde_json::Value::Object(map) => map,
        _ => panic!("an object"),
    }
}

// S46 less what is not built (OD-1): the names offered, each an object
// schema whose required arguments are its own properties.
#[test]
fn the_tools_offered_are_the_built_ones_each_with_an_object_schema() {
    let offered = tools();
    let names: Vec<&str> = offered.iter().map(|t| t.name.as_ref()).collect();
    assert_eq!(names, TOOL_NAMES);
    assert_eq!(names.last(), Some(&"approval_prompt"));
    for tool in &offered {
        let schema = tool.input_schema.as_ref();
        assert_eq!(schema["type"], "object", "{}", tool.name);
        assert!(tool.description.is_some(), "{}", tool.name);
        for required in schema["required"].as_array().unwrap() {
            let name = required.as_str().unwrap();
            assert!(
                schema["properties"].get(name).is_some(),
                "{}: {name}",
                tool.name
            );
        }
    }
    let launch = offered.iter().find(|t| t.name == "child_launch").unwrap();
    assert_eq!(launch.input_schema["required"], json!([]));
    let send = offered.iter().find(|t| t.name == "child_send").unwrap();
    assert_eq!(
        send.input_schema["required"],
        json!(["child", "urgency", "text"])
    );
    let assemble = offered
        .iter()
        .find(|t| t.name == "context_assemble")
        .unwrap();
    assert_eq!(assemble.input_schema["required"], json!(["bundles"]));
    // P0 Q2: claude calls it with exactly these three.
    let prompt = offered
        .iter()
        .find(|t| t.name == "approval_prompt")
        .unwrap();
    assert_eq!(
        prompt.input_schema["required"],
        json!(["tool_name", "input", "tool_use_id"])
    );
    assert_eq!(prompt.input_schema["properties"]["input"]["type"], "object");
}

#[test]
fn a_text_argument_is_read_or_said_missing() {
    let given = args(json!({"child": "brisk-copper-moth", "n": 3}));
    assert_eq!(
        text("child_close", &given, "child"),
        Ok("brisk-copper-moth")
    );
    assert_eq!(
        text("child_close", &given, "n"),
        Err(ToolArgs::Missing {
            tool: "child_close".to_string(),
            argument: "n".to_string()
        })
    );
    assert!(text("child_close", &given, "absent").is_err());
}

#[test]
fn a_list_argument_is_its_texts_and_absent_only_where_optional() {
    let given = args(json!({"bundles": ["a", "b"], "bad": ["a", 1], "one": "a"}));
    assert_eq!(
        texts("t", &given, "bundles", true),
        Ok(vec!["a".to_string(), "b".to_string()])
    );
    assert_eq!(texts("t", &given, "tags", false), Ok(Vec::new()));
    assert!(texts("t", &given, "tags", true).is_err());
    assert!(texts("t", &given, "bad", false).is_err());
    assert!(texts("t", &given, "one", false).is_err());
}

#[test]
fn each_argument_error_reports_its_args() {
    use angzarr_diagnostic::Diagnostic;
    let missing = ToolArgs::Missing {
        tool: "child_send".to_string(),
        argument: "text".to_string(),
    };
    assert_eq!(
        missing.to_string(),
        "mcp-server-argument-missing tool=child_send argument=text"
    );
    assert_eq!(
        ToolArgs::Unknown {
            tool: "x".to_string()
        }
        .args()
        .len(),
        1
    );
    assert_eq!(
        ToolArgs::Urgency {
            given: "shout".to_string()
        }
        .to_string(),
        "mcp-server-urgency-invalid given=shout"
    );
}

// Every tool offered has a description, the owner's approved text
// (2026-11-14) byte for byte: embedded from `tools/<tool>.md`, without the
// file's closing newline. Written out here, so an edit to a file is seen.
#[test]
fn every_tool_offered_is_described_in_the_approved_words() {
    let approved = [
        ("session_list", "List this project's sessions (all: every project's), as the daemon's session index has them."),
        ("session_show", "Show one session as the daemon has it: its state, owner, children and pending messages."),
        ("transcript_show", "Show a session's transcript, its lines as its hooks have delivered them so far."),
        ("context_assemble", "Load the fragments you chose from this project's premise index, by the names it lists."),
        ("child_launch", "Launch a child of yours: a session of its own, run by the user's daemon, that takes its task as its first turn and stays open between tasks until you close it. Give it its task when you launch it, or send it one after: a child with no task sits idle. To have it work in another ctxloom project, give that project's directory as `project` (relative to your own project's root); it runs there on that project's own config, and only once the user has trusted that project. Prefer children launched this way to your engine's own subagents; each is a recorded, resumable session of its own that the user can see and steer."),
        ("child_send", "Send one of your children a message: inform (read at its next turn), wake (start a turn) or steer (interrupt its turn)."),
        ("child_close", "Close one of your children: its engine is ended, and it takes no more messages."),
        ("child_clear", "Clear one of your children's conversation, as its engine's own /clear does."),
        // texts.md §1 (step 8 U4, D14), accepted verbatim (owner,
        // 2026-11-16; reviewed before release).
        ("approval_prompt", "Ask the user whether a tool call may run. Your engine calls this itself, as its permission prompt, when its permissions say to ask; you never need to call it. The call waits while the user decides from their own terminal, and answers with their choice: run it this once, run it for the rest of this session, always run it, or do not. If nobody answers in time, it is denied. A denial carries a message: read it, do not retry the same call unasked, and tell the user what you wanted to run and why."),
    ];
    let offered = tools();
    assert_eq!(offered.len(), approved.len());
    for tool in &offered {
        let said = tool.description.as_deref().unwrap_or_default();
        assert!(!said.trim().is_empty(), "{} is described", tool.name);
        assert_eq!(
            said,
            said.trim(),
            "{}: no surrounding whitespace",
            tool.name
        );
        let words = approved
            .iter()
            .find(|(name, _)| *name == tool.name)
            .map(|(_, words)| *words);
        assert_eq!(Some(said), words, "{}", tool.name);
    }
}

// A description file's closing newline, in either convention, is not part
// of the description; nothing else is taken off.
#[test]
fn a_description_is_its_file_without_the_closing_newline() {
    assert_eq!(super::describe("Words.\n"), "Words.");
    assert_eq!(super::describe("Words.\r\n"), "Words.");
    assert_eq!(super::describe("Words."), "Words.");
    assert_eq!(super::describe(" Words. \n"), " Words. ");
}

// A session argument is a string, said to be a harp or a full name.
#[test]
fn a_session_argument_is_a_string_naming_a_harp_or_a_full_name() {
    let offered = tools();
    let show = offered.iter().find(|t| t.name == "session_show").unwrap();
    let session = &show.input_schema["properties"]["session"];
    assert_eq!(session["type"], "string", "{session}");
    let description = session["description"].as_str().unwrap();
    assert!(
        description.starts_with("The session: a session's harp in this project"),
        "{description}"
    );
}

// A message's text is its JSON.
#[test]
fn a_messages_text_is_its_json() {
    let message = json!({"name": "x", "n": 1});
    let text = json_text(&message);
    assert_eq!(
        serde_json::from_str::<serde_json::Value>(&text).ok(),
        Some(message),
        "{text}"
    );
}

/// A daemon that answers a launch: what it was asked, in order.
#[derive(Clone, Default)]
struct Launching(std::sync::Arc<std::sync::Mutex<Vec<String>>>);

impl daemon_api::Daemons for Launching {
    type Api = Launching;

    async fn serving(&self) -> Result<Option<Launching>, daemon_api::Unreached> {
        Ok(Some(self.clone()))
    }

    async fn reach(&self) -> Result<Launching, daemon_api::Unreached> {
        Ok(self.clone())
    }
}

impl daemon_api::DaemonApi for Launching {
    async fn get_daemon(
        &self,
        _: daemon_proto::v1::GetDaemonRequest,
    ) -> Result<daemon_proto::v1::Daemon, daemon_api::Called> {
        unreachable!()
    }
    async fn stop_daemon(
        &self,
        _: daemon_proto::v1::StopDaemonRequest,
    ) -> Result<daemon_proto::v1::Daemon, daemon_api::Called> {
        unreachable!()
    }
    async fn resolve_project(
        &self,
        r: daemon_proto::v1::ResolveProjectRequest,
    ) -> Result<daemon_proto::v1::Project, daemon_api::Called> {
        self.0.lock().unwrap().push(format!("resolve {}", r.root));
        Ok(daemon_proto::v1::Project {
            name: "projects/bold-api-pine".to_string(),
            uid: r.project_id,
            root: r.root,
            ..daemon_proto::v1::Project::default()
        })
    }
    async fn get_trust(
        &self,
        r: daemon_proto::v1::GetTrustRequest,
    ) -> Result<daemon_proto::v1::Trust, daemon_api::Called> {
        Ok(daemon_proto::v1::Trust {
            name: r.name,
            trusted: true,
            ..daemon_proto::v1::Trust::default()
        })
    }
    async fn get_project(
        &self,
        r: daemon_proto::v1::GetProjectRequest,
    ) -> Result<daemon_proto::v1::Project, daemon_api::Called> {
        Ok(daemon_proto::v1::Project {
            name: r.name,
            uid: "calm-otter".to_string(),
            ..daemon_proto::v1::Project::default()
        })
    }
    async fn get_session(
        &self,
        r: daemon_proto::v1::GetSessionRequest,
    ) -> Result<daemon_proto::v1::Session, daemon_api::Called> {
        // D16: `tall-oak-crow` holds a child of its own.
        let children = match r.name.ends_with("/sessions/tall-oak-crow") {
            true => vec!["projects/calm-quiet-heron/sessions/olive-quiet-finch".to_string()],
            false => Vec::new(),
        };
        Ok(daemon_proto::v1::Session {
            name: r.name,
            children,
            ..daemon_proto::v1::Session::default()
        })
    }
    async fn list_sessions(
        &self,
        _: daemon_proto::v1::ListSessionsRequest,
    ) -> Result<daemon_proto::v1::ListSessionsResponse, daemon_api::Called> {
        unreachable!()
    }
    async fn search_sessions(
        &self,
        _: daemon_proto::v1::SearchSessionsRequest,
    ) -> Result<daemon_proto::v1::ListSessionsResponse, daemon_api::Called> {
        unreachable!()
    }
    async fn create_hand_off(
        &self,
        r: daemon_proto::v1::CreateHandOffRequest,
    ) -> Result<daemon_proto::v1::HandOff, daemon_api::Called> {
        self.0
            .lock()
            .unwrap()
            .push(format!("hand-off {} {}{}", r.owner, r.agent, r.project));
        Ok(daemon_proto::v1::HandOff {
            name: format!("{}/handOffs/{}", r.parent, r.hand_off_id),
            engine: String::new(),
            project: r.project,
        })
    }
    async fn handle_sync(
        &self,
        _: daemon_api::CommandBook,
        _: Vec<String>,
    ) -> Result<daemon_api::Synced, daemon_api::Called> {
        self.0.lock().unwrap().push("write".to_string());
        Ok(daemon_api::Synced {
            applied: true,
            projections: Vec::new(),
        })
    }
}

// `child_launch` (slice 4 U3, s4-Q1): the calling session's own child,
// named as asked (or minted), planned for the agent asked, answered with
// the child's session.
#[tokio::test]
async fn child_launch_launches_the_sessions_own_child() {
    let daemons = Launching::default();
    let caller = daemon_api::Caller::new(
        daemons.clone(),
        "projects/calm-quiet-heron/sessions/amber-quiet-heron",
        daemon_proto::v1::Caller::default(),
        Box::new(|| "id-1".to_string()),
    )
    .unwrap_or_else(|_| panic!("a name"))
    .naming(Box::new(|| "minted-child-name".to_string()));
    let answered = super::dispatch(
        &caller,
        &Engine::default(),
        "child_launch",
        &args(json!({"name": "brisk-copper-moth", "agent": "reviewer", "task": "t"})),
    )
    .await
    .unwrap_or_else(|_| panic!("launched"));
    let session: serde_json::Value = serde_json::from_str(&answered[0]).unwrap();
    assert_eq!(
        session["name"],
        "projects/calm-quiet-heron/sessions/brisk-copper-moth"
    );
    assert_eq!(
        *daemons.0.lock().unwrap(),
        [
            "hand-off amber-quiet-heron reviewer".to_string(),
            "write".to_string()
        ]
    );
    let minted = super::dispatch(
        &caller,
        &Engine::default(),
        "child_launch",
        &args(json!({})),
    )
    .await
    .unwrap_or_else(|_| panic!("launched"));
    let session: serde_json::Value = serde_json::from_str(&minted[0]).unwrap();
    assert_eq!(
        session["name"],
        "projects/calm-quiet-heron/sessions/minted-child-name"
    );
}

// Step 8 Q1, Q-C: `child_launch`'s `project` is the directory of the
// project the child works in, resolved as the caller resolves it (a
// session's own project root), and the child is named there.
#[tokio::test]
async fn child_launch_with_a_project_launches_the_child_there() {
    let daemons = Launching::default();
    let resolved = std::sync::Arc::new(std::sync::Mutex::new(Vec::new()));
    let seen = std::sync::Arc::clone(&resolved);
    let caller = daemon_api::Caller::new(
        daemons.clone(),
        "projects/calm-quiet-heron/sessions/amber-quiet-heron",
        daemon_proto::v1::Caller::default(),
        Box::new(|| "id-1".to_string()),
    )
    .unwrap_or_else(|_| panic!("a name"))
    .resolving(std::sync::Arc::new(move |dir: &str| {
        seen.lock().unwrap().push(dir.to_string());
        Ok(daemon_proto::v1::ResolveProjectRequest {
            project_id: "api-otter".to_string(),
            root: "/work/api".to_string(),
            ..daemon_proto::v1::ResolveProjectRequest::default()
        })
    }));
    let answered = super::dispatch(
        &caller,
        &Engine::default(),
        "child_launch",
        &args(json!({"name": "brisk-copper-moth", "project": "../api", "task": "t"})),
    )
    .await
    .unwrap_or_else(|_| panic!("launched"));
    assert_eq!(*resolved.lock().unwrap(), ["../api"]);
    let session: serde_json::Value = serde_json::from_str(&answered[0]).unwrap();
    assert_eq!(
        session["name"],
        "projects/bold-api-pine/sessions/brisk-copper-moth"
    );
    assert_eq!(
        daemons.0.lock().unwrap()[..2],
        [
            "resolve /work/api".to_string(),
            "hand-off amber-quiet-heron projects/bold-api-pine".to_string(),
        ]
    );
}

/// The engine, as a test stands it in (step 10c U11): whether it can be
/// asked, its answer to each confirmation, and each question it was put.
#[derive(Default)]
struct Engine {
    can: bool,
    yes: bool,
    asked: std::sync::Mutex<Vec<serde_json::Value>>,
}

impl Engine {
    fn answering(yes: bool) -> Engine {
        Engine {
            can: true,
            yes,
            ..Engine::default()
        }
    }

    fn asked(&self) -> Vec<serde_json::Value> {
        self.asked.lock().unwrap().clone()
    }
}

impl super::Elicit for Engine {
    fn can(&self) -> bool {
        self.can
    }

    async fn ask(&self, params: serde_json::Value) -> Option<(String, Option<serde_json::Value>)> {
        self.asked.lock().unwrap().push(params);
        let action = if self.yes { "accept" } else { "decline" };
        Some((action.to_string(), Some(json!({"confirmed": true}))))
    }
}

fn session_caller(daemons: &Launching) -> daemon_api::Caller<Launching> {
    daemon_api::Caller::new(
        daemons.clone(),
        "projects/calm-quiet-heron/sessions/amber-quiet-heron",
        daemon_proto::v1::Caller::default(),
        Box::new(|| "id-1".to_string()),
    )
    .unwrap_or_else(|_| panic!("a name"))
}

// D16 (S40): `child_close` of a child holding children of its own asks the
// engine to confirm the cascade, naming them; a yes closes it, a no closes
// nothing and says so.
#[tokio::test]
async fn child_close_of_a_child_with_children_asks_to_confirm_first() {
    for yes in [true, false] {
        let daemons = Launching::default();
        let engine = Engine::answering(yes);
        let answered = super::dispatch(
            &session_caller(&daemons),
            &engine,
            "child_close",
            &args(json!({"child": "tall-oak-crow"})),
        )
        .await
        .unwrap_or_else(|_| panic!("answered"));
        let asked = engine.asked();
        assert_eq!(asked.len(), 1, "{asked:?}");
        assert_eq!(
            asked[0]["message"],
            "Close \"tall-oak-crow\" and, with it, \"olive-quiet-finch\"?"
        );
        let wrote = daemons.0.lock().unwrap().contains(&"write".to_string());
        assert_eq!(wrote, yes, "{answered:?}");
        assert_eq!(answered[0].contains("was not closed"), !yes, "{answered:?}");
    }
}

// A close that takes no child with it is not asked; one an engine that
// cannot be asked would have to confirm closes nothing.
#[tokio::test]
async fn child_close_asks_only_for_a_cascade_and_only_an_engine_that_can_answer() {
    let daemons = Launching::default();
    let engine = Engine::answering(true);
    super::dispatch(
        &session_caller(&daemons),
        &engine,
        "child_close",
        &args(json!({"child": "brisk-copper-moth"})),
    )
    .await
    .unwrap_or_else(|_| panic!("answered"));
    assert!(engine.asked().is_empty());
    assert!(daemons.0.lock().unwrap().contains(&"write".to_string()));
    let daemons = Launching::default();
    let mute = Engine::default();
    let answered = super::dispatch(
        &session_caller(&daemons),
        &mute,
        "child_close",
        &args(json!({"child": "tall-oak-crow"})),
    )
    .await
    .unwrap_or_else(|_| panic!("answered"));
    assert!(mute.asked().is_empty());
    assert!(!daemons.0.lock().unwrap().contains(&"write".to_string()));
    assert!(answered[0].contains("was not closed"), "{answered:?}");
}

// A preview's needs presented one by one, each answer kept; none at all is
// nothing to ask and nothing declined.
#[tokio::test]
async fn a_previews_needs_are_each_asked_and_answered() {
    let confirm = |id: &str| daemon_proto::v1::Need {
        id: id.to_string(),
        kind: Some(daemon_proto::v1::need::Kind::Confirm(
            daemon_proto::v1::Confirm {
                text: format!("{id}?"),
                ..daemon_proto::v1::Confirm::default()
            },
        )),
        optional: false,
    };
    let engine = Engine::answering(true);
    let answered = super::answers(&engine, &[confirm("a"), confirm("b")])
        .await
        .unwrap();
    let ids: Vec<&str> = answered.iter().map(|a| a.need_id.as_str()).collect();
    assert_eq!(ids, ["a", "b"]);
    assert_eq!(engine.asked().len(), 2);
    assert_eq!(
        super::answers(&Engine::default(), &[]).await,
        Some(Vec::new())
    );
}
