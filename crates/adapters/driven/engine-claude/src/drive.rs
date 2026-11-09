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
    copy_in, Credentials, Engine, EngineError, HomeFile, PrepareRequest, Prepared, TurnCommand,
    TurnOutcome, TurnOutput, TurnRequest,
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
/// without `HOME`, and find ctxloom's home through this one explicit variable.
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
        let mut command = TurnCommand::new(PROGRAM);
        command.args = ["-p", "--output-format", "stream-json", "--verbose"]
            .map(String::from)
            .to_vec();
        command.args.push("--append-system-prompt-file".to_string());
        command.args.push(request.context_path.clone());
        if let Some(native_id) = &request.resume {
            command.args.push("--resume".to_string());
            command.args.push(native_id.clone());
        }
        command.env = environment(ENV_ALLOWLIST, &request.env, &request.home);
        command.stdin = request.prompt.clone().into_bytes();
        command.cwd = request.cwd.clone();
        Ok(command)
    }

    fn credentials(&self) -> Credentials {
        Credentials {
            child_env: CHILD_CREDENTIAL_ENV,
            primary_env: PRIMARY_CREDENTIAL_ENV,
            user_dir: ".claude",
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
        let mut command = TurnCommand::new(PROGRAM);
        command.args.push("--append-system-prompt-file".to_string());
        command.args.push(request.context_path.clone());
        if let Some(native_id) = &request.resume {
            command.args.push("--resume".to_string());
            command.args.push(native_id.clone());
        }
        if !request.prompt.trim().is_empty() {
            command.args.push(request.prompt.clone());
        }
        command.env = environment(INTERACTIVE_ENV_ALLOWLIST, &request.env, &request.home);
        command.cwd = request.cwd.clone();
        Ok(command)
    }
}

/// The command's whole environment: `caller`'s variables on `allowed` and
/// `CLAUDE_CONFIG_DIR` set to `home`.
fn environment(
    allowed: &[&str],
    caller: &BTreeMap<String, String>,
    home: &str,
) -> BTreeMap<String, String> {
    let mut env: BTreeMap<String, String> = caller
        .iter()
        .filter(|(name, _)| allowed.contains(&name.as_str()))
        .map(|(name, value)| (name.clone(), value.clone()))
        .collect();
    env.insert("CLAUDE_CONFIG_DIR".to_string(), home.to_string());
    env
}

#[cfg(test)]
#[path = "drive.test.rs"]
mod tests;
