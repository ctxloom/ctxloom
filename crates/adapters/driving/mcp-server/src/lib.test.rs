// The catalog test of the binary reads these: one value of every diagnostic
// of the crate, each this crate's code.
#[test]
fn the_samples_hold_every_diagnostic_of_the_crate() {
    let codes: Vec<String> = crate::diagnostic_samples()
        .iter()
        .map(|s| s.code().to_string())
        .collect();
    assert_eq!(
        codes,
        [
            "mcp-server-no-session",
            "mcp-server-failed",
            "mcp-server-argument-missing",
            "mcp-server-unknown-tool",
            "mcp-server-urgency-invalid",
            "mcp-server-approval-expired",
            "mcp-server-approval-denied-at-once",
            "mcp-server-approval-denied",
        ]
    );
}

// Each error says what it was given, by name.
#[test]
fn each_error_reports_its_args() {
    use coded_diagnostic::Diagnostic;
    let no = super::McpError::NoSession {
        variable: "CTXLOOM_SESSION".to_string(),
    };
    assert_eq!(
        no.to_string(),
        "mcp-server-no-session variable=CTXLOOM_SESSION"
    );
    let failed = super::McpError::Failed {
        detail: "boom".to_string(),
    };
    assert_eq!(failed.args().len(), 1);
    assert_eq!(failed.to_string(), "mcp-server-failed ext_err_txt=boom");
}

// The hidden verb an engine starts.
#[test]
fn the_verb_is_the_hidden_serve() {
    let verb = super::verb();
    assert_eq!(verb.get_name(), "serve");
    assert!(verb.is_hide_set());
}

// A tool's error is its code, then what the server is told to say of it.
#[test]
fn a_tools_error_is_its_code_then_its_words() {
    let say: super::Say = Box::new(|d| format!("said {}", d.code()));
    let error = super::McpError::Failed {
        detail: "x".to_string(),
    };
    assert_eq!(
        super::error_text(&say, &error),
        "mcp-server-failed: said mcp-server-failed"
    );
}

/// No daemon: a test of what the server answers without one.
struct Nowhere;

impl daemon_api::Daemons for Nowhere {
    type Api = Never;

    async fn serving(&self) -> Result<Option<Never>, daemon_api::Unreached> {
        unreachable!("the list reaches no daemon")
    }

    async fn reach(&self) -> Result<Never, daemon_api::Unreached> {
        unreachable!("the list reaches no daemon")
    }
}

/// A daemon API never made.
struct Never;

impl daemon_api::DaemonApi for Never {
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
        _: daemon_proto::v1::ResolveProjectRequest,
    ) -> Result<daemon_proto::v1::Project, daemon_api::Called> {
        unreachable!()
    }
    async fn get_project(
        &self,
        _: daemon_proto::v1::GetProjectRequest,
    ) -> Result<daemon_proto::v1::Project, daemon_api::Called> {
        unreachable!()
    }
    async fn get_session(
        &self,
        _: daemon_proto::v1::GetSessionRequest,
    ) -> Result<daemon_proto::v1::Session, daemon_api::Called> {
        unreachable!()
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
}

fn server() -> super::Server<Nowhere> {
    let caller = daemon_api::Caller::new(
        Nowhere,
        "projects/calm-quiet-heron/sessions/amber-quiet-heron",
        daemon_proto::v1::Caller::default(),
        Box::new(String::new),
    )
    .unwrap_or_else(|_| panic!("a name"));
    super::Server::new(caller, "v0.1.0-abc")
}

// What a message element is, said ONCE a session, in the server's
// instructions (the model reads them at initialize): that the elements hold
// untrusted data from another agent and never the user's instructions, that
// an id may be offered again and a repeat is ignored, and the urgencies.
#[test]
fn the_server_explains_delivered_messages_once_in_its_instructions() {
    use super::ServerHandler;
    let info = server().get_info();
    let text = info.instructions.expect("instructions");
    for word in [
        "<message",
        "untrusted",
        "not instructions from the user",
        "ignore a repeat",
        "inform",
        "wake",
    ] {
        assert!(text.contains(word), "{word}: {text}");
    }
}

