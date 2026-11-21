//! `mcp`'s tests, beside its verbs.

mod read {
    use crate::tests::*;

    // mcp.feature (moved from the four-noun test of lib.test.rs, unchanged)
    #[test]
    fn test_mcp_server_list_and_show_and_the_bare_noun() {
        // OD-40: `ListMcpServersResponse`, each server below its bundle; a
        // show is the list filtered by the ID (design §3.5).
        let servers = json_of(&["mcp", "server", "list"]);
        assert_eq!(
            servers["mcp_servers"][0]["name"],
            "projects/calm-otter/bundles/demo/mcpServers/tree-sitter"
        );
        assert_eq!(servers["mcp_servers"][0]["command"], "tree-sitter");
        assert_eq!(json_of(&["mcp", "server"]), servers);
        let shown = json_of(&["mcp", "server", "show", "tree-sitter"]);
        assert_eq!(
            shown["mcp_servers"][0]["name"],
            "projects/calm-otter/bundles/demo/mcpServers/tree-sitter"
        );
        assert_eq!(shown["mcp_servers"][0]["args"][0], "--stdio");
        assert!(shown.get("found").is_none());
        assert!(text_of(&["mcp", "server", "show", "tree-sitter"])
            .starts_with("MCP Server: tree-sitter\n"));
        // plan supersedes Go: mcp.feature (S2) refuses the bare noun, which
        // listed the servers in S1.
        assert!(go(&["mcp"], TEXT, BARE).refused().contains("mcp-bare"));
        assert!(go(&["mcp"], JSON, BARE).refused().contains("mcp-bare-json"));
    }
}

mod write {
    use crate::tests::write_verbs::*;
    use crate::tests::{How, JSON, TEXT};

    // mcp.feature
    #[test]
    fn test_mcp_set_edit_and_the_bare_noun() {
        let (library, mem) = world();
        assert_eq!(
            text(
                &library,
                &[
                    "mcp",
                    "server",
                    "set",
                    "demo#mcp/tools",
                    "--command",
                    "run",
                    "--arg",
                    "-x",
                    "--arg",
                    "y",
                    "--env",
                    "A=1",
                    "--env",
                    "B=2",
                    "--notes",
                    "n",
                    "--installation",
                    "i",
                ]
            ),
            "Set MCP server \"tools\" in bundle \"demo\"\n"
        );
        let file = mem.text("demo", "mcp/tools.yaml").unwrap();
        assert!(file.contains("command: run"), "{file}");
        assert!(file.contains("- -x") && file.contains("- y"), "{file}");
        assert!(
            file.contains("A: 1") && file.contains("B: \"2\"") || file.contains("B: 2"),
            "{file}"
        );
        let doc = json(
            &library,
            &[
                "mcp",
                "server",
                "set",
                "demo#mcp/tools",
                "--command",
                "",
                "--arg",
                "",
                "--env",
                "",
                "--url",
                "https://x.example/v1",
            ],
        );
        // `UpdateMcpServerResponse`: the server set, not made (it was there).
        assert_eq!(
            doc["mcp_server"]["name"],
            "projects/calm-otter/bundles/demo/mcpServers/tools"
        );
        assert_eq!(doc["applied"], true);
        assert!(doc.get("created").is_none(), "{doc}");
        let made = json(
            &library,
            &["mcp", "server", "set", "demo#mcp/fresh", "--command", "f"],
        );
        assert_eq!(made["created"], true, "{made}");
        let file = mem.text("demo", "mcp/tools.yaml").unwrap();
        assert!(
            file.contains("url: https://x.example/v1") && !file.contains("command"),
            "{file}"
        );
    }

    // mcp.feature: a bad pair is refused, the edit's round trip, and the
    // bare noun.
    #[test]
    fn test_mcp_edit_and_the_bare_noun() {
        let (library, mem) = world();
        for flags in [
            &["--command", "run"][..],
            &["--command", "", "--url", "https://x.example/v1"],
        ] {
            let mut line = vec!["mcp", "server", "set", "demo#mcp/tools"];
            line.extend(flags);
            let _ = text(&library, &line);
        }
        let bad = refused_code(
            &library,
            &["mcp", "server", "set", "demo#mcp/tools", "--env", "nope"],
            TEXT,
        );
        assert!(bad.contains("flag-pair"), "{bad}");
        assert_eq!(
            text(&library, &["mcp", "server", "edit", "demo#mcp/tools"]),
            "No changes made.\n"
        );
        mem.set_editor(|_, content| {
            Ok(content.replace("https://x.example/v1", "https://y.example/v1"))
        });
        assert_eq!(
            text(&library, &["mcp", "server", "edit", "demo#mcp/tools"]),
            "Updated MCP server \"tools\" in bundle \"demo\"\n"
        );
        // Asked for by name, the edit answers `UpdateMcpServerResponse`.
        mem.set_editor(|_, content| Ok(content.to_string()));
        let ran = run_on(
            &library,
            &["mcp", "server", "edit", "demo#mcp/tools"],
            JSON_EXPLICIT,
        );
        let edited = ran.json();
        assert_eq!(edited["applied"], false);
        assert_eq!(
            edited["mcp_server"]["name"],
            "projects/calm-otter/bundles/demo/mcpServers/tools"
        );
        // json that nobody asked for (the default, off a terminal) is not json
        let unasked = How {
            explicit: None,
            terminal: false,
            degraded: false,
        };
        let said = refused_code(&library, &["mcp"], unasked);
        assert!(
            said.contains("mcp-bare") && !said.contains("mcp-bare-json"),
            "{said}"
        );
        let said = refused_code(&library, &["mcp"], TEXT);
        assert!(
            said.contains("mcp-bare") && !said.contains("mcp-bare-json"),
            "{said}"
        );
        assert!(refused_code(&library, &["mcp"], JSON).contains("mcp-bare-json"));
    }
}

