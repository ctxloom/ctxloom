//! `WatchTop` (`ctxloom top`, DRAFT): what the daemon hosts, live. A watch
//! is a snapshot of its scope, then the changes the daemon's read models
//! make, PUSHED: it waits on the models' own wake (`SessionIndexes::watch_into`,
//! `PendingApprovals::watch_into`, sent when a fold changes them) and, woken,
//! reads them and says what differs from what it last said. It never polls.
//!
//! Filtering and scope are the daemon's, here; sorting is the client's.

use std::collections::{BTreeMap, BTreeSet, VecDeque};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::{channel, Receiver, Sender};
use std::sync::Arc;
use std::time::Duration;

use daemon_proto::v1;
use pending_approvals::{PendingApprovals, State as AskState};
use run_ports::Edge;
use session_index::{SessionIndexes, SessionRecord};
use session_timeline::{Entry, SessionTimelines};
use sessions::{ProjectName, SessionName};

use crate::refusal::{ApiError, ApiRefusal};
use crate::server::{invalid, state_resource, Api, SessionStates, EVERY_PROJECT};

/// How long after a wake the watch waits before each look again.
const SETTLE: [Duration; 3] = [
    Duration::from_millis(100),
    Duration::from_millis(400),
    Duration::from_millis(1500),
];

/// A watch's events, as `WatchTop` streams them: the snapshot, then each
/// change as it is pushed; waited for (the edge runs it on a blocking
/// thread). The watch ends when it is dropped, or when the one session it
/// is scoped to is destroyed.
pub struct TopStream {
    events: Box<dyn Iterator<Item = v1::TopEvent> + Send>,
    stop: TopStop,
}

impl Iterator for TopStream {
    type Item = v1::TopEvent;

    fn next(&mut self) -> Option<v1::TopEvent> {
        self.events.next()
    }
}

impl TopStream {
    /// What ends the watch from another thread: the edge uses it when the
    /// client has gone, so a watch with nothing to say is not parked for ever.
    pub fn stopper(&self) -> TopStop {
        self.stop.clone()
    }
}

/// Ends a watch, wherever it is waiting.
#[derive(Clone)]
pub struct TopStop {
    gone: Arc<AtomicBool>,
    wake: Sender<()>,
}

impl TopStop {
    /// The watch ends at its next look, which is woken now.
    pub fn stop(&self) {
        self.gone.store(true, Ordering::SeqCst);
        let _ = self.wake.send(());
    }
}

impl<E: Edge> Api<E> {
    /// `WatchTop`: the scope `request` names, filtered, watched from now.
    pub async fn watch_top(&self, request: v1::WatchTopRequest) -> Result<TopStream, ApiError> {
        let project = match request.parent.as_str() {
            EVERY_PROJECT => None,
            parent => Some(
                ProjectName::parse(parent)
                    .map_err(|rejection| invalid(parent, &rejection))?
                    .harp()
                    .to_string(),
            ),
        };
        self.parts.hosting.fresh().await;
        self.owners_exist(&request, project.as_deref())?;
        let scope = match request.session.as_str() {
            "" => None,
            named => {
                let name =
                    SessionName::parse(named).map_err(|rejection| invalid(named, &rejection))?;
                let record = self.parts.index.index().find_by_id(&name.uid());
                let record = record.ok_or_else(|| {
                    ApiError::from(ApiRefusal::of(&v1::SessionNotFound {
                        name: named.to_string(),
                    }))
                })?;
                Some((record.id, request.subtree))
            }
        };
        let log = match (request.log_session.as_str(), &scope) {
            ("", Some((id, false))) => Some(id.clone()),
            ("", _) => None,
            (named, _) => {
                let uid = SessionName::parse(named)
                    .map_err(|rejection| invalid(named, &rejection))?
                    .uid();
                // A log is of a session there is: asked for one nobody has,
                // the watch is refused, not left waiting for it.
                if self.parts.index.index().find_by_id(&uid).is_none() {
                    return Err(ApiError::from(ApiRefusal::of(&v1::SessionNotFound {
                        name: named.to_string(),
                    })));
                }
                Some(uid)
            }
        };
        let scene = Scene {
            states: Arc::clone(&self.parts.states),
            index: self.parts.index.clone(),
            approvals: self
                .approving
                .as_ref()
                .map(|approving| approving.index.clone()),
            timelines: self.timelines.clone(),
            log,
            all_logs: request.all_logs,
            project,
            scope,
            request,
        };
        // Registered before the snapshot is read, so a change in between
        // wakes the watch rather than being lost.
        let (wake, woken) = channel();
        let gone = Arc::new(AtomicBool::new(false));
        let stop = TopStop {
            gone: Arc::clone(&gone),
            wake: wake.clone(),
        };
        scene.index.watch_into(wake.clone());
        if let Some(approvals) = &scene.approvals {
            approvals.watch_into(wake.clone());
        }
        if scene.log.is_some() || scene.all_logs {
            if let Some(timelines) = &scene.timelines {
                timelines.watch_into(wake);
            }
        }
        Ok(TopStream {
            events: Box::new(TopWatch::open(scene, woken, gone)),
            stop,
        })
    }
}

