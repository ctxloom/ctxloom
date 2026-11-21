use library_application::fakes::{Setup, ENVELOPE};
use library_application::Flags;

use super::{get_bundle, get_loadout, get_mcp_server, list_agents, list_profiles, list_skills};

const PARENT: &str = "projects/calm-otter";

const DEMO: &[(&str, &str)] = &[
    ENVELOPE,
    (
        "skills/reviewer/SKILL.md",
        "---\nname: reviewer\ndescription: Reviews code\n---\nSKILL-BODY\n",
    ),
    (
        "skills/zz-dir/SKILL.md",
        "---\nname: renamed\ndescription: Renamed\n---\nBODY\n",
    ),
    (
        "skills/reviewer/.meta.yaml",
        "tags: [\"ctxloom:link_id=pair\"]\n",
    ),
];

const PROJECT: &[(&str, &str)] = &[
    ENVELOPE,
    ("profiles/dev.yaml", "description: dev\nbundles: [demo]\n"),
];

const SERVED: &[(&str, &str)] = &[
    ENVELOPE,
    ("mcp/tree-sitter.yaml", "command: ts\nargs: [--stdio]\n"),
];

const AGENTS: &str = "schema_version: 9\nagents:\n  dev:\n    engine: claude-code\n    profiles: [dev]\ndefault_agent: dev\n";

fn library(layers: &[&str]) -> library_application::Library {
    Setup {
        bundles: &[("demo", DEMO), ("project", PROJECT), ("served", SERVED)],
        layers,
        ..Setup::default()
    }
    .library()
}

#[test]
fn the_agents_are_answered_under_the_parent_the_default_marked() {
    let listed = list_agents(
        &library(&[AGENTS]),
        &Flags::default(),
        PARENT,
        &Default::default(),
    )
    .result
    .unwrap();
    assert_eq!(listed.agents.len(), 1);
    let dev = &listed.agents[0];
    assert_eq!(dev.name, "projects/calm-otter/agents/dev");
    assert_eq!(dev.engine, "projects/calm-otter/engines/claude-code");
    assert_eq!(dev.default, Some(true));
}

#[test]
fn no_agent_is_an_empty_listing() {
    let listed = list_agents(
        &library(&["schema_version: 9\n"]),
        &Flags::default(),
        PARENT,
        &Default::default(),
    )
    .result
    .unwrap();
    assert!(listed.agents.is_empty());
}

#[test]
fn the_profiles_are_answered_below_the_bundle_that_ships_them() {
    let listed = list_profiles(
        &library(&[AGENTS]),
        &Flags::default(),
        PARENT,
        &Default::default(),
    )
    .result
    .unwrap();
    let names: Vec<&str> = listed.profiles.iter().map(|p| p.name.as_str()).collect();
    assert!(
        names.contains(&"projects/calm-otter/bundles/project/profiles/dev"),
        "{names:?}"
    );
}

#[test]
fn the_skills_are_answered_with_their_tags_below_their_bundle() {
    let listed = list_skills(
        &library(&[AGENTS]),
        &Flags::default(),
        PARENT,
        &Default::default(),
    )
    .result
    .unwrap();
    assert_eq!(listed.skills.len(), 2, "{listed:?}");
    let reviewer = &listed.skills[0];
    // Owner ruling 2026-11-17: its frontmatter name beside its id.
    assert_eq!(reviewer.display_name, "reviewer");
    assert_eq!(
        listed.skills[1].name,
        "projects/calm-otter/bundles/demo/skills/zz-dir"
    );
    assert_eq!(listed.skills[1].display_name, "renamed");
    assert_eq!(
        reviewer.name,
        "projects/calm-otter/bundles/demo/skills/reviewer"
    );
    assert!(
        reviewer.tags.iter().any(|t| t == "ctxloom:link_id=pair"),
        "{reviewer:?}"
    );
}

#[test]
fn a_bundles_server_is_answered_by_its_own_name() {
    let lib = library(&["schema_version: 9\n"]);
    let ask = |name: &str| {
        let request = daemon_proto::v1::GetMcpServerRequest {
            name: format!("{PARENT}{}", crate::names::reference_target(name)),
            ..Default::default()
        };
        get_mcp_server(&lib, &Flags::default(), PARENT, &request).result
    };
    let server = ask("served#mcp/tree-sitter").unwrap();
    assert_eq!(
        server.name,
        "projects/calm-otter/bundles/served/mcpServers/tree-sitter"
    );
    assert_eq!(server.command, "ts");
    assert_eq!(server.args, ["--stdio"]);
    // A bundle of one server answers it whatever the ID (Go's view).
    assert_eq!(ask("served#mcp/other").unwrap().command, "ts");
    assert!(matches!(
        ask("demo#mcp/none"),
        Err(library_application::AppError::ViewMissing { .. })
    ));
}

