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
//! - start (D2): a child with an incarnation owed and no run enqueued for it
//!   is started, a long-lived child the daemon hosts;
//! - delivery (S-a): a session the daemon hosts with messages pending is
//!   given its next turn ([`crate::deliver`]);
//! - feed: each turn queued on a hosted run is handed to its child once;
//! - clear (M1): each clear asked of a hosted child is handed to it once;
//! - end (D13, D14): a hosted child whose session closed is ended.
//!
//! Only this project's sessions are acted on: a child in another project, or
//! an owner there, is that project's plan's (the cross-store owner link,
//! D19, is a deferral of this slice).

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

/// The engine children run on in this slice: the one with a child engine.
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
    /// Host a run for a child: enqueue it and start its engine.
    Start {
        key: String,
        at: RunAt,
        harp: String,
        engine: String,
        project_dir: String,
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
}

impl Step {
    /// The step's key: what the daemon marks done.
    pub fn key(&self) -> &str {
        match self {
            Step::Send(sent) => &sent.key,
            Step::Launch { key, .. }
            | Step::Start { key, .. }
            | Step::Feed { key, .. }
            | Step::Clear { key, .. }
            | Step::End { key, .. } => key,
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
}

/// The urgencies `session`'s engine takes: a child the daemon hosts takes
/// all three (its engine's stream, D15); a primary takes only an inform,
/// since nothing wakes a terminal session yet (D7 is not built).
pub fn supported(session: &SessionState) -> Vec<Urgency> {
    match session.owner.is_some() && session.engine == CHILD_ENGINE {
        true => vec![Urgency::Inform, Urgency::Wake, Urgency::Steer],
        false => vec![Urgency::Inform],
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
    runs(scene, &mut steps);
    steps.retain(|step| !scene.done.contains(step.key()));
    steps
}

/// The launches owed, and each launched child's first task.
fn launches(scene: &Scene<'_>, steps: &mut Vec<Step>) {
    let ours = |child: &SessionRef| child.project_id == scene.project_id;
    for (owner_id, owner) in scene.board.sessions() {
        let owner_ref = here(scene.project_id, owner_id);
        for launched in scene.board.launched(owner_id) {
            let Some(child) = launched.child.as_ref().filter(|c| ours(c)) else {
                continue;
            };
            let held = owner
                .children
                .iter()
                .any(|c| c.session.as_ref() == Some(child));
            match scene.board.session(&child.session) {
                None if held => {
                    let (Some(mint), Some(failed)) = (
                        launch(&owner_ref, launched, &owner.output_dir),
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
                    if let Some(sent) = prompt(child, state, &launched.prompt) {
                        steps.push(Step::Send(sent));
                    }
                }
                _ => {}
            }
        }
    }
}

/// A launched child's first task, as a wake keyed `launch:<child>`.
fn prompt(child: &SessionRef, state: &SessionState, text: &str) -> Option<Sent> {
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
                supported: supported(state),
            },
        }
        .into(),
    )
}

/// The cascade of each closed owner, and the end of each closed child on its
/// owner.
fn closes(scene: &Scene<'_>, steps: &mut Vec<Step>) {
    for (id, session) in scene.board.sessions().filter(|(_, s)| s.closed) {
        let me = here(scene.project_id, id);
        let children: Vec<_> = session
            .children
            .iter()
            .filter(|c| {
                c.session
                    .as_ref()
                    .is_some_and(|c| c.project_id == scene.project_id)
            })
            .cloned()
            .collect();
        steps.extend(
            cascade(&me, &children)
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
        if holds && owner.project_id == scene.project_id {
            steps.extend(child_ended(&me, owner, &closed).map(|k| Step::Send(k.into())));
        }
    }
}

/// Each turn a child's run ended, told to its open owner.
fn notices(scene: &Scene<'_>, steps: &mut Vec<Step>) {
    for (id, child) in scene.board.sessions() {
        let Some(owner_ref) = child
            .owner
            .as_ref()
            .filter(|o| o.project_id == scene.project_id)
        else {
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
        let urgency = match supported(owner).contains(&Urgency::Wake) {
            true => Urgency::Wake,
            false => Urgency::Inform,
        };
        for incarnation in 1..=child.incarnation {
            let Some(run) = scene.board.run(id, incarnation) else {
                continue;
            };
            for ended in &run.ended_turns {
                let key = format!("notice:{id}:{incarnation}:{}", ended.turn);
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
                            supported: supported(owner),
                        },
                    }
                    .into(),
                ));
            }
        }
    }
}

/// The steps on the children's runs: start, deliver, feed, clear, end.
fn runs(scene: &Scene<'_>, steps: &mut Vec<Step>) {
    for (id, session) in scene.board.sessions() {
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
            let startable = run.is_none() && !session.closed && !session.stopped;
            if startable {
                steps.push(Step::Start {
                    key: format!("start:{}", at.run()),
                    at: at.clone(),
                    harp: session.harp.clone(),
                    engine: session.engine.clone(),
                    project_dir: session.project_dir.clone(),
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
