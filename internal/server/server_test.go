package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/R7rainz/gomux/internal/proto"
	"github.com/R7rainz/gomux/internal/sock"
	"github.com/R7rainz/gomux/internal/testutil"
)

type running struct {
	path   string
	cancel context.CancelFunc
	done   chan error
}

// start runs a server on a socket in testutil.SocketDir.
func start(t *testing.T) *running {
	t.Helper()
	path := filepath.Join(testutil.SocketDir(t), "s.sock")
	ln, err := sock.Listen(path, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{path: path, cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- Serve(ctx, ln, "vtest", slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	t.Cleanup(func() { cancel(); <-r.done })
	return r
}

func dial(t *testing.T, path string) *net.UnixConn {
	t.Helper()
	c, err := sock.Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func roundTrip(t *testing.T, c *net.UnixConn, m proto.Message) proto.Message {
	t.Helper()
	if err := proto.Write(c, m); err != nil {
		t.Fatal(err)
	}
	reply, err := proto.Read(c)
	if err != nil {
		t.Fatalf("reading reply to %s: %v", m.Type, err)
	}
	if reply.Type != m.Type || reply.RequestID != m.RequestID {
		t.Fatalf("reply %+v does not match request %+v", reply, m)
	}
	return reply
}

func hello(t *testing.T, c *net.UnixConn) {
	t.Helper()
	reply := roundTrip(t, c, proto.Message{Version: proto.Version, Type: proto.TypeHello, RequestID: "1", Payload: []byte(`{"client_version":"vtest"}`)})
	var hr proto.HelloReply
	if reply.Error != nil || json.Unmarshal(reply.Payload, &hr) != nil || hr.ServerVersion != "vtest" {
		t.Fatalf("hello reply %+v", reply)
	}
}

// closed asserts the server has closed c.
func closed(t *testing.T, c *net.UnixConn) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := proto.Read(c); !errors.Is(err, io.EOF) {
		t.Fatalf("connection still open, or not closed cleanly: %v", err)
	}
}

func gone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket file still there: %v", err)
	}
}

func TestHandshakeAndSessionList(t *testing.T) {
	r := start(t)
	c := dial(t, r.path)
	hello(t, c)
	reply := roundTrip(t, c, proto.Message{Version: proto.Version, Type: proto.TypeSessionList, RequestID: "2"})
	if reply.Error != nil || string(reply.Payload) != `{"sessions":[]}` {
		t.Fatalf("session.list reply %+v", reply)
	}
}

func TestVersionMismatch(t *testing.T) {
	r := start(t)
	c := dial(t, r.path)
	reply := roundTrip(t, c, proto.Message{Version: proto.Version + 1, Type: proto.TypeHello, RequestID: "1"})
	if reply.Error == nil || reply.Error.Code != proto.CodeVersionMismatch {
		t.Fatalf("got %+v, want version_mismatch", reply)
	}
	closed(t, c)
}

func TestFirstMessageMustBeHello(t *testing.T) {
	r := start(t)
	c := dial(t, r.path)
	if err := proto.Write(c, proto.Message{Version: proto.Version, Type: proto.TypeSessionList, RequestID: "1"}); err != nil {
		t.Fatal(err)
	}
	closed(t, c)
}

func TestSilentConnectionTimesOut(t *testing.T) {
	old := handshakeTimeout
	handshakeTimeout = 50 * time.Millisecond
	t.Cleanup(func() { handshakeTimeout = old })
	r := start(t)
	closed(t, dial(t, r.path))
}

func TestUnknownTypeKeepsConnection(t *testing.T) {
	r := start(t)
	c := dial(t, r.path)
	hello(t, c)
	reply := roundTrip(t, c, proto.Message{Version: proto.Version, Type: "no.such", RequestID: "2"})
	if reply.Error == nil || reply.Error.Code != proto.CodeUnknownType {
		t.Fatalf("got %+v, want unknown_type", reply)
	}
	roundTrip(t, c, proto.Message{Version: proto.Version, Type: proto.TypeSessionList, RequestID: "3"})
}

func TestOtherVersionAfterHandshakeCloses(t *testing.T) {
	r := start(t)
	c := dial(t, r.path)
	hello(t, c)
	if err := proto.Write(c, proto.Message{Version: proto.Version + 1, Type: proto.TypeSessionList, RequestID: "2"}); err != nil {
		t.Fatal(err)
	}
	closed(t, c)
}

func TestKillShutsDown(t *testing.T) {
	r := start(t)
	other := dial(t, r.path) // a second client must be disconnected too
	hello(t, other)
	c := dial(t, r.path)
	hello(t, c)
	if reply := roundTrip(t, c, proto.Message{Version: proto.Version, Type: proto.TypeServerKill, RequestID: "2"}); reply.Error != nil {
		t.Fatalf("kill reply %+v", reply)
	}
	closed(t, c)
	gone(t, r.path) // already removed by the time the client sees EOF
	closed(t, other)
	select {
	case err := <-r.done:
		if err != nil {
			t.Fatal(err)
		}
		r.done <- nil // for the cleanup's receive
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}
}

func TestCancelShutsDown(t *testing.T) {
	r := start(t)
	c := dial(t, r.path)
	hello(t, c)
	r.cancel()
	closed(t, c)
	gone(t, r.path)
}

func TestConcurrentClients(t *testing.T) {
	r := start(t)
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Go(func() {
			c := dial(t, r.path)
			hello(t, c)
			for j := range 20 {
				roundTrip(t, c, proto.Message{Version: proto.Version, Type: proto.TypeSessionList, RequestID: string(rune('a'+i)) + string(rune('a'+j))})
			}
		})
	}
	wg.Wait()
}
