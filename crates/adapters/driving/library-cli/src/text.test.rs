//! The text forms, held to Go's renderers (`internal/adapters/cli`: the
//! `render*` and `print*` functions named on each test) and the goldens of
//! `crates/root/bin/ctxloom/fixtures/golden`.

use library_application::{
    AgentRow, AgentShow, BundleList, BundleRow, ItemList, ItemRow, ProfileList, ProfileRow,
    ResolvedAgent, SkillList, SkillRow,
};

use super::*;

fn shown(write: impl FnOnce(&mut Vec<u8>) -> io::Result<()>) -> String {
    let mut out = Vec::new();
    write(&mut out).unwrap();
    String::from_utf8(out).unwrap()
}

fn bundle(name: &str, fragments: u64, commands: u64, mcp: u64) -> BundleRow {
    BundleRow {
        commit: String::new(),
        name: name.to_string(),
        reference: format!("ctxloom+local:{name}"),
        path: String::new(),
        origin: String::new(),
        version: "1.2.0".to_string(),
        description: "demo bundle".to_string(),
        tags: vec!["sample".to_string()],
        fragment_count: fragments,
        command_count: commands,
        mcp_count: mcp,
        profile_count: 0,
        held: false,
    }
}

// bundle_list.go renderBundleList / bundleContentParts: one is singular, more
// are plural; golden bundle-list-text.
#[test]
fn test_the_bundle_listing_pluralizes_what_a_bundle_contains() {
    let list = BundleList {
        rows: vec![bundle("demo", 4, 1, 2), bundle("style", 1, 0, 0)],
        no_root: false,
        hint: String::new(),
    };
    let text = shown(|out| bundle_list(out, &list));
    assert!(text.starts_with("Installed bundles (2):\n\n  demo (v1.2.0)\n    demo bundle\n"));
    assert!(
        text.contains("    Contains: 4 fragments, 1 command, 2 MCP servers\n    Tags: sample\n")
    );
    assert!(text.contains("    Contains: 1 fragment\n"));
}

// bundle_list.go runBundleList: three empties, three words (goldens
// empty-bundle-list-text, emptybundles-bundle-list-text, noproject-...).
#[test]
fn test_an_empty_bundle_listing_says_why() {
    let say = |no_root: bool, hint: &str| {
        shown(|out| {
            bundle_list(
                out,
                &BundleList {
                    rows: Vec::new(),
                    no_root,
                    hint: hint.to_string(),
                },
            )
        })
    };
    assert_eq!(
        say(true, "Add remote bundles"),
        "No bundles directory found. Create one with: mkdir -p .ctxloom/content/bundles\n"
    );
    assert_eq!(
        say(
            true,
            "No project here: run 'ctxloom init' to set one up with its default content."
        ),
        "No project here: run 'ctxloom init' to set one up with its default content.\n"
    );
    assert_eq!(
        say(false, "Add remote bundles"),
        "No bundles installed.\nAdd remote bundles\n"
    );
}

// item_list.go printItemInfos / listItems: grouped by bundle, tags in
// brackets; the empty forms (goldens fragment-list-text, empty-command-list-text).
#[test]
fn test_an_item_listing_groups_by_bundle_and_an_empty_one_says_so() {
    let row = |bundle: &str, name: &str, tags: &[&str]| ItemRow {
        name: name.to_string(),
        tags: tags.iter().map(ToString::to_string).collect(),
        bundle: bundle.to_string(),
        reference: String::new(),
        remote: String::new(),
        bundle_label: bundle.to_string(),
    };
    let list = ItemList {
        rows: vec![
            row("demo", "dist", &["sample", "x"]),
            row("demo", "golang", &[]),
            row("rules", "one", &[]),
        ],
        kind: "fragment".to_string(),
        bundle_filter: String::new(),
        total: 3,
        hint: String::new(),
    };
    assert_eq!(
        shown(|out| item_list(out, &list)),
        "Fragments (3):\n\n  demo:\n    - dist [sample, x]\n    - golang\n\n  rules:\n    - one\n"
    );
    let none = ItemList {
        rows: Vec::new(),
        kind: "command".to_string(),
        bundle_filter: String::new(),
        total: 0,
        hint: "the hint".to_string(),
    };
    assert_eq!(
        shown(|out| item_list(out, &none)),
        "No commands found.\nthe hint\n"
    );
    let filtered = ItemList {
        bundle_filter: "demo".to_string(),
        total: 4,
        ..none
    };
    assert_eq!(
        shown(|out| item_list(out, &filtered)),
        "No commands in bundle \"demo\" (4 commands exist in other bundles).\n"
    );
}

// skill_cmd.go printSkillList: the description, the file count, the tags.
#[test]
fn test_a_skill_listing_shows_description_files_and_tags() {
    let list = SkillList {
        rows: vec![SkillRow {
            name: "demo/reviewer".to_string(),
            description: "SKILL-MARKER".to_string(),
            tags: vec!["sample".to_string(), "quality".to_string()],
            source: "demo".to_string(),
            file_count: 2,
        }],
        bundle_filter: String::new(),
        total: 1,
    };
    assert_eq!(
        shown(|out| skill_list(out, &list)),
        "Skills (1):\n\n  demo:\n    - demo/reviewer: SKILL-MARKER [2 file(s)] (sample, quality)\n"
    );
    let empty = SkillList {
        rows: Vec::new(),
        bundle_filter: "x".to_string(),
        total: 1,
    };
    assert!(shown(|out| skill_list(out, &empty)).starts_with("No skills found in bundle \"x\"."));
}

