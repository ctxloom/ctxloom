//! Acceptance: the hidden `hook` commands, through the real binary. Ported
//! from Go's tests/acceptance/features/cli/session_hooks.feature
//! (release/0.7): each scenario is named in a comment.
//!
//! These cover the machine contract: a hook warns and exits 0 on what it can
//! foresee, and stdout is the engine's input, so the stdout assertions parse
//! stdout ALONE, never the combined stream.

use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};

use super::{binary, Env, Ran};
use crate::tests::files;

impl Env {
    /// Run `ctxloom args` with `stdin` on its standard input and `vars` in its
    /// environment (this home, and nothing else of the caller's).
    pub(crate) fn run_hook(&self, args: &[&str], stdin: &str, vars: &[(&str, &str)]) -> Ran {
        let mut command = Command::new(binary());
        command
            .args(args)
            .env_clear()
            .env("HOME", self.home())
            .env("USERPROFILE", self.home())
            .env("TMPDIR", crate::tests::temp_root())
            .current_dir(self.home())
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        for name in ["PATH", "SystemRoot"] {
            if let Some(value) = crate::tests::host_var(name) {
                command.env(name, value);
            }
        }
        for (name, value) in vars {
            command.env(name, value);
        }
        let mut child = command.spawn().expect("ctxloom runs");
        crate::tests::feed(child.stdin.take().expect("stdin"), stdin);
        Ran::of(child.wait_with_output().expect("ctxloom finishes"))
    }
}

impl Env {
    /// Run `ctxloom args` as the interactive engine's hook process runs it:
    /// no `HOME` (nor `USERPROFILE`), `vars` and the engine's `PATH` only.
    pub(crate) fn run_hook_without_home(
        &self,
        args: &[&str],
        stdin: &str,
        vars: &[(&str, &str)],
    ) -> Ran {
        let mut command = Command::new(binary());
        command
            .args(args)
            .env_clear()
            .env("TMPDIR", crate::tests::temp_root())
            .current_dir(self.home())
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        for name in ["PATH", "SystemRoot"] {
            if let Some(value) = crate::tests::host_var(name) {
                command.env(name, value);
            }
        }
        for (name, value) in vars {
            command.env(name, value);
        }
        let mut child = command.spawn().expect("ctxloom runs");
        crate::tests::feed(child.stdin.take().expect("stdin"), stdin);
        Ran::of(child.wait_with_output().expect("ctxloom finishes"))
    }
}

/// Every path under `dir`, relative to it, sorted.
fn tree(dir: &Path) -> Vec<PathBuf> {
    fn walk(dir: &Path, root: &Path, found: &mut Vec<PathBuf>) {
        for path in files::children(dir) {
            found.push(
                path.strip_prefix(root)
                    .expect("under the root")
                    .to_path_buf(),
            );
            if files::is_dir(&path) {
                walk(&path, root, found);
            }
        }
    }
    let mut found = Vec::new();
    walk(dir, dir, &mut found);
    found.sort();
    found
}

const HUD_PAYLOAD: &str =
    r#"{"model":{"display_name":"Opus 5"},"context_window":{"used_percentage":42}}"#;

// Go's line, on the wire: no trailing newline (fmt.Print), the glyph and the
// separator.
#[test]
fn test_hud_prints_gos_line_without_a_newline() {
    let env = Env::new();
    let ran = env.run_hook(
        &["hook", "claude-code", "execute", "statusline"],
        HUD_PAYLOAD,
        &[("CTXLOOM_SESSION_HARP", "brisk-copper-moth")],
    );
    assert!(
        ran.stdout
            .starts_with("\u{1b}[36mOpus 5\u{1b}[0m \u{2502} "),
        "{:?}",
        ran.stdout
    );
    assert!(
        ran.stdout.contains("\u{2301} brisk-copper-moth"),
        "{:?}",
        ran.stdout
    );
    assert!(!ran.stdout.ends_with('\n'), "{:?}", ran.stdout);
}

// D5 (2026-11-02): the HUD hook records no context sample. Go wrote
// context-metrics.jsonl under the session's directory from here.
#[test]
fn test_hud_records_nothing() {
    let env = Env::new();
    let before = tree(env.home());
    let ran = env.run_hook(
        &["hook", "claude-code", "execute", "statusline"],
        HUD_PAYLOAD,
        &[("CTXLOOM_SESSION_HARP", "brisk-copper-moth")],
    );
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert_eq!(tree(env.home()), before, "the home is as it was");
}

