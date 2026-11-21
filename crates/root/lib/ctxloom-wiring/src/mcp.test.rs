//! `mcp serve` composed.

// A wake's nonce is of the shape its line is read back by: a wake posted
// with it is known as a wake by the prompt's hook.
#[test]
fn a_wakes_nonce_reads_back_from_its_line_and_each_is_fresh() {
    let nonce = super::nonce();
    assert_eq!(
        run_ports::wake_nonce(&run_ports::wake_line(&nonce)),
        Some(nonce.as_str())
    );
    assert_ne!(super::nonce(), nonce);
}
