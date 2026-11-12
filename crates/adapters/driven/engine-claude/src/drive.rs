//! Claude Code's drive half: a child agent runs turn-as-command.
//!
//! One turn is one `claude -p` command: the prompt on stdin, the assembled
//! context in a file given to `--append-system-prompt-file`, the stream of
//! events on stdout (`--output-format stream-json --verbose`, read by
//! [`crate::stream`]). A later turn is a new command carrying
//! `--resume <native session id>`, the id turn 1's stream named. Nothing runs
//! between turns. The turn is heard through its hooks (owner ruling 51): the
//! session home's `ctxloom-hooks.json` registers the per-turn events and the
//! command names it with `--settings` (the agent's permissions are the home's
//! own `settings.json`, apart, owner ruling 145); the stream is read for the reply and the turn's
//! outcome.
//!
//! The command's environment is built here and is all it gets: the session
//! home as `CLAUDE_CONFIG_DIR` and as `HOME` (owner ruling 112; the user's
//! `~/.claude` is never named), and the caller's variables that are on
//! [`ENV_ALLOWLIST`].

use std::collections::BTreeMap;

use run_ports::{
    copy_in, Credentials, Engine, EngineError, HomeFile, PrepareRequest, Prepared, SessionHome,
    TurnCommand, TurnOutcome, TurnOutput, TurnRequest,
};

use surface_ports::Json;

use crate::{stream, Claude, NAME};

/// ctxloom's own settings file in the session home: its hooks, which the
/// command names with `--settings` (owner ruling 145: apart from the agent's
/// permissions, since claude refuses a settings file whole, hooks and all,
/// for one invalid value).
pub const HOOKS_FILE: &str = "ctxloom-hooks.json";

/// The user-scope settings file of claude's config dir: in the session home,
/// which is the session's CLAUDE_CONFIG_DIR, it holds the agent's
/// permissions document and nothing else (owner ruling 145).
pub const USER_SETTINGS_FILE: &str = "settings.json";

/// The key of claude's settings that says the user accepted its
/// bypass-permissions warning (claude 2.1.322: "Whether the user has accepted
/// the bypass permissions mode dialog").
pub const BYPASS_ACCEPTED_KEY: &str = "skipDangerousModePermissionPrompt";

/// claude's own state file in its config dir (the session home): what it
/// keeps of its onboarding and of each project it ran in.
pub const STATE_FILE: &str = ".claude.json";

/// The context file's name in the session home.
pub const CONTEXT_PROMPT_FILE: &str = "ctxloom-context.md";

/// The program: found on the `PATH` of the process that runs the command.
const PROGRAM: &str = "claude";

/// The variables of the caller's environment a turn is given: how to find
/// programs, locale and temp space, the proxy and certificate settings a
/// network needs, the two ways claude is handed a credential, and what the
/// turn's hook processes need (owner ruling 51): ctxloom's home by name (their
/// `HOME` is the session home, ruling 112), the session's harp, and the run's
/// listener (the endpoint, the bearer, the hook principal's secret, the run
/// and the listener's public certificate). On Windows,
/// what a program needs to start (`run_ports::WINDOWS_STARTUP_VARIABLES`) and
/// the user's profile directories (`run_ports::WINDOWS_PROFILE_VARIABLES`),
/// relocated into the session home as `HOME` is; none of them is set on
/// unix. The user's `HOME` and every other variable stay behind.
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
    "CLAUDE_CODE_OAUTH_TOKEN",
    "ANTHROPIC_API_KEY",
    run_ports::HOME_VARIABLE,
    run_ports::ENDPOINT_VARIABLE,
    run_ports::BEARER_VARIABLE,
    run_ports::RUN_VARIABLE,
    run_ports::TLS_CERT_VARIABLE,
    run_ports::SESSION_VARIABLE,
    run_ports::HARP_VARIABLE,
];

