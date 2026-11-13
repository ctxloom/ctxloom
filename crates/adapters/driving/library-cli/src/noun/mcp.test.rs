//! `mcp`'s tests, beside its verbs.

mod read {
    use crate::tests::*;

    // mcp.feature (moved from the four-noun test of lib.test.rs, unchanged)
    #[test]
    fn test_mcp_server_list_and_show_and_the_bare_noun() {
        let servers = json_of(&["mcp", "server", "list"]);
        assert_eq!(servers["servers"][0]["name"], "tree-sitter");
        assert_eq!(json_of(&["mcp", "server"]), servers);
        assert_eq!(
            json_of(&["mcp", "server", "show", "tree-sitter"])["found"],
            true
        );
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
                    "--tag",
                    "t1",
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
                "--header",
                "H=v",
            ],
        );
        assert_eq!(doc["status"], "updated");
        let file = mem.text("demo", "mcp/tools.yaml").unwrap();
        assert!(
            file.contains("url: https://x.example/v1") && file.contains("H: v"),
            "{file}"
        );
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
        let ran = run_on(
            &library,
            &["mcp", "server", "edit", "demo#mcp/tools"],
            JSON_EXPLICIT,
        );
        assert!(ran.refused().contains("unsupported-format"));
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
