use std::collections::BTreeMap;

use library_application::Notice;
use run_ports::{Credentials, LinkState, LoginLinks, RecordsError};
use runs::RunRejection;

use super::*;

const CLAUDE: Credentials = Credentials {
    child_env: &["CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"],
    primary_env: &[
        "CLAUDE_CODE_OAUTH_TOKEN",
        "ANTHROPIC_API_KEY",
        "ANTHROPIC_AUTH_TOKEN",
    ],
    user_dir: ".claude",
    login_file: Some(".credentials.json"),
};

const TARGET: &str = "/home/u/.claude/.credentials.json";

fn env(pairs: &[(&str, &str)]) -> BTreeMap<String, String> {
    pairs
        .iter()
        .map(|(k, v)| (k.to_string(), v.to_string()))
        .collect()
}

/// The links of a session directory, held in memory: what is present on the
/// host, what is at the home's path, and what was linked.
struct Links {
    present: Vec<&'static str>,
    state: LinkState,
    fails: bool,
    made: Vec<(String, String, String)>,
}

impl Links {
    fn with_login() -> Links {
        Links {
            present: vec![TARGET],
            state: LinkState::Absent,
            fails: false,
            made: Vec::new(),
        }
    }
}

impl LoginLinks for Links {
    fn present(&self, path: &str) -> bool {
        self.present.contains(&path)
    }
    fn state(&self, _: &str, rel: &str) -> LinkState {
        assert_eq!(rel, "home/.credentials.json");
        self.state.clone()
    }
    fn link(&mut self, harp: &str, rel: &str, target: &str) -> Result<(), RecordsError> {
        if self.fails {
            return Err(RecordsError::Io {
                harp: harp.to_string(),
                rel: rel.to_string(),
                ext_err_txt: "denied".to_string(),
            });
        }
        self.made
            .push((harp.to_string(), rel.to_string(), target.to_string()));
        Ok(())
    }
}

fn shared(links: &mut Links, vars: &[(&str, &str)], os: &str) -> Option<Notice> {
    share_login(links, &CLAUDE.into(), &env(vars), os, "amber-swift-owl")
}

fn said(notice: Option<Notice>) -> (String, String) {
    match notice {
        Some(Notice::Finding { text, remedy }) => (text, remedy),
        other => panic!("a finding was expected, not {other:?}"),
    }
}

#[test]
fn a_child_with_no_env_credential_is_refused_naming_the_variables() {
    let refused = refuse_child("claude-code", &CLAUDE.into(), &env(&[("HOME", "/home/u")]));
    assert_eq!(
        refused,
        Some(RunRejection::NoCredential {
            engine: "claude-code".to_string(),
            variables: "CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_API_KEY".to_string(),
        })
    );
}

#[test]
fn an_empty_variable_is_no_credential() {
    let refused = refuse_child(
        "claude-code",
        &CLAUDE.into(),
        &env(&[("CLAUDE_CODE_OAUTH_TOKEN", "")]),
    );
    assert!(refused.is_some());
}

#[test]
fn either_variable_serves_a_child_and_the_primarys_own_does_not() {
    for name in ["CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"] {
        assert_eq!(
            refuse_child("claude-code", &CLAUDE.into(), &env(&[(name, "x")])),
            None
        );
    }
    // ANTHROPIC_AUTH_TOKEN authenticates the primary only.
    let only_primary = env(&[("ANTHROPIC_AUTH_TOKEN", "x")]);
    assert!(refuse_child("claude-code", &CLAUDE.into(), &only_primary).is_some());
}

#[test]
fn an_engine_that_needs_no_credential_refuses_nothing() {
    assert_eq!(
        refuse_child("fake", &Credentials::default().into(), &BTreeMap::new()),
        None
    );
}

#[test]
fn the_login_is_linked_into_the_home_when_nothing_else_authenticates() {
    let mut links = Links::with_login();
    let notice = shared(&mut links, &[("HOME", "/home/u")], "linux");
    assert!(notice.is_none());
    assert_eq!(
        links.made,
        vec![(
            "amber-swift-owl".to_string(),
            "home/.credentials.json".to_string(),
            TARGET.to_string()
        )]
    );
}

