//! Who a run authenticates as (owner ruling 2026-11-08, item 8).
//!
//! A CHILD agent's run is given only an environment credential: with none
//! set it is refused before a session is made. The human's PRIMARY session,
//! with no environment credential set, shares her own login by a link in its
//! session home; the platforms where that cannot work (the engine's
//! `no_link_on`) are told to sign in another way, and where the engine says a
//! link that cannot be made is a copy (`copy_on`: opencode on Windows, owner
//! ruling 80) it is copied whole. The login file is never read by ctxloom: the
//! only questions asked of it are whether it is there and what a link in the
//! home points to. Where
//! the login is, in her home and in the session home, and what she is told
//! without one, are the engine's (`run_ports::Credentials`): claude's config
//! dir is the session home itself, opencode's data directory is relocated
//! under it (owner ruling 69).

use std::collections::BTreeMap;

use library_application::Notice;
use run_ports::{AuthSetting, CredentialPolicy, Credentials, Ignored, LinkState, LoginLinks};
use runs_application::CredentialEvidence;

/// Whether the engine of `credentials` may share its login by a link on `os`
/// (`std::env::consts::OS`, which the caller gives): the engine says where it
/// never does (owner ruling 80: claude's macOS keychain and Windows' symlink
/// privilege are claude's; opencode links everywhere).
pub fn links_on(credentials: &CredentialPolicy, os: &str) -> bool {
    !credentials.no_link_on.iter().any(|never| never == os)
}

/// Whether, on `os`, a link that cannot be made is a copy of the login
/// (owner ruling 80: opencode on Windows).
fn copies_on(credentials: &CredentialPolicy, os: &str) -> bool {
    credentials.copy_on.iter().any(|copy| copy == os)
}

/// What a run says of a `CTXLOOM_PERMIT_*` it did not follow: by the permit's
/// name, never its value (owner ruling 78).
pub fn ignored_permit(ignored: &Ignored) -> Notice {
    let (permit, why) = match ignored {
        Ignored::RunsOwn { permit } => (
            permit,
            "the run's own CTXLOOM_* variables are never replaced by a permit".to_string(),
        ),
        Ignored::NoName { permit } => (
            permit,
            format!(
                "what follows {} is no variable name",
                run_ports::PERMIT_PREFIX
            ),
        ),
        Ignored::Owned { permit } => (
            permit,
            format!(
                "ctxloom sets {} for the engine itself",
                permit.trim_start_matches(run_ports::PERMIT_PREFIX)
            ),
        ),
    };
    Notice::Finding {
        text: format!("{permit} was not passed on to the agent: {why}"),
        remedy: format!("unset {permit}"),
    }
}

/// Whether the environment holds any of `names`, non-empty.
fn holds(env: &BTreeMap<String, String>, names: &[String]) -> bool {
    names
        .iter()
        .any(|name| env.get(name).is_some_and(|value| !value.is_empty()))
}

/// What a child agent's run authenticates with, as the run's enqueue judges
/// it (owner rulings 53, 73e): whether the engine's configured source is a
/// credentials file and no variable, the variables the engine's built-in
/// source accepts (an adapter that names none is told the library's built-in
/// source), the variables the child would be given a credential by, and
/// whether one of them is set and not empty. Names and yes-or-no only: no
/// value of the environment leaves here.
pub fn credential_evidence(
    engine: &str,
    auth: &AuthSetting,
    base: &Credentials,
    policy: &CredentialPolicy,
    env: &BTreeMap<String, String>,
) -> CredentialEvidence {
    let file_only = matches!(auth, AuthSetting::Source { env, file: Some(_) } if env.is_empty());
    let mut builtin_variables: Vec<String> =
        base.child_env.iter().map(ToString::to_string).collect();
    if builtin_variables.is_empty() {
        if let library_application::AuthSource::Credential { env, .. } =
            library_application::AuthSource::builtin(engine)
        {
            builtin_variables = env;
        }
    }
    CredentialEvidence {
        file_only,
        builtin_variables,
        accepted_variables: policy.child_env.clone(),
        accepted_set: holds(env, &policy.child_env),
    }
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
    if !links_on(credentials, os) {
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
        LinkState::Absent => match links.link(harp, &rel, &target) {
            Ok(()) => None,
            // Where the engine says so, a link that cannot be made is a copy,
            // and its cost is said: a refresh rotates the copy's token.
            Err(_) if copies_on(credentials, os) && links.copy(harp, &rel, &target).is_ok() => {
                finding(
                    format!(
                        "on {os} no link to {target} could be made, so the login was copied into the session: a token the session refreshes is written to the copy, and the rotation can sign your own login out"
                    ),
                    credentials,
                )
            }
            Err(error) => finding(
                format!("the login could not be shared with the session: {error}"),
                credentials,
            ),
        },
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
