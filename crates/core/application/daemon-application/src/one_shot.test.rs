//! The one-shots the daemon hosts (U4, T7): created and answered once
//! placed or ended, watched from their first event, stopped once. The host
//! here tells what it is scripted to inside its own call, so every order is
//! forced, none waited for (the race rule).

use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use daemon_proto::v1;
use run_application::{Answer, Done, Placement, RunError};

use super::*;

/// `future`'s output, polled once: what the tests here await (a one-shot's
/// end, a reaction) never waits on anything.
fn now<F: std::future::Future>(future: F) -> F::Output {
    let mut future = std::pin::pin!(future);
    let mut cx = std::task::Context::from_waker(std::task::Waker::noop());
    match future.as_mut().poll(&mut cx) {
        std::task::Poll::Ready(output) => output,
        std::task::Poll::Pending => panic!("waited on something"),
    }
}

/// What the scripted host does inside `host`.
#[derive(Clone, Copy)]
enum Script {
    /// Placed, and still running: the test ends it through the kept `Told`.
    Placed,
    /// Placed, then answered at once.
    Answers,
    /// Refused before it was placed.
    Refused,
}

/// A host that follows its script, keeps the `Told` of each one-shot and
/// counts the stops asked of each.
struct Scripted {
    script: Script,
    told: Arc<Mutex<Vec<Told>>>,
    stops: Arc<AtomicUsize>,
    asked: Arc<Mutex<Vec<HostRequest>>>,
}

struct Control(Arc<AtomicUsize>);

impl OneShotControl for Control {
    fn stop(&self) {
        self.0.fetch_add(1, Ordering::SeqCst);
    }
}

fn placement(n: u8) -> Placement {
    Placement {
        run: format!("0195f0a4-7b1e-7000-8000-00000000000{n}"),
        session: format!("s{n}"),
        name: format!("projects/calm-otter/sessions/harp-{n}"),
        harp: format!("harp-{n}"),
        engine: "claude-code".to_string(),
        agent: format!("agent-{n}"),
        approval_timeout: Duration::from_secs(60 * u64::from(n)),
        // The third is the human's terminal session.
        interactive: n == 3,
    }
}

fn answer(text: &str) -> Done<Answer> {
    Done {
        result: Ok(Answer {
            harp: "h".to_string(),
            session: "s".to_string(),
            engine: "claude-code".to_string(),
            answer: text.to_string(),
        }),
        notices: vec![library_application::Notice::text("noticed")],
    }
}

impl OneShotHost for Scripted {
    fn host(&self, request: HostRequest, told: Told) -> Box<dyn OneShotControl> {
        let n = u8::try_from(self.asked.lock().unwrap().len() + 1).unwrap();
        self.asked.lock().unwrap().push(request);
        match self.script {
            Script::Placed => told.placed(&placement(n)),
            Script::Answers => {
                told.placed(&placement(n));
                now(told.done(answer("HELLO")));
            }
            Script::Refused => now(told.done(Done {
                result: Err(RunError::Refused(runs::RunRejection::NoPrompt)),
                notices: vec![library_application::Notice::text("before")],
            })),
        }
        self.told.lock().unwrap().push(told);
        Box::new(Control(Arc::clone(&self.stops)))
    }
}

fn hosting(script: Script) -> (OneShots, Arc<Mutex<Vec<Told>>>, Arc<AtomicUsize>) {
    let told = Arc::new(Mutex::new(Vec::new()));
    let stops = Arc::new(AtomicUsize::new(0));
    let one_shots = OneShots::new(Box::new(Scripted {
        script,
        told: Arc::clone(&told),
        stops: Arc::clone(&stops),
        asked: Arc::new(Mutex::new(Vec::new())),
    }));
    (one_shots, told, stops)
}

/// At most this many of a watch's events are read: more is a watch that
/// never ends.
const WATCH_BOUND: usize = 16;

/// A watch's events, read to its end, which must come within
/// [`WATCH_BOUND`] of them. Bounded, so a watch that never ends (its cursor
/// stuck, or an event made up each call) fails its test at once instead of
/// growing a `Vec` until the machine has no memory left: such a mutant did
/// that twice on 2026-11-15, to about 70 GB.
fn watched(watch: impl Iterator<Item = v1::RunEvent>) -> Vec<v1::RunEvent> {
    let events: Vec<v1::RunEvent> = watch.take(WATCH_BOUND).collect();
    assert!(
        events.len() < WATCH_BOUND,
        "the watch never ended: {events:?}"
    );
    events
}

