//! `ctxloom top`'s screen as a model: what the watch said, what the person
//! selected, filtered and sorted, and the FRAME those make. The frame is the
//! one thing drawn: the text seam writes it as plain lines, and the terminal
//! UI paints the same lines with ratatui, so what a test reads is what the
//! person sees.
//!
//! Pure: events and keys in, [`Effect`]s out; the daemon, the terminal and
//! the clock are the driver's.

use std::collections::{BTreeMap, BTreeSet};

use daemon_proto::v1;

use crate::approvals::AlwaysPlan;
use crate::log_text::{text_of, Of};
use crate::top::Sort;

/// What the status line says while no daemon serves.
pub const WAITING: &str = "waiting for a ctxloom daemon";

/// The icon before `ASK`, the state of a row an ask waits on, on a UTF-8
/// terminal: a raised hand, two columns wide (owner ruling 2026-11-24).
pub const ICON_UTF8: &str = "\u{270b}";
/// The icon where the terminal is not UTF-8, and in the text seam.
pub const ICON_ASCII: &str = "!";

/// The table's header row, the columns in order.
pub const HEADER: [&str; 7] = [
    "NAME", "STATE", "AGENT", "PROFILES", "TAGS", "ENGINE", "OWNER",
];

/// A key, as the screen takes it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Key {
    Down,
    Up,
    Tab,
    Enter,
    Backspace,
    Esc,
    Digit(u8),
    Char(char),
    /// A whole line typed at a prompt (the text seam's).
    Text(String),
}

impl Key {
    /// The key a seam's line names: `down`, `up`, `tab`, `enter`, `esc`, a
    /// digit or one letter; none for anything else.
    pub fn named(word: &str) -> Option<Key> {
        let mut chars = word.chars();
        match (word, chars.next(), chars.next()) {
            ("down", ..) => Some(Key::Down),
            ("up", ..) => Some(Key::Up),
            ("tab", ..) => Some(Key::Tab),
            ("enter", ..) => Some(Key::Enter),
            ("esc", ..) => Some(Key::Esc),
            (_, Some(c), None) => Some(match c.to_digit(10) {
                Some(n @ 1..=9) => Key::Digit(n as u8),
                _ => Key::Char(c),
            }),
            _ => None,
        }
    }
}

