//! Hook capture's composition: what a hook's observations are delivered
//! through, and how a hook finds the session its harp names.
//!
//! - [`EdgeSink`]: the in-process [`FactSink`], over any [`Edge`] (the
//!   `EmbeddedEdge` of [`crate::InProcessCore`]): a fact book is served at the
//!   edge and the answer is the [`Delivery`]. It is not a hook's transport: a
//!   hook is a separate process.
//! - [`Unreachable`]: the sink of a hook whose environment names an endpoint
//!   it cannot use: every delivery is `Unavailable`.
//! - [`HookSink`]: what a hook process delivers through when its
//!   environment names an endpoint (`CTXLOOM_ENDPOINT`, kept for M10b's
//!   containers, OD-13): [`GrpcSink`] there when it and `CTXLOOM_BEARER` are
//!   well formed, else [`Unreachable`]. Such a hook never spawns a daemon
//!   and never holds (a container's hold would sit in the container).
//! - [`daemon_sink`]: the user daemon's listener as its discovery file names
//!   it, with the USER bearer it holds.
//! - [`HoldingRecording`]: a hook with no endpoint in its environment (S16,
//!   slice 4 §3.8 (ii), s4-Q14 unit H2): it reaches the user's daemon
//!   ([`HookReach`], one spawned if none serves, within the hook's bound),
//!   and delivers, or HOLDS its facts in its session's hold
//!   (`runs_application::Holding`, over fact-spool's `Spool`) when the
//!   daemon is older than the hook, none could be reached or started, or a
//!   delivery is not settled; up to date, it first delivers what its
//!   session already holds.
//! - [`SessionIds`] / [`RunIds`]: a hook's session's id. A session's id is
//!   minted at random (it is not derived from the harp); the run tells its
//!   engine it, `CTXLOOM_SESSION` (review finding 6, ruling 87), so a hook
//!   addresses its facts to its run's session and reads nothing on disk.
//! - [`HookRecording`]: [`hook_cli::Recording`] over a sink and the ids (the
//!   environment's endpoint).

use std::sync::Arc;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use angzarr_diagnostic::Diagnostic;
use angzarr_embedded::v1::{DeliveryOutcome, Uuid};
use angzarr_embedded::{Failure, KernelError};
use hook_cli::{NotRecorded, Recording, Unrecorded};
use hook_sink_grpc::GrpcSink;
use run_ports::{
    Arrival, Bearer, Bound, CertError, Delivery, Edge, FactSink, HeldFacts, PublicCert, Unserved,
};
use runs_application::{
    delivery_budget, Capture, HoldReport, Holding, HookClock, Kept, NotHeld, Outcome, Serving, Why,
};
use setup_application::HookAnswer;

/// A sink over an in-process edge.
pub struct EdgeSink<E: Edge + 'static> {
    edge: Arc<E>,
}

impl<E: Edge + 'static> EdgeSink<E> {
    /// A sink that serves the books it is handed at `edge`.
    pub fn new(edge: Arc<E>) -> EdgeSink<E> {
        EdgeSink { edge }
    }
}

impl<E: Edge + 'static> FactSink for EdgeSink<E> {
    fn deliver(&self, book: angzarr_embedded::v1::CommandBook, within: Duration) -> Delivery {
        // A runtime of its own, on a thread of its own: this may be called
        // from inside another runtime's thread, which cannot block on one.
        let edge = &self.edge;
        let asked = book.clone();
        std::thread::scope(|scope| {
            let served = scope.spawn(move || {
                let runtime = tokio::runtime::Builder::new_current_thread()
                    .enable_time()
                    .build()
                    .map_err(|e| Delivery::Unavailable {
                        detail: e.to_string(),
                    })?;
                runtime.block_on(async {
                    tokio::time::timeout(within, edge.serve(book, Arrival::Loopback))
                        .await
                        .map_err(|_| Delivery::TimedOut)
                })
            });
            match served.join() {
                // As the daemon's listener answers it (s4-Q14, H3).
                Ok(Ok(Ok(answered))) if runs_application::no_such_session(&asked, &answered) => {
                    Delivery::NoSuchSession
                }
                Ok(Ok(served)) => delivery_of(served),
                Ok(Err(delivery)) => delivery,
                Err(_) => Delivery::Unavailable {
                    detail: "the delivery panicked".to_string(),
                },
            }
        })
    }
}

