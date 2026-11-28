//! `run --one-shot` as a client of the user's daemon (U4, T7): the daemon
//! plans the run, hosts its engine and streams back what it noticed, the
//! reply and the end; the command prints them. Nothing here opens the store
//! (T1) or starts an engine.
//!
//! In order: `CreateRun{ONE_SHOT}` with the line's whole request, the
//! caller's whole environment (S7) and its working directory, answered once
//! the run is placed (or ended before that: its `end`); then `WatchRun`
//! until the end. A signal that asks ctxloom to stop (ruling 114; the
//! caller says which) is sent as the run's `runs.RequestStop`, keyed by the
//! run (`stop:<run>`), so a second is a DUPLICATE and changes nothing; the
//! daemon stops the engine and the run ends `run-application-stopped`, the
//! command exiting 128 plus the signal's number. A signal heard before the
//! run is placed is sent once it is.
//!
//! A command that vanishes without a signal (a SIGKILL, a crash) leaves the
//! run to finish: the daemon runs it to its end (owner s4-Q7), its reply in
//! the run's facts and its words in the transcript, for nobody.
//!
//! What the daemon says of a diagnostic crosses as its code and its args,
//! flattened as angzarr flattens a cause on its wire ([`flat_args`]), and is
//! rebuilt here ([`report_of`]) for the command's own catalog to render: an
//! arg that reads as an integer is rebuilt as one (so a plural selects), and
//! a wrapped cause as a nested report.

use std::collections::BTreeMap;
use std::future::Future;
use std::pin::Pin;
use std::task::Poll;

use angzarr_embedded::v1::Cover;
use coded_diagnostic::{Arg, Diagnostic, Report};
use daemon_ports::{Called, DaemonApi};
use daemon_proto::v1;
use exit_code::{Exit, ExitClass};
use library_application::{Flags, Notice};
use sessions::SessionName;

use crate::error::{HostedEnd, RunError};
use crate::one_shot::{Answer, Done, OneShot};

/// A one-shot, as the daemon is asked to host it.
#[derive(Clone, Debug, Default)]
pub struct Asked {
    /// The project's resource name (`projects/{harp}`), or `users/me`
    /// outside one.
    pub parent: String,
    /// The caller's working directory.
    pub cwd: String,
    /// The caller's whole environment (S7), held by the daemon for the
    /// run's life and never kept.
    pub env: BTreeMap<String, String>,
    pub flags: Flags,
    pub request: OneShot,
    /// The request's id (AIP-155).
    pub request_id: String,
    /// Which client asks (its kind, program and version stamp).
    pub client: v1::Client,
}

/// The signal that asks the run to stop, once one is heard (its name:
/// `SIGHUP`, `SIGINT`, `SIGTERM`); pending until then.
pub type Heard<'a> = Pin<Box<dyn Future<Output = String> + Send + 'a>>;

/// `asked` as the `CreateRun` request.
pub fn create_request(asked: &Asked) -> v1::CreateRunRequest {
    let request = &asked.request;
    v1::CreateRunRequest {
        parent: asked.parent.clone(),
        mode: v1::run::Mode::OneShot as i32,
        prompt: request.run.prompt.clone(),
        agent: request.run.agent.clone(),
        engine: request.engine.clone(),
        profiles: Some(request.run.profile.clone())
            .filter(|profile| !profile.is_empty())
            .into_iter()
            .collect(),
        session: request.session.clone(),
        fragments: request.run.fragments.clone(),
        tags: request.run.tags.clone(),
        command: request.run.saved_command.clone(),
        workspace: request.run.workspace.clone(),
        no_context: request.run.no_context,
        request_id: asked.request_id.clone(),
        caller: Some(v1::Caller {
            cwd: asked.cwd.clone(),
            env: Some(v1::EnvVars {
                vars: asked
                    .env
                    .iter()
                    .map(|(name, value)| v1::EnvVar {
                        name: name.clone(),
                        value: value.clone(),
                    })
                    .collect(),
            }),
            globals: Some(v1::Globals {
                degraded: asked.flags.degraded,
                no_companions: asked.flags.no_companions,
                config_set: asked.flags.config_set.clone(),
            }),
            presentable: None,
            client: Some(asked.client.clone()),
        }),
        ..v1::CreateRunRequest::default()
    }
}

