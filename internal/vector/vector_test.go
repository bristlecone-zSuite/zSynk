package vector

import "testing"

func TestDominates(t *testing.T) {
	a := Vector{"A": 2, "B": 1}
	b := Vector{"A": 1, "B": 1}
	if !Dominates(a, b) {
		t.Fatal("a should dominate b")
	}
	if Dominates(b, a) {
		t.Fatal("b should not dominate a")
	}
	if Concurrent(a, b) {
		t.Fatal("not concurrent")
	}
}

func TestConcurrent(t *testing.T) {
	a := Vector{"A": 2, "B": 1}
	b := Vector{"A": 1, "B": 2}
	if !Concurrent(a, b) {
		t.Fatal("expected concurrent")
	}
	if Dominates(a, b) || Dominates(b, a) {
		t.Fatal("neither should dominate")
	}
}

func TestMergeAndBump(t *testing.T) {
	a := Vector{"A": 1}
	b := Vector{"B": 3}
	m := Merge(a, b)
	if m["A"] != 1 || m["B"] != 3 {
		t.Fatalf("%v", m)
	}
	bumped := a.Bump("A")
	if bumped["A"] != 2 {
		t.Fatal(bumped)
	}
	if a["A"] != 1 {
		t.Fatal("bump must clone")
	}
}

func TestEqualEmpty(t *testing.T) {
	if !Equal(nil, Vector{}) {
		t.Fatal("empty")
	}
}
