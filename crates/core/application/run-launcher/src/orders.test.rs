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
