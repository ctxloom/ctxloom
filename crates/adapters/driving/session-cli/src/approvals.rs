//! `session approvals` (step 8 U4, ruling 142): the permission asks of the
//! sessions the user's daemon runs, listed and answered by the user (S25).
//!
//! ```text
//! ctxloom session approvals [<harp>]                     (this project's waiting asks; under a root)
//! ctxloom session approvals answer <ask|harp> <option> ["<message>"] [--yes]
//! ```
//!
//! Each is a client of the daemon ([`daemon_api::ApprovalAnswers`]). An
//! `allow-always` of a claude ask also writes its rule into the asking
//! agent's permissions in the config ([`AlwaysRules`], owner Q3): the ask is
//! checked waiting, the rule written, then the ask answered; off a terminal
//! without `--yes` it previews and changes nothing (exit 1 under
//! `--detailed-exitcode`), at a terminal it asks first. A rule written for
//! an ask that was settled before its answer reached it stays, and is said
//! (`daemon-api-approval-rule-kept`, exit 20).

use std::io::Write;

use angzarr_presentation::{write_json, Format};
use daemon_api::{
    settled_of, settled_refusal, ApiRejection, ApprovalAnswers, CallError, Rejection,
};
use daemon_proto::v1;
use serde::Serialize;

use crate::error::CliFault;
use crate::run::{call_failure, fault, wrote, Done, Failure};
use crate::text::Text;
use crate::{AnswerArgs, Approvals, ApprovalsVerb, Asking, Session, SessionVerb};

/// What an `allow-always` writes, or would: as the config's writer says it.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
pub struct AlwaysPlan {
    /// The config file.
    pub path: String,
    /// `agents.<agent>.engines.<engine>.permissions`.
    pub key: String,
    pub rule: String,
    /// The permissions document as written, JSON text.
    pub document: String,
}

/// Where an `allow-always`'s rule is written: the config, through the
/// library's writer (the root composes it).
pub trait AlwaysRules {
    /// `rule` written into `agent`'s permissions for `engine` (`apply`), or
    /// what would be.
    fn allow_always(
        &self,
        agent: &str,
        engine: &str,
        rule: &str,
        apply: bool,
    ) -> Result<AlwaysPlan, Failure>;
}

/// The preview of an `allow-always`, as JSON says it.
#[derive(Serialize)]
struct Preview<'a> {
    status: &'static str,
    applied: bool,
    ask: &'a str,
    #[serde(flatten)]
    plan: &'a AlwaysPlan,
    apply: String,
}

/// Whether `session`'s command is `session approvals ...`, which
/// [`approvals`] runs.
pub fn is_approvals(session: &Session) -> bool {
    matches!(session.verb, Some(SessionVerb::Approvals(_)))
}

/// Run `session approvals ...` on the user's `answers`, an `allow-always`'s
/// rule written by `rules` and, at a terminal, asked of `asking` first.
pub fn approvals<A: ApprovalAnswers>(
    session: &Session,
    answers: &A,
    rules: &dyn AlwaysRules,
    asking: &dyn Asking,
    format: Format,
    out: &mut dyn Write,
) -> Result<Done, Failure> {
    let Some(SessionVerb::Approvals(command)) = &session.verb else {
        unreachable!("only `session approvals`: session_cli::is_approvals")
    };
    // Time and I/O: the daemon is called over the network.
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .map_err(|e| {
            fault(CliFault::Runtime {
                ext_err_txt: e.to_string(),
            })
        })?;
    runtime.block_on(async {
        match &command.verb {
            None => listed(command, answers, format, out).await,
            Some(ApprovalsVerb::Answer(args)) => {
                answered(args, answers, rules, asking, format, out).await
            }
        }
    })
}

async fn listed<A: ApprovalAnswers>(
    command: &Approvals,
    answers: &A,
    format: Format,
    out: &mut dyn Write,
) -> Result<Done, Failure> {
    let listed = answers
        .list_approvals(command.harp.as_deref())
        .await
        .map_err(call_failure)?;
    wrote(match format {
        Format::Json => write_json(out, &listed),
        Format::Text => listing_text(&listed, out),
    })?;
    Ok(Done::default())
}

