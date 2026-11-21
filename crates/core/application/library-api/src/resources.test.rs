use daemon_proto::google::protobuf::value::Kind;

use super::{document, engine_ref, profile_id, profile_name, skill_name};

#[test]
fn a_document_keeps_its_shape() {
    let value = document(&serde_json::json!({"allow": ["Read"], "n": 2, "on": true, "x": null}));
    let Some(Kind::StructValue(fields)) = value.kind else {
        panic!("an object");
    };
    assert!(matches!(
        fields.fields["allow"].kind,
        Some(Kind::ListValue(_))
    ));
    assert!(matches!(fields.fields["n"].kind, Some(Kind::NumberValue(n)) if n == 2.0));
    assert!(matches!(
        fields.fields["on"].kind,
        Some(Kind::BoolValue(true))
    ));
    assert!(matches!(fields.fields["x"].kind, Some(Kind::NullValue(_))));
}

#[test]
fn an_agent_that_names_no_engine_references_none() {
    assert_eq!(engine_ref("users/me", ""), "");
    assert_eq!(
        engine_ref("users/me", "opencode"),
        "users/me/engines/opencode"
    );
}

#[test]
fn a_profile_is_named_below_its_bundle_the_project_s_own_for_none() {
    assert_eq!(profile_id("demo#profiles/dev"), "dev");
    assert_eq!(profile_id("dev"), "dev");
    assert_eq!(
        profile_name("projects/p", "", "dev"),
        "projects/p/bundles/project/profiles/dev"
    );
    assert_eq!(
        profile_name("projects/p", "acme/rules", "dev"),
        "projects/p/remotes/acme/bundles/rules/profiles/dev"
    );
}

#[test]
fn a_skill_is_named_below_its_bundle() {
    assert_eq!(
        skill_name("projects/p", "demo", "reviewer"),
        "projects/p/bundles/demo/skills/reviewer"
    );
}

fn item_list(kind: &str) -> library_application::ItemList {
    library_application::ItemList {
        rows: vec![library_application::ItemRow {
            name: "one".to_string(),
            tags: vec!["go".to_string()],
            bundle: "demo".to_string(),
            reference: "demo#x/one".to_string(),
            remote: String::new(),
            bundle_label: String::new(),
        }],
        kind: kind.to_string(),
        bundle_filter: String::new(),
        total: 1,
        hint: String::new(),
    }
}

#[test]
fn a_listed_item_is_named_below_its_bundle_and_keeps_its_tags() {
    let commands = super::commands("projects/p", &item_list("command"));
    assert_eq!(
        commands.commands[0].name,
        "projects/p/bundles/demo/commands/one"
    );
    assert_eq!(commands.commands[0].tags, ["go"]);
    let fragments = super::fragments("projects/p", &item_list("fragment"));
    assert_eq!(
        fragments.fragments[0].name,
        "projects/p/bundles/demo/fragments/one"
    );
    assert_eq!(fragments.fragments[0].reference, "demo#x/one");
    assert_eq!(fragments.fragments[0].tags, ["go"]);
}

#[test]
fn a_shown_item_is_its_whole_resource() {
    let show = library_application::ItemShow {
        name: "one".to_string(),
        bundle: "demo".to_string(),
        reference: "demo#x/one".to_string(),
        content: "BODY".to_string(),
        distilled: true,
        state: library_application::DistilledState::Stale,
    };
    let command = super::command("projects/p", &show);
    assert_eq!(command.name, "projects/p/bundles/demo/commands/one");
    assert_eq!(command.reference, "demo#x/one");
    assert_eq!(command.body, "BODY");
    assert!(command.body_is_distilled);
    assert_eq!(
        command.distilled,
        daemon_proto::v1::DistilledState::Stale as i32
    );
    let fragment = super::fragment("projects/p", &show);
    assert_eq!(fragment.name, "projects/p/bundles/demo/fragments/one");
    assert_eq!(
        fragment.distilled,
        daemon_proto::v1::DistilledState::Stale as i32
    );
}

#[test]
fn a_premise_index_names_each_fragment() {
    let premises = library_application::Premises {
        instruction: "Pick.".to_string(),
        fragments: vec![
            library_application::PremiseRow {
                name: "demo#fragments/rules".to_string(),
                premise: "when testing".to_string(),
            },
            library_application::PremiseRow {
                name: "no reference".to_string(),
                premise: "p".to_string(),
            },
        ],
    };
    let index = super::premise_index("users/me", &premises);
    assert_eq!(index.name, "users/me/premiseIndex");
    assert_eq!(index.instruction, "Pick.");
    assert_eq!(
        index.fragments[0].fragment,
        "users/me/bundles/demo/fragments/rules"
    );
    assert_eq!(index.fragments[1].fragment, "no reference");
}

