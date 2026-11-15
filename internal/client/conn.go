// Package client is the client side of gomux: talking to the server and,
// from item 007 on, the terminal UI (CLAUDE.md §6.2).
package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

	"github.com/R7rainz/gomux/internal/proto"
	"github.com/R7rainz/gomux/internal/sock"
)

// ErrNoServer is returned by Dial when nothing is listening on the socket.
var ErrNoServer = sock.ErrNoServer

// VersionMismatchError means the running server speaks another protocol
// version. PID is the server's process ID as reported by the kernel, which
// has already been checked to belong to this user.
type VersionMismatchError struct {
	PID     int
	Message string // the server's explanation, control characters removed
}

func (e *VersionMismatchError) Error() string {
	return fmt.Sprintf("the running server (pid %d) is incompatible: %s; run 'gomux kill-server' to stop it (ends all its sessions)", e.PID, e.Message)
}

// Conn is a handshaken connection to the server.
type Conn struct {
	c             *net.UnixConn
	lastID        int
	ServerVersion string
}

// Dial connects to the server at path, checks it runs as this user, and
// performs the hello handshake.
func Dial(path, version string) (*Conn, error) {
	c, err := sock.Dial(path)
	if err != nil {
		return nil, err
	}
	_, pid, err := sock.Peer(c)
	if err != nil {
		c.Close()
		return nil, err
	}
	conn := &Conn{c: c}
	reply, err := conn.roundTrip(proto.TypeHello, proto.Hello{ClientVersion: version})
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("handshake: %w", err)
	}
	if reply.Error != nil {
		c.Close()
		if reply.Error.Code == proto.CodeVersionMismatch {
			return nil, &VersionMismatchError{PID: pid, Message: Printable(reply.Error.Message)}
		}
		return nil, serverError(reply.Error)
	}
	var hr proto.HelloReply
	if err := json.Unmarshal(reply.Payload, &hr); err != nil {
		c.Close()
		return nil, fmt.Errorf("handshake: %w", err)
	}
	conn.ServerVersion = Printable(hr.ServerVersion)
	return conn, nil
}

// Request sends a request and waits for its reply. A reply carrying an error
// is returned as a *proto.Error with control characters removed.
func (c *Conn) Request(typ string, payload any) (proto.Message, error) {
	reply, err := c.roundTrip(typ, payload)
	if err != nil {
		return proto.Message{}, err
	}
	if reply.Error != nil {
		return reply, serverError(reply.Error)
	}
	return reply, nil
}

// Wait blocks until the server closes the connection.
func (c *Conn) Wait() error {
	for {
		if _, err := proto.Read(c.c); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// Close closes the connection.
func (c *Conn) Close() error { return c.c.Close() }

func (c *Conn) roundTrip(typ string, payload any) (proto.Message, error) {
	c.lastID++
	m := proto.Message{Version: proto.Version, Type: typ, RequestID: strconv.Itoa(c.lastID)}
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return proto.Message{}, err
		}
		m.Payload = b
	}
	if err := proto.Write(c.c, m); err != nil {
		return proto.Message{}, err
	}
	for {
		reply, err := proto.Read(c.c)
		if err != nil {
			return proto.Message{}, err
		}
		if reply.RequestID == "" {
			continue // an event; nothing subscribes to events yet
		}
		if reply.RequestID != m.RequestID {
			return proto.Message{}, fmt.Errorf("server answered request %q while %q was pending", Printable(reply.RequestID), m.RequestID)
		}
		return reply, nil
	}
}

func serverError(e *proto.Error) error {
	return &proto.Error{Code: Printable(e.Code), Message: Printable(e.Message)}
}

// Printable removes control characters (C0, DEL, C1) from server-supplied
// text before it reaches the user's terminal, so it can't carry escape
// sequences (CLAUDE.md §13).
func Printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || (r >= 0x7f && r < 0xa0) {
			return -1
		}
		return r
	}, s)
}