impl<E: Edge> Api<E> {
    /// Every session `--owner` names is one the project has.
    fn owners_exist(
        &self,
        request: &v1::WatchTopRequest,
        project: Option<&str>,
    ) -> Result<(), ApiError> {
        let index = self.parts.index.index();
        for owner in &request.owners {
            let known = index.records().iter().any(|record| {
                index.name_of(record).rsplit('/').next() == Some(owner.as_str())
                    && project
                        .is_none_or(|harp| index.project_harp(&record.project_id) == Some(harp))
            });
            if !known {
                return Err(ApiError::from(ApiRefusal::of(&v1::SessionNotFound {
                    name: format!("{}/sessions/{owner}", request.parent),
                })));
            }
        }
        Ok(())
    }
}

/// What a watch reads: its scope and filters, over the models.
struct Scene {
    states: Arc<dyn SessionStates>,
    index: SessionIndexes,
    approvals: Option<PendingApprovals>,
    timelines: Option<SessionTimelines>,
    /// The stable id of the session whose log the watch carries.
    log: Option<String>,
    /// Every session in view carries its log.
    all_logs: bool,
    /// The project's harp; none, every project's.
    project: Option<String>,
    /// The session scoped to, by its stable id, and whether its subtree.
    scope: Option<(String, bool)>,
    request: v1::WatchTopRequest,
}

/// The scene as it stands.
struct View {
    sessions: BTreeMap<String, v1::Session>,
    /// Every session of the index, in scope or not: what tells a session
    /// destroyed from one that only left the filter.
    known: BTreeSet<String>,
    asks: BTreeMap<String, v1::Approval>,
}

impl Scene {
    fn view(&self) -> View {
        let (sessions, known) = self.sessions();
        let asks = self.asks(&sessions);
        View {
            sessions,
            known,
            asks,
        }
    }

    fn sessions(&self) -> (BTreeMap<String, v1::Session>, BTreeSet<String>) {
        let index = self.index.index();
        let records: Vec<SessionRecord> = index
            .records()
            .into_iter()
            .filter(|record| match &self.project {
                None => true,
                Some(harp) => index.project_harp(&record.project_id) == Some(harp.as_str()),
            })
            .collect();
        let known = records.iter().map(|record| index.name_of(record)).collect();
        let scoped = self.scoped(&records);
        let sessions = records
            .iter()
            .filter(|record| scoped.contains(&record.id))
            .map(|record| state_resource(self.states.as_ref(), &index, record).0)
            .filter(|session| self.matches(session))
            .map(|session| (session.name.clone(), session))
            .collect();
        (sessions, known)
    }

    /// The ids of the sessions in scope: all, one, or one with its
    /// descendants (by owner, to a fixed point).
    fn scoped(&self, records: &[SessionRecord]) -> BTreeSet<String> {
        let Some((root, subtree)) = &self.scope else {
            return records.iter().map(|record| record.id.clone()).collect();
        };
        let mut scoped = BTreeSet::from([root.clone()]);
        let mut grown = *subtree;
        while grown {
            let before = scoped.len();
            for record in records {
                if record
                    .owner
                    .as_ref()
                    .is_some_and(|owner| scoped.contains(&owner.session))
                {
                    scoped.insert(record.id.clone());
                }
            }
            grown = scoped.len() > before;
        }
        scoped
    }

    /// Whether `session` passes the filters: kinds AND, a kind's values OR.
    fn matches(&self, session: &v1::Session) -> bool {
        let request = &self.request;
        let states = match request.states.is_empty() {
            true => vec![v1::session::State::Live as i32],
            false => request.states.clone(),
        };
        let any = |wanted: &[String], have: &[&str]| {
            wanted.is_empty() || wanted.iter().any(|want| have.contains(&want.as_str()))
        };
        let harp_of = |name: &str| name.rsplit('/').next().unwrap_or_default().to_string();
        states.contains(&session.state)
            && any(&request.agents, &[session.agent.as_str()])
            && any(&request.engines, &[session.engine.as_str()])
            && any(
                &request.profiles,
                &session
                    .profiles
                    .iter()
                    .map(String::as_str)
                    .collect::<Vec<_>>(),
            )
            && any(
                &request.tags,
                &session.tags.iter().map(String::as_str).collect::<Vec<_>>(),
            )
            && (request.owners.is_empty()
                || (!session.owner.is_empty() && request.owners.contains(&harp_of(&session.owner))))
    }