/// The variables of the caller's environment the human's terminal session is
/// given: a turn's, the primary's own token, and the terminal's type and
/// colour depth (`TERM`, `COLORTERM`), without which claude draws in no
/// colour.
pub const INTERACTIVE_ENV_ALLOWLIST: &[&str] = &[
    "PATH",
    "TERM",
    "COLORTERM",
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
    "CLAUDE_CODE_OAUTH_TOKEN",
    "ANTHROPIC_API_KEY",
    "ANTHROPIC_AUTH_TOKEN",
    run_ports::HOME_VARIABLE,
    run_ports::ENDPOINT_VARIABLE,
    run_ports::BEARER_VARIABLE,
    run_ports::RUN_VARIABLE,
    run_ports::TLS_CERT_VARIABLE,
    run_ports::SESSION_VARIABLE,
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

/// How the human gives her primary session a credential when no login is
/// shared with it: claude's own `/login`, or the long-lived token claude's
/// `setup-token` makes for a Pro, Max, Team or Enterprise plan.
pub const SIGN_IN: &str = "sign in with /login in the session, or run `claude setup-token` (it needs a Pro, Max, Team or Enterprise plan), then export CLAUDE_CODE_OAUTH_TOKEN";

/// The files of the user's own claude configuration that are copied into a
/// session home. None yet: which of them a child may see is the owner's to
/// rule, and until then no file of `~/.claude` crosses (one value does: her
/// acceptance of the bypass-permissions warning, owner ruling 166).
pub const COPY_IN_ALLOWLIST: &[&str] = &[];

impl Engine for Claude {
    fn name(&self) -> &str {
        NAME
    }

    fn prepare(&self, request: &PrepareRequest) -> Result<Prepared, EngineError> {
        // Hooks are how a session is heard, the terminal's and a child's turn
        // alike (owner ruling 51); a child registers the per-turn events only.
        let mut files = vec![
            HomeFile {
                rel: CONTEXT_PROMPT_FILE.to_string(),
                bytes: request.context.clone().into_bytes(),
            },
            HomeFile {
                rel: HOOKS_FILE.to_string(),
                bytes: hooks_settings(request, &self.program)?.into_bytes(),
            },
        ];
        if request.session_home == SessionHome::Session {
            files.push(HomeFile {
                rel: USER_SETTINGS_FILE.to_string(),
                bytes: user_settings(request, self.bypass_accepted)?.into_bytes(),
            });
        }
        let existing = request.existing.get(STATE_FILE).map(Vec::as_slice);
        if let Some(bytes) = seeded_state(existing, request.cwd.as_deref()) {
            files.push(HomeFile {
                rel: STATE_FILE.to_string(),
                bytes,
            });
        }
        Ok(Prepared {
            files,
            copy_in: copy_in(COPY_IN_ALLOWLIST, &request.offered),
            seeds: Vec::new(),
            context_path: format!("{}/{CONTEXT_PROMPT_FILE}", request.home),
        })
    }

    fn merges(&self) -> &'static [&'static str] {
        &[STATE_FILE]
    }

    fn drive_turn(&self, request: &TurnRequest) -> Result<TurnCommand, EngineError> {
        let mut command = TurnCommand::new(program(request));
        command.args = ["-p", "--output-format", "stream-json", "--verbose"]
            .map(String::from)
            .to_vec();
        command.args.push("--append-system-prompt-file".to_string());
        command.args.push(request.context_path.clone());
        // The turn's hooks: the session dir's settings, as the terminal's.
        push_settings(&mut command.args, request);
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
            user_dir_variable: None,
            login_file: Some(LOGIN_FILE),
            home_dir: "",
            sign_in: SIGN_IN,
            login_optional: false,
            // macOS keeps claude's login in a keychain item per config dir
            // (a file link is ignored); Windows grants a symlink only to a
            // privileged user. Neither is shared by a link, nor copied.
            no_link_on: &["macos", "windows"],
            copy_on: &[],
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
        push_settings(&mut command.args, request);
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

/// The agent's permissions document, parsed; none, none.
fn document(request: &PrepareRequest) -> Result<Option<Json>, EngineError> {
    request
        .permissions
        .as_deref()
        .map(|text| {
            Json::parse(text).map_err(|ext_err_txt| EngineError::BadPermissions {
                engine: NAME.to_string(),
                ext_err_txt,
            })
        })
        .transpose()
}

/// The session home's user-scope `settings.json` (claude's config dir is the
/// session home): `{"permissions": <the agent's document>}` exactly as it was
/// handed (owner rulings 135, 137: claude's own words, never checked; the
/// mode is its `defaultMode`, and no `--permission-mode` is passed), else
/// `{}`, written whole at every launch (T21). It is a file of its own, apart
/// from the hooks (owner ruling 145): claude refuses a settings file whole for
/// one invalid value ("These settings files did not load, so nothing in them
/// is in effect, including hooks and permission rules", probed on 2.1.322),
/// so a bad value here disables the block alone and the hooks still load.
///
/// User scope is claude's lowest file scope: the project's own
/// `.claude/settings.json` (and `.local`) override a single value of it, and
/// the lists of every scope merge (claude's settings docs, "Settings
/// precedence", "Lists merge instead of overriding"). It is also the scope
/// whose `defaultMode` may be `auto` or `bypassPermissions` (those "don't take
/// effect from project or local settings"). A profile's `deny_tools` stays the
/// project file's `permissions.deny` (surface apply), merged by claude.
///
/// A fresh config dir would ask again for the bypass-permissions warning the
/// user accepted in her own settings, whose default answer ends the session;
/// when she accepted it (`bypass_accepted`), the file carries her acceptance
/// beside the document, and nothing else of her settings (owner ruling 166).
fn user_settings(request: &PrepareRequest, bypass_accepted: bool) -> Result<String, EngineError> {
    let mut doc = match document(request)? {
        Some(permissions) => Json::object([("permissions", permissions)]),
        None => Json::Object(Vec::new()),
    };
    if bypass_accepted {
        doc.set(BYPASS_ACCEPTED_KEY, Json::Bool(true));
    }
    Ok(format!("{}\n", doc.pretty()))
}

/// Whether the user's own claude settings (the text of her config dir's
/// `settings.json`) say she accepted the bypass-permissions warning: the key
/// at the top, `true`; anything else, or a file that does not read, is not.
pub fn accepts_bypass(user_settings: &str) -> bool {
    Json::parse(user_settings)
        .ok()
        .is_some_and(|doc| doc.get(BYPASS_ACCEPTED_KEY) == Some(&Json::Bool(true)))
}

/// ctxloom's hooks file, which the command names with `--settings`: the
/// hooks (the terminal's events or a child's turn's) and, in the session
/// home, nothing else. Under `session_home: host` the config dir is the
/// user's own `~/.claude`, whose `settings.json` ctxloom never writes, so the
/// agent's permissions are this file's `permissions` there (the one key, so
/// it never overwrites the hooks), with the cost ruling 145 avoids in the
/// session home: one invalid value in them disables the hooks too.
fn hooks_settings(request: &PrepareRequest, program: &str) -> Result<String, EngineError> {
    let hooks = crate::hooks::home_settings(request.interactive, program);
    let permissions = document(request)?.filter(|_| request.session_home == SessionHome::Host);
    let Some(permissions) = permissions else {
        return Ok(hooks);
    };
    let mut settings = Json::parse(&hooks).map_err(|ext_err_txt| EngineError::BadPermissions {
        engine: NAME.to_string(),
        ext_err_txt,
    })?;
    settings.set("permissions", permissions);
    Ok(format!("{}\n", settings.pretty()))
}

/// claude's state file with the onboarding marked done and `cwd` trusted
/// (owner ruling 113: the keys claude 2.1.322 reads; a release that changes
/// them shows its onboarding again, visible and harmless), merged into what
/// the home holds (`existing`; none, a fresh file). A file that is not a JSON
/// object is claude's to deal with: none, the file is left as it is.
fn seeded_state(existing: Option<&[u8]>, cwd: Option<&str>) -> Option<Vec<u8>> {
    let mut state = match existing {
        None => Json::Object(Vec::new()),
        Some(bytes) => match std::str::from_utf8(bytes).ok().map(Json::parse) {
            Some(Ok(doc @ Json::Object(_))) => doc,
            _ => return None,
        },
    };
    state.set("hasCompletedOnboarding", Json::Bool(true));
    if let Some(dir) = cwd {
        let project = object_at(object_at(&mut state, "projects"), dir);
        project.set("hasTrustDialogAccepted", Json::Bool(true));
    }
    Some(format!("{}\n", state.pretty()).into_bytes())
}

/// The object member `key` of `parent` (an object), made an empty object
/// when it is missing or is not one.
fn object_at<'a>(parent: &'a mut Json, key: &str) -> &'a mut Json {
    if !matches!(parent.get(key), Some(Json::Object(_))) {
        parent.set(key, Json::Object(Vec::new()));
    }
    // Present and an object: set just above when it was not.
    parent.get_mut(key).expect("the member was just set")
}

