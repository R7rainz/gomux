package sock

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/R7rainz/gomux/internal/testutil"
)

func TestPrivateDir(t *testing.T) {
	base := testutil.SocketDir(t)
	uid := os.Getuid()

	fresh := filepath.Join(base, "fresh")
	if err := ensurePrivateDir(fresh, uid); err != nil {
		t.Fatalf("creating: %v", err)
	}
	if fi, err := os.Stat(fresh); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm() != 0o700 {
		t.Fatalf("created with %#o, want 0700", fi.Mode().Perm())
	}
	if err := ensurePrivateDir(fresh, uid); err != nil {
		t.Fatalf("existing private dir refused: %v", err)
	}

	open := filepath.Join(base, "open")
	if err := os.Mkdir(open, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(fresh, link); err != nil {
		t.Fatal(err)
	}

	refused := []struct {
		name, dir string
		uid       int
		mention   string
	}{
		{"group/other access", open, uid, "chmod 700"},
		{"symlink", link, uid, "not a directory"},
		{"another owner", fresh, uid + 1, "another user"},
	}
	for _, r := range refused {
		if err := ensurePrivateDir(r.dir, r.uid); err == nil || !strings.Contains(err.Error(), r.mention) {
			t.Errorf("%s: got %v, want an error mentioning %q", r.name, err, r.mention)
		}
	}
}

func TestListenSocketMode(t *testing.T) {
	path := filepath.Join(testutil.SocketDir(t), "gomux", "s.sock")
	ln, err := Listen(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %#o, want 0600", fi.Mode().Perm())
	}
	ln.Close()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket not removed on close: %v", err)
	}
}

func TestListenWhatsAlreadyThere(t *testing.T) {
	dir := testutil.SocketDir(t)

	// A live server: refuse.
	live := filepath.Join(dir, "live.sock")
	ln, err := Listen(live, false)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if _, err := Listen(live, false); !errors.Is(err, ErrRunning) {
		t.Fatalf("second server: got %v, want ErrRunning", err)
	}

	// A stale socket (bound, listener gone, file left behind): removed.
	stale := filepath.Join(dir, "stale.sock")
	sl, err := net.ListenUnix("unix", &net.UnixAddr{Name: stale, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	sl.SetUnlinkOnClose(false)
	sl.Close()
	ln2, err := Listen(stale, false)
	if err != nil {
		t.Fatalf("stale socket not cleared: %v", err)
	}
	ln2.Close()

	// A regular file: refused, and left alone.
	file := filepath.Join(dir, "file.sock")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(file, false); err == nil || !strings.Contains(err.Error(), "not a socket") {
		t.Fatalf("regular file: got %v", err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("regular file was removed: %v", err)
	}
}

func TestDialAndPeer(t *testing.T) {
	path := filepath.Join(testutil.SocketDir(t), "s.sock")
	if _, err := Dial(path); !errors.Is(err, ErrNoServer) {
		t.Fatalf("no server: got %v, want ErrNoServer", err)
	}

	ln, err := Listen(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan *net.UnixConn, 1)
	go func() {
		c, err := ln.AcceptUnix()
		if err == nil {
			accepted <- c
		}
	}()

	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	uid, pid, err := Peer(c) // the client's view of the server: this test process
	if err != nil || uid != uint32(os.Getuid()) || pid != os.Getpid() {
		t.Fatalf("client sees uid=%d pid=%d err=%v; want uid=%d pid=%d", uid, pid, err, os.Getuid(), os.Getpid())
	}
	sc := <-accepted
	defer sc.Close()
	if uid, pid, err := Peer(sc); err != nil || uid != uint32(os.Getuid()) || pid != os.Getpid() {
		t.Fatalf("server sees uid=%d pid=%d err=%v", uid, pid, err)
	}
}
