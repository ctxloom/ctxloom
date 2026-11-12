//! opencode's drive half.
//!
//! A child's turn is one `opencode run --format json` command: the prompt on
//! stdin, which `run` reads to its end (the live probe: an argument with a
//! space reaches the model wrapped in literal quotes, and a stdin left open
//! hangs `run`), the context in the session home's `AGENTS.md`, the events on
//! stdout (read by [`crate::stream`] for the reply and the outcome alone). A
//! later turn names the session the first turn's stream gave (`--session`).
//! `run` waits for the plugin's `dispose` before it exits, so every delivery
//! of the turn is made by then (probe P4). The human's session is opencode's
//! TUI on her terminal, the prompt a flag.
//!
//! Either is heard through the plugin alone (owner rulings 26, 51 and 58).
//!
//! The command's environment is built here and is all it gets: the session
//! home as `OPENCODE_CONFIG_DIR`, every XDG base and `HOME` relocated into it
//! (the user's own opencode config, data, state, cache and `~/.opencode` are
//! neither read nor written; her recent models alone seed the home, owner
//! ruling 167), and the caller's variables on the allowlist,
//! the engine block's `env` and the configured credential source.

use std::collections::BTreeMap;

use run_ports::{
    Credentials, Engine, EngineError, HomeFile, PrepareRequest, Prepared, SessionHome, TurnCommand,
    TurnOutcome, TurnOutput, TurnRequest,
};

use crate::home::{self, CONTEXT_FILE};
use crate::{Opencode, NAME};

/// The program: found on the `PATH` of the process that runs the command.
const PROGRAM: &str = "opencode";

/// The provider keys of opencode's built-in credential source (rulings 69,
/// 79): the standard variables of its Anthropic and OpenAI providers
/// (models.dev's `env` of each, which opencode reads). Each optional: a
/// built-in provider's free models need none, and no child is refused for
/// want of one. Another provider's variable is the config's `env`, or, for
/// one an `OPENCODE_*` block withholds (`OPENCODE_API_KEY`), a
/// `CTXLOOM_PERMIT_<NAME>` (ruling 78).
pub const PROVIDER_KEYS: &[&str] = &["ANTHROPIC_API_KEY", "OPENAI_API_KEY"];

/// opencode's login file, in its data directory.
pub const LOGIN_FILE: &str = "auth.json";

/// opencode's data directory under the user's home (XDG's default), where
/// its login file is; `XDG_DATA_HOME`, set, moves it to `$XDG_DATA_HOME/opencode`.
const DATA_DIR: &str = ".local/share/opencode";

/// The relocated data directory under the session home: where the session's
/// opencode looks for its login (`XDG_DATA_HOME` is `<home>/xdg/data`).
const SESSION_DATA_DIR: &str = "xdg/data/opencode";

/// How the human gives her primary session a credential when no login is
/// shared with it: opencode's own sign-in (which writes her own login, linked
/// into her next session), or a provider's key.
pub const SIGN_IN: &str = "run `opencode auth login`, then start the session again, or export ANTHROPIC_API_KEY or OPENAI_API_KEY";

/// The variables of the caller's environment a child's turn is given: how to
/// find programs, locale and temp space, the proxy and certificate settings a
/// network needs, the optional provider keys of opencode's built-in source,
/// and what the plugin's hook
/// processes need (ctxloom's home by name, the session's harp, the run's
/// listener), and on Windows what a program needs to start
/// (`run_ports::WINDOWS_STARTUP_VARIABLES`) and the user's profile
/// directories (`run_ports::WINDOWS_PROFILE_VARIABLES`, relocated into the
/// session home as `HOME` is; none is set on unix). Never `HOME`, an XDG base
/// nor any `OPENCODE_*` variable of the caller's: `OPENCODE_PURE` would skip the plugin, and capture vanish.
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
    "SYSTEMROOT",
    "SYSTEMDRIVE",
    "WINDIR",
    "TEMP",
    "TMP",
    "PATHEXT",
    "COMSPEC",
    "USERPROFILE",
    "APPDATA",
    "LOCALAPPDATA",
    "ANTHROPIC_API_KEY",
    "OPENAI_API_KEY",
    run_ports::HOME_VARIABLE,
    run_ports::ENDPOINT_VARIABLE,
    run_ports::BEARER_VARIABLE,
    run_ports::RUN_VARIABLE,
    run_ports::TLS_CERT_VARIABLE,
    run_ports::SESSION_VARIABLE,
    run_ports::HARP_VARIABLE,
];