/// `--settings <session home>/ctxloom-hooks.json`. claude reads one
/// `--settings` file (the last wins) and adds its hooks to the config
/// directory's own, so the ctxloom-owned file `prepare` wrote in the session
/// dir serves both home modes and both kinds of run; nothing is written into
/// the user's own directory. (In a container this path must be the in-container one: a
/// follow-up.)
fn push_settings(args: &mut Vec<String>, request: &TurnRequest) {
    args.push("--settings".to_string());
    args.push(format!("{}/{HOOKS_FILE}", request.home));
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

/// The variable that is the primary's alone: never a child's.
const PRIMARY_ONLY: &str = "ANTHROPIC_AUTH_TOKEN";

/// Whether the caller's variable `name` may be handed to this command.
fn grantable(name: &str, interactive: bool) -> bool {
    interactive || name != PRIMARY_ONLY
}

/// The command's whole environment: `caller`'s variables on the curated
/// allowlist, on the engine block's `env` and on the configured credential
/// source: a bounded, enumerable set (the container runtime reads it), never
/// the whole environment, and
/// `CLAUDE_CONFIG_DIR` and `HOME` set to the session's home, or to the user's
/// own directory and home when the engine block says `session_home: host`. A
/// child is never given the primary's token.
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
    // HOME is the session home (owner ruling 112, parity with opencode): what
    // the engine's tools keep under `~` lands in the session's own directory,
    // never in the project. Under `session_home: host` it is the user's own.
    // On Windows its profile variables are relocated with it (node's
    // `os.homedir()` reads USERPROFILE there); under `host` they are passed
    // on as they are. Her home is the one the composition root resolved.
    let (home, config_dir) = match settings.session_home {
        SessionHome::Session => {
            env.extend(run_ports::session_profile(&request.home, &request.env));
            (request.home.clone(), request.home.clone())
        }
        SessionHome::Host => {
            let home = request
                .user_home
                .as_ref()
                .filter(|home| !home.is_empty())
                .ok_or_else(|| EngineError::NoHostHome {
                    engine: NAME.to_string(),
                })?;
            (home.clone(), format!("{home}/{}", LOGIN_DIR))
        }
    };
    env.insert("HOME".to_string(), home);
    env.insert("CLAUDE_CONFIG_DIR".to_string(), config_dir);
    Ok(env)
}

#[cfg(test)]
#[path = "drive.test.rs"]
mod tests;