/// What a key asks the driver to do.
#[derive(Debug, PartialEq)]
pub enum Effect {
    /// Choose `option` of `ask`: its preview, if it writes anything, first.
    Choose {
        ask: v1::Approval,
        option: String,
    },
    /// The preview was confirmed: answer, its rule written.
    Confirmed {
        ask: v1::Approval,
        option: String,
    },
    Quit,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Focus {
    Table,
    Asks,
}

/// An `allow-always` shown and waiting for a yes.
struct Pending {
    ask: v1::Approval,
    option: String,
    plan: AlwaysPlan,
}

/// The screen's state.
pub struct Screen {
    scope: String,
    /// The watch's rows, by name.
    sessions: BTreeMap<String, v1::Session>,
    /// The asks waiting, by name; the settled ones the person was told of.
    asks: BTreeMap<String, v1::Approval>,
    settled: BTreeMap<String, v1::Approval>,
    /// Every ask the person has been shown or told of: a bell is for an ask
    /// not in it, once.
    seen_asks: BTreeSet<String>,
    /// The icon of the ask state, the display's to choose.
    icon: &'static str,
    /// A new ask arrived since the bell was last taken.
    bell: bool,
    /// The log of the session the watch carries one for, oldest first.
    logs: BTreeMap<String, Vec<v1::TopLog>>,
    waiting: bool,
    /// Whether the watch has said anything, or been found absent: until
    /// then there is nothing true to draw.
    heard: bool,
    selected: Option<String>,
    /// The selected session that left the view and is kept, ended, until
    /// the selection moves.
    lingering: Option<String>,
    focus: Focus,
    ask_cursor: usize,
    /// Typed filters: field to value.
    filters: BTreeMap<String, String>,
    /// None: owners with their children, indented.
    sort: Option<Sort>,
    prompt: Option<String>,
    pending: Option<Pending>,
    notice: Option<String>,
}

/// A screen's drawing: its lines in sections.
#[derive(Debug, PartialEq)]
pub struct Frame {
    pub header: String,
    /// The status line and a pending confirmation, when there are any.
    pub status: Vec<String>,
    pub columns: String,
    pub rows: Vec<String>,
    /// The row marked, an index into `rows`.
    pub selected: Option<usize>,
    pub asks_title: String,
    pub asks: Vec<String>,
    pub log_title: String,
    pub log: Vec<String>,
    /// The rows (indexes into `rows`) an ask waits on: painted highlighted.
    pub alerts: Vec<usize>,
    /// Whether this frame is drawn for a new ask: the terminal rings.
    pub bell: bool,
}

impl Frame {
    /// The frame as the seam writes it: plain lines, ended by a line `---`.
    pub fn text(&self) -> String {
        let mut lines = vec![self.header.clone()];
        lines.extend(self.status.iter().cloned());
        if !self.alerts.is_empty() {
            let names: Vec<&str> = self
                .alerts
                .iter()
                .filter_map(|&at| self.rows.get(at)?.split_whitespace().next())
                .collect();
            lines.push(format!("highlighted: {}", names.join(", ")));
        }
        if self.bell {
            lines.push("bell".to_string());
        }
        lines.push(format!("  {}", self.columns));
        for (at, row) in self.rows.iter().enumerate() {
            let mark = if self.selected == Some(at) {
                "> "
            } else {
                "  "
            };
            lines.push(format!("{mark}{row}"));
        }
        lines.push(self.asks_title.clone());
        lines.extend(self.asks.iter().cloned());
        lines.push(self.log_title.clone());
        lines.extend(self.log.iter().cloned());
        lines.push("---".to_string());
        lines.join("\n") + "\n"
    }
}

/// How many terminal columns `text` takes: a wide character (the ask
/// icon) is two, as the display counts them.
fn columns_of(text: &str) -> usize {
    ratatui::text::Span::raw(text).width()
}

fn last(name: &str) -> &str {
    name.rsplit('/').next().unwrap_or_default()
}

fn state_word(session: &v1::Session) -> String {
    v1::session::State::try_from(session.state)
        .map(|state| format!("{state:?}").to_lowercase())
        .unwrap_or_default()
}

impl Screen {
    /// A screen of `scope` (a words for the header), sorted by `sort`, none
    /// meaning the owner tree.
    pub fn new(scope: &str, sort: Option<Sort>) -> Screen {
        Screen {
            scope: scope.to_string(),
            sessions: BTreeMap::new(),
            asks: BTreeMap::new(),
            settled: BTreeMap::new(),
            seen_asks: BTreeSet::new(),
            icon: ICON_ASCII,
            bell: false,
            logs: BTreeMap::new(),
            waiting: false,
            heard: false,
            selected: None,
            lingering: None,
            focus: Focus::Table,
            ask_cursor: 0,
            filters: BTreeMap::new(),
            sort,
            prompt: None,
            pending: None,
            notice: None,
        }
    }

    /// The screen with `icon` before `ASK`: the display says which its
    /// terminal can draw; the seam keeps [`ICON_ASCII`].
    pub fn with_icon(mut self, icon: &'static str) -> Screen {
        self.icon = icon;
        self
    }

    /// No daemon serves (again).
    pub fn wait(&mut self) {
        self.waiting = true;
        self.heard = true;
    }

    /// Whether the screen has something to draw.
    pub fn ready(&self) -> bool {
        self.heard
    }

    /// Whether a new ask arrived since the last call; taken once.
    pub fn take_bell(&mut self) -> bool {
        std::mem::take(&mut self.bell)
    }

    /// Say `text` on the status line.
    pub fn say(&mut self, text: &str) {
        self.notice = Some(text.to_string());
    }

    /// An `allow-always`'s preview to confirm.
    pub fn confirm(&mut self, ask: v1::Approval, option: &str, plan: AlwaysPlan) {
        self.pending = Some(Pending {
            ask,
            option: option.to_string(),
            plan,
        });
    }