// Each view's mapping to its message, field by field (OD-40): what the
// binary's tests alone reached.
mod mapping {
    use library_application::McpListRow;
    use serde_json::json;

    const PARENT: &str = "projects/p";

    fn json_of<M: serde::Serialize>(message: &M) -> serde_json::Value {
        let mut out = Vec::new();
        crate::api::print(&mut out, message).unwrap();
        serde_json::from_slice(&out).unwrap()
    }

    #[test]
    fn test_a_shown_server_is_its_whole_resource() {
        let show = library_application::McpShow {
            name: "tree".to_string(),
            found: true,
            entries: vec![library_application::McpRow {
                name: "tree".to_string(),
                command: "tree-sitter".to_string(),
                args: vec!["--stdio".to_string()],
                env: [("K".to_string(), "v".to_string())].into(),
                url: "https://mcp.example".to_string(),
                served_by: "acme".to_string(),
                source: "demo".to_string(),
                notes: "a note".to_string(),
                installation: "npm i tree".to_string(),
            }],
        };
        assert_eq!(
            json_of(&library_api::resources::servers_shown(PARENT, &show)),
            json!({"mcp_servers": [{
                "name": "projects/p/bundles/demo/mcpServers/tree",
                "command": "tree-sitter",
                "args": ["--stdio"],
                "env": {"K": "v"},
                "url": "https://mcp.example",
                "served_by": "acme",
                "notes": "a note",
                "installation": "npm i tree",
            }]})
        );
    }

    #[test]
    fn test_a_listed_server_is_its_row() {
        let list = library_application::McpList {
            servers: vec![McpListRow {
                name: "tree".to_string(),
                command: "tree-sitter".to_string(),
                args: vec!["--stdio".to_string()],
                url: "https://mcp.example".to_string(),
                served_by: "acme".to_string(),
                source: "demo".to_string(),
            }],
        };
        assert_eq!(
            json_of(&library_api::resources::servers_listed(PARENT, &list)),
            json!({"mcp_servers": [{
                "name": "projects/p/bundles/demo/mcpServers/tree",
                "command": "tree-sitter",
                "args": ["--stdio"],
                "url": "https://mcp.example",
                "served_by": "acme",
            }]})
        );
    }
}

// Step 10a U2: `show` is the listing of one ID, `list` of every one, each
// with the command's global flags.
mod asks {
    use crate::tests::{asked_once, degraded};
    use daemon_proto::v1;
    #[allow(unused_imports)]
    use daemon_proto::Carries as _;

    #[test]
    fn test_mcp_server_reads_ask_their_id_with_the_globals() {
        let (_, show) =
            asked_once::<v1::ListMcpServersRequest>(&["mcp", "server", "show", "tree-sitter"]);
        assert_eq!(show.mcp_server_id, "tree-sitter");
        assert_eq!(show.caller_globals().cloned(), degraded());
        let (_, list) = asked_once::<v1::ListMcpServersRequest>(&["mcp", "server", "list"]);
        assert_eq!(list.mcp_server_id, "");
        assert_eq!(list.caller_globals().cloned(), degraded());
    }
}

// Step 10b U3: `mcp server set` asks `UpdateMcpServer` with the fields it
// was given named by its mask (empty, a clear), allowing a missing one;
// `mcp server edit` asks no mask, previewed first, then applied with the
// preview's etag.
mod writes_asked {
    use crate::tests::{asked, asked_once, degraded};
    use daemon_proto::v1;
    #[allow(unused_imports)]
    use daemon_proto::Carries as _;

    #[test]
    fn test_mcp_server_set_asks_its_given_fields_by_mask() {
        let (under, set) = asked_once::<v1::UpdateMcpServerRequest>(&[
            "mcp",
            "server",
            "set",
            "demo#mcp/tools",
            "--command",
            "run",
            "--env",
            "A=1",
            "--url=",
            "--notes",
            "n",
        ]);
        assert_eq!(
            under,
            library_api::names::reference_target("demo#mcp/tools")
        );
        assert_eq!(
            set.update_mask.unwrap().paths,
            ["command", "env", "url", "notes"]
        );
        let server = set.mcp_server.unwrap();
        assert_eq!(server.command, "run");
        assert_eq!(server.env.get("A").map(String::as_str), Some("1"));
        assert_eq!(server.url, "");
        assert!(set.allow_missing);
        assert!(!set.validate_only);
        assert_eq!(
            set.caller.as_ref().and_then(|c| c.globals.clone()),
            degraded()
        );
    }

    #[test]
    fn test_mcp_server_edit_previews_then_applies_with_no_mask() {
        let sent =
            asked::<v1::UpdateMcpServerRequest>(&["mcp", "server", "edit", "demo#mcp/tools"]);
        assert!(!sent.is_empty());
        let (_, preview) = &sent[0];
        assert!(preview.validate_only);
        assert!(preview.update_mask.is_none());
        assert!(preview.caller_presentable().is_some_and(|p| p.can_launch));
    }
}