// profile.go renderProfileList: the default marker, the source bundle, the
// parents and the bundle count on one line.
#[test]
fn test_a_profile_listing_summarizes_each_profile() {
    let list = ProfileList {
        rows: vec![ProfileRow {
            entry: String::new(),
            name: "dev".to_string(),
            display_name: "dev".to_string(),
            description: "dev profile".to_string(),
            parents: vec!["base".to_string()],
            tags: Vec::new(),
            bundles: vec!["demo".to_string()],
            default: true,
            path: String::new(),
            is_remote: false,
            bundle: "ctxloom+local:project".to_string(),
        }],
        hint: String::new(),
    };
    assert_eq!(
        shown(|out| profile_list(out, &list)),
        "Profiles (1):\n  dev (default)\n    dev profile\n    from bundle: ctxloom+local:project, parents: base, 1 bundles\n"
    );
}

fn agent_row(engine: &str) -> AgentRow {
    AgentRow {
        name: "dev".to_string(),
        engine: engine.to_string(),
        profiles: vec!["p1".to_string(), "p2".to_string()],
        runtime: "host".to_string(),
        driving: String::new(),
        env: Vec::new(),
        engines: serde_json::json!({}),
        engine_lines: Vec::new(),
    }
}

fn line(engine: &str, line: &str, permissions: bool) -> library_application::EngineLine {
    library_application::EngineLine {
        engine: st(engine),
        line: st(line),
        permissions,
    }
}

// Ruling 67: the listing shows each engine block as the config holds it,
// `engines.<e>: {...}`; ruling 139: a block holding permissions says they are
// passed to that engine verbatim and unchecked. No neutral posture (134).
#[test]
fn test_an_agent_listing_shows_the_engine_blocks_in_the_config_s_shape() {
    let row = AgentRow {
        engine_lines: vec![
            line(
                "claude-code",
                "engines.claude-code: {model: haiku, permissions: {defaultMode: plan}}",
                true,
            ),
            line("opencode", "engines.opencode: {effort: high}", false),
        ],
        ..agent_row("claude-code")
    };
    assert_eq!(
        shown(|out| agent_list(out, &[row])),
        "Agents (1):\n  dev (engine: claude-code)\n    profiles: p1, p2\n    runtime: host\n    engines.claude-code: {model: haiku, permissions: {defaultMode: plan}}\n      permissions passed to claude-code verbatim; ctxloom does not check it\n    engines.opencode: {effort: high}\n"
    );
}

// An agent show that did not resolve prints the blocks as written, and says
// of one with permissions that they are passed on unchecked.
#[test]
fn test_an_unresolved_agent_show_says_its_permissions_are_passed_verbatim() {
    let show = AgentShow {
        definition: AgentRow {
            engine_lines: vec![line(
                "opencode",
                "engines.opencode: {permissions: {edit: deny}}",
                true,
            )],
            ..agent_row("opencode")
        },
        resolved: None,
        resolution_error: st("nope"),
        requirements: Vec::new(),
        requirements_error: String::new(),
        not_carried: Vec::new(),
    };
    let text = shown(|out| agent_show(out, &show));
    assert!(
        text.contains("engines.opencode: {permissions: {edit: deny}}\n  permissions passed to opencode verbatim; ctxloom does not check it\n"),
        "{text}"
    );
}

// Ruling 66: an env one layer said whole is said once; a merged one names
// each name's layer.
#[test]
fn test_an_agent_s_env_line_names_each_layer_when_they_differ() {
    use library_application::{EnvKey, EnvSetting};
    let key = |name: &str, source: &'static str| EnvKey {
        name: name.to_string(),
        source,
    };
    let env = |keys: Vec<EnvKey>, source: &'static str| EnvSetting {
        value: keys.iter().map(|k| k.name.clone()).collect(),
        source,
        keys,
    };
    assert_eq!(env_line(&env(Vec::new(), "default")), "env: none (default)");
    assert_eq!(
        env_line(&env(vec![key("A", "engine"), key("B", "engine")], "engine")),
        "env: A, B (engine)"
    );
    assert_eq!(
        env_line(&env(vec![key("A", "engine"), key("B", "agent")], "agent")),
        "env: A (engine), B (agent)"
    );
}

// agent.go renderAgentList: an agent with no engine says the project default.
#[test]
fn test_an_agent_listing_says_project_default_for_no_engine() {
    assert_eq!(
        shown(|out| agent_list(out, &[agent_row("")])),
        "Agents (1):\n  dev (engine: default)\n    profiles: p1, p2\n    runtime: host\n"
    );
    assert!(shown(|out| agent_list(out, &[])).starts_with("No agents defined.\n"));
}

// The capability-loss report: what the agent's engine cannot carry of its
// profiles, one line each, named by the engine.
#[test]
fn test_an_agent_show_names_what_its_engine_cannot_carry() {
    let show = AgentShow {
        definition: agent_row("opencode"),
        resolved: None,
        resolution_error: st("nope"),
        requirements: Vec::new(),
        requirements_error: String::new(),
        not_carried: vec![library_application::NotCarried {
            engine: st("opencode"),
            surface: st("hooks"),
            event: st("session_end"),
            reason: st("opencode has no native session_end event"),
        }],
    };
    let text = shown(|out| agent_show(out, &show));
    assert!(
        text.ends_with("Not carried by opencode:\n  - hooks: session_end (opencode has no native session_end event)\n"),
        "{text}"
    );
}

