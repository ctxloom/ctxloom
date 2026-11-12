//! The sessions domain. A session is one aggregate: its stream's root is a
//! stable id minted at creation, and the harp is only its name (plan 3.1,
//! D5 Q1). The name is kept unique by a claim stream of its own, domain
//! `session-names`, one per (project, harp text).
//!
//! The domain types, their wire conversions, their `DomainMessage` impls and
//! the [`SessionCommand`] and [`SessionEvent`] payloads are generated from
//! `proto/ctxloom/sessions/v1` (angzarr-build's markup layer);
//! [`SessionState`] and the rules are this crate's, and [`SessionAggregate`]
//! and [`NameClaimAggregate`] are the aggregates angzarr-embedded runs, on
//! those payloads.
//!
//! The rules are Go's, from `internal/core/sessions` (release/0.7): its
//! `BindSession` (a repeat of the bound id records nothing; a different id
//! with a transcript is a rotation that keeps the displaced binding; one
//! without never displaces), `AppendRotations`, `SetKeep`, `Rename` and
//! `harp.Validate`.

// Generated code: the event enum's conversions grow a line per event.
#[allow(dead_code, clippy::too_many_lines)]
mod generated {
    include!(concat!(env!("OUT_DIR"), "/ctxloom.sessions.v1.domain.rs"));
}
mod aggregate;
mod daemon;
mod harp;
mod lifeline;
mod names;
mod projects;
mod refusal;
mod rejection;
mod state;

use angzarr_embedded::Timestamp;

/// The session aggregate's commands, one variant per command type.
pub type SessionCommand = SessionStateCommand;
/// The session aggregate's events, one variant per event type.
pub type SessionEvent = SessionStateEvent;
/// The name-claim aggregate's commands, one variant per command type.
pub type NameClaimCommand = NameClaimStateCommand;
/// The name-claim aggregate's events, one variant per event type.
pub type NameClaimEvent = NameClaimStateEvent;
/// The project aggregate's commands (S54).
pub type ProjectCommand = ProjectStateCommand;
/// The project aggregate's events (S54).
pub type ProjectEvent = ProjectStateEvent;
/// The project harp claim's commands (S54).
pub type ProjectNameCommand = ProjectNameStateCommand;
/// The project harp claim's events (S54).
pub type ProjectNameEvent = ProjectNameStateEvent;

pub use aggregate::SessionAggregate;
pub use daemon::{command_id, keyed_book, urgency_word};
pub use generated::*;
pub use harp::{validate, validate_rename, MAX_NAME_LEN};
pub use names::{claim_root, project_namespace, NameClaimAggregate};
pub use projects::{project_name_root, project_root, ProjectAggregate, ProjectNameAggregate};
pub use refusal::NotOurs;
#[cfg(any(test, feature = "test-support"))]
pub use rejection::diagnostic_samples;
pub use rejection::{SessionRejection, EN_US};
pub use state::{
    Child, NameClaimState, ProjectNameState, ProjectState, SessionState, NAMES_DOMAIN,
    PROJECTS_DOMAIN, PROJECT_NAMES_DOMAIN, SESSION_DOMAIN,
};

