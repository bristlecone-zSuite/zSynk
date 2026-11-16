package transport

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
)

// MaxFrame bounds every frame. Content that can be longer, such as a
// blob's, an index or a want list, follows its message as a body.
const MaxFrame = 1 << 20

// ChunkSize bounds each frame of a body. A body of any size follows its
// message as a run of these.
const ChunkSize = 1 << 20

// MaxBody bounds an index or a want list: 512 bytes for each of the
// million files a device accepts in an index. Most entries take 300 to 400.
const MaxBody = 512 << 20

type Kind string

const (
	KindHello    Kind = "hello"
	KindHelloOK  Kind = "hello-ok"
	KindIndex    Kind = "index"
	KindWant     Kind = "want"
	KindBlob     Kind = "blob"
	KindPairJoin Kind = "pair-join"
	KindPairOK   Kind = "pair-ok"
	KindError    Kind = "error"
	KindDone     Kind = "done"
)

// Msg is one frame. TLS authenticates both devices and protects every
// byte, so frames carry no signatures; who sent one is the key on the
// connection.
type Msg struct {
	Kind     Kind   `json:"kind"`
	Name     string `json:"name,omitempty"`
	FolderID string `json:"folderId,omitempty"`
	// Proof shows the sender knows a pairing offer's code and token. It's
	// bound to the connection, so it can't be used on another.
	Proof string `json:"proof,omitempty"`
	Error string `json:"error,omitempty"`
	Hash  string `json:"hash,omitempty"`
	// Size is the length of the body that follows the message: a blob's
	// content, an index, or a want list.
	Size       int64  `json:"size,omitempty"`
	ListenPort string `json:"listenPort,omitempty"`
}

func WriteMsg(w io.Writer, m Msg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return writeFrame(w, b)
}

func ReadMsg(r io.Reader) (Msg, error) {
	return ReadMsgLimit(r, MaxFrame)
}

// ReadMsgLimit reads a message no longer than limit bytes.
func ReadMsgLimit(r io.Reader, limit uint32) (Msg, error) {
	b, err := readFrame(r, limit)
	if err != nil {
		return Msg{}, err
	}
	var m Msg
	if err := json.Unmarshal(b, &m); err != nil {
		return Msg{}, err
	}
	return m, nil
}

// WriteBlob sends the size bytes r holds as the content of hash: a blob
// message with the size, then the content in frames of at most ChunkSize
// bytes. It fails if r holds more or less, or fails at its end, as
// blob.Verify does for content that doesn't match its hash.
func WriteBlob(w io.Writer, hash string, size int64, r io.Reader) error {
	if size < 0 {
		return fmt.Errorf("transport: blob %s has a negative size", hash)
	}
	if err := WriteMsg(w, Msg{Kind: KindBlob, Hash: hash, Size: size}); err != nil {
		return err
	}
	if err := writeChunks(w, r, size); err != nil {
		return fmt.Errorf("transport: blob %s: %w", hash, err)
	}
	var more [1]byte
	switch _, err := io.ReadFull(r, more[:]); err {
	case io.EOF:
		return nil
	case nil:
		return fmt.Errorf("transport: blob %s holds more than %d bytes", hash, size)
	default:
		return fmt.Errorf("transport: blob %s: %w", hash, err)
	}
}

// ReadBlob reads a blob message and returns a reader for the content
// that follows it, which ends after size bytes. Read the content to its
// end before reading anything else from r.
func ReadBlob(r io.Reader) (hash string, size int64, content io.Reader, err error) {
	m, body, err := ReadBody(r, math.MaxInt64)
	if err != nil {
		return "", 0, nil, err
	}
	if m.Kind != KindBlob {
		return "", 0, nil, fmt.Errorf("transport: expected blob, got %.40q", m.Kind)
	}
	return m.Hash, m.Size, body, nil
}

// WriteBody sends m and then body, which can be longer than a frame: m
// gives its size, and it follows in frames of at most ChunkSize bytes.
func WriteBody(w io.Writer, m Msg, body []byte) error {
	m.Size = int64(len(body))
	if err := WriteMsg(w, m); err != nil {
		return err
	}
	return writeChunks(w, bytes.NewReader(body), m.Size)
}

// ReadBody reads a message and returns a reader for the body that follows
// it, which ends after the message's size. It fails if the size is over
// limit. A message without a body, such as an error, has an empty one.
// Read the body to its end before reading anything else from r.
func ReadBody(r io.Reader, limit int64) (Msg, io.Reader, error) {
	m, err := ReadMsg(r)
	if err != nil {
		return Msg{}, nil, err
	}
	switch {
	case m.Size < 0:
		return Msg{}, nil, fmt.Errorf("transport: a %.40q message with a negative size", m.Kind)
	case m.Size > limit:
		return Msg{}, nil, fmt.Errorf("transport: a %.40q body of %d bytes, over the limit of %d", m.Kind, m.Size, limit)
	}
	return m, &chunks{r: r, left: m.Size}, nil
}

// writeChunks sends size bytes from r in frames of at most ChunkSize
// bytes.
func writeChunks(w io.Writer, r io.Reader, size int64) error {
	buf := make([]byte, 4+min(size, ChunkSize))
	for left := size; left > 0; {
		n := min(left, ChunkSize)
		frame := buf[:4+n]
		if _, err := io.ReadFull(r, frame[4:]); err != nil {
			return err
		}
		binary.BigEndian.PutUint32(frame, uint32(n))
		if _, err := w.Write(frame); err != nil {
			return err
		}
		left -= n
	}
	return nil
}

// chunks reads a body from the frames that carry it.
type chunks struct {
	r     io.Reader
	left  int64 // body not yet read
	frame int64 // body left in the current frame
}

func (c *chunks) Read(p []byte) (int, error) {
	if c.left == 0 {
		return 0, io.EOF
	}
	if c.frame == 0 {
		var hdr [4]byte
		if _, err := io.ReadFull(c.r, hdr[:]); err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return 0, err
		}
		n := int64(binary.BigEndian.Uint32(hdr[:]))
		if n == 0 || n > ChunkSize || n > c.left {
			return 0, fmt.Errorf("transport: a frame of %d bytes, with %d bytes of the body left", n, c.left)
		}
		c.frame = n
	}
	if int64(len(p)) > c.frame {
		p = p[:c.frame]
	}
	n, err := c.r.Read(p)
	c.frame -= int64(n)
	c.left -= int64(n)
	if err == io.EOF && c.left > 0 {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

func writeFrame(w io.Writer, payload []byte) error {
	if len(payload) > MaxFrame {
		return fmt.Errorf("transport: frame too large")
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func readFrame(r io.Reader, limit uint32) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > limit {
		return nil, fmt.Errorf("transport: frame %d exceeds limit %d", n, limit)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
