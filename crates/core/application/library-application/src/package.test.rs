//! What a profile set carries for an engine (S9): the bundles' commands,
//! skills, servers and hooks, and ctxloom's own callbacks. Each test names the
//! Go behaviour it holds the application to.

use crate::fakes::{plain, Setup, ENVELOPE};
use crate::PackageRequest;

const CONFIG: &str = "schema_version: 9\nagents:\n  default:\n    engine: mock\n    profiles: [dev]\ndefault_agent: default\n";

const DEMO: &[(&str, &str)] = &[
    ENVELOPE,
    ("fragments/testing.md", "FRAGMENT-BODY-testing"),
    (
        "prompts/example.md",
        "---\ndescription: Example prompt\n---\nEXAMPLE-BODY",
    ),
    ("mcp/tool.yaml", "command: tool-cmd\nargs: [--flag]\n"),
    ("mcp/ctxloom.yaml", "served_by: session-endpoint\n"),
    (
        "skills/reviewer/SKILL.md",
        "---\nname: code-reviewer\ndescription: SKILL-MARKER\n---\nReview.\n",
    ),
    ("skills/reviewer/scripts/run.sh", "echo"),
    (
        "skills/.reviewer.meta.yaml",
        "tags: [ctxloom:link_id=pair]\nexecutable: [scripts/run.sh]\n",
    ),
    (
        "hooks/pre_tool/b-first.yaml",
        "type: command\ncommand: echo FIRST\n",
    ),
    ("hooks/pre_tool/.b-first.meta.yaml", "order: 100\n"),
    (
        "hooks/pre_tool/a-second.yaml",
        "type: command\ncommand: echo SECOND\n",
    ),
    ("hooks/pre_tool/.a-second.meta.yaml", "order: 200\n"),
];

const PROJECT: &[(&str, &str)] = &[
    ENVELOPE,
    (
        "profiles/dev.yaml",
        "bundles: [demo]\ndeny_tools: [Bash]\nhooks:\n  unified:\n    pre_tool:\n      - type: command\n        command: echo FROM-PROFILE\n",
    ),
];

fn setup() -> Setup<'static> {
    Setup {
        bundles: &[("demo", DEMO), ("project", PROJECT)],
        layers: &[CONFIG],
        ..Setup::default()
    }
}

fn package() -> crate::EnginePackage {
    setup()
        .library()
        .package(&plain(), &PackageRequest::default())
        .result
        .expect("a package")
}

// materialize.feature: the default agent's profiles are the set when none is
// named; the bundle's context is assembled.
#[test]
fn test_the_package_is_the_default_agents_profiles_assembled() {
    let p = package();
    assert_eq!(p.profiles, vec!["dev".to_string()]);
    assert!(p.context.contains("FRAGMENT-BODY-testing"));
    assert_eq!(p.deny_tools, vec!["Bash".to_string()]);
    assert!(p.statusline);
    assert_eq!(p.configured_engines, vec!["mock".to_string()]);
    assert!(p.has_agents && p.has_profiles);
}

// materialize.feature: ".mock/commands/demo/example.md": a command is
// `<bundle>/<name>`.
#[test]
fn test_a_bundles_commands_are_named_for_their_bundle() {
    let p = package();
    assert_eq!(p.commands.len(), 1);
    assert_eq!(p.commands[0].name, "demo/example");
    assert_eq!(p.commands[0].description, "Example prompt");
    assert_eq!(p.commands[0].body, "EXAMPLE-BODY");
}

// j000600_agent_skill.feature: a skill arrives whole, under its frontmatter
// name, its exec bit from the sidecar's `executable:`.
#[test]
fn test_a_skill_arrives_whole_under_its_frontmatter_name() {
    let p = package();
    assert_eq!(p.skills.len(), 1);
    let skill = &p.skills[0];
    assert_eq!(skill.name, "code-reviewer");
    assert_eq!(skill.description, "SKILL-MARKER");
    assert_eq!(skill.tags, vec!["ctxloom:link_id=pair".to_string()]);
    let run = skill
        .files
        .iter()
        .find(|f| f.path == "scripts/run.sh")
        .unwrap();
    assert!(run.executable);
    assert!(
        !skill
            .files
            .iter()
            .find(|f| f.path == "SKILL.md")
            .unwrap()
            .executable
    );
}

