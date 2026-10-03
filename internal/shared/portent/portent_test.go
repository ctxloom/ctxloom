package portent

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
)

// goldenVectors pin the algorithm. They were produced by an independent
// reimplementation written from doc.go alone, so a change that breaks them
// breaks every other implementation of the spec too. The second and third
// candidates use draws n >= 1, so they also pin the counter encoding, which
// draw 0 cannot (BE32(0) and LE32(0) are the same bytes).
var goldenVectors = []struct {
	r     Range
	input string
	first []uint16
}{
	{Privileged, "", []uint16{18, 310, 602}},
	{Privileged, "a", []uint16{918, 536, 154}},
	{Privileged, "portpick", []uint16{361, 29, 720}},
	{Privileged, "example.com:service", []uint16{184, 1007, 807}},
	{Registered, "", []uint16{18395, 46300, 26077}},
	{Registered, "a", []uint16{19244, 22861, 26478}},
	{Registered, "portpick", []uint16{5372, 31737, 9974}},
	{Registered, "example.com:service", []uint16{15937, 16604, 17271}},
	{Dynamic, "", []uint16{61403, 63014, 64625}},
	{Dynamic, "a", []uint16{60204, 63931, 51274}},
	{Dynamic, "portpick", []uint16{63740, 62623, 61506}},
	{Dynamic, "example.com:service", []uint16{55873, 59900, 63927}},
	{Service, "", []uint16{24539, 7746, 22697}},
	{Service, "a", []uint16{14124, 16915, 19706}},
	{Service, "portpick", []uint16{16636, 4327, 23762}},
	{Service, "example.com:service", []uint16{10817, 4442, 29811}},
	{Range{1000, 1009}, "", []uint16{1009, 1006, 1003}},
	{Range{1000, 1009}, "a", []uint16{1004, 1005, 1006}},
	{Range{1000, 1009}, "portpick", []uint16{1000, 1009, 1008}},
	{Range{1000, 1009}, "example.com:service", []uint16{1007, 1004, 1001}},
	{Range{1, 65535}, "", []uint16{30045, 3302, 42094}},
	{Range{1, 65535}, "a", []uint16{32790, 52342, 6359}},
	{Range{1, 65535}, "portpick", []uint16{11596, 25814, 40032}},
	{Range{1, 65535}, "example.com:service", []uint16{45853, 64050, 16712}},
}

func firstN(input []byte, r Range, n int) []uint16 {
	var out []uint16
	for p := range Candidates(input, r) {
		if len(out) == n {
			break
		}
		out = append(out, p)
	}
	return out
}

func TestGoldenVectors(t *testing.T) {
	for _, g := range goldenVectors {
		name := fmt.Sprintf("%d-%d/%q", g.r.Lo, g.r.Hi, g.input)
		t.Run(name, func(t *testing.T) {
			if got := Pick([]byte(g.input), g.r); got != g.first[0] {
				t.Errorf("Pick = %d, want %d", got, g.first[0])
			}
			got := firstN([]byte(g.input), g.r, len(g.first))
			if fmt.Sprint(got) != fmt.Sprint(g.first) {
				t.Errorf("Candidates = %v, want %v", got, g.first)
			}
		})
	}
}

// TestDrawSequence pins the draw function itself: SHA-256(input || BE32(n)),
// first 8 bytes big-endian. Values come from the independent reference.
func TestDrawSequence(t *testing.T) {
	s := hashStream([]byte("portpick"))
	want := []uint64{0x0b9e85a7e308b8fc, 0xd0450b12175cd214, 0xd3aa610a3af1a01d}
	for n, w := range want {
		if got := s.next(); got != w {
			t.Errorf("draw %d = %#x, want %#x", n, got, w)
		}
	}
}

// fixedDraws is a stream that returns the given draws in order and fails the
// test if more are taken than supplied.
func fixedDraws(t *testing.T, draws ...uint64) *stream {
	t.Helper()
	i := 0
	return &stream{next: func() uint64 {
		if i >= len(draws) {
			t.Fatalf("took draw %d, only %d supplied", i, len(draws))
		}
		i++
		return draws[i-1]
	}}
}

// 2^64 mod 3 == 1, so draw 0 is the only biased value for m == 3.
func TestUniform_RejectsBelowThreshold(t *testing.T) {
	if got := fixedDraws(t, 0, 5).uniform(3); got != 2 {
		t.Errorf("uniform(3) with draws [0 5] = %d, want 2 (0 rejected)", got)
	}
}

func TestUniform_AcceptsAtThreshold(t *testing.T) {
	if got := fixedDraws(t, 1).uniform(3); got != 1 {
		t.Errorf("uniform(3) with draw [1] = %d, want 1 (1 == threshold is accepted)", got)
	}
}

func TestUniform_PowerOfTwoNeverRejects(t *testing.T) {
	if got := fixedDraws(t, 0).uniform(8); got != 0 {
		t.Errorf("uniform(8) with draw [0] = %d, want 0", got)
	}
}

// For size 10, Uniform(9) has threshold 2^64 mod 9 == 7. Draw 10 gives
// stride 1+(10 mod 9) = 2, which shares a factor with 10 and must be
// rejected; draw 11 gives stride 3, which is coprime.
func TestStride_RejectsNonCoprime(t *testing.T) {
	if got := fixedDraws(t, 10, 11).stride(10); got != 3 {
		t.Errorf("stride(10) with draws [10 11] = %d, want 3 (2 rejected)", got)
	}
}

func TestStride_SingletonUsesNoDraws(t *testing.T) {
	if got := fixedDraws(t).stride(1); got != 1 {
		t.Errorf("stride(1) = %d, want 1", got)
	}
}