// step 10a U2 (owner ruling 2026-11-17): a profile's message carries its
// select_tags, fragments (an item reference by its resource name, a bare
// name as written), hooks and deny_tools
#[test]
fn a_profile_s_message_carries_its_selecting_tags_fragments_hooks_and_denied_tools() {
    use library_application::{FragmentRef, Hook, ProfileHooks, ProfileShow};
    let hook = Hook {
        event: "pre_tool".to_string(),
        name: "0".to_string(),
        order: Some(2),
        command: "lint".to_string(),
        kind: "command".to_string(),
        args: vec!["--fix".to_string()],
        timeout: 5,
        is_async: true,
        ..Hook::default()
    };
    let mut hooks = ProfileHooks::default();
    hooks.unified.insert("pre_tool".to_string(), vec![hook]);
    hooks
        .ext
        .entry("claude-code".to_string())
        .or_default()
        .insert("Notification".to_string(), vec![Hook::default()]);
    let show = ProfileShow {
        name: "dev".to_string(),
        select_tags: vec!["go".to_string()],
        fragments: vec![
            FragmentRef {
                name: "plain".to_string(),
                priority: 0,
            },
            FragmentRef {
                name: "demo#fragments/pri".to_string(),
                priority: 3,
            },
        ],
        hooks,
        deny_tools: vec!["Bash".to_string()],
        ..ProfileShow::default()
    };
    let profile = super::profile_shown("projects/p", &show);
    assert_eq!(profile.select_tags, ["go"]);
    let fragments: Vec<(&str, i64)> = profile
        .fragments
        .iter()
        .map(|f| (f.fragment.as_str(), f.priority))
        .collect();
    assert_eq!(
        fragments,
        [("plain", 0), ("projects/p/bundles/demo/fragments/pri", 3)]
    );
    assert_eq!(profile.deny_tools, ["Bash"]);
    let hooks = profile.hooks.expect("hooks");
    let pre = &hooks.unified["pre_tool"].hooks[0];
    assert_eq!(
        (pre.event.as_str(), pre.name.as_str(), pre.order),
        ("pre_tool", "0", Some(2))
    );
    assert_eq!(
        (pre.command.as_str(), pre.kind.as_str(), pre.timeout),
        ("lint", "command", 5)
    );
    assert_eq!(pre.args, ["--fix"]);
    assert!(pre.is_async);
    assert_eq!(
        hooks.ext["claude-code"].events["Notification"].hooks.len(),
        1
    );
    let none = super::profile_shown("projects/p", &ProfileShow::default());
    assert_eq!(none.hooks, None, "a profile with no hooks carries none");
}

#[test]
fn a_loadout_is_its_run_bundle_field_for_field() {
    use library_application::loadout::{OwnLoadout, OwnServer};
    let own = OwnLoadout {
        version: "2.0.0".into(),
        tags: vec!["b".into(), "a".into()],
        author: "someone".into(),
        mcp: vec![
            OwnServer {
                id: "one".into(),
                served_by: "session-endpoint".into(),
            },
            OwnServer {
                id: "two".into(),
                served_by: "elsewhere".into(),
            },
        ],
    };
    let loadout = super::loadout("projects/calm-otter", &own);
    assert_eq!(loadout.name, "projects/calm-otter/loadout");
    let run = loadout.run.unwrap();
    assert_eq!(run.version, "2.0.0");
    assert_eq!(run.tags, ["b", "a"]);
    assert_eq!(run.author, "someone");
    let servers: Vec<(&str, &str)> = run
        .mcp_servers
        .iter()
        .map(|s| (s.id.as_str(), s.served_by.as_str()))
        .collect();
    assert_eq!(servers, [("one", "session-endpoint"), ("two", "elsewhere")]);
}

// ---- step 10a mutation pass: every mapped field asserted ----

use std::collections::BTreeMap;

use daemon_proto::v1;
use library_application::{
    AgentRow, AgentSettings, AgentShow, AuthSetting, BundleList, BundleRow, BundleShow, BundleView,
    CompanionRow, CompanionState, DistilledState, EngineAgent, EngineRow, EnvKey, EnvSetting,
    FindingRow, ItemShow, Located, McpList, McpListRow, McpRow, McpShow, NotCarried,
    PermissionsSetting, ProfileList, ProfileRow, ProfileShow, RemoteFound, Requirement,
    RequirementSource, ResolvedAgent, RuntimeSetting, SearchHit, SearchResults, ShownCommand,
    ShownFragment, ShownMcp, ShownSkill, SkillFileRow, SkillList, SkillRow, SkillShow,
    SourcedValue, UsedFragment, Viewed,
};

const P: &str = "projects/p";

fn strings(words: &[&str]) -> Vec<String> {
    words.iter().map(|w| w.to_string()).collect()
}

fn listed_row() -> BundleRow {
    BundleRow {
        name: "demo".into(),
        reference: "git@x:demo".into(),
        path: "/b/demo".into(),
        origin: String::new(),
        version: "1.2.3".into(),
        description: "a demo".into(),
        tags: strings(&["go"]),
        fragment_count: 5,
        command_count: 6,
        mcp_count: 7,
        profile_count: 8,
        held: true,
        commit: "abc123".into(),
    }
}

#[test]
fn a_listed_bundle_carries_every_field_of_its_row() {
    let list = BundleList {
        rows: vec![
            listed_row(),
            BundleRow {
                name: "ctxloom".into(),
                origin: "ctxloom/builtin".into(),
                ..BundleRow::default()
            },
        ],
        hint: String::new(),
    };
    let listed = super::bundles(P, &list);
    assert_eq!(listed.bundles.len(), 2);
    let b = &listed.bundles[0];
    assert_eq!(b.name, "projects/p/bundles/demo");
    assert_eq!(b.origin, v1::bundle::Origin::Project as i32);
    assert_eq!(b.reference, "git@x:demo");
    assert_eq!(b.commit, "abc123");
    assert_eq!(b.path, "/b/demo");
    assert_eq!(b.version, "1.2.3");
    assert_eq!(b.description, "a demo");
    assert_eq!(b.tags, ["go"]);
    assert_eq!(
        (
            b.fragment_count,
            b.command_count,
            b.mcp_server_count,
            b.profile_count
        ),
        (5, 6, 7, 8)
    );
    assert_eq!(b.held, Some(true));
    let builtin = &listed.bundles[1];
    assert_eq!(builtin.origin, v1::bundle::Origin::Builtin as i32);
    assert_eq!(builtin.held, None, "a bundle not held says nothing");
}

