use std::collections::BTreeMap;

use library_application::Notice;
use run_ports::{AuthSetting, Credentials, LinkState, LoginLinks, RecordsError};
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
    sign_in: "sign in with /login in the session, or run `claude setup-token`, then export CLAUDE_CODE_OAUTH_TOKEN",
    no_link_on: &["macos", "windows"],
    ..Credentials::NONE
};

const TARGET: &str = "/home/u/.claude/.credentials.json";

/// opencode's (ruling 69): its login is in its data directory, which the
/// session home relocates under `xdg/data`, and it runs without one.
const OPENCODE: Credentials = Credentials {
    user_dir: ".local/share/opencode",
    user_dir_variable: Some(("XDG_DATA_HOME", "opencode")),
    login_file: Some("auth.json"),
    home_dir: "xdg/data/opencode",
    sign_in: "run `opencode auth login`",
    login_optional: true,
    copy_on: &["windows"],
    ..Credentials::NONE
};

const OPENCODE_TARGET: &str = "/home/u/.local/share/opencode/auth.json";
const OPENCODE_REL: &str = "home/xdg/data/opencode/auth.json";

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
    copy_fails: bool,
    copied: Vec<(String, String, String)>,
    /// The one path of the session directory the link may be asked about.
    rel: &'static str,
}

impl Links {
    fn with_login() -> Links {
        Links {
            present: vec![TARGET],
            state: LinkState::Absent,
            fails: false,
            made: Vec::new(),
            copy_fails: false,
            copied: Vec::new(),
            rel: "home/.credentials.json",
        }
    }

    fn with_opencode_login(at: &'static str) -> Links {
        Links {
            present: vec![at],
            rel: OPENCODE_REL,
            ..Links::with_login()
        }
    }
}