const VAULT: &[(&str, &str)] = &[
    ENVELOPE,
    (
        "skills/reviewer/SKILL.md",
        "---\nname: reviewer\ndescription: R\n---\nReview.\n",
    ),
    (
        "skills/planner/SKILL.md",
        "---\nname: planner\ndescription: P\n---\nPlan.\n",
    ),
    ("prompts/review.md", "---\ndescription: r\n---\nREVIEW-BODY"),
    ("prompts/plan.md", "---\ndescription: p\n---\nPLAN-BODY"),
];

fn vault_package(profile: &'static str) -> crate::EnginePackage {
    let project: &'static [(&'static str, &'static str)] =
        Box::leak(Box::new([ENVELOPE, ("profiles/dev.yaml", profile)]));
    Setup {
        bundles: &[("vault", VAULT), ("project", project)],
        layers: &[CONFIG],
        ..Setup::default()
    }
    .library()
    .package(&plain(), &PackageRequest::default())
    .result
    .expect("a package")
}

// cli/skill.feature "Profile skill curation exports only the curated skill,
// not the bundle's other one" (Go's composite assembly: a profile that
// curates skills exports those, not every skill of the bundles it includes).
#[test]
fn test_a_profile_that_curates_skills_exports_only_those() {
    let p = vault_package("bundles: [vault]\nskills: [vault#skills/reviewer]\n");
    let names: Vec<&str> = p.skills.iter().map(|s| s.name.as_str()).collect();
    assert_eq!(names, vec!["reviewer"]);
}

// The same profile without curation exports every skill its bundles hold.
#[test]
fn test_a_profile_that_curates_nothing_exports_every_bundle_skill() {
    let p = vault_package("bundles: [vault]\n");
    let mut names: Vec<&str> = p.skills.iter().map(|s| s.name.as_str()).collect();
    names.sort_unstable();
    assert_eq!(names, vec!["planner", "reviewer"]);
}

// Go's composite assembly treats commands alike: curated commands replace the
// bundles' whole set.
#[test]
fn test_a_profile_that_curates_commands_exports_only_those() {
    let p = vault_package("bundles: [vault]\ncommands: [vault#commands/review]\n");
    let names: Vec<&str> = p.commands.iter().map(|c| c.name.as_str()).collect();
    assert_eq!(names, vec!["vault/review"]);
}

// manage.feature: a server the bundle ships is carried; ctxloom's own, served
// by the session's endpoint, is carried as such (and registers nothing at
// rest).
#[test]
fn test_the_servers_the_bundles_ship_are_carried_with_who_serves_them() {
    let p = package();
    let tool = p.mcp.iter().find(|m| m.name == "tool").unwrap();
    assert_eq!(
        (tool.command.as_str(), tool.args.clone()),
        ("tool-cmd", vec!["--flag".to_string()])
    );
    let own = p.mcp.iter().find(|m| m.name == "ctxloom").unwrap();
    assert_eq!(own.served_by, "session-endpoint");
}

// manage.feature: "Listing hooks shows the resolved order and where each came
// from": profile hooks, then the bundle's by `order:`, then ctxloom's own.
#[test]
fn test_hooks_merge_by_append_each_bundle_ordered_by_its_own_order() {
    let p = package();
    let pre: Vec<(&str, &str, usize)> = p
        .hooks
        .iter()
        .filter(|h| h.event == "pre_tool")
        .map(|h| (h.hook.command.as_str(), h.source_kind.as_str(), h.position))
        .collect();
    assert_eq!(
        pre,
        vec![
            ("echo FROM-PROFILE", "profile-directory", 1),
            ("echo FIRST", "bundle", 2),
            ("echo SECOND", "bundle", 3),
        ]
    );
    let source = |command: &str| {
        p.hooks
            .iter()
            .find(|h| h.hook.command == command)
            .map(|h| h.source.clone())
            .unwrap()
    };
    assert_eq!(source("echo FIRST"), "ctxloom+local:demo");
    assert_eq!(source("echo FROM-PROFILE"), "dev");
}

