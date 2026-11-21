//! The daemon's policy runner, planned (D28 S-d, S-e): what the holder of the
//! policy lock does next for one project, from the project's [`Board`] and
//! [`Pending`] messages (owed state, never an event's arrival) and what this
//! daemon hosts now. Pure: the daemon sends what [`plan`] answers, and marks a
//! step's key done once it took effect, so a step is not tried again in this
//! process; a restart plans from 0 and every keyed command it sends again is
//! DUPLICATE.
//!
//! The steps, in the order planned:
//!
//! - launch (K2): a child an owner holds whose session its store does not
//!   hold yet is claimed and minted ([`crate::launch`]); its launch's prompt
//!   then goes to it as a wake, `launch:<child>`;
//! - cascade (D29) and child ended: a closed owner closes each child it
//!   holds; a closed child is ended on its owner;
//! - notice: each turn a child's run ended is told to its owner, a message
//!   `notice:<child>:<incarnation>:<turn>` (the owner's engine's urgency: an
//!   inform to a primary, which has no wake yet; a wake to a daemon child);
//! - re-host (T19, s4-Q9): a child whose last incarnation was lost (ended
//!   `orphaned`: its host, a killed daemon, was lost) is owed its next
//!   incarnation, `rehost:<child>:<n+1>`, the daemon counting its recent
//!   re-hosts as the evidence; one a draining daemon handed over (ended
//!   `drained`, s4-Q4) likewise, for the cause `drain`, never counted;
//! - start (D2): a child with an incarnation owed and no run enqueued for it
//!   is started, a long-lived child the daemon hosts, resuming its engine's
//!   own conversation from its second incarnation on;
//! - delivery (S-a): a session the daemon hosts with messages pending is
//!   given its next turn ([`crate::deliver`]);
//! - feed: each turn queued on a hosted run is handed to its child once;
//! - clear (M1): each clear asked of a hosted child is handed to it once;
//! - end (D13, D14): a hosted child whose session closed is ended;
//! - stop (S19, OD-57): a hosted child whose run was asked to stop has its
//!   run stopped, the child left open.
//!
//! Only this project's sessions are planned, though one board holds every
//! project's (the one store, T14); a step on one of them reaches across to
//! another project where its owner or child is (step 8 U2b): an owner's
//! plan launches and closes its child there, a child's plan tells and ends
//! it on its owner there. Each step is planned by one project alone.

use std::collections::BTreeSet;

use angzarr_embedded::v1::CommandBook;
use daemon_board::{run_text, Board, Message};
use pending_messages::Pending;
use runs::TurnUrgency;
use sessions::{SendMessage, SessionRef, SessionState, Urgency};

use crate::{
    cascade, child_ended, deliver, launch, launch_failed, session_cover, Keyed, KeyedFact, Mint,
    RunGate,
};

/// The engines with a child adapter (OD-58): a child the daemon hosts on one
/// takes every urgency over its engine's stream (D15). The composition
/// root's `hosted_engines` is held to this list by its test, so an engine
/// given a child adapter is granted here too.
pub const CHILD_ENGINES: &[&str] = &["claude-code", "opencode"];

/// The engine a child runs on when neither its launch nor its daemon names
/// one.
pub const CHILD_ENGINE: &str = "claude-code";

/// A command (or a fact) to send to `project_id`'s store, under `key`.
#[derive(Clone, Debug, PartialEq)]
pub struct Sent {
    pub key: String,
    pub project_id: String,
    pub book: CommandBook,
}

impl<C: angzarr_embedded::markup::DomainMessage> From<Keyed<C>> for Sent {
    fn from(keyed: Keyed<C>) -> Sent {
        Sent {
            book: keyed.book(),
            key: keyed.key,
            project_id: keyed.project_id,
        }
    }
}

impl<F: angzarr_embedded::markup::DomainMessage> From<KeyedFact<F>> for Sent {
    fn from(keyed: KeyedFact<F>) -> Sent {
        Sent {
            book: keyed.book(),
            key: keyed.external_id,
            project_id: keyed.project_id,
        }
    }
}

/// One message of a turn handed to a child.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Fed {
    pub message_id: String,
    pub urgency: Urgency,
    pub text: String,
}

/// What a run of a session is, for the daemon's steps on it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RunAt {
    /// The session's stable id.
    pub session: String,
    pub incarnation: u32,
}

impl RunAt {
    /// The run's root, as text.
    pub fn run(&self) -> String {
        run_text(&self.session, self.incarnation)
    }
}

