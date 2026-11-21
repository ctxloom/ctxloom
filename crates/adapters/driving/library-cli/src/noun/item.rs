//! `fragment` and `command`: list, show and `fragment premises`, and their
//! writes (B1), each json answer its AIP resource or method response
//! (OD-40, design §5.2).

use library_application::{ItemDistilled, ItemShow};

use crate::prelude::*;
use crate::read_back;

pub(crate) fn read(
    cx: &Context<'_>,
    kind: &str,
    (verb, sub): (&str, &ArgMatches),
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let command = kind == "command";
    match verb {
        // Text unless json is asked for by name (an agent's shell runs it
        // off a terminal for the body); json is the item's resource. Each
        // the daemon's read (step 10a U2), its text from the message (D10).
        "show" => {
            let under = library_api::names::reference_target(&text_arg(sub, "ref"));
            let prefer_distilled = flag(sub, "distilled");
            let show = |reference: &str, body: &str, distilled: bool, state: i32| {
                read_back::item_show(reference, body, distilled, state)
            };
            let text = |show: ItemShow, out: &mut dyn Write, notes: &mut Vec<String>| {
                let mut said = text::Notes(Vec::new());
                let written = text::item_show(out, &mut said, &show);
                notes.extend(said.0);
                written
            };
            if command {
                let request = v1::GetCommandRequest {
                    prefer_distilled,
                    caller: v1::Caller::of(cx.globals(), None),
                    ..v1::GetCommandRequest::default()
                };
                let read = cx.served.get_command(&under, request);
                return served(cx, read, out, err, true, |c, out, notes| {
                    text(
                        show(&c.reference, &c.body, c.body_is_distilled, c.distilled),
                        out,
                        notes,
                    )
                });
            }
            let request = v1::GetFragmentRequest {
                prefer_distilled,
                caller: v1::Caller::of(cx.globals(), None),
                ..v1::GetFragmentRequest::default()
            };
            let read = cx.served.get_fragment(&under, request);
            served(cx, read, out, err, true, |f, out, notes| {
                text(
                    show(&f.reference, &f.body, f.body_is_distilled, f.distilled),
                    out,
                    notes,
                )
            })
        }
        "premises" => {
            let request = v1::GetPremiseIndexRequest {
                caller: v1::Caller::of(cx.globals(), None),
                ..v1::GetPremiseIndexRequest::default()
            };
            let read = cx.served.get_premise_index("/premiseIndex", request);
            served(cx, read, out, err, false, |index, out, _| {
                text::premises(out, &read_back::premises(index))
            })
        }
        _ => listed_by_the_daemon(cx, kind, &text_arg(sub, "bundle"), out, err),
    }
}

/// `fragment|command list`, the daemon's `ListFragments` or `ListCommands`
/// (step 10a U2), its text from the message (D10). A listing of one bundle
/// that holds none asks the whole catalog's too, to say how many the other
/// bundles hold.
fn listed_by_the_daemon(
    cx: &Context<'_>,
    kind: &str,
    bundle: &str,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let under = |bundle: &str| match bundle {
        "" => String::new(),
        bundle => library_api::names::bundle_target(bundle),
    };
    let in_project = cx.served.in_project();
    let total = |filtered: usize, count: &dyn Fn() -> usize| match (bundle, filtered) {
        ("", n) => n,
        (_, 0) => count(),
        (_, n) => n,
    };
    let text = |items: Vec<(&str, &[String])>, total: usize, out: &mut dyn Write| {
        let listed = read_back::Listed {
            kind,
            items,
            bundle_filter: bundle,
            total,
            in_project,
        };
        text::item_list(out, &read_back::item_list(&listed))
    };
    if kind == "command" {
        let request = || v1::ListCommandsRequest {
            caller: v1::Caller::of(cx.globals(), None),
            ..v1::ListCommandsRequest::default()
        };
        let every = || {
            cx.served
                .list_commands("", request())
                .map_or(0, |(listed, _)| listed.commands.len())
        };
        let read = cx.served.list_commands(&under(bundle), request());
        return served(cx, read, out, err, false, |listed, out, _| {
            let items = listed
                .commands
                .iter()
                .map(|c| (c.reference.as_str(), c.tags.as_slice()))
                .collect();
            text(items, total(listed.commands.len(), &every), out)
        });
    }
    let request = || v1::ListFragmentsRequest {
        caller: v1::Caller::of(cx.globals(), None),
        ..v1::ListFragmentsRequest::default()
    };
    let every = || {
        cx.served
            .list_fragments("", request())
            .map_or(0, |(listed, _)| listed.fragments.len())
    };
    let read = cx.served.list_fragments(&under(bundle), request());
    served(cx, read, out, err, false, |listed, out, _| {
        let items = listed
            .fragments
            .iter()
            .map(|f| (f.reference.as_str(), f.tags.as_slice()))
            .collect();
        text(items, total(listed.fragments.len(), &every), out)
    })
}

