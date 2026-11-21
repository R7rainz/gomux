package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestSocketDirIsShortPrivateAndRemoved(t *testing.T) {
	var dir string
	t.Run("use", func(t *testing.T) {
		dir = SocketDir(t)
		var sa syscall.RawSockaddrUnix
		if p := filepath.Join(dir, "default.sock"); len(p) >= len(sa.Path) {
			t.Fatalf("socket path %q is %d bytes, too long for this system", p, len(p))
		}
		if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("dir %v, mode %v: want a private 0700 directory", err, fi.Mode().Perm())
		}
	})
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("dir not removed after the test: %v", err)
	}
}

func TestEventuallyWaitsForCondition(t *testing.T) {
	calls := 0
	Eventually(t, time.Second, "the third call", func() bool { calls++; return calls == 3 })
	if calls != 3 {
		t.Fatalf("cond called %d times, want 3", calls)
	}
}

func TestBufferConcurrentWriteAndRead(t *testing.T) {
	var b Buffer
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 100 {
			_, _ = b.Write([]byte("x"))
		}
	})
	for range 100 {
		_ = b.String() // the race detector checks this against the writer
	}
	wg.Wait()
	if got := b.String(); got != strings.Repeat("x", 100) {
		t.Fatalf("got %d bytes, want 100", len(got))
	}
}

// Test-only code must never be built into the gomux binary.
func TestNotLinkedIntoGomux(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/R7rainz/gomux/cmd/gomux").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "internal/testutil") {
		t.Fatal("cmd/gomux depends on internal/testutil; only _test.go files may import it")
	}
}
