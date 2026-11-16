package logfile

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The log moves aside when it reaches its limit, so the two files never
// hold much more than twice the limit, and Tail reads across both. A log
// opened again counts what it already holds.
func TestLogStaysBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "synk.log")
	var all []string
	write := func(from, to int) {
		t.Helper()
		l, err := Open(path, 100)
		if err != nil {
			t.Fatal(err)
		}
		for i := from; i < to; i++ {
			line := fmt.Sprintf("line %02d", i)
			all = append(all, line)
			if _, err := fmt.Fprintln(l, line); err != nil {
				t.Fatal(err)
			}
		}
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{path, path + ".1"} {
			st, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			if st.Size() > 100 {
				t.Errorf("%s holds %d bytes; the limit is 100", filepath.Base(p), st.Size())
			}
			if st.Mode().Perm() != 0o600 {
				t.Errorf("%s has mode %v", filepath.Base(p), st.Mode().Perm())
			}
		}
	}
	write(0, 50)
	got, err := Tail(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	if want := all[47:]; !slices.Equal(got, want) {
		t.Fatalf("Tail(3) = %q; want %q", got, want)
	}
	got, err = Tail(path, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 13 || !slices.Equal(got, all[len(all)-len(got):]) {
		t.Fatalf("Tail read %q, which isn't the end of the log", got)
	}

	write(50, 70)
	if got, _ := Tail(path, 3); !slices.Equal(got, all[67:]) {
		t.Fatalf("after reopening, the log ends %q", got)
	}
}

// A log opened again adds to what's there.
func TestLogAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "synk.log")
	for _, line := range []string{"first", "second"} {
		l, err := Open(path, 1000)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(l, line)
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := Tail(path, 10); err != nil || !slices.Equal(got, []string{"first", "second"}) {
		t.Fatalf("Tail = %q, %v", got, err)
	}
}

func TestTailWithoutALog(t *testing.T) {
	got, err := Tail(filepath.Join(t.TempDir(), "synk.log"), 10)
	if err != nil || len(got) != 0 {
		t.Fatalf("Tail of no log = %q, %v", got, err)
	}
}