pub(crate) fn write(
    cx: &Context<'_>,
    kind: ItemKind,
    (verb, sub): (&str, &ArgMatches),
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Option<Exit>, Failure> {
    let reference = text_arg(sub, "ref");
    let code = match verb {
        // Go's two-word form (`<bundle> <name>`) asks for Go's placeholder,
        // no editor; the reference form opens the editor (step 10b U3).
        "create" => {
            let (reference, placeholder) = match given(sub, "name") {
                Some(name) => (
                    format!(
                        "{}#{}/{name}",
                        text_arg(sub, "bundle"),
                        if kind == ItemKind::Command {
                            "commands"
                        } else {
                            "fragments"
                        }
                    ),
                    true,
                ),
                None => (text_arg(sub, "bundle"), false),
            };
            create_served(
                cx,
                kind,
                &reference,
                (given(sub, "body"), placeholder),
                out,
                err,
            )?
        }
        "remove" => remove_served(cx, kind, &reference, flag(sub, "yes"), out, err)?,
        "edit" => edit_served(
            cx,
            kind,
            &reference,
            (given(sub, "body"), flag(sub, "no-distill")),
            out,
            err,
        )?,
        "distill" => distill(cx, kind, &reference, flag(sub, "force"), out, err)?,
        _ => return Ok(None),
    };
    Ok(Some(code))
}

/// What a create said, in text (Go's words).
fn created_text(
    kind: &str,
    bundle: &str,
    name: &str,
    bundle_created: bool,
    out: &mut dyn Write,
) -> std::io::Result<()> {
    if bundle_created {
        writeln!(out, "Created bundle \"{bundle}\"")?;
    }
    writeln!(out, "Created {kind} \"{name}\" in bundle \"{bundle}\"")?;
    writeln!(
        out,
        "Edit with: ctxloom {kind} edit {bundle}#{}/{name}",
        dir_of(kind)
    )
}

/// The bundle and the item a resource name names
/// (`.../bundles/{b}/{dir}/{id}`).
fn bundle_and_item(name: &str) -> (String, String) {
    let rest = name.split_once("/bundles/").map_or(name, |(_, rest)| rest);
    let mut parts = rest.splitn(3, '/');
    let bundle = parts.next().unwrap_or_default().to_string();
    let item = parts.nth(1).unwrap_or_default().to_string();
    (bundle, item)
}

/// The answer to `needs`' Edit need, the editor launched on its document
/// (step 10b U3: the preview's Edit need, presented by the CLI); none
/// when it asks none, and none for an Edit with no launch (the caller was
/// not at a terminal): no editor is run, the apply goes unanswered and
/// the daemon answers the preview again, nothing applied (needs.proto: a CLI
/// off a terminal shows the preview and applies nothing).
pub(crate) fn edit_answers(
    cx: &Context<'_>,
    needs: &[v1::Need],
) -> Result<Vec<v1::Answer>, Failure> {
    let mut answers = Vec::new();
    for need in needs {
        if let Some(v1::need::Kind::Edit(edit)) = &need.kind {
            if edit.launch.is_none() {
                continue;
            }
            let edited = cx.served.edit_document(edit)?;
            answers.push(v1::Answer {
                need_id: need.id.clone(),
                answer: Some(v1::answer::Answer::Edited(edited)),
            });
        }
    }
    Ok(answers)
}

/// `fragment|command create <bundle>#<dir>/<id> [--body]`: the daemon's
/// `CreateFragment` or `CreateCommand` (step 10b U3). With no body, its
/// preview's Edit need is presented (the editor launched here), and the
/// apply sends what the editor left.
fn create_served(
    cx: &Context<'_>,
    kind: ItemKind,
    reference: &str,
    (body, placeholder): (Option<String>, bool),
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let (bundle, path) = reference.split_once('#').unwrap_or((reference, ""));
    let id = path.split_once('/').map_or(path, |(_, id)| id).to_string();
    let under = library_api::names::bundle_target(bundle);
    // Go's placeholder asks no editor, so nothing to preview for.
    let previewed = body.is_none() && !placeholder;
    let word = if kind == ItemKind::Command {
        "command"
    } else {
        "fragment"
    };
    let text = |name: &str, bundle_created: bool, out: &mut dyn Write| {
        let (bundle, item) = bundle_and_item(name);
        created_text(word, &bundle, &item, bundle_created, out)
    };
    if kind == ItemKind::Command {
        let ask = |validate_only: bool, answers: Vec<v1::Answer>| v1::CreateCommandRequest {
            command_id: id.clone(),
            command: Some(v1::Command {
                body: body.clone().unwrap_or_default(),
                ..v1::Command::default()
            }),
            placeholder,
            validate_only,
            answers,
            caller: v1::Caller::of(cx.globals(), cx.presentable()),
            ..v1::CreateCommandRequest::default()
        };
        let mut answers = Vec::new();
        if previewed {
            let preview = cx.served.create_command(&under, ask(true, Vec::new()))?;
            answers = edit_answers(cx, &preview.needs)?;
        }
        let made = cx.served.create_command(&under, ask(false, answers));
        return crate::answer_written(
            cx,
            made,
            |m| std::mem::take(&mut m.notices),
            out,
            err,
            |m, out| {
                let name = m.command.as_ref().map_or("", |c| c.name.as_str());
                text(name, m.bundle_created, out)
            },
            |_| Exit::OK,
        );
    }
    let ask = |validate_only: bool, answers: Vec<v1::Answer>| v1::CreateFragmentRequest {
        fragment_id: id.clone(),
        fragment: Some(v1::Fragment {
            body: body.clone().unwrap_or_default(),
            ..v1::Fragment::default()
        }),
        placeholder,
        validate_only,
        answers,
        caller: v1::Caller::of(cx.globals(), cx.presentable()),
        ..v1::CreateFragmentRequest::default()
    };
    let mut answers = Vec::new();
    if previewed {
        let preview = cx.served.create_fragment(&under, ask(true, Vec::new()))?;
        answers = edit_answers(cx, &preview.needs)?;
    }
    let made = cx.served.create_fragment(&under, ask(false, answers));
    crate::answer_written(
        cx,
        made,
        |m| std::mem::take(&mut m.notices),
        out,
        err,
        |m, out| {
            let name = m.fragment.as_ref().map_or("", |f| f.name.as_str());
            text(name, m.bundle_created, out)
        },
        |_| Exit::OK,
    )
}

/// What an edit said, in text (Go's words): nothing changed, or the item
/// updated and how to publish it.
fn edited_text(
    word: &str,
    name: &str,
    applied: bool,
    no_distill: bool,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> std::io::Result<()> {
    if !applied {
        return nothing(out);
    }
    let (bundle, item) = bundle_and_item(name);
    writeln!(out, "Updated {word} \"{item}\" in bundle \"{bundle}\"")?;
    writeln!(out)?;
    writeln!(out, "Bundle modified. To publish changes:")?;
    writeln!(out, "  ctxloom bundle push {bundle} [remote]")?;
    if no_distill {
        writeln!(err, "{PROG}: distilled form not refreshed (--no-distill)")?;
    }
    Ok(())
}

/// `fragment|command edit <ref> [--body] [--no-distill]`: the daemon's
/// `UpdateFragment` or `UpdateCommand` (step 10b U3). With no body, its
/// preview's Edit need is presented (the editor launched here, on the
/// body as it is), and the apply sends what the editor left.
fn edit_served(
    cx: &Context<'_>,
    kind: ItemKind,
    reference: &str,
    (body, no_distill): (Option<String>, bool),
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let under = library_api::names::reference_target(reference);
    let previewed = body.is_none();
    let word = if kind == ItemKind::Command {
        "command"
    } else {
        "fragment"
    };
    let mut said = Vec::new();
    let code = if kind == ItemKind::Command {
        let ask = |validate_only: bool, answers: Vec<v1::Answer>, etag: String| {
            v1::UpdateCommandRequest {
                command: Some(v1::Command {
                    body: body.clone().unwrap_or_default(),
                    no_distill: no_distill.then_some(true),
                    ..v1::Command::default()
                }),
                validate_only,
                answers,
                etag,
                caller: v1::Caller::of(cx.globals(), cx.presentable()),
                ..v1::UpdateCommandRequest::default()
            }
        };
        // The preview's etag goes back with the apply, so a file changed
        // while the editor was open refuses it stale (step 10b U3).
        let (mut answers, mut etag) = (Vec::new(), String::new());
        if previewed {
            let preview = cx
                .served
                .update_command(&under, ask(true, Vec::new(), String::new()))?;
            answers = edit_answers(cx, &preview.needs)?;
            etag = preview.etag;
        }
        let edited = cx.served.update_command(&under, ask(false, answers, etag));
        crate::answer_written(
            cx,
            edited,
            |m| std::mem::take(&mut m.notices),
            out,
            err,
            |m, out| {
                let name = m.command.as_ref().map_or("", |c| c.name.as_str());
                let applied = m.applied.unwrap_or(false);
                edited_text(word, name, applied, no_distill && applied, out, &mut said)
            },
            |_| Exit::OK,
        )?
    } else {
        let ask = |validate_only: bool, answers: Vec<v1::Answer>, etag: String| {
            v1::UpdateFragmentRequest {
                fragment: Some(v1::Fragment {
                    body: body.clone().unwrap_or_default(),
                    no_distill: no_distill.then_some(true),
                    ..v1::Fragment::default()
                }),
                validate_only,
                answers,
                etag,
                caller: v1::Caller::of(cx.globals(), cx.presentable()),
                ..v1::UpdateFragmentRequest::default()
            }
        };
        // The preview's etag goes back with the apply, so a file changed
        // while the editor was open refuses it stale (step 10b U3).
        let (mut answers, mut etag) = (Vec::new(), String::new());
        if previewed {
            let preview = cx
                .served
                .update_fragment(&under, ask(true, Vec::new(), String::new()))?;
            answers = edit_answers(cx, &preview.needs)?;
            etag = preview.etag;
        }
        let edited = cx.served.update_fragment(&under, ask(false, answers, etag));
        crate::answer_written(
            cx,
            edited,
            |m| std::mem::take(&mut m.notices),
            out,
            err,
            |m, out| {
                let name = m.fragment.as_ref().map_or("", |f| f.name.as_str());
                let applied = m.applied.unwrap_or(false);
                edited_text(word, name, applied, no_distill && applied, out, &mut said)
            },
            |_| Exit::OK,
        )?
    };
    err.write_all(&said).map_err(crate::output_failure)?;
    Ok(code)
}

/// `fragment|command remove <ref> [--yes]`: the daemon's `DeleteFragment`
/// or `DeleteCommand` (step 10b U3), previewed and confirmed at a terminal.
fn remove_served(
    cx: &Context<'_>,
    kind: ItemKind,
    reference: &str,
    yes: bool,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let under = library_api::names::reference_target(reference);
    let done = |name: &str, word: &str, out: &mut dyn Write| {
        let (bundle, item) = bundle_and_item(name);
        writeln!(out, "Removed {word} \"{item}\" from bundle \"{bundle}\"")
    };
    if kind == ItemKind::Command {
        return crate::write::removal_served(
            cx,
            yes,
            |validate_only, answers, etag| {
                let request = v1::DeleteCommandRequest {
                    validate_only,
                    answers,
                    etag,
                    caller: v1::Caller::of(cx.globals(), None),
                    ..v1::DeleteCommandRequest::default()
                };
                cx.served.delete_command(&under, request)
            },
            out,
            err,
            |gone, out| {
                let name = gone.command.as_ref().map_or("", |c| c.name.as_str());
                done(name, "command", out)
            },
        );
    }
    crate::write::removal_served(
        cx,
        yes,
        |validate_only, answers, etag| {
            let request = v1::DeleteFragmentRequest {
                validate_only,
                answers,
                etag,
                caller: v1::Caller::of(cx.globals(), None),
                ..v1::DeleteFragmentRequest::default()
            };
            cx.served.delete_fragment(&under, request)
        },
        out,
        err,
        |gone, out| {
            let name = gone.fragment.as_ref().map_or("", |f| f.name.as_str());
            done(name, "fragment", out)
        },
    )
}

/// `fragment|command distill <ref> [-f]`: the daemon's `DistillFragment`
/// or `DistillCommand` (step 10b, owner 2026-11-18): the daemon asks the
/// model as a run it hosts, which this command follows to its end; the
/// distilled form is written once the run ends, and its end is what the
/// write came to.
fn distill(
    cx: &Context<'_>,
    kind: ItemKind,
    reference: &str,
    force: bool,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let under = library_api::names::reference_target(reference);
    let distilled = distill_followed(cx, kind, &under, force).map_err(|(failure, said)| {
        say(cx, &said, err);
        failure
    });
    crate::answer_written(
        cx,
        distilled,
        |m| std::mem::take(m.notices_mut()),
        out,
        err,
        |m, out| distilled_text(&m.item(), out),
        |_| Exit::OK,
    )
}

/// What a distill call answers, the fragment's or the command's.
pub(crate) type DistillAnswer = Either<v1::DistillFragmentResponse, v1::DistillCommandResponse>;

impl DistillAnswer {
    /// The run that asks its model; empty when none is asked.
    fn run(&self) -> &str {
        match self {
            Either::Fragment(f) => &f.run,
            Either::Command(c) => &c.run,
        }
    }

    /// Its run ended answered: its form is written.
    fn applied(&mut self) {
        match self {
            Either::Fragment(f) => f.applied = Some(true),
            Either::Command(c) => c.applied = Some(true),
        }
    }

    pub(crate) fn notices_mut(&mut self) -> &mut Vec<v1::Notice> {
        match self {
            Either::Fragment(f) => &mut f.notices,
            Either::Command(c) => &mut c.notices,
        }
    }

    /// What it came to, as the text says it.
    pub(crate) fn item(&self) -> ItemDistilled {
        let (kind, name, path, detail) = match self {
            Either::Fragment(f) => (
                "fragment",
                f.fragment.as_ref().map_or("", |f| f.name.as_str()),
                &f.path,
                &f.detail,
            ),
            Either::Command(c) => (
                "command",
                c.command.as_ref().map_or("", |c| c.name.as_str()),
                &c.path,
                &c.detail,
            ),
        };
        let (bundle, name) = bundle_and_item(name);
        ItemDistilled {
            status: detail
                .first()
                .cloned()
                .unwrap_or_else(|| "distilled".to_string()),
            kind: kind.to_string(),
            bundle,
            name,
            path: path.clone(),
        }
    }

    /// Whether nothing was distilled for its being marked `no_distill`.
    pub(crate) fn no_distill(&self) -> bool {
        self.item().status == "no_distill"
    }
}

/// The distill of the item `under` asked of the daemon, and the run it was
/// answered with followed to its end: its answer, its form written; else
/// why not, with what was said before it.
pub(crate) fn distill_followed(
    cx: &Context<'_>,
    kind: ItemKind,
    under: &str,
    force: bool,
) -> Result<DistillAnswer, (Failure, Vec<v1::Notice>)> {
    // Its stops heard from before the daemon starts the run (ruling 114).
    cx.served.hear_stops();
    let asked = if kind == ItemKind::Command {
        let request = v1::DistillCommandRequest {
            force,
            caller: v1::Caller::of(cx.globals(), None),
            ..v1::DistillCommandRequest::default()
        };
        cx.served
            .distill_command(under, request)
            .map(Either::Command)
    } else {
        let request = v1::DistillFragmentRequest {
            force,
            caller: v1::Caller::of(cx.globals(), None),
            ..v1::DistillFragmentRequest::default()
        };
        cx.served
            .distill_fragment(under, request)
            .map(Either::Fragment)
    };
    let mut answer = asked.map_err(|failure| (failure, Vec::new()))?;
    if answer.run().is_empty() {
        return Ok(answer);
    }
    let followed = cx.served.follow_run(answer.run());
    answer.notices_mut().extend(followed.notices);
    match followed.ended {
        Ok(()) => {
            answer.applied();
            Ok(answer)
        }
        Err(failure) => Err((failure, std::mem::take(answer.notices_mut()))),
    }
}

/// `notices` said, as a write's are.
pub(crate) fn say(cx: &Context<'_>, notices: &[v1::Notice], err: &mut dyn Write) {
    let said: Vec<library_application::Notice> =
        notices.iter().map(library_api::reads::said).collect();
    say_notices(&said, PROG, cx.format, cx.catalog, err);
}

/// What one item's distillation said, in text.
pub(crate) fn distilled_text(done: &ItemDistilled, out: &mut dyn Write) -> std::io::Result<()> {
    match done.status.as_str() {
        "no_distill" => writeln!(
            out,
            "{} \"{}\" in bundle \"{}\" is marked as no_distill: nothing distilled",
            done.kind, done.name, done.bundle
        ),
        "fresh" => writeln!(
            out,
            "{} \"{}\" in bundle \"{}\": its distilled form matches its source (-f distills it again)",
            done.kind, done.name, done.bundle
        ),
        _ => writeln!(
            out,
            "Distilled {} \"{}\" in bundle \"{}\" ({})",
            done.kind, done.name, done.bundle, done.path
        ),
    }
}

/// A fragment's message or a command's, as the kind is.
#[derive(serde::Serialize)]
#[serde(untagged)]
pub(crate) enum Either<F, C> {
    Fragment(F),
    Command(C),
}

/// Whether `kind` (the use case's word or its directory) is a command's.
fn is_command(kind: &str) -> bool {
    matches!(kind, "command" | "commands" | "prompt" | "prompts")
}

/// The directory a kind's items live under in a reference.
fn dir_of(kind: &str) -> &'static str {
    if is_command(kind) {
        "commands"
    } else {
        "fragments"
    }
}

#[cfg(test)]
#[path = "item.test.rs"]
mod tests;
