//! The steps of j001200 (recall) and of `cli/session.feature`'s reading Rule,
//! as a child of the base World.
//!
//! The March session is a real one: Alice set ctxloom up, ran a one-shot
//! asking the question, and the session was renamed to the harp everybody
//! half-remembers; an unrelated session sits beside it, so every search has a
//! wrong answer to give. Its record is what its hooks delivered (owner ruling
//! 87a), nothing seeded.
//!
//! The model is a stub `claude` first on `PATH` (the mock LLM of the steps).
//! It does what claude does with a session (owner ruling 115): it keeps the
//! conversation of each of its sessions in the config dir it was given (the
//! session home), and `--resume <id>` puts that session's conversation in
//! front of it again; an id it never had is claude's own error. Every launch
//! writes down its input, the resumed conversation and the prompt, beside the
//! stub (`model-input`), so "the mock recorded input" is what the last model
//! asked had in front of it. It fires the turn's prompt and words through the
//! hooks its one `--settings` file registers, as separate processes with its
//! own environment, so ctxloom records them as it records claude's.
//!
//! Two markers tell "found by its title" from "resumed with its
//! conversation": the first prompt carries one, the model's answer the other,
//! and the answer reaches a later model only through the engine's own
//! session.

use cucumber::{given, then};

use super::World;
use crate::tests::files;
use crate::tests::gherkin_common::CommonWorld;

/// The marker in the question the March session was opened with.
const TITLE_MARKER: &str = "J001200-TITLE-WORKTREE-QUESTION";
/// The March session everybody half-remembers.
const HARP: &str = "amber-quiet-heron";
/// The unrelated session beside it.
const NEIGHBOUR: &str = "brisk-copper-moth";
/// The question, which carries its marker.
const QUESTION: &str = "Why did we settle on that worktree layout? J001200-TITLE-WORKTREE-QUESTION";
/// The model's answer, which carries the conversation's own marker.
const ANSWER: &str =
    "Because leaf names have to name the project, not the branch. J001200-TRANSCRIPT-ONLY-MARKER";

/// The stub model (see the head of this file).
const MODEL_STUB: &str = r#"here="$(dirname "$0")"
settings=""
resume=""
while [ $# -gt 0 ]; do
  case "$1" in
    --settings) settings="$2"; shift ;;
    --resume) resume="$2"; shift ;;
  esac
  shift
done
prompt="$(cat)"
history="$CLAUDE_CONFIG_DIR/stub-history"
mkdir -p "$history"
if [ -n "$resume" ]; then
  id="$resume"
  if [ ! -f "$history/$id" ]; then
    echo "No conversation found with session ID: $id" >&2
    exit 1
  fi
else
  id="00000000-0000-4000-8000-$(printf '%s' "$prompt" | sha256sum | cut -c1-12)"
fi
{
  [ -z "$resume" ] || cat "$history/$id"
  printf '%s\n' "$prompt"
} > "$here/model-input"
reply="$(cat "$here/model-reply")"
turn=0
[ -f "$history/$id" ] && turn="$(wc -l < "$history/$id" | tr -d ' ')"
printf '%s\n%s\n' "$prompt" "$reply" >> "$history/$id"
fire() {
  [ -n "$settings" ] || return 0
  cmd="$(jq -r --arg e "$1" '.hooks[$e][0].hooks[0] | ([.command] + .args) | join(" ")' "$settings")"
  printf '%s' "$2" | sh -c "$cmd" > /dev/null 2>> "$here/hooks.err"
}
pid="$id-$turn"
fire UserPromptSubmit "$(jq -nc --arg s "$id" --arg i "$pid" --arg p "$prompt" '{session_id:$s,prompt_id:$i,permission_mode:"auto",hook_event_name:"UserPromptSubmit",prompt:$p}')"
fire MessageDisplay "$(jq -nc --arg s "$id" --arg i "$pid" --arg d "$reply" '{session_id:$s,prompt_id:$i,hook_event_name:"MessageDisplay",turn_id:($i+"-t"),message_id:($i+"-m"),index:0,final:true,delta:$d}')"
jq -nc --arg s "$id" '{type:"system",subtype:"init",session_id:$s}'
jq -nc --arg s "$id" --arg r "$reply" '{type:"result",subtype:"success",is_error:false,result:$r,session_id:$s}'
"#;