#[test]
fn a_bundle_views_item_path_is_refused_as_the_view_refuses_it() {
    let lib = library(&["schema_version: 9\n"]);
    let request = daemon_proto::v1::GetBundleRequest {
        name: format!(
            "{PARENT}{}",
            crate::names::reference_target("demo#fragments")
        ),
        view: daemon_proto::v1::bundle::View::Content as i32,
        ..Default::default()
    };
    let read = get_bundle(&lib, &Flags::default(), PARENT, &request).result;
    assert!(
        matches!(read, Err(library_application::AppError::ViewPath { .. })),
        "{read:?}"
    );
}

#[test]
fn the_loadout_is_ctxloom_s_own_answered_under_the_parent() {
    let read = get_loadout(
        &library(&[AGENTS]),
        &Flags::default(),
        PARENT,
        &Default::default(),
    );
    assert!(read.notices.is_empty());
    let loadout = read.result.unwrap();
    assert_eq!(loadout.name, "projects/calm-otter/loadout");
    let run = loadout.run.expect("its run: bundle");
    assert_eq!(run.version, "1.0.0");
    assert_eq!(run.tags, ["core", "mcp", "isolation"]);
    assert_eq!(run.author, "ctxloom");
    assert_eq!(
        run.mcp_servers,
        [daemon_proto::v1::LoadoutMcpServer {
            id: "ctxloom".into(),
            served_by: "session-endpoint".into(),
        }]
    );
}

#[test]
fn outside_a_project_the_loadout_is_the_home_s() {
    let loadout = get_loadout(
        &library(&[]),
        &Flags::default(),
        "users/me",
        &Default::default(),
    )
    .result
    .unwrap();
    assert_eq!(loadout.name, "users/me/loadout");
}

fn keys(document: &Option<daemon_proto::google::protobuf::Value>) -> Vec<String> {
    use daemon_proto::google::protobuf::value::Kind;
    match document.as_ref().and_then(|d| d.kind.as_ref()) {
        Some(Kind::StructValue(fields)) => fields.fields.keys().cloned().collect(),
        other => panic!("an object, not {other:?}"),
    }
}

#[test]
fn the_config_is_answered_whole_or_one_section_under_the_parent() {
    use daemon_proto::v1::{config::View, GetConfigRequest};
    let lib = library(&["schema_version: 9\neditor:\n  command: vim\n"]);
    let ask = |section: &str| {
        let request = GetConfigRequest {
            view: View::Effective as i32,
            section: section.to_string(),
            ..Default::default()
        };
        super::get_config(&lib, &Flags::default(), PARENT, &request)
            .result
            .unwrap()
    };
    let editor = ask("editor");
    assert_eq!(editor.name, "projects/calm-otter/config");
    assert_eq!(keys(&editor.document), ["command"]);
    let whole = keys(&ask("").document);
    assert!(whole.contains(&"schema_version".to_string()), "{whole:?}");
}

#[test]
fn only_the_set_view_asks_for_what_the_layers_set() {
    use daemon_proto::v1::{config::View, GetConfigRequest};
    let asked = |view: View| {
        super::only_set(&GetConfigRequest {
            view: view as i32,
            ..Default::default()
        })
    };
    assert!(asked(View::Set));
    assert!(!asked(View::Effective));
    assert!(!asked(View::Unspecified));
}

#[test]
fn a_read_s_flags_and_notices_are_carried_as_the_request_and_answer_say_them() {
    let flags = Flags {
        degraded: true,
        no_companions: true,
        config_set: vec!["a=b".to_string()],
        ..Flags::default()
    };
    let globals = super::globals_of(&flags);
    assert!(globals.degraded);
    assert!(globals.no_companions);
    assert_eq!(globals.config_set, ["a=b"]);
    let notice = super::notice_of(&library_application::Notice::Finding {
        text: "TEXT".to_string(),
        remedy: "REMEDY".to_string(),
    });
    assert!(!notice.code.is_empty());
    assert!(notice.text.contains("TEXT"), "{notice:?}");
}