impl LoginLinks for Links {
    fn present(&self, path: &str) -> bool {
        self.present.contains(&path)
    }
    fn state(&self, _: &str, rel: &str) -> LinkState {
        assert_eq!(rel, self.rel);
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
    fn copy(&mut self, harp: &str, rel: &str, source: &str) -> Result<(), RecordsError> {
        if self.copy_fails {
            return Err(RecordsError::Io {
                harp: harp.to_string(),
                rel: rel.to_string(),
                ext_err_txt: "no room".to_string(),
            });
        }
        self.copied
            .push((harp.to_string(), rel.to_string(), source.to_string()));
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

fn file_only() -> AuthSetting {
    AuthSetting::Source {
        env: Vec::new(),
        file: Some("~/.claude/.credentials.json".to_string()),
    }
}

// Ruling 53: a source that is a file alone can authenticate no child, so it is
// refused naming the engine and the variables its built-in source accepts.
#[test]
fn a_child_on_a_file_only_source_is_refused_naming_the_builtin_variables() {
    assert_eq!(
        refuse_file_only("claude-code", &file_only(), &CLAUDE),
        Some(RunRejection::FileOnlyCredential {
            engine: "claude-code".to_string(),
            variables: "CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_API_KEY".to_string(),
        })
    );
}

// An engine whose adapter names no child variable is told the library's
// built-in source (opencode's provider keys, rulings 69 and 79).
#[test]
fn an_engine_with_no_adapter_variable_names_its_builtin_source() {
    assert_eq!(
        refuse_file_only("opencode", &file_only(), &Credentials::default()),
        Some(RunRejection::FileOnlyCredential {
            engine: "opencode".to_string(),
            variables: "ANTHROPIC_API_KEY, OPENAI_API_KEY".to_string(),
        })
    );
}

// A source with any variable, or no file, is the environment check's (or
// nobody's): the file-only refusal is for a file and nothing else.
#[test]
fn a_source_with_a_variable_or_without_a_file_is_not_file_only() {
    let with_env = AuthSetting::Source {
        env: vec!["TEAM_TOKEN".to_string()],
        file: Some("~/.claude/.credentials.json".to_string()),
    };
    let no_file = AuthSetting::Source {
        env: Vec::new(),
        file: None,
    };
    for auth in [
        with_env,
        no_file,
        AuthSetting::Builtin,
        AuthSetting::None,
        AuthSetting::Login,
    ] {
        assert_eq!(
            refuse_file_only("claude-code", &auth, &CLAUDE),
            None,
            "{auth:?}"
        );
    }
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
    assert_eq!(remedy, CLAUDE.sign_in);
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

// Ruling 80: the platforms that never link are the engine's: claude's.
#[test]
fn macos_and_windows_never_link_and_are_told_to_set_a_token() {
    for os in ["macos", "windows"] {
        assert!(!links_on(&CLAUDE.into(), os));
        let mut links = Links::with_login();
        let (text, remedy) = said(shared(&mut links, &[("HOME", "/home/u")], os));
        assert!(text.contains(os), "{os}");
        assert!(remedy.contains("claude setup-token"), "{os}");
        assert!(remedy.contains("CLAUDE_CODE_OAUTH_TOKEN"), "{os}");
        assert_eq!(remedy, CLAUDE.sign_in, "{os}");
        assert!(links.made.is_empty(), "{os}");
        assert!(links.copied.is_empty(), "{os}");
    }
}

#[test]
fn linux_and_the_other_unixes_link() {
    for os in ["linux", "freebsd", "openbsd"] {
        assert!(links_on(&CLAUDE.into(), os), "{os}");
        assert!(links_on(&OPENCODE.into(), os), "{os}");
    }
}

fn opencode_on(links: &mut Links, os: &str) -> Option<Notice> {
    share_login(
        links,
        &OPENCODE.into(),
        &env(&[("HOME", "/home/u")]),
        os,
        "amber-swift-owl",
    )
}

// Ruling 80: opencode keeps its login in a file on macOS too (no keychain),
// so it links there as on linux.
#[test]
fn opencode_links_its_login_on_macos() {
    assert!(links_on(&OPENCODE.into(), "macos"));
    let mut links = Links::with_opencode_login(OPENCODE_TARGET);
    assert!(opencode_on(&mut links, "macos").is_none());
    assert_eq!(links.made.len(), 1);
    assert!(links.copied.is_empty());
}

// Ruling 80: on Windows opencode tries a link first, and one made is all.
#[test]
fn opencode_on_windows_links_when_a_link_can_be_made() {
    assert!(links_on(&OPENCODE.into(), "windows"));
    let mut links = Links::with_opencode_login(OPENCODE_TARGET);
    assert!(opencode_on(&mut links, "windows").is_none());
    assert_eq!(links.made.len(), 1);
    assert!(links.copied.is_empty());
}

// Ruling 80: on Windows a link that cannot be made is a copy of the login,
// and the notice says the cost: a refresh in the session rotates the copy's
// token, which can sign her own opencode out.
#[test]
fn opencode_on_windows_copies_its_login_when_no_link_can_be_made_and_says_the_cost() {
    let mut links = Links::with_opencode_login(OPENCODE_TARGET);
    links.fails = true;
    let (text, remedy) = said(opencode_on(&mut links, "windows"));
    assert_eq!(
        links.copied,
        vec![(
            "amber-swift-owl".to_string(),
            OPENCODE_REL.to_string(),
            OPENCODE_TARGET.to_string()
        )]
    );
    assert!(text.contains("copied"), "{text}");
    assert!(text.contains("sign your own opencode out"), "{text}");
    assert_eq!(remedy, "run `opencode auth login`");
}

// A copy that fails too is the link's failure, said as one.
#[test]
fn a_copy_that_cannot_be_made_is_said_as_a_login_not_shared() {
    let mut links = Links::with_opencode_login(OPENCODE_TARGET);
    links.fails = true;
    links.copy_fails = true;
    let (text, _) = said(opencode_on(&mut links, "windows"));
    assert!(text.contains("could not be shared"), "{text}");
    assert!(links.copied.is_empty());
}

// Only where the engine says so is a failed link a copy: elsewhere it is
// said, and nothing is copied.
#[test]
fn a_failed_link_is_no_copy_where_the_engine_does_not_copy() {
    for os in ["linux", "macos"] {
        let mut links = Links::with_opencode_login(OPENCODE_TARGET);
        links.fails = true;
        let (text, _) = said(opencode_on(&mut links, os));
        assert!(text.contains("could not be shared"), "{os}: {text}");
        assert!(links.copied.is_empty(), "{os}");
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

fn opencode_shared(links: &mut Links, vars: &[(&str, &str)]) -> Option<Notice> {
    share_login(
        links,
        &OPENCODE.into(),
        &env(vars),
        "linux",
        "amber-swift-owl",
    )
}

// Ruling 69: opencode's primary links the user's own auth.json (never a copy:
// a refresh rotates the token in place) where its relocated data directory is.
#[test]
fn opencodes_login_is_linked_into_the_relocated_data_directory() {
    let mut links = Links::with_opencode_login(OPENCODE_TARGET);
    assert!(opencode_shared(&mut links, &[("HOME", "/home/u")]).is_none());
    assert_eq!(
        links.made,
        vec![(
            "amber-swift-owl".to_string(),
            OPENCODE_REL.to_string(),
            OPENCODE_TARGET.to_string()
        )]
    );
}

// opencode reads its data directory from XDG_DATA_HOME when it is set: the
// link is to the login opencode itself would use. A relative value is no
// directory (the XDG rule), so the home's default stands.
#[test]
fn xdg_data_home_moves_the_login_the_primary_shares() {
    let mut links = Links::with_opencode_login("/data/opencode/auth.json");
    let vars = [("HOME", "/home/u"), ("XDG_DATA_HOME", "/data")];
    assert!(opencode_shared(&mut links, &vars).is_none());
    assert_eq!(links.made[0].2, "/data/opencode/auth.json");
    let mut links = Links::with_opencode_login(OPENCODE_TARGET);
    let vars = [("HOME", "/home/u"), ("XDG_DATA_HOME", "data")];
    assert!(opencode_shared(&mut links, &vars).is_none());
    assert_eq!(links.made[0].2, OPENCODE_TARGET);
    let mut links = Links::with_opencode_login(OPENCODE_TARGET);
    let vars = [("HOME", "/home/u"), ("XDG_DATA_HOME", "")];
    assert!(opencode_shared(&mut links, &vars).is_none());
    assert_eq!(links.made[0].2, OPENCODE_TARGET);
}

// opencode runs without a login (its free models): none at its default place
// is nothing to say, and nothing is linked.
#[test]
fn an_optional_login_missing_from_its_default_place_is_nothing_to_say() {
    let mut links = Links::with_opencode_login(OPENCODE_TARGET);
    links.present.clear();
    assert!(opencode_shared(&mut links, &[("HOME", "/home/u")]).is_none());
    assert!(links.made.is_empty());
}

// A login file the config names is asked for, so its absence is said, with
// the engine's own way to sign in.
#[test]
fn a_configured_login_that_is_missing_is_said_even_when_logins_are_optional() {
    let mut links = Links::with_opencode_login(OPENCODE_TARGET);
    links.present.clear();
    let settings = run_ports::EngineSettings {
        auth: AuthSetting::Source {
            env: Vec::new(),
            file: Some("~/team/auth.json".to_string()),
        },
        ..run_ports::EngineSettings::default()
    };
    let (text, remedy) = said(share_login(
        &mut links,
        &settings.credentials(OPENCODE),
        &env(&[("HOME", "/home/u")]),
        "linux",
        "amber-swift-owl",
    ));
    assert!(text.contains("/home/u/team/auth.json"), "{text}");
    assert_eq!(remedy, "run `opencode auth login`");
    assert!(links.made.is_empty());
}

// What the human is told is the engine's own way to sign in, wherever no
// login is shared: on a platform it never links on, and where a link fails.
#[test]
fn every_notice_says_the_engines_own_way_to_sign_in() {
    let never = Credentials {
        no_link_on: &["plan9"],
        ..OPENCODE
    };
    let mut links = Links::with_opencode_login(OPENCODE_TARGET);
    let notice = share_login(
        &mut links,
        &never.into(),
        &env(&[("HOME", "/home/u")]),
        "plan9",
        "h",
    );
    assert_eq!(said(notice).1, "run `opencode auth login`");
    let mut links = Links::with_opencode_login(OPENCODE_TARGET);
    links.fails = true;
    let (text, remedy) = said(opencode_shared(&mut links, &[("HOME", "/home/u")]));
    assert!(text.contains("could not be shared"), "{text}");
    assert_eq!(remedy, "run `opencode auth login`");
    let mut links = Links::with_opencode_login(OPENCODE_TARGET);
    links.state = LinkState::Other;
    let (text, _) = said(opencode_shared(&mut links, &[("HOME", "/home/u")]));
    assert!(text.contains("auth.json of its own"), "{text}");
}

// A configured login file: `~/` is the home, an absolute path is itself, and
// anything else is under the home.
#[test]
fn a_login_path_is_resolved_against_the_home_only_where_it_is_not_absolute() {
    assert_eq!(resolve("/h", "~/a/b"), "/h/a/b");
    assert_eq!(resolve("/h", "/etc/c.json"), "/etc/c.json");
    assert_eq!(resolve("/h", ".acme/c.json"), "/h/.acme/c.json");
}