// The warning rides the diagnostic channel in the format the process chose:
// an explicit json format makes it an envelope, still on stderr.
#[test]
fn test_session_start_warning_is_an_envelope_under_json() {
    let env = Env::new();
    let ran = env.run_hook(
        &[
            "--format",
            "json",
            "hook",
            "claude-code",
            "execute",
            "SessionStart",
        ],
        "{}",
        &[],
    );
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert_eq!(ran.stdout, "");
    assert_eq!(ran.err_json()["code"], "hook-cli-no-usable-harp");
}

// session_hooks.feature: "The retired turn-changed callback is not a command".
#[test]
fn test_the_retired_turn_changed_hook_is_not_a_command() {
    let env = Env::new();
    let ran = env.run_hook(
        &["hook", "turn-changed"],
        r#"{"hook_event_name":"Stop"}"#,
        &[],
    );
    assert_eq!(ran.code, 2, "{}", ran.said());
}

// Go: the hook namespace is hidden ("Machine callback ... not for direct
// use"), but answers -h.
#[test]
fn test_hook_is_hidden_from_the_root_help_and_answers_help() {
    let env = Env::new();
    let root = env.run(&["-h"]);
    assert_eq!(root.code, 0, "{}", root.said());
    assert!(
        !root
            .stdout
            .lines()
            .any(|l| l.trim_start().starts_with("hook")),
        "{}",
        root.stdout
    );
    let hook = env.run(&["hook", "-h"]);
    assert_eq!(hook.code, 0, "{}", hook.said());
}

// ---------------------------------------------------------------------------
// `hook <engine> execute <hook-name>`, the one form
// (owner, 2026-11-08). The engine is validated by the registry, the hook name
// is the engine's native event; the retired `session-bind` and `hud` are
// `hook claude-code execute SessionStart` and `... statusline`.

#[test]
fn test_execute_statusline_renders_the_engines_half_and_ctxloom_s_own() {
    let env = Env::new();
    let ran = env.run_hook(
        &["hook", "claude-code", "execute", "statusline"],
        HUD_PAYLOAD,
        &[("CTXLOOM_SESSION_HARP", "brisk-copper-moth")],
    );
    assert_eq!(ran.code, 0, "{}", ran.said());
    for want in ["Opus 5", "42%", "brisk-copper-moth"] {
        assert!(ran.stdout.contains(want), "{want:?} in {}", ran.said());
    }
    assert_eq!(ran.stderr, "", "a diagnostic would corrupt the bar");
    assert!(!ran.stdout.ends_with('\n'));
}

#[test]
fn test_execute_statusline_on_garbage_is_the_sentinel_and_exit_0() {
    let env = Env::new();
    let ran = env.run_hook(
        &["hook", "claude-code", "execute", "statusline"],
        "not json",
        &[],
    );
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert_eq!(ran.stdout, "ctxloom");
}

// The typed refusal lists the registry's names; nothing is guessed.
#[test]
fn test_execute_under_an_unknown_engine_is_refused_listing_the_known_ones() {
    let env = Env::new();
    let ran = env.run_hook(
        &[
            "--format",
            "json",
            "hook",
            "no-such-engine",
            "execute",
            "Stop",
        ],
        "{}",
        &[],
    );
    assert_eq!(ran.code, 1, "{}", ran.said());
    assert_eq!(ran.stdout, "");
    let err = ran.err_json();
    assert_eq!(err["code"], "setup-application-unknown-hook-engine");
    assert_eq!(err["args"]["engine"], "no-such-engine");
    assert!(
        err["args"]["known"]
            .as_str()
            .unwrap()
            .contains("claude-code"),
        "{err}"
    );
}