    /// Take in what the watch said.
    pub fn apply(&mut self, event: v1::TopEvent) {
        use v1::top_event::Event;
        let Some(event) = event.event else { return };
        match event {
            Event::Waiting(_) => self.wait(),
            Event::Snapshot(snapshot) => {
                self.waiting = false;
                self.heard = true;
                self.sessions = snapshot
                    .sessions
                    .into_iter()
                    .map(|session| (session.name.clone(), session))
                    .collect();
                self.asks = snapshot
                    .approvals
                    .into_iter()
                    .map(|ask| (ask.name.clone(), ask))
                    .collect();
                // The asks of a snapshot (the first, or the next daemon's) are
                // what the person finds, not news.
                self.seen_asks.extend(self.asks.keys().cloned());
                self.logs.clear();
                self.lingering = None;
            }
            Event::SessionChanged(changed) => {
                if let Some(session) = changed.session {
                    if self.lingering.as_ref() == Some(&session.name) {
                        self.lingering = None;
                    }
                    self.sessions.insert(session.name.clone(), session);
                }
            }
            Event::SessionRemoved(removed) => self.removed(&removed),
            Event::ApprovalAsked(asked) => {
                if let Some(ask) = asked.approval {
                    let waiting = ask.state == "waiting" || ask.state.is_empty();
                    if self.seen_asks.insert(ask.name.clone()) && waiting {
                        self.bell = true;
                    }
                    self.asks.insert(ask.name.clone(), ask);
                }
            }
            Event::ApprovalSettled(settled) => {
                if let Some(ask) = settled.approval {
                    if self
                        .pending
                        .as_ref()
                        .is_some_and(|pending| pending.ask.name == ask.name)
                    {
                        self.pending = None;
                        self.notice = Some(format!(
                            "{} was settled meanwhile: {}",
                            last(&ask.name),
                            ask.state
                        ));
                    }
                    self.asks.remove(&ask.name);
                    self.settled.insert(ask.name.clone(), ask);
                }
            }
            Event::Log(log) => self.logs.entry(log.session.clone()).or_default().push(log),
        }
    }

    /// A session the watch took out of the view. The one the selection is on
    /// that only left the filter (its run ended) stays, marked ended, until
    /// the selection moves, so its last words can be read where it was; every
    /// other, and one destroyed, goes.
    fn removed(&mut self, removed: &v1::TopSessionRemoved) {
        let left_filter = removed.reason == v1::top_session_removed::Reason::LeftFilter as i32;
        let selected = self.selected_name().as_deref() == Some(removed.name.as_str());
        match self.sessions.get_mut(&removed.name) {
            Some(session) if left_filter && selected => {
                session.state = v1::session::State::Ended as i32;
                self.lingering = Some(removed.name.clone());
            }
            _ => {
                self.sessions.remove(&removed.name);
                if self.lingering.as_ref() == Some(&removed.name) {
                    self.lingering = None;
                }
            }
        }
    }

    fn visible(&self, session: &v1::Session) -> bool {
        self.filters.iter().all(|(field, value)| {
            let has = |values: &[String]| values.iter().any(|v| v == value);
            match field.as_str() {
                "agent" => session.agent == *value,
                "engine" => session.engine == *value,
                "profile" => has(&session.profiles),
                "tag" => has(&session.tags),
                "owner" => last(&session.owner) == value,
                "state" => state_word(session) == *value,
                _ => true,
            }
        })
    }

    /// The rows in view, in order, each with its depth.
    fn rows(&self) -> Vec<(usize, &v1::Session)> {
        let mut in_view: Vec<&v1::Session> = self
            .sessions
            .values()
            .filter(|session| self.visible(session))
            .collect();
        if let Some(sort) = &self.sort {
            let mut owned: Vec<v1::Session> = in_view.iter().map(|s| (*s).clone()).collect();
            sort.order(&mut owned);
            let names: Vec<String> = owned.into_iter().map(|s| s.name).collect();
            return names
                .iter()
                .filter_map(|name| self.sessions.get(name))
                .map(|session| (0, session))
                .collect();
        }
        in_view.sort_by_key(|session| {
            std::cmp::Reverse(session.start_time.as_ref().map_or(0, |at| at.seconds))
        });
        let names: Vec<&str> = in_view.iter().map(|s| s.name.as_str()).collect();
        let mut rows = Vec::new();
        let roots = in_view
            .iter()
            .filter(|s| s.owner.is_empty() || !names.contains(&s.owner.as_str()));
        for root in roots {
            Self::descend(&in_view, root, 0, &mut rows);
        }
        rows
    }

