use std::net::{IpAddr, Ipv4Addr, Ipv6Addr};

use angzarr_diagnostic::Diagnostic;
use run_ports::{CertMint, CertNames};

use crate::{validity, Clock, MintError, Minter, Validity, BACKDATE_SECONDS, LIFETIME_SECONDS};

/// 2030-01-01T00:00:00Z.
const NOW: i64 = 1_893_456_000;

#[derive(Debug)]
struct Fixed(i64);

impl Clock for Fixed {
    fn unix_seconds(&self) -> i64 {
        self.0
    }
}

fn names(ips: Vec<IpAddr>, dns: &[&str]) -> CertNames {
    CertNames::new(ips, dns.iter().map(|d| d.to_string()).collect())
}

fn loopback_names() -> CertNames {
    names(
        vec![
            IpAddr::V4(Ipv4Addr::LOCALHOST),
            IpAddr::V6(Ipv6Addr::LOCALHOST),
        ],
        &[],
    )
}

/// The `GeneralizedTime`/`UTCTime` texts of a certificate, in order: not
/// before, then not after (the only two times in a certificate rcgen makes).
fn times(der: &[u8]) -> Vec<String> {
    let mut found = Vec::new();
    let mut at = 0;
    while at + 15 <= der.len() {
        if der[at] == 0x17 && der[at + 1] == 13 && der[at + 14] == b'Z' {
            let text = &der[at + 2..at + 15];
            if text.iter().take(12).all(u8::is_ascii_digit) {
                found.push(String::from_utf8_lossy(text).into_owned());
                at += 15;
                continue;
            }
        }
        at += 1;
    }
    found
}

fn contains(haystack: &[u8], needle: &[u8]) -> bool {
    haystack.windows(needle.len()).any(|w| w == needle)
}

#[test]
fn the_window_starts_five_minutes_before_now_and_ends_a_day_after() {
    assert_eq!(BACKDATE_SECONDS, 300);
    assert_eq!(LIFETIME_SECONDS, 86_400);
    assert_eq!(
        validity(NOW),
        Validity {
            not_before: NOW - 300,
            not_after: NOW + 86_400
        }
    );
    assert_eq!(
        validity(0),
        Validity {
            not_before: -300,
            not_after: 86_400
        }
    );
}

#[test]
fn the_minted_certificate_carries_the_window_exactly() {
    let minted = Minter::new(Fixed(NOW)).mint(&loopback_names()).unwrap();
    // 2029-12-31T23:55:00Z to 2030-01-02T00:00:00Z.
    assert_eq!(
        times(minted.public().der()),
        vec!["291231235500Z".to_string(), "300102000000Z".to_string()]
    );
    let later = Minter::new(Fixed(NOW + 3600))
        .mint(&loopback_names())
        .unwrap();
    assert_eq!(
        times(later.public().der()),
        vec!["300101005500Z".to_string(), "300102010000Z".to_string()]
    );
}

#[test]
fn the_certificate_covers_every_name_it_is_given() {
    let gateway = IpAddr::V4(Ipv4Addr::new(172, 18, 0, 1));
    let minted = Minter::new(Fixed(NOW))
        .mint(&names(
            vec![
                IpAddr::V4(Ipv4Addr::LOCALHOST),
                gateway,
                IpAddr::V6(Ipv6Addr::LOCALHOST),
            ],
            &["host.docker.internal"],
        ))
        .unwrap();
    let der = minted.public().der();
    assert!(contains(der, &[127, 0, 0, 1]));
    assert!(contains(der, &[172, 18, 0, 1]));
    assert!(contains(der, &Ipv6Addr::LOCALHOST.octets()));
    assert!(contains(der, b"host.docker.internal"));
    assert!(!contains(der, &[10, 0, 0, 1]));
}

#[test]
fn the_certificate_is_a_server_auth_certificate() {
    let minted = Minter::new(Fixed(NOW)).mint(&loopback_names()).unwrap();
    // The serverAuth key purpose, OID 1.3.6.1.5.5.7.3.1.
    assert!(contains(
        minted.public().der(),
        &[0x2b, 0x06, 0x01, 0x05, 0x05, 0x07, 0x03, 0x01]
    ));
    // ECDSA with SHA-256, OID 1.2.840.10045.4.3.2 (a P-256 key).
    assert!(contains(
        minted.public().der(),
        &[0x2a, 0x86, 0x48, 0xce, 0x3d, 0x04, 0x03, 0x02]
    ));
}

#[test]
fn each_mint_is_a_new_key_and_certificate() {
    let minter = Minter::new(Fixed(NOW));
    let a = minter.mint(&loopback_names()).unwrap();
    let b = minter.mint(&loopback_names()).unwrap();
    assert_ne!(a.public(), b.public());
    assert_ne!(a.key_pem(), b.key_pem());
    assert!(a.key_pem().starts_with("-----BEGIN PRIVATE KEY-----"));
}

#[test]
fn the_key_is_in_no_debug_or_error_rendering() {
    let minted = Minter::new(Fixed(NOW)).mint(&loopback_names()).unwrap();
    let key_body: String = minted
        .key_pem()
        .lines()
        .filter(|l| !l.starts_with("-----"))
        .collect();
    assert!(!key_body.is_empty());
    let shown = format!("{minted:?} {:?}", Minter::new(Fixed(NOW)));
    assert!(!shown.contains(&key_body), "{shown}");
    for error in [
        MintError::Clock { seconds: i64::MAX },
        MintError::Name {
            name: "bad name".to_string(),
            ext_err_txt: "x".to_string(),
        },
        MintError::Generate {
            ext_err_txt: "y".to_string(),
        },
    ] {
        assert!(!format!("{error} {error:?}").contains(&key_body));
    }
}

#[test]
fn a_clock_no_date_can_be_made_from_is_a_typed_refusal() {
    let error = Minter::new(Fixed(i64::MAX))
        .mint(&loopback_names())
        .unwrap_err();
    assert_eq!(error.code(), "hook-tls-clock");
}

#[test]
fn a_name_a_certificate_cannot_carry_is_a_typed_refusal() {
    let error = Minter::new(Fixed(NOW))
        .mint(&names(vec![], &["not ascii \u{e9}"]))
        .unwrap_err();
    assert_eq!(error.code(), "hook-tls-name");
}

#[test]
fn the_errors_display_and_report_their_args_exactly() {
    let clock = MintError::Clock { seconds: 7 };
    assert_eq!(clock.to_string(), "hook-tls-clock seconds=7");
    assert_eq!(clock.args()[0].0, "seconds");
    let name = MintError::Name {
        name: "n".to_string(),
        ext_err_txt: "e".to_string(),
    };
    assert_eq!(name.to_string(), "hook-tls-name name=n ext_err_txt=e");
    let generate = MintError::Generate {
        ext_err_txt: "e".to_string(),
    };
    assert_eq!(generate.to_string(), "hook-tls-generate ext_err_txt=e");
    assert_eq!(generate.args().len(), 1);
}
