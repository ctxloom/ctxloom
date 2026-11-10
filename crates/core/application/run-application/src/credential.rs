//! Who a run authenticates as (owner ruling 2026-11-08, item 8).
//!
//! A CHILD agent's run is given only an environment credential: with none
//! set it is refused before a session is made. The human's PRIMARY session,
//! with no environment credential set, shares her own login by a link in its
//! session home; the platforms where that cannot work are told to set a
//! token instead. The login file is never read: the only questions asked of
//! it are whether it is there and what a link in the home points to.

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

/// What to do about a missing token, on every platform that cannot link and
/// wherever there is no login to link.
const TOKEN_FIX: &str = "run `claude setup-token`, then export CLAUDE_CODE_OAUTH_TOKEN";

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

/// Give the primary session of `harp` her login, where she has one and
/// nothing else authenticates it. What it noticed and did not stop for.
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
        return Some(Notice::Finding {
            text: format!(
                "on {os} this session's login cannot be shared with it by a link; the primary session will not be signed in"
            ),
            remedy: format!("{TOKEN_FIX} (it needs a Pro, Max, Team or Enterprise plan)"),
        });
    }
    let home = env.get("HOME").filter(|home| !home.is_empty());
    let target = home.map(|home| match &credentials.login_source {
        Some(source) => resolve(home, source),
        None => format!("{home}/{}/{file}", credentials.user_dir),
    });
    let target = match target {
        Some(target) if links.present(&target) => target,
        _ => {
            return Some(Notice::Finding {
                text: format!(
                    "there is no login at ~/{}/{file} to share, so the primary session will not be signed in",
                    credentials.user_dir
                ),
                remedy: format!("sign in with /login in the session, or {TOKEN_FIX}"),
            });
        }
    };
    let rel = format!("home/{file}");
    match links.state(harp, &rel) {
        LinkState::LinkTo(held) if held == target => None,
        LinkState::Absent => links
            .link(harp, &rel, &target)
            .err()
            .map(|error| Notice::Finding {
                text: format!("the login could not be shared with the session: {error}"),
                remedy: format!("sign in with /login in the session, or {TOKEN_FIX}"),
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
