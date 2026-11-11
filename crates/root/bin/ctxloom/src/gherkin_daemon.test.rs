//! The steps of the user daemon's spec (ruling 132, the target T1-T12):
//! `journeys/j002700_user_daemon.feature`, `cli/daemon.feature`,
//! `cli/session_children.feature` and the daemon Rule of `cli/run.feature`,
//! as the owner's rulings on the spec calls (S1-S20, 2026-11-11) shaped them;
//! and trust and approvals (ruling 142): `journeys/j002800_trusting_a_project.feature`,
//! `cli/trust.feature` and `cli/session_approvals.feature`; and the
//! architecture rulings T13-T30: `cli/mcp_serve.feature` and
//! `cli/atomic_writes.feature`.
//!
//! RED BY DESIGN. The spec is committed before the daemon is reworked to
//! it, so every step here that the existing harness cannot already take is a
//! stub that FAILS, naming itself: nothing is implemented. Each stub becomes
//! a real step with the milestone that builds what it asserts.

use cucumber::{given, then, when};

use super::World;

/// A step of the daemon's spec that nothing implements yet.
fn red(step: &str) -> ! {
    panic!("RED (ruling 132, the daemon spec): not built yet: {step}")
}

/// The `Alice ...:` steps of the spec that need a harness not built yet
/// (the generic `Alice ...:` step is the World's `alice_runs`, which hands
/// it here): whether it is one. The ones the existing harness can run (her
/// own commands, typed) are left to it.
pub(super) fn claims(w: &mut World, step: &cucumber::gherkin::Step) -> bool {
    let _ = w;
    let text = step.value.as_str();
    let stubbed = text.contains("presses Ctrl-C while it works")
        || text.contains("in both projects at once")
        || text.contains("which her daemon runs")
        || text.contains("and ctxloom is sent a ")
        || text.contains("through her daemon")
        || text.contains("its daemon is killed outright while it works")
        || text.contains("at her terminal, and answers yes when asked")
        || text.contains("says yes at her terminal");
    if stubbed {
        red(text);
    }
    false
}

// ------------------------------------------------------------------ given

#[given("no ctxloom daemon is serving for Alice")]
fn none_serving_for_alice(_: &mut World) {
    red("no ctxloom daemon is serving for Alice")
}

#[given(regex = r#"^Alice has a second project, "([^"]+)", set up with ctxloom$"#)]
fn second_project(_: &mut World, _name: String) {
    red("Alice has a second project, set up with ctxloom")
}

#[given(regex = r#"^Alice's session "([^"]+)" is running$"#)]
fn alices_session_running(_: &mut World, _harp: String) {
    red("Alice's session is running")
}

#[given(regex = r#"^Alice's session "([^"]+)" is running, with a child "([^"]+)" working for it$"#)]
fn alices_session_with_child(_: &mut World, _owner: String, _child: String) {
    red("Alice's session is running, with a child working for it")
}

#[given(
    "Alice's daemon has nothing to do: no session runs, no child is hosted and no command is open"
)]
fn alices_daemon_idle(_: &mut World) {
    red("Alice's daemon has nothing to do")
}

#[given("no ctxloom daemon is serving for this home")]
fn none_serving(_: &mut World) {
    red("no ctxloom daemon is serving for this home")
}

#[given("the discovery file names a daemon that no longer runs")]
fn stale_discovery(_: &mut World) {
    red("the discovery file names a daemon that no longer runs")
}

#[given("no ctxloom daemon can be started for this home")]
fn unstartable(_: &mut World) {
    red("no ctxloom daemon can be started for this home")
}

#[given("a ctxloom daemon is serving for this home")]
fn serving(_: &mut World) {
    red("a ctxloom daemon is serving for this home")
}

#[given(expr = "a ctxloom daemon is serving for this home, hosting a child {string}")]
fn serving_hosting(_: &mut World, _child: String) {
    red("a ctxloom daemon is serving for this home, hosting a child")
}

#[given("a ctxloom daemon is serving for this home, and has nothing running, hosted or open")]
fn serving_idle(_: &mut World) {
    red("a ctxloom daemon is serving for this home, and has nothing to do")
}

#[given(regex = r"^a ctxloom daemon is serving for this home, with (.+)$")]
fn serving_busy(_: &mut World, _busy: String) {
    red("a ctxloom daemon is serving for this home, with something to do")
}

#[given(regex = r"^the daemon will be killed outright (.+)$")]
fn killed_at(_: &mut World, _when: String) {
    red("the daemon will be killed outright at a step")
}

#[given(expr = "a child {string} that is open and idle")]
fn child_idle(_: &mut World, _child: String) {
    red("a child that is open and idle")
}

