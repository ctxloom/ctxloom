//! The seed the acceptance tests plant sessions with (`ctxloom test
//! seed-session`, built with `test-support` only). Go's fixtures write a
//! session's sidecar; the Rust store has no sidecar to write, so the seed
//! mints the session in the cwd's project store through the domain and writes
//! beside it what a fixture writes: the session's lock and authored files.
//! Its transcript is what its hooks would deliver, a capture fact on its
//! stream (owner ruling 87a: no transcript file).
//!
//! It mints WITHOUT the session-name claim: the claim is the owner's design
//! (D5 Q1) and its tests await review. When it lands, the real mint (S6)
//! takes the claim first and this seed goes through it.

use std::path::Path;
use std::time::{Duration, SystemTime};

use angzarr_embedded::v1::{command_page, Command, CommandBook, CommandPage, Cover, Uuid};
use angzarr_embedded::{Kernel, Payload, Processed, ReadStore, Sampled, Store, Timestamp};
use ctxloom_store_sqlite::SqliteStores;
use file_system::{Open, Perms};
use os::Mode;
use ports::{Clock, CommandIds, Project, ProjectIdentity};
use session_ports::ProjectStores;
use sessions::{
    BindEngineSession, ClaimName, MintSession, NameClaimAggregate, NameClaimCommand, Origin,
    RecordEnd, SessionAggregate, SessionCommand, SetKept, NAMES_DOMAIN, SESSION_DOMAIN,
};
use system::System;

use crate::{TestError, Wiring};

/// A session to plant.
#[derive(Clone, Debug, Default)]
pub struct Seed {
    pub harp: String,
    /// Another project than the cwd's.
    pub project: Option<String>,
    /// Seconds ago it was minted.
    pub started_ago: u64,
    /// Seconds ago its last run ended; none: it never ended.
    pub ended_ago: Option<u64>,
    /// Make its lock file, nothing holding it.
    pub lock_free: bool,
    /// Record the first prompt Go's fixture's transcript holds, as its hooks
    /// would (a capture fact).
    pub transcript: bool,
    pub keep: bool,
    pub oneshot: bool,
    /// The engine's session and transcript it is bound to.
    pub native: Option<(String, String)>,
    /// Authored files in the session directory.
    pub other_files: Vec<String>,
}

fn seed_error(detail: impl ToString) -> TestError {
    TestError::Seed {
        detail: detail.to_string(),
    }
}

impl Wiring {
    /// Plant `seed`; its stable id.
    pub fn seed_session(&self, seed: &Seed) -> Result<String, TestError> {
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .map_err(seed_error)?;
        runtime.block_on(self.seed(seed))
    }

    async fn seed(&self, seed: &Seed) -> Result<String, TestError> {
        let mut sessions = self.sessions();
        // The project is named first, as a run names it (slice 2 M1): the
        // session is rooted on its name.
        let named = sessions
            .use_cases
            .name_project(sessions_application::NameProjectRequest {
                project: seed.project.clone(),
                harps: crate::fresh_project_harps(),
            })
            .await
            .map_err(|e| seed_error(angzarr_diagnostic::Diagnostic::code(&e)));
        let mut identity = sessions.use_cases.identity;
        let mut stores = sessions.use_cases.stores;
        let project = identity
            .for_write(seed.project.as_deref())
            .map_err(seed_error_of)?;
        let seeded = match named {
            Ok(named) => self.seed_in(&mut stores, &project, &named.harp, seed).await,
            Err(error) => Err(error),
        };
        // Closed, not dropped (ruling 131): the seed's commits are in the
        // file, with no log beside it, when the command exits.
        let closed = stores.close().await.map_err(seed_error);
        let id = seeded?;
        closed?;
        Ok(id)
    }