/// The stub model (see the head of this file) as `claude` in `bin`, beside
/// a `ctxloom` that runs the binary under test, which its hooks call.
pub(super) fn stub_model(bin: &std::path::Path) {
    let ctxloom = crate::tests::binary().display().to_string();
    files::put_program(bin.join("ctxloom"), &format!("exec '{ctxloom}' \"$@\"\n"));
    files::put_program(bin.join("claude"), MODEL_STUB);
}

impl World {
    /// The directory of the stub model and what it writes down.
    fn model_dir(&self) -> std::path::PathBuf {
        self.adopt_scratch().join("bin")
    }

    /// A one-shot asking `prompt`, answered `answer`, its session known to
    /// the scenario as `harp` (a harp is minted, never chosen: owner S55; the
    /// scenario's name is an alias of the minted one, and the session is
    /// titled by its first prompt as ever).
    fn record_session(&mut self, harp: &str, prompt: &str, answer: &str) {
        files::put(self.model_dir().join("model-reply"), answer);
        self.before_run();
        self.common
            .run_lines(&format!("ctxloom run --one-shot \"{prompt}\""), "");
        self.ran().succeeds();
        let minted = self.json()["harp"]
            .as_str()
            .unwrap_or_else(|| panic!("the reply names its session: {}", self.ran().said()))
            .to_string();
        self.common.aliases.push((harp.to_string(), minted));
        let hooks = files::read(self.model_dir().join("hooks.err")).unwrap_or_default();
        assert!(hooks.trim().is_empty(), "the model's hooks said: {hooks}");
    }

    /// The last command line run again in each format, `check` given each
    /// one's outcome (the scenario names no `--format`; both are read).
    fn in_both_formats(&mut self, check: impl Fn(&crate::tests::Ran, &str)) {
        let line = self.common.line.clone();
        for format in ["json", "text"] {
            self.common
                .run_lines(&format!("{line} --format {format}"), "");
            check(self.ran(), format);
        }
    }
}

// ------------------------------------------------------------------ given

#[given("a design question everyone remembers deciding and nobody remembers why")]
fn march(w: &mut World) {
    w.common.project = crate::tests::library::Project::bare();
    super::adopt::claude_code(w);
    super::adopt::token(w);
    w.common.set_env("CTXLOOM_ROOT", String::new());
    w.before_run();
    w.common.run_lines("ctxloom init", "");
    w.ran().succeeds();
    let bin = w.model_dir();
    stub_model(&bin);
    w.record_session(HARP, QUESTION, ANSWER);
    w.record_session(NEIGHBOUR, "unrelated chatter", "unrelated reply");
}

/// "the mock LLM responds" where the model is this file's stub (staged by
/// the journey's Background, which leaves its reply beside it): a fresh
/// mock, its answer, and nothing it was asked yet. False, and nothing done,
/// in any other scenario of the World (the base step's mock engine).
pub(super) fn mock_responds(w: &mut World, reply: &str) -> bool {
    let dir = w.model_dir();
    if !files::exists(dir.join("model-reply")) {
        return false;
    }
    files::put(dir.join("model-reply"), reply);
    let input = dir.join("model-input");
    if files::exists(&input) {
        files::remove(&input);
    }
    true
}

#[given("Alice has already resumed the March session")]
fn already_resumed(w: &mut World) {
    w.before_run();
    w.common.run_lines(
        &format!("ctxloom run --one-shot --session {HARP} -p default \"What did we conclude?\""),
        "",
    );
    w.ran().succeeds();
}

/// `cli/session.feature`'s "recorded session": finished, its first prompt
/// recorded as its hooks would.
#[given(expr = "a recorded session {string}")]
fn recorded(w: &mut World, harp: String) {
    w.plant(&harp, "finished");
}

