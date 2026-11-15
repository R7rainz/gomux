package proto

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

// frame builds a raw frame around body, bypassing Write's checks.
func frame(body string) []byte {
	b := make([]byte, 4, 4+len(body))
	binary.BigEndian.PutUint32(b, uint32(len(body)))
	return append(b, body...)
}

func mustWrite(t *testing.T, w io.Writer, m Message) {
	t.Helper()
	if err := Write(w, m); err != nil {
		t.Fatal(err)
	}
}

func TestRoundTrip(t *testing.T) {
	cases := map[string]Message{
		"request":        {Version: Version, Type: "session.list", RequestID: "7"},
		"response":       {Version: Version, Type: "session.list", RequestID: "7", Payload: json.RawMessage(`{"sessions":[]}`)},
		"error response": {Version: Version, Type: "session.kill", RequestID: "8", Error: &Error{Code: "not_found", Message: "no session api"}},
		"event":          {Version: Version, Type: "pane.output", Payload: json.RawMessage(`{"pane":1}`)},
		"html chars":     {Version: Version, Type: "paste", Payload: json.RawMessage(`{"text":"a<b>&c"}`)},
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			mustWrite(t, &buf, want)
			got, err := Read(&buf)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %+v, want %+v", got, want)
			}
		})
	}
}

func TestStreamOfFrames(t *testing.T) {
	var buf bytes.Buffer
	for _, id := range []string{"1", "2", "3"} {
		mustWrite(t, &buf, Message{Version: Version, Type: "ping", RequestID: id})
	}
	for _, want := range []string{"1", "2", "3"} {
		m, err := Read(&buf)
		if err != nil || m.RequestID != want {
			t.Fatalf("got %+v, %v; want request %s", m, err, want)
		}
	}
	if _, err := Read(&buf); err != io.EOF {
		t.Fatalf("after the last frame: %v, want io.EOF", err)
	}
}

func TestReadRejects(t *testing.T) {
	oversized := make([]byte, 4)
	binary.BigEndian.PutUint32(oversized, MaxFrame+1)
	cases := []struct {
		name  string
		input []byte
		want  error  // matched with errors.Is, if set
		text  string // substring of the error, if set
	}{
		{"clean EOF", nil, io.EOF, ""},
		{"truncated header", []byte{0, 0}, io.ErrUnexpectedEOF, ""},
		{"truncated body", frame(`{"type":"x"}`)[:8], io.ErrUnexpectedEOF, ""},
		{"header only, body missing", frame(`{"type":"x"}`)[:4], io.ErrUnexpectedEOF, ""},
		{"zero length", []byte{0, 0, 0, 0}, nil, "empty frame"},
		{"oversized length", oversized, ErrFrameTooLarge, ""},
		{"invalid JSON", frame(`{"type":`), nil, "bad message"},
		{"array body", frame(`[]`), nil, "bad message"},
		{"string body", frame(`"x"`), nil, "bad message"},
		{"null body", frame(`null`), nil, "no type"},
		{"missing type", frame(`{"version":1}`), nil, "no type"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Read(bytes.NewReader(c.input))
			if err == nil {
				t.Fatal("no error")
			}
			if c.want != nil && !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
			if c.text != "" && !strings.Contains(err.Error(), c.text) {
				t.Fatalf("got %v, want it to mention %q", err, c.text)
			}
		})
	}
}

func TestOversizedHeaderLeavesBodyUnread(t *testing.T) {
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, MaxFrame+1)
	r := bytes.NewReader(append(hdr, "rest"...))
	if _, err := Read(r); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("got %v", err)
	}
	if r.Len() != len("rest") {
		t.Fatalf("Read consumed %d body bytes after rejecting the header", len("rest")-r.Len())
	}
}

func TestWriteRefusesOversized(t *testing.T) {
	big := json.RawMessage(`"` + strings.Repeat("x", MaxFrame) + `"`)
	var buf bytes.Buffer
	if err := Write(&buf, Message{Version: Version, Type: "x", Payload: big}); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("got %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("wrote %d bytes of a refused frame", buf.Len())
	}
}

func TestUnknownFieldsIgnored(t *testing.T) {
	m, err := Read(bytes.NewReader(frame(`{"version":1,"type":"hello","added_later":true}`)))
	if err != nil || m.Type != "hello" {
		t.Fatalf("got %+v, %v", m, err)
	}
}

func TestHTMLCharactersNotEscaped(t *testing.T) {
	var buf bytes.Buffer
	mustWrite(t, &buf, Message{Version: Version, Type: "paste", Payload: json.RawMessage(`{"text":"<&>"}`)})
	if !bytes.Contains(buf.Bytes(), []byte(`"<&>"`)) {
		t.Fatalf("escaped on the wire: %s", buf.Bytes()[4:])
	}
}

func TestErrorIsAnError(t *testing.T) {
	var err error = &Error{Code: CodeVersionMismatch, Message: "server speaks protocol 1, client speaks 2"}
	if err.Error() != "version_mismatch: server speaks protocol 1, client speaks 2" {
		t.Fatalf("got %q", err.Error())
	}
}

// TestHandshakeWireFormat pins the exact bytes of the handshake shown in the
// brief, since every future client and server version must agree on them.
func TestHandshakeWireFormat(t *testing.T) {
	payload := func(v any) json.RawMessage {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	cases := []struct {
		m    Message
		want string
	}{
		{
			Message{Version: Version, Type: TypeHello, RequestID: "1", Payload: payload(Hello{ClientVersion: "v0.1.0"})},
			`{"version":1,"type":"hello","request_id":"1","payload":{"client_version":"v0.1.0"}}`,
		},
		{
			Message{Version: Version, Type: TypeHello, RequestID: "1", Payload: payload(HelloReply{ServerVersion: "v0.1.0"})},
			`{"version":1,"type":"hello","request_id":"1","payload":{"server_version":"v0.1.0"}}`,
		},
		{
			Message{Version: Version, Type: TypeHello, RequestID: "1", Error: &Error{Code: CodeVersionMismatch, Message: "server speaks protocol 1, client speaks 2"}},
			`{"version":1,"type":"hello","request_id":"1","error":{"code":"version_mismatch","message":"server speaks protocol 1, client speaks 2"}}`,
		},
	}
	for _, c := range cases {
		var buf bytes.Buffer
		mustWrite(t, &buf, c.m)
		if got := buf.String(); got != string(frame(c.want)) {
			t.Errorf("got  %q\nwant %q", got[4:], c.want)
		}
	}
}
