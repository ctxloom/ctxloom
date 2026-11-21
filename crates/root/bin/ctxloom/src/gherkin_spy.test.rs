//! j002200's recording spy, shared by the Worlds whose features read an
//! engine's environment back out of the process the run spawned (j002200's
//! session-home outlines in the base World, agent.feature's `--session-home`
//! Rule in the surfaces World): claude-code is a stub first on `PATH` that
//! records each turn's working directory and its `HOME` and
//! `CLAUDE_CONFIG_DIR`, writes a note where it works, and replies.

use std::path::{Path, PathBuf};

use super::gherkin_common::Common;
use super::Ran;
use crate::tests::files;

/// The stub claude: each turn's working directory appended to `cwds`, its
/// `HOME` and `CLAUDE_CONFIG_DIR` to `homes`, a note written where it
/// works, and a reply.
pub(crate) const STUB: &str = r#"here="$(dirname "$0")"
pwd -P >> "$here/cwds"
printf '%s\t%s\n' "$HOME" "$CLAUDE_CONFIG_DIR" >> "$here/homes"
cat > /dev/null
echo "a short note" > note.txt
echo '{"type":"system","subtype":"init","session_id":"0c3f2c1e-7b9a-4d1e-9a51-2f6c1d0e8b7a"}'
echo '{"type":"result","subtype":"success","is_error":false,"result":"J002200-NOTE-WRITTEN","session_id":"0c3f2c1e-7b9a-4d1e-9a51-2f6c1d0e8b7a"}'
"#;

/// What the stub replies, so a run that reached it is told apart.
pub(crate) const REPLY: &str = "J002200-NOTE-WRITTEN";

/// The token a child authenticates by.
const TOKEN: &str = "sk-ant-oat01-acceptance-fixture-not-real"; // gitleaks:allow

/// The stub installed as `claude` in `bin`, first on the commands' `PATH`.
pub(crate) fn install(common: &mut Common, bin: &Path) {
    files::put_program(bin.join("claude"), STUB);
    let host = crate::tests::host_var("PATH").unwrap_or_default();
    common.set_env(
        "PATH",
        format!("{}:{}", bin.display(), host.to_string_lossy()),
    );
}

/// What a claude-code child needs to authenticate: its token.
pub(crate) fn authenticate(common: &mut Common) {
    common.set_env("CLAUDE_CODE_OAUTH_TOKEN", TOKEN.to_string());
}

/// A path as the host resolves it (the stub prints `pwd -P`).
pub(crate) fn resolved(path: &Path) -> PathBuf {
    if files::exists(path) {
        files::canonical(path)
    } else {
        path.to_path_buf()
    }
}

/// Each turn's `HOME` and `CLAUDE_CONFIG_DIR`, in order, as the stub in
/// `bin` saw them.
pub(crate) fn turn_homes(bin: &Path) -> Vec<(PathBuf, PathBuf)> {
    files::read(bin.join("homes"))
        .unwrap_or_default()
        .lines()
        .map(|line| {
            let (home, config) = line.split_once('\t').expect("HOME\tCLAUDE_CONFIG_DIR");
            (PathBuf::from(home), PathBuf::from(config))
        })
        .collect()
}

/// The one turn's `HOME` and `CLAUDE_CONFIG_DIR`, the run having reached
/// the stub and succeeded.
pub(crate) fn one_turn_home(ran: &Ran, bin: &Path) -> (PathBuf, PathBuf) {
    ran.succeeds().contains(REPLY);
    match turn_homes(bin).as_slice() {
        [one] => one.clone(),
        other => panic!("one turn, not {other:?}"),
    }
}

/// The session home of the run's session (`harp`, from its json answer).
pub(crate) fn session_home(common: &Common) -> PathBuf {
    let harp = common.json()["harp"]
        .as_str()
        .expect("the run's harp")
        .to_string();
    resolved(
        &common
            .project
            .dir()
            .join(".ctxloom/sessions")
            .join(harp)
            .join("home"),
    )
}

/// The codes of the json documents the run said on its error stream.
pub(crate) fn said_codes(ran: &Ran) -> Vec<String> {
    serde_json::Deserializer::from_str(&ran.stderr)
        .into_iter::<serde_json::Value>()
        .map(|doc| {
            let doc = doc.unwrap_or_else(|e| panic!("{e}: {}", ran.said()));
            doc["code"].as_str().unwrap_or_default().to_string()
        })
        .collect()
}

/// The run said, in the approved words, that it runs in her own home
/// (`place` the key that set `host`: `agents.<a>` or `engines.<e>`).
pub(crate) fn says_her_own_home(ran: &Ran, place: &str) {
    let codes = said_codes(ran);
    assert!(
        codes
            .iter()
            .any(|code| code == "library-application-notice-session-home-host"),
        "{codes:?}: {}",
        ran.said()
    );
    ran.contains("runs in your own home")
        .contains(&format!("({place}.session_home: host)"))
        .contains("reads and changes your own memory")
        .contains("a relocation, not confinement");
}
