//! The checks that read only the text they are given.

use auth_application::Auth;
use serde::Serialize;

use crate::hooks::Stale;

/// A row's verdict.
#[derive(Clone, Copy, PartialEq, Eq, Debug, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum Status {
    Ok,
    Warn,
    Fail,
    /// A stated limit: not a verdict, so never `ok`.
    Info,
}

/// One row of the report.
#[derive(Clone, PartialEq, Eq, Debug, Serialize)]
pub struct Check {
    pub marker: &'static str,
    pub name: &'static str,
    pub status: Status,
    pub detail: String,
    /// What to do about a warning or failure.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub remedy: String,
}

fn row(
    marker: &'static str,
    name: &'static str,
    status: Status,
    detail: &str,
    remedy: &str,
) -> Check {
    Check {
        marker,
        name,
        status,
        detail: detail.to_string(),
        remedy: remedy.to_string(),
    }
}

/// `SETUP-MARKER-e5`: the project's `.ctxloom` and the config in it. A config
/// that will not parse is a finding, never a reason to stop the diagnosis.
///
/// `refused` is what every run would refuse the config with (its message and
/// fix), read as a run reads it: a `fail` row, since no run can start.
pub fn setup_marker(
    has_project: bool,
    config: Option<&str>,
    refused: Option<(&str, &str)>,
) -> Check {
    const M: &str = "DOCTOR-CHECK-SETUP-MARKER-e5";
    const N: &str = "setup and config";
    if let Some((message, fix)) = refused {
        return row(M, N, Status::Fail, message, fix);
    }
    if !has_project {
        return row(
            M,
            N,
            Status::Warn,
            "no .ctxloom directory here or above: this is not a ctxloom project",
            "ctxloom init",
        );
    }
    match config.map(angzarr_yaml::parse) {
        None => row(
            M,
            N,
            Status::Warn,
            ".ctxloom is there but holds no config.yaml",
            "ctxloom config create",
        ),
        Some(Err(error)) => row(
            M,
            N,
            Status::Warn,
            &format!(".ctxloom/config.yaml does not parse: {error}"),
            "ctxloom config edit",
        ),
        Some(Ok(_)) => row(M, N, Status::Ok, ".ctxloom and its config.yaml read", ""),
    }
}

/// `DEPS-a1`: what the machine needs whatever the project is: git, and the
/// identity it commits with (as git resolves it where ctxloom runs, in her
/// own environment: `ctxloom_ports::GitIdentity`).
pub fn deps(git_found: bool, identity_known: bool) -> Check {
    const M: &str = "DOCTOR-CHECK-DEPS-a1";
    const N: &str = "system dependencies";
    if !git_found {
        return row(
            M,
            N,
            Status::Warn,
            "git is not on the PATH: bundles and remotes need it",
            "install git",
        );
    }
    if !identity_known {
        return row(
            M,
            N,
            Status::Warn,
            "git is there but has no identity (user.name and user.email): a commit would fail",
            "git config --global user.name \"Your Name\" && git config --global user.email you@example.com",
        );
    }
    row(
        M,
        N,
        Status::Ok,
        "git is on the PATH and has an identity",
        "",
    )
}

/// `AGENTS-b2`: every configured agent names the engine it runs, or the
/// profiles it is for (an agent bound to neither resolves nothing).
pub fn agents(config: Option<&str>) -> Check {
    const M: &str = "DOCTOR-CHECK-AGENTS-b2";
    const N: &str = "agents";
    let Some(Ok(value)) = config.map(angzarr_yaml::parse) else {
        return row(
            M,
            N,
            Status::Info,
            "no readable config to read agents from",
            "",
        );
    };
    let agents = match value.get("agents") {
        Some(angzarr_yaml::Value::Map(agents)) => agents.clone(),
        _ => Vec::new(),
    };
    if agents.is_empty() {
        return row(M, N, Status::Ok, "no agents are configured", "");
    }
    let unbound: Vec<&str> = agents
        .iter()
        .filter(|(_, agent)| {
            let named = |key: &str| match agent.get(key) {
                Some(angzarr_yaml::Value::Scalar(text)) => !text.is_empty(),
                Some(angzarr_yaml::Value::Seq(items)) => !items.is_empty(),
                _ => false,
            };
            !named("engine") && !named("profiles")
        })
        .map(|(name, _)| name.as_str())
        .collect();
    if unbound.is_empty() {
        return row(
            M,
            N,
            Status::Ok,
            &format!(
                "{} agent(s) configured, each bound to an engine or profiles",
                agents.len()
            ),
            "",
        );
    }
    row(
        M,
        N,
        Status::Warn,
        &format!(
            "agent(s) {} name neither an engine nor profiles, so none resolves",
            unbound.join(", ")
        ),
        "ctxloom agent edit <name> --engine <engine>",
    )
}

