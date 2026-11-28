use super::RunLockTable;

#[test]
fn a_run_is_this_processs_from_its_claim_until_its_release() {
    let table = RunLockTable::new();
    assert!(!table.holds("s1", 1));
    table.claim("s1", 1, None);
    assert!(table.holds("s1", 1));
    assert!(!table.holds("s1", 2), "another incarnation is not claimed");
    assert!(!table.holds("s2", 1), "nor another session's");
    table.release("s1", 1);
    assert!(!table.holds("s1", 1));
    table.release("s1", 1);
}
