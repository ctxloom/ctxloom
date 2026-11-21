//! What `session show` says of a session's latest run (step 9, owner ruling
//! 2026-11-17): what the run was launched with, as its enqueue journaled it,
//! and how it failed, its last turn's class or a start that failed, in a
//! notice's words. Pure: the board's run fold in, the resource's fields out.

use daemon_board::RunFold;
use daemon_proto::v1;
use runs::TurnFailureClass;

/// What the run was launched with; none for a run that journaled no grant.
pub(crate) fn grant_of(run: &RunFold) -> Option<v1::SessionGrant> {
    run.state.grant.as_ref().map(|grant| v1::SessionGrant {
        mcp_servers: grant.mcp_servers.clone(),
        permissions: grant.permissions.clone(),
    })
}

/// A notice: its code, its args and its en-US text (the crate's catalog
/// holds the same text under the code).
fn notice(code: &str, args: &[(&str, &str)], text: String) -> v1::Notice {
    v1::Notice {
        code: code.to_string(),
        args: args
            .iter()
            .map(|(name, value)| v1::NeedArg {
                name: (*name).to_string(),
                value: (*value).to_string(),
            })
            .collect(),
        text,
    }
}

/// How the run failed, for the session `harp` (a child's words when it has
/// an owner): a start that failed, else its last turn's failure; none when
/// neither did.
pub(crate) fn failure_of(harp: &str, child: bool, run: &RunFold) -> Option<v1::SessionFailure> {
    if run.state.ended == Some(runs::EndCause::LaunchFailed) {
        let detail = run.end_detail.as_str();
        let said = match child {
            true => notice(
                "daemon-api-run-launch-failed-child",
                &[("detail", detail)],
                format!("could not start the child's engine: {detail}"),
            ),
            false => notice(
                "daemon-api-run-launch-failed",
                &[("detail", detail)],
                format!("could not start the session's engine: {detail}"),
            ),
        };
        return Some(v1::SessionFailure {
            kind: "launch-failed".to_string(),
            detail: detail.to_string(),
            notice: Some(said),
        });
    }
    let failed = run.last_failure.as_ref()?;
    let (kind, text) = match failed.class {
        TurnFailureClass::RateLimited => (
            "rate-limited",
            format!(
                "its last turn ran into its engine's rate limit, and nothing tries it again: once the limit has reset, resume it with `ctxloom session send {harp} wake <text>`"
            ),
        ),
        TurnFailureClass::CredentialRefused => (
            "credential-refused",
            "its engine refused its credential on its last turn".to_string(),
        ),
        TurnFailureClass::EngineError => {
            ("engine-error", "its engine failed its last turn".to_string())
        }
        TurnFailureClass::Interrupted => {
            ("interrupted", "its last turn was interrupted".to_string())
        }
        TurnFailureClass::RunnerLost => (
            "runner-lost",
            "its host was lost while its last turn was in flight".to_string(),
        ),
    };
    let named = [("harp", harp)];
    let args: &[(&str, &str)] = match failed.class {
        TurnFailureClass::RateLimited => &named,
        _ => &[],
    };
    Some(v1::SessionFailure {
        kind: kind.to_string(),
        detail: failed.detail.clone(),
        notice: Some(notice(&format!("daemon-api-turn-{kind}"), args, text)),
    })
}

#[cfg(test)]
#[path = "shown.test.rs"]
mod tests;