#[test]
fn a_count_saturates_at_the_api_s_width() {
    assert_eq!(super::count(5), 5);
    assert_eq!(super::count(u64::MAX), u32::MAX);
}

fn agent_row() -> AgentRow {
    AgentRow {
        name: "dev".into(),
        engine: "claude-code".into(),
        profiles: strings(&["dev"]),
        runtime: "host".into(),
        driving: "terminal".into(),
        env: strings(&["TOKEN"]),
        session_home: "session".into(),
        engines: serde_json::json!({
            "claude-code": {"model": "opus", "effort": "high", "permissions": {"allow": ["Read"]}},
            "opencode": {}
        }),
        engine_lines: Vec::new(),
    }
}

#[test]
fn an_agent_carries_its_binding_and_its_engine_blocks() {
    let listed = super::agents(P, &[agent_row()], "other");
    let a = &listed.agents[0];
    assert_eq!(a.name, "projects/p/agents/dev");
    assert_eq!(a.engine, "projects/p/engines/claude-code");
    assert_eq!(a.profiles, ["projects/p/bundles/project/profiles/dev"]);
    assert_eq!(a.runtime, "host");
    assert_eq!(a.driving, "terminal");
    assert_eq!(a.env, ["TOKEN"]);
    assert_eq!(a.session_home, "session");
    assert_eq!(a.default, Some(false));
    let keys: Vec<&str> = a.engines.keys().map(String::as_str).collect();
    assert_eq!(keys, ["claude-code", "opencode"]);
    let claude = &a.engines["claude-code"];
    assert_eq!(
        (claude.model.as_str(), claude.effort.as_str()),
        ("opus", "high")
    );
    assert!(claude.permissions.is_some());
    let opencode = &a.engines["opencode"];
    assert_eq!(opencode.model, "");
    assert_eq!(opencode.permissions, None);
}

fn agent_settings() -> AgentSettings {
    AgentSettings {
        model: SourcedValue {
            value: Some("opus".into()),
            source: "agents.dev.engines.claude-code",
        },
        effort: SourcedValue {
            value: None,
            source: "default",
        },
        permissions: PermissionsSetting {
            value: Some(serde_json::json!({"defaultMode": "acceptEdits"})),
            source: "engines.claude-code",
            line: String::new(),
        },
        runtime: RuntimeSetting {
            value: "host".into(),
            source: "agents.dev",
            overrides: Some("container".into()),
            overrides_source: Some("runtime"),
            change: Some("weakens"),
        },
        env: EnvSetting {
            value: strings(&["A"]),
            source: "agents.dev",
            keys: vec![EnvKey {
                name: "A".into(),
                source: "engines.claude-code",
            }],
        },
        session_home: SourcedValue {
            value: "session".into(),
            source: "engines.claude-code",
        },
        approval_timeout: SourcedValue {
            value: "30s".into(),
            source: "default",
        },
    }
}

fn sourced(value: &Option<v1::SourcedText>) -> (&str, &str) {
    let text = value.as_ref().expect("a sourced value");
    (text.value.as_str(), text.source.as_str())
}

fn assert_settings(s: &v1::AgentSettings) {
    assert_eq!(
        sourced(&s.model),
        ("opus", "agents.dev.engines.claude-code")
    );
    assert_eq!(sourced(&s.effort), ("", "default"));
    let permissions = s.permissions.as_ref().unwrap();
    assert_eq!(permissions.source, "engines.claude-code");
    assert!(permissions.value.is_some());
    let runtime = s.runtime.as_ref().unwrap();
    assert_eq!(
        (
            runtime.value.as_str(),
            runtime.source.as_str(),
            runtime.overrides.as_str(),
            runtime.overrides_source.as_str(),
            runtime.change.as_str()
        ),
        ("host", "agents.dev", "container", "runtime", "weakens")
    );
    let env = s.env.as_ref().unwrap();
    assert_eq!(
        (env.value.clone(), env.source.as_str()),
        (strings(&["A"]), "agents.dev")
    );
    assert_eq!(
        (env.keys[0].variable.as_str(), env.keys[0].source.as_str()),
        ("A", "engines.claude-code")
    );
    assert_eq!(sourced(&s.session_home), ("session", "engines.claude-code"));
    assert_eq!(sourced(&s.approval_timeout), ("30s", "default"));
}

#[test]
fn an_agent_s_settings_carry_each_value_with_its_source() {
    assert_settings(&super::settings(&agent_settings()));
    assert_eq!(sourced(&super::text("v", "s")), ("v", "s"));
}