// agent.go renderAgentShow: a resolution that failed ends the view.
#[test]
fn test_agent_show_says_a_resolution_failed_and_carries_on() {
    let show = AgentShow {
        definition: agent_row("claude-code"),
        resolved: None,
        resolution_error: "agent \"dev\": compose profiles: nope".to_string(),
        requirements: Vec::new(),
        requirements_error: String::new(),
        not_carried: Vec::new(),
    };
    let text = shown(|out| agent_show(out, &show));
    assert!(text.starts_with(
        "Agent: dev\nEngine (declared): claude-code\nRuntime: host\nProfiles:\n  - p1\n  - p2\n"
    ));
    assert!(
        text.ends_with("Resolved engine: unavailable (agent \"dev\": compose profiles: nope)\n")
    );
    let resolved = AgentShow {
        resolved: Some(ResolvedAgent {
            name: "dev".to_string(),
            profiles: Vec::new(),
            engine: "mock".to_string(),
            settings: settings(Some("o1")),
            context: String::new(),
            fragments: vec!["a".to_string()],
            driving: String::new(),
            command: String::new(),
        }),
        resolution_error: String::new(),
        ..show
    };
    let text = shown(|out| agent_show(out, &resolved));
    assert!(text.contains("Resolved engine: mock (model: o1)\n"));
    assert!(!text.contains("Resolved permissions"));
    assert!(text.contains("Session home: session (default)\nComposed fragments: 1\n"));
}

// companion.go renderCompanionList: a command whose program is not there says
// how to drop it; one that is says so; nothing is run to know.
#[test]
fn test_a_companion_whose_program_is_not_there_says_so() {
    let rows = [
        library_application::CompanionRow {
            name: "acme".to_string(),
            command: vec![
                "acme-bin".to_string(),
                "--as".to_string(),
                "acme".to_string(),
            ],
            exists: false,
        },
        library_application::CompanionRow {
            name: "ltk".to_string(),
            command: vec!["ltk".to_string()],
            exists: true,
        },
    ];
    assert_eq!(
        shown(|out| companion_list(out, &rows)),
        "acme         acme-bin --as acme           NOT FOUND \u{2014} fix the command, or: ctxloom companion remove acme --yes\nltk          ltk                          found\n"
    );
    assert_eq!(
        shown(|out| companion_list(out, &[])),
        "No companions registered.\nUse 'ctxloom companion add <name> --command <program> [args...]' to register one.\n"
    );
}

fn st(s: &str) -> String {
    s.to_string()
}

fn sv(items: &[&str]) -> Vec<String> {
    items.iter().map(|s| st(s)).collect()
}

fn mcp_shown(notes: &str, installation: &str) -> library_application::ShownMcp {
    library_application::ShownMcp {
        command: st("srv"),
        args: sv(&["--a", "--b"]),
        env: [(st("K"), st("V"))].into_iter().collect(),
        notes: st(notes),
        installation: st(installation),
    }
}

fn bundle_shown() -> BundleShow {
    BundleShow {
        listing: BundleRow::default(),
        name: st("demo"),
        path: st("/p/bundle.yaml"),
        version: st("1.0.0"),
        author: st("Ann"),
        description: st("A demo"),
        tags: sv(&["x", "y"]),
        notes: st("bundle notes"),
        installation: String::new(),
        fragments: [(
            st("frag"),
            library_application::ShownFragment {
                tags: sv(&["t"]),
                preview: st("preview"),
                distilled: true,
                no_distill: false,
                premise: String::new(),
            },
        )]
        .into_iter()
        .collect(),
        commands: [(
            st("cmd"),
            library_application::ShownCommand {
                tags: Vec::new(),
                description: st("does it"),
                preview: String::new(),
                distilled: false,
                no_distill: true,
            },
        )]
        .into_iter()
        .collect(),
        mcp: [(st("srv"), mcp_shown("n1", "i1"))].into_iter().collect(),
        skills: Default::default(),
        profiles: Vec::new(),
        hooks: Vec::new(),
    }
}

// bundle_show.go renderBundleShow: every optional line appears when it has
// text, in order, and each publisher body is noted when it was altered.
#[test]
fn test_a_bundle_show_prints_every_part_it_has_in_order() {
    let mut notes = Notes(Vec::new());
    let text = shown(|out| bundle_show(out, &mut notes, &bundle_shown()));
    assert_eq!(
        text,
        "Bundle: demo\nVersion: 1.0.0\nAuthor: Ann\nDescription: A demo\nTags: x, y\nPath: /p/bundle.yaml\n\n\
MCP Servers (1):\n  - srv\n      Command: srv\n      Args: --a --b\n      Env:\n        K=V\n      Notes:\n        n1\n      Installation:\n        i1\n\n\
Fragments (1):\n  - frag [t] (distilled)\n      preview\n\n\
Commands (1):\n  - cmd (no_distill)\n      does it\n\n\
Notes:\n  bundle notes\n"
    );
    assert!(notes.0.is_empty());
}

#[test]
fn test_a_bundle_show_leaves_out_what_it_does_not_have() {
    let bare = BundleShow {
        listing: BundleRow::default(),
        name: st("bare"),
        path: st("/p/bundle.yaml"),
        version: String::new(),
        author: String::new(),
        description: String::new(),
        tags: Vec::new(),
        notes: String::new(),
        installation: String::new(),
        fragments: Default::default(),
        commands: Default::default(),
        mcp: [(
            st("plain"),
            library_application::ShownMcp {
                command: st("srv"),
                args: Vec::new(),
                env: Default::default(),
                notes: String::new(),
                installation: String::new(),
            },
        )]
        .into_iter()
        .collect(),
        skills: Default::default(),
        profiles: Vec::new(),
        hooks: Vec::new(),
    };
    let mut notes = Notes(Vec::new());
    assert_eq!(
        shown(|out| bundle_show(out, &mut notes, &bare)),
        "Bundle: bare\nPath: /p/bundle.yaml\n\nMCP Servers (1):\n  - plain\n      Command: srv\n\n"
    );
    let mut command_without_text = bundle_shown();
    command_without_text
        .commands
        .get_mut("cmd")
        .unwrap()
        .description = String::new();
    let text = shown(|out| bundle_show(out, &mut notes, &command_without_text));
    assert!(text.contains("  - cmd (no_distill)\n\nNotes:"));
}

