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
    sign_in: "sign in with /login",
    ..Credentials::NONE
};

/// opencode's shape (ruling 69): its login is in its data directory, which the
/// session home relocates, and `XDG_DATA_HOME` moves the user's.
const OPENCODE: Credentials = Credentials {
    user_dir: ".local/share/opencode",
    user_dir_variable: Some(("XDG_DATA_HOME", "opencode")),
    login_file: Some("auth.json"),
    home_dir: "xdg/data/opencode",
    sign_in: "run `opencode auth login`",
    login_optional: true,
    ..Credentials::NONE
};

fn names(of: &[&str]) -> Vec<String> {
    of.iter().map(ToString::to_string).collect()
}

fn with(auth: AuthSetting, session_home: SessionHome) -> EngineSettings {
    EngineSettings {
        auth,
        session_home,
        ..EngineSettings::default()
    }
}

#[test]
fn nothing_configured_is_the_engines_own_credentials() {
    let policy = EngineSettings::default().credentials(CLAUDE);
    assert_eq!(policy, CredentialPolicy::from(CLAUDE));
    assert_eq!(
        policy.child_env,
        names(&["CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"])
    );
    assert_eq!(policy.login_file.as_deref(), Some(".credentials.json"));
    assert_eq!(policy.login_source, None);
}

#[test]
fn a_configured_env_source_is_what_authenticates_a_child_and_the_primary() {
    let source = AuthSetting::Source {
        env: names(&["ACME_TOKEN"]),
        file: None,
    };
    let policy = with(source, SessionHome::Session).credentials(CLAUDE);
    assert_eq!(policy.child_env, names(&["ACME_TOKEN"]));
    // ANTHROPIC_AUTH_TOKEN is the built-in primary's; a source names its own.
    assert_eq!(policy.primary_env, names(&["ACME_TOKEN"]));
    // The source names no file: the primary shares no login.
    assert_eq!(policy.login_file, None);
}

#[test]
fn a_source_file_is_the_primarys_login_and_never_a_childs() {
    let source = AuthSetting::Source {
        env: names(&["ACME_TOKEN"]),
        file: Some("~/.acme/creds.json".to_string()),
    };
    let policy = with(source, SessionHome::Session).credentials(CLAUDE);
    assert_eq!(policy.child_env, names(&["ACME_TOKEN"]));
    assert_eq!(policy.login_file.as_deref(), Some(".credentials.json"));
    assert_eq!(policy.login_source.as_deref(), Some("~/.acme/creds.json"));
}

#[test]
fn a_file_only_source_asks_a_child_for_nothing_it_can_be_given() {
    let source = AuthSetting::Source {
        env: Vec::new(),
        file: Some("/x/creds.json".to_string()),
    };
    let policy = with(source, SessionHome::Session).credentials(CLAUDE);
    assert!(policy.child_env.is_empty());
    assert!(policy.primary_env.is_empty());
}

#[test]
fn none_needs_no_credential_and_shares_no_login() {
    let policy = with(AuthSetting::None, SessionHome::Session).credentials(CLAUDE);
    assert!(policy.child_env.is_empty() && policy.primary_env.is_empty());
    assert_eq!(policy.login_file, None);
}

#[test]
fn login_shares_the_login_whatever_the_environment_holds() {
    let policy = with(AuthSetting::Login, SessionHome::Session).credentials(CLAUDE);
    assert!(policy.child_env.is_empty() && policy.primary_env.is_empty());
    assert_eq!(policy.login_file.as_deref(), Some(".credentials.json"));
    assert_eq!(policy.login_source, None);
}

#[test]
fn the_hosts_own_home_has_its_own_login_and_nothing_is_linked_into_it() {
    let policy = with(AuthSetting::Builtin, SessionHome::Host).credentials(CLAUDE);
    assert_eq!(policy.login_file, None);
    assert_eq!(
        policy.child_env,
        names(&["CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"])
    );
}

// Ruling 69: where the login is, in the user's home and in the session home,
// and what to say without one, reach the policy as the engine said them.
#[test]
fn the_policy_carries_where_the_login_is_and_how_to_sign_in() {
    let policy = EngineSettings::default().credentials(OPENCODE);
    assert_eq!(policy.user_dir, ".local/share/opencode");
    assert_eq!(
        policy.user_dir_variable,
        Some(("XDG_DATA_HOME".to_string(), "opencode".to_string()))
    );
    assert_eq!(policy.login_file.as_deref(), Some("auth.json"));
    assert_eq!(policy.home_dir, "xdg/data/opencode");
    assert_eq!(policy.sign_in, "run `opencode auth login`");
    assert!(policy.login_optional);
    let claude = CredentialPolicy::from(CLAUDE);
    assert_eq!(
        (claude.home_dir.as_str(), claude.user_dir_variable),
        ("", None)
    );
    assert!(!claude.login_optional);
    // The host's own home: nothing is linked, whatever the engine.
    let host = with(AuthSetting::Builtin, SessionHome::Host).credentials(OPENCODE);
    assert_eq!(host.login_file, None);
}

#[test]
fn credentials_none_is_the_default() {
    assert_eq!(Credentials::NONE, Credentials::default());
}

#[test]
fn settings_render_names_and_paths_only() {
    let settings = EngineSettings {
        command: "/opt/claude".to_string(),
        auth: AuthSetting::Source {
            env: names(&["ACME_TOKEN"]),
            file: Some("/x/c.json".to_string()),
        },
        ..EngineSettings::default()
    };
    assert_eq!(
        format!("{settings:?}"),
        r#"EngineSettings { command: "/opt/claude", session_home: Session, auth: Source { env: ["ACME_TOKEN"], file: Some("/x/c.json") }, env: [] }"#
    );
}