#[test]
fn a_shown_agent_carries_what_it_resolves_to_beside_its_definition() {
    let show = AgentShow {
        definition: agent_row(),
        resolved: Some(ResolvedAgent {
            name: "dev".into(),
            profiles: strings(&["dev"]),
            engine: "claude-code".into(),
            settings: agent_settings(),
            context: "CONTEXT".into(),
            fragments: strings(&["demo#fragments/a"]),
            driving: "terminal".into(),
            command: "claude".into(),
        }),
        resolution_error: "no resolve".into(),
        requirements: vec![Requirement {
            command: "rg".into(),
            sources: vec![RequirementSource {
                kind: "skill".into(),
                reference: "demo#skills/s".into(),
            }],
        }],
        requirements_error: "no requirements".into(),
        not_carried: vec![NotCarried {
            engine: "opencode".into(),
            surface: "hooks".into(),
            event: "session_end".into(),
            reason: "none native".into(),
        }],
    };
    let a = super::agent_shown(P, &show, "dev");
    assert_eq!(a.name, "projects/p/agents/dev");
    assert_eq!(a.default, Some(true));
    let r = a.resolved.as_ref().expect("resolved");
    assert_eq!(r.profiles, ["dev"]);
    assert_eq!(r.engine, "projects/p/engines/claude-code");
    assert_eq!(
        sourced(&r.model),
        ("opus", "agents.dev.engines.claude-code")
    );
    assert_eq!(r.context, "CONTEXT");
    assert_eq!(r.fragments, ["demo#fragments/a"]);
    assert_eq!(
        (r.driving.as_str(), r.command.as_str()),
        ("terminal", "claude")
    );
    assert_eq!(sourced(&r.session_home), ("session", "engines.claude-code"));
    assert_eq!(a.resolution_error, "no resolve");
    assert_eq!(a.requirements_error, "no requirements");
    assert_eq!(a.requirements[0].command, "rg");
    assert_eq!(
        (
            a.requirements[0].sources[0].kind.as_str(),
            a.requirements[0].sources[0].reference.as_str()
        ),
        ("skill", "demo#skills/s")
    );
    let lost = &a.not_carried[0];
    assert_eq!(
        (
            lost.engine.as_str(),
            lost.surface.as_str(),
            lost.event.as_str(),
            lost.reason.as_str()
        ),
        ("opencode", "hooks", "session_end", "none native")
    );
}

fn auth(value: serde_json::Value, state: &'static str) -> AuthSetting {
    AuthSetting {
        value,
        source: "engines.claude-code",
        state,
        via: Some("TOKEN".into()),
    }
}

fn engine_row() -> EngineRow {
    EngineRow {
        name: "claude-code".into(),
        command: SourcedValue {
            value: "claude".into(),
            source: "default",
        },
        session_home: SourcedValue {
            value: "session".into(),
            source: "engines.claude-code",
        },
        runtime: SourcedValue {
            value: "host".into(),
            source: "runtime",
        },
        env: SourcedValue {
            value: strings(&["A"]),
            source: "engines.claude-code",
        },
        auth: auth(serde_json::json!("token"), "present"),
        approval_timeout: SourcedValue {
            value: "30s".into(),
            source: "default",
        },
        agents: vec![EngineAgent {
            name: "dev".into(),
            settings: agent_settings(),
        }],
    }
}

#[test]
fn an_engine_carries_what_its_agents_share_and_each_agent() {
    let listed = super::engines(P, &[engine_row()]);
    let e = &listed.engines[0];
    assert_eq!(e.name, "projects/p/engines/claude-code");
    assert_eq!(sourced(&e.command), ("claude", "default"));
    assert_eq!(sourced(&e.session_home), ("session", "engines.claude-code"));
    assert_eq!(sourced(&e.runtime), ("host", "runtime"));
    let env = e.env.as_ref().unwrap();
    assert_eq!(
        (env.value.clone(), env.source.as_str()),
        (strings(&["A"]), "engines.claude-code")
    );
    assert_eq!(sourced(&e.approval_timeout), ("30s", "default"));
    let a = e.auth.as_ref().unwrap();
    assert_eq!(a.value.as_ref().unwrap().word, "token");
    let agent = &e.agents[0];
    assert_eq!(agent.agent, "dev");
    assert_eq!(
        sourced(&agent.model),
        ("opus", "agents.dev.engines.claude-code")
    );
    assert_eq!(sourced(&agent.effort), ("", "default"));
    assert_eq!(
        agent.permissions.as_ref().unwrap().source,
        "engines.claude-code"
    );
    assert_eq!(agent.runtime.as_ref().unwrap().value, "host");
    assert_eq!(agent.env.as_ref().unwrap().value, ["A"]);
    assert_eq!(
        sourced(&agent.session_home),
        ("session", "engines.claude-code")
    );
}

#[test]
fn an_engine_s_auth_is_its_word_or_its_source_and_its_state() {
    use v1::engine_auth::State;
    let word = super::engine_auth(&auth(serde_json::json!("login"), "present"));
    let value = word.value.unwrap();
    assert_eq!(
        (value.word.as_str(), value.env.len(), value.file.as_str()),
        ("login", 0, "")
    );
    assert_eq!(word.source, "engines.claude-code");
    assert_eq!(word.via, "TOKEN");
    assert_eq!(word.state, State::Present as i32);
    let source = super::engine_auth(&auth(
        serde_json::json!({"env": ["A", "B"], "file": "/k"}),
        "missing",
    ));
    let value = source.value.unwrap();
    assert_eq!(value.word, "");
    assert_eq!(value.env, ["A", "B"]);
    assert_eq!(value.file, "/k");
    assert_eq!(source.state, State::Missing as i32);
    let neither = super::engine_auth(&auth(serde_json::json!(3), "not-needed"));
    assert_eq!(neither.value.unwrap(), v1::AuthValue::default());
    assert_eq!(neither.state, State::NotNeeded as i32);
    let unknown = super::engine_auth(&AuthSetting {
        via: None,
        ..auth(serde_json::json!("x"), "odd")
    });
    assert_eq!(unknown.state, State::Unspecified as i32);
    assert_eq!(unknown.via, "");
}

