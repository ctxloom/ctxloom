//! What the terminal's painting and keys come to, on ratatui's test
//! backend: no terminal is taken.

use crossterm::event::{KeyCode, KeyModifiers};
use ratatui::backend::TestBackend;
use ratatui::Terminal;

use super::{named, paint};
use crate::screen::{Frame, Key};

fn frame() -> Frame {
    Frame {
        header: "ctxloom top  scope: this project  filter: none  sort: tree".to_string(),
        status: vec![],
        columns: "NAME STATE".to_string(),
        rows: vec!["amber live".to_string(), "brisk live".to_string()],
        selected: Some(1),
        asks_title: "--- asks of brisk".to_string(),
        asks: vec![],
        log_title: "--- log of brisk".to_string(),
        log: (0..30).map(|n| format!("  entry {n}")).collect(),
    }
}

fn painted(width: u16, height: u16) -> Vec<String> {
    let mut terminal = Terminal::new(TestBackend::new(width, height)).expect("a test terminal");
    terminal
        .draw(|screen| paint(screen, &frame()))
        .expect("it paints");
    let buffer = terminal.backend().buffer().clone();
    (0..height)
        .map(|y| {
            (0..width)
                .map(|x| buffer[(x, y)].symbol().to_string())
                .collect::<String>()
                .trim_end()
                .to_string()
        })
        .collect()
}

#[test]
fn the_table_is_above_the_pane_and_the_selected_row_is_marked() {
    let lines = painted(80, 20);
    let at = |needle: &str| lines.iter().position(|l| l.contains(needle));
    assert!(at("NAME STATE") < at("--- asks of brisk"), "{lines:#?}");
    assert!(
        at("--- asks of brisk") < at("--- log of brisk"),
        "{lines:#?}"
    );
    assert!(
        lines.iter().any(|l| l.starts_with("> brisk live")),
        "{lines:#?}"
    );
    assert!(
        lines.iter().any(|l| l.starts_with("  amber live")),
        "{lines:#?}"
    );
}

#[test]
fn a_pane_too_short_for_the_log_shows_its_newest_lines() {
    let lines = painted(80, 14);
    assert!(lines.iter().any(|l| l.contains("entry 29")), "{lines:#?}");
    assert!(!lines.iter().any(|l| l.contains("entry 0 ")), "{lines:#?}");
}

#[test]
fn terminal_keys_are_the_screens() {
    let none = KeyModifiers::NONE;
    assert_eq!(named(KeyCode::Down, none), Some(Key::Down));
    assert_eq!(named(KeyCode::Char('2'), none), Some(Key::Digit(2)));
    assert_eq!(named(KeyCode::Char('q'), none), Some(Key::Char('q')));
    assert_eq!(
        named(KeyCode::Char('c'), KeyModifiers::CONTROL),
        Some(Key::Char('q'))
    );
    assert_eq!(named(KeyCode::F(5), none), None);
}
