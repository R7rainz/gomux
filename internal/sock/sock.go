// Package sock is gomux's Unix socket transport: listening in a private
// directory, and checking with the kernel that the process on the other end
// belongs to the same user (CLAUDE.md §6.1, §13).
package sock

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

var (
	// ErrNoServer means nothing is listening at the socket path.
	ErrNoServer = errors.New("no server running")
	// ErrRunning means a live server already listens at the socket path.
	ErrRunning = errors.New("a gomux server is already running")
)

// Listen creates the server socket at path, mode 0600. With ownDir, the
// socket's directory is created (0700) or must already be a private
// directory of this user. A stale socket left by a dead server is removed.
func Listen(path string, ownDir bool) (*net.UnixListener, error) {
	if ownDir {
		if err := ensurePrivateDir(filepath.Dir(path), os.Getuid()); err != nil {
			return nil, err
		}
	}
	if err := clearStale(path); err != nil {
		return nil, err
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	// Closing ln removes the socket file (Go unlinks sockets it created).
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// Dial connects to the server at path and checks it runs as this user, so a
// client never talks to an impostor.
func Dial(path string) (*net.UnixConn, error) {
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			return nil, ErrNoServer
		}
		return nil, err
	}
	uid, _, err := Peer(c)
	if err != nil {
		c.Close()
		return nil, err
	}
	if uid != uint32(os.Getuid()) {
		c.Close()
		return nil, fmt.Errorf("the process listening on %s belongs to uid %d, not you; refusing to talk to it", path, uid)
	}
	return c, nil
}

// Peer returns the user and process IDs of the process on the other end of
// c, as reported by the kernel.
func Peer(c *net.UnixConn) (uid uint32, pid int, err error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	var perr error
	if err := raw.Control(func(fd uintptr) { uid, pid, perr = peer(int(fd)) }); err != nil {
		return 0, 0, err
	}
	return uid, pid, perr
}

// ensurePrivateDir creates dir with mode 0700, or checks an existing dir is a
// real directory (not a symlink) owned by uid with no group or other access.
// It refuses rather than repairs: an unsafe directory may already hold a
// planted socket.
func ensurePrivateDir(dir string, uid int) error {
	fi, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return err
		}
		fi, err = os.Lstat(dir)
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory (it may be a symlink); remove it so gomux can create its own", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || st.Uid != uint32(uid) {
		return fmt.Errorf("%s is owned by another user; remove it or set GOMUX_SOCKET", dir)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("%s has permissions %#o, but must be private to you; run: chmod 700 %s", dir, perm, dir)
	}
	return nil
}

// clearStale makes path free to bind: nothing there is fine, a live server is
// ErrRunning, a socket nobody answers on is removed, anything else is refused.
func clearStale(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&fs.ModeSocket == 0 {
		return fmt.Errorf("%s exists and is not a socket; refusing to remove it", path)
	}
	c, err := net.DialTimeout("unix", path, time.Second)
	if err == nil {
		c.Close()
		return fmt.Errorf("%w on %s", ErrRunning, path)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("checking %s: %w", path, err)
	}
	return os.Remove(path)
}
