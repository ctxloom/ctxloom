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
        ]
    );
}

// Each error says what it was given, by name.
#[test]
fn each_error_reports_its_args() {
    use angzarr_diagnostic::Diagnostic;
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

// S45: the handshake and the list are answered over the transport, from the
// static list, reaching no daemon; an unknown tool is the tool's error.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn the_server_answers_the_handshake_and_its_list_over_its_transport() {
    use rmcp::ServiceExt;
    use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
    let caller = daemon_api::Caller::new(
        Nowhere,
        "projects/calm-quiet-heron/sessions/amber-quiet-heron",
        daemon_proto::v1::EnvVars::default(),
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
        .write_all(b"{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\",\"params\":{}}\n{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"approval_prompt\",\"arguments\":{}}}\n")
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
        Some(7),
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