// 8.4: ctxloom's own session-start and skill-mates callbacks ride every
// package; skill-mates is narrowed to the skill tool class.
#[test]
fn test_ctxloom_own_callbacks_ride_every_package() {
    let p = package();
    let own: Vec<_> = p
        .hooks
        .iter()
        .filter(|h| h.source_kind == "context")
        .collect();
    assert_eq!(own.len(), 2);
    let start = own.iter().find(|h| h.event == "session_start").unwrap();
    assert_eq!(start.hook.args, vec!["hook", "session-start"]);
    assert_eq!(start.hook.event, "session_start");
    assert_eq!(start.hook.name, "session-start");
    assert_eq!(start.hook.command, "ctxloom");
    assert_eq!(start.hook.kind, "command");
    assert_eq!(start.hook.timeout, 15);
    let mates = own.iter().find(|h| h.event == "post_tool").unwrap();
    assert_eq!(
        (mates.hook.args[1].as_str(), mates.hook.tool.as_str()),
        ("skill-mates", "skill")
    );
}

// manage.feature: "Statusline can be disabled and re-enabled": the choice is
// kept in the project's config.
#[test]
fn test_the_statusline_choice_is_kept_in_the_projects_config() {
    let (library, mem) = setup().fixture();
    library
        .set_statusline(&plain(), false)
        .result
        .expect("written");
    let config = mem.config(ctxloom_ports::Scope::Project).unwrap();
    assert!(config.contains("statusline: none"), "{config}");
    let p = library
        .package(&plain(), &PackageRequest::default())
        .result
        .unwrap();
    assert!(!p.statusline);
    library
        .set_statusline(&plain(), true)
        .result
        .expect("written");
    let config = mem.config(ctxloom_ports::Scope::Project).unwrap();
    assert!(config.contains("statusline: ctxloom"), "{config}");
    let p = library
        .package(&plain(), &PackageRequest::default())
        .result
        .unwrap();
    assert!(p.statusline);
}

// manage.feature: a profile naming a bundle that is not installed is a fatal
// finding under strict, and none under --degraded.
#[test]
fn test_a_missing_bundle_is_fatal_unless_degraded() {
    let bundles: &[(&str, &[(&str, &str)])] = &[(
        "project",
        &[ENVELOPE, ("profiles/dev.yaml", "bundles: [absent]\n")],
    )];
    let setup = Setup {
        bundles,
        layers: &[CONFIG],
        ..Setup::default()
    };
    let strict = setup
        .library()
        .package(&plain(), &PackageRequest::default());
    let strict = strict.result.unwrap();
    // Only the failing finding is fatal, never the warning beside it.
    assert_eq!(strict.fatal.len(), 1);
    assert!(
        matches!(&strict.fatal[0], crate::Notice::Finding { text, .. } if text.starts_with("failed to load bundle")),
        "{:?}",
        strict.fatal
    );
    let mut flags = plain();
    flags.degraded = true;
    let waived = setup.library().package(&flags, &PackageRequest::default());
    assert!(waived.result.unwrap().fatal.is_empty());
}

// materialize.feature: profiles named on the command line replace the default
// agent's.
#[test]
fn test_named_profiles_replace_the_default_agents() {
    let request = PackageRequest {
        profiles: vec!["dev".to_string()],
    };
    let p = setup()
        .library()
        .package(&plain(), &request)
        .result
        .unwrap();
    assert_eq!(p.profiles, vec!["dev".to_string()]);
}