#[given(expr = "a child {string} that is in the middle of a turn")]
fn child_mid_turn(_: &mut World, _child: String) {
    red("a child that is in the middle of a turn")
}

#[given(expr = "a child {string} that has been closed")]
fn child_closed(_: &mut World, _child: String) {
    red("a child that has been closed")
}

#[given(expr = "a child {string} of {string} that is open and idle")]
fn child_of_idle(_: &mut World, _child: String, _owner: String) {
    red("a child of an owner, open and idle")
}

#[given("a hand-off file is left for a child that is no longer open")]
fn stale_hand_off(_: &mut World) {
    red("a hand-off file is left for a child that is no longer open")
}

// ------------------------------------------------------------------- when

#[when(
    "Alice's daemon is killed outright while her assistant works, and her assistant goes on firing its hooks"
)]
fn alices_daemon_killed(_: &mut World) {
    red("Alice's daemon is killed outright while her assistant works")
}

#[when("her daemon's idle time passes")]
fn her_idle_passes(_: &mut World) {
    red("her daemon's idle time passes")
}

#[when("the daemon's idle time passes")]
fn idle_passes(_: &mut World) {
    red("the daemon's idle time passes")
}

#[when(expr = "two commands are run at once, {string} and {string}")]
fn two_at_once(_: &mut World, _first: String, _second: String) {
    red("two commands are run at once")
}

#[when("a client calls the daemon with a bearer it did not mint")]
fn foreign_bearer(_: &mut World) {
    red("a client calls the daemon with a bearer it did not mint")
}

#[when("the daemon is stopped and the next command starts another")]
fn restarted(_: &mut World) {
    red("the daemon is stopped and the next command starts another")
}

#[when("the session's assistant fires its UserPromptSubmit hook")]
fn hook_fires(_: &mut World) {
    red("the session's assistant fires its UserPromptSubmit hook")
}

#[when("Alice asks her assistant one thing, which the killed daemon leaves unfinished")]
fn unfinished(_: &mut World) {
    red("Alice asks her assistant one thing, which the killed daemon leaves unfinished")
}

// ------------------------------------------------------- then: the journey

#[then("a ctxloom daemon is serving for Alice, started by that command")]
fn started_by_command(_: &mut World) {
    red("a ctxloom daemon is serving for Alice, started by that command")
}

#[then("that command used the same daemon, and started none")]
fn same_daemon(_: &mut World) {
    red("that command used the same daemon, and started none")
}

#[then("both sessions were served by the one daemon")]
fn one_daemon(_: &mut World) {
    red("both sessions were served by the one daemon")
}

#[then("each session's record is its own project's, in the one store her daemon keeps")]
fn records_in_one_store(_: &mut World) {
    red("each session's record is its own project's, in the one store")
}

#[then("each project lists its own session, and not the other's")]
fn own_listing(_: &mut World) {
    red("each project lists its own session, and not the other's")
}

#[then("her assistant ran on her terminal exactly as her daemon prepared it")]
fn ran_as_prepared(_: &mut World) {
    red("her assistant ran on her terminal exactly as her daemon prepared it")
}

#[then(
    "the command she typed recorded nothing itself: her daemon recorded the session and prepared its session home"
)]
fn frontend_recorded_nothing(_: &mut World) {
    red("the command she typed recorded nothing itself")
}

#[then(
    "the session's transcript holds what her assistant's hooks delivered to her daemon, each once"
)]
fn transcript_via_daemon(_: &mut World) {
    red("the session's transcript holds what the hooks delivered to her daemon")
}

#[then("her assistant ran under her daemon, not under the command she typed")]
fn ran_under_daemon(_: &mut World) {
    red("her assistant ran under her daemon")
}

#[then("her daemon stopped her assistant, and it was gone before the command exited")]
fn daemon_stopped_it(_: &mut World) {
    red("her daemon stopped her assistant")
}

#[then("ctxloom exited 130, saying the run was stopped by SIGINT")]
fn exited_sigint(_: &mut World) {
    red("ctxloom exited 130, saying the run was stopped by SIGINT")
}

#[then(regex = r#"^"([^"]+)" is listed as a child of "([^"]+)", run by her daemon$"#)]
fn listed_child(_: &mut World, _child: String, _owner: String) {
    red("the child is listed under its owner, run by her daemon")
}

#[then(regex = r#"^"([^"]+)"'s first turn was "([^"]*)"$"#)]
fn first_turn(_: &mut World, _child: String, _text: String) {
    red("the child's first turn")
}

#[then(regex = r#"^"([^"]+)"'s next turn was "([^"]*)"$"#)]
fn next_turn(_: &mut World, _child: String, _text: String) {
    red("the child's next turn")
}

