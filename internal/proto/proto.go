// Package proto is the wire format between the gomux client and server:
// length-prefixed JSON messages over a Unix socket (CLAUDE.md §6.8).
//
// Each frame is a 4-byte big-endian body length followed by a JSON Message.
// Read is a trust boundary: it never allocates before checking the length
// against MaxFrame, and any error means the stream must be closed, because
// after a bad frame the next frame boundary can't be found.
package proto

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Version is the protocol version, carried in every message's envelope.
// Bump it on any change an older peer would misread.
const Version = 1

// MaxFrame is the largest message body accepted or sent, in bytes.
const MaxFrame = 16 << 20

// ErrFrameTooLarge reports a frame body over MaxFrame.
var ErrFrameTooLarge = errors.New("proto: frame too large")

// Message is the envelope every frame carries. A request sets RequestID; its
// response repeats the request's Type and RequestID and carries either
// Payload or Error. An event has no RequestID.
type Message struct {
	Version   int             `json:"version"`
	Type      string          `json:"type"`
	RequestID string          `json:"request_id,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Error     *Error          `json:"error,omitempty"`
}

// Error is a failed response's error. It implements error.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// The handshake: a connection's first frame is a hello request, whose
// envelope Version is the client's protocol version.
const (
	TypeHello           = "hello"
	CodeVersionMismatch = "version_mismatch"
)

// Hello is the payload of the client's hello request.
type Hello struct {
	ClientVersion string `json:"client_version"`
}

// HelloReply is the payload of the server's successful hello response.
type HelloReply struct {
	ServerVersion string `json:"server_version"`
}

// Write sends m as one frame, in a single w.Write call.
func Write(w io.Writer, m Message) error {
	var buf bytes.Buffer
	buf.Write([]byte{0, 0, 0, 0}) // length, filled in below
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // default escaping turns <, >, & into 6-byte sequences
	if err := enc.Encode(m); err != nil {
		return fmt.Errorf("proto: encode %q: %w", m.Type, err)
	}
	frame := bytes.TrimSuffix(buf.Bytes(), []byte("\n")) // Encode appends a newline
	n := len(frame) - 4
	if n > MaxFrame {
		return fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, n)
	}
	binary.BigEndian.PutUint32(frame, uint32(n))
	_, err := w.Write(frame)
	return err
}

// Read reads one frame. It returns io.EOF only when the stream ends cleanly
// between frames; a frame cut short is io.ErrUnexpectedEOF.
func Read(r io.Reader) (Message, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Message{}, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 {
		return Message{}, errors.New("proto: empty frame")
	}
	if n > MaxFrame {
		return Message{}, fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return Message{}, err
	}
	var m Message
	if err := json.Unmarshal(body, &m); err != nil {
		return Message{}, fmt.Errorf("proto: bad message: %w", err)
	}
	if m.Type == "" {
		return Message{}, errors.New("proto: message has no type")
	}
	return m, nil
}