/// The one-shot a `CreateRun` request asks for, and its flags: what the
/// daemon plans (the inverse of [`create_request`]).
pub fn asked_of(request: &v1::CreateRunRequest) -> (OneShot, Flags) {
    let globals = request
        .caller
        .as_ref()
        .and_then(|caller| caller.globals.clone())
        .unwrap_or_default();
    let one_shot = OneShot {
        run: library_application::RunRequest {
            agent: request.agent.clone(),
            profile: request.profiles.first().cloned().unwrap_or_default(),
            fragments: request.fragments.clone(),
            tags: request.tags.clone(),
            engine: request.engine.clone(),
            engine_named: false,
            prompt: request.prompt.clone(),
            saved_command: request.command.clone(),
            workspace: request.workspace.clone(),
            no_context: request.no_context,
        },
        engine: request.engine.clone(),
        session: request.session.clone(),
        distill: None,
    };
    let flags = Flags {
        degraded: globals.degraded,
        config_set: globals.config_set,
        no_companions: globals.no_companions,
        trusted: false,
    };
    (one_shot, flags)
}

/// `d`'s args as the wire carries them: each in order, an integer as its
/// digits, a wrapped cause as its code under the arg's name and each of
/// its own args under `<name>.<arg>` (angzarr's flattening, decision 14.37).
pub fn flat_args(d: &dyn Diagnostic) -> Vec<v1::NeedArg> {
    let mut flat = Vec::new();
    flatten(d, "", &mut flat);
    flat
}

fn flatten(d: &dyn Diagnostic, prefix: &str, flat: &mut Vec<v1::NeedArg>) {
    for (name, arg) in d.args() {
        let name = format!("{prefix}{name}");
        let value = match arg {
            Arg::Int(n) => n.to_string(),
            Arg::Uint(n) => n.to_string(),
            Arg::Str(text) => text,
            Arg::Nested(cause) => {
                flat.push(v1::NeedArg {
                    name: name.clone(),
                    value: cause.code().to_string(),
                });
                flatten(cause.as_ref(), &format!("{name}."), flat);
                continue;
            }
        };
        flat.push(v1::NeedArg { name, value });
    }
}

/// The diagnostic `code` with the flattened `args` ([`flat_args`]), rebuilt:
/// an arg that reads back as an integer is one, and an arg whose name
/// others extend (`<name>.<arg>`) is the cause it wrapped.
pub fn report_of(code: &str, args: &[v1::NeedArg]) -> Report {
    let pairs: Vec<(&str, &str)> = args
        .iter()
        .map(|arg| (arg.name.as_str(), arg.value.as_str()))
        .collect();
    rebuilt(code, &pairs)
}

fn rebuilt(code: &str, pairs: &[(&str, &str)]) -> Report {
    let mut report = Report::new(code.to_string());
    for (index, (name, value)) in pairs.iter().enumerate() {
        if name.contains('.') {
            continue;
        }
        let prefix = format!("{name}.");
        let inner: Vec<(&str, &str)> = pairs[index + 1..]
            .iter()
            .filter_map(|(n, v)| n.strip_prefix(prefix.as_str()).map(|rest| (rest, *v)))
            .collect();
        report = match inner.is_empty() {
            true => report.arg(name.to_string(), integer_or_text(value)),
            false => report.arg(
                name.to_string(),
                Arg::Nested(Box::new(rebuilt(value, &inner))),
            ),
        };
    }
    report
}

/// `value` as an arg: an integer where it is one's canonical digits, else
/// its text.
fn integer_or_text(value: &str) -> Arg {
    if let Ok(n) = value.parse::<i64>() {
        if n.to_string() == value {
            return Arg::Int(n);
        }
    }
    if let Ok(n) = value.parse::<u64>() {
        if n.to_string() == value {
            return Arg::Uint(n);
        }
    }
    Arg::Str(value.to_string())
}

/// The exit a hosted run's `exit` says, by its block (rust-support's
/// convention): 0-9 a success, 10-19 a failure, 20-29 a decline, 128 and
/// over a signal; anything else a failure.
pub fn exit_of(code: i32) -> Exit {
    match u8::try_from(code) {
        Ok(0 | 16) => Exit::OK,
        Ok(n @ 17..=31) => Exit::success(n),
        Ok(n @ 32..=47) => Exit::failed(n),
        Ok(n @ 48..=63) => Exit::declined(n),
        Ok(n) if n > 128 => Exit::signal(n - 128),
        _ => Exit::FAILED,
    }
}

/// The wire's end of a run that ended in `error`: its exit, its code and
/// args, its display (code and args, never English, decision 14.37) and its
/// refusals.
pub fn end_of(error: &RunError) -> v1::RunEnd {
    v1::RunEnd {
        exit: i32::from(error.exit().code(true)),
        code: error.code().to_string(),
        args: flat_args(error),
        text: error.to_string(),
        refusals: error
            .refusals()
            .iter()
            .map(|refusal| v1::RunEnd {
                exit: 0,
                code: refusal.code().to_string(),
                args: flat_args(refusal.as_ref()),
                text: Report::of(refusal.as_ref()).to_string(),
                refusals: Vec::new(),
            })
            .collect(),
    }
}

