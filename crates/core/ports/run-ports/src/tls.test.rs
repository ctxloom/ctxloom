use std::net::{IpAddr, Ipv4Addr, Ipv6Addr};

use angzarr_diagnostic::Diagnostic;

use crate::bind::{bind_for, ContainerRuntime, PlatformFacts, Runtime};
use crate::tls::{CertError, CertNames, HookCertificate, PublicCert};

const SEQUENCE_DER: [u8; 5] = [0x30, 0x03, 0x01, 0x02, 0x03];
const SEQUENCE_B64: &str = "MAMBAgM=";

fn loopback4() -> IpAddr {
    IpAddr::V4(Ipv4Addr::LOCALHOST)
}

fn loopback6() -> IpAddr {
    IpAddr::V6(Ipv6Addr::LOCALHOST)
}

#[test]
fn the_host_run_names_the_loopback_addresses_only() {
    let plan = bind_for(Runtime::Host, &PlatformFacts::NONE).unwrap();
    let names = CertNames::for_plan(&plan);
    assert_eq!(names.ips(), vec![loopback4(), loopback6()]);
    assert!(names.dns().is_empty());
}

#[test]
fn a_bridge_run_names_the_gateway_and_the_hosts_alias() {
    let gateway = IpAddr::V4(Ipv4Addr::new(172, 18, 0, 1));
    let plan = bind_for(
        Runtime::Container(ContainerRuntime::DockerRootful),
        &PlatformFacts {
            bridge_gateway: Some(gateway),
            ..PlatformFacts::NONE
        },
    )
    .unwrap();
    let names = CertNames::for_plan(&plan);
    assert_eq!(names.ips(), vec![loopback4(), gateway, loopback6()]);
    assert_eq!(names.dns(), vec!["host.docker.internal".to_string()]);
}

#[test]
fn every_runtimes_alias_is_a_name_and_nothing_is_named_twice() {
    for (runtime, alias) in [
        (ContainerRuntime::PodmanRootless, "host.containers.internal"),
        (ContainerRuntime::DockerDesktop, "host.docker.internal"),
        (ContainerRuntime::Colima, "host.lima.internal"),
        (ContainerRuntime::Lima, "host.lima.internal"),
    ] {
        let plan = bind_for(Runtime::Container(runtime), &PlatformFacts::NONE).unwrap();
        let names = CertNames::for_plan(&plan);
        assert_eq!(names.dns(), vec![alias.to_string()], "{runtime:?}");
        // The bind is the loopback the list already holds.
        assert_eq!(names.ips(), vec![loopback4(), loopback6()], "{runtime:?}");
    }
}

#[test]
fn the_env_value_is_one_line_of_base64_and_round_trips() {
    let cert = PublicCert::from_der(SEQUENCE_DER.to_vec()).unwrap();
    assert_eq!(cert.der(), SEQUENCE_DER);
    assert_eq!(cert.to_env_value(), SEQUENCE_B64);
    let big = PublicCert::from_der([&[0x30u8][..], &[7u8; 300][..]].concat()).unwrap();
    let value = big.to_env_value();
    assert!(!value.contains('\n') && !value.contains(' '));
    assert_eq!(PublicCert::from_env_value(&value).unwrap(), big);
}

#[test]
fn a_missing_or_empty_value_is_missing() {
    assert_eq!(
        PublicCert::from_environment(None).unwrap_err(),
        CertError::Missing
    );
    assert_eq!(
        PublicCert::from_environment(Some("")).unwrap_err(),
        CertError::Missing
    );
    assert_eq!(
        PublicCert::from_environment(Some(SEQUENCE_B64))
            .unwrap()
            .der(),
        SEQUENCE_DER
    );
}

#[test]
fn a_garbled_value_is_malformed_and_is_not_echoed() {
    for text in [
        "!!!!",
        "MAMBAgM=\n",
        " MAMBAgM=",
        "AAAA",
        "MAMB AgM=",
        "MAM",
    ] {
        let error = PublicCert::from_env_value(text).unwrap_err();
        assert_eq!(
            error,
            CertError::Malformed {
                length: text.len() as u64
            },
            "{text:?}"
        );
        assert_eq!(error.code(), "run-ports-cert-malformed");
    }
    assert_eq!(
        PublicCert::from_der(Vec::new()).unwrap_err(),
        CertError::Malformed { length: 0 }
    );
    assert_eq!(
        PublicCert::from_der(vec![0x31, 0]).unwrap_err(),
        CertError::Malformed { length: 2 }
    );
}

#[test]
fn the_pem_wraps_the_der_at_64_columns() {
    let cert = PublicCert::from_der(SEQUENCE_DER.to_vec()).unwrap();
    assert_eq!(
        cert.to_pem(),
        format!("-----BEGIN CERTIFICATE-----\n{SEQUENCE_B64}\n-----END CERTIFICATE-----\n")
    );
    let big = PublicCert::from_der([&[0x30u8][..], &[7u8; 100][..]].concat()).unwrap();
    let pem = big.to_pem();
    let body: Vec<&str> = pem.lines().collect();
    assert_eq!(body.first(), Some(&"-----BEGIN CERTIFICATE-----"));
    assert_eq!(body.last(), Some(&"-----END CERTIFICATE-----"));
    let lines = &body[1..body.len() - 1];
    assert_eq!(lines.len(), 3, "{lines:?}");
    assert_eq!(lines[0].len(), 64);
    assert_eq!(lines[1].len(), 64);
    assert_eq!(lines.concat(), big.to_env_value());
}

#[test]
fn the_private_key_is_in_no_debug_rendering() {
    let public = PublicCert::from_der(SEQUENCE_DER.to_vec()).unwrap();
    let key = "-----BEGIN PRIVATE KEY-----\nSECRETKEYTEXT\n-----END PRIVATE KEY-----\n";
    let cert = HookCertificate::new(public.clone(), key.to_string());
    assert_eq!(cert.public(), &public);
    assert_eq!(cert.key_pem(), key);
    let shown = format!("{cert:?}");
    assert!(!shown.contains("SECRETKEYTEXT"), "{shown}");
    assert!(shown.contains("redacted"), "{shown}");
}

#[test]
fn the_cert_errors_display_and_report_their_args_exactly() {
    assert_eq!(CertError::Missing.to_string(), "run-ports-cert-missing");
    assert!(CertError::Missing.args().is_empty());
    let malformed = CertError::Malformed { length: 4 };
    assert_eq!(malformed.to_string(), "run-ports-cert-malformed length=4");
    assert_eq!(malformed.args()[0].0, "length");
    assert_eq!(malformed.code(), "run-ports-cert-malformed");
    assert_eq!(CertError::Missing.code(), "run-ports-cert-missing");
}

#[test]
fn names_are_sorted_and_none_is_twice() {
    let names = CertNames::new(
        vec![loopback6(), loopback4(), loopback6()],
        vec!["b".to_string(), "a".to_string(), "b".to_string()],
    );
    assert_eq!(names.ips(), vec![loopback4(), loopback6()]);
    assert_eq!(names.dns(), vec!["a".to_string(), "b".to_string()]);
}

#[test]
fn the_public_cert_debugs_as_its_size_alone() {
    let cert = PublicCert::from_der(SEQUENCE_DER.to_vec()).unwrap();
    assert_eq!(format!("{cert:?}"), "PublicCert(5 bytes)");
    let hook = HookCertificate::new(cert, "KEY-SECRET".to_string());
    assert_eq!(
        format!("{hook:?}"),
        "HookCertificate(PublicCert(5 bytes), key redacted)"
    );
}