#[test]
fn test_the_engines_are_the_ones_this_build_knows() {
    let names = setup().library().engine_names();
    let known: Vec<String> = crate::ENGINES.iter().map(|e| e.to_string()).collect();
    assert!(!known.is_empty());
    assert_eq!(names, known);
}

#[test]
fn test_distilled_forms_are_preferred_unless_the_config_says_not() {
    assert!(package().use_distilled);
    let off = Setup {
        layers: &["schema_version: 9\nconfig:\n  content_form: original\n"],
        ..setup()
    };
    let p = off
        .library()
        .package(&plain(), &PackageRequest::default())
        .result
        .unwrap();
    assert!(!p.use_distilled);
}

fn packaged(
    layer: &'static str,
    bundles: &'static [(&str, &[(&str, &str)])],
) -> crate::EnginePackage {
    Setup {
        bundles,
        layers: &[layer],
        ..Setup::default()
    }
    .library()
    .package(&plain(), &PackageRequest::default())
    .result
    .expect("a package")
}

const NO_AGENTS: &str = "schema_version: 9\n";
const AGENT_WITHOUT_PROFILES: &str =
    "schema_version: 9\nagents:\n  a:\n    engine: mock\ndefault_agent: a\n";

#[test]
fn test_a_project_says_whether_it_has_agents_and_profiles_to_bind_them_to() {
    let bare = packaged(NO_AGENTS, &[("project", &[ENVELOPE])]);
    assert!(!bare.has_agents);
    assert!(!bare.has_profiles);

    // Agents, but no profile anywhere.
    let agent = packaged(AGENT_WITHOUT_PROFILES, &[("project", &[ENVELOPE])]);
    assert!(agent.has_agents);
    assert!(!agent.has_profiles);

    // A profile in a bundle, with no default agent profiles.
    let in_bundle = packaged(
        NO_AGENTS,
        &[(
            "project",
            &[ENVELOPE, ("profiles/dev.yaml", "bundles: []\n")],
        )],
    );
    assert!(!in_bundle.has_agents);
    assert!(in_bundle.has_profiles);
}

#[test]
fn test_the_statusline_choice_keeps_the_other_config_entries() {
    let held = Setup {
        layers: &["schema_version: 9\nconfig:\n  content_form: original\n  statusline: ctxloom\n"],
        ..setup()
    };
    let (library, mem) = held.fixture();
    library
        .set_statusline(&plain(), false)
        .result
        .expect("written");
    let config = mem.config(ctxloom_ports::Scope::Project).unwrap();
    assert!(config.contains("content_form: original"), "{config}");
    assert!(config.contains("statusline: none"), "{config}");
    assert!(!config.contains("statusline: ctxloom"), "{config}");
}

// Owner ruling of 2026-11-12: init writes when conditional guidance loads
// into the project's config as `config.premised`, keeping the section's
// other entries; a second answer replaces the first.
#[test]
fn test_the_premised_choice_is_written_and_keeps_the_other_config_entries() {
    let held = Setup {
        layers: &["schema_version: 9\nconfig:\n  content_form: original\n  statusline: none\n"],
        ..setup()
    };
    let (library, mem) = held.fixture();
    library
        .set_premised(&plain(), "always")
        .result
        .expect("written");
    let config = mem.config(ctxloom_ports::Scope::Project).unwrap();
    assert!(config.contains("premised: always"), "{config}");
    assert!(config.contains("content_form: original"), "{config}");
    assert!(config.contains("statusline: none"), "{config}");
    library
        .set_premised(&plain(), "on-demand")
        .result
        .expect("written");
    let config = mem.config(ctxloom_ports::Scope::Project).unwrap();
    assert!(config.contains("premised: on-demand"), "{config}");
    assert!(!config.contains("premised: always"), "{config}");
}

