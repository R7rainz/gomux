package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSocketPath(t *testing.T) {
	uidDir := fmt.Sprintf("gomux-%d", os.Getuid())
	cases := []struct {
		name                string
		gomux, runtime, tmp string
		want                string
		ownDir              bool
	}{
		{"GOMUX_SOCKET wins", "/tmp/dev.sock", "/run/user/501", "/var/tmp", "/tmp/dev.sock", false},
		{"XDG_RUNTIME_DIR", "", "/run/user/501", "/var/tmp", "/run/user/501/gomux/default.sock", true},
		{"relative XDG_RUNTIME_DIR ignored", "", "run/user", "/var/tmp", "/var/tmp/" + uidDir + "/default.sock", true},
		{"TMPDIR", "", "", "/var/tmp", "/var/tmp/" + uidDir + "/default.sock", true},
		{"/tmp fallback", "", "", "", "/tmp/" + uidDir + "/default.sock", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("GOMUX_SOCKET", c.gomux)
			t.Setenv("XDG_RUNTIME_DIR", c.runtime)
			t.Setenv("TMPDIR", c.tmp)
			got, ownDir, err := SocketPath()
			if err != nil || got != c.want || ownDir != c.ownDir {
				t.Fatalf("got %q, ownDir=%v, err=%v; want %q, ownDir=%v", got, ownDir, err, c.want, c.ownDir)
			}
		})
	}
}

func TestSocketPathTooLong(t *testing.T) {
	t.Setenv("GOMUX_SOCKET", "/tmp/"+strings.Repeat("x", 200)+".sock")
	if _, _, err := SocketPath(); err == nil || !strings.Contains(err.Error(), "GOMUX_SOCKET") {
		t.Fatalf("got %v, want a too-long error mentioning GOMUX_SOCKET", err)
	}
}

func TestStateDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ xdg, want string }{
		{"/x/state", "/x/state/gomux"},
		{"relative", filepath.Join(home, ".local", "state", "gomux")},
		{"", filepath.Join(home, ".local", "state", "gomux")},
	}
	for _, c := range cases {
		t.Setenv("XDG_STATE_HOME", c.xdg)
		if got, err := StateDir(); err != nil || got != c.want {
			t.Errorf("XDG_STATE_HOME=%q: got %q, %v; want %q", c.xdg, got, err, c.want)
		}
	}
}
