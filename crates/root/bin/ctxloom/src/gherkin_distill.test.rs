//! The steps of distill (plan step 5, finishes M3), as a child of the base
//! World: `cli/content_distill.feature`, `cli/editing.feature` and the
//! distilled-form Rule of `cli/atomic_writes.feature` (owner T30, S51).
//!
//! The distiller is a one-shot on no context (`run --no-context`, OD-38) of
//! a stub model: j001200's stub `claude` first on `PATH` ("the mock LLM"),
//! which writes down what it was asked (`model-input`) and answers what the
//! steps staged (`model-reply`). Nothing reaches a real model. A Background
//! that says "the mock LLM responds" stages that stub; the step that writes
//! the content to distill stages it again, the project's agent on Claude
//! Code.

use cucumber::{given, then, when};
use serde_json::Value;

use super::World;
use crate::tests::files;
use crate::tests::gherkin_common::CommonWorld;

/// What the World keeps for distill.
#[derive(Default)]
pub(super) struct State {
    /// What "the mock LLM responds" said last: the distiller's answer.
    pub(super) reply: Option<String>,
}

/// Where a bundle's tree is.
fn tree(bundle: &str) -> String {
    format!(".ctxloom/content/bundles/v2/{bundle}")
}

/// The answer the stub gives when no step said one: well over the 2% floor
/// of the sources here.
const PLAIN_ANSWER: &str = "DISTILLED-FORM-OF-rules: be kind; say why.";

impl World {
    /// The stub model, first on `PATH`, answering `reply`, with the token a
    /// child run needs; and the project's agent back on Claude Code.
    pub(super) fn distill_model(&mut self, reply: &str) {
        super::adopt::claude_code(self);
        super::adopt::token(self);
        let bin = self.adopt_scratch().join("bin");
        super::recall::stub_model(&bin);
        files::put(bin.join("model-reply"), reply);
        self.common.project.write(
            ".ctxloom/config.yaml",
            crate::tests::library::MINIMAL_CONFIG,
        );
    }

    /// The body of a distilled form, after its front matter; none, none.
    fn distilled_body(&self, kind_dir: &str, bundle: &str, name: &str) -> Option<String> {
        let text = self
            .common
            .project
            .read(&format!("{}/{kind_dir}/{name}.distilled.md", tree(bundle)))?;
        Some(
            match text
                .strip_prefix("---\n")
                .and_then(|t| t.split_once("\n---\n"))
            {
                Some((_, body)) => body.to_string(),
                None => text,
            },
        )
    }

    /// The assembled context the last command printed as json.
    fn assembled_context(&self) -> String {
        let ran = self.common.ran();
        let doc: Value = serde_json::from_str(&ran.stdout)
            .unwrap_or_else(|e| panic!("not json ({e}): {}", ran.said()));
        doc["context"].as_str().unwrap_or_default().to_string()
    }
}

/// The mock LLM's answer, kept for the steps that read the stored form.
pub(super) fn remember(w: &mut World, reply: &str) {
    w.distill.reply = Some(reply.to_string());
}

// --------------------------------------------------- content_distill.feature

/// `lore`: a long fragment whose content holds "dead-letter queue", which
/// nothing else in the project does, and a command `guidance` of its own
/// content; and the stub model answering what the Background said.
#[given(expr = "a bundle {string} with a long fragment {string}")]
fn long_fragment(w: &mut World, bundle: String, fragment: String) {
    let reply = w
        .distill
        .reply
        .clone()
        .unwrap_or_else(|| PLAIN_ANSWER.to_string());
    w.distill_model(&reply);
    let project = &w.common.project;
    let root = tree(&bundle);
    project.write(
        &format!("{root}/bundle.yaml"),
        "schema_version: 4\nversion: 1.0.0\n",
    );
    project.write(
        &format!("{root}/fragments/{fragment}.md"),
        &"When a webhook delivery fails, retry it with backoff, park it on the dead-letter queue after the last try, and tell the tenant; never drop a token or a cached vault secret.\n"
            .repeat(12),
    );
    project.write(
        &format!("{root}/prompts/guidance.md"),
        &"Operational guidance for reviewers: read the retries, the tokens, the tenants and the caches before approving.\n"
            .repeat(12),
    );
}

