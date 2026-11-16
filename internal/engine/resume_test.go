package engine

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
	"github.com/bristlecone-zSuite/zSynk/internal/config"
	"github.com/bristlecone-zSuite/zSynk/internal/reconcile"
	"github.com/bristlecone-zSuite/zSynk/internal/transport"
	"github.com/bristlecone-zSuite/zSynk/internal/vector"
)

// A transfer that stops part way leaves what arrived staged, and the
// next one continues from there instead of starting over.
func TestRecvBlobContinuesAPartial(t *testing.T) {
	n := openNode(t, "A")
	content := bytes.Repeat([]byte("continuing "), 200_000)
	h := atomicfile.HashBytes(content)
	remote := reconcile.NewIndex("notes", "PEER")
	remote.Entries["big.bin"] = reconcile.Entry{
		Path: "big.bin", Kind: reconcile.KindFile, Hash: h,
		Size: int64(len(content)), Vector: vector.Vector{"PEER": 1},
	}
	cut := len(content) / 3

	// A transfer that says it's sending the whole blob but stops after a
	// third: what arrived stays staged, and nothing reaches the store.
	err := recvFrom(n, []wantEntry{{Hash: h}}, remote, func(w io.Writer) error {
		if err := transport.WriteMsg(w, transport.Msg{Kind: transport.KindBlob, Hash: h, Size: int64(len(content))}); err != nil {
			return err
		}
		return writeBodyFrames(w, content[:cut])
	})
	if err == nil {
		t.Fatal("a transfer that stopped short succeeded")
	}
	if got := n.Store.PartialSize(h); got != int64(cut) {
		t.Fatalf("staged %d bytes of the cut-off blob; want %d", got, cut)
	}
	if n.Store.Has(h) {
		t.Fatal("a cut-off transfer's content was stored")
	}

	// The next transfer continues from where this one stopped.
	err = recvFrom(n, []wantEntry{{Hash: h, Offset: int64(cut)}}, remote, func(w io.Writer) error {
		return transport.WriteBlobFrom(w, h, int64(cut), int64(len(content)-cut), bytes.NewReader(content[cut:]))
	})
	if err != nil {
		t.Fatal(err)
	}
	if !n.Store.Has(h) {
		t.Fatal("the continued transfer didn't store the blob")
	}
	if got, err := n.Store.Get(h); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("the blob the two transfers assembled: %d bytes, %v", len(got), err)
	}
	if got := n.Store.PartialSize(h); got != 0 {
		t.Fatalf("staged content left behind: %d bytes", got)
	}
}