#[test]
fn test_an_altered_publisher_body_is_noted_by_what_it_names() {
    let mut show = bundle_shown();
    show.notes = st("a\u{1b}b");
    show.mcp.get_mut("srv").unwrap().notes = st("n\u{7}");
    show.mcp.get_mut("srv").unwrap().installation = st("i\u{7}");
    let mut notes = Notes(Vec::new());
    let text = shown(|out| bundle_show(out, &mut notes, &show));
    assert!(text.contains("        n\u{27e8}U+0007\u{27e9}\n"));
    assert_eq!(notes.0.len(), 3);
    assert!(notes.0[0].starts_with("srv notes is publisher-authored"));
    assert!(notes.0[1].starts_with("srv installation is publisher-authored"));
    assert!(notes.0[2].starts_with("demo notes is publisher-authored"));
}

// bundle_view.go: an item collapses runs of blank lines, a whole bundle dump
// does not.
#[test]
fn test_a_view_collapses_blank_runs_only_for_one_item() {
    let content = "a\n\n\n\n\n\n\nb\n";
    let view = |item| BundleView {
        viewed: Default::default(),
        bundle: st("demo"),
        path: String::new(),
        distilled: false,
        content: st(content),
        item,
    };
    let mut notes = Notes(Vec::new());
    let one = shown(|out| bundle_view(out, &mut notes, &view(true), "demo#x"));
    assert_eq!(one, "a\n\n\n\nb\n");
    assert_eq!(notes.0.len(), 1);
    assert!(notes.0[0].contains("3 blank line(s) collapsed"));
    let mut notes = Notes(Vec::new());
    let all = shown(|out| bundle_view(out, &mut notes, &view(false), "demo"));
    assert_eq!(all, "a\n\n\n\n\n\n\nb\n");
    assert!(notes.0.is_empty());
}

// item_show.go: the distilled marker, the name, then the body.
#[test]
fn test_an_item_show_prints_its_marker_name_and_body() {
    let show = |distilled| ItemShow {
        bundle: String::new(),
        name: st("golang"),
        reference: st("demo#fragments/golang"),
        content: st("BODY\n\n\n\n\n\n\nEND"),
        distilled,
    };
    let mut notes = Notes(Vec::new());
    assert_eq!(
        shown(|out| item_show(out, &mut notes, &show(false))),
        "golang\n\nBODY\n\n\n\nEND\n"
    );
    assert_eq!(notes.0.len(), 1);
    assert_eq!(
        shown(|out| item_show(out, &mut notes, &show(true))),
        "# (distilled version)\ngolang\n\nBODY\n\n\n\nEND\n"
    );
}

// premises.go: the instruction alone when nothing is premised; otherwise a
// line per fragment.
#[test]
fn test_the_premises_print_the_instruction_then_a_line_each() {
    let none = Premises {
        instruction: st("INSTRUCTION"),
        fragments: Vec::new(),
    };
    assert_eq!(shown(|out| premises(out, &none)), "INSTRUCTION\n");
    let some = Premises {
        instruction: st("INSTRUCTION"),
        fragments: vec![library_application::PremiseRow {
            name: st("b#fragments/x"),
            premise: st("when x"),
        }],
    };
    assert_eq!(
        shown(|out| premises(out, &some)),
        "INSTRUCTION\n- b#fragments/x: when x\n"
    );
}

fn skill_shown() -> SkillShow {
    SkillShow {
        name: st("rev"),
        bundle: st("demo"),
        description: st("Reviews"),
        license: st("MIT"),
        compatibility: st("any"),
        allowed_tools: sv(&["Read", "Grep"]),
        body: st("SKILL\n"),
        files: vec![library_application::SkillFileRow {
            path: st("SKILL.md"),
            sha256: st("sha256:ab"),
            mode: st("0644"),
        }],
    }
}

// skill_show.go: the optional frontmatter lines, the body, the manifest.
#[test]
fn test_a_skill_show_prints_its_frontmatter_body_and_manifest() {
    let mut notes = Notes(Vec::new());
    assert_eq!(
        shown(|out| skill_show(out, &mut notes, &skill_shown())),
        "demo#skills/rev\n\ndescription: Reviews\nlicense: MIT\ncompatibility: any\nallowed-tools: Read, Grep\n\nSKILL\n\nFiles (1):\n  0644  sha256:ab  SKILL.md\n"
    );
    let bare = SkillShow {
        license: String::new(),
        compatibility: String::new(),
        allowed_tools: Vec::new(),
        ..skill_shown()
    };
    assert_eq!(
        shown(|out| skill_show(out, &mut notes, &bare)),
        "demo#skills/rev\n\ndescription: Reviews\n\nSKILL\n\nFiles (1):\n  0644  sha256:ab  SKILL.md\n"
    );
}

