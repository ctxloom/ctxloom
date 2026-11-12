//! The doctor over fakes of the ports. Go: `cli/doctor.feature` (release/0.7).

use std::collections::BTreeMap;
use std::sync::Arc;
use std::sync::Mutex;

use auth_application::Auth;
use ctxloom_ports::{
    ConfigFiles, Executables, ExportedVars, Failure, GitIdentity, LayerText, Put, Scope, Source,
    UserFiles,
};

use crate::{Doctor, Status};

#[derive(Default)]
struct Mem {
    files: Mutex<BTreeMap<String, String>>,
    project: bool,
}

impl ConfigFiles for Mem {
    fn source(&self) -> Source {
        if self.project {
            Source::Project
        } else {
            Source::Home
        }
    }
    fn app_dir(&self) -> String {
        "/work/.ctxloom".to_string()
    }
    fn layers(&self) -> Result<Vec<LayerText>, Failure> {
        let files = self.files.lock().unwrap();
        Ok(
            ["/home/u/.ctxloom/config.yaml", "/work/.ctxloom/config.yaml"]
                .iter()
                .filter_map(|path| {
                    Some(LayerText {
                        path: path.to_string(),
                        text: files.get(*path)?.clone(),
                    })
                })
                .collect(),
        )
    }
    fn layer(&self, _: Scope) -> Result<Option<LayerText>, Failure> {
        Ok(self
            .files
            .lock()
            .unwrap()
            .get("/work/.ctxloom/config.yaml")
            .map(|text| LayerText {
                path: "/work/.ctxloom/config.yaml".to_string(),
                text: text.clone(),
            }))
    }
    fn layer_path(&self, _: Scope) -> String {
        "/work/.ctxloom/config.yaml".to_string()
    }
    fn write_layer(&self, _: Scope, _: &str) -> Result<(), Failure> {
        Ok(())
    }
}

impl UserFiles for Mem {
    fn exists(&self, path: &str) -> Result<bool, Failure> {
        Ok(self.files.lock().unwrap().contains_key(path))
    }
    fn is_dir(&self, _: &str) -> Result<bool, Failure> {
        Ok(false)
    }
    fn read_file(&self, path: &str) -> Result<Vec<u8>, Failure> {
        Ok(self.files.lock().unwrap()[path].clone().into_bytes())
    }
    fn read_tree(&self, _: &str) -> Result<Vec<Put>, Failure> {
        Ok(Vec::new())
    }
    fn write_file(&self, path: &str, bytes: &[u8], _: bool) -> Result<bool, Failure> {
        self.files
            .lock()
            .unwrap()
            .insert(path.to_string(), String::from_utf8(bytes.to_vec()).unwrap());
        Ok(true)
    }
    fn write_tree(&self, _: &str, _: &[Put]) -> Result<(), Failure> {
        Ok(())
    }
}

struct Git(bool);

impl Executables for Git {
    fn find(&self, name: &str) -> Option<String> {
        (self.0 && name == "git").then(|| "/usr/bin/git".to_string())
    }
}

/// Git's answer for the identity it commits with, known or not.
struct Identity(bool);

impl GitIdentity for Identity {
    fn is_known(&self) -> bool {
        self.0
    }
}

/// A launch that exports the claude token, or does not.
struct Token(bool);

impl ExportedVars for Token {
    fn is_exported(&self, name: &str) -> bool {
        self.0 && name == "CLAUDE_CODE_OAUTH_TOKEN"
    }
}

fn doctor(mem: &Arc<Mem>, git: bool) -> Doctor {
    doctor_with_token(mem, git, true)
}

fn doctor_with_token(mem: &Arc<Mem>, git: bool, token: bool) -> Doctor {
    doctor_with(mem, git, true, token)
}

fn doctor_with(mem: &Arc<Mem>, git: bool, identity: bool, token: bool) -> Doctor {
    Doctor::new(
        mem.clone(),
        mem.clone(),
        Arc::new(Git(git)),
        Arc::new(Identity(identity)),
        Auth::new(Arc::new(Token(token))),
        Some("/home/u"),
        "0.1.0",
    )
}

fn project() -> Arc<Mem> {
    let mem = Arc::new(Mem {
        project: true,
        ..Mem::default()
    });
    mem.files.lock().unwrap().insert(
        "/work/.ctxloom/config.yaml".to_string(),
        "schema_version: 8\n".to_string(),
    );
    mem
}