#[given("the distillation backend cannot start")]
fn backend_cannot_start(w: &mut World) {
    let bin = w.adopt_scratch().join("bin");
    files::put_program(
        bin.join("claude"),
        "echo 'claude: cannot start' >&2\nexit 1\n",
    );
}

#[then(
    regex = r#"^the distilled (fragment|command) "([^"]+)" in bundle "([^"]+)" is the distiller's answer$"#
)]
fn is_the_answer(w: &mut World, kind: String, name: String, bundle: String) {
    let dir = if kind == "fragment" {
        "fragments"
    } else {
        "prompts"
    };
    let reply = w
        .distill
        .reply
        .clone()
        .expect("the mock LLM was told an answer");
    let body = w
        .distilled_body(dir, &bundle, &name)
        .unwrap_or_else(|| panic!("no distilled {kind} {name}: {}", w.common.ran().said()));
    assert_eq!(body.trim(), reply.trim());
}

#[then(
    regex = r#"^the distilled fragment "([^"]+)" in bundle "([^"]+)" still contains "([^"]+)"$"#
)]
fn still_contains(w: &mut World, name: String, bundle: String, text: String) {
    let body = w
        .distilled_body("fragments", &bundle, &name)
        .unwrap_or_else(|| panic!("the distilled fragment {name} is gone"));
    assert!(body.contains(&text), "{body}");
}

#[then(regex = r#"^the fragment "([^"]+)" in bundle "([^"]+)" has no distilled rendering$"#)]
fn no_rendering(w: &mut World, name: String, bundle: String) {
    assert_eq!(w.distilled_body("fragments", &bundle, &name), None);
}

/// The stub model that works until it is asked to stop: it says it works
/// (its pid, written whole: the hold point a signal waits for), and on a
/// SIGTERM writes down that it was asked (`asked`) and exits.
const WORKING_MODEL: &str = r#"here="$(dirname "$0")"
trap 'echo asked > "$here/asked"; kill "$!" 2>/dev/null; exit 0' TERM
echo "$$" > "$here/pid.tmp" && mv "$here/pid.tmp" "$here/pid"
sleep 30 &
wait
"#;

#[given("the distiller works until it is asked to stop")]
fn distiller_works(w: &mut World) {
    let bin = w.adopt_scratch().join("bin");
    files::put_program(bin.join("claude"), WORKING_MODEL);
}

/// The command started, and once the distiller says it works (its pid
/// written: never a sleep), sent the signal; then waited for to its end.
#[when(
    regex = r#"^I run "([^"]+)", and ctxloom is sent a SIG(HUP|INT|TERM) while the distiller works$"#
)]
fn distill_signalled(w: &mut World, line: String, signal: String) {
    let bin = w.adopt_scratch().join("bin");
    w.before_line(&line);
    w.before_run();
    let words =
        crate::tests::gherkin_common::split(line.strip_prefix("ctxloom ").expect("a ctxloom line"));
    let args: Vec<&str> = words.iter().map(String::as_str).collect();
    let env: Vec<(&str, &str)> = w
        .common
        .env
        .iter()
        .map(|(k, v)| (k.as_str(), v.as_str()))
        .collect();
    let child = w.common.project.start_with(&env, &args);
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(60);
    while !files::exists(bin.join("pid")) {
        assert!(
            std::time::Instant::now() < deadline,
            "the distiller never said it works"
        );
        std::thread::sleep(std::time::Duration::from_millis(20));
    }
    let sent = std::process::Command::new("kill")
        .args([&format!("-{signal}"), &child.id().to_string()])
        .status()
        .expect("kill runs");
    assert!(sent.success(), "the SIG{signal} was sent");
    w.common.ran = Some(crate::tests::Ran::of(
        child.wait_with_output().expect("ctxloom finishes"),
    ));
}

#[then("the distiller was asked to stop, and it was gone before the command exited")]
fn distiller_stopped(w: &mut World) {
    let bin = w.adopt_scratch().join("bin");
    let asked = files::read(bin.join("asked")).unwrap_or_default();
    assert_eq!(
        asked.trim(),
        "asked",
        "the distiller was not asked to stop: {}",
        w.common.ran().said()
    );
    let pid = files::read(bin.join("pid")).expect("its pid");
    assert!(
        !crate::tests::procs::alive(pid.trim().parse().expect("a pid")),
        "the distiller ({}) outlived the command",
        pid.trim()
    );
}

// ------------------------------------------------------- editing.feature