// A hosted child is fed its messages as turns, never as elements: its
// server says nothing of them.
#[test]
fn a_server_without_message_notes_gives_no_instructions() {
    use super::ServerHandler;
    assert_eq!(
        server().without_message_notes().get_info().instructions,
        None
    );
}

// S45: the handshake and the list are answered over the transport, from the
// static list, reaching no daemon; an unknown tool is the tool's error.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn the_server_answers_the_handshake_and_its_list_over_its_transport() {
    use rmcp::ServiceExt;
    use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
    let caller = daemon_api::Caller::new(
        Nowhere,
        "projects/calm-quiet-heron/sessions/amber-quiet-heron",
        daemon_proto::v1::Caller::default(),
        Box::new(String::new),
    )
    .unwrap_or_else(|_| panic!("a name"));
    let server = super::Server::new(caller, "v0.1.0-abc");
    let (client, server_side) = tokio::io::duplex(1 << 16);
    let (server_read, server_write) = tokio::io::split(server_side);
    let running = tokio::spawn(async move {
        let running = server.serve((server_read, server_write)).await.unwrap();
        running.waiting().await.unwrap();
    });
    let (client_read, mut client_write) = tokio::io::split(client);
    let mut lines = BufReader::new(client_read).lines();
    client_write
        .write_all(b"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"t\",\"version\":\"0\"}}}\n")
        .await
        .unwrap();
    let init: serde_json::Value =
        serde_json::from_str(&lines.next_line().await.unwrap().unwrap()).unwrap();
    assert_eq!(init["result"]["serverInfo"]["name"], "ctxloom", "{init}");
    assert_eq!(
        init["result"]["serverInfo"]["version"], "v0.1.0-abc",
        "{init}"
    );
    client_write
        .write_all(b"{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\",\"params\":{}}\n{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"nope\",\"arguments\":{}}}\n")
        .await
        .unwrap();
    // Answered as each is done, not in the order asked: found by id.
    let mut answers = std::collections::BTreeMap::new();
    for _ in 0..2 {
        let answer: serde_json::Value =
            serde_json::from_str(&lines.next_line().await.unwrap().unwrap()).unwrap();
        answers.insert(answer["id"].as_u64().unwrap(), answer);
    }
    let listed = &answers[&2];
    assert_eq!(
        listed["result"]["tools"].as_array().map(Vec::len),
        Some(crate::TOOL_NAMES.len()),
        "{listed}"
    );
    let unknown = &answers[&3];
    assert_eq!(unknown["result"]["isError"], true, "{unknown}");
    assert!(
        unknown["result"]["content"][0]["text"]
            .as_str()
            .unwrap()
            .starts_with("mcp-server-unknown-tool: "),
        "{unknown}"
    );
    drop(client_write);
    drop(lines);
    running.await.unwrap();
}

// A server told how to say an error says a tool's error so.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_server_saying_its_errors_says_them_so() {
    use rmcp::ServiceExt;
    use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
    let caller = daemon_api::Caller::new(
        Nowhere,
        "projects/calm-quiet-heron/sessions/amber-quiet-heron",
        daemon_proto::v1::Caller::default(),
        Box::new(String::new),
    )
    .unwrap_or_else(|_| panic!("a name"));
    let server = super::Server::new(caller, "v0.1.0-abc")
        .saying(Box::new(|d| format!("in words: {}", d.code())));
    let (client, server_side) = tokio::io::duplex(1 << 16);
    let (server_read, server_write) = tokio::io::split(server_side);
    let running = tokio::spawn(async move {
        let running = server.serve((server_read, server_write)).await.unwrap();
        running.waiting().await.unwrap();
    });
    let (client_read, mut client_write) = tokio::io::split(client);
    let mut lines = BufReader::new(client_read).lines();
    client_write
        .write_all(b"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"t\",\"version\":\"0\"}}}\n")
        .await
        .unwrap();
    lines.next_line().await.unwrap().unwrap();
    client_write
        .write_all(b"{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"nope\",\"arguments\":{}}}\n")
        .await
        .unwrap();
    let unknown: serde_json::Value =
        serde_json::from_str(&lines.next_line().await.unwrap().unwrap()).unwrap();
    assert_eq!(
        unknown["result"]["content"][0]["text"],
        "mcp-server-unknown-tool: in words: mcp-server-unknown-tool",
        "{unknown}"
    );
    drop(client_write);
    drop(lines);
    running.await.unwrap();
}

