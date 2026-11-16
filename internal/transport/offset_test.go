package transport

import (
	"bytes"
	"io"
	"testing"
)

// A blob can start part way in: the message says where its body begins,
// and the body holds only the remaining bytes. This is how a transfer
// that stopped part way continues instead of starting over.
func TestBlobOffsetRoundTrip(t *testing.T) {
	content := bytes.Repeat([]byte("abcdefg"), ChunkSize) // well over one frame
	const offset = 3*ChunkSize/2 + 5
	rest := content[offset:]

	var buf bytes.Buffer
	if err := WriteBlobFrom(&buf, "deadbeef", offset, int64(len(rest)), bytes.NewReader(rest)); err != nil {
		t.Fatal(err)
	}
	stream := bytes.NewReader(buf.Bytes())
	h, size, off, r, err := ReadBlob(stream)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if h != "deadbeef" || off != offset || size != int64(len(rest)) || !bytes.Equal(got, rest) {
		t.Fatalf("got %s, %d bytes from offset %d: %d bytes", h, size, off, len(got))
	}
	if stream.Len() != 0 {
		t.Fatalf("%d bytes left after the blob", stream.Len())
	}
}

// A transfer from the start carries no offset at all, so a device that
// doesn't know about offsets can still read it.
func TestBlobFromZeroOmitsOffset(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteBlobFrom(&buf, "deadbeef", 0, 3, bytes.NewReader([]byte("abc"))); err != nil {
		t.Fatal(err)
	}
	m, err := ReadMsg(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if m.Offset != 0 {
		t.Fatalf("a blob from the start carried offset %d", m.Offset)
	}
}

// Nonsense offsets are refused, not served.
func TestWriteBlobFromRejectsBadOffsets(t *testing.T) {
	if err := WriteBlobFrom(io.Discard, "deadbeef", -1, 3, bytes.NewReader([]byte("abc"))); err == nil {
		t.Error("sent from a negative offset")
	}
	if err := WriteBlobFrom(io.Discard, "deadbeef", 1, -3, bytes.NewReader(nil)); err == nil {
		t.Error("sent a negative number of bytes")
	}
}