fn mcp_row() -> McpRow {
    McpRow {
        name: "ts".into(),
        command: "ts".into(),
        args: strings(&["--stdio"]),
        env: BTreeMap::from([("K".to_string(), "V".to_string())]),
        url: "https://x".into(),
        served_by: "session-endpoint".into(),
        source: "demo".into(),
        notes: "NOTES".into(),
        installation: "INSTALL".into(),
    }
}

#[test]
fn a_shown_server_carries_every_field() {
    let shown = super::servers_shown(
        P,
        &McpShow {
            name: "ts".into(),
            found: true,
            entries: vec![mcp_row()],
        },
    );
    let s = &shown.mcp_servers[0];
    assert_eq!(s.name, "projects/p/bundles/demo/mcpServers/ts");
    assert_eq!(
        (s.command.as_str(), s.args.clone()),
        ("ts", strings(&["--stdio"]))
    );
    assert_eq!(s.env["K"], "V");
    assert_eq!(s.url, "https://x");
    assert_eq!(s.served_by, "session-endpoint");
    assert_eq!(
        (s.notes.as_str(), s.installation.as_str()),
        ("NOTES", "INSTALL")
    );
}

#[test]
fn a_listed_server_carries_its_listed_fields() {
    let listed = super::servers_listed(
        P,
        &McpList {
            servers: vec![McpListRow {
                name: "ts".into(),
                command: "ts".into(),
                args: strings(&["--stdio"]),
                url: "https://x".into(),
                served_by: "elsewhere".into(),
                source: "demo".into(),
            }],
        },
    );
    let s = &listed.mcp_servers[0];
    assert_eq!(s.name, "projects/p/bundles/demo/mcpServers/ts");
    assert_eq!(s.command, "ts");
    assert_eq!(s.args, ["--stdio"]);
    assert_eq!(s.url, "https://x");
    assert_eq!(s.served_by, "elsewhere");
}