/// One step of the daemon's plan, each under its key.
#[derive(Clone, Debug, PartialEq)]
pub enum Step {
    /// Send a keyed command or fact.
    Send(Sent),
    /// Claim and mint a child; either refused, record `failed` instead.
    Launch {
        key: String,
        mint: Box<Mint>,
        failed: Sent,
    },
    /// Host a run for a child: enqueue it and start its engine, resuming
    /// the engine's own session `resume` (its bound native id) when there
    /// is one to resume (an incarnation after the first).
    Start {
        key: String,
        at: RunAt,
        harp: String,
        engine: String,
        project_dir: String,
        resume: Option<String>,
    },
    /// Owe a lost child its next incarnation, `at` (T19): the daemon counts
    /// its recent re-hosts and sends `RehostSession`; refused for the
    /// budget, `owner` is told the child ended.
    Rehost {
        key: String,
        at: RunAt,
        harp: String,
        owner: SessionRef,
        /// Why it is owed: `rehost` (its host was lost, counted against
        /// its budget) or `drain` (a draining daemon handed it over, s4-Q5,
        /// never counted).
        cause: String,
    },
    /// Hand the turn `turn` (its messages, in order) to the hosted child.
    Feed {
        key: String,
        at: RunAt,
        turn: u64,
        urgency: TurnUrgency,
        messages: Vec<Fed>,
    },
    /// Hand a clear to the hosted child.
    Clear { key: String, at: RunAt },
    /// End the hosted child.
    End { key: String, at: RunAt },
    /// Stop the hosted child's run, which its stream asked (S19's `session
    /// stop`, OD-57): its engine ends, the child stays open.
    Stop { key: String, at: RunAt },
}

impl Step {
    /// Whether it delivers a session's pending messages to its run as its
    /// next turn ([`crate::deliver`], keyed `send:<message_id>`): what a
    /// draining daemon no longer does (s4-Q4), the messages left pending
    /// for the daemon that re-hosts the child.
    pub fn delivers(&self) -> bool {
        matches!(self, Step::Send(sent) if sent.key.starts_with(crate::DELIVERY_KEY))
    }

    /// The step's key: what the daemon marks done.
    pub fn key(&self) -> &str {
        match self {
            Step::Send(sent) => &sent.key,
            Step::Launch { key, .. }
            | Step::Start { key, .. }
            | Step::Rehost { key, .. }
            | Step::Feed { key, .. }
            | Step::Clear { key, .. }
            | Step::End { key, .. }
            | Step::Stop { key, .. } => key,
        }
    }
}

/// What a plan is made from.
pub struct Scene<'a> {
    /// The project planned for: whose store `board` and `pending` are of.
    pub project_id: &'a str,
    pub board: &'a Board,
    pub pending: &'a Pending,
    /// The runs (their roots' text) this daemon hosts now.
    pub hosted: &'a BTreeSet<String>,
    /// The keys of the steps already taken in this process.
    pub done: &'a BTreeSet<String>,
    /// The sessions (stable ids) whose ctxloom MCP server watches them now
    /// (`WatchSession`): a primary among them can be woken.
    pub wakeable: &'a BTreeSet<String>,
}

impl<'a> Scene<'a> {
    /// The board's sessions of this scene's project, by stable id: one board
    /// holds every project's (T14), and a plan is its project's alone.
    fn sessions(&self) -> impl Iterator<Item = (&'a str, &'a SessionState)> + '_ {
        self.board
            .sessions()
            .filter(|(_, session)| session.project_id == self.project_id)
    }
}

/// The urgencies `session`'s engine takes: a child the daemon hosts on an
/// engine with a child adapter ([`CHILD_ENGINES`]) takes all three (its
/// engine's stream, D15; OD-58); a primary takes an inform, and a wake only
/// while its ctxloom MCP server watches it (`wakeable`: the terminal wake,
/// owner ruling 2026-11-18), never a steer; any other session only an
/// inform.
pub fn supported(session: &SessionState, wakeable: bool) -> Vec<Urgency> {
    let hosted = CHILD_ENGINES.contains(&session.engine.as_str());
    match (session.owner.is_some(), hosted) {
        (true, true) => vec![Urgency::Inform, Urgency::Wake, Urgency::Steer],
        (false, _) if wakeable => vec![Urgency::Inform, Urgency::Wake],
        _ => vec![Urgency::Inform],
    }
}

/// The reference to `session` of this project.
fn here(project_id: &str, session: &str) -> SessionRef {
    SessionRef {
        project_id: project_id.to_string(),
        session: session.to_string(),
    }
}

/// The steps for `scene`, none already done.
pub fn plan(scene: &Scene<'_>) -> Vec<Step> {
    let mut steps = Vec::new();
    launches(scene, &mut steps);
    closes(scene, &mut steps);
    notices(scene, &mut steps);
    rehosts(scene, &mut steps);
    runs(scene, &mut steps);
    steps.retain(|step| !scene.done.contains(step.key()));
    steps
}