#[test]
fn test_a_skill_listing_says_why_it_is_empty() {
    let say = |filter: &str, total: usize| {
        shown(|out| {
            skill_list(
                out,
                &SkillList {
                    rows: Vec::new(),
                    bundle_filter: st(filter),
                    total,
                },
            )
        })
    };
    assert!(say("demo", 2)
        .starts_with("No skills found in bundle \"demo\".\nSkills exist in other bundles"));
    assert_eq!(
        say("demo", 0),
        "No skills found.\nCreate one with: ctxloom skill create <bundle> <name>\n"
    );
    assert_eq!(
        say("", 3),
        "No skills found.\nCreate one with: ctxloom skill create <bundle> <name>\n"
    );
}

#[test]
fn test_a_skill_row_without_a_description_or_tags_is_just_its_name_and_files() {
    let list = SkillList {
        rows: vec![SkillRow {
            name: st("demo/a"),
            description: String::new(),
            tags: Vec::new(),
            source: st("demo"),
            file_count: 2,
        }],
        bundle_filter: String::new(),
        total: 1,
    };
    assert_eq!(
        shown(|out| skill_list(out, &list)),
        "Skills (1):\n\n  demo:\n    - demo/a [2 file(s)]\n"
    );
}

fn profile_shown() -> ProfileShow {
    ProfileShow {
        entry: String::new(),
        name: st("dev"),
        description: st("Dev"),
        parents: sv(&["base"]),
        tags: sv(&["t"]),
        bundles: sv(&["demo"]),
        variables: [(st("k"), st("v"))].into_iter().collect(),
        exclude_fragments: sv(&["f"]),
        exclude_mcp: sv(&["m"]),
        path: st("/p/dev.yaml"),
        bundle: st("proj"),
        default: true,
    }
}

// profile_show.go: every section when it has entries.
#[test]
fn test_a_profile_show_prints_every_section_it_has() {
    assert_eq!(
        shown(|out| profile_show(out, &profile_shown())),
        "Profile: dev\nPath: /p/dev.yaml\nBundle: proj\nDefault: yes\nDescription: Dev\nParents:\n  - base\nBundles:\n  - demo\nTags:\n  - t\nVariables:\n  k: v\nExcluded fragments:\n  - f\nExcluded MCP servers:\n  - m\n"
    );
    let bare = ProfileShow {
        description: String::new(),
        parents: Vec::new(),
        tags: Vec::new(),
        bundles: Vec::new(),
        variables: Default::default(),
        exclude_fragments: Vec::new(),
        exclude_mcp: Vec::new(),
        bundle: String::new(),
        default: false,
        ..profile_shown()
    };
    assert_eq!(
        shown(|out| profile_show(out, &bare)),
        "Profile: dev\nPath: /p/dev.yaml\n"
    );
}

// agent_show.go: a resolved agent says no headless posture (ruling 134: ctxloom
// has none of its own).
#[test]
fn test_an_agent_show_prints_no_posture() {
    use library_application::{Requirement, RequirementSource, ResolvedAgent};
    let show = AgentShow {
        definition: agent_row("claude-code"),
        resolved: Some(ResolvedAgent {
            name: st("dev"),
            profiles: Vec::new(),
            engine: st("claude-code"),
            settings: settings(None),
            context: String::new(),
            fragments: sv(&["a", "b"]),
            driving: String::new(),
            command: String::new(),
        }),
        resolution_error: String::new(),
        requirements: vec![Requirement {
            command: st("tree-sitter"),
            sources: vec![RequirementSource {
                kind: st("mcp"),
                reference: st("ts"),
            }],
        }],
        requirements_error: String::new(),
        not_carried: Vec::new(),
    };
    let text = shown(|out| agent_show(out, &show));
    assert!(text.contains(
        "Resolved engine: claude-code\nSession home: session (default)\nComposed fragments: 2\nRequires:\n  - tree-sitter (mcp ts)\n"
    ));
    assert!(!text.contains("Headless posture"));
    assert!(!text.contains("Resolved permissions"));
    assert!(!text.contains("Resolved config home"));
    let failing = AgentShow {
        requirements_error: st("boom"),
        ..show
    };
    assert!(shown(|out| agent_show(out, &failing)).ends_with("Requires: unavailable (boom)\n"));
}

fn mcp_entry(served_by: &str, url: &str, args: &[&str]) -> library_application::McpRow {
    library_application::McpRow {
        name: st("srv"),
        command: st("cmd"),
        args: sv(args),
        env: [(st("K"), st("V"))].into_iter().collect(),
        url: st(url),
        served_by: st(served_by),
        source: st("ctxloom+local:demo"),
        notes: String::new(),
        installation: String::new(),
    }
}

// mcp_show.go: the session's endpoint, a URL, or a command with its args.
#[test]
fn test_an_mcp_server_shows_how_it_is_reached() {
    let show = |entry| McpShow {
        name: st("srv"),
        found: true,
        entries: vec![entry],
    };
    assert_eq!(
        shown(|out| mcp_show(out, &show(mcp_entry("session-endpoint", "", &[])))),
        "MCP Server: srv\nBundle: ctxloom+local:demo\nServed by: the running session's endpoint\nEnvironment:\n  K=V\n"
    );
    assert_eq!(
        shown(|out| mcp_show(out, &show(mcp_entry("", "https://x/y", &["ignored"])))),
        "MCP Server: srv\nBundle: ctxloom+local:demo\nURL: https://x/y\nEnvironment:\n  K=V\n"
    );
    assert_eq!(
        shown(|out| mcp_show(out, &show(mcp_entry("", "", &["--a", "--b"])))),
        "MCP Server: srv\nBundle: ctxloom+local:demo\nCommand: cmd\nArgs: --a --b\nEnvironment:\n  K=V\n"
    );
    let mut bare = mcp_entry("", "", &[]);
    bare.env = Default::default();
    assert_eq!(
        shown(|out| mcp_show(out, &show(bare))),
        "MCP Server: srv\nBundle: ctxloom+local:demo\nCommand: cmd\n"
    );
}