// Rulings 89, 106: init writes the output directory she chose into the
// project's config as `output.dir`, keeping what else the config holds.
#[test]
fn test_the_output_directory_is_written_to_the_projects_config() {
    let (library, mem) = setup().fixture();
    library
        .set_output_dir(&plain(), "docs/sessions")
        .result
        .expect("written");
    let config = mem.config(ctxloom_ports::Scope::Project).unwrap();
    assert!(
        config.contains("output:\n  dir: docs/sessions\n"),
        "{config}"
    );
    assert!(
        config.contains("schema_version: 9"),
        "the rest is kept: {config}"
    );
    assert_eq!(
        library.run_settings(&plain()).map(|s| s.output_dir),
        Some("docs/sessions".to_string())
    );
    // A second answer replaces the first: one `dir`, the new one.
    library
        .set_output_dir(&plain(), "notes")
        .result
        .expect("written");
    let config = mem.config(ctxloom_ports::Scope::Project).unwrap();
    assert!(config.contains("output:\n  dir: notes\n"), "{config}");
    assert!(!config.contains("docs/sessions"), "{config}");
}

// ---- companion loadouts in the package (virtuous-word, companions slice 2) ----

mod companions {
    use ctxloom_ports::CompanionLoadout::{self, Loaded};

    use crate::fakes::{plain, Setup};
    use crate::{EnginePackage, Flags, PackageRequest};

    use super::{CONFIG, DEMO, PROJECT};

    const WITH: &str = "schema_version: 9\nagents:\n  default:\n    engine: mock\n    profiles: [dev]\ndefault_agent: default\ncompanions:\n  zeta:\n    command: [zeta]\n  alpha:\n    command: [alpha]\n";

    const ALPHA: &str = "run:\n  version: 1.0.0\n  hooks:\n    pre_tool:\n      - command: alpha guard\n        tool: shell\n        type: command\n      - command: alpha guard\n        tool: file_edit\n        type: command\n  commands:\n    check:\n      description: Alpha check\n      content: ALPHA-CHECK-BODY\n  mcp:\n    alpha-tool:\n      command: alpha-mcp\n      args: [--serve]\n";

    const ZETA: &str = "run:\n  version: 1.0.0\n  hooks:\n    pre_tool:\n      - command: zeta guard\n        type: command\n  commands:\n    check:\n      description: Zeta check\n      content: ZETA-CHECK-BODY\n";

    fn package_with(
        flags: &Flags,
        layer: &'static str,
        companions: &[(&str, CompanionLoadout)],
    ) -> EnginePackage {
        Setup {
            bundles: &[("demo", DEMO), ("project", PROJECT)],
            layers: &[layer],
            companions,
            ..Setup::default()
        }
        .library()
        .package(flags, &PackageRequest::default())
        .result
        .expect("a package")
    }

