//! Owner rulings 163, 164: the hooks ctxloom installs call the RUNNING
//! binary, by its file name, so a ctxloom installed under another name
//! (`ctxloom-rs`, beside the Go `ctxloom`) is the one its hooks run. The
//! binary under test is copied to `ctxloom-other` in a directory of its own
//! and run from there; nothing named `ctxloom-other` is anywhere else, so a
//! hook that resolves is this copy, found on the engine's `PATH` (the binary's
//! directory leads it).

use std::path::{Path, PathBuf};

use serde_json::Value;

use super::library::Project;
use crate::tests::files;

/// The name the copy is installed under.
const OTHER: &str = "ctxloom-other";

/// A copy of the binary under test as `<dir>/ctxloom-other`, once it runs.
fn installed_as_other(dir: &Path) -> PathBuf {
    let program = dir.join(OTHER);
    files::mkdirs(dir);
    files::copy(crate::tests::binary(), &program);
    // A file just written may still be held open for writing by a process a
    // thread of this one forked meanwhile (ETXTBSY): wait it out, bounded.
    for _ in 0..50 {
        match std::process::Command::new(&program)
            .arg("--version")
            .env_clear()
            .output()
        {
            Ok(_) => return program,
            Err(e) if e.raw_os_error() == Some(26) => {
                std::thread::sleep(std::time::Duration::from_millis(100));
            }
            Err(e) => panic!("{OTHER} does not run: {e}"),
        }
    }
    panic!("{OTHER} stayed busy");
}

/// The stub child: a `claude -p` turn that records the command its
/// `--settings` registers for `UserPromptSubmit`, runs it once with a prompt
/// payload (the engine's own environment, as claude runs a hook), records
/// its exit and stderr, and answers as claude's stream-json does.
const CHILD: &str = r#"here="$(dirname "$0")"
settings=""
while [ $# -gt 0 ]; do
  if [ "$1" = "--settings" ]; then settings="$2"; shift; fi
  shift
done
prompt="$(cat)"
cmd="$(jq -r '.hooks.UserPromptSubmit[0].hooks[0] | ([.command] + .args) | join(" ")' "$settings")"
echo "$cmd" > "$here/cmd"
printf '{"session_id":"9d764b45-a56a-48d7-ac9d-c2a16c5669f1","hook_event_name":"UserPromptSubmit","prompt_id":"p-1","prompt":"%s","cwd":"%s"}' "$prompt" "$PWD" \
  | sh -c "$cmd" > "$here/out" 2> "$here/err"
echo "$?" > "$here/code"
echo '{"type":"system","subtype":"init","session_id":"9d764b45-a56a-48d7-ac9d-c2a16c5669f1"}'
echo '{"type":"result","subtype":"success","is_error":false,"result":"HOOK-NAME-REPLY","session_id":"9d764b45-a56a-48d7-ac9d-c2a16c5669f1"}'
"#;

/// A project run by `ctxloom-other` (in its home's `lib/`, outside any
/// `PATH`), with a `dev` profile.
fn project() -> Project {
    let project = Project::initialized();
    let program = installed_as_other(&project.home().join("lib"));
    let project = project.run_by(program);
    project.bundle("demo");
    project.profile("dev", "demo");
    project
}

#[test]
fn test_a_one_shot_of_a_renamed_binary_registers_hooks_that_call_it_and_they_resolve() {
    let project = project();
    let bin = project.home().join("bin");
    files::put_program(bin.join("claude"), CHILD);
    let path = match crate::tests::host_var("PATH") {
        Some(host) => format!("{}:{}", bin.display(), host.to_string_lossy()),
        None => bin.display().to_string(),
    };
    let ran = project.run_with(
        &[
            ("PATH", &path),
            ("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-fixture-not-real"), // gitleaks:allow
        ],
        &[
            "run",
            "--profile",
            "dev",
            "--one-shot",
            "hello-from-the-renamed-binary",
            "--format",
            "json",
        ],
    );
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert!(ran.stdout.contains("HOOK-NAME-REPLY"), "{}", ran.said());
    let cmd = files::read(bin.join("cmd")).expect("the turn ran");
    assert_eq!(
        cmd.trim(),
        format!("{OTHER} hook claude-code execute UserPromptSubmit"),
        "the hook calls the running binary by its file name"
    );
    let err = files::read(bin.join("err")).unwrap_or_default();
    assert_eq!(
        files::read(bin.join("code")).unwrap_or_default().trim(),
        "0",
        "the hook resolved and answered: {err}"
    );
    assert!(!err.contains("not recorded"), "the hook recorded: {err}");
    // What the hook delivered is the session's transcript: the copy was the
    // hook, found on the engine's PATH.
    let sessions = project.dir().join(".ctxloom/sessions");
    let harps: Vec<String> = files::children(&sessions)
        .iter()
        .filter(|p| files::is_dir(p))
        .filter_map(|p| p.file_name().map(|n| n.to_string_lossy().into_owned()))
        .collect();
    let [harp] = harps.as_slice() else {
        panic!("one session: {harps:?}")
    };
    let shown = project.run(&["session", "transcript", "show", harp, "--format", "json"]);
    assert_eq!(shown.code, 0, "{}", shown.said());
    assert!(
        shown.stdout.contains("hello-from-the-renamed-binary"),
        "the prompt the hook delivered: {}",
        shown.said()
    );
}

/// The `(command, args)` of every hook entry `event` registers in `doc`.
fn commands_of(doc: &Value, event: &str) -> Vec<String> {
    doc["hooks"][event]
        .as_array()
        .into_iter()
        .flatten()
        .flat_map(|group| group["hooks"].as_array().cloned().unwrap_or_default())
        .map(|hook| {
            let mut words = vec![hook["command"].as_str().unwrap_or_default().to_string()];
            words.extend(
                hook["args"]
                    .as_array()
                    .into_iter()
                    .flatten()
                    .filter_map(|a| a.as_str().map(String::from)),
            );
            words.join(" ")
        })
        .collect()
}

#[test]
fn test_a_renamed_binary_materializes_hooks_and_a_statusline_that_call_it_and_checks_them_as_its_own(
) {
    let project = project();
    project.run(&["materialize", "--yes"]).succeeds();
    let text = project.read(".claude/settings.json").expect("settings");
    let doc: Value = serde_json::from_str(&text).expect("json");
    let session_start = commands_of(&doc, "SessionStart");
    assert!(
        session_start
            .iter()
            .any(|c| c == &format!("{OTHER} hook claude-code execute SessionStart")),
        "{text}"
    );
    assert!(
        !session_start.iter().any(|c| c.starts_with("ctxloom ")),
        "no hook calls the bare ctxloom: {text}"
    );
    assert_eq!(
        doc["statusLine"]["command"],
        format!("{OTHER} hook claude-code execute statusline"),
        "{text}"
    );
    let check = project.run(&["manage", "hooks", "check", "--format", "json"]);
    assert_eq!(check.code, 0, "{}", check.said());
    let claude = check.json()["backends"]
        .as_array()
        .into_iter()
        .flatten()
        .find(|row| row["backend"] == "claude-code")
        .cloned()
        .unwrap_or_else(|| panic!("a claude-code row: {}", check.said()));
    assert_eq!(
        (&claude["hooks_present"], &claude["statusline"]),
        (&Value::Bool(true), &Value::Bool(true)),
        "the renamed binary's hooks are its wiring: {claude}"
    );
}