/// `CREDENTIALS-m5`: whether the credential every configured engine needs is
/// exported, in the mode the human's session authenticates in. One that is
/// not is a `fail`: every run is refused for it. Only presence is read, never
/// a value, and "present" is not "accepted": whether the engine accepts it is
/// known only by launching the engine's own CLI. A source that is a file
/// alone is a `warn`: the primary session may run on it, no child can.
pub fn credentials(layers: &[String], auth: &Auth) -> Check {
    const M: &str = "DOCTOR-CHECK-CREDENTIALS-m5";
    const N: &str = "credentials";
    let engines =
        library::config::engines::engines_in_use(&library::config::engines::merged(layers));
    let verdicts = auth.needed(&engines, layers);
    let missing: Vec<_> = verdicts.iter().filter(|v| !v.ready).collect();
    if let Some(first) = missing.first() {
        let detail = missing
            .iter()
            .map(|v| {
                let names = v.vars.join(", ");
                let unset = if v.vars.len() == 1 {
                    format!("{names} is not exported")
                } else {
                    format!("none of {names} is exported")
                };
                format!("{} (auth {}): {unset}", v.engine, v.mode)
            })
            .collect::<Vec<_>>()
            .join("; ");
        return row(M, N, Status::Fail, &detail, &first.remedy);
    }
    // A file alone serves the primary session; every child run of that
    // engine is refused (ruling 53), and the row says so in the refusal's
    // words (ruling 81).
    let primary_only: Vec<_> = verdicts.iter().filter(|v| v.primary_only).collect();
    if let Some(first) = primary_only.first() {
        let detail = primary_only
            .iter()
            .map(|v| format!("{} (auth {}): {}", v.engine, v.mode, v.message))
            .collect::<Vec<_>>()
            .join("; ");
        return row(M, N, Status::Warn, &detail, &first.remedy);
    }
    if verdicts.is_empty() {
        return row(
            M,
            N,
            Status::Info,
            "no configured engine needs a credential from you",
            "",
        );
    }
    // An engine whose keys are optional and none exported needs none, and
    // is said so in `auth status`'s words, never as present.
    let present: Vec<String> = verdicts
        .iter()
        .filter(|v| !v.keyless)
        .map(|v| format!("{} (auth {})", v.engine, v.mode))
        .collect();
    let keyless = verdicts
        .iter()
        .filter(|v| v.keyless)
        .map(|v| format!("{} (auth {}): {}", v.engine, v.mode, v.message));
    let detail: Vec<String> = (!present.is_empty())
        .then(|| {
            format!(
                "every configured engine's credential is present: {} (doctor does not test whether the engine accepts it; launching the engine's own CLI shows that)",
                present.join(", ")
            )
        })
        .into_iter()
        .chain(keyless)
        .collect();
    row(M, N, Status::Ok, &detail.join("; "), "")
}

/// `VERSION-c3`.
pub fn version(version: &str) -> Check {
    row(
        "DOCTOR-CHECK-VERSION-c3",
        "version",
        Status::Ok,
        &format!("ctxloom {version}"),
        "",
    )
}

/// `HOOKS-TRUST-d4`: whether ctxloom's hooks (by `ctxloom` or by `name`, the
/// running binary's file name) are registered with the engine.
pub fn hooks(settings: &[(String, String)], name: &str) -> Check {
    const M: &str = "DOCTOR-CHECK-HOOKS-TRUST-d4";
    const N: &str = "hooks";
    let registered = settings
        .iter()
        .any(|(_, text)| crate::hooks::registered(text, name));
    if registered {
        return row(
            M,
            N,
            Status::Ok,
            "ctxloom's hooks are registered with the engine",
            "",
        );
    }
    row(
        M,
        N,
        Status::Info,
        "no ctxloom hook is registered in the engine's settings; a launch carries them in its own session home",
        "ctxloom materialize",
    )
}