#[test]
fn test_the_mcp_listing_names_each_server_and_says_when_there_are_none() {
    let row = |served_by: &str, url: &str| library_application::McpListRow {
        name: st("srv"),
        command: st("cmd"),
        args: sv(&["--a"]),
        url: st(url),
        served_by: st(served_by),
        source: st("demo"),
    };
    let list = McpList {
        servers: vec![
            row("", ""),
            row("session-endpoint", ""),
            row("", "https://x"),
        ],
    };
    assert_eq!(
        shown(|out| mcp_list(out, &list)),
        "MCP Servers:\n  srv\n    Command: cmd\n    Args: --a\n    Bundle: demo\n  srv\n    Served by: the running session's endpoint\n    Bundle: demo\n  srv\n    URL: https://x\n    Bundle: demo\n"
    );
    assert!(shown(|out| mcp_list(
        out,
        &McpList {
            servers: Vec::new()
        }
    ))
    .starts_with("No MCP servers registered.\n\nMCP servers ship in bundles"));
}

fn hit(kind: &str, tags: &[&str], source: &str) -> library_application::SearchHit {
    library_application::SearchHit {
        kind: st(kind),
        name: st("n"),
        tags: sv(tags),
        source: st(source),
        r#match: st("name"),
        located: None,
    }
}

// search.go: hits grouped by kind in a fixed order; tags and source when set.
#[test]
fn test_search_results_group_by_kind_and_show_tags_and_source() {
    let results = SearchResults {
        query: st("n"),
        local: vec![
            hit("mcp_server", &[], ""),
            hit("fragment", &["a", "b"], "demo"),
            hit("fragment", &[], ""),
            hit("skill", &[], "demo"),
            hit("profile", &["t"], ""),
            hit("command", &[], ""),
        ],
        remote: Vec::new(),
        count: 6,
        hidden_local: 0,
    };
    assert_eq!(
        shown(|out| search(out, &results)),
        "Results (6):\n\nFragments:\n  - n [a, b] (demo)\n  - n\nCommands:\n  - n\nSkills:\n  - n (demo)\nProfiles:\n  - n [t]\nMCP Servers:\n  - n\n"
    );
    let none = SearchResults {
        count: 0,
        local: Vec::new(),
        ..results
    };
    assert_eq!(shown(|out| search(out, &none)), "No results found.\n");
}

fn plan() -> DryRun {
    DryRun {
        agent: st("dev"),
        workspace: st("worktree"),
        runtime: st("host"),
        engine: st("claude-code"),
        profiles: sv(&["p"]),
        fragments: sv(&["f1", "f2"]),
        context: st("CTX"),
        tokens: 7,
        prompt: st("go"),
        findings: Vec::new(),
        axes: (st("worktree"), st("host")),
        model: String::new(),
        effort: String::new(),
        permissions: None,
        drive: Default::default(),
        settings: None,
        settings_agent: st("dev"),
        surfaces: Default::default(),
    }
}

// dry_run.go: the sections, with a word for each that is empty.
#[test]
fn test_a_dry_run_prints_its_sections_and_names_the_empty_ones() {
    assert_eq!(
        shown(|out| dry_run(out, &plan())),
        "=== Agent ===\ndev (workspace: worktree, runtime: host)\n=== Engine ===\nclaude-code\n\n=== Profiles ===\n  p\n\n=== Fragments Loaded ===\n  f1\n  f2\n\n=== Assembled Context (~7 tokens) ===\nCTX\n\n=== Prompt ===\ngo\n"
    );
    let empty = DryRun {
        agent: String::new(),
        profiles: Vec::new(),
        fragments: Vec::new(),
        context: String::new(),
        prompt: String::new(),
        ..plan()
    };
    assert_eq!(
        shown(|out| dry_run(out, &empty)),
        "=== Engine ===\nclaude-code\n\n=== Profiles ===\n(no profiles)\n\n=== Fragments Loaded ===\n(no fragments)\n\n=== Assembled Context (~7 tokens) ===\n(no context)\n\n=== Prompt ===\n(interactive mode)\n"
    );
}

