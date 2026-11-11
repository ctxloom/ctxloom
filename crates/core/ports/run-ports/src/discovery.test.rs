use super::*;

fn sample() -> Discovery {
    Discovery {
        version: "0.1.0".to_string(),
        pid: 4242,
        endpoint: "https://127.0.0.1:40123".to_string(),
        cert: PublicCert::from_der(vec![0x30, 0x03, 0x01, 0x02, 0x03]).unwrap(),
    }
}

#[test]
fn a_discovery_round_trips_through_its_text() {
    let text = sample().encode();
    assert_eq!(
        text,
        "ctxloom-daemon 1\nversion 0.1.0\npid 4242\nendpoint https://127.0.0.1:40123\ncert MAMBAgM=\n"
    );
    assert_eq!(Discovery::decode(&text), Some(sample()));
}

#[test]
fn an_unknown_key_is_passed_over() {
    let text = format!("{}later something\n", sample().encode());
    assert_eq!(Discovery::decode(&text), Some(sample()));
}

#[test]
fn anything_unreadable_is_no_discovery() {
    let good = sample().encode();
    for bad in [
        String::new(),
        good.replace("ctxloom-daemon 1", "ctxloom-daemon 2"),
        good.replace("pid 4242\n", ""),
        good.replace("version 0.1.0\n", ""),
        good.replace("cert MAMBAgM=\n", ""),
        good.replace("endpoint https://127.0.0.1:40123\n", ""),
        good.replace("pid 4242", "pid many"),
        good.replace("https://", "http://"),
        good.replace(":40123", ":0"),
        good.replace("MAMBAgM=", "not base64!"),
        good.replace("pid 4242", "pid4242"),
    ] {
        assert_eq!(Discovery::decode(&bad), None, "{bad:?}");
    }
}

#[test]
fn the_daemons_certificate_lasts_about_a_month() {
    assert_eq!(DAEMON_CERT_LIFETIME_SECONDS, 2_592_000);
}

#[test]
fn the_files_live_under_the_ctxloom_home() {
    assert_eq!(DAEMON_DIR, ".ctxloom/daemon");
    assert_eq!(DAEMON_LOCK_FILE, "daemon.lock");
    assert_eq!(DISCOVERY_FILE, "daemon.endpoint");
}