#[then(regex = r#"^"([^"]+)" is listed as closed, and its assistant has exited$"#)]
fn listed_closed(_: &mut World, _child: String) {
    red("the child is listed as closed, and its assistant has exited")
}

#[then(regex = r#"^nothing "([^"]+)" was launched with is left at rest$"#)]
fn nothing_at_rest(_: &mut World, _child: String) {
    red("nothing the child was launched with is left at rest")
}

#[then("a new ctxloom daemon is serving for Alice")]
fn new_daemon(_: &mut World) {
    red("a new ctxloom daemon is serving for Alice")
}

#[then(regex = r#"^"([^"]+)" is listed as live, and its assistant never stopped$"#)]
fn still_live(_: &mut World, _harp: String) {
    red("the session is listed as live, and its assistant never stopped")
}

#[then(
    regex = r#"^"([^"]+)" found the new daemon and delivered again what the old one had not answered, and the record holds each once$"#
)]
fn redelivered(_: &mut World, _harp: String) {
    red("the session redelivered to the new daemon, each once")
}

#[then(
    regex = r#"^"([^"]+)" is working again in its own conversation, with what it was launched with$"#
)]
fn rehosted(_: &mut World, _child: String) {
    red("the child is re-hosted from its hand-off file")
}

#[then("her daemon has exited, and nothing says it serves")]
fn her_daemon_exited(_: &mut World) {
    red("her daemon has exited, and nothing says it serves")
}

#[then("it proves itself with a new certificate, and knows her by a new credential")]
fn new_credentials(_: &mut World) {
    red("a new certificate and a new user bearer")
}

// ---------------------------------------------------- then: cli/daemon

#[then("a ctxloom daemon serves for this home, started detached in a session of its own")]
fn started_detached(_: &mut World) {
    red("a ctxloom daemon serves, started detached")
}

#[then(expr = "the daemon's output goes to {string}")]
fn daemon_log(_: &mut World, _path: String) {
    red("the daemon's output goes to its log")
}

#[then("both commands succeed")]
fn both_succeed(_: &mut World) {
    red("both commands succeed")
}

#[then("exactly one daemon served them both")]
fn exactly_one(_: &mut World) {
    red("exactly one daemon served them both")
}

#[then("a new daemon serves, and the discovery file names it")]
fn replaced(_: &mut World) {
    red("a new daemon serves, and the discovery file names it")
}

#[then("no project store was opened, and no engine was started")]
fn nothing_opened(_: &mut World) {
    red("no project store was opened, and no engine was started")
}

#[then("no ctxloom daemon was started")]
fn none_started(_: &mut World) {
    red("no ctxloom daemon was started")
}

#[then(
    "the discovery file is readable by its owner alone, in a directory only its owner can enter"
)]
fn discovery_private(_: &mut World) {
    red("the discovery file is 0600 in a 0700 directory")
}

#[then(
    "the discovery file names the daemon's endpoint, its certificate, a user bearer and its pid"
)]
fn discovery_holds(_: &mut World) {
    red("the discovery file names the endpoint, certificate, bearer and pid")
}

#[then(regex = r#"^the output reports "([^"]+)" as the daemon's pid$"#)]
fn reports_pid(_: &mut World, _path: String) {
    red("the output reports the daemon's pid")
}

#[then(regex = r#"^the output reports "([^"]+)" as the discovery file's endpoint$"#)]
fn reports_endpoint(_: &mut World, _path: String) {
    red("the output reports the discovery file's endpoint")
}

#[then("neither the output, the daemon's log nor the project's store holds the user bearer")]
fn bearer_unseen(_: &mut World) {
    red("the user bearer is never shown, logged or recorded")
}

#[then("the call is refused as unauthenticated, and the refusal names no reason")]
fn unauthenticated(_: &mut World) {
    red("the call is refused as unauthenticated")
}

#[then("the new discovery file holds another certificate and another bearer")]
fn rotated(_: &mut World) {
    red("the new discovery file holds another certificate and another bearer")
}

#[then("a client still holding the old bearer is refused")]
fn old_bearer_refused(_: &mut World) {
    red("a client still holding the old bearer is refused")
}

#[then("the daemon has exited, and nothing says it serves")]
fn daemon_exited(_: &mut World) {
    red("the daemon has exited, and nothing says it serves")
}

#[then("the daemon still serves")]
fn still_serves(_: &mut World) {
    red("the daemon still serves")
}

#[then("the hook exits 0, and says nothing was left unrecorded")]
fn hook_delivered(_: &mut World) {
    red("the hook exits 0, and says nothing was left unrecorded")
}