    fn both() -> [(&'static str, CompanionLoadout); 2] {
        [
            ("alpha", Loaded(ALPHA.to_string())),
            ("zeta", Loaded(ZETA.to_string())),
        ]
    }

    fn pre_tool(p: &EnginePackage) -> Vec<(String, String, String, usize)> {
        p.hooks
            .iter()
            .filter(|h| h.event == "pre_tool")
            .map(|h| {
                (
                    h.hook.command.clone(),
                    h.source_kind.clone(),
                    h.source.clone(),
                    h.position,
                )
            })
            .collect()
    }

    // Go's ResolveBundleHooksFor: companions' hooks, then the profile
    // bundles', after the profiles' own and before ctxloom's callbacks.
    #[test]
    fn companion_hooks_follow_the_profiles_own_and_precede_the_bundles() {
        let p = package_with(&plain(), WITH, &both());
        let row = |c: &str, kind: &str, source: &str, n: usize| {
            (c.to_string(), kind.to_string(), source.to_string(), n)
        };
        assert_eq!(
            pre_tool(&p),
            vec![
                row("echo FROM-PROFILE", "profile-directory", "dev", 1),
                row("alpha guard", "bundle", "ctxloom+companion:alpha", 2),
                row("alpha guard", "bundle", "ctxloom+companion:alpha", 3),
                row("zeta guard", "bundle", "ctxloom+companion:zeta", 4),
                row("echo FIRST", "bundle", "ctxloom+local:demo", 5),
                row("echo SECOND", "bundle", "ctxloom+local:demo", 6),
            ]
        );
    }

    #[test]
    fn a_companion_hook_keeps_its_tool_class_for_the_engine_to_map() {
        let p = package_with(&plain(), WITH, &both());
        let tools: Vec<&str> = p
            .hooks
            .iter()
            .filter(|h| h.hook.command == "alpha guard")
            .map(|h| h.hook.tool.as_str())
            .collect();
        assert_eq!(tools, ["shell", "file_edit"]);
    }

    #[test]
    fn ctxloom_own_callbacks_still_come_last() {
        let p = package_with(&plain(), WITH, &both());
        let start: Vec<&str> = p
            .hooks
            .iter()
            .filter(|h| h.event == "session_start")
            .map(|h| h.source_kind.as_str())
            .collect();
        assert_eq!(start, ["context"]);
        let kinds: Vec<&str> = p
            .hooks
            .iter()
            .filter(|h| h.event == "post_tool")
            .map(|h| h.source_kind.as_str())
            .collect();
        assert_eq!(kinds, ["context"]);
    }

    // Go's commandCollector.fromBundles(CompanionRefs()): every command of
    // every companion, by companion name, named for the companion.
    #[test]
    fn a_companions_commands_are_named_for_the_companion_in_name_order() {
        let p = package_with(&plain(), WITH, &both());
        let names: Vec<(&str, &str, &str)> = p
            .commands
            .iter()
            .map(|c| (c.name.as_str(), c.description.as_str(), c.body.as_str()))
            .collect();
        assert_eq!(
            names,
            [
                ("demo/example", "Example prompt", "EXAMPLE-BODY"),
                ("alpha/check", "Alpha check", "ALPHA-CHECK-BODY"),
                ("zeta/check", "Zeta check", "ZETA-CHECK-BODY"),
            ]
        );
    }

    // "a curation names bundle commands, never theirs".
    #[test]
    fn a_companions_commands_are_unconditional_under_a_curation() {
        let project: &'static [(&str, &str)] = &[
            crate::fakes::ENVELOPE,
            (
                "profiles/dev.yaml",
                "bundles: [demo]\ncommands: [demo#commands/example]\n",
            ),
        ];
        let companions = both();
        let p = Setup {
            bundles: &[("demo", DEMO), ("project", project)],
            layers: &[WITH],
            companions: &companions,
            ..Setup::default()
        }
        .library()
        .package(&plain(), &PackageRequest::default())
        .result
        .expect("a package");
        let names: Vec<&str> = p.commands.iter().map(|c| c.name.as_str()).collect();
        assert_eq!(names, ["demo/example", "alpha/check", "zeta/check"]);
    }

    #[test]
    fn a_companions_mcp_server_is_carried_with_its_command() {
        let p = package_with(&plain(), WITH, &both());
        let server = p.mcp.iter().find(|m| m.name == "alpha-tool").unwrap();
        assert_eq!(
            (server.command.as_str(), server.args.clone()),
            ("alpha-mcp", vec!["--serve".to_string()])
        );
        assert!(p.mcp.iter().any(|m| m.name == "tool"), "the bundle's too");
        assert!(p.fatal.is_empty(), "{:?}", p.fatal);
    }

    // Go: companions claim first; a profile bundle declaring the name is the
    // contest's loser, loudly.
    #[test]
    fn a_profile_bundle_cannot_take_a_name_a_companion_claimed() {
        let rival = "run:\n  version: 1.0.0\n  mcp:\n    tool:\n      command: companion-tool\n";
        let companions = [("alpha", Loaded(rival.to_string()))];
        let p = package_with(&plain(), WITH, &companions);
        let tool: Vec<&str> = p
            .mcp
            .iter()
            .filter(|m| m.name == "tool")
            .map(|m| m.command.as_str())
            .collect();
        assert_eq!(tool, ["companion-tool"]);
        assert_eq!(p.fatal.len(), 1, "{:?}", p.fatal);
    }