/// The variables of the caller's environment the human's session is given:
/// a turn's (the TUI is heard through the same plugin), and the terminal's
/// type and colour depth (`TERM`, `COLORTERM`), without which the TUI draws
/// in no colour.
pub const INTERACTIVE_ENV_ALLOWLIST: &[&str] = &[
    "TERM",
    "COLORTERM",
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
    "SYSTEMROOT",
    "SYSTEMDRIVE",
    "WINDIR",
    "TEMP",
    "TMP",
    "PATHEXT",
    "COMSPEC",
    "USERPROFILE",
    "APPDATA",
    "LOCALAPPDATA",
    "ANTHROPIC_API_KEY",
    "OPENAI_API_KEY",
    run_ports::HOME_VARIABLE,
    run_ports::ENDPOINT_VARIABLE,
    run_ports::BEARER_VARIABLE,
    run_ports::RUN_VARIABLE,
    run_ports::TLS_CERT_VARIABLE,
    run_ports::SESSION_VARIABLE,
    run_ports::HARP_VARIABLE,
];

/// The variables a command is given whatever the config names: opencode's
/// own, which the relocation sets, and `OPENCODE_PURE` above all.
fn withheld(name: &str) -> bool {
    name.starts_with("OPENCODE_") || name == "HOME" || name.starts_with("XDG_")
}

impl Engine for Opencode {
    fn name(&self) -> &str {
        NAME
    }

    fn prepare(&self, request: &PrepareRequest) -> Result<Prepared, EngineError> {
        let permissions = match &request.permissions {
            Some(document) => {
                Some(surface_ports::Json::parse(document).map_err(|ext_err_txt| {
                    EngineError::BadPermissions {
                        engine: NAME.to_string(),
                        ext_err_txt,
                    }
                })?)
            }
            None => None,
        };
        // Under a session home the config starts from her own global one
        // (owner ruling 169); under a host home opencode reads hers itself.
        let (base, left_out) = match request.session_home {
            SessionHome::Session => home::user_base(self.user_config.as_ref()),
            SessionHome::Host => (surface_ports::Json::Object(Vec::new()), None),
        };
        Ok(Prepared {
            files: home::files(
                &request.context,
                &home::config(base, permissions.as_ref()),
                &self.program,
            ),
            // No file of the user's own opencode configuration crosses as
            // it is (her config is the base of the home's, above).
            copy_in: Vec::new(),
            // Of her state, her recent models alone, once, where the
            // relocated state directory is (owner ruling 167); under a host
            // home the state directory is her own.
            seeds: match (&self.model, request.session_home) {
                (Some(bytes), SessionHome::Session) => vec![HomeFile {
                    rel: home::MODEL_SEED.to_string(),
                    bytes: bytes.clone(),
                }],
                _ => Vec::new(),
            },
            context_path: format!("{}/{CONTEXT_FILE}", request.home),
            left_out: left_out.into_iter().collect(),
        })
    }

    fn drive_turn(&self, request: &TurnRequest) -> Result<TurnCommand, EngineError> {
        let mut command = TurnCommand::new(program(request));
        command.args = ["run", "--format", "json"].map(String::from).to_vec();
        if let Some(native_id) = &request.resume {
            command.args.push("--session".to_string());
            command.args.push(native_id.clone());
        }
        push_model(&mut command.args, request);
        command.env = environment(self, ENV_ALLOWLIST, request)?;
        command.stdin = request.prompt.clone().into_bytes();
        command.cwd = request.cwd.clone();
        Ok(command)
    }

    /// Every key is optional (a built-in provider's free models need none),
    /// so a child is never refused for want of one. opencode's login is
    /// `auth.json` in its data directory, which an OAuth provider's refresh
    /// rewrites in place (owner ruling 69): the primary shares it by a link
    /// in the relocated data directory, never a copy, whatever variable is
    /// set (the login may hold other providers than a key does); a child is
    /// never given it.
    fn credentials(&self) -> Credentials {
        Credentials {
            child_env: &[],
            primary_env: &[],
            user_dir: DATA_DIR,
            user_dir_variable: Some(("XDG_DATA_HOME", "opencode")),
            login_file: Some(LOGIN_FILE),
            home_dir: SESSION_DATA_DIR,
            sign_in: SIGN_IN,
            login_optional: true,
            // A file on every platform (no keychain): linked everywhere, and
            // on Windows, where a symlink needs a privilege, copied when no
            // link can be made (ruling 80; the cost: a refresh rotates the
            // copy's token, which can sign the real login out).
            no_link_on: &[],
            copy_on: &["windows"],
        }
    }