// Ruling 72: a dry run's text shows each setting the run takes with the
// place in the config that said it, as the JSON's `settings` does (by word):
// an agent's block, the agent, the engine block, the top level, or nothing.
#[test]
fn test_a_dry_run_shows_each_setting_with_the_place_that_said_it() {
    use library_application::{EnvKey, EnvSetting, RuntimeSetting, SourcedValue};
    let settings = library_application::AgentSettings {
        model: SourcedValue {
            value: Some(st("sonnet")),
            source: "agent",
        },
        effort: SourcedValue {
            value: None,
            source: "default",
        },
        permissions: library_application::PermissionsSetting {
            value: Some(serde_json::json!({"defaultMode": "acceptEdits"})),
            source: "default",
            line: st("{defaultMode: acceptEdits}"),
        },
        runtime: RuntimeSetting {
            value: st("host"),
            source: "agent",
            overrides: Some(st("container-rootless")),
            overrides_source: Some("engine"),
            change: Some("weakens"),
        },
        session_home: library_application::SourcedValue {
            value: "session".to_string(),
            source: "default",
        },
        env: EnvSetting {
            value: sv(&["HTTPS_PROXY", "ALPHA"]),
            source: "agent",
            keys: vec![
                EnvKey {
                    name: st("HTTPS_PROXY"),
                    source: "engine",
                },
                EnvKey {
                    name: st("ALPHA"),
                    source: "agent",
                },
            ],
        },
    };
    let shown_plan = shown(|out| {
        dry_run(
            out,
            &DryRun {
                settings: Some(settings.clone()),
                ..plan()
            },
        )
    });
    assert!(
        shown_plan.starts_with(concat!(
            "=== Agent ===\ndev (workspace: worktree, runtime: host)\n=== Engine ===\nclaude-code\n",
            "\n=== Settings ===\n",
            "  model        sonnet  (agents.dev.engines.claude-code)\n",
            "  effort       engine default, no flag passed  (default)\n",
            "  permissions  {defaultMode: acceptEdits}  (default)\n",
            "               passed to claude-code verbatim; ctxloom does not check it\n",
            "  runtime      host  (agents.dev; replaces engines.claude-code container-rootless: weakens isolation)\n",
            "  env          HTTPS_PROXY  (engines.claude-code), ALPHA  (agents.dev)\n",
            "\n=== Profiles ===\n",
        )),
        "{shown_plan}"
    );
    // the top level and an empty env
    let project = library_application::AgentSettings {
        runtime: RuntimeSetting {
            value: st("container-rootless"),
            source: "project",
            overrides: None,
            overrides_source: None,
            change: None,
        },
        env: EnvSetting {
            value: Vec::new(),
            source: "default",
            keys: Vec::new(),
        },
        ..settings
    };
    let shown_plan = shown(|out| {
        dry_run(
            out,
            &DryRun {
                settings: Some(project),
                ..plan()
            },
        )
    });
    assert!(
        shown_plan.contains(
            "  runtime      container-rootless  (runtime)\n  env          none  (default)\n"
        ),
        "{shown_plan}"
    );
}

// hand-test 2026-11-10: a run with no --agent takes the default agent's
// settings, and each is said at its full place, `agents.<name>`, not `agents.`
// (ruling 72).
#[test]
fn test_a_dry_run_of_the_default_agent_names_its_place_in_full() {
    use library_application::{EnvSetting, RuntimeSetting, SourcedValue};
    let settings = library_application::AgentSettings {
        model: SourcedValue {
            value: Some(st("sonnet")),
            source: "agent",
        },
        effort: SourcedValue {
            value: None,
            source: "default",
        },
        permissions: library_application::PermissionsSetting {
            value: Some(serde_json::json!({"defaultMode": "acceptEdits"})),
            source: "default",
            line: st("{defaultMode: acceptEdits}"),
        },
        runtime: RuntimeSetting {
            value: st("host"),
            source: "agent",
            overrides: None,
            overrides_source: None,
            change: None,
        },
        session_home: library_application::SourcedValue {
            value: "session".to_string(),
            source: "default",
        },
        env: EnvSetting {
            value: Vec::new(),
            source: "default",
            keys: Vec::new(),
        },
    };
    let shown_plan = shown(|out| {
        dry_run(
            out,
            &DryRun {
                agent: String::new(),
                settings_agent: st("default"),
                settings: Some(settings),
                ..plan()
            },
        )
    });
    assert!(
        shown_plan.contains("  model        sonnet  (agents.default.engines.claude-code)\n"),
        "{shown_plan}"
    );
    assert!(
        shown_plan.contains("  runtime      host  (agents.default)\n"),
        "{shown_plan}"
    );
    assert!(!shown_plan.contains("(agents.)"), "{shown_plan}");
    assert!(!shown_plan.contains("=== Agent ==="), "{shown_plan}");
}

// Owner rulings 135, 137, 139: the dry run, `engine show` and `agent show`
// print the permissions document as written (flow form), from where it was
// said, and the line that ctxloom passes it to the engine verbatim and checks
// nothing; with none, they say nothing is passed (and no such line).
#[test]
fn test_a_permissions_document_is_shown_as_written_and_said_unchecked() {
    use library_application::{EnvSetting, PermissionsSetting, RuntimeSetting, SourcedValue};
    let settings = |line: Option<&str>, source: &'static str| library_application::AgentSettings {
        model: SourcedValue {
            value: None,
            source: "default",
        },
        effort: SourcedValue {
            value: None,
            source: "default",
        },
        permissions: PermissionsSetting {
            value: line.map(|_| serde_json::json!({"any": "thing"})),
            source,
            line: line.map(ToString::to_string).unwrap_or_default(),
        },
        runtime: RuntimeSetting {
            value: st("host"),
            source: "default",
            overrides: None,
            overrides_source: None,
            change: None,
        },
        session_home: library_application::SourcedValue {
            value: "session".to_string(),
            source: "default",
        },
        env: EnvSetting {
            value: Vec::new(),
            source: "default",
            keys: Vec::new(),
        },
    };
    let on = |engine: &str, s: library_application::AgentSettings| {
        shown(|out| {
            dry_run(
                out,
                &DryRun {
                    engine: st(engine),
                    settings: Some(s),
                    ..plan()
                },
            )
        })
    };
    let claude = on(
        "claude-code",
        settings(Some("{defaultMode: acceptEdits}"), "default"),
    );
    assert!(
        claude.contains("  permissions  {defaultMode: acceptEdits}  (default)\n               passed to claude-code verbatim; ctxloom does not check it\n"),
        "{claude}"
    );
    let opencode = on("opencode", settings(Some("{edit: deny}"), "agent"));
    assert!(
        opencode.contains("  permissions  {edit: deny}  (agents.dev.engines.opencode)\n               passed to opencode verbatim; ctxloom does not check it\n"),
        "{opencode}"
    );
    let unset = on("opencode", settings(None, "default"));
    assert!(
        unset.contains("  permissions  none, nothing passed  (default)\n"),
        "{unset}"
    );
    assert!(!unset.contains("verbatim"), "{unset}");
    let engine = shown(|out| {
        settings_lines(
            out,
            &settings(Some("{defaultMode: plan}"), "agent"),
            "claude-code",
            "      ",
        )
    });
    assert!(
        engine.contains("      permissions: {defaultMode: plan} (agent)\n        passed to claude-code verbatim; ctxloom does not check it\n"),
        "{engine}"
    );
    let agent = shown(|out| settings_lines(out, &settings(None, "default"), "opencode", ""));
    assert!(
        agent.contains("Permissions: none, nothing passed (default)\n"),
        "{agent}"
    );
    assert!(!agent.contains("verbatim"), "{agent}");
}

