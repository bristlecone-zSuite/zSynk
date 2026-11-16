package transport

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteMsg(&buf, Msg{Kind: KindHello, Name: "Mac"}); err != nil {
		t.Fatal(err)
	}
	m, err := ReadMsg(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != KindHello || m.Name != "Mac" {
		t.Fatalf("%+v", m)
	}
}

// Blobs of any size cross in frames of at most ChunkSize bytes.
func TestBlobsAreChunked(t *testing.T) {
	for _, size := range []int{0, 1, ChunkSize, ChunkSize + 1, 2*ChunkSize + ChunkSize/2} {
		content := bytes.Repeat([]byte("abcdefg"), size/7+1)[:size]
		var buf bytes.Buffer
		if err := WriteBlob(&buf, "deadbeef", int64(size), bytes.NewReader(content)); err != nil {
			t.Fatal(err)
		}
		stream := bytes.NewReader(buf.Bytes())
		h, n, r, err := ReadBlob(stream)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if h != "deadbeef" || n != int64(size) || !bytes.Equal(got, content) {
			t.Fatalf("%d bytes: got %s, %d, %d bytes", size, h, n, len(got))
		}
		if stream.Len() != 0 {
			t.Fatalf("%d bytes: %d bytes left after the blob", size, stream.Len())
		}
		if frames := chunkSizes(t, buf.Bytes()); slices.Max(append(frames, 0)) > ChunkSize {
			t.Fatalf("%d bytes went in frames of %v", size, frames)
		}
	}
}

// An index or a want list of any size follows its message in frames of at
// most ChunkSize bytes, and the message gives its size.
func TestBodiesAreChunked(t *testing.T) {
	for _, size := range []int{0, 1, ChunkSize, ChunkSize + 1, 2*ChunkSize + ChunkSize/2} {
		body := bytes.Repeat([]byte("abcdefg"), size/7+1)[:size]
		var buf bytes.Buffer
		if err := WriteBody(&buf, Msg{Kind: KindIndex, FolderID: "notes", Size: 99}, body); err != nil {
			t.Fatal(err)
		}
		stream := bytes.NewReader(buf.Bytes())
		m, r, err := ReadBody(stream, int64(size))
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if m.Kind != KindIndex || m.FolderID != "notes" || m.Size != int64(size) || !bytes.Equal(got, body) {
			t.Fatalf("%d bytes: got %+v and %d bytes", size, m, len(got))
		}
		if stream.Len() != 0 {
			t.Fatalf("%d bytes: %d bytes left after the body", size, stream.Len())
		}
		if frames := chunkSizes(t, buf.Bytes()); slices.Max(append(frames, 0)) > ChunkSize {
			t.Fatalf("%d bytes went in frames of %v", size, frames)
		}
	}
}

// ReadBody refuses a body over its limit before reading any of it, and a
// message without a body, such as a refusal, has an empty one.
func TestReadBodyLimit(t *testing.T) {
	body := []byte(`["a","b"]`)
	read := func(limit int64) ([]byte, error) {
		var buf bytes.Buffer
		if err := WriteBody(&buf, Msg{Kind: KindWant}, body); err != nil {
			t.Fatal(err)
		}
		_, r, err := ReadBody(&buf, limit)
		if err != nil {
			return nil, err
		}
		return io.ReadAll(r)
	}
	if got, err := read(int64(len(body))); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("a body at the limit: %q, %v", got, err)
	}
	if _, err := read(int64(len(body)) - 1); err == nil {
		t.Fatal("read a body over the limit")
	}
	for name, m := range map[string]Msg{
		"a negative size":                    {Kind: KindIndex, Size: -1},
		"a size over the limit, and no body": {Kind: KindIndex, Size: MaxBody + 1},
	} {
		var buf bytes.Buffer
		if err := WriteMsg(&buf, m); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ReadBody(&buf, MaxBody); err == nil {
			t.Errorf("read a message with %s", name)
		}
	}

	var refusal bytes.Buffer
	if err := WriteMsg(&refusal, Msg{Kind: KindError, Error: "library paused"}); err != nil {
		t.Fatal(err)
	}
	m, r, err := ReadBody(&refusal, MaxBody)
	if err != nil || m.Kind != KindError || m.Error != "library paused" {
		t.Fatalf("a refusal: %+v, %v", m, err)
	}
	if got, err := io.ReadAll(r); err != nil || len(got) != 0 {
		t.Fatalf("a refusal's body: %q, %v", got, err)
	}
}