    fn finish_turn(&self, output: &TurnOutput) -> TurnOutcome {
        crate::stream::read_turn(output)
    }

    /// opencode has no native "session ended" event, and the plugin forwards
    /// none: the launcher records the end at the exit (ruling 58).
    fn session_ends_with_exit(&self) -> bool {
        true
    }

    /// An agent with no model is told opencode chooses its own (owner
    /// ruling 168).
    fn advise(&self, planned: &run_ports::Planned) -> Vec<run_ports::Advice> {
        crate::advice::advise(planned)
    }

    /// The human's session: the TUI, given her terminal; the prompt (when she
    /// gave one) a flag.
    fn drive_interactive(&self, request: &TurnRequest) -> Result<TurnCommand, EngineError> {
        let mut command = TurnCommand::new(program(request));
        push_model(&mut command.args, request);
        if let Some(native_id) = &request.resume {
            command.args.push("--session".to_string());
            command.args.push(native_id.clone());
        }
        if !request.prompt.trim().is_empty() {
            command.args.push("--prompt".to_string());
            command.args.push(request.prompt.clone());
        }
        command.env = environment(self, INTERACTIVE_ENV_ALLOWLIST, request)?;
        command.cwd = request.cwd.clone();
        Ok(command)
    }
}

/// `-m <provider/model>` when the agent names one. An effort level is not
/// passed: no opencode flag for it is verified (it is stored and shown only).
fn push_model(args: &mut Vec<String>, request: &TurnRequest) {
    if !request.model.is_empty() {
        args.push("-m".to_string());
        args.push(request.model.clone());
    }
}

/// The program: the engine block's `command`, else `opencode` on the `PATH`.
pub(crate) fn program(request: &TurnRequest) -> &str {
    match request.settings.command.as_str() {
        "" => PROGRAM,
        configured => configured,
    }
}

/// The command's whole environment: the caller's variables on `curated`
/// (a child's turn's list or the TUI's), the config's and the credential's.
pub(crate) fn environment(
    engine: &Opencode,
    curated: &[&str],
    request: &TurnRequest,
) -> Result<BTreeMap<String, String>, EngineError> {
    let settings = &request.settings;
    let policy = settings.credentials(engine.credentials());
    let passes = |name: &str| {
        !withheld(name)
            && (curated.contains(&name)
                || settings.env.iter().any(|n| n == name)
                || policy.child_env.iter().any(|n| n == name))
    };
    let mut env: BTreeMap<String, String> = request
        .env
        .iter()
        .filter(|(name, _)| passes(name))
        .map(|(name, value)| (name.clone(), value.clone()))
        .collect();
    let home = request.home.clone();
    // The session home is a config directory in both home modes: the plugin
    // and the context are found there.
    env.insert("OPENCODE_CONFIG_DIR".to_string(), home.clone());
    match settings.session_home {
        SessionHome::Session => {
            env.insert("HOME".to_string(), home.clone());
            // On Windows the profile variables with it (node's
            // `os.homedir()` reads USERPROFILE there); none on unix.
            env.extend(run_ports::session_profile(&home, &request.env));
            for (variable, dir) in [
                ("XDG_CONFIG_HOME", "xdg/config"),
                ("XDG_DATA_HOME", "xdg/data"),
                ("XDG_STATE_HOME", "xdg/state"),
                ("XDG_CACHE_HOME", "xdg/cache"),
            ] {
                env.insert(variable.to_string(), format!("{home}/{dir}"));
            }
        }
        SessionHome::Host => {
            // Her home as the composition root resolved it (USERPROFILE on
            // Windows); her profile variables pass on as they are.
            let user = request
                .user_home
                .as_ref()
                .filter(|h| !h.is_empty())
                .ok_or_else(|| EngineError::NoHostHome {
                    engine: NAME.to_string(),
                })?;
            env.insert("HOME".to_string(), user.clone());
            for variable in [
                "XDG_CONFIG_HOME",
                "XDG_DATA_HOME",
                "XDG_STATE_HOME",
                "XDG_CACHE_HOME",
            ] {
                if let Some(value) = request.env.get(variable) {
                    env.insert(variable.to_string(), value.clone());
                }
            }
        }
    }
    Ok(env)
}

#[cfg(test)]
#[path = "drive.test.rs"]
mod tests;