/// `mem` with a home config that says Go ctxloom is retired (ruling 162).
fn retired(mem: Arc<Mem>) -> Arc<Mem> {
    mem.files.lock().unwrap().insert(
        "/home/u/.ctxloom/config.yaml".to_string(),
        "schema_version: 9\nlegacy:\n  go: retired\n".to_string(),
    );
    mem
}

const GO_AND_RUST: &str = r#"{"hooks":{"PostToolUse":[{"matcher":"Edit|Write","hooks":[{"command":"ctxloom hook stamp-plan"}]}],"SessionStart":[{"hooks":[{"command":"ctxloom hook session-bind"},{"command":"ctxloom hook claude-code execute SessionStart"}]}]}}"#;

// Owner ruling 162: while Go ctxloom is present (the default, and said), a
// Go-era entry is an info row of its own, the stale row stays ok, and --fix
// leaves the file as it was.
#[test]
fn a_go_era_entry_is_reported_and_left_alone_while_go_is_present() {
    for home in [None, Some("schema_version: 9\nlegacy:\n  go: present\n")] {
        let mem = project();
        if let Some(home) = home {
            mem.files
                .lock()
                .unwrap()
                .insert("/home/u/.ctxloom/config.yaml".to_string(), home.to_string());
        }
        mem.files.lock().unwrap().insert(
            "/home/u/.claude/settings.json".to_string(),
            GO_AND_RUST.to_string(),
        );
        let doctor = doctor(&mem, true);
        let report = doctor.report(false);
        assert_eq!(status(&report, "DOCTOR-CHECK-STALE-HOOKS-n5"), Status::Ok);
        let go: Vec<&crate::Check> = report
            .checks
            .iter()
            .filter(|c| c.marker == "DOCTOR-CHECK-GO-ERA-HOOK-n6")
            .collect();
        assert_eq!(go.len(), 2, "{home:?}");
        assert!(go.iter().all(|c| c.status == Status::Info));
        assert!(go[0]
            .detail
            .contains("PostToolUse runs `ctxloom hook stamp-plan` (~/.claude/settings.json)"));
        assert!(go[1]
            .detail
            .contains("SessionStart runs `ctxloom hook session-bind`"));
        assert!(!report.failed());
        assert!(doctor.fix().unwrap().is_empty());
        assert_eq!(
            mem.files.lock().unwrap()["/home/u/.claude/settings.json"],
            GO_AND_RUST
        );
    }
}

// Once Go is retired, the same entries are stale, no info row is given, and
// --fix removes them and keeps the Rust-era entry.
#[test]
fn a_go_era_entry_is_stale_and_fixed_once_go_is_retired() {
    let mem = retired(project());
    mem.files.lock().unwrap().insert(
        "/home/u/.claude/settings.json".to_string(),
        GO_AND_RUST.to_string(),
    );
    let doctor = doctor(&mem, true);
    let report = doctor.report(false);
    assert_eq!(status(&report, "DOCTOR-CHECK-STALE-HOOKS-n5"), Status::Warn);
    assert!(report
        .checks
        .iter()
        .all(|c| c.marker != "DOCTOR-CHECK-GO-ERA-HOOK-n6"));
    let removed = doctor.fix().unwrap();
    let subs: Vec<&str> = removed.iter().map(|r| r.sub.as_str()).collect();
    assert_eq!(subs, ["stamp-plan", "session-bind"]);
    let kept = mem.files.lock().unwrap()["/home/u/.claude/settings.json"].clone();
    assert!(kept.contains("ctxloom hook claude-code execute SessionStart"));
    assert!(!kept.contains("stamp-plan") && !kept.contains("session-bind"));
}

// A config no run can read retires nothing: Go is taken as present, so a
// broken config never lets --fix delete Go's live hooks.
#[test]
fn a_refused_config_leaves_go_era_entries_alone() {
    let mem = project();
    mem.files.lock().unwrap().insert(
        "/home/u/.ctxloom/config.yaml".to_string(),
        "schema_version: 9\nlegacy:\n  go: retired\nnosuch: 1\n".to_string(),
    );
    mem.files.lock().unwrap().insert(
        "/home/u/.claude/settings.json".to_string(),
        GO_AND_RUST.to_string(),
    );
    let doctor = doctor(&mem, true);
    assert!(doctor.fix().unwrap().is_empty());
    assert_eq!(
        status(&doctor.report(false), "DOCTOR-CHECK-STALE-HOOKS-n5"),
        Status::Ok
    );
}

fn status(report: &crate::Report, marker: &str) -> Status {
    report
        .checks
        .iter()
        .find(|c| c.marker == marker)
        .unwrap_or_else(|| panic!("no {marker}"))
        .status
}

