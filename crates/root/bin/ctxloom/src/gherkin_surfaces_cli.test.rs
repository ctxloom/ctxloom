//! The steps of the surfaces World's CLI contract features (`features/cli/`)
//! that no journey's module has, written as step 11 un-pended them (each
//! step names its feature).

use cucumber::gherkin::Step;
use cucumber::{given, then, when};

use super::World;
use crate::tests::files;
use crate::tests::gherkin_common::CommonWorld;

/// remote.feature: a command run from a subdirectory of the project (made
/// if it is not there), with no `CTXLOOM_ROOT`, so the project is found by
/// walking up.
#[when(expr = "I run {string} from the project subdirectory {string}")]
fn run_from_subdirectory(w: &mut World, line: String, sub: String) {
    let dir = w.common.project.dir().join(&sub);
    files::mkdirs(&dir);
    w.common.run_lines_at(Some(&dir), &line, "");
}

/// remote.feature: ctxloom has never run in this home.
#[given(expr = "the home has no {string} directory yet")]
fn home_lacks_dir(w: &mut World, rel: String) {
    assert!(
        !files::exists(w.common.project.home().join(&rel)),
        "the home already has {rel}"
    );
}

/// remote.feature: the directory's owner may read, write and search it,
/// where the host has permission bits at all.
#[then(expr = "the home directory {string} is readable, writable and searchable by its owner")]
fn home_dir_usable(w: &mut World, rel: String) {
    let dir = w.common.project.home().join(&rel);
    assert!(files::exists(&dir), "{rel} does not exist");
    if let Some(mode) = files::mode_of(&dir) {
        assert_eq!(mode & 0o700, 0o700, "{rel} is {mode:o}");
    }
}

#[then(expr = "the home file {string} contains {string}")]
fn home_holds(w: &mut World, rel: String, text: String) {
    let held = w.common.project.read_home(&rel).expect("a home file");
    assert!(held.contains(&text), "{rel}: {held}");
}

/// A Given or And after a Given that runs Alice's commands (the common
/// `Alice ...:` step is a When): deps.feature's "Alice has already let a
/// pull refresh the project:".
#[given(regex = r"^Alice .*:$")]
fn alice_already_ran(w: &mut World, step: &Step) {
    w.alice_runs(step);
}

/// The seeded remotes, once one was seeded.
fn remotes(w: &mut World) -> &mut super::super::deps::Remotes {
    w.remotes.as_mut().expect("a git remote was seeded")
}

/// deps.feature: Go's `the remote "<name>" changes fragment "<fragment>"
/// to "<content>"`, as a When (the surfaces World's Given is the same).
#[when(expr = "the remote {string} changes fragment {string} to {string}")]
fn remote_changes_fragment(w: &mut World, name: String, fragment: String, content: String) {
    remotes(w).changes_fragment(&name, &fragment, &content);
}

/// deps.feature: Go's `the remote "<name>" stops publishing bundle
/// "<bundle>"`.
#[given(expr = "the remote {string} stops publishing bundle {string}")]
fn remote_stops_publishing(w: &mut World, name: String, bundle: String) {
    remotes(w).stops_publishing(&name, &bundle);
}

/// deps.feature: Go's `BreakRemote`, its address left pointing at nothing.
#[given(expr = "the remote {string} becomes unreachable")]
fn remote_unreachable(w: &mut World, name: String) {
    remotes(w).becomes_unreachable(&name);
}

/// deps.feature: Go's `the remote "<name>" ships an MCP server ... and a
/// skill script printing ...`.
#[given(
    expr = "the remote {string} ships an MCP server {string} running {string} and a skill script printing {string}"
)]
fn remote_ships(w: &mut World, name: String, server: String, command: String, say: String) {
    remotes(w).ships(&name, &server, &command, &say);
}

/// deps.feature: the one cached clone of the remote, reset to its first
/// commit.
#[given(expr = "the remote {string}'s cached clone is forced back to its first commit")]
fn clone_forced_back(w: &mut World, _name: String) {
    super::super::deps::clone_forced_back_to_first_commit(&w.common.project);
}

