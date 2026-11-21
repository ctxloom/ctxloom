use prost::Message;

use super::*;

const BEARER: &str = "4f1c2a7b9e0d3c6a5b8f7e1d2c3b4a5968778695a4b3c2d1e0f1a2b3c4d5e6f7";

fn sample() -> Discovery {
    Discovery {
        stamp: "v0.1.0-abc1234".to_string(),
        pid: 4242,
        endpoint: "https://127.0.0.1:40123".to_string(),
        cert: PublicCert::from_der(vec![0x30, 0x03, 0x01, 0x02, 0x03]).unwrap(),
        bearer: Token::from_text(BEARER).unwrap(),
        home: "/home/u".to_string(),
    }
}

fn wire() -> daemon_proto::v1::Discovery {
    daemon_proto::v1::Discovery {
        format: 1,
        pid: 4242,
        endpoint: "https://127.0.0.1:40123".to_string(),
        certificate: vec![0x30, 0x03, 0x01, 0x02, 0x03],
        bearer: BEARER.to_string(),
        started_at: None,
        stamp: "v0.1.0-abc1234".to_string(),
        home: "/home/u".to_string(),
    }
}

// The file is the proto message, in protobuf's binary encoding (S5).
#[test]
fn a_discovery_is_the_proto_message_and_round_trips() {
    let bytes = sample().encode();
    assert_eq!(
        daemon_proto::v1::Discovery::decode(bytes.as_slice()).unwrap(),
        wire()
    );
    assert_eq!(Discovery::decode(&bytes), Some(sample()));
    assert_eq!(Discovery::decode(&wire().encode_to_vec()), Some(sample()));
}

#[test]
fn anything_unreadable_is_no_discovery() {
    let with = |change: fn(&mut daemon_proto::v1::Discovery)| {
        let mut message = wire();
        change(&mut message);
        message.encode_to_vec()
    };
    for bad in [
        Vec::new(),
        vec![0xff, 0xff, 0xff],
        with(|m| m.format = 0),
        with(|m| m.format = 2),
        with(|m| m.endpoint = "http://127.0.0.1:40123".to_string()),
        with(|m| m.endpoint = "https://127.0.0.1:0".to_string()),
        with(|m| m.endpoint = String::new()),
        with(|m| m.certificate = Vec::new()),
        with(|m| m.bearer = String::new()),
        with(|m| m.bearer = "not hex".to_string()),
        with(|m| m.bearer = BEARER.to_uppercase()),
        with(|m| m.stamp = String::new()),
        // One daemon per OS account: a file naming no home is unreadable.
        with(|m| m.home = String::new()),
    ] {
        assert_eq!(Discovery::decode(&bad), None, "{bad:?}");
    }
}

// The bearer is the user's: a Debug never shows it.
#[test]
fn a_discoverys_debug_withholds_the_bearer() {
    let shown = format!("{:?}", sample());
    assert!(!shown.contains(BEARER), "{shown}");
    assert!(shown.contains("redacted"), "{shown}");
    assert!(shown.contains("v0.1.0-abc1234"), "{shown}");
    assert!(shown.contains("https://127.0.0.1:40123"), "{shown}");
}

#[test]
fn the_daemons_certificate_lasts_about_a_month() {
    assert_eq!(DAEMON_CERT_LIFETIME_SECONDS, 2_592_000);
}

#[test]
fn the_files_live_under_the_ctxloom_home() {
    assert_eq!(DAEMON_DIR, ".ctxloom/daemon");
    assert_eq!(DAEMON_LOCK_FILE, "daemon.lock");
    assert_eq!(DISCOVERY_FILE, "daemon.discovery");
    assert_eq!(DISCOVERY_FORMAT, 1);
}