    fn descend<'s>(
        all: &[&'s v1::Session],
        at: &'s v1::Session,
        depth: usize,
        rows: &mut Vec<(usize, &'s v1::Session)>,
    ) {
        rows.push((depth, at));
        let mut kids: Vec<&&v1::Session> = all.iter().filter(|s| s.owner == at.name).collect();
        kids.sort_by_key(|s| s.start_time.as_ref().map_or(0, |t| t.seconds));
        for kid in kids {
            Self::descend(all, kid, depth + 1, rows);
        }
    }

    /// The name of the session the selection is on: the one chosen if it
    /// is in view, else the first row.
    pub fn selected_name(&self) -> Option<String> {
        let rows = self.rows();
        rows.iter()
            .find(|(_, s)| Some(&s.name) == self.selected.as_ref())
            .or(rows.first())
            .map(|(_, s)| s.name.clone())
    }

    fn waiting_asks(&self, session: &str) -> Vec<&v1::Approval> {
        self.asks
            .values()
            .filter(|ask| ask.child == session)
            .collect()
    }

    /// The asks section's asks for `session`: waiting, then settled.
    fn shown_asks(&self, session: &str) -> Vec<&v1::Approval> {
        let mut asks = self.waiting_asks(session);
        asks.extend(
            self.settled
                .values()
                .filter(|ask| ask.child == session && !self.asks.contains_key(&ask.name)),
        );
        asks
    }

    /// The ask the digit keys answer: the cursor's among the waiting ones.
    fn ask_in_focus(&self, session: &str) -> Option<&v1::Approval> {
        let waiting = self.waiting_asks(session);
        waiting
            .get(self.ask_cursor.min(waiting.len().saturating_sub(1)))
            .copied()
    }

    /// Whether the filter prompt is open.
    pub fn prompting(&self) -> bool {
        self.prompt.is_some()
    }

    /// Take a key.
    pub fn key(&mut self, key: Key) -> Option<Effect> {
        self.notice = None;
        if let Some(typed) = self.prompt.take() {
            return self.typing(typed, key);
        }
        if let Some(pending) = self.pending.take() {
            return match key {
                Key::Char('y') => Some(Effect::Confirmed {
                    ask: pending.ask,
                    option: pending.option,
                }),
                Key::Char('n') | Key::Esc => {
                    self.notice = Some("not answered".to_string());
                    None
                }
                _ => {
                    self.pending = Some(pending);
                    None
                }
            };
        }
        let before = self.selected_name();
        match key {
            Key::Char('q') => Some(Effect::Quit),
            Key::Tab => {
                self.focus = match self.focus {
                    Focus::Table => Focus::Asks,
                    Focus::Asks => Focus::Table,
                };
                None
            }
            Key::Down | Key::Up if self.focus == Focus::Asks => {
                let count = before
                    .as_deref()
                    .map_or(0, |name| self.waiting_asks(name).len());
                self.ask_cursor = match key {
                    Key::Down => (self.ask_cursor + 1).min(count.saturating_sub(1)),
                    _ => self.ask_cursor.saturating_sub(1),
                };
                None
            }
            Key::Down | Key::Up => {
                self.move_selection(key == Key::Down);
                None
            }
            Key::Digit(n) => self.chosen(before.as_deref(), usize::from(n)),
            Key::Char('s') => {
                self.sort = Some(match &self.sort {
                    Some(sort) => sort.next(),
                    None => Sort::first(),
                });
                None
            }
            Key::Char('r') => {
                self.sort = Some(match &self.sort {
                    Some(sort) => sort.reversed(),
                    None => Sort::first().reversed(),
                });
                None
            }
            Key::Char('f') => {
                self.prompt = Some(String::new());
                None
            }
            _ => None,
        }
    }

    fn chosen(&mut self, session: Option<&str>, nth: usize) -> Option<Effect> {
        let session = session?;
        let Some(ask) = self.ask_in_focus(session).cloned() else {
            // Nothing waits: if an ask was settled meanwhile (elsewhere), say
            // how, rather than leaving the key unanswered.
            if let Some(settled) = self.shown_asks(session).first() {
                let how = match settled.option.is_empty() {
                    true => settled.state.clone(),
                    false => format!("{} ({})", settled.state, settled.option),
                };
                self.notice = Some(format!(
                    "{} was already settled, {how}",
                    last(&settled.name)
                ));
            }
            return None;
        };
        let option = ask.options.get(nth - 1)?.id.clone();
        Some(Effect::Choose { ask, option })
    }

    fn move_selection(&mut self, down: bool) {
        let rows = self.rows();
        let at = rows
            .iter()
            .position(|(_, s)| Some(&s.name) == self.selected_name().as_ref())
            .unwrap_or(0);
        let to = match down {
            true => (at + 1).min(rows.len().saturating_sub(1)),
            false => at.saturating_sub(1),
        };
        self.selected = rows.get(to).map(|(_, s)| s.name.clone());
        self.ask_cursor = 0;
        if let Some(gone) = self.lingering.take() {
            if self.selected.as_ref() != Some(&gone) {
                self.sessions.remove(&gone);
            } else {
                self.lingering = Some(gone);
            }
        }
    }

    /// A key while the filter prompt is open.
    fn typing(&mut self, mut typed: String, key: Key) -> Option<Effect> {
        match key {
            Key::Char(c) => typed.push(c),
            Key::Backspace => {
                typed.pop();
            }
            Key::Text(line) => return self.filter(&line),
            Key::Enter => return self.filter(&typed),
            Key::Esc => return None,
            _ => {}
        }
        self.prompt = Some(typed);
        None
    }

    fn filter(&mut self, line: &str) -> Option<Effect> {
        match line.split_once('=') {
            Some((field, value)) if !value.is_empty() => {
                self.filters
                    .insert(field.trim().to_string(), value.trim().to_string());
            }
            _ => self.filters.clear(),
        }
        None
    }

    fn header(&self) -> String {
        let filter = match self.filters.is_empty() {
            true => "none".to_string(),
            false => self
                .filters
                .iter()
                .map(|(field, value)| format!("{field}={value}"))
                .collect::<Vec<_>>()
                .join(","),
        };
        let sort = self.sort.as_ref().map_or("tree".to_string(), Sort::word);
        format!(
            "ctxloom top  scope: {}  filter: {filter}  sort: {sort}",
            self.scope
        )
    }

    fn cells(&self, depth: usize, session: &v1::Session) -> Vec<String> {
        vec![
            format!("{}{}", "  ".repeat(depth), last(&session.name)),
            self.state_cell(session),
            session.agent.clone(),
            session.profiles.join(","),
            session.tags.join(","),
            session.engine.clone(),
            last(&session.owner).to_string(),
        ]
    }

    /// The asks waiting on a session that is live: an ended session's row
    /// says it ended, whatever it left unanswered.
    fn asking(&self, session: &v1::Session) -> usize {
        match v1::session::State::try_from(session.state) {
            Ok(v1::session::State::Live) => self.waiting_asks(&session.name).len(),
            _ => 0,
        }
    }

    /// A row's state: its own, or `ASK` (and the count when several wait)
    /// while an ask waits on it. Plain words and a glyph: the colour is the
    /// display's.
    fn state_cell(&self, session: &v1::Session) -> String {
        match self.asking(session) {
            0 => state_word(session),
            1 => format!("{} ASK", self.icon),
            waiting => format!("{} ASK x{waiting}", self.icon),
        }
    }

    /// The frame of the screen as it stands.
    pub fn frame(&self) -> Frame {
        let rows = self.rows();
        let table: Vec<Vec<String>> = rows
            .iter()
            .map(|(depth, session)| self.cells(*depth, session))
            .collect();
        let widths: Vec<usize> = (0..HEADER.len())
            .map(|c| {
                table
                    .iter()
                    .map(|row| columns_of(&row[c]))
                    .chain([HEADER[c].len()])
                    .max()
                    .unwrap_or(0)
            })
            .collect();
        let line = |cells: &[String]| {
            cells
                .iter()
                .zip(&widths)
                .map(|(cell, width)| {
                    format!(
                        "{cell}{}",
                        " ".repeat(width.saturating_sub(columns_of(cell)))
                    )
                })
                .collect::<Vec<_>>()
                .join(" ")
                .trim_end()
                .to_string()
        };
        let selected_name = self.selected_name();
        let selected = rows
            .iter()
            .position(|(_, s)| Some(&s.name) == selected_name.as_ref());
        let alerts = rows
            .iter()
            .enumerate()
            .filter(|(_, (_, session))| self.asking(session) > 0)
            .map(|(at, _)| at)
            .collect();
        let harp = selected_name.as_deref().map(last).unwrap_or_default();
        let mut status = Vec::new();
        if self.waiting {
            status.push(WAITING.to_string());
        }
        if let Some(pending) = &self.pending {
            status.extend(self.preview(pending));
        }
        if let Some(typed) = &self.prompt {
            status.push(format!("filter (field=value): {typed}"));
        }
        if let Some(notice) = &self.notice {
            status.push(notice.clone());
        }
        let asks = selected_name
            .as_deref()
            .map(|name| self.ask_lines(name))
            .unwrap_or_default();
        let log = selected_name
            .as_deref()
            .and_then(|name| self.logs.get(name))
            .map(|entries| {
                let session = selected_name.as_deref().and_then(|n| self.sessions.get(n));
                let of = Of {
                    project_root: session.map_or("", |s| s.project_root.as_str()),
                    engine: session.map_or("", |s| s.engine.as_str()),
                };
                entries.iter().map(|entry| log_line(entry, &of)).collect()
            })
            .unwrap_or_default();
        Frame {
            header: self.header(),
            status,
            columns: line(&HEADER.map(String::from)),
            rows: table.iter().map(|cells| line(cells)).collect(),
            selected,
            asks_title: format!("--- asks of {harp}"),
            asks,
            log_title: format!("--- log of {harp}"),
            log,
            alerts,
            bell: false,
        }
    }

    fn preview(&self, pending: &Pending) -> Vec<String> {
        vec![
            format!(
                "allow-always would write {} to {}",
                pending.plan.rule, pending.plan.path
            ),
            format!("  {}: {}", pending.plan.key, pending.plan.document),
            "answer? [y/N]".to_string(),
        ]
    }

    fn ask_lines(&self, session: &str) -> Vec<String> {
        let mut lines = Vec::new();
        for ask in self.shown_asks(session) {
            let name = last(&ask.name);
            if ask.state == "waiting" || ask.state.is_empty() {
                lines.push(format!("  {name} {} {}", ask.tool_name, ask.input_excerpt));
                for (at, option) in ask.options.iter().enumerate() {
                    lines.push(format!("    {} {}", at + 1, option.id));
                }
            } else {
                let how = match ask.option.is_empty() {
                    true => ask.state.clone(),
                    false => format!("{} {}", ask.state, ask.option),
                };
                lines.push(format!(
                    "  {name} {} {}: {how}",
                    ask.tool_name, ask.input_excerpt
                ));
            }
        }
        lines
    }
}

/// A log entry as a line: its time, then what it says (plain words).
fn log_line(log: &v1::TopLog, of: &Of<'_>) -> String {
    let seconds = log
        .time
        .as_ref()
        .map_or(0, |t| t.seconds)
        .rem_euclid(86_400);
    let clock = format!(
        "{:02}:{:02}:{:02}",
        seconds / 3600,
        seconds % 3600 / 60,
        seconds % 60
    );
    let said = log
        .entry
        .as_ref()
        .map_or_else(String::new, |entry| text_of(entry, of));
    format!("  {clock} {said}")
}

#[cfg(test)]
#[path = "screen.test.rs"]
mod tests;