// writeBodyFrames sends b as the framed body of a blob message, the way
// the wire format frames a body.
func writeBodyFrames(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n := len(b)
		if n > transport.ChunkSize {
			n = transport.ChunkSize
		}
		var hdr [4]byte
		binary.BigEndian.PutUint32(hdr[:], uint32(n))
		if _, err := w.Write(hdr[:]); err != nil {
			return err
		}
		if _, err := w.Write(b[:n]); err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

// Content that arrives from the start replaces whatever an earlier
// transfer left staged, and is verified before it's stored.
func TestRecvBlobStartsOverWhenThePeerSendsFromZero(t *testing.T) {
	n := openNode(t, "A")
	content := []byte("what the peer actually holds")
	h := atomicfile.HashBytes(content)

	// Stage something that isn't a prefix of h.
	p, err := n.Store.Resume(h)
	must(t, err)
	_, err = p.Write(bytes.Repeat([]byte("stale"), 10))
	must(t, err)
	must(t, p.Close())
	stale := n.Store.PartialSize(h)
	if stale == 0 {
		t.Fatal("nothing was staged for the test to replace")
	}

	remote := reconcile.NewIndex("notes", "PEER")
	remote.Entries["a.bin"] = reconcile.Entry{
		Path: "a.bin", Kind: reconcile.KindFile, Hash: h,
		Size: int64(len(content)), Vector: vector.Vector{"PEER": 1},
	}
	err = recvFrom(n, []wantEntry{{Hash: h}}, remote, func(w io.Writer) error {
		return writeBlob(w, h, content)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := n.Store.Get(h); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("the blob that arrived: %d bytes, %v", len(got), err)
	}
	if got := n.Store.PartialSize(h); got != 0 {
		t.Fatalf("staged content left behind: %d bytes", got)
	}
}

// A suffix that arrives without the staged prefix it was meant to
// continue can't be used; it's dropped, and the next sync asks again.
func TestRecvBlobLeavesAnUnusableSuffixForTheNextSync(t *testing.T) {
	n := openNode(t, "A")
	content := []byte("the whole content, unstaged")
	h := atomicfile.HashBytes(content)
	remote := reconcile.NewIndex("notes", "PEER")
	remote.Entries["a.bin"] = reconcile.Entry{
		Path: "a.bin", Kind: reconcile.KindFile, Hash: h,
		Size: int64(len(content)), Vector: vector.Vector{"PEER": 1},
	}
	const off = 10
	err := recvFrom(n, []wantEntry{{Hash: h, Offset: off}}, remote, func(w io.Writer) error {
		return transport.WriteBlobFrom(w, h, off, int64(len(content)-off), bytes.NewReader(content[off:]))
	})
	if err != nil {
		t.Fatalf("a suffix without its staged prefix ended the session: %v", err)
	}
	if n.Store.Has(h) {
		t.Fatal("a suffix without its prefix was stored as if it were whole")
	}
	if got := n.Store.PartialSize(h); got != 0 {
		t.Fatalf("staged %d bytes of a suffix that couldn't be used", got)
	}
}

// A transfer over the network that stops part way resumes where it
// stopped on the next sync, and the file arrives exactly once, whole.
func TestTransferResumesAfterAnInterruption(t *testing.T) {
	a := openNode(t, "A")
	b := openNode(t, "B")
	must(t, Pair(a, b))
	dirA := library(t, a, "notes")
	dirB := library(t, b, "notes")

	content := make([]byte, 3*transport.ChunkSize+7)
	for i := range content {
		content[i] = byte(i * 7)
	}
	must(t, os.WriteFile(filepath.Join(dirA, "big.bin"), content, 0o600))
	h := atomicfile.HashBytes(content)

	// The first sync reads a little of the blob and then the connection
	// dies, the way a laptop closing its lid does.
	const cut = 64 << 10
	addr := listen(t, a)
	if err := syncWithCut(t, b, addr, "notes", cut); err == nil {
		t.Fatal("the interrupted sync reported success")
	}
	staged := b.Store.PartialSize(h)
	if staged <= 0 || staged >= int64(len(content)) {
		t.Fatalf("staged %d bytes after the interruption", staged)
	}
	if _, err := os.Stat(filepath.Join(dirB, "big.bin")); !os.IsNotExist(err) {
		t.Fatal("a partial transfer reached the library")
	}

	// The next sync finishes the job, and reads less than the whole blob.
	var read int64
	c, p := dialNode(t, b, addr)
	must(t, b.hello(active{&countingConn{Conn: c, n: &read}, b.idleTimeout}, p, addr, "notes"))
	wantFile(t, dirB, "big.bin", string(content))
	if got := b.Store.PartialSize(h); got != 0 {
		t.Fatalf("staged content left behind: %d bytes", got)
	}
	if read >= int64(len(content)) {
		t.Fatalf("the resumed sync read %d bytes, as if it started over", read)
	}

	// A third sync changes nothing.
	must(t, SyncOnce(a, b, "notes"))
}

// syncWithCut runs one sync session whose reads stop after limit bytes.
func syncWithCut(t testing.TB, n *Node, addr, folderID string, limit int64) error {
	c, p := dialNode(t, n, addr)
	err := n.hello(active{&cutConn{Conn: c, limit: limit}, n.idleTimeout}, p, addr, folderID)
	return err
}

// dialNode dials addr as n and returns the connection and the peer it
// belongs to.
func dialNode(t testing.TB, n *Node, addr string) (net.Conn, config.Peer) {
	t.Helper()
	c, err := transport.Dial(addr, dialTimeout, transport.Config(n.cert, n.anyPeer))
	must(t, err)
	t.Cleanup(func() { _ = c.Close() })
	pub, err := transport.PeerKey(c.ConnectionState())
	must(t, err)
	p, ok := n.peerByKey(pub)
	if !ok {
		t.Fatal("dialed a device that isn't paired")
	}
	return c, p
}

// cutConn stops a transfer: after limit bytes have been read the
// connection is closed, and every later read fails.
type cutConn struct {
	net.Conn
	limit int64
	total int64
	cut   bool
}

func (c *cutConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.total += int64(n)
	if !c.cut && c.total >= c.limit {
		c.cut = true
		_ = c.Conn.Close()
	}
	return n, err
}

// countingConn counts the bytes a session reads.
type countingConn struct {
	net.Conn
	n *int64
}

func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	*c.n += int64(n)
	return n, err
}

// A suffix that finishes a staged prefix is verified against the whole
// blob's hash before anything is stored: a partial that doesn't hash to
// its name is thrown away, never kept.
func TestRecvBlobVerifiesTheWholeBlobBeforeStoring(t *testing.T) {
	n := openNode(t, "A")
	content := []byte("the real content, delivered in two parts")
	h := atomicfile.HashBytes(content)
	remote := reconcile.NewIndex("notes", "PEER")
	remote.Entries["a.bin"] = reconcile.Entry{
		Path: "a.bin", Kind: reconcile.KindFile, Hash: h,
		Size: int64(len(content)), Vector: vector.Vector{"PEER": 1},
	}

	// Stage the honest prefix.
	p, err := n.Store.Resume(h)
	must(t, err)
	_, err = p.Write(content[:10])
	must(t, err)
	must(t, p.Close())

	// The peer sends a wrong suffix.
	err = recvFrom(n, []wantEntry{{Hash: h, Offset: 10}}, remote, func(w io.Writer) error {
		return transport.WriteBlobFrom(w, h, 10, int64(len(content)-10), bytes.NewReader(bytes.Repeat([]byte("x"), len(content)-10)))
	})
	if err == nil {
		t.Fatal("a wrong suffix completed the blob")
	}
	if n.Store.Has(h) {
		t.Fatal("content that doesn't hash to its name was stored")
	}
	if got := n.Store.PartialSize(h); got != 0 {
		t.Fatalf("a partial that failed to verify was kept: %d bytes", got)
	}
}

// Two transfers staging the same blob don't corrupt each other: the
// second leaves the bytes it reads unstored, and the next sync asks again.
func TestRecvBlobLeavesABusyStagingToTheOtherTransfer(t *testing.T) {
	n := openNode(t, "A")
	content := []byte("content two transfers want at once")
	h := atomicfile.HashBytes(content)
	remote := reconcile.NewIndex("notes", "PEER")
	remote.Entries["a.bin"] = reconcile.Entry{
		Path: "a.bin", Kind: reconcile.KindFile, Hash: h,
		Size: int64(len(content)), Vector: vector.Vector{"PEER": 1},
	}

	// Hold the staging open, the way another session would.
	p, err := n.Store.Resume(h)
	must(t, err)
	defer p.Close()

	err = recvFrom(n, []wantEntry{{Hash: h}}, remote, func(w io.Writer) error {
		return writeBlob(w, h, content)
	})
	if err != nil {
		t.Fatal(err)
	}
	if n.Store.Has(h) {
		t.Fatal("content was stored while another transfer held the staging")
	}
	if got := n.Store.PartialSize(h); got != 0 {
		t.Fatalf("the busy transfer's bytes were written anyway: %d", got)
	}
	if p.Size() != 0 {
		t.Fatalf("the holding transfer saw %d bytes appended", p.Size())
	}
}