// doctor.feature: "Doctor reports every check with the shared marker vocabulary".
#[test]
fn the_report_carries_every_marker_and_none_fails_on_a_plain_project() {
    let report = doctor(&project(), true).report(false);
    for marker in [
        "DOCTOR-CHECK-DEPS-a1",
        "DOCTOR-CHECK-AGENTS-b2",
        "DOCTOR-CHECK-CREDENTIALS-m5",
        "DOCTOR-CHECK-VERSION-c3",
        "DOCTOR-CHECK-HOOKS-TRUST-d4",
        "DOCTOR-CHECK-SETUP-MARKER-e5",
        "DOCTOR-CHECK-GITIGNORE-f6",
        "DOCTOR-CHECK-MCP-INVOCATION-g7",
        "DOCTOR-CHECK-STALE-HOOKS-n5",
        "DOCTOR-CHECK-INGESTION-q7",
    ] {
        assert!(report.checks.iter().any(|c| c.marker == marker), "{marker}");
    }
    assert!(report.checks.iter().all(|c| !c.detail.is_empty()));
    assert!(!report.failed());
}

// doctor.feature: "The scoped report keeps the capability probes and drops
// the setup checks".
#[test]
fn deps_narrows_to_the_capability_probes() {
    let report = doctor(&project(), true).report(true);
    assert_eq!(report.checks.len(), 1);
    assert_eq!(report.checks[0].marker, "DOCTOR-CHECK-DEPS-a1");
}

// doctor.feature: "Doctor names a stale ctxloom hook entry and --fix removes
// exactly it" (the project's settings and the user's).
#[test]
fn fix_removes_the_stale_entries_of_the_project_and_the_user_and_then_the_row_is_ok() {
    let mem = retired(project());
    let stale = r#"{"hooks":{"SessionStart":[{"hooks":[{"command":"ctxloom hook inject-context abc"},{"command":"ctxloom hook claude-code execute SessionStart"}]}]}}"#;
    mem.files
        .lock()
        .unwrap()
        .insert("/work/.claude/settings.json".to_string(), stale.to_string());
    mem.files.lock().unwrap().insert(
        "/home/u/.claude/settings.json".to_string(),
        stale.to_string(),
    );
    let doctor = doctor(&mem, true);
    assert_eq!(
        status(&doctor.report(false), "DOCTOR-CHECK-STALE-HOOKS-n5"),
        Status::Warn
    );
    let removed = doctor.fix().unwrap();
    assert_eq!(removed.len(), 2);
    assert_eq!(
        status(&doctor.report(false), "DOCTOR-CHECK-STALE-HOOKS-n5"),
        Status::Ok
    );
    let kept = mem.files.lock().unwrap()["/work/.claude/settings.json"].clone();
    assert!(
        kept.contains("ctxloom hook claude-code execute SessionStart")
            && !kept.contains("inject-context")
    );
}

// The identity is git's answer where ctxloom runs, in her own environment
// (handtest D2): a `.gitconfig` in the doctor's home, which is ctxloom's home
// (`CTXLOOM_HOME`), is not read for it, either way.
#[test]
fn the_git_identity_is_gits_answer_and_never_the_homes_gitconfig() {
    let mem = project();
    mem.files.lock().unwrap().insert(
        "/home/u/.gitconfig".to_string(),
        "[user]\n\tname = A\n\temail = a@b.c\n".to_string(),
    );
    let unknown = doctor_with(&mem, true, false, true).report(true);
    assert_eq!(status(&unknown, "DOCTOR-CHECK-DEPS-a1"), Status::Warn);
    assert!(unknown.checks[0].detail.contains("has no identity"));
    let known = doctor_with(&project(), true, true, true).report(true);
    assert_eq!(status(&known, "DOCTOR-CHECK-DEPS-a1"), Status::Ok);
}

// No git on the PATH is said as that, whatever an identity would answer.
#[test]
fn without_git_the_row_says_git_is_missing() {
    let report = doctor_with(&project(), false, true, true).report(true);
    assert!(report.checks[0].detail.contains("not on the PATH"));
}

#[test]
fn without_a_project_the_marker_warns_and_project_files_are_not_read() {
    let mem = Arc::new(Mem::default());
    let report = doctor(&mem, false).report(false);
    assert_eq!(
        status(&report, "DOCTOR-CHECK-SETUP-MARKER-e5"),
        Status::Warn
    );
    assert_eq!(status(&report, "DOCTOR-CHECK-DEPS-a1"), Status::Warn);
}