async fn answered<A: ApprovalAnswers>(
    args: &AnswerArgs,
    answers: &A,
    rules: &dyn AlwaysRules,
    asking: &dyn Asking,
    format: Format,
    out: &mut dyn Write,
) -> Result<Done, Failure> {
    let approval = answers.approval(&args.target).await.map_err(call_failure)?;
    let ask = daemon_api::ask_of(&approval).to_string();
    let message = args.message.clone().unwrap_or_default();
    let always = approval
        .options
        .iter()
        .find(|option| option.id == args.option && option.kind == "allow_always")
        .filter(|option| !option.rule.is_empty());
    let mut kept = None;
    if let Some(option) = always {
        // Q3's order: the ask is checked waiting, the rule written, then
        // the ask answered.
        if let Some(settled) = settled_refusal(&approval) {
            return Err(call_failure(settled));
        }
        let plan = rules.allow_always(&approval.agent, &approval.engine, &option.rule, false)?;
        if !args.yes {
            let apply = format!(
                "ctxloom session approvals answer {ask} {} --yes",
                args.option
            );
            wrote(match format {
                Format::Json => write_json(
                    out,
                    &Preview {
                        status: "preview",
                        applied: false,
                        ask: &ask,
                        plan: &plan,
                        apply: apply.clone(),
                    },
                ),
                Format::Text => preview_text(&ask, &plan, &apply, out),
            })?;
            let question = Text::AlwaysQuestion {
                rule: plan.rule.clone(),
                path: plan.path.clone(),
            }
            .said();
            if !(asking.is_terminal() && asking.confirm(&question)) {
                return Ok(Done {
                    would_change: !asking.is_terminal(),
                    ..Done::default()
                });
            }
        }
        kept = Some(rules.allow_always(&approval.agent, &approval.engine, &option.rule, true)?);
    }
    let answered = match answers.answer(&approval, &args.option, &message).await {
        Ok(answered) => answered,
        Err(error) => {
            // A rule written for an ask settled meanwhile stays, and is said.
            return Err(call_failure(match (settled_of(&error), kept) {
                (Some((ask_id, how)), Some(plan)) => {
                    CallError::Refused(Rejection::Api(ApiRejection::ApprovalRuleKept {
                        ask_id,
                        how,
                        rule: plan.rule,
                        path: plan.path,
                    }))
                }
                _ => error,
            }));
        }
    };
    wrote(match format {
        Format::Json => write_json(out, &answered.approval),
        Format::Text => match &kept {
            Some(plan) => writeln!(
                out,
                "{}",
                Text::AnsweredWritten {
                    ask: ask.clone(),
                    option: args.option.clone(),
                    rule: plan.rule.clone(),
                    path: plan.path.clone(),
                }
                .said()
            ),
            None => writeln!(
                out,
                "{}",
                Text::Answered {
                    ask: ask.clone(),
                    option: args.option.clone(),
                }
                .said()
            ),
        },
    })?;
    Ok(Done::default())
}

/// The words a claude option is shown with (texts.md §6), or an
/// ACP option's own label.
fn label(option: &v1::ApprovalOption, approval: &v1::Approval) -> String {
    if !option.label.is_empty() {
        return option.label.clone();
    }
    let text = match option.id.as_str() {
        "allow-once" => Text::LabelAllowOnce,
        "allow-session" => Text::LabelAllowSession {
            rule: option.rule.clone(),
        },
        "allow-always" => Text::LabelAllowAlways {
            rule: option.rule.clone(),
            agent: approval.agent.clone(),
            engine: approval.engine.clone(),
        },
        "deny" => Text::LabelDeny,
        other => return other.to_string(),
    };
    text.said()
}

/// The harp a session's resource name ends with.
fn harp_of(name: &str) -> &str {
    name.rsplit('/').next().unwrap_or_default()
}

/// The listing in text (texts.md §6): each ask, what it asks to
/// run, its options and how to answer it.
fn listing_text(listed: &v1::ListApprovalsResponse, out: &mut dyn Write) -> std::io::Result<()> {
    if listed.approvals.is_empty() {
        return writeln!(out, "{}", Text::NoneWaiting.said());
    }
    for approval in &listed.approvals {
        let ask = daemon_api::ask_of(approval);
        let asks = Text::Asks {
            child: harp_of(&approval.child).to_string(),
            tool: approval.tool_name.clone(),
            input: approval.input_excerpt.clone(),
        };
        writeln!(out, "{}", asks.said())?;
        let (ask, root) = (ask.to_string(), harp_of(&approval.root).to_string());
        let under = match approval.expire_time.as_ref() {
            Some(t) => Text::AskUnderUntil {
                ask: ask.clone(),
                root,
                seconds: t.seconds,
            },
            None => Text::AskUnder {
                ask: ask.clone(),
                root,
            },
        };
        writeln!(out, "  {}", under.said())?;
        for option in &approval.options {
            writeln!(out, "  {:<15} {}", option.id, label(option, approval))?;
        }
        writeln!(out, "  {}", Text::AnswerWith { ask }.said())?;
    }
    Ok(())
}

/// An `allow-always`'s preview in text.
fn preview_text(
    ask: &str,
    plan: &AlwaysPlan,
    apply: &str,
    out: &mut dyn Write,
) -> std::io::Result<()> {
    let would = Text::AlwaysWould {
        ask: ask.to_string(),
        rule: plan.rule.clone(),
        path: plan.path.clone(),
    };
    writeln!(out, "{}", would.said())?;
    writeln!(out, "  {}: {}", plan.key, plan.document)?;
    let apply = Text::AlwaysApply {
        apply: apply.to_string(),
    };
    writeln!(out, "{}", apply.said())
}

#[cfg(test)]
#[path = "approvals.test.rs"]
mod tests;