// ------------------------------------------------------------------- then

#[then("the search names the March session by a phrase from its first prompt")]
fn search_names(w: &mut World) {
    w.ran().succeeds();
    let minted = |name: &str| {
        w.common
            .aliases
            .iter()
            .find(|(written, _)| written == name)
            .map(|(_, minted)| minted.clone())
            .expect("a recorded session")
    };
    let (march, neighbour) = (minted(HARP), minted(NEIGHBOUR));
    w.in_both_formats(|ran, format| {
        assert_eq!(ran.code, 0, "{format}: {}", ran.said());
        assert!(ran.stdout.contains(&march), "{format}: {}", ran.said());
        assert!(
            !ran.stdout.contains(&neighbour),
            "{format}: a search that returns everything is not a search: {}",
            ran.said()
        );
    });
}

#[then("ctxloom prints the question the session was opened with")]
fn prints_question(w: &mut World) {
    w.ran().succeeds();
    w.in_both_formats(|ran, format| {
        assert_eq!(ran.code, 0, "{format}: {}", ran.said());
        assert!(
            ran.stdout.contains(TITLE_MARKER),
            "{format}: {}",
            ran.said()
        );
    });
}

#[then(expr = "the mock recorded input contains {string}")]
fn recorded_input(w: &mut World, text: String) {
    let input = files::read(w.model_dir().join("model-input"))
        .unwrap_or_else(|e| panic!("the model was asked nothing ({e}): {}", w.ran().said()));
    assert!(
        input.contains(&text),
        "the model had in front of it: {input}"
    );
}

#[then("ctxloom refuses, naming the harp it could not find and the command that lists the sessions there are")]
fn refuses_unknown(w: &mut World) {
    assert_eq!(w.ran().code, 20, "{}", w.ran().said());
    w.in_both_formats(|ran, format| {
        assert_eq!(ran.code, 20, "{format}: {}", ran.said());
        assert!(
            ran.stderr.contains("no-such-harp-anywhere"),
            "{format}: {}",
            ran.said()
        );
        assert!(
            ran.stderr.contains("ctxloom session list"),
            "{format}: {}",
            ran.said()
        );
    });
}

#[then("no model was asked anything")]
fn no_model_asked(w: &mut World) {
    assert!(
        !files::exists(w.model_dir().join("model-input")),
        "the model was asked something: {}",
        w.ran().said()
    );
}

#[then(
    expr = "the JSON output array {string} contains an object whose {string} is {string} and that has no {string}"
)]
fn array_has_without(w: &mut World, path: String, key: String, value: String, absent: String) {
    let doc = w.json();
    assert!(
        super::array(&doc, &path)
            .iter()
            .any(|o| super::is(o, &key, &value) && o.get(&absent).is_none()),
        "no object with {key}={value} and no {absent}: {doc}"
    );
}

#[then(expr = "the JSON output array {string} contains no object whose {string} is {string}")]
fn array_lacks(w: &mut World, path: String, key: String, value: String) {
    let doc = w.json();
    assert!(
        !super::array(&doc, &path)
            .iter()
            .any(|o| super::is(o, &key, &value)),
        "an object with {key}={value}: {doc}"
    );
}

/// "Nothing matched" said in so many words: the text says `(no sessions)`,
/// the json is the daemon's answer with no session in it (`{}`, its
/// `sessions` empty and so left out), and neither is silence or a session's
/// name.
#[then("ctxloom says plainly that nothing matched")]
fn nothing_matched(w: &mut World) {
    w.ran().succeeds();
    w.in_both_formats(|ran, format| {
        assert_eq!(ran.code, 0, "{format}: {}", ran.said());
        let said = ran.stdout.trim();
        match format {
            "text" => assert_eq!(said, "(no sessions)", "{}", ran.said()),
            _ => assert_eq!(ran.json(), serde_json::json!({}), "{}", ran.said()),
        }
    });
}