fn kinds(events: &[v1::RunEvent]) -> Vec<&'static str> {
    events
        .iter()
        .map(|event| match &event.event {
            Some(v1::run_event::Event::Notice(_)) => "notice",
            Some(v1::run_event::Event::Reply(_)) => "reply",
            Some(v1::run_event::Event::End(_)) => "end",
            Some(v1::run_event::Event::Stop(_)) => "stop",
            None => "none",
        })
        .collect()
}

fn request() -> HostRequest {
    HostRequest {
        cwd: "/work".to_string(),
        ..HostRequest::default()
    }
}

#[test]
fn test_a_placed_one_shot_is_answered_placed_and_hosted_until_it_ends() {
    let (one_shots, told, _) = hosting(Script::Placed);
    let created = one_shots.create(request());
    assert_eq!(created, Created::Placed(placement(1)));
    assert_eq!(one_shots.hosting(), 1);
    assert_eq!(one_shots.live(), vec![placement(1)]);
    assert_eq!(
        one_shots.get(&placement(1).run),
        Some(Hosted {
            placement: placement(1),
            ended: false
        })
    );
    now(told.lock().unwrap()[0].done(answer("HELLO")));
    assert_eq!(one_shots.hosting(), 0, "ended");
    assert!(one_shots.live().is_empty());
    assert!(one_shots.get(&placement(1).run).unwrap().ended, "kept");
    assert!(one_shots.wait_ended(Duration::from_millis(1)));
}

#[test]
fn test_a_watch_gives_every_event_from_the_first_then_none() {
    let (one_shots, _, _) = hosting(Script::Answers);
    let Created::Placed(placed) = one_shots.create(request()) else {
        panic!("placed");
    };
    let events = watched(one_shots.watch(&placed.run).expect("hosted"));
    assert_eq!(kinds(&events), ["notice", "reply", "end"]);
    let Some(v1::run_event::Event::Reply(reply)) = &events[1].event else {
        panic!("a reply");
    };
    assert_eq!(reply, "HELLO");
    let Some(v1::run_event::Event::End(end)) = &events[2].event else {
        panic!("an end");
    };
    assert_eq!(*end, run_application::client::answered());
    // A second watch, later, gives the same.
    assert_eq!(watched(one_shots.watch(&placed.run).unwrap()).len(), 3);
    assert!(one_shots.watch("no-such-run").is_none());
    assert!(one_shots.get("no-such-run").is_none());
}

#[test]
fn test_a_one_shot_refused_before_it_was_placed_answers_its_notices_and_end() {
    let (one_shots, _, _) = hosting(Script::Refused);
    let Created::Ended(events) = one_shots.create(request()) else {
        panic!("ended");
    };
    assert_eq!(kinds(&events), ["notice", "end"]);
    let Some(v1::run_event::Event::End(end)) = &events[1].event else {
        panic!("an end");
    };
    assert_eq!(end.code, "runs-no-prompt");
    assert_eq!(end.exit, 48);
    assert_eq!(one_shots.hosting(), 0, "nothing hosted");
    assert!(one_shots.live().is_empty());
}

#[test]
fn test_a_stop_is_asked_once_said_to_its_watchers_and_ends_the_run_as_the_signal_says() {
    let (one_shots, told, stops) = hosting(Script::Placed);
    let Created::Placed(placed) = one_shots.create(request()) else {
        panic!("placed");
    };
    assert_eq!(one_shots.unstopped(), vec![placed.run.clone()]);
    assert!(one_shots.stop(&placed.run, "SIGINT"));
    assert!(
        !one_shots.stop(&placed.run, "SIGTERM"),
        "a second changes nothing"
    );
    assert_eq!(stops.load(Ordering::SeqCst), 1, "its control asked once");
    assert!(one_shots.unstopped().is_empty());
    // Its turn answered anyway (the stop raced its end): the stop says.
    now(told.lock().unwrap()[0].done(answer("too late")));
    let events = watched(one_shots.watch(&placed.run).unwrap());
    assert_eq!(kinds(&events), ["stop", "notice", "end"]);
    let Some(v1::run_event::Event::Stop(stop)) = &events[0].event else {
        panic!("a stop");
    };
    assert_eq!(stop.reason, "SIGINT");
    assert_eq!(stop.grace_ms, STOP_GRACE_MS);
    let Some(v1::run_event::Event::End(end)) = &events[2].event else {
        panic!("an end");
    };
    assert_eq!(end.code, "run-application-stopped");
    assert_eq!(end.exit, 130);
    assert!(
        !one_shots.stop(&placed.run, "SIGINT"),
        "ended: nothing to stop"
    );
    assert!(!one_shots.stop("no-such-run", "SIGINT"));
}