    /// Plant `seed` in `project`'s store, opened through `stores`.
    async fn seed_in(
        &self,
        stores: &mut SqliteStores,
        project: &Project,
        project_harp: &str,
        seed: &Seed,
    ) -> Result<String, TestError> {
        let store = stores.open(project).await.map_err(seed_error)?;
        let name = sessions::SessionName::new(project_harp, &seed.harp).map_err(seed_error)?;
        let cwd = self.fs.cwd().map_err(seed_error)?.to_path_buf();
        let output_dir = self.seed_output_dir(&seed.harp)?;

        let mut system = System;
        let now = system.now();
        let before = |ago: u64| Timestamp {
            seconds: now.seconds - i64::try_from(ago).unwrap_or(0),
            nanos: 0,
        };
        // Its stable id is the UUIDv5 of its name (slice 2 B3).
        let id = *name.id().as_bytes();
        let registry = sessions_application::registry();
        let registry = &registry;
        let session_cover = Cover {
            domain: SESSION_DOMAIN.to_string(),
            root: Some(Uuid::from_bytes(id)),
            edition: None,
        };
        let run_book = |at: Timestamp, book: CommandBook| {
            let sampled = Sampled {
                now: at,
                command_id: System.command_id(),
            };
            async move {
                Kernel::new(registry)
                    .handle::<SessionAggregate, _>(store, book, &sampled)
                    .await
                    .map(drop)
                    .map_err(|failure| seed_error(format!("{failure:?}")))
            }
        };
        let run = |at: Timestamp, command: SessionCommand| {
            let book = CommandBook {
                cover: Some(session_cover.clone()),
                page: Some(CommandPage {
                    body: Some(command_page::Body::Command(Command {
                        payload: Some(command.to_any()),
                        handling: None,
                    })),
                    ..CommandPage::default()
                }),
            };
            run_book(at, book)
        };
        let started = before(seed.started_ago);
        // The name first, as the real mint takes it (plan 3.1).
        claim_name(store, &project.id, seed, &name, started).await?;
        run(
            started,
            MintSession {
                harp: seed.harp.clone(),
                project_id: project.id.clone(),
                project_dir: cwd.display().to_string(),
                output_dir: output_dir.display().to_string(),
                engine: "claude-code".to_string(),
                origin: if seed.oneshot {
                    Origin::Oneshot
                } else {
                    Origin::Session
                },
                started_at: Some(started),
                depth: 0,
                owner: None,
            }
            .into(),
        )
        .await?;
        if let Some((native_id, transcript_path)) = &seed.native {
            // A bind is a fact (owner ruling 2026-11-04, S5 review item 8).
            run_book(
                before(seed.started_ago.saturating_sub(1)),
                sessions::bind_book(
                    session_cover.clone(),
                    &BindEngineSession {
                        native_id: native_id.clone(),
                        transcript_path: transcript_path.clone(),
                        engine_version: None,
                    },
                ),
            )
            .await?;
        }
        if seed.transcript {
            let book = first_prompt(&session_cover, id, &seed.harp)?;
            run_book(before(seed.started_ago.saturating_sub(1)), book).await?;
        }
        if seed.keep {
            run(now, SetKept { kept: true }.into()).await?;
        }
        if let Some(ago) = seed.ended_ago {
            run(now, RecordEnd { at: before(ago) }.into()).await?;
        }
        self.plant_files(seed)?;
        Ok(uuid::Uuid::from_bytes(id).to_string())
    }

    /// Where a seeded session's output goes: where a run's would
    /// (`<project>/<output.dir>/<harp>`, owner rulings 89, 106).
    fn seed_output_dir(&self, harp: &str) -> Result<std::path::PathBuf, TestError> {
        let library = crate::library::build(self, None).map_err(seed_error)?;
        let written = library
            .run_settings(&library_application::Flags::default())
            .map_or_else(
                || library_application::OUTPUT_DIR_DEFAULT.to_string(),
                |s| s.output_dir,
            );
        Ok(crate::oneshot::output_root(&self.project_dir(), &written).join(harp))
    }