/// `GITIGNORE-f6`: a blanket `.ctxloom` ignore rule predates content moving
/// into `.ctxloom/`; left in place it un-tracks the project's own content.
/// And the other half: ctxloom's private state that git can see
/// (`unignored`, the patterns no ignore file covers).
pub fn gitignore(text: Option<&str>, unignored: &[&str]) -> Check {
    const M: &str = "DOCTOR-CHECK-GITIGNORE-f6";
    const N: &str = ".gitignore posture";
    let blanket = text
        .unwrap_or_default()
        .lines()
        .map(str::trim)
        .find(|line| {
            matches!(
                *line,
                ".ctxloom"
                    | ".ctxloom/"
                    | "/.ctxloom"
                    | "/.ctxloom/"
                    | ".ctxloom/*"
                    | "/.ctxloom/*"
                    | ".ctxloom/**"
                    | "/.ctxloom/**"
            )
        });
    match blanket {
        Some(rule) => row(
            M,
            N,
            Status::Warn,
            &format!(
                ".gitignore has the blanket rule `{rule}`: it un-tracks the project's own content under .ctxloom, so `git add` reports nothing and a content repository publishes an empty tree"
            ),
            "ctxloom manage gitignore install",
        ),
        None if !unignored.is_empty() => row(
            M,
            N,
            Status::Warn,
            &format!(
                "git can see ctxloom's private state under .ctxloom ({}): neither .ctxloom/.gitignore nor .gitignore ignores it, so `git status` lists session homes, which hold an engine's login state",
                unignored.join(", ")
            ),
            "ctxloom manage gitignore install",
        ),
        None => row(
            M,
            N,
            Status::Ok,
            "no blanket .ctxloom ignore rule, and ctxloom's private state is ignored",
            "",
        ),
    }
}

/// `MCP-INVOCATION-g7`: a settings file whose ctxloom entry LAUNCHES a
/// server. ctxloom ships no stdio MCP server, so the engine waits on a
/// handshake that never arrives. No record of this machine writing the entry
/// is read yet (S9 ports the records): the remedy is the by-hand one.
/// ctxloom is `ctxloom` or `name`, the running binary's file name.
pub fn mcp_invocation(files: &[(String, String)], name: &str) -> Check {
    const M: &str = "DOCTOR-CHECK-MCP-INVOCATION-g7";
    const N: &str = "MCP invocation";
    let bad: Vec<&str> = files
        .iter()
        .filter(|(_, text)| crate::hooks::launches_ctxloom_mcp(text, name))
        .map(|(path, _)| path.as_str())
        .collect();
    let Some(first) = bad.first() else {
        return row(
            M,
            N,
            Status::Ok,
            "no settings file launches a ctxloom MCP server",
            "",
        );
    };
    row(
        M,
        N,
        Status::Warn,
        &format!(
            "the ctxloom entry from {first} launches `ctxloom mcp serve`, which does not exist: the session comes up without ctxloom's tools and nothing says why"
        ),
        &format!(
            "this machine holds no record of writing that entry, so delete it from {first} by hand; a session renders its own endpoint"
        ),
    )
}

/// `STALE-HOOKS-n5`: hook entries running a `ctxloom hook` subcommand this
/// ctxloom does not have: the Go-era ones, once Go ctxloom is retired.
pub fn stale_hooks(stale: &[(String, Stale)]) -> Check {
    const M: &str = "DOCTOR-CHECK-STALE-HOOKS-n5";
    const N: &str = "stale hook entries";
    let Some((file, first)) = stale.first() else {
        return row(
            M,
            N,
            Status::Ok,
            "no hook entry runs a ctxloom subcommand that is gone",
            "",
        );
    };
    let mut detail = format!(
        "{} runs `ctxloom hook {}` ({file}), which this ctxloom does not have; the engine runs it at every {} and it fails",
        first.event, first.sub, first.event
    );
    if stale.len() > 1 {
        detail.push_str(&format!("; {} entries in all", stale.len()));
    }
    row(M, N, Status::Warn, &detail, "ctxloom doctor --fix")
}

/// `GO-ERA-HOOK-n6` (owner ruling 162): while Go ctxloom is present
/// (`legacy.go`), each Go-era hook entry is its own `info` row, left alone:
/// it may be a live hook of the Go ctxloom that shares the user's settings.
pub fn go_era_hooks(entries: &[(String, Stale)]) -> Vec<Check> {
    entries
        .iter()
        .map(|(file, entry)| {
            row(
                "DOCTOR-CHECK-GO-ERA-HOOK-n6",
                "Go-era hook",
                Status::Info,
                &format!(
                    "Go-era hook, left alone: {} runs `ctxloom hook {}` ({file}); Go ctxloom is present (legacy.go), and the hook is its own",
                    entry.event, entry.sub
                ),
                "once Go ctxloom is gone, set `legacy.go: retired` in the home config; then `ctxloom doctor --fix` removes it",
            )
        })
        .collect()
}

/// `INGESTION-q7`: the one thing no check can confirm.
pub fn ingestion() -> Check {
    row(
        "DOCTOR-CHECK-INGESTION-q7",
        "delivered versus ingested",
        Status::Info,
        "ctxloom writes the context onto the engine's surface, but whether the engine reads it happens in a process ctxloom does not own",
        "",
    )
}