/// profile.feature: an initialized project whose editor (the home config's
/// `editor`, through `sh`) rewrites the document's `description:` line to
/// `EDITED-BY-TEST`, keeping it valid YAML.
#[given("a ctxloom project with a description-rewriting editor")]
fn description_rewriting_editor(w: &mut World) {
    w.common.project = crate::tests::library::Project::initialized();
    w.common.project.at_a_terminal();
    w.common.project.write_home(
        ".ctxloom/config.yaml",
        "schema_version: 9\neditor:\n  command: sh\n  args:\n    - \"-c\"\n    - 'sed \"s/^description:.*/description: EDITED-BY-TEST/\" \"$1\" > \"$1.new\" && mv \"$1.new\" \"$1\"'\n    - editor\n",
    );
}

/// profile.feature: the engine file registers the MCP server `name` (the
/// surfaces World's negative step's reading).
#[then(expr = "the file {string} registers an MCP server named {string}")]
fn registers_mcp(w: &mut World, rel: String, name: String) {
    assert!(
        w.common.project.registers_mcp(&rel, "mcpServers", &name),
        "{rel} registers no MCP server {name}: {:?}",
        w.common.project.read(&rel)
    );
}

/// mcp.feature: an initialized project whose editor (the home config's
/// `editor`, through `sh`) rewrites the buffer's `command:` line to
/// `command: EDITED-BY-TEST`, keeping the rest of the entry.
#[given("a ctxloom project with a command-rewriting editor")]
fn command_rewriting_editor(w: &mut World) {
    w.common.project = crate::tests::library::Project::initialized();
    w.common.project.at_a_terminal();
    w.common.project.write_home(
        ".ctxloom/config.yaml",
        "schema_version: 9\neditor:\n  command: sh\n  args:\n    - \"-c\"\n    - 'sed \"s/^command:.*/command: EDITED-BY-TEST/\" \"$1\" > \"$1.new\" && mv \"$1.new\" \"$1\"'\n    - editor\n",
    );
}

/// An initialized project whose editor (the home config's `editor`, run
/// through `sh` on the file it is given) is `script`.
fn project_with_editor(w: &mut World, script: &str) {
    w.common.project = crate::tests::library::Project::initialized();
    w.common.project.at_a_terminal();
    w.common.project.write_home(
        ".ctxloom/config.yaml",
        &format!(
            "schema_version: 9\neditor:\n  command: sh\n  args:\n    - \"-c\"\n    - '{script}'\n    - editor\n"
        ),
    );
}

/// command.feature: an editor that adds the line `EDITED-BY-TEST` to the
/// file it opens.
#[given("a ctxloom project with a marker editor")]
fn marker_editor(w: &mut World) {
    project_with_editor(w, "printf \"\\nEDITED-BY-TEST\\n\" >> \"$1\"");
}

/// command.feature: an editor that comes back with the file emptied.
#[given("a ctxloom project with an editor that empties the file")]
fn emptying_editor(w: &mut World) {
    project_with_editor(w, ": > \"$1\"");
}

/// command.feature: "she ...:" runs her commands as "Alice ...:" does.
#[when(regex = r"^she .*:$")]
fn she_runs(w: &mut World, step: &Step) {
    w.alice_runs(step);
}

/// What the surfaces World's CLI contract steps keep.
#[derive(Default)]
pub(super) struct State {
    /// The bundle Trent authored, by name: its tree's files, relative to the
    /// bundle (deps.feature's publication Rule).
    authored: Vec<(String, Vec<(String, String)>)>,
}

