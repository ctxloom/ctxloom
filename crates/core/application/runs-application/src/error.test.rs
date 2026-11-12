use crate::{diagnostic_samples, AppError, EN_US};

#[test]
fn every_diagnostic_has_a_message_using_exactly_its_args() {
    let (name, catalog) = EN_US;
    assert_eq!(name, "runs-application");
    for sample in diagnostic_samples() {
        let code = sample.code();
        assert!(code.starts_with("runs-application-"), "{code} is prefixed");
        let line = catalog
            .lines()
            .find(|l| l.starts_with(&format!("{code} =")))
            .unwrap_or_else(|| panic!("{code} has no message"));
        let mut args: Vec<&str> = sample.args().into_iter().map(|(n, _)| n).collect();
        args.sort_unstable();
        let mut used: Vec<&str> = line
            .split('$')
            .skip(1)
            .map(|rest| {
                rest.split(|c: char| !(c.is_ascii_alphanumeric() || c == '_' || c == '-'))
                    .next()
                    .unwrap()
            })
            .collect();
        used.sort_unstable();
        used.dedup();
        assert_eq!(args, used, "{code}");
    }
}

// NEW-DESIGN: an error is its code and args, and the catalog test has samples.
#[test]
fn an_error_displays_its_code_and_the_samples_are_not_empty() {
    let failed = AppError::Failed {
        reason: "unavailable".to_string(),
    };
    assert_eq!(
        failed.to_string(),
        "runs-application-failed reason=unavailable"
    );
    assert_eq!(diagnostic_samples().len(), 3);
}

// The refusals of every domain the edge serves are told by their stable code
// and args, read back from the refusal's TYPE (never the status it travelled
// under): the runs', the sessions' and the trees'.
mod domains {
    use std::sync::Arc;

    use angzarr_embedded::v1::{command_page, Command, CommandBook, CommandPage, Cover, Uuid};
    use angzarr_embedded::Payload;
    use run_ports::{Arrival, Edge};

    use crate::fakes::KernelEdge;
    use crate::AppError;

    fn to(domain: &str, command: impl Payload) -> CommandBook {
        CommandBook {
            cover: Some(Cover {
                domain: domain.to_string(),
                root: Some(Uuid::from_bytes([5; 16])),
                edition: None,
            }),
            page: Some(CommandPage {
                body: Some(command_page::Body::Command(Command {
                    payload: Some(command.to_any()),
                    handling: None,
                })),
                ..CommandPage::default()
            }),
        }
    }

    async fn refused(book: CommandBook) -> (String, Vec<(String, String)>) {
        let edge = Arc::new(KernelEdge::new());
        let unserved = edge
            .serve(book, Arrival::Loopback)
            .await
            .expect_err("refused");
        let AppError::Refused { code, args } = AppError::from_unserved(unserved) else {
            panic!("not a refusal");
        };
        (code, args.into_iter().collect())
    }

    #[tokio::test]
    async fn a_runs_refusal_is_told_as_the_runs_rejection() {
        let queue = runs::RunCommand::from(runs::QueueTurn {
            prompt_ref: "p".to_string(),
            resume: false,
            message_ids: Vec::new(),
            urgency: runs::TurnUrgency::Wake,
        });
        assert_eq!(
            refused(to("runs", queue)).await,
            ("runs-no-run".to_string(), Vec::new())
        );
    }

    #[tokio::test]
    async fn a_sessions_refusal_is_told_as_the_sessions_rejection() {
        let keep = sessions::SessionCommand::from(sessions::SetKept { kept: true });
        assert_eq!(
            refused(to("sessions", keep)).await,
            ("sessions-no-session".to_string(), Vec::new())
        );
    }

    #[tokio::test]
    async fn a_trees_refusal_is_told_as_the_trees_rejection() {
        let occupy = trees::TreeCommand::from(trees::OccupyTree {
            session: "s".to_string(),
        });
        assert_eq!(
            refused(to("trees", occupy)).await,
            ("trees-no-tree".to_string(), Vec::new())
        );
    }
}
