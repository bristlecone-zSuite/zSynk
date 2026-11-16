package engine

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/apply"
	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
	"github.com/bristlecone-zSuite/zSynk/internal/config"
	"github.com/bristlecone-zSuite/zSynk/internal/reconcile"
	"github.com/bristlecone-zSuite/zSynk/internal/transport"
	"github.com/bristlecone-zSuite/zSynk/internal/vector"
)

func TestRemoteIndexCannotEscapeLibraryRoot(t *testing.T) {
	b := openNode(t, "B")
	parent := t.TempDir()
	dirB := filepath.Join(parent, "notes")
	must(t, b.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Label: "n", Path: dirB}))
	payload := []byte("written outside the library")
	hash := atomicfile.HashBytes(payload)
	remote := reconcile.NewIndex("notes", "PEER")
	for _, p := range []string{"../escaped.txt", filepath.Join(parent, "absolute.txt"), ".synk/planted.txt", "sub/../../escaped2.txt"} {
		remote.Entries[p] = reconcile.Entry{
			Path: p, Kind: reconcile.KindFile, Hash: hash, Size: int64(len(payload)),
			Vector: vector.Vector{"PEER": 1},
		}
	}

	_ = b.PullFrom(remote, func(string) ([]byte, error) { return payload, nil })

	for _, p := range []string{
		filepath.Join(parent, "escaped.txt"),
		filepath.Join(parent, "escaped2.txt"),
		filepath.Join(parent, "absolute.txt"),
		filepath.Join(dirB, ".synk", "planted.txt"),
	} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("a remote index entry was written to %s", p)
		}
	}
}

func TestPeerBlobHashCannotBeAPath(t *testing.T) {
	n := openNode(t, "A")
	outside := t.TempDir()
	evil := "x/" + strings.Repeat("../", 32) + strings.TrimPrefix(outside, "/") + "/planted"
	requested := atomicfile.HashBytes([]byte("what we asked for"))
	// The second case can't happen through MissingBlobs, which only sees
	// validated hashes; it checks recvBlobs doesn't depend on that.
	for _, wanted := range [][]string{{requested}, {evil}} {
		err := recvFrom(n, wanted, reconcile.NewIndex("notes", "PEER"), func(w io.Writer) error {
			return writeBlob(w, evil, []byte("attacker bytes"))
		})
		if err == nil {
			t.Fatalf("wanted %q: a blob frame whose hash field was a path was accepted", wanted)
		}
		if entries, _ := os.ReadDir(outside); len(entries) != 0 {
			t.Fatal("a blob frame whose hash field was a path wrote peer bytes outside SYNK_DIR")
		}
	}
}

// A device asks only for content the other's index references, which is
// all the other serves. A conflict's losing copy can be this device's own
// version, which it must already have.
func TestMissingBlobsAreOnesThePeerHas(t *testing.T) {
	n := openNode(t, "A")
	theirs, ours := atomicfile.HashBytes([]byte("theirs")), atomicfile.HashBytes([]byte("ours, no longer stored"))
	remote := reconcile.NewIndex("notes", "PEER")
	remote.Entries["a.md"] = reconcile.Entry{Path: "a.md", Kind: reconcile.KindFile, Hash: theirs, Size: 6, Vector: vector.Vector{"PEER": 1}}
	ops := []reconcile.Op{{Kind: reconcile.OpConflict, Path: "a.md", KindFile: reconcile.KindFile, Hash: theirs, ConflictPath: "a.synk-conflict.md", ConflictHash: ours}}
	if got := n.MissingBlobs(ops, remote); !slices.Equal(got, []string{theirs}) {
		t.Fatalf("MissingBlobs asked for %v; want only %s", got, theirs)
	}
}

func TestRecvBlobsRejectsUnrequestedContent(t *testing.T) {
	n := openNode(t, "A")
	wanted := []byte("wanted")
	extra := []byte("pushed without being asked")
	err := recvFrom(n, []string{atomicfile.HashBytes(wanted)}, reconcile.NewIndex("notes", "PEER"), func(w io.Writer) error {
		return writeBlob(w, atomicfile.HashBytes(extra), extra)
	})
	if err == nil {
		t.Fatal("recvBlobs stored a blob that wasn't requested")
	}
	if n.Store.Has(atomicfile.HashBytes(extra)) {
		t.Fatal("an unrequested blob is in the store")
	}
}

