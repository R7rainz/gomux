// Package config holds gomux's file locations and, as config keys arrive,
// its settings (CLAUDE.md §6.9).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// SocketPath returns where the server listens. ownDir reports whether gomux
// owns the socket's directory, and so must create it or check it is private.
// GOMUX_SOCKET is used exactly as given, with ownDir false.
func SocketPath() (path string, ownDir bool, err error) {
	if p := os.Getenv("GOMUX_SOCKET"); p != "" {
		return p, false, checkSocketLen(p)
	}
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("gomux-%d", os.Getuid())) // TempDir is $TMPDIR or /tmp
	if rt := xdg("XDG_RUNTIME_DIR"); rt != "" {
		dir = filepath.Join(rt, "gomux")
	}
	path = filepath.Join(dir, "default.sock")
	return path, true, checkSocketLen(path)
}

// StateDir is where the server keeps its log and, later, other state.
func StateDir() (string, error) {
	if d := xdg("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "gomux"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "gomux"), nil
}

// xdg returns an XDG base-directory variable, ignoring relative paths as the
// XDG spec requires.
func xdg(name string) string {
	if v := os.Getenv(name); filepath.IsAbs(v) {
		return v
	}
	return ""
}

// checkSocketLen rejects paths that don't fit in sockaddr_un.sun_path, which
// also holds a terminating zero byte (104 bytes on macOS, 108 on Linux).
func checkSocketLen(path string) error {
	var sa syscall.RawSockaddrUnix
	if limit := len(sa.Path) - 1; len(path) > limit {
		return fmt.Errorf("socket path %s is %d bytes, over this system's limit of %d; set GOMUX_SOCKET to a shorter path", path, len(path), limit)
	}
	return nil
}