// No message is longer than MaxFrame; longer content goes in a body.
func TestMessagesFitInAFrame(t *testing.T) {
	if err := WriteMsg(io.Discard, Msg{Kind: KindError, Error: strings.Repeat("x", MaxFrame)}); err == nil {
		t.Error("wrote a message longer than MaxFrame")
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], MaxFrame+1)
	if _, err := ReadMsg(bytes.NewReader(hdr[:])); err == nil || errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("a header claiming more than MaxFrame: %v", err)
	}
}

// chunkSizes returns the sizes of the frames after the first.
func chunkSizes(t *testing.T, b []byte) []int {
	t.Helper()
	var sizes []int
	for first := true; len(b) > 0; first = false {
		n := int(binary.BigEndian.Uint32(b))
		if !first {
			sizes = append(sizes, n)
		}
		b = b[4+n:]
	}
	return sizes
}

func TestReadBlobRejectsBadFrames(t *testing.T) {
	blob := func(size int64, frames ...[]byte) []byte {
		var buf bytes.Buffer
		if err := WriteMsg(&buf, Msg{Kind: KindBlob, Hash: "deadbeef", Size: size}); err != nil {
			t.Fatal(err)
		}
		for _, f := range frames {
			buf.Write(binary.BigEndian.AppendUint32(nil, uint32(len(f))))
			buf.Write(f)
		}
		return buf.Bytes()
	}
	big := make([]byte, ChunkSize+1)
	whole := blob(3, []byte("abc"))
	for name, stream := range map[string][]byte{
		"a frame over ChunkSize":            blob(int64(len(big)), big),
		"a frame past the blob's end":       blob(3, []byte("four")),
		"an empty frame":                    blob(3, nil, []byte("abc")),
		"a stream that ends inside a blob":  blob(6, []byte("abc")),
		"a stream that ends inside a frame": whole[:len(whole)-1],
	} {
		_, size, r, err := ReadBlob(bytes.NewReader(stream))
		var got []byte
		if err == nil {
			got, err = io.ReadAll(r)
		}
		if err == nil || int64(len(got)) > size {
			t.Errorf("%s: read %d bytes of a %d-byte blob, %v", name, len(got), size, err)
		}
	}
	if _, _, _, err := ReadBlob(bytes.NewReader(blob(-1))); err == nil {
		t.Error("a blob with a negative size was read")
	}
	var other bytes.Buffer
	if err := WriteMsg(&other, Msg{Kind: KindError, Error: "no"}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ReadBlob(&other); err == nil {
		t.Error("a message that isn't a blob was read as one")
	}
}

func TestWriteBlobSendsExactlyTheContent(t *testing.T) {
	corrupt := errors.New("corrupt")
	for name, r := range map[string]io.Reader{
		"less":                 bytes.NewReader([]byte("ab")),
		"more":                 bytes.NewReader([]byte("abcd")),
		"a failure at its end": io.MultiReader(bytes.NewReader([]byte("abc")), iotest.ErrReader(corrupt)),
	} {
		if err := WriteBlob(io.Discard, "deadbeef", 3, r); err == nil {
			t.Errorf("WriteBlob sent a source holding %s as a 3-byte blob", name)
		}
	}
	if err := WriteBlob(io.Discard, "deadbeef", 3, strings.NewReader("abcd")); err == nil || !strings.Contains(err.Error(), "more than 3 bytes") {
		t.Errorf("a source holding more: %v", err)
	}
	err := WriteBlob(io.Discard, "deadbeef", 3, io.MultiReader(bytes.NewReader([]byte("abc")), iotest.ErrReader(corrupt)))
	if !errors.Is(err, corrupt) {
		t.Errorf("the source's failure was lost: %v", err)
	}
	if err := WriteBlob(io.Discard, "deadbeef", -1, bytes.NewReader(nil)); err == nil {
		t.Error("WriteBlob sent a blob with a negative size")
	}
}

func TestReadMsgLimit(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteMsg(&buf, Msg{Kind: KindHello, Name: string(bytes.Repeat([]byte("x"), 100))}); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMsgLimit(bytes.NewReader(buf.Bytes()), 50); err == nil {
		t.Fatal("read a frame over the limit")
	}
	if _, err := ReadMsgLimit(bytes.NewReader(buf.Bytes()), 200); err != nil {
		t.Fatal(err)
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], MaxFrame)
	if _, err := ReadMsgLimit(bytes.NewReader(hdr[:]), MaxPreAuthFrame); err == nil || errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("a header claiming more than the limit: %v", err)
	}
}