#[then("a ctxloom daemon serves for this home, started by that hook")]
fn started_by_hook(_: &mut World) {
    red("a ctxloom daemon serves, started by that hook")
}

#[then("the session's transcript holds the prompt, once")]
fn prompt_once(_: &mut World) {
    red("the session's transcript holds the prompt, once")
}

#[then("the hook exits 0, and its answer to the engine is unchanged")]
fn hook_fails_open(_: &mut World) {
    red("the hook exits 0, and its answer to the engine is unchanged")
}

#[then(expr = "the hook warns {string} for the prompt, within its bound")]
fn hook_warns(_: &mut World, _code: String) {
    red("the hook warns that the prompt was not recorded, within its bound")
}

#[then("no run is left owed a runner, no incarnation is left open and no fact is recorded twice")]
fn record_whole(_: &mut World) {
    red("the record is whole after the daemon was killed")
}

// ------------------------------------------- then: cli/session_children

#[then(expr = "the child {string} of {string} ran its first turn on {string}")]
fn child_first_turn(_: &mut World, _child: String, _owner: String, _text: String) {
    red("the child ran its first turn on its task")
}

#[then("the child's hand-off file is readable by its owner alone while the child is open")]
fn hand_off_private(_: &mut World) {
    red("the child's hand-off file is 0600 while the child is open")
}

#[then("no hand-off file was left behind")]
fn no_hand_off(_: &mut World) {
    red("no hand-off file was left behind")
}

#[then(regex = r#"^the child ran a turn on "([^"]*)"$"#)]
fn child_ran_turn(_: &mut World, _text: String) {
    red("the child ran a turn on the message")
}

#[then(regex = r#"^the child ran no turn, and lists "([^"]*)" as waiting for one$"#)]
fn child_waits(_: &mut World, _text: String) {
    red("the child ran no turn, and lists the message as pending")
}

#[then(expr = "the child's turn in flight was interrupted, and its next turn was {string}")]
fn child_steered(_: &mut World, _text: String) {
    red("the child's turn in flight was interrupted by the steer")
}

#[then(expr = "the child ran one turn on {string}")]
fn child_one_turn(_: &mut World, _text: String) {
    red("the child ran one turn on the message")
}

#[then("the child's next turn runs in a new conversation of the same session")]
fn child_cleared(_: &mut World) {
    red("the child's next turn runs in a new conversation of the same session")
}

#[then(
    expr = "the child {string} is listed as closed, its engine has exited and its hand-off file is gone"
)]
fn child_ended(_: &mut World, _child: String) {
    red("the child is closed, its engine exited and its hand-off file gone")
}

// ------------------------------------------------- then: cli/run's Rule

#[then("the assistant was a child of the daemon, and its session's lock was the daemon's")]
fn one_shot_hosted(_: &mut World) {
    red("the one-shot's assistant was the daemon's child")
}

#[then("the daemon asked the assistant to stop, and it was gone before the command exited")]
fn daemon_asked_stop(_: &mut World) {
    red("the daemon asked the assistant to stop")
}

#[then(
    regex = r"^the command exits 128 plus the signal's number, (\d+), saying the run was stopped by (SIG[A-Z]+)$"
)]
fn exits_by_signal(_: &mut World, _code: String, _signal: String) {
    red("the command exits 128 plus the signal's number")
}

#[then(
    "the assistant was started with the program, arguments, environment and directory of the daemon's launch, and nothing else"
)]
fn started_as_launched(_: &mut World) {
    red("the TUI was started exactly as the daemon's launch says")
}

#[then(
    "the command took the session's lock before the assistant started and let it go after it exited"
)]
fn frontend_lock(_: &mut World) {
    red("the command held the session's lock around the TUI")
}

#[then("the daemon recorded the assistant's start and its exit, each once")]
fn start_and_exit(_: &mut World) {
    red("the daemon recorded the TUI's start and exit, each once")
}

#[then("no session was minted")]
fn no_mint(_: &mut World) {
    red("no session was minted")
}

#[then("the assistant was never stopped")]
fn never_stopped(_: &mut World) {
    red("the assistant was never stopped")
}

#[then("the command started a new daemon, and reported the assistant's exit to it once")]
fn reported_to_new(_: &mut World) {
    red("the command reported the TUI's exit to a new daemon, once")
}

// ---------------------------------------- the owner's rulings S8 to S20

#[then("the hosted engines were ended cleanly before the daemon exited")]
fn ended_cleanly(_: &mut World) {
    red("the hosted engines were ended cleanly before the daemon exited")
}

#[then(
    "the run the killed daemon hosted ended lost, and its turn in flight ended failed, with its owner told"
)]
fn lost_and_told(_: &mut World) {
    red("the lost run's turn in flight ended failed, and its owner was told")
}

