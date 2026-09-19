package parity

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// FieldSetPair is one field-set parity claim: two sides of a boundary that
// must carry the same set of names — a Go struct's fields and the wire
// message's, a codec's encoder and its decoder — each read live by a
// function so the table never restates either side. Register a pair in
// tests/arch's fieldSetPairs table; TestArch_FieldSetParity walks it.
type FieldSetPair struct {
	Name  string
	Left  func() []string
	Right func() []string
	// LeftName and RightName label the sides in a failure ("launch.Launch",
	// "the proto Launch").
	LeftName, RightName string
}

// FieldSetDiff reports the symmetric difference of two name sets: the names
// only a carries and the names only b carries, each sorted and
// deduplicated. Both empty means the sets are equal.
func FieldSetDiff(a, b []string) (onlyA, onlyB []string) {
	inA, inB := map[string]bool{}, map[string]bool{}
	for _, n := range a {
		inA[n] = true
	}
	for _, n := range b {
		inB[n] = true
	}
	for n := range inA {
		if !inB[n] {
			onlyA = append(onlyA, n)
		}
	}
	for n := range inB {
		if !inA[n] {
			onlyB = append(onlyB, n)
		}
	}
	slices.Sort(onlyA)
	slices.Sort(onlyB)
	return onlyA, onlyB
}

// FieldSetReport judges one pair: ok is true when both sides carry the same
// set; otherwise the message names each side's extra names. It is the pure
// half of CheckFieldSets so the judgement itself can be tested without a
// *testing.T to capture.
func FieldSetReport(p FieldSetPair) (msg string, ok bool) {
	onlyL, onlyR := FieldSetDiff(p.Left(), p.Right())
	if len(onlyL) == 0 && len(onlyR) == 0 {
		return "", true
	}
	return fmt.Sprintf("%s and %s do not carry the same field set:\n  only %s: %s\n  only %s: %s\n"+
		"A field on one side of a boundary and not the other is silent loss on every value that crosses it.",
		p.LeftName, p.RightName, p.LeftName, strings.Join(onlyL, ", "), p.RightName, strings.Join(onlyR, ", ")), false
}

// CheckFieldSets runs every pair and fails, naming each side's extra names,
// when the two sides do not carry the same set.
func CheckFieldSets(t *testing.T, pairs []FieldSetPair) {
	t.Helper()
	for _, p := range pairs {
		t.Run(p.Name, func(t *testing.T) {
			if msg, ok := FieldSetReport(p); !ok {
				t.Error(msg)
			}
		})
	}
}