/// deps.feature: Trent's directory-form bundle, one of every surface kind,
/// each with the metadata the publication Rule probes: a fragment and a
/// command with front matter, an MCP server with its command, args and env,
/// a skill whose script its sidecar declares executable, hooks whose
/// declared order (stamp before audit) is not the alphabetical one, and a
/// profile.
fn atelier_files() -> Vec<(String, String)> {
    [
        ("bundle.yaml", "version: \"1.0.0\"\ndescription: \"Trent's studio conventions\"\n"),
        ("fragments/house-style.md", "---\ntags: [style]\n---\n\nATELIER-HOUSE-STYLE: tabs, never spaces.\n"),
        ("fragments/when-shipping.md", "---\nnotes: ATELIER-FRAGMENT-DESC\n---\n\nShip on green only.\n"),
        ("prompts/ship-it.md", "---\ndescription: ATELIER-COMMAND-DESC\n---\n\nATELIER-SHIP-IT: cut the release.\n"),
        ("mcp/ledger.yaml", "command: ledger-mcp\nargs:\n  - --stdio\n  - --ledger=studio\nenv:\n  LEDGER_MODE: read-only\nnotes: ATELIER-MCP-DESC\n"),
        ("hooks/post_file_edit/stamp.yaml", "type: command\ncommand: echo ATELIER-STAMP\n"),
        ("hooks/post_file_edit/.stamp.meta.yaml", "order: 1\n"),
        ("hooks/post_file_edit/audit.yaml", "type: command\ncommand: echo ATELIER-AUDIT\n"),
        ("hooks/post_file_edit/.audit.meta.yaml", "order: 2\n"),
        ("hooks/session_start/greet.yaml", "type: command\ncommand: echo ATELIER-GREET\n"),
        ("skills/reviewer/SKILL.md", "---\nname: reviewer\ndescription: Reviews the studio's changes.\n---\n\nATELIER-REVIEWER body.\n"),
        ("skills/reviewer/scripts/run.sh", "#!/bin/sh\necho ATELIER-REVIEWER-RUN\n"),
        ("skills/reviewer/.meta.yaml", "notes: ATELIER-SKILL-DESC\nexecutable:\n  - scripts/run.sh\n"),
        ("profiles/studio.yaml", "description: ATELIER-PROFILE\nbundles:\n  - atelier\n"),
    ]
    .into_iter()
    .map(|(path, text)| (path.to_string(), text.to_string()))
    .collect()
}

/// The files Trent authored for `bundle`.
fn authored<'a>(w: &'a World, bundle: &str) -> &'a [(String, String)] {
    w.cli
        .authored
        .iter()
        .find(|(name, _)| name == bundle)
        .map(|(_, files)| files.as_slice())
        .unwrap_or_else(|| panic!("Trent authored no bundle {bundle}"))
}

/// The one authored file at `rel` of `bundle`.
fn authored_file<'a>(w: &'a World, bundle: &str, rel: &str) -> &'a str {
    authored(w, bundle)
        .iter()
        .find(|(path, _)| path == rel)
        .map(|(_, text)| text.as_str())
        .unwrap_or_else(|| panic!("Trent's {bundle} has no {rel}"))
}

#[given(expr = "Trent authors a directory-form bundle {string} carrying every surface kind")]
fn trent_authors(w: &mut World, bundle: String) {
    w.cli.authored.retain(|(name, _)| *name != bundle);
    w.cli.authored.push((bundle, atelier_files()));
}

/// Trent's company repository is a git remote, `company`, registered in
/// Alice's project, whose `main` holds the tree under its bundles.
#[given(expr = "Trent publishes the {string} tree to his company repo")]
fn trent_publishes(w: &mut World, bundle: String) {
    let root = super::super::deps::tree_bundle(&bundle);
    let files: Vec<(String, String)> = authored(w, &bundle)
        .iter()
        .map(|(path, text)| (format!("{root}/{path}"), text.clone()))
        .collect();
    let env: Vec<(&str, &str)> = w
        .common
        .env
        .iter()
        .map(|(k, v)| (k.as_str(), v.as_str()))
        .collect();
    let project = &w.common.project;
    w.remotes
        .get_or_insert_with(super::super::deps::Remotes::new)
        .publish(project, &env, "company", &files);
}

/// Alice's profile `dev` draws on the company's bundle by its canonical
/// reference, and she pulls it.
#[given(expr = "Alice references the company's {string} bundle and pulls it")]
#[when(expr = "Alice references the company's {string} bundle and pulls it")]
fn alice_pulls(w: &mut World, bundle: String) {
    let url = remotes(w).url("company");
    w.common
        .project
        .profile("dev", &format!("ctxloom+{url}//bundles/{bundle}"));
    w.common.run_lines("ctxloom deps pull", "");
    w.common.ran().succeeds();
}

/// The installed tree of the pulled `bundle`: the one commit directory
/// under its name in `.ctxloom/cache/bundles`.
fn pulled_tree(w: &World, bundle: &str) -> std::path::PathBuf {
    fn find(dir: &std::path::Path, name: &str) -> Option<std::path::PathBuf> {
        for path in files::children(dir) {
            if !files::is_dir(&path) {
                continue;
            }
            if path.file_name().is_some_and(|n| n == name) {
                return Some(path);
            }
            if let Some(found) = find(&path, name) {
                return Some(found);
            }
        }
        None
    }
    let cache = w.common.project.dir().join(".ctxloom/cache/bundles");
    let dir = find(&cache, bundle).unwrap_or_else(|| panic!("no pulled {bundle} under {cache:?}"));
    let shas: Vec<_> = files::children(&dir)
        .into_iter()
        .filter(|p| files::is_dir(p))
        .collect();
    assert_eq!(shas.len(), 1, "one pinned tree of {bundle}: {shas:?}");
    shas[0].clone()
}