#[then(
    regex = r#"^"([^"]+)" runs again as its session's next incarnation, in its own conversation$"#
)]
fn next_incarnation(_: &mut World, _child: String) {
    red("the child runs again as its session's next incarnation")
}

#[then("the command started a new daemon and sent the write once more, under the same id")]
fn retried_once(_: &mut World) {
    red("the command retried its write once on a new daemon, under the same id")
}

#[then("the write was applied once, and its second delivery was a duplicate")]
fn applied_once(_: &mut World) {
    red("the write was applied once, and its retry was a duplicate")
}

#[then("the daemon's confirmation was asked at her terminal, and the session is gone")]
fn asked_at_terminal(_: &mut World) {
    red("the daemon's confirmation was asked at her terminal")
}

#[when("its orchestrator calls a ctxloom tool whose daemon call asks for confirmation")]
fn tool_asks(_: &mut World) {
    red("its orchestrator calls a ctxloom tool whose daemon call asks for confirmation")
}

#[then(
    "the question reached its engine as an MCP elicitation, and the engine's answer reached the daemon"
)]
fn elicited(_: &mut World) {
    red("the question reached its engine as an MCP elicitation")
}

#[given(expr = "an interactive session {string} whose assistant is running on its terminal")]
fn primary_running(_: &mut World, _harp: String) {
    red("an interactive session whose assistant is running on its terminal")
}

#[then("the assistant on its terminal was asked to stop, then killed once its grace had passed")]
fn terminal_stopped(_: &mut World) {
    red("the assistant on its terminal was stopped, gracefully, then killed")
}

#[then(regex = r#"^"([^"]+)" is listed as closed$"#)]
fn closed_listed(_: &mut World, _harp: String) {
    red("the session is listed as closed")
}

#[given(
    expr = "a session {string} whose engine environment names ANTHROPIC_API_KEY through its engine block"
)]
fn owner_curated_env(_: &mut World, _harp: String) {
    red("a session whose engine environment names ANTHROPIC_API_KEY")
}

#[given("the daemon was started from a shell holding CLAUDE_CODE_OAUTH_TOKEN")]
fn daemon_env_token(_: &mut World) {
    red("the daemon was started from a shell holding CLAUDE_CODE_OAUTH_TOKEN")
}

#[when(expr = "its orchestrator launches a child {string}")]
fn orchestrator_launches(_: &mut World, _child: String) {
    red("its orchestrator launches a child")
}

#[then(expr = "the child {string} was given ANTHROPIC_API_KEY, and not CLAUDE_CODE_OAUTH_TOKEN")]
fn child_given_curated(_: &mut World, _child: String) {
    red("the child was given the owner's curated credential only")
}

// ------------------------------------------------------ trust (ruling 142)

#[given(
    "Alice has cloned a project whose own ctxloom config names the program that runs as her assistant"
)]
fn cloned_project(_: &mut World) {
    red("Alice has cloned a project whose config names its own assistant program")
}

#[then(
    regex = r#"^she is told the project's own "([^"]+)" waits for her trust, and that `ctxloom trust` is how to give it$"#
)]
fn told_untrusted(_: &mut World, _path: String) {
    red("she is told the project-only setting waits for her trust")
}

#[then("the project is listed as trusted by her, just now")]
fn trusted_by_her(_: &mut World) {
    red("the project is listed as trusted by her, just now")
}

#[then("her assistant ran as the program the project names")]
fn ran_projects_program(_: &mut World) {
    red("her assistant ran as the program the project names")
}

#[given("Alice has trusted the cloned project")]
fn has_trusted(_: &mut World) {
    red("Alice has trusted the cloned project")
}

#[given("a pull changes the program the project names")]
fn pull_changes(_: &mut World) {
    red("a pull changes the program the project names")
}

#[then("her assistant ran as the program the project names now")]
fn ran_projects_program_now(_: &mut World) {
    red("her assistant ran as the program the project names now")
}

#[given(expr = "an initialized ctxloom project whose project config says {string}")]
fn project_says(_: &mut World, _setting: String) {
    red("an initialized ctxloom project whose project config says a project-only setting")
}

#[given(
    expr = "an initialized ctxloom project whose own {string} bundle replaces the setup prompt"
)]
fn project_overrides_setup(_: &mut World, _bundle: String) {
    red("a project whose own ctxloom bundle replaces the setup prompt")
}

#[then("the setup used the built-in prompt, and said the project's own waits for trust")]
fn builtin_setup(_: &mut World) {
    red("the setup used the built-in prompt, and said the project's waits for trust")
}