func TestDeterministic(t *testing.T) {
	in := []byte("same input")
	for _, r := range []Range{Privileged, Registered, Dynamic, Service} {
		a, b := firstN(in, r, 50), firstN(append([]byte(nil), in...), r, 50)
		if fmt.Sprint(a) != fmt.Sprint(b) || Pick(in, r) != Pick(in, r) {
			t.Errorf("range %v not deterministic: %v vs %v", r, a, b)
		}
	}
}

func TestPick_WithinRange(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	ranges := []Range{Privileged, Registered, Dynamic, Service, {7, 7}, {1, 65535}, {65535, 65535}, {1000, 1009}}
	for i := range 5000 {
		in := []byte(fmt.Sprintf("input-%d-%d", i, rng.Uint64()))
		for _, r := range ranges {
			if p := Pick(in, r); p < r.Lo || p > r.Hi {
				t.Fatalf("Pick(%q, %v) = %d, out of range", in, r, p)
			}
		}
	}
}

// checkFullCoverage consumes Candidates fully and requires every port in r
// exactly once, with the first equal to Pick.
func checkFullCoverage(t *testing.T, in []byte, r Range) {
	t.Helper()
	size := int(r.Hi) - int(r.Lo) + 1
	seen := make([]bool, size)
	count := 0
	for p := range Candidates(in, r) {
		if p < r.Lo || p > r.Hi {
			t.Fatalf("%q %v: candidate %d out of range", in, r, p)
		}
		if count == 0 && p != Pick(in, r) {
			t.Fatalf("%q %v: first candidate %d != Pick %d", in, r, p, Pick(in, r))
		}
		if seen[p-r.Lo] {
			t.Fatalf("%q %v: candidate %d repeated", in, r, p)
		}
		seen[p-r.Lo] = true
		count++
	}
	if count != size {
		t.Fatalf("%q %v: %d candidates, want %d", in, r, count, size)
	}
}

// Many inputs on a composite size: a stride sharing a factor with 10 (2, 4,
// 5, 6, 8) would repeat ports, so this also kills a missing coprimality
// check end to end.
func TestCandidates_SmallRangeCoverage(t *testing.T) {
	for i := range 500 {
		checkFullCoverage(t, []byte(fmt.Sprintf("in-%d", i)), Range{1000, 1009})
	}
}

func TestCandidates_EdgeRanges(t *testing.T) {
	checkFullCoverage(t, []byte("x"), Range{443, 443})
	checkFullCoverage(t, []byte("x"), Range{65535, 65535})
	checkFullCoverage(t, []byte("x"), Range{1, 65535})
	checkFullCoverage(t, []byte("x"), Range{1, 2})
	checkFullCoverage(t, []byte("x"), Privileged)
}

func TestCandidates_StopsEarly(t *testing.T) {
	n := 0
	for range Candidates([]byte("x"), Dynamic) {
		n++
		if n == 3 {
			break
		}
	}
	if n != 3 {
		t.Errorf("got %d yields, want 3", n)
	}
}

func TestCandidates_Reiterable(t *testing.T) {
	seq := Candidates([]byte("x"), Service)
	var a, b []uint16
	for p := range seq {
		if a = append(a, p); len(a) == 5 {
			break
		}
	}
	for p := range seq {
		if b = append(b, p); len(b) == 5 {
			break
		}
	}
	if fmt.Sprint(a) != fmt.Sprint(b) {
		t.Errorf("second iteration %v differs from first %v", b, a)
	}
}

// A loose chi-square smoke test: 100 bins, 100000 inputs, 99 degrees of
// freedom (mean 99, sd about 14). The inputs are fixed, so this cannot flake;
// a modulo-biased or broken mapping lands far above the bound.
func TestPick_Uniformity(t *testing.T) {
	r := Range{2000, 2099}
	const n, bins = 100000, 100
	var counts [bins]int
	for i := range n {
		counts[Pick([]byte(fmt.Sprintf("u-%d", i)), r)-r.Lo]++
	}
	expected := float64(n) / bins
	chi := 0.0
	for _, c := range counts {
		d := float64(c) - expected
		chi += d * d / expected
	}
	if chi > 200 {
		t.Errorf("chi-square = %.1f over %d bins, want <= 200", chi, bins)
	}
}

func TestValidate(t *testing.T) {
	for _, r := range []Range{Privileged, Registered, Dynamic, Service, {1, 1}, {1, 65535}, {65535, 65535}} {
		if err := r.Validate(); err != nil {
			t.Errorf("%v.Validate() = %v, want nil", r, err)
		}
	}
	cases := []struct {
		r    Range
		want error
	}{
		{Range{0, 10}, ErrPortZero},
		{Range{0, 0}, ErrPortZero},
		{Range{10, 9}, ErrEmptyRange},
		{Range{65535, 1}, ErrEmptyRange},
	}
	for _, c := range cases {
		if err := c.r.Validate(); !errors.Is(err, c.want) {
			t.Errorf("%v.Validate() = %v, want %v", c.r, err, c.want)
		}
	}
}

func expectPanic(t *testing.T, want error, f func()) {
	t.Helper()
	defer func() {
		t.Helper()
		err, _ := recover().(error)
		if !errors.Is(err, want) {
			t.Errorf("panic value %v, want error wrapping %v", err, want)
		}
	}()
	f()
}

func TestInvalidRangePanics(t *testing.T) {
	expectPanic(t, ErrPortZero, func() { Pick(nil, Range{0, 5}) })
	expectPanic(t, ErrEmptyRange, func() { Pick(nil, Range{6, 5}) })
	// Candidates panics at the call, not on first iteration.
	expectPanic(t, ErrEmptyRange, func() { _ = Candidates(nil, Range{6, 5}) })
}