/// The pulled file `rel` of `bundle`, as text.
fn pulled_file(w: &World, bundle: &str, rel: &str) -> String {
    let path = pulled_tree(w, bundle).join(rel);
    files::read(&path).unwrap_or_else(|e| panic!("no pulled {bundle}/{rel}: {e}"))
}

#[then(expr = "Alice's pulled {string} tree carries {string} byte for byte as published")]
fn carries_byte_for_byte(w: &mut World, bundle: String, rel: String) {
    assert_eq!(
        pulled_file(w, &bundle, &rel),
        authored_file(w, &bundle, &rel),
        "{bundle}/{rel}"
    );
}

#[then(
    regex = r#"^Alice's pulled "([^"]+)" tree stores the "([^"]+)" metadata "([^"]+)" as (front-matter in|a field of|the sidecar) "([^"]+)"$"#
)]
fn stores_metadata(
    w: &mut World,
    bundle: String,
    kind: String,
    probe: String,
    placement: String,
    rel: String,
) {
    let text = pulled_file(w, &bundle, &rel);
    match placement.as_str() {
        "front-matter in" => {
            let front = text
                .strip_prefix("---\n")
                .and_then(|rest| rest.split_once("\n---\n"))
                .map(|(front, _)| front)
                .unwrap_or_else(|| panic!("{kind}: {rel} has no front matter: {text:?}"));
            assert!(
                front.contains(&probe),
                "{kind}: {rel}'s front matter: {front:?}"
            );
        }
        "a field of" => {
            let doc = angzarr_yaml::parse(&text).unwrap_or_else(|e| panic!("{rel}: {e}"));
            let held = doc
                .as_map()
                .unwrap_or_default()
                .iter()
                .any(|(_, value)| value.as_str() == Some(probe.as_str()));
            assert!(held, "{kind}: no field of {rel} is {probe}: {text:?}");
        }
        _ => assert!(text.contains(&probe), "{kind}: the sidecar {rel}: {text:?}"),
    }
}

/// A server's `command`, `args` and `env`, parsed.
fn server_shape(text: &str) -> (Option<String>, Vec<String>, Vec<(String, String)>) {
    let doc = angzarr_yaml::parse(text).unwrap_or_else(|e| panic!("{e}: {text:?}"));
    let command = doc
        .get("command")
        .and_then(angzarr_yaml::Value::as_str)
        .map(str::to_string);
    let args = doc
        .get("args")
        .and_then(angzarr_yaml::Value::as_seq)
        .unwrap_or_default()
        .iter()
        .filter_map(|a| a.as_str().map(str::to_string))
        .collect();
    let env = doc
        .get("env")
        .and_then(angzarr_yaml::Value::as_map)
        .unwrap_or_default()
        .iter()
        .map(|(k, v)| (k.clone(), v.as_str().unwrap_or_default().to_string()))
        .collect();
    (command, args, env)
}

#[then(
    expr = "Alice's pulled {string} MCP server {string} parses with its command, args and env intact"
)]
fn server_intact(w: &mut World, bundle: String, server: String) {
    let rel = format!("mcp/{server}.yaml");
    let pulled = server_shape(&pulled_file(w, &bundle, &rel));
    let published = server_shape(authored_file(w, &bundle, &rel));
    assert!(published.0.is_some() && !published.1.is_empty() && !published.2.is_empty());
    assert_eq!(pulled, published, "{bundle}/{rel}");
}

/// Every file of the authored skill `skill` of `bundle`, by its path in the
/// bundle.
fn skill_files<'a>(w: &'a World, bundle: &str, skill: &str) -> Vec<&'a (String, String)> {
    let prefix = format!("skills/{skill}/");
    authored(w, bundle)
        .iter()
        .filter(|(path, _)| path.starts_with(&prefix))
        .collect()
}