    #[test]
    fn no_companions_flag_delivers_nothing_of_them() {
        let flags = Flags {
            no_companions: true,
            ..Flags::default()
        };
        let p = package_with(&flags, WITH, &both());
        assert!(pre_tool(&p)
            .iter()
            .all(|h| h.2 != "ctxloom+companion:alpha"));
        assert_eq!(p.commands.len(), 1);
        assert!(p.mcp.iter().all(|m| m.name != "alpha-tool"));
    }

    #[test]
    fn a_package_without_companions_is_unchanged() {
        let p = package_with(&plain(), CONFIG, &[]);
        assert_eq!(pre_tool(&p).len(), 3);
        assert_eq!(p.commands.len(), 1);
    }
}

/// The package of a project whose one profile, `dev`, names `bundles` (as the
/// profile's own YAML list), over the DEMO bundle.
fn item_scoped_package(bundles: &'static str) -> crate::EnginePackage {
    let profile: &'static str = Box::leak(format!("bundles: [{bundles}]\n").into_boxed_str());
    let project: &'static [(&'static str, &'static str)] =
        Box::leak(Box::new([ENVELOPE, ("profiles/dev.yaml", profile)]));
    Setup {
        bundles: &[("demo", DEMO), ("project", project)],
        layers: &[CONFIG],
        ..Setup::default()
    }
    .library()
    .package(&plain(), &PackageRequest::default())
    .result
    .expect("a package")
}

// Go's `reportBundleRefLoadFailure` (internal/core/config/config_bundles.go):
// a profile's `bundles:` entry may be ITEM-SCOPED (`<bundle>#fragments/<f>`),
// selecting one fragment; it names no bundle to load servers and hooks from,
// so it contributes none, and is no finding. Its fragment still loads.
#[test]
fn test_a_fragment_reference_in_bundles_is_no_finding_and_its_fragment_loads() {
    let p = item_scoped_package("demo#fragments/testing");
    assert!(p.fatal.is_empty(), "{:?}", p.fatal);
    assert!(p.context.contains("FRAGMENT-BODY-testing"), "{}", p.context);
}

// The same rule, whichever spelling the item reference takes: a canonical one
// reaches its bundle, but selects one fragment of it, so the bundle's
// servers, hooks, commands and skills are not the profile's.
#[test]
fn test_a_fragment_reference_brings_none_of_its_bundles_servers_hooks_commands_or_skills() {
    for reference in [
        "demo#fragments/testing",
        "ctxloom+local:demo#fragments/testing",
    ] {
        let p = item_scoped_package(reference);
        assert!(p.fatal.is_empty(), "{reference}: {:?}", p.fatal);
        assert!(
            p.context.contains("FRAGMENT-BODY-testing"),
            "{reference}: {}",
            p.context
        );
        assert!(
            !p.mcp.iter().any(|m| m.name == "tool"),
            "{reference}: {:?}",
            p.mcp.iter().map(|m| &m.name).collect::<Vec<_>>()
        );
        assert!(
            !p.hooks.iter().any(|h| h.hook.command == "echo FIRST"),
            "{reference}: a bundle hook leaked"
        );
        assert!(
            !p.commands.iter().any(|c| c.name == "demo/example"),
            "{reference}: a bundle command leaked"
        );
        assert!(p.skills.is_empty(), "{reference}: a bundle skill leaked");
    }
}

// Beside a whole-bundle reference to the same bundle, the item reference
// changes nothing: the whole bundle's servers are carried once.
#[test]
fn test_a_whole_bundle_reference_beside_its_fragment_reference_still_brings_its_servers() {
    let p = item_scoped_package("demo#fragments/testing, demo");
    assert!(p.fatal.is_empty(), "{:?}", p.fatal);
    assert!(p.mcp.iter().any(|m| m.name == "tool"));
}