// ------------------------------------------------- the approval prompt (U4)

/// What the asking fake daemon saw.
#[derive(Default)]
struct Asked {
    calls: Vec<String>,
    withdrawn: Vec<String>,
}

/// A daemon that records the ask as `asked` and settles it as `settled`
/// (none: the wait never ends).
#[derive(Clone)]
struct Asking {
    seen: std::sync::Arc<std::sync::Mutex<Asked>>,
    asked: daemon_proto::v1::Approval,
    settled: Option<daemon_proto::v1::Approval>,
}

impl daemon_api::Daemons for Asking {
    type Api = Asking;

    async fn serving(&self) -> Result<Option<Asking>, daemon_api::Unreached> {
        unreachable!()
    }

    async fn reach(&self) -> Result<Asking, daemon_api::Unreached> {
        Ok(self.clone())
    }
}

impl daemon_api::DaemonApi for Asking {
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
        _: daemon_proto::v1::ResolveProjectRequest,
    ) -> Result<daemon_proto::v1::Project, daemon_api::Called> {
        unreachable!()
    }
    async fn get_project(
        &self,
        _: daemon_proto::v1::GetProjectRequest,
    ) -> Result<daemon_proto::v1::Project, daemon_api::Called> {
        unreachable!()
    }
    async fn get_session(
        &self,
        _: daemon_proto::v1::GetSessionRequest,
    ) -> Result<daemon_proto::v1::Session, daemon_api::Called> {
        unreachable!()
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
    async fn create_approval(
        &self,
        r: daemon_proto::v1::CreateApprovalRequest,
    ) -> Result<daemon_proto::v1::Approval, daemon_api::Called> {
        self.seen.lock().unwrap().calls.push(format!(
            "create {} {} {}",
            r.tool_name, r.engine_request_id, r.input_json
        ));
        Ok(self.asked.clone())
    }
    async fn wait_approval(
        &self,
        r: daemon_proto::v1::WaitApprovalRequest,
        until: daemon_api::Until,
    ) -> Result<daemon_proto::v1::Approval, daemon_api::Called> {
        self.seen
            .lock()
            .unwrap()
            .calls
            .push(format!("wait {} until {}", r.name, until.seconds));
        match &self.settled {
            Some(settled) => Ok(settled.clone()),
            None => std::future::pending().await,
        }
    }
    async fn handle(&self, book: daemon_api::CommandBook) -> Result<bool, daemon_api::Called> {
        use prost::Message as _;
        let fact = match book.page.and_then(|p| p.body) {
            Some(angzarr_embedded::v1::command_page::Body::Fact(fact)) => fact,
            other => panic!("not a fact: {other:?}"),
        };
        let withdrawn = approvals_proto::v1::RecordApprovalWithdrawn::decode(
            fact.payload.unwrap().value.as_slice(),
        )
        .unwrap();
        self.seen
            .lock()
            .unwrap()
            .withdrawn
            .push(format!("{} {}", withdrawn.ask_id, withdrawn.reason));
        Ok(true)
    }
}