#[then(
    expr = "Alice's pulled skill {string} contains exactly the files Trent published, byte for byte"
)]
fn skill_whole(w: &mut World, skill: String) {
    let bundle = w
        .cli
        .authored
        .first()
        .expect("Trent authored a bundle")
        .0
        .clone();
    let tree = pulled_tree(w, &bundle);
    let dir = tree.join("skills").join(&skill);
    fn walk(dir: &std::path::Path, base: &std::path::Path, out: &mut Vec<String>) {
        for path in files::children(dir) {
            if files::is_dir(&path) {
                walk(&path, base, out);
            } else {
                let rel = path.strip_prefix(base).expect("under the tree");
                out.push(rel.to_string_lossy().replace('\\', "/"));
            }
        }
    }
    let mut pulled = Vec::new();
    walk(&dir, &tree, &mut pulled);
    pulled.sort();
    let published = skill_files(w, &bundle, &skill);
    let mut names: Vec<String> = published.iter().map(|(p, _)| p.clone()).collect();
    names.sort();
    assert_eq!(pulled, names, "the skill's files");
    for (path, text) in published {
        let held = files::read(tree.join(path)).expect("a pulled file");
        assert_eq!(&held, text, "{path}");
    }
}

#[then(expr = "Alice's pulled skill {string} file {string} is executable")]
fn skill_file_executable(w: &mut World, skill: String, file: String) {
    let bundle = w
        .cli
        .authored
        .first()
        .expect("Trent authored a bundle")
        .0
        .clone();
    let path = pulled_tree(w, &bundle)
        .join("skills")
        .join(&skill)
        .join(&file);
    assert!(files::exists(&path), "no pulled {skill}/{file}");
    // Where the host has permission bits at all.
    if let Some(mode) = files::mode_of(&path) {
        assert_ne!(mode & 0o111, 0, "{skill}/{file} is {mode:o}");
    }
}

/// The hooks of `event` in the pulled `bundle`, as assembly reads them: each
/// hook file's stem, sorted by its sidecar's `order` (then by name).
fn pulled_hooks(w: &World, bundle: &str, event: &str) -> Vec<String> {
    let dir = pulled_tree(w, bundle).join("hooks").join(event);
    let mut hooks: Vec<(i64, String)> = files::children(&dir)
        .into_iter()
        .filter_map(|path| {
            let name = path.file_name()?.to_string_lossy().into_owned();
            let stem = name.strip_suffix(".yaml")?;
            if stem.starts_with('.') {
                return None;
            }
            let order = files::read(dir.join(format!(".{stem}.meta.yaml")))
                .ok()
                .and_then(|meta| {
                    angzarr_yaml::parse(&meta)
                        .ok()?
                        .get("order")?
                        .as_str()?
                        .parse()
                        .ok()
                })
                .unwrap_or(i64::MAX);
            Some((order, stem.to_string()))
        })
        .collect();
    hooks.sort();
    hooks.into_iter().map(|(_, name)| name).collect()
}

#[then(expr = "Alice's pulled {string} hooks under {string} are exactly {string} in that order")]
fn hooks_in_order(w: &mut World, bundle: String, event: String, names: String) {
    let want: Vec<&str> = names.split(',').map(str::trim).collect();
    assert_eq!(pulled_hooks(w, &bundle, &event), want, "{bundle} {event}");
}

#[then(expr = "no pulled {string} hook appears under an event it was not declared in")]
fn hooks_where_declared(w: &mut World, bundle: String) {
    let mut declared: Vec<(String, String)> = authored(w, &bundle)
        .iter()
        .filter_map(|(path, _)| {
            let rest = path.strip_prefix("hooks/")?;
            let (event, file) = rest.split_once('/')?;
            let stem = file.strip_suffix(".yaml")?;
            (!stem.starts_with('.')).then(|| (event.to_string(), stem.to_string()))
        })
        .collect();
    declared.sort();
    let hooks = pulled_tree(w, &bundle).join("hooks");
    let mut pulled: Vec<(String, String)> = Vec::new();
    for dir in files::children(&hooks) {
        let event = dir
            .file_name()
            .expect("a name")
            .to_string_lossy()
            .into_owned();
        pulled.extend(
            pulled_hooks(w, &bundle, &event)
                .into_iter()
                .map(|h| (event.clone(), h)),
        );
    }
    pulled.sort();
    assert_eq!(pulled, declared, "each hook under its declared event");
}