/// What the edge's answer to a fact means to its sender.
fn delivery_of(served: Result<angzarr_embedded::Answered, Unserved>) -> Delivery {
    match served {
        Ok(answered) => match answered.delivery {
            DeliveryOutcome::Applied => Delivery::Applied,
            DeliveryOutcome::Duplicate => Delivery::Duplicate,
            DeliveryOutcome::Unspecified => Delivery::Refused {
                detail: "the kernel gave no delivery outcome".to_string(),
            },
        },
        Err(Unserved::Hidden { domain }) => Delivery::Refused {
            detail: format!("the domain {domain} is not served"),
        },
        Err(Unserved::Failed(Failure::Kernel(KernelError::FactDeadLettered { .. }))) => {
            Delivery::Parked
        }
        Err(Unserved::Failed(Failure::Rejected(rejected))) => Delivery::Refused {
            detail: format!("{rejected:?}"),
        },
        Err(Unserved::Failed(other)) => Delivery::Unavailable {
            detail: format!("{other:?}"),
        },
    }
}

/// The sink of a hook process whose environment names no usable listener.
pub struct Unreachable;

impl FactSink for Unreachable {
    fn deliver(&self, _: angzarr_embedded::v1::CommandBook, _: Duration) -> Delivery {
        Delivery::Unavailable {
            detail: "the listener is not running".to_string(),
        }
    }
}

/// The sink of a hook process whose environment names an endpoint: the
/// listener there, else [`Unreachable`].
pub enum HookSink {
    Listener(GrpcSink),
    /// A listener is named but its certificate is not usable: refused, and
    /// nothing is sent (never plaintext, never the system's roots).
    Refused(CertError),
    Unreachable(Unreachable),
}

impl HookSink {
    /// The sink the hook environment makes: the endpoint (a URL, `https://<host>:<port>`), the certificate (`CTXLOOM_TLS_CERT`)
    /// and the bearer (`<run>.<token>`). A missing, empty or malformed one
    /// gives [`Unreachable`]: the hook fails open.
    pub fn from_environment(
        endpoint: Option<&str>,
        bearer: Option<&str>,
        cert: Option<&str>,
    ) -> HookSink {
        let named = endpoint.filter(|e| !e.is_empty()).and_then(|endpoint| {
            let (run, token) = Bearer::parse(bearer?).ok()?;
            Some((endpoint, Bearer::compose(&run, &token)))
        });
        match named {
            None => HookSink::Unreachable(Unreachable),
            Some((endpoint, bearer)) => match PublicCert::from_environment(cert) {
                Ok(cert) => HookSink::Listener(GrpcSink::new(endpoint, bearer, cert)),
                Err(error) => HookSink::Refused(error),
            },
        }
    }
}

/// The user daemon's listener as `discovery` (its discovery file, read at
/// each hook) names it: its endpoint, its certificate pinned, and the USER
/// bearer, so a restarted daemon's new port, certificate and bearer are
/// found. The daemon has one principal, the user: a hook presents her
/// bearer, never its run's.
pub fn daemon_sink(discovery: run_ports::Discovery) -> GrpcSink {
    GrpcSink::new(
        discovery.endpoint,
        Bearer::user(&discovery.bearer),
        discovery.cert,
    )
}

/// Why nothing was delivered for want of a usable certificate, as the hook
/// says it: the code, what is wrong, and that nothing was sent.
fn refusal_text(error: &CertError) -> String {
    let wrong = match error {
        CertError::Missing => "CTXLOOM_TLS_CERT is not set",
        CertError::Malformed { .. } => "CTXLOOM_TLS_CERT is not a certificate",
    };
    format!(
        "{}: {wrong}; nothing was sent (the hook never falls back to plaintext)",
        error.code()
    )
}

impl FactSink for HookSink {
    fn deliver(&self, book: angzarr_embedded::v1::CommandBook, within: Duration) -> Delivery {
        match self {
            HookSink::Listener(sink) => sink.deliver(book, within),
            HookSink::Refused(error) => Delivery::Unavailable {
                detail: refusal_text(error),
            },
            HookSink::Unreachable(sink) => sink.deliver(book, within),
        }
    }
}

/// A hook's session.
pub trait SessionIds {
    /// The id of the session the hook's `harp` names, if it can be told.
    fn session_of(&self, harp: &str) -> Option<Uuid>;
}

/// The run's session, as the hook environment names it (`CTXLOOM_SESSION`,
/// review finding 6): the one session a hook's facts are addressed to,
/// whatever its harp says; its stable id is the UUIDv5 of the resource name
/// the variable carries (s3-Q3); none where the variable is unset or names
/// no session.
pub struct RunIds(Option<String>);

impl RunIds {
    /// The ids the environment's `CTXLOOM_SESSION` names.
    pub fn from_environment(session: Option<&str>) -> RunIds {
        RunIds(session.map(str::to_string))
    }
}

impl SessionIds for RunIds {
    fn session_of(&self, _: &str) -> Option<Uuid> {
        let name = sessions::SessionName::parse(self.0.as_deref()?).ok()?;
        Some(Uuid::from_bytes(*name.id().as_bytes()))
    }
}

