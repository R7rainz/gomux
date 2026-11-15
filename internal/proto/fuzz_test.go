package proto

import (
	"bytes"
	"testing"
)

// FuzzRead feeds arbitrary bytes to the decoder, which reads input from other
// processes (a trust boundary, CLAUDE.md §13). Plain `go test` replays the
// seeds; `go test -fuzz FuzzRead` explores further.
func FuzzRead(f *testing.F) {
	var valid bytes.Buffer
	if err := Write(&valid, Message{Version: Version, Type: TypeHello, RequestID: "1", Payload: []byte(`{"client_version":"v0.1.0"}`)}); err != nil {
		f.Fatal(err)
	}
	f.Add(valid.Bytes())
	f.Add(valid.Bytes()[:valid.Len()-3]) // truncated body
	f.Add([]byte{0xff, 0xff, 0xff, 0xff})  // length far over MaxFrame
	f.Add([]byte{0, 0, 0, 0})              // zero length
	f.Add(frame(`{"type":"x","payload":[1,{"a":"é<>"}],"error":{"code":"c"}}`))
	f.Add(frame("{\"type\":\"\xff\xfe\"}")) // invalid UTF-8
	f.Add([]byte("garbage that is not a frame"))

	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := Read(bytes.NewReader(data)) // must never panic
		if err != nil {
			return
		}
		// Whatever Read accepts must re-encode, and re-encoding must be stable.
		var b1, b2 bytes.Buffer
		if err := Write(&b1, m); err != nil {
			t.Fatalf("Write rejected a message Read accepted: %v", err)
		}
		m2, err := Read(bytes.NewReader(b1.Bytes()))
		if err != nil {
			t.Fatalf("Read rejected Write's own output: %v", err)
		}
		if err := Write(&b2, m2); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(b1.Bytes(), b2.Bytes()) {
			t.Fatalf("re-encoding is not stable:\n%q\n%q", b1.Bytes(), b2.Bytes())
		}
	})
}