/// The launches owed, and each launched child's first task: its owner's
/// plan's, a child in another project's too (step 8 Q1), minted in its own
/// project (one store holds every project's, T14).
fn launches(scene: &Scene<'_>, steps: &mut Vec<Step>) {
    for (owner_id, owner) in scene.sessions() {
        let owner_ref = here(scene.project_id, owner_id);
        for launched in scene.board.launched(owner_id) {
            let Some(child) = launched.child.as_ref() else {
                continue;
            };
            let held = owner
                .children
                .iter()
                .any(|c| c.session.as_ref() == Some(child));
            match scene.board.session(&child.session) {
                None if held => {
                    let (Some(mint), Some(failed)) = (
                        launch(&owner_ref, launched),
                        launch_failed(&owner_ref, child, "sessions-launch-refused"),
                    ) else {
                        continue;
                    };
                    steps.push(Step::Launch {
                        key: mint.mint.key.clone(),
                        mint: Box::new(mint),
                        failed: failed.into(),
                    });
                }
                Some(state) if !state.closed && !launched.prompt.trim().is_empty() => {
                    let wakeable = scene.wakeable.contains(&child.session);
                    if let Some(sent) = prompt(child, state, wakeable, &launched.prompt) {
                        steps.push(Step::Send(sent));
                    }
                }
                _ => {}
            }
        }
    }
}

/// A launched child's first task, as a wake keyed `launch:<child>`.
fn prompt(child: &SessionRef, state: &SessionState, wakeable: bool, text: &str) -> Option<Sent> {
    let key = format!("launch:{}", child.session);
    Some(
        Keyed {
            project_id: child.project_id.clone(),
            cover: session_cover(&child.session)?,
            key: key.clone(),
            command: SendMessage {
                message_id: key,
                urgency: Urgency::Wake,
                text: text.to_string(),
                requester: None,
                supported: supported(state, wakeable),
            },
        }
        .into(),
    )
}

/// The cascade of each closed owner, and the end of each closed child on its
/// owner.
fn closes(scene: &Scene<'_>, steps: &mut Vec<Step>) {
    for (id, session) in scene.sessions().filter(|(_, s)| s.closed) {
        let me = here(scene.project_id, id);
        // Every child it holds, one in another project too (step 8 U2b):
        // its close is sent to that project's store (one board, T14).
        steps.extend(
            cascade(&me, &session.children)
                .into_iter()
                .map(|k| Step::Send(k.into())),
        );
        let Some(owner) = session.owner.as_ref() else {
            continue;
        };
        let holds = scene
            .board
            .session(&owner.session)
            .is_some_and(|o| o.children.iter().any(|c| c.session.as_ref() == Some(&me)));
        let closed = sessions::SessionClosed {
            cause: session.closed_cause.clone(),
        };
        // Its owner, in this project or another (step 8 U2b).
        if holds {
            steps.extend(child_ended(&me, owner, &closed).map(|k| Step::Send(k.into())));
        }
    }
}

/// Each turn a child's run ended, told to its open owner, in this project
/// or another (step 8 U2b): the child's project's plan tells it.
fn notices(scene: &Scene<'_>, steps: &mut Vec<Step>) {
    for (id, child) in scene.sessions() {
        let Some(owner_ref) = child.owner.as_ref() else {
            continue;
        };
        let Some(owner) = scene
            .board
            .session(&owner_ref.session)
            .filter(|o| !o.closed)
        else {
            continue;
        };
        let Some(cover) = session_cover(&owner_ref.session) else {
            continue;
        };
        let owner_takes = supported(owner, scene.wakeable.contains(&owner_ref.session));
        let urgency = match owner_takes.contains(&Urgency::Wake) {
            true => Urgency::Wake,
            false => Urgency::Inform,
        };
        // Each incarnation its run ended, but one a re-host continues.
        let me = here(scene.project_id, id);
        for ended in scene.board.ends(id) {
            if [ORPHANED, ENDED_DRAINED].contains(&ended.cause.as_str()) {
                continue;
            }
            if let Some(told) = crate::notice(&me, &child.harp, owner_ref, ended, &owner_takes) {
                steps.push(Step::Send(told.into()));
            }
        }
        for incarnation in 1..=child.incarnation {
            let Some(run) = scene.board.run(id, incarnation) else {
                continue;
            };
            for ended in &run.ended_turns {
                let key = format!("{}{id}:{incarnation}:{}", crate::NOTICE_PREFIX, ended.turn);
                let how = if ended.failed { "failed" } else { "finished" };
                steps.push(Step::Send(
                    Keyed {
                        project_id: owner_ref.project_id.clone(),
                        cover: cover.clone(),
                        key: key.clone(),
                        command: SendMessage {
                            message_id: key,
                            urgency,
                            text: format!(
                                "child {} {how} its turn for {}",
                                child.harp,
                                ended.message_ids.join(", ")
                            ),
                            requester: None,
                            supported: owner_takes.clone(),
                        },
                    }
                    .into(),
                ));
            }
        }
    }
}