const LAUNCHES: &str = r#"{"mcpServers":{"ctxloom":{"command":"ctxloom","args":["mcp","serve"]}}}"#;

fn mcp_detail(mem: &Arc<Mem>) -> (Status, String) {
    let report = doctor(mem, true).report(false);
    let row = report
        .checks
        .iter()
        .find(|c| c.marker == "DOCTOR-CHECK-MCP-INVOCATION-g7")
        .unwrap();
    (row.status, row.detail.clone())
}

// The files that register MCP servers: the project's `.mcp.json` and the
// user's `~/.claude.json`, and only those.
#[test]
fn the_project_mcp_file_is_read() {
    let mem = project();
    mem.files
        .lock()
        .unwrap()
        .insert("/work/.mcp.json".to_string(), LAUNCHES.to_string());
    let (status, detail) = mcp_detail(&mem);
    assert_eq!(status, Status::Warn);
    assert!(detail.contains("from .mcp.json "), "{detail}");
}

#[test]
fn the_user_mcp_file_is_read() {
    let mem = project();
    mem.files
        .lock()
        .unwrap()
        .insert("/home/u/.claude.json".to_string(), LAUNCHES.to_string());
    let (status, detail) = mcp_detail(&mem);
    assert_eq!(status, Status::Warn);
    assert!(detail.contains("from ~/.claude.json "), "{detail}");
}

#[test]
fn without_a_project_only_the_user_mcp_file_is_read() {
    let mem = Arc::new(Mem::default());
    mem.files
        .lock()
        .unwrap()
        .insert("/work/.mcp.json".to_string(), LAUNCHES.to_string());
    assert_eq!(mcp_detail(&mem).0, Status::Ok);
}

#[test]
fn a_report_fails_only_when_a_row_fails() {
    let row = |status| crate::Check {
        marker: "M",
        name: "n",
        status,
        detail: String::new(),
        remedy: String::new(),
    };
    let report = |statuses: &[Status]| crate::Report {
        checks: statuses.iter().map(|s| row(*s)).collect(),
    };
    assert!(report(&[Status::Ok, Status::Fail]).failed());
    assert!(!report(&[Status::Ok, Status::Warn, Status::Info]).failed());
}

// doctor.feature: "A missing agent credential is a fail row with run's fix,
// and exit 3": the project's config is what is checked, and the report fails.
#[test]
fn a_missing_credential_fails_the_report_and_an_exported_one_does_not() {
    let report = doctor_with_token(&project(), true, false).report(false);
    assert_eq!(status(&report, "DOCTOR-CHECK-CREDENTIALS-m5"), Status::Fail);
    assert!(report.failed());
    let report = doctor_with_token(&project(), true, true).report(false);
    assert_eq!(status(&report, "DOCTOR-CHECK-CREDENTIALS-m5"), Status::Ok);
    assert!(!report.failed());
}

// The home's config is a layer too: its `engines.claude-code.auth: login` stands where the
// project says nothing.
#[test]
fn the_homes_auth_mode_is_read_with_the_projects_config() {
    let mem = project();
    mem.files.lock().unwrap().insert(
        "/home/u/.ctxloom/config.yaml".to_string(),
        "schema_version: 9\nengines:\n  claude-code:\n    auth: login\n".to_string(),
    );
    let report = doctor_with_token(&mem, true, false).report(false);
    assert_eq!(status(&report, "DOCTOR-CHECK-CREDENTIALS-m5"), Status::Info);
}

fn row(report: &crate::Report, marker: &str) -> crate::Check {
    report
        .checks
        .iter()
        .find(|c| c.marker == marker)
        .unwrap_or_else(|| panic!("no {marker}"))
        .clone()
}

// Hand-test 2026-11-10: a config every run refuses (a key the schema does not
// know) is a fail row in the run's own words, and the report fails.
#[test]
fn a_config_every_run_refuses_fails_the_setup_marker_in_the_runs_words() {
    let mem = project();
    mem.files.lock().unwrap().insert(
        "/work/.ctxloom/config.yaml".to_string(),
        "schema_version: 9\nsync:\n  auto_sync: true\n".to_string(),
    );
    let report = doctor(&mem, true).report(false);
    let marker = row(&report, "DOCTOR-CHECK-SETUP-MARKER-e5");
    assert_eq!(marker.status, Status::Fail, "{marker:?}");
    assert!(
        marker
            .detail
            .contains("failed to load config: the config breaks its schema")
            && marker.detail.contains("unknown key `sync.auto_sync`")
            && marker.detail.contains("/work/.ctxloom/config.yaml"),
        "{}",
        marker.detail
    );
    assert_eq!(
        marker.remedy,
        "remove or rename the key in config.yaml (ctxloom config edit)"
    );
    assert!(report.failed());
}

