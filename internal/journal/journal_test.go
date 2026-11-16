package journal

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestAppendAndIncomplete(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	r, err := l.Append(Record{ID: "tx1", Status: StatusStarted, FolderID: "lib", Op: "set", Path: "a.md"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Seq != 1 {
		t.Fatal(r.Seq)
	}
	if _, err := l.Append(Record{ID: "tx1", Status: StatusPrepared, FolderID: "lib", Op: "set", Path: "a.md"}); err != nil {
		t.Fatal(err)
	}
	if inc := l.Incomplete("lib"); len(inc) != 1 || inc[0].Status != StatusPrepared {
		t.Fatalf("%+v", inc)
	}
	if inc := l.Incomplete("other"); len(inc) != 0 {
		t.Fatalf("another library's record leaked: %+v", inc)
	}
	if _, err := l.Append(Record{ID: "tx1", Status: StatusCommitted, FolderID: "lib", Op: "set", Path: "a.md"}); err != nil {
		t.Fatal(err)
	}
	if inc := l.Incomplete("lib"); len(inc) != 0 {
		t.Fatalf("%+v", inc)
	}
	if _, err := os.Stat(l.Path()); err != nil {
		t.Fatal(err)
	}
}

func TestReopenKeepsOnlyOpenTransactions(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []Record{
		{ID: "done", Status: StatusStarted, FolderID: "lib"},
		{ID: "done", Status: StatusCommitted, FolderID: "lib"},
		{ID: "open", Status: StatusStarted, FolderID: "lib"},
		{ID: "open", Status: StatusPrepared, FolderID: "lib", Staging: ".synk/staging/open"},
	} {
		if _, err := l.Append(r); err != nil {
			t.Fatal(err)
		}
	}
	l2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	inc := l2.Incomplete("lib")
	if len(inc) != 1 || inc[0].ID != "open" || inc[0].Staging != ".synk/staging/open" {
		t.Fatalf("%+v", inc)
	}
	raw, err := os.ReadFile(l2.Path())
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(raw), "\n"); n != 1 {
		t.Fatalf("compacted journal has %d lines, want 1:\n%s", n, raw)
	}
	r, err := l2.Append(Record{ID: "next", Status: StatusStarted, FolderID: "lib"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Seq != 5 {
		t.Fatalf("sequence restarted at %d after compaction", r.Seq)
	}
}

func TestTornFinalLineIsIgnored(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append(Record{ID: "tx", Status: StatusPrepared, FolderID: "lib"}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(l.Path(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"id":"tx","seq":2,"stat`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	l2, err := Open(dir)
	if err != nil {
		t.Fatalf("a torn final line made the journal unreadable: %v", err)
	}
	if inc := l2.Incomplete("lib"); len(inc) != 1 {
		t.Fatalf("%+v", inc)
	}
	if _, err := l2.Append(Record{ID: "tx", Status: StatusAborted, FolderID: "lib"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err != nil {
		t.Fatalf("appending after a torn line corrupted the journal: %v", err)
	}
}

func TestCorruptMiddleLineIsAnError(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.Path(), []byte("{not json\n{\"id\":\"a\",\"status\":\"started\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("a corrupt line before valid records was ignored")
	}
}

func TestAppendCompacts(t *testing.T) {
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3*compactAfter; i++ {
		st := StatusStarted
		if i%2 == 1 {
			st = StatusCommitted
		}
		if _, err := l.Append(Record{ID: fmt.Sprintf("tx%d", i/2), Status: st, FolderID: "lib"}); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(raw), "\n"); n > compactAfter+1 {
		t.Fatalf("journal grew to %d lines without compacting", n)
	}
}
