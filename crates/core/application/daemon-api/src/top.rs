//! `WatchTop` (`ctxloom top`, DRAFT): what the daemon hosts, live. A watch
//! is a snapshot of its scope, then the changes the daemon's read models
//! make, PUSHED: it waits on the models' own wake (`SessionIndexes::watch_into`,
//! `PendingApprovals::watch_into`, sent when a fold changes them) and, woken,
//! reads them and says what differs from what it last said. It never polls.
//!
//! Filtering and scope are the daemon's, here; sorting is the client's.

use std::collections::{BTreeMap, BTreeSet, VecDeque};
use std::sync::mpsc::{channel, Receiver};
use std::sync::Arc;

use daemon_proto::v1;
use pending_approvals::{PendingApprovals, State as AskState};
use run_ports::Edge;
use session_index::{SessionIndexes, SessionRecord};
use session_timeline::{Content, Entry, SessionTimelines};
use sessions::{ProjectName, SessionName};

use crate::refusal::{ApiError, ApiRefusal};
use crate::server::{invalid, state_resource, Api, SessionStates, EVERY_PROJECT};

/// A watch's events, as `WatchTop` streams them: the snapshot, then each
/// change as it is pushed; waited for (the edge runs it on a blocking
/// thread). The watch ends when it is dropped, or when the one session it
/// is scoped to is destroyed.
pub type TopStream = Box<dyn Iterator<Item = v1::TopEvent> + Send>;

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
            (named, _) => Some(
                SessionName::parse(named)
                    .map_err(|rejection| invalid(named, &rejection))?
                    .uid(),
            ),
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
            project,
            scope,
            request,
        };
        // Registered before the snapshot is read, so a change in between
        // wakes the watch rather than being lost.
        let (wake, woken) = channel();
        scene.index.watch_into(wake.clone());
        if let Some(approvals) = &scene.approvals {
            approvals.watch_into(wake.clone());
        }
        if scene.log.is_some() {
            if let Some(timelines) = &scene.timelines {
                timelines.watch_into(wake);
            }
        }
        Ok(Box::new(TopWatch::open(scene, woken)))
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

    /// The log entries of the session `log` names, received after `after`,
    /// and the newest entry number among them (`after` when none).
    fn log_after(&self, after: u64) -> (Vec<v1::TopLog>, u64) {
        let (Some(id), Some(timelines)) = (&self.log, &self.timelines) else {
            return (Vec::new(), after);
        };
        let name = {
            let index = self.index.index();
            index
                .find_by_id(id)
                .map(|record| index.name_of(&record))
                .unwrap_or_default()
        };
        let entries = timelines.index().after(id, after);
        let newest = entries.iter().map(|entry| entry.n).max().unwrap_or(after);
        let logs = entries.iter().map(|entry| log_of(&name, entry)).collect();
        (logs, newest)
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
    said_sessions: BTreeMap<String, v1::Session>,
    said_asks: BTreeMap<String, v1::Approval>,
    owed: VecDeque<v1::TopEvent>,
    /// The newest log entry told.
    said_log: u64,
    /// The one session scoped to was destroyed: the watch ends once its
    /// last event is read.
    ended: bool,
}

fn log_of(session: &str, entry: &Entry) -> v1::TopLog {
    let (kind, line, event, text) = match &entry.content {
        Content::Transcript { line } => (
            v1::top_log::Kind::Transcript,
            line.clone(),
            String::new(),
            String::new(),
        ),
        Content::Daemon { event, text } => (
            v1::top_log::Kind::Daemon,
            String::new(),
            event.clone(),
            text.clone(),
        ),
    };
    v1::TopLog {
        session: session.to_string(),
        time: Some(entry.time.into()),
        kind: kind as i32,
        line,
        event,
        text,
    }
}

fn event(event: v1::top_event::Event) -> v1::TopEvent {
    v1::TopEvent { event: Some(event) }
}

impl TopWatch {
    fn open(scene: Scene, woken: Receiver<()>) -> TopWatch {
        let view = scene.view();
        let snapshot = v1::TopSnapshot {
            sessions: view.sessions.values().cloned().collect(),
            approvals: view.asks.values().cloned().collect(),
        };
        let mut owed = VecDeque::from([event(v1::top_event::Event::Snapshot(snapshot))]);
        let (told, said_log) = scene.log_after(0);
        owed.extend(
            told.into_iter()
                .map(|log| event(v1::top_event::Event::Log(log))),
        );
        TopWatch {
            scene,
            woken,
            said_sessions: view.sessions,
            said_asks: view.asks,
            owed,
            said_log,
            ended: false,
        }
    }

    /// Say what differs between what was last said and the scene now.
    fn refresh(&mut self) {
        use v1::top_event::Event;
        let view = self.scene.view();
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
            self.ended |= destroyed && self.scene.scope.is_some() && view.sessions.is_empty();
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
        let (entries, newest) = self.scene.log_after(self.said_log);
        self.said_log = newest;
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
            if self.ended {
                return None;
            }
            // Parked until a model's fold wakes it; a wake is all it is told.
            self.woken.recv().ok()?;
            while self.woken.try_recv().is_ok() {}
            self.refresh();
        }
    }
}

#[cfg(test)]
#[path = "top.test.rs"]
mod tests;
