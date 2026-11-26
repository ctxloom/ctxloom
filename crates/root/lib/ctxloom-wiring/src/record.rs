//! What the acceptance tests read and plant of the daemon's ONE store
//! (T14), built with `test-support` only (slice 4 U6): `ctxloom test
//! record` prints every stream it holds, and `ctxloom test seed-rehosts`
//! plants a child's past re-hosts, each with its recorded instant.

use angzarr_embedded::v1::{command_page, event_page, Command, CommandBook, CommandPage, Cover};
use angzarr_embedded::v1::{Fact, FactOrigin};
use angzarr_embedded::{
    EventStore, Kernel, ListStreams, Payload, ReadStore, Sampled, StreamRef, Timestamp,
};
use ports::{Clock, CommandIds};
use serde_json::{json, Value};
use sessions::{SessionAggregate, SessionEvent, SESSION_DOMAIN};
use system::System;

use crate::{TestError, Wiring};

fn record_error(detail: impl ToString) -> TestError {
    TestError::Seed {
        detail: detail.to_string(),
    }
}

/// A stream's root as UUID text.
fn root_text(cover: &Cover) -> String {
    cover
        .root
        .as_ref()
        .and_then(|root| uuid::Uuid::from_slice(&root.value).ok())
        .map(|id| id.hyphenated().to_string())
        .unwrap_or_default()
}

/// One event as the record prints it: its type, its payload's bytes (hex:
/// two events alike are one fact recorded twice), and what the record's
/// readers ask of it (a session removal's harp and steps among them).
fn row(any: &angzarr_embedded::Any) -> Value {
    let kind = any.type_url.rsplit('.').next().unwrap_or_default();
    let hex: String = any.value.iter().map(|b| format!("{b:02x}")).collect();
    let mut row = json!({"type": kind, "payload": hex});
    if let Ok(event) = SessionEvent::from_any(any) {
        match event {
            SessionEvent::SessionMinted(minted) => {
                row["harp"] = minted.harp.into();
                row["workspace"] = minted.workspace.into();
            }
            SessionEvent::IncarnationOwed(owed) => {
                row["incarnation"] = owed.incarnation.into();
                row["reason"] = owed.reason.into();
            }
            SessionEvent::IncarnationEnded(ended) => {
                row["incarnation"] = ended.incarnation.into();
                row["cause"] = ended.cause.into();
            }
            SessionEvent::MessageSent(sent) => {
                row["message_id"] = sent.message_id.into();
                row["text"] = sent.text.into();
            }
            _ => {}
        }
    } else if let Ok(event) = runs::RunEvent::from_any(any) {
        match event {
            runs::RunEvent::RunEnqueued(enqueued) => {
                row["session"] = enqueued.session.into();
                row["incarnation"] = enqueued.incarnation.into();
            }
            runs::RunEvent::RunEnded(ended) => {
                row["cause"] = format!("{:?}", ended.cause).into();
            }
            runs::RunEvent::TurnFailedRecorded(failed) => {
                row["class"] = format!("{:?}", failed.class).into();
            }
            runs::RunEvent::RunnerStartedRecorded(started) => {
                row["workspace"] = started.workspace.into();
            }
            _ => {}
        }
    } else if let Ok(event) = sessions::ProjectEvent::from_any(any) {
        // A session's removal on its project's stream (O2, step 10b U9).
        match event {
            sessions::ProjectEvent::SessionRemovalIntended(intended) => {
                row["request_id"] = intended.request_id.into();
                row["harp"] = intended.harp.into();
                row["steps"] = intended.steps.into();
            }
            sessions::ProjectEvent::RemovalStepDone(done) => {
                row["request_id"] = done.request_id.into();
                row["step"] = done.step.into();
            }
            _ => {}
        }
    }
    row
}