    /// The files a fixture writes beside a session: its lock and authored
    /// files; each backdated to the session's end, as
    /// Go's `backdateSessionDir` does, so a session that ENDED then looks
    /// untouched since.
    fn plant_files(&self, seed: &Seed) -> Result<(), TestError> {
        let sessions_dir = self.project_dir().join(".ctxloom/sessions");
        let dir = sessions_dir.join(&seed.harp);
        let mut written = Vec::new();
        let mut write = |path: &Path, body: &str| -> Result<(), TestError> {
            if let Some(parent) = path.parent() {
                self.fs
                    .create_dir_all(parent, Mode::from_bits(0o755))
                    .map_err(seed_error)?;
            }
            self.fs
                .write_atomic(path, body.as_bytes(), Perms::Exact(Mode::from_bits(0o600)))
                .map_err(seed_error)?;
            written.push(path.to_path_buf());
            Ok(())
        };
        for rel in &seed.other_files {
            write(&dir.join(rel), "authored\n")?;
        }
        if seed.lock_free {
            let lock = sessions_dir.join(format!("{}.lock", seed.harp));
            if let Some(parent) = lock.parent() {
                self.fs
                    .create_dir_all(parent, Mode::from_bits(0o755))
                    .map_err(seed_error)?;
            }
            self.fs
                .open(&lock, Open::Create(Mode::from_bits(0o600)))
                .map_err(seed_error)?;
            written.push(lock);
        }
        if let Some(ago) = seed.ended_ago {
            let at = SystemTime::now() - Duration::from_secs(ago);
            for path in written {
                self.fs
                    .open(&path, Open::Append(Mode::from_bits(0o600)))
                    .and_then(|file| file.set_modified(at))
                    .map_err(seed_error)?;
            }
        }
        Ok(())
    }
}

/// The fact its hooks would deliver for the session's first prompt, Go's
/// fixture's: keyed as a hook keys it.
fn first_prompt(cover: &Cover, id: [u8; 16], harp: &str) -> Result<CommandBook, TestError> {
    use angzarr_embedded::markup::DomainMessage;
    use angzarr_embedded::v1::{Fact, FactOrigin};
    let prompt = sessions::RecordPromptSubmitted {
        prompt_id: "seed".to_string(),
        prompt: format!("Seeded prompt for {harp}."),
    };
    Ok(CommandBook {
        cover: Some(cover.clone()),
        page: Some(CommandPage {
            body: Some(command_page::Body::Fact(Fact {
                payload: Some(
                    angzarr_embedded::Any::from_msg(&prompt.to_wire()).map_err(seed_error)?,
                ),
                origin: Some(FactOrigin {
                    external_id: format!("hook:prompt:{}:seed", uuid::Uuid::from_bytes(id)),
                    description: String::new(),
                }),
            })),
            ..CommandPage::default()
        }),
    })
}

/// A report from a port, as a seed error.
fn seed_error_of(report: angzarr_diagnostic::Report) -> TestError {
    seed_error(angzarr_diagnostic::Diagnostic::code(&report))
}

/// Claim the session's name, first, as the real mint does.
async fn claim_name<S: Store + ReadStore + Processed + Send + Sync>(
    store: &S,
    project_id: &str,
    seed: &Seed,
    name: &sessions::SessionName,
    started: Timestamp,
) -> Result<(), TestError> {
    let claim = NameClaimCommand::from(ClaimName {
        project_id: project_id.to_string(),
        harp: seed.harp.clone(),
        session: name.uid(),
    });
    let claims = sessions_application::registry();
    Kernel::new(&claims)
        .handle::<NameClaimAggregate, _>(
            store,
            CommandBook {
                cover: Some(Cover {
                    domain: NAMES_DOMAIN.to_string(),
                    root: Some(name.root()),
                    edition: None,
                }),
                page: Some(CommandPage {
                    body: Some(command_page::Body::Command(Command {
                        payload: Some(claim.to_any()),
                        handling: None,
                    })),
                    ..CommandPage::default()
                }),
            },
            &Sampled {
                now: started,
                command_id: System.command_id(),
            },
        )
        .await
        .map(drop)
        .map_err(|failure| seed_error(format!("{failure:?}")))
}

#[cfg(test)]
#[path = "seed.test.rs"]
mod tests;