/// The harp of the session `CTXLOOM_SESSION` names (`session`, its resource
/// name: s3-Q3), as a hook and the statusline say it; empty where it names
/// none.
pub(crate) fn harp_of(session: Option<&str>) -> String {
    session
        .and_then(|name| sessions::SessionName::parse(name).ok())
        .map(|name| name.harp().to_string())
        .unwrap_or_default()
}

/// A hook's recording: its observations, delivered through a sink to the
/// session its harp names.
pub struct HookRecording<S: FactSink, I: SessionIds> {
    capture: Capture<S>,
    ids: I,
}

impl<S: FactSink, I: SessionIds> HookRecording<S, I> {
    pub fn new(sink: S, ids: I) -> HookRecording<S, I> {
        HookRecording {
            capture: Capture::new(sink),
            ids,
        }
    }
}

impl<S: FactSink, I: SessionIds> Recording for HookRecording<S, I> {
    fn record(&self, native: &str, harp: &str, answer: &HookAnswer) -> Vec<Unrecorded> {
        let Some(event) = &answer.event else {
            return Vec::new();
        };
        let Some(session) = session_or_said(&self.ids, harp, event) else {
            return no_session(harp, event);
        };
        self.capture
            .record(&session, event, delivery_budget(native))
            .unrecorded()
            .into_iter()
            .map(|attempt| {
                Unrecorded::from(NotRecorded {
                    what: attempt.external_id.clone(),
                    why: match &attempt.delivery {
                        Some(delivery) => delivery_text(delivery),
                        None => "not tried: an earlier delivery found no one answering".to_string(),
                    },
                })
            })
            .collect()
    }
}

/// The session `harp`'s id, when `event` observes anything and `ids` can
/// tell it; none otherwise ([`no_session`] says which).
fn session_or_said<I: SessionIds>(
    ids: &I,
    harp: &str,
    event: &surface_ports::HookEvent,
) -> Option<Uuid> {
    if runs_application::observe(event).is_empty() {
        return None;
    }
    ids.session_of(harp)
}

/// What a hook says of `event`'s observations when it has no session to
/// address them to (nothing, when it observed nothing).
fn no_session(harp: &str, event: &surface_ports::HookEvent) -> Vec<Unrecorded> {
    runs_application::observe(event)
        .iter()
        .map(|observation| {
            Unrecorded::from(NotRecorded {
                what: observation.type_url(),
                why: format!(
                    "no session for {harp}: {} names none",
                    run_ports::SESSION_VARIABLE
                ),
            })
        })
        .collect()
}

/// A delivery's answer, as the hook says it.
fn delivery_text(delivery: &Delivery) -> String {
    match delivery {
        Delivery::Unavailable { detail } => detail.clone(),
        Delivery::TimedOut => "the delivery timed out".to_string(),
        Delivery::NoSuchSession => "the daemon has no record of this session".to_string(),
        other => format!("{other:?}"),
    }
}

// ------------------------------------------------------------- the hold

/// The user's daemon, as a hook reaches it.
pub trait HookReach {
    type Sink: FactSink;
    /// The daemon serving this home (one started if none serves, waited for
    /// at most `wait`): its versionator stamp and the sink to it; none when
    /// none could be reached or started.
    fn reach(&self, wait: Duration) -> Option<(String, Self::Sink)>;
}

/// The user's daemon as its discovery file names it, spawned when none
/// serves ([`crate::daemon::hook_daemon`]).
pub struct DaemonReach(pub(crate) Arc<process_env::EnvSnapshot>);

impl HookReach for DaemonReach {
    type Sink = GrpcSink;

    fn reach(&self, wait: Duration) -> Option<(String, GrpcSink)> {
        let found = crate::daemon::hook_daemon(&self.0, wait)?;
        Some((found.stamp.clone(), daemon_sink(found)))
    }
}

/// A hook's time: its bound's end, as the clock reads it.
pub struct Deadline(pub Instant);

impl HookClock for Deadline {
    fn remaining(&self) -> Duration {
        self.0.saturating_duration_since(Instant::now())
    }

    fn now(&self) -> Option<daemon_proto::google::protobuf::Timestamp> {
        let since = SystemTime::now().duration_since(UNIX_EPOCH).ok()?;
        Some(daemon_proto::google::protobuf::Timestamp {
            seconds: i64::try_from(since.as_secs()).ok()?,
            nanos: i32::try_from(since.subsec_nanos()).ok()?,
        })
    }
}