/// Why a run's incarnation ends when its owner (its host) was lost.
const ORPHANED: &str = "orphaned";

/// Why a hosted child's incarnation ends when a draining daemon ended it to
/// hand it over to the next daemon (OD-54).
pub const ENDED_DRAINED: &str = "drained";

/// Each child of this project whose last incarnation was lost (cause
/// `rehost`) or handed over by a draining daemon (cause `drain`, s4-Q5), and
/// is neither closed nor owed another: its re-host. A child with
/// no native session has no conversation to resume, and is left ended.
fn rehosts(scene: &Scene<'_>, steps: &mut Vec<Step>) {
    for (id, session) in scene.sessions() {
        let Some(owner) = session.owner.clone() else {
            continue;
        };
        let cause = match scene.board.last_end(id) {
            Some((n, ORPHANED)) if n == session.incarnation => sessions::REHOSTED,
            Some((n, ENDED_DRAINED)) if n == session.incarnation => sessions::DRAINED,
            _ => continue,
        };
        let open = session.open_incarnation.is_some() || session.closed;
        if open || session.native_id.is_empty() {
            continue;
        }
        // `session stop` (owner 2026-11-20): a run asked to stop was meant to
        // end; losing its host after the request does not bring it back.
        let stopped = scene
            .board
            .run(id, session.incarnation)
            .is_some_and(|run| run.state.stop_requested);
        if stopped {
            continue;
        }
        let at = RunAt {
            session: id.to_string(),
            incarnation: session.incarnation + 1,
        };
        steps.push(Step::Rehost {
            key: format!("rehost:{id}:{}", at.incarnation),
            at,
            harp: session.harp.clone(),
            owner,
            cause: cause.to_string(),
        });
    }
}

/// The steps on the children's runs: start, deliver, feed, clear, end.
fn runs(scene: &Scene<'_>, steps: &mut Vec<Step>) {
    for (id, session) in scene.sessions() {
        if session.owner.is_none() {
            continue;
        }
        let Some(incarnation) = session.open_incarnation else {
            continue;
        };
        let at = RunAt {
            session: id.to_string(),
            incarnation,
        };
        let run = scene.board.run(id, incarnation);
        let hosted = scene.hosted.contains(&at.run());
        if !hosted {
            let startable = run.is_none() && !session.closed;
            if startable {
                steps.push(Step::Start {
                    key: format!("start:{}", at.run()),
                    at: at.clone(),
                    harp: session.harp.clone(),
                    engine: session.engine.clone(),
                    project_dir: session.project_dir.clone(),
                    // Its own conversation, from its second incarnation on.
                    resume: (incarnation > 1 && !session.native_id.is_empty())
                        .then(|| session.native_id.clone()),
                });
            }
            continue;
        }
        let Some(run) = run.filter(|run| run.live()) else {
            continue;
        };
        if session.closed {
            steps.push(Step::End {
                key: format!("end:{}", at.run()),
                at,
            });
            continue;
        }
        // Its run asked to stop (S19): stopped, the child left open.
        if run.state.stop_requested {
            steps.push(Step::Stop {
                key: format!("stop:{}", at.run()),
                at,
            });
            continue;
        }
        let gate = RunGate {
            session: here(scene.project_id, id),
            incarnation,
            live: true,
            in_flight: !run.state.turns.is_empty(),
        };
        if let Some(keyed) = deliver(&gate, &scene.pending.pending(id)) {
            steps.push(Step::Send(keyed.into()));
        }
        for turn in &run.state.turns {
            let messages = turn
                .message_ids
                .iter()
                .map(|message_id| {
                    let Message { urgency, text } = scene
                        .board
                        .message(id, message_id)
                        .cloned()
                        .unwrap_or(Message {
                            urgency: Urgency::Wake,
                            text: String::new(),
                        });
                    Fed {
                        message_id: message_id.clone(),
                        urgency,
                        text,
                    }
                })
                .collect();
            steps.push(Step::Feed {
                key: format!("feed:{}:{}", at.run(), turn.turn),
                at: at.clone(),
                turn: turn.turn,
                urgency: turn.urgency,
                messages,
            });
        }
        for clear in scene.board.clears(id) {
            steps.push(Step::Clear {
                key: format!("clear-child:{id}:{clear}"),
                at: at.clone(),
            });
        }
    }
}

#[cfg(test)]
#[path = "plan.test.rs"]
mod tests;