/// Fold `event`, recorded at `recorded_at` (its page's
/// `EventMeta.recorded_at`), into `state`: what the aggregate's appliers do,
/// one place (public, so a reader of the stream folds as the aggregate does). A mint's start and a rotation's are
/// `recorded_at`, or the instant the event carries (an imported or adopted
/// one), read in preference. Pure: the same state, event and instant always
/// give the same result.
pub fn apply(state: &mut SessionState, event: &SessionEvent, recorded_at: Timestamp) {
    match event {
        SessionEvent::SessionMinted(minted) => {
            state.harp = minted.harp.clone();
            state.project_id = minted.project_id.clone();
            state.project_dir = minted.project_dir.clone();
            state.output_dir = minted.output_dir.clone();
            state.engine = minted.engine.clone();
            state.origin = Some(minted.origin);
            state.started_at = minted.started_at.unwrap_or(recorded_at);
            state.depth = minted.depth;
            state.owner = minted.owner.clone();
        }
        SessionEvent::EngineSessionBound(bound) => {
            if let Some(displaced) = &bound.displaced {
                // A displaced binding joins the lineage once: a duplicate
                // entry would convert the same segment twice on a rebuild.
                if !state
                    .rotations
                    .iter()
                    .any(|r| r.native_id == displaced.native_id)
                {
                    state.rotations.push(stamped(displaced, recorded_at));
                }
            }
            state.native_id = bound.native_id.clone();
            state.transcript_path = bound.transcript_path.clone();
            state.engine_version = bound.engine_version.clone();
        }
        SessionEvent::KeptChanged(changed) => state.kept = changed.kept,
        SessionEvent::RenamedTo(renamed) => {
            state.harp = renamed.harp.clone();
            state.output_dir = renamed.output_dir.clone();
        }
        SessionEvent::SessionAdopted(adopted) => {
            for rotation in &adopted.rotations {
                if !state
                    .rotations
                    .iter()
                    .any(|r| r.native_id == rotation.native_id)
                {
                    state.rotations.push(stamped(rotation, recorded_at));
                }
            }
            // Oldest first, by when each rotated: an adopted orphan older than
            // everything on record lands before it (Go: AppendRotations).
            state.rotations.sort_by_key(|r| {
                let at = r.rotated_at.unwrap_or_default();
                (at.seconds, at.nanos)
            });
        }
        SessionEvent::EndRecorded(ended) => state.ended_at = Some(ended.at),
        // The transcript's observations are the read side's: no state of the session.
        SessionEvent::PromptSubmitted(_)
        | SessionEvent::AssistantTextShown(_)
        | SessionEvent::ToolCalled(_)
        | SessionEvent::TurnEnded(_)
        | SessionEvent::SessionEnded(_)
        | SessionEvent::Compacted(_) => {}
        // A run's end is when the session's last run ended (the proto's
        // RecordEnd: IncarnationEnded supersedes it), at its page's instant.
        SessionEvent::IncarnationEnded(_) => {
            state.ended_at = Some(recorded_at);
            lifeline::apply(state, event);
        }
        SessionEvent::IncarnationOwed(_) | SessionEvent::StopOrdered(_) => {
            lifeline::apply(state, event)
        }
        // A message and a clear are the read side's (the pending messages,
        // the delivery and clear policies): no state of the session.
        SessionEvent::MessageSent(_) | SessionEvent::ClearRequested(_) => {}
        SessionEvent::SessionClosed(_)
        | SessionEvent::ChildLaunched(_)
        | SessionEvent::ChildLaunchFailed(_)
        | SessionEvent::ChildEnded(_)
        | SessionEvent::ChildForgotten(_) => daemon::apply(state, event),
    }
}

/// `rotation` with the instant it rotated: its own, else `recorded_at`.
fn stamped(rotation: &Rotation, recorded_at: Timestamp) -> Rotation {
    Rotation {
        rotated_at: Some(rotation.rotated_at.unwrap_or(recorded_at)),
        ..rotation.clone()
    }
}

/// A fact's answer: the event it records, or none when it changed nothing.
pub(crate) fn record<E: angzarr_embedded::markup::DomainMessage>(
    event: Option<E>,
) -> Result<angzarr_embedded::markup::FactEmit<SessionAggregate>, angzarr_embedded::markup::Bug> {
    Ok(angzarr_embedded::markup::FactEmit::of(event.map(|event| {
        angzarr_embedded::markup::pack_message(&event)
    })))
}

/// What a mint records: refused when the stream is `taken` (it holds a
/// session: its id is never reused) or the harp cannot be a
/// name.
fn session_minted(
    held: &SessionState,
    taken: bool,
    mint: &MintSession,
) -> Result<SessionMinted, SessionRejection> {
    if taken {
        return Err(SessionRejection::SessionExists {
            harp: held.harp.clone(),
        });
    }
    validate(&mint.harp)?;
    Ok(SessionMinted {
        harp: mint.harp.clone(),
        project_id: mint.project_id.clone(),
        project_dir: mint.project_dir.clone(),
        output_dir: mint.output_dir.clone(),
        engine: mint.engine.clone(),
        origin: mint.origin,
        started_at: mint.started_at,
        depth: mint.depth,
        owner: mint.owner.clone(),
    })
}

/// What a bind records: `None` when it changes nothing (Go's `BindSession`).
///
/// A bind naming nothing records nothing. On a session already bound, the
/// bound id again records nothing, an id that comes with no transcript never
/// displaces the binding, and one that comes with a transcript is a
/// rotation: the displaced binding is carried in the event. On a session not
/// yet bound, the id and the transcript named are set.
fn engine_bound(
    held: &SessionState,
    bind: &BindEngineSession,
) -> Result<Option<EngineSessionBound>, SessionRejection> {
    let held = live(held)?;
    let (id, path) = (bind.native_id.as_str(), bind.transcript_path.as_str());
    if id.is_empty() && path.is_empty() {
        return Ok(None);
    }
    let engine_version = match bind.engine_version.as_deref() {
        Some(version) if !version.is_empty() => version.to_string(),
        _ => held.engine_version.clone(),
    };
    if held.native_id.is_empty() {
        let bound = EngineSessionBound {
            native_id: if id.is_empty() {
                held.native_id.clone()
            } else {
                id.to_string()
            },
            transcript_path: if path.is_empty() {
                held.transcript_path.clone()
            } else {
                path.to_string()
            },
            engine_version,
            displaced: None,
        };
        let unchanged = bound.native_id == held.native_id
            && bound.transcript_path == held.transcript_path
            && bound.engine_version == held.engine_version;
        return Ok((!unchanged).then_some(bound));
    }
    if id == held.native_id || id.is_empty() || path.is_empty() {
        return Ok(None);
    }
    Ok(Some(EngineSessionBound {
        native_id: id.to_string(),
        transcript_path: path.to_string(),
        engine_version,
        displaced: Some(Rotation {
            native_id: held.native_id.clone(),
            transcript_path: held.transcript_path.clone(),
            rotated_at: None,
        }),
    }))
}