/// A hook's recording when its environment names no endpoint (S16, s4-Q14
/// H2): to the user's daemon, or into its session's hold.
pub struct HoldingRecording<R: HookReach, H: HeldFacts, I: SessionIds> {
    reach: R,
    held: H,
    ids: I,
    /// The hook's own versionator stamp.
    own: String,
    /// `CTXLOOM_SESSION`'s resource name.
    session: String,
    /// When the hook started: its bound runs from here.
    started: Instant,
}

impl<R: HookReach, H: HeldFacts, I: SessionIds> HoldingRecording<R, H, I> {
    /// The recording of a hook of stamp `own` for the session named
    /// `session`, started at `started`.
    pub fn new(
        reach: R,
        held: H,
        ids: I,
        own: impl Into<String>,
        session: impl Into<String>,
        started: Instant,
    ) -> HoldingRecording<R, H, I> {
        HoldingRecording {
            reach,
            held,
            ids,
            own: own.into(),
            session: session.into(),
            started,
        }
    }
}

/// How long a hook may take in all for `native`: S16's bound, or less where
/// the engine gives the hook less ([`delivery_budget`]).
pub(crate) fn hook_bound(native: &str) -> Duration {
    crate::daemon::HOOK_BOUND.min(delivery_budget(native))
}

/// How long a hook whose bound ends at `deadline` waits for a daemon it
/// spawned: what the bound leaves a delivery its margin of, and never more
/// than a client's [`crate::daemon::SPAWN_WAIT`].
pub(crate) fn spawn_wait(clock: &Deadline) -> Duration {
    clock
        .remaining()
        .saturating_sub(crate::daemon::DELIVERY_MARGIN)
        .min(crate::daemon::SPAWN_WAIT)
}

impl<R: HookReach, H: HeldFacts, I: SessionIds> Recording for HoldingRecording<R, H, I> {
    fn record(&self, native: &str, harp: &str, answer: &HookAnswer) -> Vec<Unrecorded> {
        let Some(event) = &answer.event else {
            return Vec::new();
        };
        let Some(session) = session_or_said(&self.ids, harp, event) else {
            return no_session(harp, event);
        };
        let clock = Deadline(self.started + hook_bound(native));
        let reached = self.reach.reach(spawn_wait(&clock));
        let holding = Holding::new(&self.held, &clock, &self.own, &self.session);
        let report = match &reached {
            Some((stamp, sink)) => holding.record(
                &session,
                event,
                Serving::Daemon {
                    stamp: stamp.as_str(),
                    sink,
                },
            ),
            None => holding.record::<R::Sink>(
                &session,
                event,
                Serving::Unreached {
                    why: format!(
                        "no ctxloom daemon could be reached or started within {} s",
                        hook_bound(native).as_secs()
                    ),
                },
            ),
        };
        said(&report, harp)
    }
}

/// What a hook says of what its holding did: each fact that was not
/// recorded now.
fn said(report: &HoldReport, harp: &str) -> Vec<Unrecorded> {
    report
        .said()
        .into_iter()
        .map(|kept| unrecorded(kept, harp))
        .collect()
}

fn unrecorded(kept: &Kept, harp: &str) -> Unrecorded {
    let what = kept.external_id.clone();
    let why = match &kept.outcome {
        Outcome::Held(why) => {
            return Unrecorded::Held {
                what,
                harp: harp.to_string(),
                why: held_text(why),
            }
        }
        Outcome::HeldBehind => {
            return Unrecorded::HeldBehind {
                what,
                harp: harp.to_string(),
            }
        }
        Outcome::Delivered(delivery) => delivery_text(delivery),
        Outcome::NotHeld(NotHeld::Full(Bound::Session(bytes))) => {
            format!("the hold for {harp} is at its bound ({} MiB)", bytes >> 20)
        }
        Outcome::NotHeld(NotHeld::Full(Bound::Home(bytes))) => format!(
            "the hold for {harp} is at its bound: this home's holds are at theirs ({} MiB)",
            bytes >> 20
        ),
        Outcome::NotHeld(NotHeld::Failed(error)) => format!("it could not be held: {error}"),
        Outcome::NotHeld(NotHeld::Foreign) => {
            format!("a held record that is not a fact of {harp}'s stream was set aside")
        }
    };
    Unrecorded::from(NotRecorded { what, why })
}

/// Why a fact was held, as the hook says it.
fn held_text(why: &Why) -> String {
    match why {
        Why::Older { daemon, own } => {
            format!("the serving daemon is ctxloom {daemon}, older than this hook's ctxloom {own}")
        }
        Why::Unreached { why } => why.clone(),
        Why::Unsettled(delivery) => delivery_text(delivery),
        Why::OutOfTime => "the hook's time ran out before it was delivered".to_string(),
    }
}

#[cfg(test)]
#[path = "capture.test.rs"]
mod tests;
