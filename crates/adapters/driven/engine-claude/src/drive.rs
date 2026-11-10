//! Claude Code's drive half: a child agent runs turn-as-command.
//!
//! One turn is one `claude -p` command: the prompt on stdin, the assembled
//! context in a file given to `--append-system-prompt-file`, the stream of
//! events on stdout (`--output-format stream-json --verbose`, read by
//! [`crate::stream`]). A later turn is a new command carrying
//! `--resume <native session id>`, the id turn 1's stream named. Nothing runs
//! between turns.
//!
//! The command's environment is built here and is all it gets: the session
//! home as `CLAUDE_CONFIG_DIR` (the user's `~/.claude` is never named), and
//! the caller's variables that are on [`ENV_ALLOWLIST`].

use std::collections::BTreeMap;

use run_ports::{
    copy_in, Credentials, Engine, EngineError, HomeFile, PrepareRequest, Prepared, SessionHome,
    TurnCommand, TurnOutcome, TurnOutput, TurnRequest,
};

use crate::{stream, Claude, NAME};

/// The settings file of a session home (claude's config dir).
const SETTINGS_FILE: &str = "settings.json";

/// The context file's name in the session home.
pub const CONTEXT_PROMPT_FILE: &str = "ctxloom-context.md";

/// The program: found on the `PATH` of the process that runs the command.
const PROGRAM: &str = "claude";

/// The variables of the caller's environment a turn is given: how to find
/// programs, locale and temp space, the proxy and certificate settings a
/// network needs, and the two ways claude is handed a credential. The user's
/// `HOME` and every other variable stay behind.
pub const ENV_ALLOWLIST: &[&str] = &[
    "PATH",
    "LANG",
    "LC_ALL",
    "TMPDIR",
    "HTTPS_PROXY",
    "HTTP_PROXY",
    "NO_PROXY",
    "https_proxy",
    "http_proxy",
    "no_proxy",
    "SSL_CERT_FILE",
    "SSL_CERT_DIR",
    "NODE_EXTRA_CA_CERTS",
    "CLAUDE_CODE_OAUTH_TOKEN",
    "ANTHROPIC_API_KEY",
];

/// The variables of the caller's environment the human's terminal session is
/// given: a turn's, and ctxloom's home by name. Its hook processes run
/// without `HOME`, and find ctxloom's home through this one explicit variable;
/// they reach the run's listener through the endpoint and the bearer (the hook
/// principal's secret) and the run, which a child's turn is never given.
pub const INTERACTIVE_ENV_ALLOWLIST: &[&str] = &[
    "PATH",
    "LANG",
    "LC_ALL",
    "TMPDIR",
    "HTTPS_PROXY",
    "HTTP_PROXY",
    "NO_PROXY",
    "https_proxy",
    "http_proxy",
    "no_proxy",
    "SSL_CERT_FILE",
    "SSL_CERT_DIR",
    "NODE_EXTRA_CA_CERTS",
    "CLAUDE_CODE_OAUTH_TOKEN",
    "ANTHROPIC_API_KEY",
    "ANTHROPIC_AUTH_TOKEN",
    run_ports::HOME_VARIABLE,
    run_ports::ENDPOINT_VARIABLE,
    run_ports::BEARER_VARIABLE,
    run_ports::RUN_VARIABLE,
    run_ports::HARP_VARIABLE,
];

/// The variables any one of which authenticates a child agent's run: the
/// only credential a child is given (never a shared credentials file).
pub const CHILD_CREDENTIAL_ENV: &[&str] = &["CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"];

/// The variables any one of which authenticates the human's primary session;
/// with one set it shares no login file. (`ANTHROPIC_AUTH_TOKEN` is the
/// primary's alone: it is in the interactive allowlist, not the turn's.)
pub const PRIMARY_CREDENTIAL_ENV: &[&str] = &[
    "CLAUDE_CODE_OAUTH_TOKEN",
    "ANTHROPIC_API_KEY",
    "ANTHROPIC_AUTH_TOKEN",
];

/// claude's configuration directory under the user's home.
const LOGIN_DIR: &str = ".claude";

/// claude's login file, in its configuration directory.
pub const LOGIN_FILE: &str = ".credentials.json";

/// The files of the user's own claude configuration that are copied into a
/// session home. None yet: which of them a child may see is the owner's to
/// rule, and until then nothing of `~/.claude` crosses.
pub const COPY_IN_ALLOWLIST: &[&str] = &[];

impl Engine for Claude {
    fn name(&self) -> &str {
        NAME
    }

    fn prepare(&self, request: &PrepareRequest) -> Result<Prepared, EngineError> {
        let mut files = vec![HomeFile {
            rel: CONTEXT_PROMPT_FILE.to_string(),
            bytes: request.context.clone().into_bytes(),
        }];
        // Hooks are the terminal session's way to be heard; a child's turn is
        // one command, whose end is its exit.
        if request.interactive {
            files.push(HomeFile {
                rel: SETTINGS_FILE.to_string(),
                bytes: crate::hooks::home_settings().into_bytes(),
            });
        }
        Ok(Prepared {
            files,
            copy_in: copy_in(COPY_IN_ALLOWLIST, &request.offered),
            context_path: format!("{}/{CONTEXT_PROMPT_FILE}", request.home),
        })
    }

