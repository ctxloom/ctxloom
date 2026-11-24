//! A store written by the build before the editions migration (angzarr's
//! migrations 1 to 7, frozen as `fixtures/store/ctxloom-migration-7.db`: one
//! session minted by `ctxloom test seed-session --transcript`, six events on
//! four streams) is backed up, migrated to 8 and read back whole by the
//! build that carries 8. Migration 0008 only adds: a partial index on the
//! edition streams' rows, the `outbox_edition_streams` table with its
//! trigger, and a backfill of it from the pending outbox; it rewrites no
//! event row. `fixtures/store/ctxloom-migration-7.record.json` is what
//! `ctxloom test record` printed of that store before the migration.

use std::path::{Path, PathBuf};

use super::{parse_one, Env};
use crate::tests::files;

const STORE: &[u8] = include_bytes!("../fixtures/store/ctxloom-migration-7.db");
const RECORD: &str = include_str!("../fixtures/store/ctxloom-migration-7.record.json");

/// `.ctxloom` of `home`, holding the frozen store as `ctxloom.db`.
fn planted(env: &Env) -> PathBuf {
    let dir = env.home().join(".ctxloom");
    files::mkdirs(&dir);
    files::put(dir.join("ctxloom.db"), STORE);
    dir
}

/// The backups taken beside the store: `ctxloom.db.pre-<level>-<stamp>`.
fn backups(dir: &Path) -> Vec<String> {
    let mut found: Vec<String> = files::children(dir)
        .into_iter()
        .filter_map(|path| path.file_name()?.to_str().map(str::to_string))
        .filter(|name| name.starts_with("ctxloom.db.pre-"))
        .collect();
    found.sort();
    found
}

#[test]
fn test_a_store_of_migration_7_is_backed_up_and_reads_back_its_events_after_migrating_to_8() {
    let env = Env::new();
    let dir = planted(&env);
    let ran = env.run(&["test", "record", "--format", "json"]);
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert_eq!(
        ran.json(),
        parse_one(RECORD),
        "every stream and event the store held, as it held them"
    );
    let backed_up = backups(&dir);
    assert_eq!(backed_up.len(), 1, "one backup, of level 7: {backed_up:?}");
    assert!(
        backed_up[0].starts_with("ctxloom.db.pre-7-"),
        "{backed_up:?}"
    );
    assert!(
        !files::read_bytes(dir.join(&backed_up[0]))
            .unwrap()
            .is_empty(),
        "the backup holds the store"
    );
}

#[test]
fn test_a_migrated_store_is_not_backed_up_or_changed_again() {
    let env = Env::new();
    let dir = planted(&env);
    let first = env.run(&["test", "record", "--format", "json"]);
    assert_eq!(first.code, 0, "{}", first.said());
    let again = env.run(&["test", "record", "--format", "json"]);
    assert_eq!(again.code, 0, "{}", again.said());
    assert_eq!(again.json(), first.json());
    assert_eq!(backups(&dir).len(), 1, "migrated once, so backed up once");
}