fn found(remote: &str, name: &str, tags: &[&str]) -> library_application::RemoteFound {
    library_application::RemoteFound {
        remote: remote.to_string(),
        name: name.to_string(),
        kind: "bundle".to_string(),
        tags: tags.iter().map(ToString::to_string).collect(),
        description: String::new(),
        author: String::new(),
        pull_ref: String::new(),
    }
}

fn remote_only(hits: Vec<library_application::RemoteFound>) -> library_application::SearchResults {
    library_application::SearchResults {
        query: "q".to_string(),
        local: Vec::new(),
        count: hits.len() as u64,
        remote: hits,
        hidden_local: 0,
    }
}

// The remote table: columns as wide as the widest entry, tags cut at twenty
// characters.
#[test]
fn test_remote_search_results_are_a_table_as_wide_as_its_entries() {
    let results = remote_only(vec![
        found("origin", "kit", &["go", "tdd"]),
        found(
            "a-longer-remote",
            "a-longer-bundle-name",
            &["exactly-twenty-chars"],
        ),
        found("o", "n", &["exactly-twenty-one-chr"]),
    ]);
    let table = "Remote:\n  Type     \u{2502} Remote          \u{2502} Name                 \u{2502} Tags\n  \u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{253c}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{253c}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{253c}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\n  bundle   \u{2502} origin          \u{2502} kit                  \u{2502} go, tdd\n  bundle   \u{2502} a-longer-remote \u{2502} a-longer-bundle-name \u{2502} exactly-twenty-chars\n  bundle   \u{2502} o               \u{2502} n                    \u{2502} exactly-twenty-on...\n\nUse one: add its ref to a profile (ctxloom profile create/modify), then ctxloom deps pull\n";
    assert_eq!(
        shown(|out| search(out, &results)),
        format!("Results (3):\n\n{table}")
    );
    let mixed = SearchResults {
        local: vec![hit("fragment", &[], "")],
        count: 4,
        ..results
    };
    assert_eq!(
        shown(|out| search(out, &mixed)),
        format!("Results (4):\n\nFragments:\n  - n\n\n{table}")
    );
}

#[test]
fn test_local_results_alone_have_no_remote_section() {
    let results = SearchResults {
        query: st("n"),
        local: vec![hit("fragment", &[], "")],
        remote: Vec::new(),
        count: 1,
        hidden_local: 0,
    };
    assert_eq!(
        shown(|out| search(out, &results)),
        "Results (1):\n\nFragments:\n  - n\n"
    );
}

// Columns are counted in terminal width, so a wide character takes two.
#[test]
fn test_the_remote_table_aligns_wide_characters_by_display_width() {
    let results = remote_only(vec![found("origin", "\u{65e5}\u{672c}", &[])]);
    let shown = shown(|out| search(out, &results));
    assert!(
        shown.contains("bundle   \u{2502} origin \u{2502} \u{65e5}\u{672c} \u{2502} \n"),
        "{shown}"
    );
}

/// An agent's settings as `agent show` resolves them: the model as given,
/// the rest on their defaults.
fn settings(model: Option<&str>) -> library_application::AgentSettings {
    use library_application::{AgentSettings, RuntimeSetting, SourcedValue};
    AgentSettings {
        model: SourcedValue {
            value: model.map(ToString::to_string),
            source: if model.is_some() { "agent" } else { "default" },
        },
        effort: SourcedValue {
            value: None,
            source: "default",
        },
        permissions: library_application::PermissionsSetting {
            value: None,
            source: "default",
            line: String::new(),
        },
        runtime: RuntimeSetting {
            value: "host".to_string(),
            source: "default",
            overrides: None,
            overrides_source: None,
            change: None,
        },
        session_home: library_application::SourcedValue {
            value: "session".to_string(),
            source: "default",
        },
        env: library_application::EnvSetting {
            value: Vec::new(),
            source: "default",
            keys: Vec::new(),
        },
    }
}

// Owner rulings 110, 111, 127: a built-in bundle is labelled `[built-in]`.
#[test]
fn test_a_built_in_bundle_is_listed_under_its_origin() {
    let list = BundleList {
        rows: vec![BundleRow {
            origin: "ctxloom/builtin".to_string(),
            ..bundle("testing", 3, 0, 0)
        }],
        no_root: false,
        hint: String::new(),
    };
    let mut out = Vec::new();
    bundle_list(&mut out, &list).unwrap();
    let text = String::from_utf8(out).unwrap();
    assert!(text.contains("  testing (v1.2.0) [built-in]\n"), "{text}");
}
