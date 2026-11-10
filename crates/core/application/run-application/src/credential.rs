//! Who a run authenticates as (owner ruling 2026-11-08, item 8).
//!
//! A CHILD agent's run is given only an environment credential: with none
//! set it is refused before a session is made. The human's PRIMARY session,
//! with no environment credential set, shares her own login by a link in its
//! session home; the platforms where that cannot work are told to set a
//! token instead. The login file is never read: the only questions asked of
//! it are whether it is there and what a link in the home points to. Where
//! the login is, in her home and in the session home, and what she is told
//! without one, are the engine's (`run_ports::Credentials`): claude's config
//! dir is the session home itself, opencode's data directory is relocated
//! under it (owner ruling 69).

use std::collections::BTreeMap;

use library_application::Notice;
use run_ports::{AuthSetting, CredentialPolicy, Credentials, LinkState, LoginLinks};
use runs::RunRejection;

/// The platforms whose login cannot be shared by a link: macOS keeps claude's
/// login in a keychain item per configuration directory (a file link is
/// ignored), and Windows grants a symlink only to a privileged user. This is
/// the one place the platform is judged; the caller gives the OS name
/// (`std::env::consts::OS`).
const NO_LINK: [&str; 2] = ["macos", "windows"];

/// Whether `os` can share a login by a link.
pub fn can_link(os: &str) -> bool {
    !NO_LINK.contains(&os)
}

/// Whether the environment holds any of `names`, non-empty.
fn holds(env: &BTreeMap<String, String>, names: &[String]) -> bool {
    names
        .iter()
        .any(|name| env.get(name).is_some_and(|value| !value.is_empty()))
}

/// The refusal of a child agent's run that has no environment credential, or
/// none when its engine needs none or one is set.
pub fn refuse_child(
    engine: &str,
    credentials: &CredentialPolicy,
    env: &BTreeMap<String, String>,
) -> Option<RunRejection> {
    if credentials.child_env.is_empty() || holds(env, &credentials.child_env) {
        return None;
    }
    Some(RunRejection::NoCredential {
        engine: engine.to_string(),
        variables: credentials.child_env.join(", "),
    })
}

/// The refusal of a child agent's run whose engine's configured source is a
/// credentials file and no variable (owner ruling 53), naming the variables
/// the engine's built-in source accepts; none for any other source. Asked
/// before anything of the run is made: its listener, its session, its engine.
pub fn refuse_file_only(
    engine: &str,
    auth: &AuthSetting,
    base: &Credentials,
) -> Option<RunRejection> {
    let AuthSetting::Source { env, file: Some(_) } = auth else {
        return None;
    };
    if !env.is_empty() {
        return None;
    }
    let mut variables: Vec<String> = base.child_env.iter().map(ToString::to_string).collect();
    if variables.is_empty() {
        if let library_application::AuthSource::Credential { env, .. } =
            library_application::AuthSource::builtin(engine)
        {
            variables = env;
        }
    }
    Some(RunRejection::FileOnlyCredential {
        engine: engine.to_string(),
        variables: variables.join(", "),
    })
}

/// Where a configured login file is: `~/` is the caller's home, a relative
/// path is under it, an absolute path is itself.
fn resolve(home: &str, path: &str) -> String {
    match path.strip_prefix("~/") {
        Some(rest) => format!("{home}/{rest}"),
        None if path.starts_with('/') => path.to_string(),
        None => format!("{home}/{path}"),
    }
}

/// The user's own directory for the engine: where its variable moves it (an
/// absolute path, as XDG reads one), else under her home.
fn user_dir(credentials: &CredentialPolicy, env: &BTreeMap<String, String>, home: &str) -> String {
    let moved = credentials
        .user_dir_variable
        .as_ref()
        .and_then(|(variable, dir)| {
            env.get(variable)
                .filter(|value| value.starts_with('/'))
                .map(|value| format!("{value}/{dir}"))
        });
    moved.unwrap_or_else(|| format!("{home}/{}", credentials.user_dir))
}

/// A finding with the engine's own way to sign in.
fn finding(text: String, credentials: &CredentialPolicy) -> Option<Notice> {
    Some(Notice::Finding {
        text,
        remedy: credentials.sign_in.clone(),
    })
}

/// Give the primary session of `harp` her login, where she has one and
/// nothing else authenticates it: a link at the engine's place in the session
/// home (`home/<home_dir>/<login_file>`) to her own file, never a copy. What
/// it noticed and did not stop for.
pub fn share_login(
    links: &mut dyn LoginLinks,
    credentials: &CredentialPolicy,
    env: &BTreeMap<String, String>,
    os: &str,
    harp: &str,
) -> Option<Notice> {
    let file = credentials.login_file.as_deref()?;
    if holds(env, &credentials.primary_env) {
        return None;
    }
    if !can_link(os) {
        return finding(
            format!(
                "on {os} this session's login cannot be shared with it by a link; the primary session will not be signed in"
            ),
            credentials,
        );
    }
    let home = env.get("HOME").filter(|home| !home.is_empty());
    let target = home.map(|home| match &credentials.login_source {
        Some(source) => resolve(home, source),
        None => format!("{}/{file}", user_dir(credentials, env, home)),
    });
    let target = match target {
        Some(target) if links.present(&target) => target,
        // A login the engine runs without is asked for only where the config
        // names one.
        _ if credentials.login_optional && credentials.login_source.is_none() => return None,
        missing => {
            let shown = match (&credentials.login_source, missing) {
                (Some(_), Some(target)) => target,
                _ => format!("~/{}/{file}", credentials.user_dir),
            };
            return finding(
                format!(
                    "there is no login at {shown} to share, so the primary session will not be signed in"
                ),
                credentials,
            );
        }
    };
    let rel = match credentials.home_dir.as_str() {
        "" => format!("home/{file}"),
        dir => format!("home/{dir}/{file}"),
    };
    match links.state(harp, &rel) {
        LinkState::LinkTo(held) if held == target => None,
        LinkState::Absent => links.link(harp, &rel, &target).err().and_then(|error| {
            finding(
                format!("the login could not be shared with the session: {error}"),
                credentials,
            )
        }),
        _ => Some(Notice::Finding {
            text: format!(
                "the session home already holds a {file} of its own, which was left as it is"
            ),
            remedy: String::new(),
        }),
    }
}

#[cfg(test)]
#[path = "credential.test.rs"]
mod tests;