// The events that carry the primary agent's data are accepted and silent (M2
// records them as facts).
#[test]
fn test_execute_of_the_data_events_succeeds_silently() {
    let env = Env::new();
    let fixtures = [
        (
            "Stop",
            r#"{"session_id":"s","prompt_id":"p","hook_event_name":"Stop","last_assistant_message":"done"}"#,
        ),
        (
            "MessageDisplay",
            r#"{"session_id":"s","prompt_id":"p","hook_event_name":"MessageDisplay","turn_id":"t","message_id":"m","index":0,"final":true,"delta":"done"}"#,
        ),
        (
            "PostToolBatch",
            r#"{"session_id":"s","prompt_id":"p","hook_event_name":"PostToolBatch","tool_calls":[]}"#,
        ),
    ];
    for (native, payload) in fixtures {
        let ran = env.run_hook(
            &["hook", "claude-code", "execute", native],
            payload,
            &[("CTXLOOM_SESSION_HARP", "brisk-copper-moth")],
        );
        assert_eq!(ran.code, 0, "{native}: {}", ran.said());
        assert_eq!(ran.stdout.trim(), "{}", "{native}");
        assert_eq!(ran.stderr, "", "{native}");
    }
}

// session_hooks.feature: "With no active harp the hook stays silent on stdout
// and warns instead", now as SessionStart.
#[test]
fn test_execute_session_start_with_no_harp_is_silent_on_stdout_and_warns() {
    let env = Env::new();
    let ran = env.run_hook(
        &["hook", "claude-code", "execute", "SessionStart"],
        r#"{"session_id":"vendor-session-42","hook_event_name":"SessionStart"}"#,
        &[],
    );
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert_eq!(ran.stdout, "", "nothing truthful to emit");
    assert!(ran.stderr.contains("no usable harp"), "{}", ran.said());
}

// The hook name is a verb of the engine, not of ctxloom: the old verbs and the
// `--engine` flag are not commands.
#[test]
fn test_the_old_hook_verbs_are_not_commands() {
    let env = Env::new();
    for line in [
        vec!["hook", "hud"],
        vec!["hook", "session-bind", "--engine", "claude-code"],
        vec!["hook", "session-start", "--engine", "claude-code"],
        vec!["hook", "skill-mates", "--engine", "claude-code"],
    ] {
        let ran = env.run_hook(&line, "{}", &[]);
        assert_eq!(ran.code, 2, "{line:?}: {}", ran.said());
    }
}

#[test]
fn test_hook_help_names_execute() {
    let env = Env::new();
    let hook = env.run(&["hook", "-h"]);
    assert_eq!(hook.code, 0, "{}", hook.said());
    assert!(hook.stdout.contains("execute"), "{}", hook.stdout);
    assert!(!hook.stdout.contains("session-bind"), "{}", hook.stdout);
}

// Owner ruling 2026-11-08: the interactive engine's env carries no HOME, so a
// hook process finds ctxloom's home through CTXLOOM_HOME, and answers as it
// does with a HOME.
#[test]
fn test_a_hook_process_without_home_finds_the_home_through_ctxloom_home() {
    let env = Env::new();
    let home = env.home().to_string_lossy().to_string();
    let payload = r#"{"session_id":"s","prompt_id":"p","hook_event_name":"Stop","last_assistant_message":"done"}"#;
    let ran = env.run_hook_without_home(
        &["hook", "claude-code", "execute", "Stop"],
        payload,
        &[("CTXLOOM_HOME", &home)],
    );
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert_eq!(
        ran.stdout.trim(),
        "{}",
        "a data event answers the empty response"
    );
    assert_eq!(ran.stderr, "", "no error: the home was found");
    let start = env.run_hook_without_home(
        &["hook", "claude-code", "execute", "SessionStart"],
        "{}",
        &[("CTXLOOM_HOME", &home)],
    );
    assert_eq!(start.code, 0, "{}", start.said());
    assert!(!start.stderr.contains("no home"), "{}", start.said());
}

// Without HOME and without the variable there is no home to find, and the
// refusal says which variables were looked for.
#[test]
fn test_a_hook_process_with_no_home_at_all_is_refused_naming_both_variables() {
    let env = Env::new();
    let ran = env.run_hook_without_home(
        &["--format", "json", "hook", "claude-code", "execute", "Stop"],
        "{}",
        &[],
    );
    assert_eq!(ran.code, 1, "{}", ran.said());
    let err = ran.err_json();
    assert_eq!(err["code"], "ctxloom-wiring-no-home");
    assert!(
        err["message"].as_str().unwrap().contains("CTXLOOM_HOME"),
        "{err}"
    );
}