impl Wiring {
    /// Every stream of the one store, as `ctxloom test record` prints it:
    /// `[{domain, root, events: [row]}]`, by domain then root. None where
    /// no store was made.
    #[expect(
        clippy::disallowed_methods,
        reason = "P2 one-writer debt: test-support `test record` and `test seed-rehosts` go through a daemon, not their own store and kernel"
    )]
    pub fn record(&self) -> Result<Value, TestError> {
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .map_err(record_error)?;
        runtime.block_on(async {
            let stores = ctxloom_store_sqlite::Stores::new(self.fs(), self.home().to_path_buf());
            let Some(store) = stores.open_existing().await.map_err(record_error)? else {
                return Ok(Value::Array(Vec::new()));
            };
            let read = async {
                let mut streams = Vec::new();
                for cover in store.list_streams().await.map_err(record_error)? {
                    let stream = StreamRef::try_from(&cover).map_err(record_error)?;
                    let slice = store.read(stream, 0, None).await.map_err(record_error)?;
                    let events: Vec<Value> = slice
                        .pages
                        .into_iter()
                        .filter_map(|page| match page.body {
                            Some(event_page::Body::Event(any)) => Some(row(&any)),
                            _ => None,
                        })
                        .collect();
                    streams.push(json!({
                        "domain": cover.domain,
                        "root": root_text(&cover),
                        "events": events,
                    }));
                }
                Ok::<_, TestError>(Value::Array(streams))
            };
            let read = read.await;
            let closed = store.close().await.map_err(record_error);
            let read = read?;
            closed?;
            Ok(read)
        })
    }

    /// Plant `count` past re-hosts of the session `harp` (its session
    /// stream found by its mint), each its incarnation ended `orphaned` and
    /// its next owed `rehost`, recorded over the last few minutes, then the
    /// last one's lost too: a child re-hosted `count` times in the last
    /// hour, whose host was lost again.
    #[expect(
        clippy::disallowed_methods,
        reason = "P2 one-writer debt: test-support `test record` and `test seed-rehosts` go through a daemon, not their own store and kernel"
    )]
    pub fn seed_rehosts(&self, harp: &str, count: u32) -> Result<(), TestError> {
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .map_err(record_error)?;
        runtime.block_on(async {
            let stores = ctxloom_store_sqlite::Stores::new(self.fs(), self.home().to_path_buf());
            let store = stores.open().await.map_err(record_error)?;
            let planted = plant_rehosts(&store, harp, count).await;
            let closed = store.close().await.map_err(record_error);
            planted?;
            closed?;
            Ok(())
        })
    }
}

/// The session stream whose mint named `harp`, and its incarnation now.
async fn session_named<S: EventStore + ListStreams>(
    store: &S,
    harp: &str,
) -> Result<(Cover, u32), TestError> {
    for cover in store.list_streams().await.map_err(record_error)? {
        if cover.domain != SESSION_DOMAIN {
            continue;
        }
        let stream = StreamRef::try_from(&cover).map_err(record_error)?;
        let slice = store.read(stream, 0, None).await.map_err(record_error)?;
        let mut named = false;
        let mut incarnation = 0;
        for page in slice.pages {
            let Some(event_page::Body::Event(any)) = page.body else {
                continue;
            };
            match SessionEvent::from_any(&any) {
                Ok(SessionEvent::SessionMinted(minted)) => named = minted.harp == harp,
                Ok(SessionEvent::IncarnationOwed(owed)) => incarnation = owed.incarnation,
                _ => {}
            }
        }
        if named {
            return Ok((cover, incarnation));
        }
    }
    Err(record_error(format!("no session {harp}")))
}

#[expect(
    clippy::disallowed_methods,
    reason = "P2 one-writer debt: test-support `test record` and `test seed-rehosts` go through a daemon, not their own store and kernel"
)]
async fn plant_rehosts<S>(store: &S, harp: &str, count: u32) -> Result<(), TestError>
where
    S: angzarr_embedded::Store
        + ReadStore
        + ListStreams
        + angzarr_embedded::Processed
        + Send
        + Sync,
{
    let (cover, from) = session_named(store, harp).await?;
    let session = root_text(&cover);
    let registry = sessions_application::registry();
    let now = System.now();
    let handle = |seconds_ago: i64, book: CommandBook| {
        let sampled = Sampled {
            now: Timestamp {
                seconds: now.seconds - seconds_ago,
                nanos: 0,
            },
            command_id: System.command_id(),
        };
        let registry = &registry;
        async move {
            Kernel::new(registry)
                .handle::<SessionAggregate, _>(store, book, &sampled)
                .await
                .map(drop)
                .map_err(|failure| record_error(format!("{failure:?}")))
        }
    };
    let ended = |incarnation: u32| CommandBook {
        cover: Some(cover.clone()),
        page: Some(CommandPage {
            body: Some(command_page::Body::Fact(Fact {
                payload: angzarr_embedded::Any::from_msg(
                    &angzarr_embedded::markup::DomainMessage::to_wire(
                        &sessions::RecordIncarnationEnded {
                            incarnation,
                            cause: "orphaned".to_string(),
                        },
                    ),
                )
                .ok(),
                origin: Some(FactOrigin {
                    external_id: format!("incarnation-ended:{session}:{incarnation}"),
                    description: String::new(),
                }),
            })),
            ..CommandPage::default()
        }),
    };
    let rehost = || CommandBook {
        cover: Some(cover.clone()),
        page: Some(CommandPage {
            body: Some(command_page::Body::Command(Command {
                payload: Some(
                    sessions::SessionCommand::from(sessions::RehostSession {
                        recent_rehosts: 0,
                        cause: String::new(),
                    })
                    .to_any(),
                ),
                handling: None,
            })),
            ..CommandPage::default()
        }),
    };
    for n in 0..count {
        // Spread over the minutes before now, oldest first.
        let ago = i64::from(count - n) * 2;
        handle(ago + 1, ended(from + n)).await?;
        handle(ago, rehost()).await?;
    }
    handle(0, ended(from + count)).await
}
