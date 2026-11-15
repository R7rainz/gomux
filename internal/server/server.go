// Package server is the gomux server: it owns all runtime state and serves
// clients over the Unix socket (CLAUDE.md §6.1–§6.3).
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/R7rainz/gomux/internal/proto"
	"github.com/R7rainz/gomux/internal/sock"
)

// handshakeTimeout bounds how long a new connection may take to send hello.
var handshakeTimeout = 5 * time.Second

type server struct {
	version string
	log     *slog.Logger
	reqs    chan request // to the event loop
}

type request struct {
	msg   proto.Message
	reply chan response
}

type response struct {
	msg      proto.Message
	shutdown bool // send msg, then shut the server down
}

// Serve serves clients on ln until ctx is canceled or a client sends
// server.kill. It then closes the listener (which removes the socket file),
// closes every client connection, waits for their goroutines and returns.
func Serve(ctx context.Context, ln *net.UnixListener, version string, log *slog.Logger) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	s := &server{version: version, log: log, reqs: make(chan request)}
	go s.loop(ctx)
	go func() { <-ctx.Done(); ln.Close() }() // unblocks AcceptUnix

	var (
		mu    sync.Mutex
		conns = map[*net.UnixConn]struct{}{}
		wg    sync.WaitGroup
	)
	for {
		c, err := ln.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				break
			}
			log.Error("accept failed", "err", err)
			time.Sleep(100 * time.Millisecond) // e.g. out of file descriptors: don't spin
			continue
		}
		mu.Lock()
		conns[c] = struct{}{}
		mu.Unlock()
		wg.Go(func() {
			s.serveConn(ctx, c, stop)
			mu.Lock()
			defer mu.Unlock()
			if ctx.Err() != nil {
				return // shutting down: closed below, after the listener
			}
			c.Close()
			delete(conns, c)
		})
	}
	// Reached only after the listener is closed (socket file removed), so a
	// client that sees its connection close knows the server is gone.
	mu.Lock()
	for c := range conns {
		c.Close()
	}
	mu.Unlock()
	wg.Wait()
	log.Info("server stopped")
	return nil
}

// loop is the event loop: the only goroutine that touches server state.
func (s *server) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case r := <-s.reqs:
			r.reply <- s.handle(r.msg)
		}
	}
}

func (s *server) handle(m proto.Message) response {
	reply := proto.Message{Version: proto.Version, Type: m.Type, RequestID: m.RequestID}
	switch m.Type {
	case proto.TypeSessionList:
		reply.Payload = mustJSON(proto.SessionList{Sessions: []proto.SessionInfo{}})
	case proto.TypeServerKill:
		return response{msg: reply, shutdown: true}
	default:
		reply.Error = &proto.Error{Code: proto.CodeUnknownType, Message: fmt.Sprintf("unknown request type %q", m.Type)}
	}
	return response{msg: reply}
}

func (s *server) serveConn(ctx context.Context, c *net.UnixConn, shutdown func()) {
	uid, pid, err := sock.Peer(c)
	if err != nil || uid != uint32(os.Getuid()) {
		s.log.Warn("rejected a connection from another user", "uid", uid, "pid", pid, "err", err)
		return
	}
	log := s.log.With("client_pid", pid)
	if !s.handshake(c, log) {
		return
	}
	log.Debug("client connected")
	defer log.Debug("client disconnected")
	for {
		m, err := proto.Read(c)
		if err != nil {
			if !errors.Is(err, io.EOF) && ctx.Err() == nil {
				log.Debug("closing connection", "err", err)
			}
			return
		}
		if m.Version != proto.Version {
			log.Debug("closing connection: message from another protocol version", "version", m.Version)
			return
		}
		log.Debug("request", "type", m.Type) // never the payload: it may hold keystrokes
		reply := make(chan response, 1)
		select {
		case s.reqs <- request{m, reply}:
		case <-ctx.Done():
			return
		}
		var resp response
		select {
		case resp = <-reply:
		case <-ctx.Done():
			return
		}
		if err := proto.Write(c, resp.msg); err != nil {
			return
		}
		if resp.shutdown { // only after the reply is sent, so the client gets it
			log.Info("shutting down at a client's request")
			shutdown()
			return
		}
	}
}

// handshake reads the first frame, which must be hello within handshakeTimeout
// and in this server's protocol version, and answers it.
func (s *server) handshake(c *net.UnixConn, log *slog.Logger) bool {
	_ = c.SetReadDeadline(time.Now().Add(handshakeTimeout))
	m, err := proto.Read(c)
	if err != nil {
		log.Debug("no handshake", "err", err)
		return false
	}
	_ = c.SetReadDeadline(time.Time{})
	if m.Type != proto.TypeHello {
		log.Debug("first message was not hello", "type", m.Type)
		return false
	}
	reply := proto.Message{Version: proto.Version, Type: proto.TypeHello, RequestID: m.RequestID}
	if m.Version != proto.Version {
		log.Info("client speaks another protocol version", "client_protocol", m.Version)
		reply.Error = &proto.Error{
			Code:    proto.CodeVersionMismatch,
			Message: fmt.Sprintf("server speaks protocol %d (gomux %s), client speaks %d", proto.Version, s.version, m.Version),
		}
		_ = proto.Write(c, reply)
		return false
	}
	reply.Payload = mustJSON(proto.HelloReply{ServerVersion: s.version})
	return proto.Write(c, reply) == nil
}

// mustJSON marshals protocol payload structs, which cannot fail to encode.
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
