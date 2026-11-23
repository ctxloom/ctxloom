//! The terminal form of the screen: the frame painted with ratatui on the
//! alternate screen, keys read with crossterm. It paints the lines the
//! model's [`Frame`] holds and decides nothing; the model and the loop
//! are `screen` and `screen_run`.

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::{channel, Receiver};
use std::sync::Arc;
use std::time::Duration;

use crossterm::event::{self, Event, KeyCode, KeyEventKind, KeyModifiers};
use ratatui::layout::{Constraint, Layout};
use ratatui::style::{Modifier, Style};
use ratatui::text::Line;
use ratatui::widgets::Paragraph;
use ratatui::DefaultTerminal;

use crate::run::Failure;
use crate::screen::{Frame, Key};
use crate::screen_run::{drawing, Display, Input};

/// How long a key read waits before it looks at whether it is to stop.
const LOOK: Duration = Duration::from_millis(100);

/// A terminal taken for the screen: raw mode and the alternate screen, let
/// go when it is dropped, however the screen ended.
pub struct Tui {
    terminal: DefaultTerminal,
}

impl Tui {
    /// Take the terminal over.
    pub fn take() -> Result<Tui, Failure> {
        let terminal = ratatui::try_init().map_err(|e| drawing(&e))?;
        Ok(Tui { terminal })
    }
}

impl Drop for Tui {
    // Puts the person's terminal back; nothing a unit test can observe.
    #[cfg_attr(test, mutants::skip)]
    fn drop(&mut self) {
        ratatui::restore();
    }
}

impl Display for Tui {
    fn draw(&mut self, frame: &Frame) -> Result<(), Failure> {
        self.terminal
            .draw(|screen| paint(screen, frame))
            .map(|_| ())
            .map_err(|e| drawing(&e))
    }
}

/// `frame` on `screen`: the header and status, the table above and the
/// reading pane below, its newest log lines kept in view.
fn paint(screen: &mut ratatui::Frame<'_>, frame: &Frame) {
    let top = (frame.rows.len() + 2).min(usize::from(screen.area().height) * 2 / 5) as u16;
    let [header, table, pane] = Layout::vertical([
        Constraint::Length(1 + frame.status.len() as u16),
        Constraint::Length(top.max(2)),
        Constraint::Min(1),
    ])
    .areas(screen.area());
    let mut head = vec![Line::styled(
        frame.header.clone(),
        Style::default().add_modifier(Modifier::BOLD),
    )];
    head.extend(frame.status.iter().cloned().map(Line::from));
    screen.render_widget(Paragraph::new(head), header);

    let mut rows = vec![Line::styled(
        format!("  {}", frame.columns),
        Style::default().add_modifier(Modifier::UNDERLINED),
    )];
    for (at, row) in frame.rows.iter().enumerate() {
        let chosen = frame.selected == Some(at);
        let style = match chosen {
            true => Style::default().add_modifier(Modifier::REVERSED),
            false => Style::default(),
        };
        let mark = if chosen { "> " } else { "  " };
        rows.push(Line::styled(format!("{mark}{row}"), style));
    }
    let kept = usize::from(table.height).saturating_sub(1);
    let skip = frame.selected.map_or(0, |at| (at + 1).saturating_sub(kept));
    let shown: Vec<Line> = rows
        .into_iter()
        .enumerate()
        .filter(|(at, _)| *at == 0 || *at > skip)
        .map(|(_, line)| line)
        .collect();
    screen.render_widget(Paragraph::new(shown), table);

    // The asks keep their place; the log takes what is left and shows its
    // newest lines.
    let asks_height = (1 + frame.asks.len())
        .min(usize::from(pane.height) / 2)
        .max(1) as u16;
    let [asks_area, log_area] =
        Layout::vertical([Constraint::Length(asks_height), Constraint::Min(1)]).areas(pane);
    let mut asks = vec![Line::styled(
        frame.asks_title.clone(),
        Style::default().add_modifier(Modifier::BOLD),
    )];
    asks.extend(frame.asks.iter().cloned().map(Line::from));
    screen.render_widget(Paragraph::new(asks), asks_area);
    let mut log = vec![Line::styled(
        frame.log_title.clone(),
        Style::default().add_modifier(Modifier::BOLD),
    )];
    let room = usize::from(log_area.height).saturating_sub(1);
    let hidden = frame.log.len().saturating_sub(room);
    log.extend(frame.log.iter().skip(hidden).cloned().map(Line::from));
    screen.render_widget(Paragraph::new(log), log_area);
}

/// The keys of the terminal, read on a thread of their own until `stop`.
pub fn keys(stop: &Arc<AtomicBool>) -> Receiver<Input> {
    let (send, keys) = channel();
    let stop = Arc::clone(stop);
    std::thread::spawn(move || {
        while !stop.load(Ordering::Relaxed) {
            match event::poll(LOOK) {
                Ok(true) => {}
                Ok(false) => continue,
                Err(_) => return,
            }
            let Ok(Event::Key(key)) = event::read() else {
                continue;
            };
            if key.kind == KeyEventKind::Release {
                continue;
            }
            let Some(key) = named(key.code, key.modifiers) else {
                continue;
            };
            if send.send(Input::Key(key)).is_err() {
                return;
            }
        }
    });
    keys
}

/// The screen's key for a terminal's.
fn named(code: KeyCode, modifiers: KeyModifiers) -> Option<Key> {
    Some(match code {
        KeyCode::Char('c') if modifiers.contains(KeyModifiers::CONTROL) => Key::Char('q'),
        KeyCode::Char(c) => match c.to_digit(10) {
            Some(n @ 1..=9) => Key::Digit(n as u8),
            _ => Key::Char(c),
        },
        KeyCode::Down => Key::Down,
        KeyCode::Up => Key::Up,
        KeyCode::Tab => Key::Tab,
        KeyCode::Enter => Key::Enter,
        KeyCode::Backspace => Key::Backspace,
        KeyCode::Esc => Key::Esc,
        _ => return None,
    })
}

#[cfg(test)]
#[path = "tui.test.rs"]
mod tests;
