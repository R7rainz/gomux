package client

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/R7rainz/gomux/internal/proto"
	"github.com/R7rainz/gomux/internal/sock"
	"github.com/R7rainz/gomux/internal/testutil"
)

// fakeServer listens on a socket in testutil.SocketDir and runs script on the
// first connection.
func fakeServer(t *testing.T, script func(c *net.UnixConn)) string {
	t.Helper()
	path := filepath.Join(testutil.SocketDir(t), "s.sock")
	ln, err := sock.Listen(path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.AcceptUnix()
		if err != nil {
			return
		}
		defer c.Close()
		script(c)
	}()
	return path
}

// reply reads one request and answers it with payload or e.
func reply(c *net.UnixConn, payload string, e *proto.Error) proto.Message {
	m, err := proto.Read(c)
	if err != nil {
		return proto.Message{}
	}
	r := proto.Message{Version: proto.Version, Type: m.Type, RequestID: m.RequestID, Error: e}
	if payload != "" {
		r.Payload = []byte(payload)
	}
	_ = proto.Write(c, r)
	return m
}

func TestDialNoServer(t *testing.T) {
	if _, err := Dial("/tmp/gomux-test-nothing-here.sock", "vtest"); !errors.Is(err, ErrNoServer) {
		t.Fatalf("got %v, want ErrNoServer", err)
	}
}

func TestRequestSkipsEventsAndMatchesIDs(t *testing.T) {
	ids := make(chan string, 3)
	path := fakeServer(t, func(c *net.UnixConn) {
		ids <- reply(c, `{"server_version":"v9"}`, nil).RequestID
		m, _ := proto.Read(c)
		ids <- m.RequestID
		_ = proto.Write(c, proto.Message{Version: proto.Version, Type: "pane.output"}) // an event first
		_ = proto.Write(c, proto.Message{Version: proto.Version, Type: m.Type, RequestID: m.RequestID, Payload: []byte(`{"sessions":[]}`)})
		ids <- reply(c, `{}`, nil).RequestID
	})
	conn, err := Dial(path, "vtest")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if conn.ServerVersion != "v9" {
		t.Fatalf("server version %q", conn.ServerVersion)
	}
	r, err := conn.Request(proto.TypeSessionList, nil)
	if err != nil || string(r.Payload) != `{"sessions":[]}` {
		t.Fatalf("got %+v, %v", r, err)
	}
	if _, err := conn.Request(proto.TypeServerKill, nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1", "2", "3"} {
		if got := <-ids; got != want {
			t.Fatalf("request id %q, want %q", got, want)
		}
	}
}

func TestVersionMismatch(t *testing.T) {
	path := fakeServer(t, func(c *net.UnixConn) {
		reply(c, "", &proto.Error{Code: proto.CodeVersionMismatch, Message: "server speaks protocol 7\x1b]0;evil\x07"})
	})
	_, err := Dial(path, "vtest")
	var vm *VersionMismatchError
	if !errors.As(err, &vm) {
		t.Fatalf("got %v, want *VersionMismatchError", err)
	}
	if vm.PID != os.Getpid() { // the fake server runs in this process
		t.Fatalf("pid %d, want %d", vm.PID, os.Getpid())
	}
	if vm.Message != "server speaks protocol 7]0;evil" {
		t.Fatalf("control characters not removed: %q", vm.Message)
	}
}

func TestServerErrorIsPrintable(t *testing.T) {
	path := fakeServer(t, func(c *net.UnixConn) {
		reply(c, `{"server_version":"v9"}`, nil)
		reply(c, "", &proto.Error{Code: "bad\x1b", Message: "no\u009bpe"})
	})
	conn, err := Dial(path, "vtest")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = conn.Request("x", nil)
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != "bad" || pe.Message != "nope" {
		t.Fatalf("got %#v", err)
	}
}

func TestWaitReturnsWhenServerCloses(t *testing.T) {
	path := fakeServer(t, func(c *net.UnixConn) {
		reply(c, `{"server_version":"v9"}`, nil)
	}) // script returns, closing the connection
	conn, err := Dial(path, "vtest")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.Wait(); err != nil {
		t.Fatal(err)
	}
}