#[test]
fn test_stop_all_stops_every_live_one_as_a_sigterm() {
    let (one_shots, told, stops) = hosting(Script::Placed);
    let _ = one_shots.create(request());
    let _ = one_shots.create(request());
    assert_eq!(one_shots.hosting(), 2);
    one_shots.stop_all();
    assert_eq!(stops.load(Ordering::SeqCst), 2);
    assert!(
        !one_shots.wait_ended(Duration::from_millis(1)),
        "not ended yet"
    );
    for told in told.lock().unwrap().iter() {
        now(told.done(answer("x")));
    }
    assert!(one_shots.wait_ended(Duration::from_millis(1)));
    let ends: Vec<i32> = (1..=2)
        .map(|n| {
            let events = watched(one_shots.watch(&placement(n).run).unwrap());
            match &events.last().unwrap().event {
                Some(v1::run_event::Event::End(end)) => end.exit,
                _ => panic!("an end"),
            }
        })
        .collect();
    assert_eq!(ends, [143, 143]);
}

#[test]
fn test_only_the_newest_ended_one_shots_are_kept() {
    let (one_shots, _, _) = hosting(Script::Answers);
    for _ in 0..=ENDED_KEPT {
        let _ = one_shots.create(request());
    }
    assert!(
        one_shots.get(&placement(1).run).is_none(),
        "the oldest forgotten"
    );
    assert!(one_shots.get(&placement(2).run).is_some(), "the rest kept");
}

// Step 8 (D7): a live one-shot's ask is recorded with its run, engine, agent
// and approval_timeout as it was placed; the terminal session's is not routed
// (S28), and an ended one, or a session none of them is, has none.
#[test]
fn test_a_live_one_shot_asks_as_it_was_placed_and_the_terminal_never_does() {
    let (one_shots, told, _) = hosting(Script::Placed);
    for _ in 0..3 {
        one_shots.create(request());
    }
    let first = placement(1);
    assert_eq!(
        one_shots.asker(&first.session),
        Some(crate::Asker {
            session: first.session.clone(),
            run: first.run.clone(),
            engine: "claude-code".to_string(),
            agent: "agent-1".to_string(),
            approval_timeout: Duration::from_secs(60),
        })
    );
    assert_eq!(
        one_shots.asker(&placement(2).session).map(|a| a.agent),
        Some("agent-2".to_string())
    );
    assert_eq!(one_shots.asker(&placement(3).session), None, "a terminal's");
    assert_eq!(one_shots.asker("s9"), None);
    now(told.lock().unwrap()[0].done(answer("HELLO")));
    assert_eq!(one_shots.asker(&first.session), None, "ended");
}

/// A reaction that is told each outcome it is handed, and answers it
/// changed: a reply's text upper-cased, an error left as it was, its own
/// notice added.
struct Upper {
    handed: Handed,
    /// Held until the test lets it go, when given, having said it was
    /// entered.
    hold: Option<Hold>,
}

/// Each outcome a reaction was handed: its run, where it was placed, and
/// whether it answered.
type Handed = Arc<Mutex<Vec<(Option<String>, bool)>>>;

/// Where a held reaction says it was entered, and what lets it go.
type Hold = (
    Mutex<std::sync::mpsc::Sender<()>>,
    Mutex<std::sync::mpsc::Receiver<()>>,
);

impl Reaction for Upper {
    fn react<'a>(&'a self, placed: Option<&'a Placement>, done: Done<Answer>) -> Reacted<'a> {
        Box::pin(async move { self.upper(placed, done) })
    }
}

