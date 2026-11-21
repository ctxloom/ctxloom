use super::*;

#[test]
fn test_judge_table() {
    let table = [
        ("fresh", 7, 7, Judged::Fresh),
        ("baseline", 0, 0, Judged::Fresh),
        (
            "stale, the path moved ahead",
            3,
            9,
            Judged::Stale { seen: 3, now: 9 },
        ),
        (
            "stale, the caller is ahead",
            9,
            3,
            Judged::Stale { seen: 9, now: 3 },
        ),
        (
            "stale, baseline seen, since written",
            0,
            1,
            Judged::Stale { seen: 0, now: 1 },
        ),
    ];
    for (name, seen, now, want) in table {
        assert_eq!(judge(seen, now), want, "{name}");
    }
}