#[then("the project is not trusted")]
fn not_trusted(_: &mut World) {
    red("the project is not trusted")
}

#[then("the project is listed as trusted by the user running ctxloom, with the time it was given")]
fn listed_trusted(_: &mut World) {
    red("the project is listed as trusted, with its actor and time")
}

#[given("the project's settings change after the preview and before the answer")]
fn changed_mid_trust(_: &mut World) {
    red("the project's settings change after the preview and before the answer")
}

#[when("its assistant sends a trust of its project with its own credential")]
fn agent_trusts(_: &mut World) {
    red("its assistant sends a trust of its project with its own credential")
}

#[then("the trust is refused as not the user's, and the project is not trusted")]
fn agent_refused(_: &mut World) {
    red("an agent's trust is refused")
}

#[given("an initialized ctxloom project that the user has trusted")]
fn project_trusted(_: &mut World) {
    red("an initialized ctxloom project that the user has trusted")
}

#[given(expr = "an initialized ctxloom project with a remote {string}")]
fn project_with_remote(_: &mut World, _remote: String) {
    red("an initialized ctxloom project with a remote")
}

#[then(expr = "the remote {string} is listed as trusted by the user running ctxloom")]
fn remote_trusted(_: &mut World, _remote: String) {
    red("the remote is listed as trusted")
}

// -------------------------------------------------- approvals (ruling 142)

#[given(
    regex = r#"^a child "([^"]+)" of "([^"]+)" whose engine has asked to run "([^"]+)" with "([^"]*)"$"#
)]
fn child_asked(_: &mut World, _child: String, _owner: String, _tool: String, _input: String) {
    red("a child whose engine has asked to run a tool")
}

#[then(regex = r"^the child's engine was told (.+)$")]
fn engine_told(_: &mut World, _what: String) {
    red("the child's engine was told the answer")
}

#[then(expr = "no ask is waiting under {string}")]
fn none_waiting(_: &mut World, _root: String) {
    red("no ask is waiting under the root")
}

#[given(expr = "a child {string} of {string} whose ask has been answered")]
fn ask_answered(_: &mut World, _child: String, _owner: String) {
    red("a child whose ask has been answered")
}

#[when("the ask's time runs out")]
fn ask_expires(_: &mut World) {
    red("the ask's time runs out")
}

#[when(regex = r#"^the assistant of "([^"]+)" answers the ask allow, with its own credential$"#)]
fn agent_answers(_: &mut World, _harp: String) {
    red("an assistant answers the ask with its own credential")
}

#[then("the answer is refused as not the approver's, and the ask still waits")]
fn not_approver(_: &mut World) {
    red("the answer is refused as not the approver's")
}

#[then(regex = r#"^the child's claude was launched with "([^"]+)" "([^"]+)"$"#)]
fn claude_flag(_: &mut World, _flag: String, _value: String) {
    red("the child's claude was launched with the permission prompt tool")
}

#[then("the child's session home names ctxloom's MCP server")]
fn home_names_mcp(_: &mut World) {
    red("the child's session home names ctxloom's MCP server")
}

#[given(
    expr = "an opencode child {string} of {string} that sends a permission request for {string}"
)]
fn opencode_asks(_: &mut World, _child: String, _owner: String, _tool: String) {
    red("an opencode child that sends a permission request")
}

#[then("the child's opencode was answered with its request's allow-once option")]
fn opencode_allowed(_: &mut World) {
    red("the child's opencode was answered allow-once")
}

#[given(regex = r#"^"([^"]+)" asks to run "([^"]+)" with "([^"]*)"$"#)]
fn journey_child_asks(_: &mut World, _child: String, _tool: String, _input: String) {
    red("the child asks to run a tool")
}

#[then(regex = r#"^she sees "([^"]+)" asking to run "([^"]+)" with "([^"]*)"$"#)]
fn sees_ask(_: &mut World, _child: String, _tool: String, _input: String) {
    red("she sees the child's ask")
}

#[then(regex = r#"^"([^"]+)" ran "([^"]*)", and nothing waits for her any more$"#)]
fn ran_after_allow(_: &mut World, _child: String, _input: String) {
    red("the child ran the allowed tool, and nothing waits")
}

// ------------------------------------- the architecture rulings T13-T30

#[then("the removal was previewed first, and applied with her answer and the preview's tag")]
fn previewed_then_applied(_: &mut World) {
    red("the removal was previewed, then applied with the answer and tag")
}

#[given(
    expr = "the bundle {string} changes after its removal is previewed and before Alice answers"
)]
fn bundle_changes_mid(_: &mut World, _b: String) {
    red("the bundle changes between preview and answer")
}