#[test]
fn a_search_names_each_local_hit_and_keeps_each_remote_find() {
    let results = SearchResults {
        query: "go".into(),
        local: vec![
            SearchHit {
                kind: "fragment".into(),
                name: "demo#fragments/a".into(),
                tags: strings(&["go"]),
                source: "demo".into(),
                r#match: "name".into(),
                located: Some(Located {
                    bundle: "demo".into(),
                    item: "a".into(),
                }),
            },
            SearchHit {
                kind: "skill".into(),
                name: "lost".into(),
                tags: Vec::new(),
                source: String::new(),
                r#match: String::new(),
                located: None,
            },
        ],
        remote: vec![RemoteFound {
            remote: "acme".into(),
            name: "rules".into(),
            kind: "bundle".into(),
            tags: strings(&["t"]),
            description: "D".into(),
            author: "A".into(),
            pull_ref: "acme/rules".into(),
        }],
        count: 3,
        hidden_local: 2,
    };
    let found = super::searched(P, &results);
    assert_eq!(found.query, "go");
    assert_eq!((found.count, found.hidden_local), (3, 2));
    let hit = &found.local[0];
    assert_eq!(hit.resource, "projects/p/bundles/demo/fragments/a");
    assert_eq!(hit.kind, v1::ItemKind::Fragment as i32);
    assert_eq!(hit.tags, ["go"]);
    assert_eq!(
        (hit.source.as_str(), hit.r#match.as_str()),
        ("demo", "name")
    );
    assert_eq!(found.local[1].resource, "lost");
    let remote = &found.remote[0];
    assert_eq!(
        (remote.remote.as_str(), remote.item.as_str()),
        ("acme", "rules")
    );
    assert_eq!(remote.kind, v1::ItemKind::Bundle as i32);
    assert_eq!(remote.tags, ["t"]);
    assert_eq!(
        (remote.description.as_str(), remote.author.as_str()),
        ("D", "A")
    );
    assert_eq!(remote.pull_reference, "acme/rules");
}

#[test]
fn each_kind_word_is_its_item_kind_and_back() {
    let kinds = [
        ("fragment", v1::ItemKind::Fragment, "fragments"),
        ("command", v1::ItemKind::Command, "commands"),
        ("skill", v1::ItemKind::Skill, "skills"),
        ("profile", v1::ItemKind::Profile, "profiles"),
        ("mcp_server", v1::ItemKind::McpServer, "mcpServers"),
        ("bundle", v1::ItemKind::Bundle, "bundles"),
    ];
    for (word, kind, collection) in kinds {
        assert_eq!(super::item_kind(word), (kind, collection), "{word}");
        assert_eq!(super::kind_word(kind as i32), word);
    }
    assert_eq!(super::item_kind("odd"), (v1::ItemKind::Unspecified, ""));
    assert_eq!(super::kind_word(v1::ItemKind::Unspecified as i32), "");
}

#[test]
fn a_config_is_its_document_under_the_parent() {
    let shown = library_application::ConfigShown {
        json: serde_json::json!({"a": 1}),
        text: String::new(),
    };
    let config = super::config(P, &shown);
    assert_eq!(config.name, "projects/p/config");
    assert_eq!(
        config.document,
        Some(super::document(&serde_json::json!({"a": 1})))
    );
}

#[test]
fn a_shown_distilled_form_is_stale_fresh_or_none() {
    use v1::DistilledState as S;
    assert_eq!(super::shown_distilled(true, false), S::Fresh as i32);
    assert_eq!(super::shown_distilled(false, false), S::None as i32);
    assert_eq!(super::shown_distilled(false, true), S::Stale as i32);
}

fn shown_bundle() -> BundleShow {
    BundleShow {
        name: "demo".into(),
        path: "/shown/demo".into(),
        version: "2.0".into(),
        author: "someone".into(),
        description: "shown".into(),
        tags: strings(&["x"]),
        notes: "NOTES".into(),
        installation: "INSTALL".into(),
        fragments: BTreeMap::from([(
            "f".to_string(),
            ShownFragment {
                tags: strings(&["ft"]),
                preview: "FP".into(),
                distilled: true,
                no_distill: true,
                premise: "when".into(),
                stale: false,
            },
        )]),
        commands: BTreeMap::from([(
            "c".to_string(),
            ShownCommand {
                tags: strings(&["ct"]),
                description: "CD".into(),
                preview: "CP".into(),
                distilled: false,
                no_distill: true,
                stale: true,
            },
        )]),
        mcp: BTreeMap::from([(
            "m".to_string(),
            ShownMcp {
                command: "mc".into(),
                args: strings(&["-a"]),
                env: BTreeMap::from([("K".to_string(), "V".to_string())]),
                notes: "MN".into(),
                installation: "MI".into(),
            },
        )]),
        skills: BTreeMap::from([(
            "s".to_string(),
            ShownSkill {
                path: "skills/s".into(),
                tags: strings(&["st"]),
                notes: "SN".into(),
            },
        )]),
        profiles: strings(&["dev"]),
        hooks: strings(&["pre_tool"]),
        listing: BundleRow {
            name: "ignored".into(),
            ..listed_row()
        },
    }
}

#[test]
fn a_shown_bundle_carries_its_listing_and_its_own_fields() {
    let b = super::bundle_shown(P, &shown_bundle());
    assert_eq!(b.name, "projects/p/bundles/demo");
    assert_eq!(b.commit, "abc123", "the listing's fields stay");
    assert_eq!(b.fragment_count, 5);
    assert_eq!((b.author.as_str(), b.notes.as_str()), ("someone", "NOTES"));
    assert_eq!(b.installation, "INSTALL");
    assert_eq!(
        (b.description.as_str(), b.version.as_str()),
        ("shown", "2.0")
    );
    assert_eq!(b.tags, ["x"]);
    assert_eq!(
        b.path, "/shown/demo",
        "the show's own path, not its listing's"
    );
    assert_eq!(b.skill_count, 1);
    assert_eq!(b.profiles, ["projects/p/bundles/demo/profiles/dev"]);
    assert_eq!(b.hooks, ["pre_tool"]);
}

#[test]
fn a_shown_bundle_s_fragments_and_commands_carry_every_field() {
    let b = super::bundle_shown(P, &shown_bundle());
    let f = &b.fragments[0];
    assert_eq!(f.item, "projects/p/bundles/demo/fragments/f");
    assert_eq!(f.tags, ["ft"]);
    assert_eq!(f.preview, "FP");
    assert_eq!(f.distilled, v1::DistilledState::Fresh as i32);
    assert_eq!(f.no_distill, Some(true));
    assert_eq!(f.premise, "when");
    let c = &b.commands[0];
    assert_eq!(c.item, "projects/p/bundles/demo/commands/c");
    assert_eq!(c.tags, ["ct"]);
    assert_eq!((c.description.as_str(), c.preview.as_str()), ("CD", "CP"));
    assert_eq!(c.distilled, v1::DistilledState::Stale as i32);
    assert_eq!(c.no_distill, Some(true));
}

#[test]
fn a_shown_bundle_s_skills_and_servers_carry_every_field() {
    let b = super::bundle_shown(P, &shown_bundle());
    let s = &b.skills[0];
    assert_eq!(s.item, "projects/p/bundles/demo/skills/s");
    assert_eq!(s.tags, ["st"]);
    assert_eq!((s.notes.as_str(), s.path.as_str()), ("SN", "skills/s"));
    let m = &b.mcp_servers[0];
    assert_eq!(m.item, "projects/p/bundles/demo/mcpServers/m");
    assert_eq!(
        (m.command.as_str(), m.args.clone()),
        ("mc", strings(&["-a"]))
    );
    assert_eq!(m.env["K"], "V");
    assert_eq!((m.notes.as_str(), m.installation.as_str()), ("MN", "MI"));
}

#[test]
fn a_viewed_bundle_carries_its_files_in_order() {
    let view = |viewed| BundleView {
        bundle: "demo".into(),
        path: String::new(),
        distilled: true,
        content: String::new(),
        item: false,
        viewed,
    };
    let b = super::bundle_viewed(
        P,
        &view(Viewed::Files(vec![
            ("bundle.yaml".into(), "M".into()),
            ("a.md".into(), "A".into()),
        ])),
    );
    assert_eq!(b.name, "projects/p/bundles/demo");
    assert_eq!(b.distilled, Some(true));
    let files: Vec<(&str, &str)> = b
        .files
        .iter()
        .map(|f| (f.path.as_str(), f.content.as_str()))
        .collect();
    assert_eq!(files, [("bundle.yaml", "M"), ("a.md", "A")]);
    assert!(super::bundle_viewed(P, &view(Viewed::Nothing))
        .files
        .is_empty());
}

fn a_plan() -> library_application::DryRun {
    let library = library_application::fakes::Setup {
        layers: &[
            "schema_version: 9\nagents:\n  dev:\n    engine: claude-code\ndefault_agent: dev\n",
        ],
        ..library_application::fakes::Setup::default()
    }
    .library();
    let previewed = library
        .dry_run(
            &library_application::Flags::default(),
            &library_application::RunRequest::default(),
        )
        .result
        .expect("a dry run");
    let mut plan = previewed.plan;
    plan.agent = "dev".into();
    plan.engine = "claude-code".into();
    plan.profiles = strings(&["dev", ""]);
    plan.context = "CONTEXT".into();
    plan.tokens = 42;
    plan.findings = vec![FindingRow {
        kind: "k".into(),
        text: "T".into(),
        remedy: "R".into(),
        fatal: true,
    }];
    plan.settings = Some(agent_settings());
    plan.settings_agent = "dev".into();
    plan.workspace = "worktree".into();
    plan.runtime = "host".into();
    plan.axes = ("none".into(), "container".into());
    plan.prompt = "PROMPT".into();
    plan.fragments = strings(&["demo#fragments/a"]);
    plan
}

#[test]
fn a_planned_run_carries_the_plan_named_under_its_parent() {
    let run = super::planned(Some(P), &a_plan());
    assert_eq!(run.name, "projects/p/runs/-");
    assert_eq!(run.agent, "projects/p/agents/dev");
    assert_eq!(run.engine, "projects/p/engines/claude-code");
    assert_eq!(
        run.profiles,
        ["projects/p/bundles/project/profiles/dev", ""],
        "an empty text is named nothing"
    );
    assert_eq!(run.context, "CONTEXT");
    assert_eq!(run.tokens, 42);
    let f = &run.findings[0];
    assert_eq!(
        (f.kind.as_str(), f.text.as_str(), f.remedy.as_str(), f.fatal),
        ("k", "T", "R", true)
    );
    assert_settings(run.settings.as_ref().unwrap());
    assert_eq!(run.settings_agent, "dev");
    assert_eq!(
        (run.workspace.as_str(), run.runtime.as_str()),
        ("worktree", "host")
    );
    assert_eq!(
        (run.settled_workspace.as_str(), run.settled_runtime.as_str()),
        ("none", "container")
    );
    assert_eq!(run.prompt, "PROMPT");
    assert_eq!(run.fragments, ["demo#fragments/a"]);
    let mut nameless = a_plan();
    nameless.engine = String::new();
    assert_eq!(super::planned(Some(P), &nameless).engine, "");
}

#[test]
fn a_planned_run_with_no_parent_keeps_its_configured_text() {
    let run = super::planned(None, &a_plan());
    assert_eq!(run.name, "");
    assert_eq!(run.agent, "dev");
    assert_eq!(run.engine, "claude-code");
}

#[test]
fn a_listed_profile_carries_every_field_of_its_row() {
    let list = ProfileList {
        rows: vec![ProfileRow {
            name: "acme/rules#profiles/dev".into(),
            display_name: "dev".into(),
            description: "D".into(),
            parents: strings(&["base"]),
            tags: strings(&["go"]),
            bundles: strings(&["demo"]),
            default: true,
            path: "/p/dev.yaml".into(),
            is_remote: true,
            bundle: "rules".into(),
            entry: "acme/rules".into(),
        }],
        hint: String::new(),
    };
    let p = &super::profiles(P, &list).profiles[0];
    assert_eq!(p.name, "projects/p/remotes/acme/bundles/rules/profiles/dev");
    assert_eq!(p.description, "D");
    assert_eq!(p.parents, ["projects/p/bundles/project/profiles/base"]);
    assert_eq!(p.tags, ["go"]);
    assert_eq!(p.bundles, ["projects/p/bundles/demo"]);
    assert_eq!(p.default, Some(true));
    assert_eq!(p.path, "/p/dev.yaml");
}

#[test]
fn a_shown_profile_carries_every_field() {
    let show = ProfileShow {
        name: "dev".into(),
        description: "D".into(),
        parents: strings(&["base"]),
        tags: strings(&["go"]),
        bundles: strings(&["demo"]),
        variables: BTreeMap::from([("V".to_string(), "1".to_string())]),
        exclude_fragments: strings(&["demo#fragments/x"]),
        exclude_mcp: strings(&["ts"]),
        path: "/p/dev.yaml".into(),
        default: true,
        ..ProfileShow::default()
    };
    let p = super::profile_shown(P, &show);
    assert_eq!(p.name, "projects/p/bundles/project/profiles/dev");
    assert_eq!(p.description, "D");
    assert_eq!(p.parents, ["projects/p/bundles/project/profiles/base"]);
    assert_eq!(p.tags, ["go"]);
    assert_eq!(p.bundles, ["projects/p/bundles/demo"]);
    assert_eq!(p.variables["V"], "1");
    assert_eq!(p.exclude_fragments, ["demo#fragments/x"]);
    assert_eq!(p.exclude_mcp_servers, ["ts"]);
    assert_eq!(p.path, "/p/dev.yaml");
    assert_eq!(p.default, Some(true));
}

#[test]
fn a_profile_with_unified_hooks_alone_carries_them() {
    let mut hooks = library_application::ProfileHooks::default();
    hooks.unified.insert(
        "pre_tool".to_string(),
        vec![library_application::Hook::default()],
    );
    let mapped = super::profile_hooks(&hooks).expect("hooks");
    assert_eq!(mapped.unified["pre_tool"].hooks.len(), 1);
    assert!(mapped.ext.is_empty());
}

#[test]
fn a_skill_carries_every_field_its_files_executable_by_their_mode() {
    let show = SkillShow {
        name: "reviewer".into(),
        bundle: "demo".into(),
        description: "Reviews".into(),
        license: "MIT".into(),
        compatibility: "any".into(),
        allowed_tools: strings(&["Read"]),
        body: "BODY".into(),
        files: vec![
            SkillFileRow {
                path: "run.sh".into(),
                sha256: "aa".into(),
                mode: "0755".into(),
            },
            SkillFileRow {
                path: "SKILL.md".into(),
                sha256: "bb".into(),
                mode: "0644".into(),
            },
        ],
    };
    let s = super::skill(P, &show);
    assert_eq!(s.name, "projects/p/bundles/demo/skills/reviewer");
    assert_eq!(s.description, "Reviews");
    assert_eq!(
        (s.license.as_str(), s.compatibility.as_str()),
        ("MIT", "any")
    );
    assert_eq!(s.allowed_tools, ["Read"]);
    assert_eq!(s.body, "BODY");
    assert_eq!(s.file_count, 2);
    let files: Vec<(&str, &str, bool)> = s
        .files
        .iter()
        .map(|f| (f.path.as_str(), f.sha256.as_str(), f.executable))
        .collect();
    assert_eq!(files, [("run.sh", "aa", true), ("SKILL.md", "bb", false)]);
}

#[test]
fn a_listed_skill_carries_its_description_and_file_count() {
    let list = SkillList {
        rows: vec![SkillRow {
            name: "demo/reviewer".into(),
            display_name: "reviewer".into(),
            description: "Reviews".into(),
            tags: strings(&["t"]),
            source: "demo".into(),
            file_count: 3,
        }],
        bundle_filter: String::new(),
        total: 1,
    };
    let s = &super::skills(P, &list).skills[0];
    assert_eq!(s.name, "projects/p/bundles/demo/skills/reviewer");
    assert_eq!(s.description, "Reviews");
    assert_eq!(s.file_count, 3);
}

#[test]
fn a_companion_is_named_under_the_parent_with_its_state() {
    let rows = [
        CompanionRow {
            name: "lsp".into(),
            program: "lsp-bin".into(),
            exists: true,
            state: CompanionState::Disabled,
        },
        CompanionRow {
            name: "other".into(),
            program: "o".into(),
            exists: false,
            state: CompanionState::Enabled,
        },
    ];
    let listed = super::companions(P, &rows);
    let c = &listed.companions[0];
    assert_eq!(c.name, "projects/p/companions/lsp");
    assert_eq!(c.bin, "lsp-bin");
    assert_eq!(c.resolves, Some(true));
    assert_eq!(c.state, v1::companion::State::Disabled as i32);
    assert_eq!(
        listed.companions[1].state,
        v1::companion::State::Enabled as i32
    );
    assert_eq!(
        super::companion_name("users/me", "x"),
        "users/me/companions/x"
    );
}

#[test]
fn a_listed_command_keeps_its_reference_and_a_fragment_its_body() {
    let commands = super::commands(P, &item_list("command"));
    assert_eq!(commands.commands[0].reference, "demo#x/one");
    let fragment = super::fragment(
        P,
        &ItemShow {
            name: "one".into(),
            bundle: "demo".into(),
            reference: "demo#fragments/one".into(),
            content: "BODY".into(),
            distilled: true,
            state: DistilledState::Fresh,
        },
    );
    assert_eq!(fragment.reference, "demo#fragments/one");
    assert_eq!(fragment.body, "BODY");
    assert!(fragment.body_is_distilled);
}

#[test]
fn an_assembly_names_what_it_used_and_keeps_what_it_did_not_find() {
    let assembled = library_application::Assembled {
        profiles: strings(&["dev", "lost"]),
        context: "CONTEXT".into(),
        fragments_loaded: strings(&["demo#fragments/a", "b"]),
        missing_fragments: strings(&["gone"]),
        missing_tags: strings(&["nope"]),
        profiles_located: vec![
            Some(Located {
                bundle: "project".into(),
                item: "dev".into(),
            }),
            None,
        ],
        used: vec![
            UsedFragment {
                located: Some(Located {
                    bundle: "demo".into(),
                    item: "a".into(),
                }),
                distilled: true,
            },
            UsedFragment::default(),
        ],
        tokens: 7,
        premised: vec![library_application::premise::Entry {
            name: "demo#fragments/p".into(),
            premise: "when".into(),
        }],
    };
    let a = super::assembled(P, &assembled);
    assert_eq!(a.context, "CONTEXT");
    assert_eq!(a.tokens, 7);
    let fragments: Vec<(&str, i32)> = a
        .fragments
        .iter()
        .map(|f| (f.fragment.as_str(), f.form))
        .collect();
    use v1::assembled_fragment::Form;
    assert_eq!(
        fragments,
        [
            (
                "projects/p/bundles/demo/fragments/a",
                Form::Distilled as i32
            ),
            ("b", Form::Original as i32)
        ]
    );
    assert_eq!(
        a.profiles,
        ["projects/p/bundles/project/profiles/dev", "lost"]
    );
    assert_eq!(a.missing_fragments, ["gone"]);
    assert_eq!(a.missing_tags, ["nope"]);
    assert_eq!(
        (
            a.premised[0].fragment.as_str(),
            a.premised[0].premise.as_str()
        ),
        ("demo#fragments/p", "when")
    );
}