/// The end of a run that answered.
pub fn answered() -> v1::RunEnd {
    v1::RunEnd::default()
}

/// The run's error an end says, none for a run that answered (exit 0, no
/// code).
pub fn error_of(end: &v1::RunEnd) -> Option<RunError> {
    if end.code.is_empty() && end.exit == 0 {
        return None;
    }
    Some(RunError::Hosted(Box::new(HostedEnd {
        report: report_of(&end.code, &end.args),
        exit: exit_of(end.exit),
        refusals: end
            .refusals
            .iter()
            .map(|refusal| report_of(&refusal.code, &refusal.args))
            .collect(),
    })))
}

/// The wire's notice of `notice`.
pub fn notice_of(notice: &Notice) -> v1::Notice {
    v1::Notice {
        code: notice.code().to_string(),
        args: flat_args(notice),
        text: notice.to_string(),
    }
}

/// A notice the daemon said, as the command says it: in its own code and
/// args, rendered by the command's catalog.
pub fn noticed(notice: &v1::Notice) -> Notice {
    Notice::Engine(Box::new(report_of(&notice.code, &notice.args)))
}

/// What a one-shot's events came to, gathered as they come.
#[derive(Default)]
struct Followed {
    notices: Vec<Notice>,
    reply: Option<String>,
    end: Option<v1::RunEnd>,
}

impl Followed {
    /// Take `event`; whether it ended the run.
    fn take(&mut self, event: v1::RunEvent) -> bool {
        match event.event {
            Some(v1::run_event::Event::Notice(notice)) => self.notices.push(noticed(&notice)),
            Some(v1::run_event::Event::Reply(reply)) => self.reply = Some(reply),
            Some(v1::run_event::Event::End(end)) => {
                self.end = Some(end);
                return true;
            }
            Some(v1::run_event::Event::Stop(_)) | None => {}
        }
        false
    }
}

/// Which of two futures was ready first.
enum First<A, B> {
    Left(A),
    Right(B),
}

/// Poll `left` and `right` until one is ready; the other is left as it was,
/// to be polled again.
async fn first<A, B>(
    left: &mut Pin<Box<dyn Future<Output = A> + Send + '_>>,
    right: &mut Pin<Box<dyn Future<Output = B> + Send + '_>>,
) -> First<A, B> {
    std::future::poll_fn(|cx| {
        if let Poll::Ready(a) = left.as_mut().poll(cx) {
            return Poll::Ready(First::Left(a));
        }
        if let Poll::Ready(b) = right.as_mut().poll(cx) {
            return Poll::Ready(First::Right(b));
        }
        Poll::Pending
    })
    .await
}

/// The run's stream named by `run` (`projects/{p}/runs/{root}`).
fn run_cover(run: &str) -> Cover {
    let root = run.rsplit('/').next().unwrap_or_default();
    let id = uuid::Uuid::parse_str(root).unwrap_or_default();
    Cover {
        domain: runs::RUN_DOMAIN.to_string(),
        root: Some(angzarr_embedded::v1::Uuid::from_bytes(*id.as_bytes())),
        edition: None,
    }
}

/// Ask the daemon to stop the run `run` for `signal` (API Q11: the run's
/// `RequestStop` through the coordinator's `Handle`, keyed by the run, so a
/// second is a DUPLICATE). What it
/// came to, as a notice when it did not reach the daemon: the engine then
/// goes on under the daemon until its turn ends (s4-Q7).
async fn request_stop<A: DaemonApi>(api: &A, run: &str, signal: &str) -> Option<Notice> {
    let command = runs::RequestStop {
        reason: signal.to_string(),
    };
    let book = sessions::keyed_book(run_cover(run), &command, &format!("stop:{run}"));
    match api.handle(book).await {
        Ok(_) => None,
        Err(called) => Some(Notice::text(format!(
            "the stop ({signal}) could not be delivered to the daemon ({called}); the run goes on under the daemon until its turn ends"
        ))),
    }
}

/// `run --one-shot` through `api`, the daemon reached: the run created,
/// followed to its end, stopped when `heard` is (once). Its answer, or the
/// error it ended in; a signal heard makes it `Stopped`, whatever it ended in
/// (ruling 114).
pub async fn one_shot<A: DaemonApi>(api: &A, asked: &Asked, heard: Heard<'_>) -> Done<Answer> {
    let mut heard = heard;
    let mut signal: Option<String> = None;
    let mut creating: Pin<Box<dyn Future<Output = Result<v1::Run, Called>> + Send + '_>> =
        Box::pin(api.create_run(create_request(asked)));
    let created = loop {
        match first(&mut creating, &mut heard).await {
            First::Left(created) => break created,
            First::Right(name) => {
                signal = Some(name);
                heard = Box::pin(std::future::pending());
            }
        }
    };
    match created {
        Err(called) => {
            let mut result = Err(RunError::Called(Box::new(called)));
            if let Some(signal) = &signal {
                overrule(&mut result, signal);
            }
            Done {
                result,
                notices: Vec::new(),
            }
        }
        Ok(run) => followed(api, &run, heard, signal).await,
    }
}