#[then("the apply was refused as stale, and the CLI previewed the removal again")]
fn refused_stale(_: &mut World) {
    red("the apply was refused as stale")
}

#[given(expr = "an initialized ctxloom project with a recorded session {string}")]
fn project_with_session(_: &mut World, _h: String) {
    red("a project with a recorded session")
}

#[given(expr = "a second initialized project with a recorded session {string}")]
fn second_project_session(_: &mut World, _h: String) {
    red("a second project with a recorded session")
}

#[then("both sessions are in the one store under ctxloom's home, and no project has a store of its own")]
fn one_store(_: &mut World) {
    red("both sessions are in the one store")
}

#[given("the project has a store of its own from before the one store")]
fn per_project_store(_: &mut World) {
    red("the project has a per-project store")
}

#[then("nothing was imported from it")]
fn nothing_imported(_: &mut World) {
    red("nothing was imported from the per-project store")
}

#[then(regex = r#"^its incarnation was recorded with the cause "([^"]+)"$"#)]
fn incarnation_cause(_: &mut World, _c: String) {
    red("its incarnation was recorded with the cause")
}

#[given(expr = "a child {string} of {string} that has been re-hosted {int} times in the last hour")]
fn rehosted_n(_: &mut World, _c: String, _o: String, _n: i64) {
    red("a child re-hosted n times in the last hour")
}

#[then(regex = r#"^"([^"]+)" was not re-hosted again, and ended runner-lost$"#)]
fn past_budget(_: &mut World, _c: String) {
    red("the child was not re-hosted past its budget")
}

#[then(regex = r#"^"([^"]+)" was told that its child ended$"#)]
fn owner_told(_: &mut World, _o: String) {
    red("the owner was told its child ended")
}

#[given(expr = "a child {string} whose session settings its agent has edited")]
fn settings_edited(_: &mut World, _c: String) {
    red("a child whose session settings its agent edited")
}

#[then("the re-hosted child's session settings are the ones the user's config makes, and its agent's edit is gone")]
fn settings_regenerated(_: &mut World) {
    red("the re-hosted child's settings were regenerated")
}

#[given(regex = r"^a (child|interactive) session on (claude-code|opencode)$")]
fn session_on_engine(_: &mut World, _k: String, _e: String) {
    red("a session on an engine")
}

#[then(expr = "its session home registers {string} as the MCP server {string}")]
fn registers_mcp(_: &mut World, _cmd: String, _n: String) {
    red("its session home registers ctxloom's MCP server")
}

#[when("its engine lists the tools of ctxloom's MCP server")]
fn lists_tools(_: &mut World) {
    red("its engine lists ctxloom's MCP tools")
}

#[then(regex = r"^the tools are (.+)$")]
fn tools_are(_: &mut World, _t: String) {
    red("the MCP tools are the spec's")
}

#[when(
    regex = r#"^the engine of "([^"]+)" calls "([^"]+)" for "([^"]+)" with (inform|wake|steer) "([^"]*)"$"#
)]
fn tool_send(_: &mut World, _o: String, _t: String, _c: String, _u: String, _x: String) {
    red("an engine calls a ctxloom tool with a message")
}

#[then("the tool answered with the child's session as the daemon's read model has it after the message")]
fn tool_answered_view(_: &mut World) {
    red("the tool answered with the read model's session")
}

#[then("the message was sent once, under the id the server chose")]
fn sent_once(_: &mut World) {
    red("the message was sent once, under the server's id")
}

#[when(regex = r#"^the engine of "([^"]+)" calls "([^"]+)" for "([^"]+)"$"#)]
fn tool_call(_: &mut World, _o: String, _t: String, _c: String) {
    red("an engine calls a ctxloom tool")
}

#[then("the tool answered once the new daemon had the close, and the close was applied once")]
fn tool_retried(_: &mut World) {
    red("the tool retried once on a new daemon")
}

#[when("its engine calls a tool whose change asks for an edited document, and answers the elicitation with new text")]
fn tool_edit(_: &mut World) {
    red("an engine answers an edit need with text")
}

#[then("the daemon applied the new text, and no editor was started")]
fn applied_text(_: &mut World) {
    red("the daemon applied the new text without an editor")
}

#[given(
    regex = r#"^a claude child "([^"]+)" of "([^"]+)" whose engine calls "approval_prompt" for "([^"]+)" with "([^"]*)"$"#
)]
fn approval_prompt_called(_: &mut World, _c: String, _o: String, _t: String, _i: String) {
    red("a claude child calls approval_prompt")
}

#[when(regex = r#"^(Alice runs "[^"]+"|nobody answers within the ask's time)$"#)]
fn approval_answered_by(_: &mut World, _w: String) {
    red("the ask is answered, or times out")
}

