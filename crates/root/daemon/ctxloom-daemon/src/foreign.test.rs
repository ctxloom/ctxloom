//! The daemon's foreign writers: their reports said as the API's messages,
//! and the doctor's files recorded, never written.

use std::collections::BTreeMap;
use std::sync::{Arc, Mutex};

use ctxloom_ports::{Failure, Put, TreeFiles, UserFiles};
use setup_application::{EngineOutcome, MaterializeResult, NotCarried, UninstallReport};

use super::{notice_of, Recording};
use surface_cli::wire::{materialized, uninstalled};

#[test]
fn a_materialize_report_is_said_field_for_field_its_failures_in_its_errors_too() {
    let result = MaterializeResult {
        target: "/work/demo/out".to_string(),
        created: true,
        profiles: vec!["dev".to_string()],
        engines: vec![EngineOutcome {
            engine: "claude-code".to_string(),
            wrote: vec!["context".to_string()],
            released: vec!["mcp".to_string()],
            context_file: "/work/demo/out/CLAUDE.md".to_string(),
            not_carried: vec![NotCarried {
                surface: "hooks".to_string(),
                reason: "none".to_string(),
            }],
            skipped: vec!["s".to_string()],
            warnings: vec!["w".to_string()],
            refused: Vec::new(),
        }],
        status: "partial".to_string(),
        errors: vec!["earlier".to_string()],
        warnings: vec!["top".to_string()],
        failures: vec![surface_ports::SurfaceError::Io {
            file: "/x".to_string(),
            detail: "denied".to_string(),
        }],
    };
    let said = materialized(&result, &notice_of);
    assert_eq!(said.target, "/work/demo/out");
    assert!(said.created);
    assert_eq!(said.profiles, ["dev"]);
    assert_eq!(said.status, "partial");
    assert_eq!(said.warnings, ["top"]);
    let engine = &said.engines[0];
    assert_eq!(engine.engine, "claude-code");
    assert_eq!(engine.wrote, ["context"]);
    assert_eq!(engine.released, ["mcp"]);
    assert_eq!(engine.context_file, "/work/demo/out/CLAUDE.md");
    assert_eq!(
        (
            engine.not_carried[0].surface.as_str(),
            engine.not_carried[0].reason.as_str()
        ),
        ("hooks", "none")
    );
    assert_eq!((engine.skipped.len(), engine.warnings.len()), (1, 1));
    assert_eq!(said.failures.len(), 1);
    let failure = &said.failures[0];
    assert_eq!(
        failure.code,
        coded_diagnostic::Diagnostic::code(&result.failures[0])
    );
    assert_eq!(said.errors, ["earlier".to_string(), failure.text.clone()]);
}

#[test]
fn an_uninstall_report_is_said_field_for_field() {
    let said = uninstalled(&UninstallReport {
        status: "partial".to_string(),
        backends: vec!["mock".to_string()],
        errors: vec!["e".to_string()],
        note: "n".to_string(),
    });
    assert_eq!(said.status, "partial");
    assert_eq!(said.backends, ["mock"]);
    assert_eq!(said.errors, ["e"]);
    assert_eq!(said.note, "n");
}

/// Files in memory, counting the writes that reach them.
#[derive(Default)]
struct Mem {
    files: Mutex<BTreeMap<String, Vec<u8>>>,
}

impl UserFiles for Mem {
    fn exists(&self, path: &str) -> Result<bool, Failure> {
        Ok(self.files.lock().unwrap().contains_key(path))
    }
    fn is_dir(&self, _: &str) -> Result<bool, Failure> {
        Ok(false)
    }
    fn read_file(&self, path: &str) -> Result<Vec<u8>, Failure> {
        Ok(self.files.lock().unwrap()[path].clone())
    }
    fn read_tree(&self, _: &str) -> Result<TreeFiles, Failure> {
        Ok(TreeFiles::default())
    }
    fn write_file(&self, path: &str, bytes: &[u8], _: bool) -> Result<bool, Failure> {
        self.files
            .lock()
            .unwrap()
            .insert(path.to_string(), bytes.to_vec());
        Ok(true)
    }
    fn write_tree(&self, _: &str, _: &[Put]) -> Result<(), Failure> {
        Ok(())
    }
}

#[test]
fn recording_user_files_write_nothing_and_read_back_what_they_recorded() {
    let mem = Arc::new(Mem::default());
    mem.write_file("/w/a.json", b"old", true).unwrap();
    let recording = Recording::over(mem.clone());
    assert!(recording.write_file("/w/b.json", b"1", true).unwrap());
    assert!(recording.write_file("/w/a.json", b"new", true).unwrap());
    assert!(recording.write_file("/w/b.json", b"2", true).unwrap());
    // Not over what is there, when asked not to be.
    assert!(!recording.write_file("/w/a.json", b"no", false).unwrap());
    assert!(recording.exists("/w/b.json").unwrap());
    assert_eq!(recording.read_file("/w/a.json").unwrap(), b"new");
    assert_eq!(
        mem.files.lock().unwrap().len(),
        1,
        "a write reached the files"
    );
    assert_eq!(mem.files.lock().unwrap()["/w/a.json"], b"old");
    let taken: Vec<(String, Option<Vec<u8>>, String)> = recording
        .take()
        .into_iter()
        .map(|put| (put.path, put.bytes, put.keep))
        .collect();
    assert_eq!(
        taken,
        [
            (
                "/w/b.json".to_string(),
                Some(b"2".to_vec()),
                "/w".to_string()
            ),
            (
                "/w/a.json".to_string(),
                Some(b"new".to_vec()),
                "/w".to_string()
            ),
        ]
    );
    assert!(recording.take().is_empty());
}

// Step 10c U5: `manage check`'s report, said by the daemon, is the report
// the CLI prints, field for field.
#[test]
fn a_manage_check_report_reads_back_field_for_field() {
    use setup_application::{BackendStatus, CheckReport, SurfaceCurrency};
    use surface_cli::wire::{check_report, checked};
    let made = CheckReport {
        work_dir: "/work/demo".to_string(),
        manage_statusline: true,
        backends: vec![BackendStatus {
            backend: "claude-code".to_string(),
            settings_exists: true,
            hooks_present: false,
            hooks_at_launch: true,
            status_line: true,
            mcp_present: false,
        }],
        surfaces: vec![SurfaceCurrency {
            route: "hooks".to_string(),
            backend: "claude-code".to_string(),
            status: "stale".to_string(),
        }],
    };
    let back = check_report(&checked(&made));
    assert_eq!(
        serde_json::to_value(&back).unwrap(),
        serde_json::to_value(&made).unwrap()
    );
}

// Step 10c U5: `manage gitignore install`'s report, said by the daemon, is
// the report the CLI prints, field for field.
#[test]
fn a_gitignore_report_reads_back_field_for_field() {
    use setup_application::GitignoreReport;
    use surface_cli::wire::{gitignore_installed, gitignore_report};
    let made = GitignoreReport {
        status: "updated".to_string(),
        path: "/work/demo/.gitignore".to_string(),
        nested_path: "/work/demo/.ctxloom/.gitignore".to_string(),
        nested_written: true,
    };
    let back = gitignore_report(&gitignore_installed(&made));
    assert_eq!(
        serde_json::to_value(&back).unwrap(),
        serde_json::to_value(&made).unwrap()
    );
}
