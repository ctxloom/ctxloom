//! What the terminal's painting and keys come to, on ratatui's test
//! backend: no terminal is taken.

use crossterm::event::{KeyCode, KeyModifiers};
use ratatui::backend::TestBackend;
use ratatui::style::{Color, Modifier};
use ratatui::Terminal;

use super::{colour_wanted, named, paint, utf8_wanted, Look};
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
        alerts: vec![],
        bell: false,
    }
}

fn painted(width: u16, height: u16) -> Vec<String> {
    let mut terminal = Terminal::new(TestBackend::new(width, height)).expect("a test terminal");
    terminal
        .draw(|screen| paint(screen, &frame(), true))
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

#[test]
fn test_a_row_with_an_ask_waiting_is_painted_red_and_bold_and_the_others_are_not() {
    let buffer = painted_buffer(true);
    let (amber, brisk) = (row_of(&buffer, "amber live"), row_of(&buffer, "brisk live"));
    assert_eq!(buffer[(4, amber)].fg, Color::Red);
    assert!(buffer[(4, amber)].modifier.contains(Modifier::BOLD));
    assert_ne!(buffer[(4, brisk)].fg, Color::Red);
}

fn painted_buffer(colour: bool) -> ratatui::buffer::Buffer {
    let mut alerted = frame();
    alerted.alerts = vec![0];
    let mut terminal = Terminal::new(TestBackend::new(80, 20)).expect("a test terminal");
    terminal
        .draw(|screen| paint(screen, &alerted, colour))
        .expect("it paints");
    terminal.backend().buffer().clone()
}

fn row_of(buffer: &ratatui::buffer::Buffer, text: &str) -> u16 {
    (0..20)
        .find(|&y| {
            (0..80)
                .map(|x| buffer[(x, y)].symbol())
                .collect::<String>()
                .contains(text)
        })
        .unwrap_or_else(|| panic!("no row {text}"))
}

#[test]
fn test_without_colour_nothing_is_painted_in_a_colour_and_the_row_still_stands_out() {
    let buffer = painted_buffer(false);
    let amber = row_of(&buffer, "amber live");
    for y in 0..20 {
        for x in 0..80 {
            assert_eq!(buffer[(x, y)].fg, Color::Reset, "colour at {x},{y}");
        }
    }
    assert!(buffer[(4, amber)].modifier.contains(Modifier::BOLD));
}

fn env(pairs: &'static [(&'static str, &'static str)]) -> impl Fn(&str) -> Option<String> {
    move |name| {
        pairs
            .iter()
            .find(|(k, _)| *k == name)
            .map(|(_, v)| (*v).to_string())
    }
}

#[test]
fn test_colour_is_wanted_on_a_terminal_that_has_it() {
    assert!(colour_wanted(env(&[("TERM", "xterm-256color")])));
}

#[test]
fn test_no_colour_when_no_color_is_set_to_anything_non_empty() {
    assert!(!colour_wanted(env(&[("TERM", "xterm"), ("NO_COLOR", "1")])));
}

#[test]
fn test_an_empty_no_color_does_not_turn_colour_off() {
    assert!(colour_wanted(env(&[("TERM", "xterm"), ("NO_COLOR", "")])));
}

#[test]
fn test_no_colour_on_a_dumb_terminal_or_with_no_term() {
    assert!(!colour_wanted(env(&[("TERM", "dumb")])));
    assert!(!colour_wanted(env(&[])));
}

#[test]
fn test_a_utf8_locale_is_found_by_lc_all_then_lc_ctype_then_lang() {
    assert!(utf8_wanted(env(&[("LANG", "en_US.UTF-8")])));
    assert!(utf8_wanted(env(&[("LC_CTYPE", "C.utf8")])));
    assert!(utf8_wanted(env(&[("LC_ALL", "de_DE.Utf-8")])));
}

#[test]
fn test_the_first_locale_variable_set_decides() {
    assert!(!utf8_wanted(env(&[
        ("LC_ALL", "C"),
        ("LANG", "en_US.UTF-8")
    ])));
    assert!(utf8_wanted(env(&[("LC_ALL", ""), ("LANG", "en_US.UTF-8")])));
    assert!(!utf8_wanted(env(&[
        ("LC_CTYPE", "POSIX"),
        ("LANG", "en_US.UTF-8")
    ])));
}

#[test]
fn test_no_locale_or_a_non_utf8_one_is_not_utf8() {
    assert!(!utf8_wanted(env(&[])));
    assert!(!utf8_wanted(env(&[("LANG", "en_US.ISO-8859-1")])));
}

#[test]
fn test_the_look_is_each_part_judged_alone() {
    let look = Look::of(env(&[
        ("TERM", "xterm"),
        ("NO_COLOR", "1"),
        ("LANG", "C.UTF-8"),
    ]));
    assert_eq!(
        look,
        Look {
            colour: false,
            utf8: true
        }
    );
    assert_eq!(
        Look::default(),
        Look {
            colour: false,
            utf8: false
        }
    );
}