// A blob is stored only if it's no bigger than the index says, there's
// room for it, and it holds what its hash says.
func TestRecvBlobsChecksWhatArrives(t *testing.T) {
	n := openNode(t, "A")
	hash := atomicfile.HashBytes
	fits, over := []byte("as big as the index says"), []byte("a byte bigger than the index says")
	part, bigPart := []byte("a part"), []byte("a part bigger than its package")
	manifest := []byte(`{"kind":"package"}`)
	huge := hash([]byte("huge"))
	remote := reconcile.NewIndex("notes", "PEER")
	add := func(path string, e reconcile.Entry) {
		e.Path, e.Vector = path, vector.Vector{"PEER": 1}
		remote.Entries[path] = e
	}
	add("fits.md", reconcile.Entry{Kind: reconcile.KindFile, Hash: hash(fits), Size: int64(len(fits))})
	add("over.md", reconcile.Entry{Kind: reconcile.KindFile, Hash: hash(over), Size: int64(len(over)) - 1})
	add("Talk.key", reconcile.Entry{Kind: reconcile.KindPackage, Hash: hash(manifest), Size: int64(len(part)), Parts: []string{hash(part), hash(bigPart)}})
	add("huge.mov", reconcile.Entry{Kind: reconcile.KindFile, Hash: huge, Size: 1 << 60})
	recv := func(h string, content []byte) error {
		t.Helper()
		return recvFrom(n, []string{h}, remote, func(w io.Writer) error { return writeBlob(w, h, content) })
	}
	// head sends only a blob's message, claiming size.
	head := func(h string, size int64) error {
		t.Helper()
		return recvFrom(n, []string{h}, remote, func(w io.Writer) error {
			return transport.WriteMsg(w, transport.Msg{Kind: transport.KindBlob, Hash: h, Size: size})
		})
	}

	for name, c := range map[string][]byte{"a file": fits, "a part": part, "a manifest": manifest} {
		if err := recv(hash(c), c); err != nil || !n.Store.Has(hash(c)) {
			t.Errorf("%s wasn't stored: %v", name, err)
		}
	}
	for name, c := range map[string][]byte{"a file": over, "a part": bigPart} {
		if err := recv(hash(c), c); err == nil || n.Store.Has(hash(c)) {
			t.Errorf("%s bigger than its index entry allows was stored: %v", name, err)
		}
	}
	if err := head(hash(manifest), maxManifest+1); err == nil || strings.Contains(err.Error(), "EOF") {
		t.Errorf("a manifest over maxManifest was read: %v", err)
	}
	if err := head(huge, 1<<60); err == nil || !strings.Contains(err.Error(), "free space") {
		t.Errorf("a blob too big for the disk: %v", err)
	}
	wrong := bytes.ToUpper(over[:len(over)-1])
	if err := recv(hash(over), wrong); err == nil || n.Store.Has(hash(over)) || n.Store.Has(hash(wrong)) {
		t.Errorf("content that didn't match its hash was stored: %v", err)
	}
	if left, _ := os.ReadDir(filepath.Join(n.Store.Root, "tmp")); len(left) != 0 {
		t.Errorf("rejected blobs left %d temporary files", len(left))
	}

	measure := diskAvailable
	t.Cleanup(func() { diskAvailable = measure })
	diskAvailable = func(string) (uint64, error) { return 3 * apply.MinFree, nil }
	tight := hash([]byte("tight"))
	add("tight.mov", reconcile.Entry{Kind: reconcile.KindFile, Hash: tight, Size: 3 * apply.MinFree})
	if err := head(tight, 2*apply.MinFree+1); err == nil || !strings.Contains(err.Error(), "free space") {
		t.Errorf("a blob that would leave less than apply.MinFree free: %v", err)
	}
	if err := head(tight, 2*apply.MinFree); err == nil || strings.Contains(err.Error(), "free space") {
		t.Errorf("a blob that leaves apply.MinFree free: %v", err)
	}
}

// recvFrom runs n.recvBlobs for hashes from remote on the content send
// writes.
func recvFrom(n *Node, hashes []string, remote *reconcile.Index, send func(w io.Writer) error) error {
	client, server := net.Pipe()
	go func() {
		_ = send(client)
		_ = client.Close()
	}()
	defer server.Close()
	return n.recvBlobs(server, hashes, remote)
}