fn approval(state: &str, kind: &str, option: &str) -> daemon_proto::v1::Approval {
    daemon_proto::v1::Approval {
        name: "projects/calm-quiet-heron/approvals/ask-tidy-wren".to_string(),
        engine: "claude-code".to_string(),
        state: state.to_string(),
        option: option.to_string(),
        option_kind: kind.to_string(),
        timeout_seconds: 300,
        ask_time: Some(daemon_api::Until {
            seconds: 1000,
            nanos: 0,
        }),
        expire_time: Some(daemon_api::Until {
            seconds: 1300,
            nanos: 0,
        }),
        uid: "0b0b0b0b-0b0b-4b0b-8b0b-0b0b0b0b0b0b".to_string(),
        options: vec![daemon_proto::v1::ApprovalOption {
            id: "allow-session".to_string(),
            rule: "Bash(rm -rf build/)".to_string(),
            ..daemon_proto::v1::ApprovalOption::default()
        }],
        ..daemon_proto::v1::Approval::default()
    }
}

/// The server over `daemons`, its input read through the pump, as an
/// engine reaches it: initialized, then each line answered.
struct Engine {
    write: tokio::io::WriteHalf<tokio::io::DuplexStream>,
    lines: tokio::io::Lines<tokio::io::BufReader<tokio::io::ReadHalf<tokio::io::DuplexStream>>>,
    running: tokio::task::JoinHandle<()>,
}

impl Engine {
    async fn of(daemons: Asking) -> Engine {
        use rmcp::ServiceExt;
        use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
        let caller = daemon_api::Caller::new(
            daemons,
            "projects/calm-quiet-heron/sessions/brisk-copper-moth",
            daemon_proto::v1::Caller::default(),
            Box::new(|| "id".to_string()),
        )
        .unwrap_or_else(|_| panic!("a name"));
        let server = super::Server::new(caller, "v0.1.0-abc")
            .saying(Box::new(|d| format!("said {}", d.code())));
        let cancels = server.cancels.clone();
        let (client, server_side) = tokio::io::duplex(1 << 16);
        let (server_read, server_write) = tokio::io::split(server_side);
        let (feed, input) = tokio::io::duplex(1 << 16);
        let (answers, mut answered) = tokio::sync::mpsc::unbounded_channel::<String>();
        let output = std::sync::Arc::new(tokio::sync::Mutex::new(server_write));
        let out = std::sync::Arc::clone(&output);
        tokio::spawn(async move {
            while let Some(line) = answered.recv().await {
                out.lock().await.write_all(line.as_bytes()).await.unwrap();
            }
        });
        tokio::spawn(super::pump(
            server_read,
            feed,
            move |line| answers.send(line).unwrap(),
            cancels,
        ));
        let running = tokio::spawn(async move {
            let running = server.serve((input, Shared(output))).await.unwrap();
            running.waiting().await.unwrap();
        });
        let (client_read, write) = tokio::io::split(client);
        Engine {
            write,
            lines: BufReader::new(client_read).lines(),
            running,
        }
    }

    async fn send(&mut self, line: &str) {
        use tokio::io::AsyncWriteExt;
        self.write.write_all(line.as_bytes()).await.unwrap();
        self.write.write_all(b"\n").await.unwrap();
    }

    async fn next(&mut self) -> serde_json::Value {
        let line = tokio::time::timeout(std::time::Duration::from_secs(10), self.lines.next_line())
            .await
            .expect("an answer within its time")
            .unwrap()
            .unwrap();
        serde_json::from_str(&line).unwrap()
    }

