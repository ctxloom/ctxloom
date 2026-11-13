//! The library's read side for `mcp` (S1), ported from Go's acceptance
//! scenarios as library_cli.test.rs describes; one file per noun (design
//! §8.4), its harness library_cli.test.rs's.

use super::library_cli::*;

// ---------------------------------------------------------------- cli/mcp.feature

// mcp.feature: Scenario: (the listing of a bundle's server, j000400 in the
// same feature): the servers a profile's bundles ship are listed with their
// command
#[test]
fn test_mcp_server_list_names_the_servers_the_default_agents_bundles_ship() {
    let project = Project::initialized().on_gos_catalog();
    project.bundle("demo");
    project.write(
        ".ctxloom/content/bundles/v2/demo/mcp/toolserver.yaml",
        "command: j000400-mcp-tool-9e1b52\n",
    );
    project.profile("dev", "demo");
    project.write(
        ".ctxloom/config.yaml",
        "schema_version: 9\nagents:\n  developer:\n    profiles: [dev]\ndefault_agent: developer\n",
    );
    let ran = project.run(&["mcp", "server", "list"]);
    ran.succeeds();
    assert_eq!(
        reports_as(
            &ran.json(),
            "mcp_servers[name$=/bundles/demo/mcpServers/toolserver].command"
        ),
        "j000400-mcp-tool-9e1b52"
    );
}

// mcp.feature: Scenario Outline: The config-level write leaves do not exist
#[test]
fn test_the_config_level_mcp_write_leaves_do_not_exist() {
    for leaf in ["create", "remove"] {
        let project = Project::initialized().on_gos_catalog();
        project
            .run(&["mcp", "server", leaf, "tools"])
            .fails()
            .contains("unknown command");
    }
}

// mcp.feature: Scenario: A native mcp: block fails the load and names the key
#[test]
fn test_a_native_mcp_block_fails_the_load_and_names_the_key() {
    let project = Project::initialized().on_gos_catalog();
    project.write(
        ".ctxloom/config.yaml",
        "schema_version: 9\nmcp:\n    auto_register_ctxloom: true\n    servers:\n        tools:\n            command: echo\n",
    );
    project
        .run(&["mcp", "server", "list"])
        .fails()
        .contains("unknown key `mcp`");
}