impl Upper {
    fn upper(&self, placed: Option<&Placement>, done: Done<Answer>) -> Done<Answer> {
        if let Some((entered, hold)) = &self.hold {
            entered.lock().unwrap().send(()).unwrap();
            hold.lock().unwrap().recv().unwrap();
        }
        self.handed
            .lock()
            .unwrap()
            .push((placed.map(|p| p.run.clone()), done.result.is_ok()));
        let Done {
            result,
            mut notices,
        } = done;
        notices.push(library_application::Notice::text("reacted"));
        Done {
            result: result.map(|answer| Answer {
                answer: answer.answer.to_uppercase(),
                ..answer
            }),
            notices,
        }
    }
}

#[test]
fn test_a_reacting_one_shot_ends_as_its_reaction_answers_its_outcome() {
    let (one_shots, _, _) = hosting(Script::Answers);
    let handed = Arc::new(Mutex::new(Vec::new()));
    let reaction = Arc::new(Upper {
        handed: Arc::clone(&handed),
        hold: None,
    });
    let Created::Placed(placed) = one_shots.create_reacting(request(), reaction) else {
        panic!("placed");
    };
    assert_eq!(
        *handed.lock().unwrap(),
        vec![(Some(placed.run.clone()), true)],
        "handed its outcome once, with its placement"
    );
    let events = watched(one_shots.watch(&placed.run).expect("hosted"));
    assert_eq!(kinds(&events), ["notice", "notice", "reply", "end"]);
    let Some(v1::run_event::Event::Reply(reply)) = &events[2].event else {
        panic!("a reply");
    };
    assert_eq!(reply, "HELLO", "the reaction's answer");
}

#[test]
fn test_a_reacting_one_shot_is_not_ended_until_its_reaction_returns() {
    // Forced: the reaction is held on a channel the test lets go of, so the
    // end can only be seen early if the run ends before its reaction.
    let (one_shots, told, _) = hosting(Script::Placed);
    let (release, held) = std::sync::mpsc::channel();
    let (enter, entered) = std::sync::mpsc::channel();
    let handed = Arc::new(Mutex::new(Vec::new()));
    let reaction = Arc::new(Upper {
        handed: Arc::clone(&handed),
        hold: Some((Mutex::new(enter), Mutex::new(held))),
    });
    let Created::Placed(placed) = one_shots.create_reacting(request(), reaction) else {
        panic!("placed");
    };
    let teller = told.lock().unwrap()[0].clone();
    std::thread::scope(|scope| {
        let ending = scope.spawn(move || now(teller.done(answer("hello"))));
        entered
            .recv_timeout(Duration::from_secs(10))
            .expect("its reaction was handed the outcome");
        // The engine is done; its reaction is held.
        assert!(
            !one_shots.get(&placed.run).unwrap().ended,
            "held: not ended"
        );
        assert_eq!(one_shots.hosting(), 1, "still hosted while it reacts");
        assert!(
            !one_shots.wait_ended(Duration::from_millis(50)),
            "nothing ends before its reaction"
        );
        release.send(()).unwrap();
        ending.join().unwrap();
    });
    assert!(one_shots.get(&placed.run).unwrap().ended);
    let events = watched(one_shots.watch(&placed.run).unwrap());
    assert_eq!(kinds(&events), ["notice", "notice", "reply", "end"]);
}

#[test]
fn test_a_reacting_one_shot_refused_before_it_was_placed_is_reacted_to_unplaced() {
    let (one_shots, _, _) = hosting(Script::Refused);
    let handed = Arc::new(Mutex::new(Vec::new()));
    let reaction = Arc::new(Upper {
        handed: Arc::clone(&handed),
        hold: None,
    });
    let Created::Ended(events) = one_shots.create_reacting(request(), reaction) else {
        panic!("ended");
    };
    assert_eq!(*handed.lock().unwrap(), vec![(None, false)]);
    assert_eq!(kinds(&events), ["notice", "notice", "end"]);
}

#[test]
fn test_a_stopped_reacting_one_shot_is_reacted_to_as_stopped() {
    let (one_shots, told, _) = hosting(Script::Placed);
    let handed = Arc::new(Mutex::new(Vec::new()));
    let reaction = Arc::new(Upper {
        handed: Arc::clone(&handed),
        hold: None,
    });
    let Created::Placed(placed) = one_shots.create_reacting(request(), reaction) else {
        panic!("placed");
    };
    assert!(one_shots.stop(&placed.run, "SIGTERM"));
    now(told.lock().unwrap()[0].done(answer("too late")));
    assert_eq!(
        *handed.lock().unwrap(),
        vec![(Some(placed.run.clone()), false)],
        "the stop's end, never the answer"
    );
}
