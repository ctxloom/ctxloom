use crate::sealing::{Sealing, SealingError, TestSealing};

#[test]
fn a_value_opens_only_for_the_field_it_was_sealed_for() {
    let sealed = TestSealing.seal("p", "TOKEN", b"sk-secret").unwrap();
    assert_ne!(sealed, b"sk-secret".to_vec());
    assert_eq!(
        TestSealing.open("p", "TOKEN", &sealed).unwrap(),
        b"sk-secret"
    );
    assert!(matches!(
        TestSealing.open("p", "OTHER", &sealed),
        Err(SealingError::Open { .. })
    ));
}