func writeBlob(w io.Writer, hash string, content []byte) error {
	return transport.WriteBlob(w, hash, int64(len(content)), bytes.NewReader(content))
}

func TestServeWantsOnlyServesTheSentLibrary(t *testing.T) {
	n := openNode(t, "A")
	notes := filepath.Join(t.TempDir(), "notes")
	private := filepath.Join(t.TempDir(), "private")
	must(t, n.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Label: "n", Path: notes}))
	must(t, n.EnableLibrary(config.Library{ID: "private", Preset: "custom", Label: "p", Path: private}))
	must(t, os.WriteFile(filepath.Join(notes, "shared.md"), []byte("shared"), 0o600))
	secret := []byte("not shared with this peer")
	must(t, os.WriteFile(filepath.Join(private, "secret.md"), secret, 0o600))
	_, err := n.ExportIndex("private")
	must(t, err)
	sent, err := n.ExportIndex("notes")
	must(t, err)

	// serve asks for hashes and reports whether a blob came back.
	serve := func(hashes ...string) (served bool, err error) {
		client, server := net.Pipe()
		defer client.Close()
		errc := make(chan error, 1)
		go func() {
			errc <- n.serveWants(server, sent)
			_ = server.Close()
		}()
		_ = client.SetDeadline(time.Now().Add(5 * time.Second))
		must(t, writeWant(client, hashes))
		_, _, content, readErr := transport.ReadBlob(client)
		if readErr == nil {
			_, _ = io.Copy(io.Discard, content)
		}
		return readErr == nil, <-errc
	}
	if served, err := serve(atomicfile.HashBytes(secret)); served || err == nil {
		t.Fatalf("serveWants served content from a library that wasn't part of the session: %v", err)
	}
	shared := atomicfile.HashBytes([]byte("shared"))
	if served, err := serve(shared, shared); served || err == nil {
		t.Fatalf("serveWants served a want for more blobs than the index references: %v", err)
	}
	if served, err := serve(shared); !served || err != nil {
		t.Fatalf("serveWants didn't serve the library's content: %v", err)
	}
}

func TestCheckRemote(t *testing.T) {
	h := atomicfile.HashBytes([]byte("x"))
	v := vector.Vector{"PEER": 1}
	idx := reconcile.NewIndex("notes", "PEER")
	add := func(key string, e reconcile.Entry) {
		if e.Path == "" {
			e.Path = key
		}
		if e.Vector == nil {
			e.Vector = v
		}
		idx.Entries[key] = e
	}
	add("ok.md", reconcile.Entry{Kind: reconcile.KindFile, Hash: h, Size: 1})
	add("Talk.key", reconcile.Entry{Kind: reconcile.KindPackage, Hash: h, Parts: []string{h}})
	add("gone.md", reconcile.Entry{Kind: reconcile.KindFile, Hash: "junk", Deleted: true})
	add("../up.md", reconcile.Entry{Kind: reconcile.KindFile, Hash: h})
	add("alias.md", reconcile.Entry{Path: "other.md", Kind: reconcile.KindFile, Hash: h})
	add("dir", reconcile.Entry{Kind: reconcile.KindDir, Hash: h})
	add("upper.md", reconcile.Entry{Kind: reconcile.KindFile, Hash: strings.ToUpper(h)})
	add("neg.md", reconcile.Entry{Kind: reconcile.KindFile, Hash: h, Size: -1})
	add("parts.key", reconcile.Entry{Kind: reconcile.KindPackage, Hash: h, Parts: []string{"../x"}})
	add("huge.md", reconcile.Entry{Kind: reconcile.KindFile, Hash: h, Vector: vector.Vector{"PEER": 1 << 60}})

	if _, _, err := checkRemote(idx, "other"); err == nil {
		t.Fatal("accepted an index for a different library")
	}
	got, dropped, err := checkRemote(idx, "notes")
	must(t, err)
	var keys []string
	for k := range got.Entries {
		keys = append(keys, k)
	}
	if len(got.Entries) != 3 || got.Entries["ok.md"].Hash != h || got.Entries["Talk.key"].Hash != h {
		t.Fatalf("kept %v, dropped %v", keys, dropped)
	}
	if e := got.Entries["gone.md"]; !e.Deleted || e.Hash != "" {
		t.Fatalf("tombstone kept an invalid hash: %+v", e)
	}
	if len(dropped) != 7 {
		t.Fatalf("dropped %d entries, want 7: %v", len(dropped), dropped)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