    fn drive_turn(&self, request: &TurnRequest) -> Result<TurnCommand, EngineError> {
        let mut command = TurnCommand::new(program(request));
        command.args = ["-p", "--output-format", "stream-json", "--verbose"]
            .map(String::from)
            .to_vec();
        command.args.push("--append-system-prompt-file".to_string());
        command.args.push(request.context_path.clone());
        push_model_and_effort(&mut command.args, request)?;
        if let Some(native_id) = &request.resume {
            command.args.push("--resume".to_string());
            command.args.push(native_id.clone());
        }
        command.env = environment(&Claude::new(), false, request)?;
        command.stdin = request.prompt.clone().into_bytes();
        command.cwd = request.cwd.clone();
        Ok(command)
    }

    fn credentials(&self) -> Credentials {
        Credentials {
            child_env: CHILD_CREDENTIAL_ENV,
            primary_env: PRIMARY_CREDENTIAL_ENV,
            user_dir: LOGIN_DIR,
            login_file: Some(LOGIN_FILE),
        }
    }

    fn finish_turn(&self, output: &TurnOutput) -> TurnOutcome {
        stream::read_turn(output)
    }

    /// The human's terminal run: no print mode, no stream, the context in
    /// the system prompt, and the prompt (when she gave one) as the
    /// conversation's opening. The command is given her terminal.
    fn drive_interactive(&self, request: &TurnRequest) -> Result<TurnCommand, EngineError> {
        let mut command = TurnCommand::new(program(request));
        command.args.push("--append-system-prompt-file".to_string());
        command.args.push(request.context_path.clone());
        push_model_and_effort(&mut command.args, request)?;
        if let Some(native_id) = &request.resume {
            command.args.push("--resume".to_string());
            command.args.push(native_id.clone());
        }
        if !request.prompt.trim().is_empty() {
            command.args.push(request.prompt.clone());
        }
        command.env = environment(&Claude::new(), true, request)?;
        command.cwd = request.cwd.clone();
        Ok(command)
    }
}

/// The effort levels `claude --effort` takes (`claude --help`, 2.1.321).
pub const EFFORT_LEVELS: [&str; 5] = ["low", "medium", "high", "xhigh", "max"];

/// `--model <model>` and `--effort <level>` when the agent names them; none
/// named is no flag (the engine's own default). A level claude does not take
/// is refused here, before it is launched.
fn push_model_and_effort(args: &mut Vec<String>, request: &TurnRequest) -> Result<(), EngineError> {
    if !request.model.is_empty() {
        args.push("--model".to_string());
        args.push(request.model.clone());
    }
    if !request.effort.is_empty() {
        if !EFFORT_LEVELS.contains(&request.effort.as_str()) {
            return Err(EngineError::UnknownEffort {
                engine: NAME.to_string(),
                level: request.effort.clone(),
                known: EFFORT_LEVELS.join(", "),
            });
        }
        args.push("--effort".to_string());
        args.push(request.effort.clone());
    }
    Ok(())
}

/// The program: the engine block's `command`, else `claude` on the `PATH`.
fn program(request: &TurnRequest) -> &str {
    match request.settings.command.as_str() {
        "" => PROGRAM,
        configured => configured,
    }
}

/// The variables only the terminal session is given: ctxloom's home and the
/// listener's endpoint, bearer, run and session. A child's turn never gets one,
/// whatever the config names.
fn interactive_only(name: &str) -> bool {
    name == run_ports::HOME_VARIABLE
        || name == run_ports::HARP_VARIABLE
        || run_ports::HOOK_VARIABLES.contains(&name)
}

/// The variable that is the primary's alone: never a child's.
const PRIMARY_ONLY: &str = "ANTHROPIC_AUTH_TOKEN";

/// Whether the caller's variable `name` may be handed to this command.
fn grantable(name: &str, interactive: bool) -> bool {
    interactive || !(interactive_only(name) || name == PRIMARY_ONLY)
}

/// The command's whole environment: `caller`'s variables on the curated
/// allowlist, on the engine block's `env` and on the configured credential
/// source: a bounded, enumerable set (the container runtime reads it), never
/// the whole environment, and
/// `CLAUDE_CONFIG_DIR` set to the session's home, or to the user's own
/// directory when the engine block says `session_home: host`. A child is never
/// given the interactive-only variables nor the primary's token.
fn environment(
    engine: &Claude,
    interactive: bool,
    request: &TurnRequest,
) -> Result<BTreeMap<String, String>, EngineError> {
    let settings = &request.settings;
    let policy = settings.credentials(engine.credentials());
    let credential = if interactive {
        &policy.primary_env
    } else {
        &policy.child_env
    };
    let curated = if interactive {
        INTERACTIVE_ENV_ALLOWLIST
    } else {
        ENV_ALLOWLIST
    };
    let passes = |name: &str| {
        grantable(name, interactive)
            && (curated.contains(&name)
                || settings.env.iter().any(|n| n == name)
                || credential.iter().any(|n| n == name))
    };
    let mut env: BTreeMap<String, String> = request
        .env
        .iter()
        .filter(|(name, _)| passes(name))
        .map(|(name, value)| (name.clone(), value.clone()))
        .collect();
    let config_dir = match settings.session_home {
        SessionHome::Session => request.home.clone(),
        SessionHome::Host => {
            let home = request
                .env
                .get("HOME")
                .filter(|home| !home.is_empty())
                .ok_or_else(|| EngineError::NoHostHome {
                    engine: NAME.to_string(),
                })?;
            format!("{home}/{}", LOGIN_DIR)
        }
    };
    env.insert("CLAUDE_CONFIG_DIR".to_string(), config_dir);
    Ok(env)
}

#[cfg(test)]
#[path = "drive.test.rs"]
mod tests;
