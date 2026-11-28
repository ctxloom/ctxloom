//! Settings through the kernel (`Harness`): a file's change is recorded
//! once, a re-read of it nothing.

use angzarr_embedded::markup::DomainMessage;
use angzarr_embedded::testing::{Harness, Samples};
use angzarr_embedded::v1::{command_page, CommandBook, CommandPage, Cover, Fact, FactOrigin};
use angzarr_embedded::{Any, Registry};

use crate::{
    settings_root, RecordSettingsObserved, SettingsAggregate, SettingsEvent, SettingsKind,
    SETTINGS_DOMAIN,
};

const CLAUDE: SettingsKind = SettingsKind::ClaudeSettings;
const OPENCODE: SettingsKind = SettingsKind::OpencodeConfig;

struct Settings {
    harness: Harness<SettingsAggregate>,
    /// Deliveries so far: each scan is its own fact, so the aggregate (not
    /// the kernel's duplicate check) is what drops an unchanged re-read.
    deliveries: u32,
}

impl Settings {
    fn new() -> Settings {
        let mut registry = Registry::new();
        registry
            .aggregate::<SettingsAggregate>(SettingsAggregate::default)
            .unwrap();
        let cover = Cover {
            domain: SETTINGS_DOMAIN.to_string(),
            root: Some(settings_root()),
            edition: None,
        };
        Settings {
            harness: Harness::new(registry, cover).with_samples(Samples::starting_at(100)),
            deliveries: 0,
        }
    }

    fn observe(&mut self, fact: RecordSettingsObserved) -> Vec<SettingsEvent> {
        self.deliveries += 1;
        let key = format!(
            "settings:{:?}:{}:{}",
            fact.kind, fact.content_id, self.deliveries
        );
        let any = Any::from_msg(&fact.to_wire()).unwrap();
        let book = CommandBook {
            cover: Some(self.cover()),
            page: Some(CommandPage {
                body: Some(command_page::Body::Fact(Fact {
                    payload: Some(any),
                    origin: Some(FactOrigin {
                        external_id: key,
                        description: String::new(),
                    }),
                })),
                ..CommandPage::default()
            }),
        };
        self.harness.when_book(book).events()
    }

    fn cover(&self) -> Cover {
        Cover {
            domain: SETTINGS_DOMAIN.to_string(),
            root: Some(settings_root()),
            edition: None,
        }
    }
}

fn file(kind: SettingsKind, id: &str, bytes: &str) -> RecordSettingsObserved {
    RecordSettingsObserved {
        kind,
        path: "/home/u/.claude/settings.json".to_string(),
        present: true,
        content_id: id.to_string(),
        content: bytes.as_bytes().to_vec(),
        sealed: Vec::new(),
    }
}

fn gone(kind: SettingsKind) -> RecordSettingsObserved {
    RecordSettingsObserved {
        kind,
        path: String::new(),
        present: false,
        content_id: String::new(),
        content: Vec::new(),
        sealed: Vec::new(),
    }
}

#[test]
fn a_file_seen_first_is_recorded_with_its_bytes() {
    let mut settings = Settings::new();
    let events = settings.observe(file(CLAUDE, "c1", "{}"));
    let [SettingsEvent::SettingsObserved(seen)] = &events[..] else {
        panic!("one SettingsObserved: {events:?}");
    };
    assert_eq!(
        (seen.content_id.as_str(), seen.content.as_slice()),
        ("c1", b"{}".as_slice())
    );
    let state = settings.harness.state();
    let held = &state.state().files[&CLAUDE];
    assert_eq!((held.sequence, held.content_id.as_str()), (0, "c1"));
}

#[test]
fn the_same_content_again_records_nothing() {
    let mut settings = Settings::new();
    settings.observe(file(CLAUDE, "c1", "{}"));
    // A second delivery under another key: the aggregate still adds nothing.
    let mut again = file(CLAUDE, "c1", "{}");
    again.path = "/elsewhere".to_string();
    assert!(settings.observe(again).is_empty());
}

#[test]
fn changed_content_is_recorded_at_its_own_sequence_per_kind() {
    let mut settings = Settings::new();
    settings.observe(file(CLAUDE, "c1", "{}"));
    settings.observe(file(OPENCODE, "o1", "{\"a\":1}"));
    let events = settings.observe(file(CLAUDE, "c2", "{\"b\":2}"));
    assert_eq!(events.len(), 1);
    let state = settings.harness.state();
    let files = &state.state().files;
    assert_eq!(files[&CLAUDE].content_id, "c2");
    assert_eq!(files[&CLAUDE].sequence, 2);
    assert_eq!(files[&OPENCODE].sequence, 1, "the other kind is untouched");
}

#[test]
fn a_file_that_goes_is_recorded_once_and_a_file_never_seen_absent_is_nothing() {
    let mut settings = Settings::new();
    assert!(settings.observe(gone(CLAUDE)).is_empty(), "never there");
    settings.observe(file(CLAUDE, "c1", "{}"));
    let events = settings.observe(gone(CLAUDE));
    let [SettingsEvent::SettingsObserved(seen)] = &events[..] else {
        panic!("one SettingsObserved: {events:?}");
    };
    assert!(!seen.present);
    assert!(!settings.harness.state().state().files[&CLAUDE].present);
    assert!(settings.observe(gone(CLAUDE)).is_empty(), "gone already");
}

#[test]
fn the_stream_root_is_one_fixed_name() {
    assert_eq!(settings_root(), settings_root());
    assert_eq!(crate::SETTINGS_NAME, "users/me/settings");
}