#[then(regex = r"^the tool answered the engine (.+)$")]
fn tool_answered_engine(_: &mut World, _a: String) {
    red("the approval tool answered the engine")
}

#[given(expr = "a child {string} whose engine runs in a container")]
fn child_in_container(_: &mut World, _c: String) {
    red("a child whose engine runs in a container")
}

#[then("the container holds no ctxloom binary of the host's, and its MCP server and hooks reach the daemon over TCP and TLS")]
fn container_path(_: &mut World) {
    red("the container reaches the daemon through its MCP server")
}

#[then("the change is in the tree whole, once, and every bundle still reads")]
fn change_whole(_: &mut World) {
    red("the change is in the tree whole, once")
}

#[given(expr = "another process creates the bundle {string} while its creation is staged")]
fn concurrent_create(_: &mut World, _b: String) {
    red("another process creates the bundle meanwhile")
}

#[then(
    regex = r#"^the other process's "([^"]+)" is untouched, and nothing staged is left behind$"#
)]
fn other_untouched(_: &mut World, _b: String) {
    red("the other process's bundle is untouched")
}

#[then(
    regex = r#"^the skill "([^"]+)" is the one directory "([^"]+)" of the bundle "([^"]+)", holding its "\.meta\.yaml"$"#
)]
fn skill_one_dir(_: &mut World, _s: String, _d: String, _b: String) {
    red("the skill is one directory with its metadata inside")
}

#[given(expr = "a fragment {string} in bundle {string} with a fresh distilled form")]
fn fresh_distilled(_: &mut World, _f: String, _b: String) {
    red("a fragment with a fresh distilled form")
}

#[then(regex = r#"^only "([^"]+)" of the bundle "([^"]+)" changed$"#)]
fn only_changed(_: &mut World, _p: String, _b: String) {
    red("only the source file changed")
}

#[then(regex = r#"^"([^"]+)" says its distilled form is stale$"#)]
fn says_stale(_: &mut World, _c: String) {
    red("fragment show says the distilled form is stale")
}

#[given(expr = "the source of {string} is edited by hand")]
#[when(expr = "the source of {string} is edited by hand")]
fn source_edited(_: &mut World, _f: String) {
    red("the source is edited by hand")
}

#[then(
    regex = r#"^the assembled context holds the original text of "([^"]+)", not its distilled form$"#
)]
fn serves_original(_: &mut World, _f: String) {
    red("assembly served the original")
}

#[given(
    expr = "a fragment {string} in bundle {string} whose distilled form records no source digest"
)]
fn no_digest(_: &mut World, _f: String, _b: String) {
    red("a distilled form with no digest")
}

#[then(expr = "the assembled context holds the distilled form of {string}")]
fn serves_distilled(_: &mut World, _f: String) {
    red("assembly served the distilled form")
}

#[then(
    regex = r#"^the session's directory, its lock inside it, and its output folder are under "([^"]+)"$"#
)]
fn renamed_whole(_: &mut World, _h: String) {
    red("the session's directory, lock and output moved")
}

#[then(
    regex = r#"^the directory, lock and output folder are under "([^"]+)", and its name is claimed once$"#
)]
fn rename_converged(_: &mut World, _h: String) {
    red("the rename converged")
}

#[then(regex = r#"^nothing lists "([^"]+)", its streams are purged and no trash is left$"#)]
fn remove_converged(_: &mut World, _h: String) {
    red("the remove converged")
}

#[then("the files it writes are as one uninterrupted run would leave them")]
fn foreign_converged(_: &mut World) {
    red("the foreign writer converged")
}

// ------------------------------------- the rulings after T30 (S42, S53)

#[then("nothing was applied, and the daemon answered the preview")]
fn preview_only(_: &mut World) {
    red("nothing was applied, and the daemon answered the preview")
}

#[then("the session's directory, lock and output folder were not moved")]
fn not_moved(_: &mut World) {
    red("a title change moves nothing")
}

#[then(
    regex = r#"^the output reports "([^"]+)" as the project's harp and the session's, "([^"]+)"$"#
)]
fn six_word_name(_: &mut World, _path: String, _shape: String) {
    red("a session's name is its project's harp and its own")
}

#[then(regex = r#"^its lock is "([^"]+)", inside its directory$"#)]
fn lock_inside(_: &mut World, _path: String) {
    red("the session's lock is inside its directory")
}

#[then(regex = r#"^the bundle "([^"]+)" is the imported one, whole, and no trash is left$"#)]
fn import_converged(_: &mut World, _bundle: String) {
    red("bundle import --force converged")
}
