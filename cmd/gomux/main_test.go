package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/R7rainz/gomux/internal/proto"
	"github.com/R7rainz/gomux/internal/sock"
)

// TestMain lets the test binary double as a fake old server (see
// fakeOldServer), started as a separate process by TestKillServerStopsOldServer.
func TestMain(m *testing.M) {
	if path := os.Getenv("GOMUX_TEST_OLD_SERVER"); path != "" {
		fakeOldServer(path)
		return
	}
	os.Exit(m.Run())
}

// fakeOldServer behaves like a server from another protocol version: it
// answers every hello with version_mismatch, and shuts down on SIGTERM.
func fakeOldServer(path string) {
	ln, err := sock.Listen(path, false)
	if err != nil {
		os.Exit(3)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		ln.Close() // removes the socket file
		os.Exit(0)
	}()
	for {
		c, err := ln.AcceptUnix()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			if m, err := proto.Read(c); err == nil {
				_ = proto.Write(c, proto.Message{Version: 0, Type: m.Type, RequestID: m.RequestID,
					Error: &proto.Error{Code: proto.CodeVersionMismatch, Message: "server speaks protocol 0 (gomux v0.0.1), client speaks 1"}})
			}
		}()
	}
}

type env struct {
	t    *testing.T
	bin  string
	path string // GOMUX_SOCKET
	dir  string
}

// setup builds gomux with the race detector into a short /tmp directory
// (macOS limits socket paths to 104 bytes) and points GOMUX_SOCKET there.
func setup(t *testing.T) *env {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "gmx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	bin := filepath.Join(dir, "gomux")
	if out, err := exec.Command("go", "build", "-race", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return &env{t: t, bin: bin, path: filepath.Join(dir, "s.sock"), dir: dir}
}

func (e *env) cmd(args ...string) *exec.Cmd {
	c := exec.Command(e.bin, args...)
	c.Env = append(os.Environ(), "GOMUX_SOCKET="+e.path)
	return c
}

// gomux runs a client command and returns stdout, stderr and the exit code.
func (e *env) gomux(args ...string) (string, string, int) {
	e.t.Helper()
	var stdout, stderr bytes.Buffer
	c := e.cmd(args...)
	c.Stdout, c.Stderr = &stdout, &stderr
	err := c.Run()
	code := 0
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		code = ee.ExitCode()
	} else if err != nil {
		e.t.Fatal(err)
	}
	return stdout.String(), stderr.String(), code
}

func (e *env) waitFor(what string, ok func() bool) {
	e.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !ok(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func (e *env) socketExists() bool { _, err := os.Stat(e.path); return err == nil }

func waitExit(t *testing.T, c *exec.Cmd) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		_ = c.Process.Kill()
		t.Fatal("process did not exit")
		return nil
	}
}

func TestEndToEnd(t *testing.T) {
	e := setup(t)

	if _, stderr, code := e.gomux("ls"); code != 1 || !strings.Contains(stderr, "no server running on "+e.path) {
		t.Fatalf("ls without a server: code %d, stderr %q", code, stderr)
	}
	if _, stderr, code := e.gomux("kill-server"); code != 1 || !strings.Contains(stderr, "no server running") {
		t.Fatalf("kill-server without a server: code %d, stderr %q", code, stderr)
	}
	if _, _, code := e.gomux(); code != 2 {
		t.Fatalf("no command: code %d, want 2", code)
	}
	if _, _, code := e.gomux("bogus"); code != 2 {
		t.Fatalf("unknown command: code %d, want 2", code)
	}
	if stdout, _, code := e.gomux("--version"); code != 0 || !regexp.MustCompile(`^gomux \S+ \(protocol 1\)\n$`).MatchString(stdout) {
		t.Fatalf("--version: code %d, stdout %q", code, stdout)
	}

	logFile, err := os.Create(filepath.Join(e.dir, "server.stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	srv := e.cmd("server", "--foreground", "--log-level", "debug")
	srv.Stderr = logFile
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	e.waitFor("the server's socket", e.socketExists)

	if stdout, stderr, code := e.gomux("ls"); code != 0 || stdout != "no sessions\n" {
		t.Fatalf("ls: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if _, stderr, code := e.gomux("server", "--foreground"); code != 1 || !strings.Contains(stderr, "already running") {
		t.Fatalf("second server: code %d, stderr %q", code, stderr)
	}
	if fi, err := os.Stat(e.path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode: %v, %v", fi, err)
	}

	if stdout, stderr, code := e.gomux("kill-server"); code != 0 {
		t.Fatalf("kill-server: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if e.socketExists() {
		t.Fatal("socket still there after kill-server returned")
	}
	if err := waitExit(t, srv); err != nil {
		t.Fatalf("server exit: %v", err) // includes race reports: exit 66
	}
	log, _ := os.ReadFile(logFile.Name())
	for _, want := range []string{"server started", `type=session.list`, "shutting down at a client's request", "server stopped"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("server log lacks %q:\n%s", want, log)
		}
	}
}

func TestKillServerStopsOldServer(t *testing.T) {
	e := setup(t)
	old := exec.Command(os.Args[0], "-test.run=^$")
	old.Env = append(os.Environ(), "GOMUX_TEST_OLD_SERVER="+e.path)
	if err := old.Start(); err != nil {
		t.Fatal(err)
	}
	e.waitFor("the old server's socket", e.socketExists)

	_, stderr, code := e.gomux("ls")
	if code != 1 || !strings.Contains(stderr, "incompatible") || !strings.Contains(stderr, "gomux kill-server") {
		t.Fatalf("ls against an old server: code %d, stderr %q", code, stderr)
	}
	stdout, stderr, code := e.gomux("kill-server")
	if code != 0 || !strings.Contains(stdout, "stopped the old gomux server") {
		t.Fatalf("kill-server: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if err := waitExit(t, old); err != nil {
		t.Fatalf("old server did not exit cleanly: %v", err)
	}
	if e.socketExists() {
		t.Fatal("old server's socket still there")
	}
}
