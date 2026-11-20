//! The client's TLS: ring, and the roots the system's store gives.

use rustls::pki_types::CertificateDer;

use super::*;

fn a_root() -> CertificateDer<'static> {
    let key = rcgen::KeyPair::generate().expect("a key");
    let params = rcgen::CertificateParams::new(vec!["root.test".to_string()]).expect("params");
    params
        .self_signed(&key)
        .expect("a certificate")
        .der()
        .clone()
}

fn suites(provider: &rustls::crypto::CryptoProvider) -> Vec<rustls::CipherSuite> {
    provider.cipher_suites.iter().map(|s| s.suite()).collect()
}

#[test]
fn test_the_provider_is_ring() {
    let config = config(vec![a_root()], Vec::new()).expect("a config");
    assert_eq!(
        suites(config.crypto_provider()),
        suites(&rustls::crypto::ring::default_provider())
    );
}

#[test]
fn test_a_store_with_no_root_is_refused_saying_so() {
    let refused = config(Vec::new(), Vec::new()).expect_err("no root");
    assert_eq!(
        refused,
        "cannot make an HTTPS client: the system's certificate store holds no trusted root"
    );
}

#[test]
fn test_a_store_with_no_root_says_what_the_store_answered() {
    let refused = config(Vec::new(), vec!["no such file".to_string()]).expect_err("no root");
    assert_eq!(
        refused,
        "cannot make an HTTPS client: the system's certificate store holds no trusted root: no such file"
    );
}

#[test]
fn test_a_root_that_does_not_parse_is_passed_over_and_the_others_kept() {
    let garbled = CertificateDer::from(vec![0x30, 0x03, 0x01, 0x01, 0xff]);
    assert!(config(vec![garbled.clone()], Vec::new()).is_err());
    assert!(config(vec![garbled, a_root()], vec!["one unreadable".to_string()]).is_ok());
}

#[test]
fn test_the_system_store_of_this_host_gives_a_config() {
    platform().expect("the host's trusted roots");
}