/// The run `run` the daemon answered (a one-shot's `CreateRun`, or a
/// distill's, step 10b) followed through `api` to its end, stopped when
/// `heard` is (once); `signal` one heard already. Its answer, or the error
/// it ended in; a signal heard makes it `Stopped`, whatever it ended in
/// (ruling 114).
pub async fn followed<A: DaemonApi>(
    api: &A,
    run: &v1::Run,
    heard: Heard<'_>,
    signal: Option<String>,
) -> Done<Answer> {
    let (mut heard, mut signal) = (heard, signal);
    let mut followed = Followed::default();
    followed.notices.extend(run.notices.iter().map(noticed));
    let result = match &run.end {
        Some(end) => {
            followed.end = Some(end.clone());
            Ok(())
        }
        None => follow(api, run, &mut heard, &mut signal, &mut followed).await,
    };
    let mut result = match result {
        Err(error) => Err(error),
        Ok(()) => match error_of(&followed.end.clone().unwrap_or_default()) {
            Some(error) => Err(error),
            None => reply_of(Some(run), &followed).ok_or(RunError::EmptyAnswer),
        },
    };
    if let Some(signal) = &signal {
        overrule(&mut result, signal);
    }
    Done {
        result,
        notices: followed.notices,
    }
}

/// Follow `run` to its end into `followed`, stopping it when `heard` is
/// (or was, `signal`).
async fn follow<A: DaemonApi>(
    api: &A,
    run: &v1::Run,
    heard: &mut Heard<'_>,
    signal: &mut Option<String>,
    followed: &mut Followed,
) -> Result<(), RunError> {
    if let Some(name) = signal.clone() {
        followed
            .notices
            .extend(request_stop(api, &run.name, &name).await);
    }
    let mut events = api
        .watch_run(v1::WatchRunRequest {
            name: run.name.clone(),
        })
        .await
        .map_err(|called| RunError::Called(Box::new(called)))?;
    loop {
        let event = {
            let mut next: Pin<Box<dyn Future<Output = _> + Send + '_>> = events.next();
            match first(&mut next, heard).await {
                First::Left(event) => event,
                First::Right(name) => {
                    drop(next);
                    *heard = Box::pin(std::future::pending());
                    followed
                        .notices
                        .extend(request_stop(api, &run.name, &name).await);
                    *signal = Some(name);
                    continue;
                }
            }
        };
        match event {
            None => {
                return Err(RunError::Called(Box::new(Called::Unreachable {
                    detail: "the run's stream ended before the run did".to_string(),
                })))
            }
            Some(Err(called)) => return Err(RunError::Called(Box::new(called))),
            Some(Ok(event)) => {
                if followed.take(event) {
                    return Ok(());
                }
            }
        }
    }
}

/// The reply the run came to, under the session `run` names; none when it
/// gave none.
fn reply_of(run: Option<&v1::Run>, followed: &Followed) -> Option<Answer> {
    let reply = followed.reply.clone()?;
    let (harp, session) = run
        .and_then(|run| session_of(&run.session))
        .unwrap_or_default();
    let engine = run
        .map(|run| {
            run.engine
                .rsplit('/')
                .next()
                .unwrap_or_default()
                .to_string()
        })
        .unwrap_or_default();
    Some(Answer {
        harp,
        session,
        engine,
        answer: reply,
    })
}

/// Make the run's result what the signal `signal` says: stopped, with the
/// status a shell gives a process that signal ended.
fn overrule<T>(result: &mut Result<T, RunError>, signal: &str) {
    *result = Err(RunError::Stopped {
        signal: signal.to_string(),
        status: status_of(signal),
    });
}

/// The status a shell gives a process `signal` ended: 128 plus its number
/// (SIGHUP 1, SIGINT 2, SIGTERM 15); any other the hangup's.
pub fn status_of(signal: &str) -> u8 {
    match signal {
        "SIGINT" => 130,
        "SIGTERM" => 143,
        _ => 129,
    }
}

/// The session `name` names, as an answer gives it: its harp and its stable
/// id; none for a name that is no session's.
pub fn session_of(name: &str) -> Option<(String, String)> {
    SessionName::parse(name)
        .ok()
        .map(|name| (name.harp().to_string(), name.uid()))
}

#[cfg(test)]
#[path = "client.test.rs"]
mod tests;