// The home's layer is read as a run reads it, too.
#[test]
fn a_home_config_every_run_refuses_fails_the_setup_marker() {
    let mem = project();
    mem.files.lock().unwrap().insert(
        "/home/u/.ctxloom/config.yaml".to_string(),
        "schema_version: 9\nzzz: 1\n".to_string(),
    );
    let marker = row(
        &doctor(&mem, true).report(false),
        "DOCTOR-CHECK-SETUP-MARKER-e5",
    );
    assert_eq!(marker.status, Status::Fail);
    assert!(
        marker.detail.contains("/home/u/.ctxloom/config.yaml"),
        "{}",
        marker.detail
    );
}

// Hand-test 2026-11-10: a project whose private state git can see (no
// nested `.ctxloom/.gitignore`, nothing in the root one) is a warning naming
// what is exposed; the nested rules `manage gitignore install` writes, or
// the same rules in the root `.gitignore`, keep it quiet.
#[test]
fn private_state_git_can_see_is_a_gitignore_warning() {
    let mem = project();
    let check = row(
        &doctor(&mem, true).report(false),
        "DOCTOR-CHECK-GITIGNORE-f6",
    );
    assert_eq!(check.status, Status::Warn, "{check:?}");
    assert!(
        check.detail.contains("/sessions") && check.detail.contains("/cache"),
        "{}",
        check.detail
    );
    assert_eq!(check.remedy, "ctxloom manage gitignore install");

    mem.files.lock().unwrap().insert(
        "/work/.ctxloom/.gitignore".to_string(),
        "# ctxloom\n/cache\n/sessions\n/project-id\n/state\n*.lock\n".to_string(),
    );
    let check = row(
        &doctor(&mem, true).report(false),
        "DOCTOR-CHECK-GITIGNORE-f6",
    );
    assert_eq!(check.status, Status::Ok, "{check:?}");
}

#[test]
fn private_state_ignored_from_the_root_gitignore_is_quiet() {
    let mem = project();
    mem.files.lock().unwrap().insert(
        "/work/.gitignore".to_string(),
        ".ctxloom/cache/\n/.ctxloom/sessions\n.ctxloom/project-id\n.ctxloom/state/\n*.lock\n"
            .to_string(),
    );
    let check = row(
        &doctor(&mem, true).report(false),
        "DOCTOR-CHECK-GITIGNORE-f6",
    );
    assert_eq!(check.status, Status::Ok, "{check:?}");
}

// One pattern left out is named alone.
#[test]
fn a_private_state_pattern_left_out_is_named() {
    let mem = project();
    mem.files.lock().unwrap().insert(
        "/work/.ctxloom/.gitignore".to_string(),
        "/cache\n/project-id\n/state\n*.lock\n".to_string(),
    );
    let check = row(
        &doctor(&mem, true).report(false),
        "DOCTOR-CHECK-GITIGNORE-f6",
    );
    assert_eq!(check.status, Status::Warn);
    assert!(check.detail.contains("/sessions"), "{}", check.detail);
    assert!(!check.detail.contains("/cache"), "{}", check.detail);
}

// With no HOME of hers known, none of her files is read: never a fallback to
// another home (CTXLOOM_HOME's), which is the two-homes defect.
#[test]
fn without_her_home_none_of_her_files_is_read() {
    let mem = retired(project());
    let stale =
        r#"{"hooks":{"SessionStart":[{"hooks":[{"command":"ctxloom hook inject-context abc"}]}]}}"#;
    mem.files.lock().unwrap().insert(
        "/home/u/.claude/settings.json".to_string(),
        stale.to_string(),
    );
    let doctor = Doctor::new(
        mem.clone(),
        mem.clone(),
        Arc::new(Git(true)),
        Arc::new(Identity(true)),
        Auth::new(Arc::new(Token(true))),
        None,
        "0.1.0",
    );
    let report = doctor.report(false);
    assert_eq!(status(&report, "DOCTOR-CHECK-STALE-HOOKS-n5"), Status::Ok);
    assert!(report
        .checks
        .iter()
        .all(|c| !c.detail.contains("~/.claude")));
    assert!(doctor_with(&mem, true, true, true)
        .report(false)
        .checks
        .iter()
        .any(|c| c.marker == "DOCTOR-CHECK-STALE-HOOKS-n5" && c.status == Status::Warn));
}