    /// The asks waiting under `sessions`, by name.
    fn asks(&self, sessions: &BTreeMap<String, v1::Session>) -> BTreeMap<String, v1::Approval> {
        let Some(approvals) = &self.approvals else {
            return BTreeMap::new();
        };
        let projects: BTreeSet<&str> = sessions
            .keys()
            .filter_map(|name| name.split("/sessions/").next())
            .collect();
        let index = approvals.index();
        projects
            .into_iter()
            .flat_map(|project| index.listed(project, None, true))
            .filter(|row| sessions.contains_key(&row.child_name))
            .map(|row| (row.name.clone(), row.resource()))
            .collect()
    }

    /// The log entries received since `said` (each session's newest told),
    /// of the sessions whose logs the watch carries, oldest first by session.
    fn log_after(
        &self,
        said: &mut BTreeMap<String, u64>,
        carried: &BTreeSet<String>,
    ) -> Vec<v1::TopLog> {
        let Some(timelines) = &self.timelines else {
            return Vec::new();
        };
        let ids: Vec<String> = match (self.all_logs, &self.log) {
            (true, _) => carried
                .iter()
                .filter_map(|name| SessionName::parse(name).ok())
                .map(|name| name.uid())
                .collect(),
            (false, Some(id)) => vec![id.clone()],
            (false, None) => Vec::new(),
        };
        let mut told = Vec::new();
        for id in ids {
            let name = {
                let index = self.index.index();
                index
                    .find_by_id(&id)
                    .map(|record| index.name_of(&record))
                    .unwrap_or_default()
            };
            let after = said.get(&id).copied().unwrap_or(0);
            let entries = timelines.index().after(&id, after);
            if let Some(newest) = entries.iter().map(|entry| entry.n).max() {
                said.insert(id, newest);
            }
            told.extend(entries.iter().map(|entry| log_of(&name, entry)));
        }
        told
    }

    /// The ask `name` as the index has it now, settled or not.
    fn ask(&self, name: &str) -> Option<v1::Approval> {
        let row = self.approvals.as_ref()?.index().named(name)?;
        (row.state != AskState::Waiting).then(|| row.resource())
    }
}

/// A watch: what it last said, and what it is owed.
struct TopWatch {
    scene: Scene,
    woken: Receiver<()>,
    /// The client is gone: the watch ends.
    gone: Arc<AtomicBool>,
    said_sessions: BTreeMap<String, v1::Session>,
    said_asks: BTreeMap<String, v1::Approval>,
    owed: VecDeque<v1::TopEvent>,
    /// The newest log entry told.
    said_log: BTreeMap<String, u64>,
    /// The sessions it said had left the view, and may be destroyed after.
    left: BTreeSet<String>,
    /// When it looks again after a wake, soonest first.
    settle: VecDeque<Duration>,
    /// The one session scoped to was destroyed: the watch ends once its
    /// last event is read.
    ended: bool,
}

fn log_of(session: &str, entry: &Entry) -> v1::TopLog {
    v1::TopLog {
        session: session.to_string(),
        time: Some(entry.time.into()),
        entry: Some(entry.log.clone()),
    }
}

fn event(event: v1::top_event::Event) -> v1::TopEvent {
    v1::TopEvent { event: Some(event) }
}

impl TopWatch {
    fn open(scene: Scene, woken: Receiver<()>, gone: Arc<AtomicBool>) -> TopWatch {
        let view = scene.view();
        let snapshot = v1::TopSnapshot {
            sessions: view.sessions.values().cloned().collect(),
            approvals: view.asks.values().cloned().collect(),
        };
        let mut owed = VecDeque::from([event(v1::top_event::Event::Snapshot(snapshot))]);
        let mut said_log = BTreeMap::new();
        let told = scene.log_after(&mut said_log, &view.sessions.keys().cloned().collect());
        owed.extend(
            told.into_iter()
                .map(|log| event(v1::top_event::Event::Log(log))),
        );
        TopWatch {
            scene,
            woken,
            gone,
            said_sessions: view.sessions,
            said_asks: view.asks,
            owed,
            said_log,
            settle: VecDeque::new(),
            left: BTreeSet::new(),
            ended: false,
        }
    }