    async fn initialized(&mut self) {
        self.send(r#"{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}"#).await;
        let init = self.next().await;
        assert_eq!(init["result"]["serverInfo"]["name"], "ctxloom", "{init}");
        self.send(r#"{"jsonrpc":"2.0","method":"notifications/initialized"}"#)
            .await;
    }

    async fn ask(&mut self, id: u64) {
        self.send(&format!(
            r#"{{"jsonrpc":"2.0","id":{id},"method":"tools/call","params":{{"name":"approval_prompt","arguments":{{"tool_name":"Bash","input":{{"command":"rm -rf build/"}},"tool_use_id":"toolu_{id}"}}}}}}"#
        ))
        .await;
    }

    async fn end(self) {
        drop(self.write);
        drop(self.lines);
        self.running.await.unwrap();
    }
}

/// The server's output, shared by rmcp and the pump's answers.
struct Shared(std::sync::Arc<tokio::sync::Mutex<tokio::io::WriteHalf<tokio::io::DuplexStream>>>);

impl tokio::io::AsyncWrite for Shared {
    fn poll_write(
        self: std::pin::Pin<&mut Self>,
        cx: &mut std::task::Context<'_>,
        bytes: &[u8],
    ) -> std::task::Poll<std::io::Result<usize>> {
        let mut held = match self.0.try_lock() {
            Ok(held) => held,
            Err(_) => {
                cx.waker().wake_by_ref();
                return std::task::Poll::Pending;
            }
        };
        std::pin::Pin::new(&mut *held).poll_write(cx, bytes)
    }

    fn poll_flush(
        self: std::pin::Pin<&mut Self>,
        cx: &mut std::task::Context<'_>,
    ) -> std::task::Poll<std::io::Result<()>> {
        match self.0.try_lock() {
            Ok(mut held) => std::pin::Pin::new(&mut *held).poll_flush(cx),
            Err(_) => {
                cx.waker().wake_by_ref();
                std::task::Poll::Pending
            }
        }
    }

    fn poll_shutdown(
        self: std::pin::Pin<&mut Self>,
        cx: &mut std::task::Context<'_>,
    ) -> std::task::Poll<std::io::Result<()>> {
        match self.0.try_lock() {
            Ok(mut held) => std::pin::Pin::new(&mut *held).poll_shutdown(cx),
            Err(_) => {
                cx.waker().wake_by_ref();
                std::task::Poll::Pending
            }
        }
    }
}

fn answer_text(answer: &serde_json::Value) -> serde_json::Value {
    assert_ne!(answer["result"]["isError"], true, "{answer}");
    serde_json::from_str(answer["result"]["content"][0]["text"].as_str().unwrap()).unwrap()
}

// P0 Q11: claude's discovery probe, a string id, before `initialize`, is
// answered an empty result, and the handshake goes on.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn the_discovery_probe_is_answered_empty_before_the_handshake() {
    let seen = std::sync::Arc::new(std::sync::Mutex::new(Asked::default()));
    let mut engine = Engine::of(Asking {
        seen,
        asked: approval("waiting", "", ""),
        settled: None,
    })
    .await;
    engine
        .send(r#"{"jsonrpc":"2.0","id":"server-discover-probe-1","method":"server/discover","params":{}}"#)
        .await;
    let discovered = engine.next().await;
    assert_eq!(discovered["id"], "server-discover-probe-1", "{discovered}");
    assert_eq!(discovered["result"], serde_json::json!({}), "{discovered}");
    engine.initialized().await;
    engine
        .send(r#"{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}"#)
        .await;
    let listed = engine.next().await;
    assert_eq!(
        listed["result"]["tools"].as_array().map(Vec::len),
        Some(crate::TOOL_NAMES.len()),
        "{listed}"
    );
    engine.end().await;
}

// S27: the ask is recorded, waited on under its own deadline, and answered
// in claude's shape: an allow-session with the session rule.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn an_ask_is_recorded_waited_on_and_answered_as_claude_reads_it() {
    let seen = std::sync::Arc::new(std::sync::Mutex::new(Asked::default()));
    let mut engine = Engine::of(Asking {
        seen: std::sync::Arc::clone(&seen),
        asked: approval("waiting", "", ""),
        settled: Some(approval("answered", "allow_session", "allow-session")),
    })
    .await;
    engine.initialized().await;
    engine.ask(2).await;
    let answered = answer_text(&engine.next().await);
    assert_eq!(answered["behavior"], "allow");
    assert_eq!(answered["updatedInput"]["command"], "rm -rf build/");
    assert_eq!(
        answered["updatedPermissions"][0]["rules"][0]["ruleContent"],
        "rm -rf build/"
    );
    assert_eq!(
        seen.lock().unwrap().calls,
        [
            r#"create Bash toolu_2 {"command":"rm -rf build/"}"#.to_string(),
            // Its recorded expiry and the margin, by the wall clock (U5).
            "wait projects/calm-quiet-heron/approvals/ask-tidy-wren until 1360".to_string(),
        ]
    );
    engine.end().await;
}

// D16: an ask settled when it was made (approval_timeout: now) is answered
// at once, never waited on.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn an_ask_settled_at_once_is_answered_without_a_wait() {
    let seen = std::sync::Arc::new(std::sync::Mutex::new(Asked::default()));
    let mut expired = approval("expired", "", "");
    expired.timeout_seconds = 0;
    let mut engine = Engine::of(Asking {
        seen: std::sync::Arc::clone(&seen),
        asked: expired,
        settled: None,
    })
    .await;
    engine.initialized().await;
    engine.ask(2).await;
    let answered = answer_text(&engine.next().await);
    assert_eq!(answered["behavior"], "deny");
    assert_eq!(
        answered["message"],
        "said mcp-server-approval-denied-at-once"
    );
    assert_eq!(seen.lock().unwrap().calls.len(), 1);
    engine.end().await;
}

// The post-P0 ruling: a call claude cancels withdraws its ask, with
// claude's reason, is answered nothing, and the next call is served.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_cancelled_call_withdraws_its_ask_and_is_answered_nothing() {
    let seen = std::sync::Arc::new(std::sync::Mutex::new(Asked::default()));
    let mut engine = Engine::of(Asking {
        seen: std::sync::Arc::clone(&seen),
        asked: approval("waiting", "", ""),
        settled: None,
    })
    .await;
    engine.initialized().await;
    engine.ask(2).await;
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(10);
    while seen.lock().unwrap().calls.len() < 2 {
        assert!(std::time::Instant::now() < deadline, "the wait never began");
        tokio::time::sleep(std::time::Duration::from_millis(10)).await;
    }
    engine
        .send(r#"{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":2,"reason":"AbortError: remote-cancel"}}"#)
        .await;
    while seen.lock().unwrap().withdrawn.is_empty() {
        assert!(std::time::Instant::now() < deadline, "never withdrawn");
        tokio::time::sleep(std::time::Duration::from_millis(10)).await;
    }
    assert_eq!(
        seen.lock().unwrap().withdrawn,
        ["ask-tidy-wren AbortError: remote-cancel"]
    );
    engine
        .send(r#"{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}"#)
        .await;
    let next = engine.next().await;
    assert_eq!(next["id"], 3, "nothing answered the cancelled call: {next}");
    engine.end().await;
}

// The tool's arguments are its own: a missing one is the tool's error.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn an_ask_missing_an_argument_is_the_tools_error() {
    let seen = std::sync::Arc::new(std::sync::Mutex::new(Asked::default()));
    let mut engine = Engine::of(Asking {
        seen: std::sync::Arc::clone(&seen),
        asked: approval("waiting", "", ""),
        settled: None,
    })
    .await;
    engine.initialized().await;
    for (id, arguments) in [
        (2, r#"{"input":{},"tool_use_id":"t"}"#),
        (3, r#"{"tool_name":"Bash","input":{}}"#),
        (4, r#"{"tool_name":"Bash","tool_use_id":"t"}"#),
    ] {
        engine
            .send(&format!(
                r#"{{"jsonrpc":"2.0","id":{id},"method":"tools/call","params":{{"name":"approval_prompt","arguments":{arguments}}}}}"#
            ))
            .await;
        let answer = engine.next().await;
        assert_eq!(answer["result"]["isError"], true, "{answer}");
        assert!(
            answer["result"]["content"][0]["text"]
                .as_str()
                .unwrap()
                .starts_with("mcp-server-argument-missing"),
            "{answer}"
        );
    }
    assert!(seen.lock().unwrap().calls.is_empty());
    engine.end().await;
}

#[test]
fn test_serve_takes_no_wake_to_start_without_the_wake_poster() {
    let plain = super::verb().try_get_matches_from(["serve"]).unwrap();
    assert!(
        super::posts_wakes(&plain),
        "a terminal session's server posts wakes"
    );
    let hosted = super::verb()
        .try_get_matches_from(["serve", "--no-wake"])
        .unwrap();
    assert!(!super::posts_wakes(&hosted));
}