#[test]
fn any_of_the_three_env_credentials_means_no_link() {
    for name in [
        "CLAUDE_CODE_OAUTH_TOKEN",
        "ANTHROPIC_API_KEY",
        "ANTHROPIC_AUTH_TOKEN",
    ] {
        let mut links = Links::with_login();
        let notice = shared(&mut links, &[("HOME", "/home/u"), (name, "x")], "linux");
        assert!(notice.is_none(), "{name}");
        assert!(links.made.is_empty(), "{name}");
    }
}

#[test]
fn an_empty_env_credential_is_not_one() {
    let mut links = Links::with_login();
    shared(
        &mut links,
        &[("HOME", "/home/u"), ("ANTHROPIC_API_KEY", "")],
        "linux",
    );
    assert_eq!(links.made.len(), 1);
}

#[test]
fn a_missing_login_is_a_notice_and_no_link() {
    let mut links = Links::with_login();
    links.present.clear();
    let (text, remedy) = said(shared(&mut links, &[("HOME", "/home/u")], "linux"));
    assert!(text.contains("no login at ~/.claude/.credentials.json"));
    assert!(remedy.contains("/login"));
    assert!(remedy.contains("claude setup-token"));
    assert!(links.made.is_empty());
}

#[test]
fn no_home_is_no_login() {
    let mut links = Links::with_login();
    let (text, _) = said(shared(&mut links, &[], "linux"));
    assert!(text.contains("no login"));
    assert!(links.made.is_empty());
}

#[test]
fn a_link_already_right_is_left_alone() {
    let mut links = Links::with_login();
    links.state = LinkState::LinkTo(TARGET.to_string());
    assert!(shared(&mut links, &[("HOME", "/home/u")], "linux").is_none());
    assert!(links.made.is_empty());
}

#[test]
fn a_link_to_elsewhere_and_a_file_of_its_own_are_left_and_said() {
    for state in [
        LinkState::LinkTo("/elsewhere".to_string()),
        LinkState::Other,
    ] {
        let mut links = Links::with_login();
        links.state = state;
        let (text, _) = said(shared(&mut links, &[("HOME", "/home/u")], "linux"));
        assert!(text.contains("left as it is"));
        assert!(links.made.is_empty());
    }
}

#[test]
fn a_link_that_cannot_be_made_is_a_notice_not_an_error() {
    let mut links = Links::with_login();
    links.fails = true;
    let (text, remedy) = said(shared(&mut links, &[("HOME", "/home/u")], "linux"));
    assert!(text.contains("could not be shared"));
    assert!(remedy.contains("claude setup-token"));
}

#[test]
fn macos_and_windows_never_link_and_are_told_to_set_a_token() {
    for os in ["macos", "windows"] {
        assert!(!can_link(os));
        let mut links = Links::with_login();
        let (text, remedy) = said(shared(&mut links, &[("HOME", "/home/u")], os));
        assert!(text.contains(os), "{os}");
        assert!(remedy.contains("claude setup-token"), "{os}");
        assert!(remedy.contains("CLAUDE_CODE_OAUTH_TOKEN"), "{os}");
        assert!(links.made.is_empty(), "{os}");
    }
}

#[test]
fn linux_and_the_other_unixes_link() {
    for os in ["linux", "freebsd", "openbsd"] {
        assert!(can_link(os), "{os}");
    }
}

#[test]
fn an_engine_with_no_login_file_shares_nothing() {
    let mut links = Links::with_login();
    let none = Credentials {
        login_file: None,
        ..CLAUDE
    };
    let notice = share_login(
        &mut links,
        &none.into(),
        &env(&[("HOME", "/home/u")]),
        "linux",
        "h",
    );
    assert!(notice.is_none());
    assert!(links.made.is_empty());
}