// ---------------------------------------------------- what a launch carries

const TWO_AGENTS: &str = "schema_version: 9\nagents:\n  default:\n    engine: mock\n    profiles: [dev]\n  other:\n    engine: mock\n    profiles: [lean]\ndefault_agent: default\n";

const SIDE: &[(&str, &str)] = &[
    ENVELOPE,
    ("fragments/side.md", "FRAGMENT-BODY-side"),
    (
        "prompts/tidy.md",
        "---\ndescription: Tidy prompt\n---\nTIDY-BODY",
    ),
    ("mcp/side-tool.yaml", "command: side-cmd\n"),
    (
        "skills/tidier/SKILL.md",
        "---\nname: tidier\ndescription: TIDY-SKILL\n---\nTidy.\n",
    ),
    (
        "hooks/post_tool/side.yaml",
        "type: command\ncommand: echo SIDE\n",
    ),
];

const PROJECT_TWO: &[(&str, &str)] = &[
    ENVELOPE,
    ("profiles/dev.yaml", "bundles: [demo]\ndeny_tools: [Bash]\n"),
    (
        "profiles/lean.yaml",
        "bundles: [side]\ndeny_tools: [Edit]\n",
    ),
];

fn surfaces_of(agent: &str) -> surface_ports::Inputs {
    let setup = Setup {
        bundles: &[("demo", DEMO), ("side", SIDE), ("project", PROJECT_TWO)],
        layers: &[TWO_AGENTS],
        ..Setup::default()
    };
    let request = crate::RunRequest {
        agent: agent.to_string(),
        ..crate::RunRequest::default()
    };
    setup
        .library()
        .dry_run(&plain(), &request)
        .result
        .expect("a plan")
        .plan
        .surfaces
}

// Owner ruling 2026-11-12: everything rides the launch, from the RUN agent's
// selection: its bundles' hooks, deny list, servers, commands and skills, and
// none of the default agent's.
#[test]
fn test_a_launch_carries_the_run_agents_surfaces_not_the_default_agents() {
    let s = surfaces_of("other");
    let commands: Vec<&str> = s.commands.iter().map(|c| c.name.as_str()).collect();
    assert_eq!(commands, ["side/tidy"]);
    let skills: Vec<&str> = s.skills.iter().map(|k| k.name.as_str()).collect();
    assert_eq!(skills, ["tidier"]);
    let servers: Vec<&str> = s.mcp.iter().map(|m| m.name.as_str()).collect();
    assert_eq!(servers, ["side-tool"]);
    assert_eq!(s.deny_tools, ["Edit"]);
    assert!(s.statusline);
    let post: Vec<&str> = s.hooks["post_tool"]
        .iter()
        .map(|h| h.command.as_str())
        .collect();
    assert!(post.contains(&"echo SIDE"), "{post:?}");
    assert!(s.context.is_empty(), "the context rides on its own");
}

// ctxloom's own callbacks ride with them (the session-start and skill-mates
// hooks a session needs in any directory).
#[test]
fn test_a_launch_carries_ctxlooms_own_callbacks() {
    let s = surfaces_of("other");
    let start = &s.hooks["session_start"];
    assert!(start
        .iter()
        .any(|h| h.command == "ctxloom" && h.args == ["hook", "session-start"]));
    assert!(s.hooks["post_tool"]
        .iter()
        .any(|h| h.args == ["hook", "skill-mates"] && h.tool == "skill"));
}

// A launch with no agent named carries the default agent's.
#[test]
fn test_a_launch_with_no_agent_carries_the_default_agents() {
    let s = surfaces_of("");
    assert_eq!(s.deny_tools, ["Bash"]);
    let commands: Vec<&str> = s.commands.iter().map(|c| c.name.as_str()).collect();
    assert_eq!(commands, ["demo/example"]);
    // A server the session serves is not registered for it.
    assert!(s.mcp.iter().all(|m| m.name != "ctxloom"));
}