/// What marking or unmarking keep records: `None` when it changes nothing.
fn kept_changed(
    held: &SessionState,
    set: &SetKept,
) -> Result<Option<KeptChanged>, SessionRejection> {
    let held = live(held)?;
    Ok((held.kept != set.kept).then_some(KeptChanged { kept: set.kept }))
}

/// What a rename records: refused with a name a human may not choose;
/// `None` when the name is the one held.
fn renamed(
    held: &SessionState,
    rename: &RenameSession,
) -> Result<Option<RenamedTo>, SessionRejection> {
    let held = live(held)?;
    // Owner ruling 91 (per 73e): what the session's lock said, judged here. A
    // held lock is a live session: its engine works in the directory a rename
    // moves. A free or unproven one does not refuse.
    if rename.lock == crate::LockEvidence::Held {
        return Err(SessionRejection::SessionLive {
            harp: held.harp.clone(),
        });
    }
    validate_rename(&rename.harp)?;
    if rename.harp == held.harp {
        return Ok(None);
    }
    Ok(Some(RenamedTo {
        harp: rename.harp.clone(),
        output_dir: rename.output_dir.clone(),
    }))
}

/// What an adopt records: the rotations new to the lineage (not the current
/// binding, not already in it, once each); `None` when none is.
fn adopted(
    held: &SessionState,
    adopt: &AdoptRotations,
) -> Result<Option<SessionAdopted>, SessionRejection> {
    let held = live(held)?;
    let mut known: Vec<&str> = held
        .rotations
        .iter()
        .map(|r| r.native_id.as_str())
        .collect();
    if !held.native_id.is_empty() {
        known.push(&held.native_id);
    }
    let mut new: Vec<Rotation> = Vec::new();
    for rotation in &adopt.rotations {
        let id = rotation.native_id.as_str();
        if known.contains(&id) || new.iter().any(|r| r.native_id == id) {
            continue;
        }
        new.push(rotation.clone());
    }
    Ok((!new.is_empty()).then_some(SessionAdopted { rotations: new }))
}

/// What an end fact records.
fn end_recorded(held: &SessionState, end: &RecordEnd) -> Result<EndRecorded, SessionRejection> {
    live(held)?;
    Ok(EndRecorded { at: end.at })
}

/// The session, if the stream holds one.
pub(crate) fn live(state: &SessionState) -> Result<&SessionState, SessionRejection> {
    if state.harp.is_empty() {
        return Err(SessionRejection::NoSession);
    }
    Ok(state)
}

/// The book that records `bind` on the session stream `cover` names: a Fact
/// body with `external_id` `bind:<session>:<native_id>` (plan 4.3), the session
/// being the cover's root, so a repeat is answered DUPLICATE. A bind that names
/// no native id is keyed by its transcript: `bind:<session>:path:<path>`.
/// (Owner ruling 2026-11-04, S5 review item 8.)
pub fn bind_book(
    cover: angzarr_embedded::v1::Cover,
    bind: &BindEngineSession,
) -> angzarr_embedded::v1::CommandBook {
    use angzarr_embedded::markup::DomainMessage;
    use angzarr_embedded::v1::{command_page, CommandBook, CommandPage, Fact, FactOrigin};
    let session = cover
        .root
        .as_ref()
        .and_then(|root| uuid::Uuid::from_slice(&root.value).ok())
        .map(|id| id.to_string())
        .unwrap_or_default();
    let key = if bind.native_id.is_empty() {
        format!("path:{}", bind.transcript_path)
    } else {
        bind.native_id.clone()
    };
    CommandBook {
        cover: Some(cover),
        page: Some(CommandPage {
            body: Some(command_page::Body::Fact(Fact {
                payload: Some(
                    angzarr_embedded::Any::from_msg(&bind.to_wire()).expect("a bind encodes"),
                ),
                origin: Some(FactOrigin {
                    external_id: format!("bind:{session}:{key}"),
                    description: String::new(),
                }),
            })),
            ..CommandPage::default()
        }),
    }
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
