package journal

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
)

type Status string

const (
	StatusStarted   Status = "started"
	StatusPrepared  Status = "prepared"
	StatusCommitted Status = "committed"
	StatusAborted   Status = "aborted"
)

type Record struct {
	ID       string `json:"id"`
	Seq      int    `json:"seq"`
	Status   Status `json:"status"`
	FolderID string `json:"folderId"`
	Op       string `json:"op"`
	Path     string `json:"path"`
	Hash     string `json:"hash,omitempty"`
	// Staging is relative to the library root.
	Staging string    `json:"staging,omitempty"`
	At      time.Time `json:"at"`
	Note    string    `json:"note,omitempty"`
}

func (r Record) open() bool { return r.Status == StatusStarted || r.Status == StatusPrepared }

// compactAfter is how many lines the file may hold before finished
// transactions are dropped from it.
const compactAfter = 1024

type Log struct {
	path  string
	mu    sync.Mutex
	seq   int
	lines int
	// open holds the last record of every transaction that hasn't committed
	// or aborted.
	open map[string]Record
}

// Open loads the journal and rewrites it to hold only unfinished
// transactions. That also drops a final line torn by a crash, so later
// appends never follow a partial line.
func Open(dataDir string) (*Log, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	l := &Log{path: filepath.Join(dataDir, "journal.jsonl"), open: map[string]Record{}}
	recs, err := readAll(l.path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, r := range recs {
		l.seq = max(l.seq, r.Seq)
		l.track(r)
	}
	if err == nil {
		if err := l.compactLocked(); err != nil {
			return nil, err
		}
	}
	return l, nil
}

func (l *Log) track(r Record) {
	if r.open() {
		l.open[r.ID] = r
	} else {
		delete(l.open, r.ID)
	}
}

func (l *Log) Append(r Record) (Record, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	r.Seq = l.seq
	if r.At.IsZero() {
		r.At = time.Now().UTC()
	}
	b, err := json.Marshal(r)
	if err != nil {
		return r, err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return r, err
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return r, err
	}
	if err := f.Sync(); err != nil {
		return r, err
	}
	l.lines++
	l.track(r)
	if l.lines > compactAfter && len(l.open) < l.lines/2 {
		if err := l.compactLocked(); err != nil {
			return r, err
		}
	}
	return r, nil
}

func (l *Log) compactLocked() error {
	keep := l.incompleteLocked(func(Record) bool { return true })
	var buf bytes.Buffer
	for _, r := range keep {
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if err := atomicfile.WriteFile(l.path, buf.Bytes(), 0o600); err != nil {
		return err
	}
	l.lines = len(keep)
	return nil
}

// Incomplete returns the last record of each transaction in folderID that
// started but never committed or aborted, oldest first.
func (l *Log) Incomplete(folderID string) []Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.incompleteLocked(func(r Record) bool { return r.FolderID == folderID })
}

func (l *Log) incompleteLocked(match func(Record) bool) []Record {
	out := []Record{}
	for _, r := range l.open {
		if match(r) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// Finished reports whether the journal has no open transaction with this ID.
func (l *Log) Finished(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.open[id]
	return !ok
}

func (l *Log) Path() string { return l.path }

// readAll reads every record. A final line that doesn't parse is a write
// torn by a crash and is ignored; a bad line anywhere else is corruption.
func readAll(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Record
	var bad error
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		if bad != nil {
			return nil, bad
		}
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			bad = fmt.Errorf("journal: corrupt line: %w", err)
			continue
		}
		out = append(out, r)
	}
	return out, sc.Err()
}