#[given("a ctxloom project with a marker editor")]
fn marker_editor(w: &mut World) {
    w.common.project =
        crate::tests::library::Project::with_editor(crate::tests::library::Editor::Marker);
}

#[given(regex = r#"^a command "([^"]+)" in bundle "([^"]+)" with a fresh distilled form$"#)]
fn fresh_command(w: &mut World, name: String, bundle: String) {
    fresh(
        w,
        "prompts",
        &name,
        &bundle,
        &format!("COMMAND-BODY-{name}"),
    );
}

// ------------------------------------- atomic_writes.feature (T30, S51)

/// The source and distilled form of `name`, each carrying a marker of its
/// own, the form recording `digest` (none: no key).
fn plant(w: &World, dir: &str, name: &str, bundle: &str, source: &str, digest: Option<String>) {
    let project = &w.common.project;
    let root = tree(bundle);
    if project.read(&format!("{root}/bundle.yaml")).is_none() {
        project.bundle(bundle);
    }
    project.write(&format!("{root}/{dir}/{name}.md"), source);
    let form = format!("DISTILLED-FORM-OF-{name}: short.");
    let text = match digest {
        Some(digest) => library::distill::distilled_file(&digest, &form),
        None => format!("{form}\n"),
    };
    project.write(&format!("{root}/{dir}/{name}.distilled.md"), &text);
}

fn fresh(w: &World, dir: &str, name: &str, bundle: &str, source: &str) {
    let digest = library::distill::source_digest(source.as_bytes());
    plant(w, dir, name, bundle, source, Some(digest));
}

fn original(name: &str) -> String {
    format!(
        "ORIGINAL-TEXT-OF-{name}: {}\n",
        "say what you mean, and why it matters, every time. ".repeat(6)
    )
}

#[given(expr = "a fragment {string} in bundle {string} with a fresh distilled form")]
fn fresh_fragment(w: &mut World, name: String, bundle: String) {
    fresh(w, "fragments", &name, &bundle, &original(&name));
}

#[given(
    expr = "a fragment {string} in bundle {string} whose distilled form records no source digest"
)]
fn no_digest(w: &mut World, name: String, bundle: String) {
    w.distill_model(&format!("DISTILLED-FORM-OF-{name}: say what and why."));
    plant(w, "fragments", &name, &bundle, &original(&name), None);
}

#[given(expr = "the source of {string} is edited by hand")]
#[cucumber::when(expr = "the source of {string} is edited by hand")]
fn edited_by_hand(w: &mut World, name: String) {
    let project = &w.common.project;
    let path = project
        .tree(".ctxloom/content/bundles/v2")
        .into_keys()
        .find(|p| p.ends_with(&format!("/{name}.md")))
        .unwrap_or_else(|| panic!("no source of {name}"));
    let path = format!(".ctxloom/content/bundles/v2/{path}");
    let text = project.read(&path).expect("the source reads");
    project.write(&path, &format!("{text}EDITED-BY-HAND.\n"));
}

#[then(regex = r#"^"([^"]+)" says its distilled form is stale$"#)]
fn says_stale(w: &mut World, line: String) {
    w.before_run();
    w.common.run_lines(&format!("{line} --format json"), "");
    let ran = w.common.ran();
    assert_eq!(ran.code, 0, "{}", ran.said());
    let doc: Value = serde_json::from_str(&ran.stdout)
        .unwrap_or_else(|e| panic!("not json ({e}): {}", ran.said()));
    assert_eq!(doc["distilled"], "STALE", "{doc}");
}

#[then(expr = "the assembled context holds the distilled form of {string}")]
fn holds_distilled(w: &mut World, name: String) {
    let context = w.assembled_context();
    assert!(
        context.contains(&format!("DISTILLED-FORM-OF-{name}")),
        "{context}"
    );
    assert!(
        !context.contains(&format!("ORIGINAL-TEXT-OF-{name}")),
        "{context}"
    );
}

#[then(
    regex = r#"^the assembled context holds the original text of "([^"]+)", not its distilled form$"#
)]
fn holds_original(w: &mut World, name: String) {
    let context = w.assembled_context();
    assert!(
        context.contains(&format!("ORIGINAL-TEXT-OF-{name}")),
        "{context}"
    );
    assert!(
        !context.contains(&format!("DISTILLED-FORM-OF-{name}")),
        "{context}"
    );
}