    /// Say what differs between what was last said and the scene now.
    fn refresh(&mut self) {
        use v1::top_event::Event;
        let view = self.scene.view();
        // What a session said is told while the watch still has it and after
        // it left (the word that explains its leaving is folded about the
        // time its lock is let go): a leaving session's entries come before
        // its removal.
        let leaving: BTreeSet<String> = self
            .said_sessions
            .keys()
            .filter(|name| !view.sessions.contains_key(*name))
            .cloned()
            .collect();
        let carried: BTreeSet<String> = view
            .sessions
            .keys()
            .chain(&leaving)
            .chain(&self.left)
            .cloned()
            .collect();
        let (parting, entries): (Vec<v1::TopLog>, Vec<v1::TopLog>) = self
            .scene
            .log_after(&mut self.said_log, &carried)
            .into_iter()
            .partition(|log| leaving.contains(&log.session));
        self.owed
            .extend(parting.into_iter().map(|log| event(Event::Log(log))));
        for (name, session) in &view.sessions {
            if self.said_sessions.get(name) != Some(session) {
                self.owed
                    .push_back(event(Event::SessionChanged(v1::TopSessionChanged {
                        session: Some(session.clone()),
                    })));
            }
        }
        for name in self.said_sessions.keys() {
            if view.sessions.contains_key(name) {
                continue;
            }
            let destroyed = !view.known.contains(name);
            let reason = match destroyed {
                true => v1::top_session_removed::Reason::Destroyed,
                false => v1::top_session_removed::Reason::LeftFilter,
            };
            self.owed
                .push_back(event(Event::SessionRemoved(v1::TopSessionRemoved {
                    name: name.clone(),
                    reason: reason as i32,
                })));
            if !destroyed {
                self.left.insert(name.clone());
            }
            self.ended |= destroyed && self.scene.scope.is_some() && view.sessions.is_empty();
        }
        // A session that left the view (its lock let go, a destroy's
        // directory taken) and is destroyed after is removed once more, for
        // what became of it; one back in view is no longer gone.
        self.left.retain(|name| !view.sessions.contains_key(name));
        let destroyed: Vec<String> = self
            .left
            .iter()
            .filter(|name| !view.known.contains(*name))
            .cloned()
            .collect();
        for name in destroyed {
            self.left.remove(&name);
            self.owed
                .push_back(event(Event::SessionRemoved(v1::TopSessionRemoved {
                    name,
                    reason: v1::top_session_removed::Reason::Destroyed as i32,
                })));
            self.ended |= self.scene.scope.is_some() && view.sessions.is_empty();
        }
        for (name, ask) in &view.asks {
            if !self.said_asks.contains_key(name) {
                self.owed
                    .push_back(event(Event::ApprovalAsked(v1::TopApprovalAsked {
                        approval: Some(ask.clone()),
                    })));
            }
        }
        for name in self.said_asks.keys() {
            if view.asks.contains_key(name) {
                continue;
            }
            if let Some(settled) = self.scene.ask(name) {
                self.owed
                    .push_back(event(Event::ApprovalSettled(v1::TopApprovalSettled {
                        approval: Some(settled),
                    })));
            }
        }
        self.owed.extend(
            entries
                .into_iter()
                .map(|log| event(v1::top_event::Event::Log(log))),
        );
        self.said_sessions = view.sessions;
        self.said_asks = view.asks;
    }
}

impl Iterator for TopWatch {
    type Item = v1::TopEvent;

    fn next(&mut self) -> Option<v1::TopEvent> {
        loop {
            if let Some(next) = self.owed.pop_front() {
                return Some(next);
            }
            if self.ended || self.gone.load(Ordering::SeqCst) {
                return None;
            }
            // Parked until a model's fold wakes it; a wake is all it is told.
            // A session's lock is let go just after the fold that says its
            // run ended, and nothing folds that: after a wake the watch
            // looks again a few times at set instants, then parks.
            let woken = match self.settle.front().copied() {
                Some(wait) => self.woken.recv_timeout(wait),
                None => self
                    .woken
                    .recv()
                    .map_err(|_| std::sync::mpsc::RecvTimeoutError::Disconnected),
            };
            match woken {
                Ok(()) => {
                    while self.woken.try_recv().is_ok() {}
                    self.settle = SETTLE.into_iter().collect();
                }
                Err(std::sync::mpsc::RecvTimeoutError::Timeout) => {
                    self.settle.pop_front();
                }
                Err(std::sync::mpsc::RecvTimeoutError::Disconnected) => return None,
            }
            self.refresh();
        }
    }
}

#[cfg(test)]
#[path = "top.test.rs"]
mod tests;
