use std::collections::BTreeMap;

use super::{Order, Orders};

fn order(harp: &str) -> Order {
    Order {
        harp: harp.to_string(),
        engine: "claude-code".to_string(),
        context: "ctx".to_string(),
        prompt: "p".to_string(),
        env: BTreeMap::new(),
        cwd: None,
        interactive: false,
        model: String::new(),
        effort: String::new(),
        settings: Default::default(),
        owned: Vec::new(),
    }
}

#[test]
fn an_order_is_taken_once() {
    let orders = Orders::new();
    orders.place("s1", 1, order("amber-swift-owl"));
    assert_eq!(orders.take("s1", 1), Some(order("amber-swift-owl")));
    assert_eq!(orders.take("s1", 1), None);
}

#[test]
fn an_order_is_for_its_session_and_incarnation_alone() {
    let orders = Orders::new();
    orders.place("s1", 1, order("a"));
    assert_eq!(orders.take("s2", 1), None);
    assert_eq!(orders.take("s1", 2), None);
    assert_eq!(orders.take("s1", 1), Some(order("a")));
}

#[test]
fn placing_again_replaces_the_order() {
    let orders = Orders::new();
    orders.place("s1", 1, order("first"));
    orders.place("s1", 1, order("second"));
    assert_eq!(orders.take("s1", 1), Some(order("second")));
}

// An order is logged without the bearer's value: the exact rendering.
#[test]
fn an_order_renders_its_env_keys_and_never_the_bearer() {
    let order = Order {
        harp: "h".to_string(),
        engine: "e".to_string(),
        context: "c".to_string(),
        prompt: "p".to_string(),
        env: BTreeMap::from([
            ("CTXLOOM_BEARER".to_string(), "run.TOPSECRET".to_string()),
            ("PATH".to_string(), "/bin".to_string()),
        ]),
        cwd: None,
        interactive: true,
        model: String::new(),
        effort: String::new(),
        settings: Default::default(),
        owned: Vec::new(),
    };
    assert_eq!(
        format!("{order:?}"),
        r#"Order { harp: "h", engine: "e", context: "c", prompt: "p", env: {"CTXLOOM_BEARER": "<redacted>", "PATH": "<redacted>"}, cwd: None, interactive: true, model: "", effort: "", settings: EngineSettings { command: "", session_home: Session, auth: Builtin, env: [] }, owned: [] }"#
    );
}

/// A run lock's guard that says when it is let go.
struct Guard(std::sync::Arc<std::sync::atomic::AtomicBool>);

impl Drop for Guard {
    fn drop(&mut self) {
        self.0.store(true, std::sync::atomic::Ordering::SeqCst);
    }
}

fn held() -> (
    run_ports::HeldLock,
    std::sync::Arc<std::sync::atomic::AtomicBool>,
) {
    let gone = std::sync::Arc::new(std::sync::atomic::AtomicBool::new(false));
    let lock = run_ports::HeldLock::new("/h/.ctxloom/runs/r.lock".to_string(), Guard(gone.clone()));
    (lock, gone)
}

// Rulings 86, 92: a run lock handed to the orders is held there until it is
// released, for its session and incarnation alone, and then let go.
#[test]
fn a_run_lock_is_held_until_its_release() {
    use std::sync::atomic::Ordering;
    let orders = Orders::new();
    let (lock, gone) = held();
    orders.hold("s1", 1, lock);
    assert!(
        !gone.load(Ordering::SeqCst),
        "held while the orders hold it"
    );
    orders.release("s1", 2);
    orders.release("s2", 1);
    assert!(
        !gone.load(Ordering::SeqCst),
        "another run's release lets it be"
    );
    orders.release("s1", 1);
    assert!(gone.load(Ordering::SeqCst), "let go at its release");
}
